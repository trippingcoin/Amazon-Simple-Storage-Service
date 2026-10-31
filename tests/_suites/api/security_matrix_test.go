package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizationMatrix(t *testing.T) {
	h, _, _ := testHandler(t, 1024)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/abc/file", "private", testToken), 200)
	for _, path := range []string{"/", "/abc", "/abc/file", "/abc/file?metadata=1", "/missing/file"} {
		for _, method := range []string{"GET", "HEAD", "PUT", "DELETE", "OPTIONS"} {
			for _, auth := range [][]string{nil, {"Bearer wrong"}, {"Basic " + testToken}, {"Bearer " + testToken, "Bearer " + testToken}} {
				r := httptest.NewRequest(method, path, strings.NewReader("overwrite"))
				for _, v := range auth {
					r.Header.Add("Authorization", v)
				}
				r.Header.Set("If-None-Match", "*")
				r.Header.Set("Range", "bytes=0-1")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 401 || strings.Contains(w.Body.String(), testToken) || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("%s %s: %d %q", method, path, w.Code, w.Body.String())
				}
				if method == "HEAD" && w.Body.Len() != 0 {
					t.Fatal("HEAD leaked body")
				}
			}
		}
	}
	w := request(h, "GET", "/abc/file", "", testToken)
	requireStatus(t, w, 200)
	if w.Body.String() != "private" {
		t.Fatal("unauthorized mutation")
	}
}
