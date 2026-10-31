package main

import (
	"A3S/internal/access"
	"A3S/internal/httpapi"
	"A3S/internal/objectstore"
	"A3S/internal/service"
	"A3S/internal/storage"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const aliceToken = "alice-test-credential-at-least-32-bytes"
const bobToken = "bob-test-credential-at-least-32-bytes"

func identity(id, token, role string) access.User {
	h := sha256.Sum256([]byte(token))
	return access.User{ID: id, Role: role, TokenSHA256: hex.EncodeToString(h[:])}
}
func multiAPI(t *testing.T, dir string, users []access.User) (http.Handler, func()) {
	t.Helper()
	limits := objectstore.Limits{MaxObjectBytes: 1 << 20, MaxConcurrentUploads: 4, MaxConcurrentDownloads: 4, MaxObjects: 10}
	root, err := storage.Open(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, u := range users {
		names = append(names, access.Namespace(u.ID))
	}
	stores, err := root.OpenNamespaces(names, limits)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	apis := map[string]httpapi.Operations{}
	for _, u := range users {
		apis[u.ID] = service.New(stores[access.Namespace(u.ID)])
	}
	h, err := httpapi.New(service.New(root), httpapi.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Token: testToken, Users: users, Tenants: apis, MaxObjectBytes: limits.MaxObjectBytes, RequestTimeout: time.Minute, MaxConcurrentUploads: 4, MaxConcurrentDownloads: 4})
	if err != nil {
		t.Fatal(err)
	}
	close := func() {
		for _, s := range stores {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(close)
	return h, close
}
func scoped(h http.Handler, method, path, body, token string, scope ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	for _, id := range scope {
		r.Header.Add("X-Storage-User", id)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestMultiuserIsolationRecoveryAndRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	users := []access.User{identity("alice", aliceToken, "user"), identity("bob", bobToken, "user")}
	h, close := multiAPI(t, dir, users)
	for _, tc := range []struct{ token, body string }{{testToken, "legacy"}, {aliceToken, "alice"}, {bobToken, "bob"}} {
		requireStatus(t, scoped(h, "PUT", "/same", "", tc.token), 200)
		requireStatus(t, scoped(h, "PUT", "/same/file", tc.body, tc.token), 200)
	}
	requireStatus(t, scoped(h, "PUT", "/private-alice", "", aliceToken), 200)
	requireStatus(t, scoped(h, "PUT", "/private-alice/secret", "private", aliceToken), 200)
	for _, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
		requireStatus(t, scoped(h, method, "/private-alice/secret", "bad", bobToken), 404)
		requireStatus(t, scoped(h, method, "/private-alice/secret?metadata=1", "bad", bobToken), 404)
		requireStatus(t, scoped(h, method, "/same/file", "bad", bobToken, "alice"), 403)
	}
	requireStatus(t, scoped(h, "GET", "/-/metrics", "", aliceToken), 403)
	requireStatus(t, scoped(h, "HEAD", "/-/metrics", "", aliceToken), 403)
	requireStatus(t, scoped(h, "GET", "/-/metrics", "", testToken), 200)
	requireStatus(t, scoped(h, "GET", "/same/file", "", testToken, "alice", "bob"), 400)
	requireStatus(t, scoped(h, "GET", "/same/file", "", testToken, "missing"), 400)
	if w := scoped(h, "GET", "/", "", bobToken); strings.Contains(w.Body.String(), "private-alice") {
		t.Fatal("bucket list leaked")
	}
	usageA := scoped(h, "GET", "/-/usage", "", aliceToken).Body.String()
	usageB := scoped(h, "GET", "/-/usage", "", bobToken).Body.String()
	if usageA == usageB {
		t.Fatal("usage not scoped")
	}
	check := func(api http.Handler, token, want string, scope ...string) {
		t.Helper()
		w := scoped(api, "GET", "/same/file", "", token, scope...)
		requireStatus(t, w, 200)
		if w.Body.String() != want {
			t.Fatalf("namespace contents: %q want %q", w.Body.String(), want)
		}
	}
	check(h, testToken, "legacy")
	check(h, aliceToken, "alice")
	check(h, bobToken, "bob")
	check(h, testToken, "alice", "alice")
	close()
	rotated := "rotated-alice-test-credential-at-least-32-bytes"
	users[0] = identity("alice", rotated, "user")
	users[1].Disabled = true
	h, close = multiAPI(t, dir, users)
	requireStatus(t, scoped(h, "GET", "/same/file", "", aliceToken), 401)
	requireStatus(t, scoped(h, "GET", "/same/file", "", bobToken), 401)
	check(h, rotated, "alice")
	check(h, testToken, "bob", "bob")
	close()
	archive := filepath.Join(t.TempDir(), "backup.tar")
	if out, err := exec.Command("tar", "-cpf", archive, "-C", dir, ".").CombinedOutput(); err != nil {
		t.Fatalf("backup: %v %s", err, out)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := os.Mkdir(restored, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-xpf", archive, "-C", restored).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	h, _ = multiAPI(t, restored, users)
	check(h, rotated, "alice")
	check(h, testToken, "bob", "bob")
	check(h, testToken, "legacy")
	requireStatus(t, scoped(h, "GET", "/same/file", "", bobToken), 401)
}
func TestMultiuserConcurrentSameNames(t *testing.T) {
	h, _ := multiAPI(t, filepath.Join(t.TempDir(), "data"), []access.User{identity("alice", aliceToken, "user"), identity("bob", bobToken, "user")})
	var wg sync.WaitGroup
	for _, token := range []string{aliceToken, bobToken} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			if w := scoped(h, "PUT", "/same", "", token); w.Code != 200 {
				t.Error(w.Code)
				return
			}
			for i := 0; i < 10; i++ {
				if w := scoped(h, "PUT", "/same/file", token, token); w.Code != 200 {
					t.Error(w.Code)
				}
				w := scoped(h, "GET", "/same/file", "", token)
				if w.Code != 200 || w.Body.String() != token {
					t.Error("cross-tenant content")
				}
			}
		}(token)
	}
	wg.Wait()
}

// Hold an upload body open: the second user's upload must share admission.
type signaledReader struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *signaledReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}
func TestMultiuserSharedAdmission(t *testing.T) {
	h, _ := multiAPI(t, filepath.Join(t.TempDir(), "data"), []access.User{identity("alice", aliceToken, "user"), identity("bob", bobToken, "user")})
	requireStatus(t, scoped(h, "PUT", "/same", "", aliceToken), 200)
	requireStatus(t, scoped(h, "PUT", "/same", "", bobToken), 200)
	var wg sync.WaitGroup
	readers := make([]*io.PipeWriter, 4)
	for i := range readers {
		r, w := io.Pipe()
		readers[i] = w
		started := make(chan struct{})
		body := &signaledReader{Reader: r, started: started}
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("PUT", "/same/held", body)
			req.Header.Set("Authorization", "Bearer "+aliceToken)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			for _, w := range readers {
				if w != nil {
					_ = w.Close()
				}
			}
			wg.Wait()
			t.Fatal("upload did not start")
		}
	}
	// A bucket PUT does not consume Bob's store upload slot.
	response := scoped(h, "PUT", "/probe", "", bobToken)
	for _, w := range readers {
		_ = w.Close()
	}
	wg.Wait()
	requireStatus(t, response, 503)
}
