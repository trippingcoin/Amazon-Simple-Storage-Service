package main

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadHTTP(t *testing.T) {
	h, _, _ := testHandler(t, 1024)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/abc/file", "0123456789", testToken), 200)
	sum := sha256.Sum256([]byte("0123456789"))
	etag := fmt.Sprintf(`"%x"`, sum)
	for _, tc := range []struct {
		name, method, path, header, value, body string
		status                                  int
	}{
		{"full", "GET", "/abc/file", "", "", "0123456789", 200},
		{"head", "HEAD", "/abc/file", "", "", "", 200},
		{"range", "GET", "/abc/file", "Range", "bytes=2-5", "2345", 206},
		{"suffix", "GET", "/abc/file", "Range", "bytes=-3", "789", 206},
		{"open range", "GET", "/abc/file", "Range", "bytes=8-", "89", 206},
		{"not modified", "GET", "/abc/file", "If-None-Match", etag, "", 304},
		{"precondition", "GET", "/abc/file", "If-Match", `"different"`, "", 412},
		{"metadata head", "HEAD", "/abc/file?metadata=1", "", "", "", 200},
		{"missing head", "HEAD", "/abc/missing", "", "", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+testToken)
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			requireStatus(t, w, tc.status)
			if w.Body.String() != tc.body {
				t.Fatalf("body=%q", w.Body.String())
			}
			if tc.status == 200 && !strings.Contains(tc.path, "metadata") {
				if w.Header().Get("ETag") != etag || w.Header().Get("Content-Length") != "10" || w.Header().Get("Content-Disposition") != "attachment; filename=file" {
					t.Fatal(w.Header())
				}
			}
		})
	}
	metadata := request(h, "GET", "/abc/file?metadata=1", "", testToken)
	if !strings.Contains(metadata.Body.String(), "<Object>") {
		t.Fatal("metadata contract changed", metadata.Body.String())
	}
	for _, tc := range []struct {
		rangeHeader, ifRange string
		status               int
	}{{"bytes=20-", "", 416}, {"bytes=0-1", etag, 206}, {"bytes=0-1", `"stale"`, 200}, {"bytes=0-1,8-9", "", 206}} {
		r := httptest.NewRequest("GET", "/abc/file", nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("Range", tc.rangeHeader)
		r.Header.Set("If-Range", tc.ifRange)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		requireStatus(t, w, tc.status)
	}
	r := httptest.NewRequest("GET", "/abc/file", nil)
	r.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, 401)
}
