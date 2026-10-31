package main

import (
	"A3S/internal/httpapi"
	"A3S/internal/service"
	"A3S/internal/storage"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "test-only-token-not-a-real-secret-123456789"

func testHandler(t *testing.T, max int64) (http.Handler, *storage.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := storage.Open(dir, storage.Options{MaxObjectBytes: max, MaxConcurrentUploads: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return testAPI(t, s), s, dir
}
func request(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body)
	}
}
func TestAuthenticationBeforeMutation(t *testing.T) {
	h, s, _ := testHandler(t, 1024)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		for _, token := range []string{"", "wrong"} {
			w := request(h, method, "/abc", "payload", token)
			requireStatus(t, w, 401)
			if w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing authentication challenge")
			}
		}
	}
	list, err := s.ListBuckets(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("unauthorized request mutated store: %v %v", list, err)
	}
	r := httptest.NewRequest("PUT", "/abc", nil)
	r.Header.Add("Authorization", "Bearer "+testToken)
	r.Header.Add("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 401)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
}
func TestMetadataAPIAndRestart(t *testing.T) {
	h, s, dir := testHandler(t, 1024)
	w := request(h, "GET", "/", "", testToken)
	requireStatus(t, w, 200)
	var list struct {
		XMLName xml.Name
		Buckets []httpapi.BucketResponse `xml:"Bucket"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &list); err != nil || list.XMLName.Local != "Buckets" || len(list.Buckets) != 0 {
		t.Fatalf("invalid list: %v %s", err, w.Body)
	}
	requireStatus(t, request(h, "PUT", "/abc/", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/abc/file/", "hello", testToken), 200)
	w = request(h, "GET", "/abc/file?metadata=1", "", testToken)
	requireStatus(t, w, 200)
	var obj httpapi.ObjectResponse
	if err := xml.Unmarshal(w.Body.Bytes(), &obj); err != nil || obj.Size != 5 || obj.ContentType != "text/plain; charset=utf-8" || obj.ObjectKey != "data/abc/file" {
		t.Fatalf("%+v %v", obj, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := storage.Open(dir, storage.Options{MaxObjectBytes: 1024, MaxConcurrentUploads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	h = testAPI(t, next)
	requireStatus(t, request(h, "GET", "/abc/file?metadata=1", "", testToken), 200)
	requireStatus(t, request(h, "DELETE", "/abc", "", testToken), 409)
	requireStatus(t, request(h, "DELETE", "/abc/file", "", testToken), 204)
	requireStatus(t, request(h, "DELETE", "/abc/file", "", testToken), 404)
	requireStatus(t, request(h, "DELETE", "/abc", "", testToken), 200)
	w = request(h, "GET", "/abc/file?metadata=1", "", testToken)
	requireStatus(t, w, 404)
	var xe httpapi.ErrorResponse
	if err = xml.Unmarshal(w.Body.Bytes(), &xe); err != nil || xe.XMLName.Local != "Error" || xe.Code != 404 {
		t.Fatalf("invalid error XML: %v %s", err, w.Body)
	}
}
func TestHTTPTraversalAndPathAliases(t *testing.T) {
	h, _, dir := testHandler(t, 1024)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
	outside := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-sentinel")
	if err := os.WriteFile(outside, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	for _, path := range []string{"/abc/..%2F..%2F" + filepath.Base(outside), "/..%2Fabc/file", "/abc/a%2Fb", "/abc/a%5Cb", "/abc/.bucket.json", "/abc/ObjectMetaData.csv", "/abc/name/ignored", "/abc/%00"} {
		for _, method := range []string{"PUT", "GET", "DELETE"} {
			w := request(h, method, path, "bad", testToken)
			requireStatus(t, w, 400)
		}
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "safe" {
		t.Fatalf("outside modified: %q %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "abc", "name")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nested key was truncated")
	}
}

type failingBody struct{ done bool }

func (b *failingBody) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		return copy(p, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}
func TestHTTPUploadLimitsAndInterruptedOverwrite(t *testing.T) {
	h, _, dir := testHandler(t, 8)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/abc/file", "original", testToken), 200)
	for _, chunked := range []bool{false, true} {
		r := httptest.NewRequest("PUT", "/abc/file", strings.NewReader("too-large"))
		if chunked {
			r.ContentLength = -1
		}
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		requireStatus(t, w, 413)
	}
	r := httptest.NewRequest("PUT", "/abc/file", &failingBody{})
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 408)
	data, err := os.ReadFile(filepath.Join(dir, "abc", "file"))
	if err != nil || string(data) != "original" {
		t.Fatalf("old object changed: %q %v", data, err)
	}
	requireStatus(t, request(h, "PUT", "/abc/empty", "", testToken), 200)
}
func TestCanceledRequestCannotCreateBucket(t *testing.T) {
	h, s, _ := testHandler(t, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("PUT", "/abc", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 408)
	list, _ := s.ListBuckets(context.Background())
	if len(list) != 0 {
		t.Fatal("canceled request created bucket")
	}
}
func TestInvalidTokenNeverAppearsInResponse(t *testing.T) {
	h, _, _ := testHandler(t, 1024)
	secret := "invalid-sensitive-client-value"
	w := request(h, "PUT", "/abc", "", secret)
	if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
		t.Fatal("token leaked")
	}
}

func testAPI(t *testing.T, s *storage.Store) http.Handler {
	t.Helper()
	h, err := httpapi.New(service.New(s), httpapi.Options{Token: testToken, RequestTimeout: time.Minute, MaxObjectBytes: s.MaxObjectBytes()})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
