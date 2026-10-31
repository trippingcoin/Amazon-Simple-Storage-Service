//go:build integration

package main

import (
	"A3S/internal/storage"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerQuotaAndListing(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "data"), storage.Options{MaxObjectBytes: 1024, MaxConcurrentUploads: 2, MaxStorageBytes: 4, MaxObjects: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	}()
	server := httptest.NewServer(testAPI(t, s))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	call := func(method, path, body string, want int) string {
		t.Helper()
		r, e := http.NewRequest(method, server.URL+path, io.NopCloser(strings.NewReader(body)))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+testToken)
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || response.StatusCode != want {
			t.Fatalf("%s %s: %d %v %s", method, path, response.StatusCode, e, data)
		}
		return string(data)
	}
	call("PUT", "/abc", "", 200)
	call("PUT", "/abc/one", "data", 200)
	call("PUT", "/abc/one", "grown", 409)
	call("PUT", "/abc/two", "", 409)
	if body := call("GET", "/abc/one", "", 200); body != "data" {
		t.Fatal(body)
	}
	if body := call("GET", "/abc?limit=1", "", 200); !strings.Contains(body, "<Key>one</Key>") {
		t.Fatal(body)
	}
	if body := call("GET", "/-/usage", "", 200); !strings.Contains(body, "<StoredBytes>4</StoredBytes>") {
		t.Fatal(body)
	}
	if body := call("HEAD", "/-/usage", "", 200); body != "" {
		t.Fatal(body)
	}
	call("DELETE", "/abc/one", "", 204)
	call("PUT", "/abc/two", "free", 200)
}
