package httpapi

import (
	"A3S/internal/objectstore"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const observerToken = "observation-test-token-12345678901234567890"

type observedService struct {
	Operations
	put func(context.Context, io.Reader) error
	get func(context.Context) (objectstore.Object, error)
}

func (s observedService) PutObject(ctx context.Context, _, _ string, r io.Reader) error {
	return s.put(ctx, r)
}
func (s observedService) GetObject(ctx context.Context, _, _ string) (objectstore.Object, error) {
	return s.get(ctx)
}
func newObservedAPI(t *testing.T, s Operations, logs io.Writer) http.Handler {
	t.Helper()
	h, e := New(s, Options{Token: observerToken, RequestTimeout: time.Minute, MaxObjectBytes: 1024, Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func observeRequest(h http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		r.Header.Set("Authorization", "Bearer "+observerToken)
	}
	r.Header.Set("X-Request-ID", "untrusted-secret-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestRequestTelemetryPrivacyAndCounters(t *testing.T) {
	var logs bytes.Buffer
	var contextID string
	s := observedService{put: func(ctx context.Context, r io.Reader) error {
		contextID = RequestID(ctx)
		_, e := io.Copy(io.Discard, r)
		return e
	}, get: func(context.Context) (objectstore.Object, error) {
		return objectstore.Object{}, fmt.Errorf("credential-secret-path")
	}}
	h := newObservedAPI(t, s, &logs)
	upload := observeRequest(h, "PUT", "/abc/private-object?secret=query-value", "private-body", true)
	if upload.Code != 200 || len(upload.Header().Get("X-Request-ID")) != 32 || contextID != upload.Header().Get("X-Request-ID") {
		t.Fatal("request ID not propagated", upload.Header())
	}
	failed := observeRequest(h, "GET", "/abc/private-object?metadata=1", "", true)
	if failed.Code != 500 {
		t.Fatal(failed.Code)
	}
	denied := observeRequest(h, "GET", "/-/metrics", "", false)
	if denied.Code != 401 || strings.Contains(denied.Body.String(), "storage_http_requests_total") {
		t.Fatal("metrics auth bypass")
	}
	metrics := observeRequest(h, "GET", "/-/metrics", "", true)
	if metrics.Code != 200 || metrics.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatal(metrics.Header())
	}
	for _, want := range []string{
		`storage_http_requests_total{operation="upload",status="200",outcome="success"} 1`,
		`storage_operation_errors_total{operation="metadata",status="500",outcome="error"} 1`,
		`storage_operation_errors_total{operation="unauthorized",status="401",outcome="error"} 1`,
		"storage_upload_bytes_total 12\n", "storage_http_inflight_requests 0\n",
		`storage_operation_duration_seconds_bucket{operation="upload",le="+Inf"} 1`,
		`storage_operation_duration_seconds_count{operation="upload"} 1`,
	} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	again := observeRequest(h, "GET", "/-/metrics", "", true)
	if again.Body.String() != metrics.Body.String() {
		t.Fatal("scrapes contaminated measurements")
	}
	head := observeRequest(h, "HEAD", "/-/metrics", "", true)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(metrics.Body.Len()) {
		t.Fatal("metrics HEAD")
	}
	for _, secret := range []string{observerToken, "private-object", "query-value", "private-body", "credential-secret-path", "untrusted-secret-id"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(metrics.Body.String(), secret) {
			t.Errorf("telemetry leaked %q", secret)
		}
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("events: %s", logs.String())
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["request_id"] == "" || event["duration_seconds"] == nil {
			t.Fatal(event)
		}
	}
}
func TestAbortedDownloadTelemetry(t *testing.T) {
	var logs bytes.Buffer
	f := &failingContent{Reader: strings.NewReader("data")}
	h := newObservedAPI(t, downloadOnly{content: f}, &logs)
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("abort lost")
			}
		}()
		observeRequest(h, "GET", "/abc/file", "", true)
	}()
	m := observeRequest(h, "GET", "/-/metrics", "", true).Body.String()
	if !strings.Contains(m, `storage_operation_errors_total{operation="download",status="200",outcome="aborted"} 1`) || !strings.Contains(m, "storage_http_inflight_requests 0\n") || !f.closed {
		t.Fatal(m)
	}
}
func TestPanicIsSanitized(t *testing.T) {
	var logs bytes.Buffer
	h := newObservedAPI(t, observedService{get: func(context.Context) (objectstore.Object, error) { panic("panic-secret-value") }}, &logs)
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("panic not sanitized")
			}
		}()
		observeRequest(h, "GET", "/abc/file?metadata=1", "", true)
	}()
	if strings.Contains(logs.String(), "panic-secret-value") || !strings.Contains(logs.String(), `"outcome":"panic"`) {
		t.Fatal(logs.String())
	}
}
func TestInflightAndConcurrentScrapes(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := newObservedAPI(t, observedService{put: func(context.Context, io.Reader) error { close(entered); <-release; return nil }}, io.Discard)
	done := make(chan struct{})
	go func() { defer close(done); observeRequest(h, "PUT", "/abc/file", "", true) }()
	<-entered
	if !strings.Contains(observeRequest(h, "GET", "/-/metrics", "", true).Body.String(), "storage_http_inflight_requests 1\n") {
		close(release)
		<-done
		t.Fatal("missing active request")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); observeRequest(h, "GET", "/-/metrics", "", true) }()
	}
	close(release)
	<-done
	wg.Wait()
	if !strings.Contains(observeRequest(h, "GET", "/-/metrics", "", true).Body.String(), "storage_http_inflight_requests 0\n") {
		t.Fatal("active requests leaked")
	}
}

type successfulDownload struct{ Operations }
type closeableString struct{ *strings.Reader }

func (closeableString) Close() error { return nil }
func (successfulDownload) OpenObject(context.Context, string, string) (objectstore.Download, error) {
	return objectstore.Download{Object: objectstore.Object{Size: 10, ContentType: "text/plain", SHA256: strings.Repeat("a", 64)}, Content: closeableString{strings.NewReader("0123456789")}}, nil
}
func TestDownloadByteAccounting(t *testing.T) {
	h := newObservedAPI(t, successfulDownload{}, io.Discard)
	full := observeRequest(h, "GET", "/abc/file", "", true)
	if full.Body.String() != "0123456789" {
		t.Fatal(full.Body.String())
	}
	r := httptest.NewRequest("GET", "/abc/file", nil)
	r.Header.Set("Authorization", "Bearer "+observerToken)
	r.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "234" {
		t.Fatal(w.Code, w.Body.String())
	}
	observeRequest(h, "HEAD", "/abc/file", "", true)
	r = httptest.NewRequest("GET", "/abc/file", nil)
	r.Header.Set("Authorization", "Bearer "+observerToken)
	r.Header.Set("If-None-Match", full.Header().Get("ETag"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	metrics := observeRequest(h, "GET", "/-/metrics", "", true).Body.String()
	if !strings.Contains(metrics, "storage_download_bytes_total 13\n") {
		t.Fatal(metrics)
	}
}

type failedResponse struct{ header http.Header }

func (w failedResponse) Header() http.Header     { return w.header }
func (failedResponse) WriteHeader(int)           {}
func (failedResponse) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestWriteFailureAndController(t *testing.T) {
	m := newTelemetry()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := m.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("payload")) }), logger)
	h.ServeHTTP(failedResponse{make(http.Header)}, httptest.NewRequest("GET", "/abc/file", nil))
	w := httptest.NewRecorder()
	m.serve(w, httptest.NewRequest("GET", "/-/metrics", nil))
	if !strings.Contains(w.Body.String(), `storage_operation_errors_total{operation="download",status="200",outcome="write_error"} 1`) {
		t.Fatal(w.Body.String())
	}
	// ResponseController must still reach optional transport functionality.
	inner := httptest.NewRecorder()
	outer := &observedWriter{ResponseWriter: inner}
	if err := http.NewResponseController(outer).Flush(); err != nil || !inner.Flushed {
		t.Fatal("controller blocked", err)
	}
}
