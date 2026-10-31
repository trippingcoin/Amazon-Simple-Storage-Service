package main

import (
	"A3S/internal/access"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const carolToken = "carol-test-credential-at-least-32-bytes"

func TestSharedBucketPermissions(t *testing.T) {
	users := []access.User{identity("alice", aliceToken, "user"), identity("bob", bobToken, "user"), identity("carol", carolToken, "user")}
	users[1].Grants = []access.Grant{{Owner: "alice", Bucket: "shared", Permission: "read"}}
	users[2].Grants = []access.Grant{{Owner: "alice", Bucket: "shared", Permission: "write"}}
	dir := filepath.Join(t.TempDir(), "data")
	h, close := multiAPI(t, dir, users)
	for _, bucket := range []string{"shared", "private"} {
		requireStatus(t, scoped(h, "PUT", "/"+bucket, "", aliceToken), 200)
		requireStatus(t, scoped(h, "PUT", "/"+bucket+"/file", "owner-data", aliceToken), 200)
	}
	requireStatus(t, scoped(h, "PUT", "/shared", "", bobToken), 200)
	requireStatus(t, scoped(h, "PUT", "/shared/file", "bob-data", bobToken), 200)
	// Freeze the caller's registry; mutating it cannot escalate a running handler.
	users[1].Grants[0].Permission = "write"
	for _, token := range []string{bobToken, carolToken} {
		for _, path := range []string{"/shared", "/shared/file", "/shared/file?metadata=1"} {
			for _, method := range []string{"GET", "HEAD"} {
				requireStatus(t, scoped(h, method, path, "", token, "alice"), 200)
			}
		}
		w := scoped(h, "GET", "/", "", token, "alice")
		requireStatus(t, w, 200)
		if !strings.Contains(w.Body.String(), "shared") || strings.Contains(w.Body.String(), "private") {
			t.Fatal("shared listing leaked or omitted a bucket")
		}
		for _, path := range []string{"/private", "/private/file", "/private/file?metadata=1", "/absent/file", "/-/usage", "/-/metrics"} {
			for _, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
				requireStatus(t, scoped(h, method, path, "", token, "alice"), expectedSharedStatus(path, method))
			}
		}
		requireStatus(t, scoped(h, "PUT", "/shared", "", token, "alice"), 403)
		requireStatus(t, scoped(h, "DELETE", "/shared", "", token, "alice"), 403)
		requireStatus(t, scoped(h, "GET", "/shared/file", "", token, "alice", "alice"), 403)
		requireStatus(t, scoped(h, "GET", "/shared/file", "", token, "unknown"), 403)
		requireStatus(t, scoped(h, "GET", "/shared/missing", "", token, "alice"), 404)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		requireStatus(t, scoped(h, method, "/shared/file", "unauthorized", bobToken, "alice"), 403)
	}
	// Range and conditional requests remain available within a read grant.
	r := httptest.NewRequest("GET", "/shared/file", nil)
	r.Header.Set("Authorization", "Bearer "+bobToken)
	r.Header.Set("X-Storage-User", "alice")
	r.Header.Set("Range", "bytes=0-4")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireStatus(t, w, http.StatusPartialContent)
	if w.Body.String() != "owner" {
		t.Fatal("wrong ranged data")
	}
	requireStatus(t, scoped(h, "PUT", "/shared/file", "updated", carolToken, "alice"), 200)
	if w := scoped(h, "GET", "/shared/file", "", aliceToken); w.Body.String() != "updated" {
		t.Fatal("shared write not visible to owner")
	}
	if w := scoped(h, "GET", "/shared/file", "", bobToken); w.Body.String() != "bob-data" {
		t.Fatal("own namespace was changed")
	}
	requireStatus(t, scoped(h, "DELETE", "/shared/file", "", carolToken, "alice"), 204)
	requireStatus(t, scoped(h, "GET", "/shared/file", "", aliceToken), 404)
	requireStatus(t, scoped(h, "PUT", "/shared/file", "survives", carolToken, "alice"), 200)
	close()
	// The registry is authoritative after restart, and grants survive without
	// granting ownership or moving the shared data.
	users[1].Grants[0].Permission = "read"
	h, close = multiAPI(t, dir, users)
	w = scoped(h, "GET", "/shared/file", "", bobToken, "alice")
	requireStatus(t, w, 200)
	if w.Body.String() != "survives" {
		t.Fatal("shared data lost at restart")
	}
	close()
	users[1].Grants = nil
	h, _ = multiAPI(t, dir, users)
	requireStatus(t, scoped(h, "GET", "/shared/file", "", bobToken, "alice"), 403)
	requireStatus(t, scoped(h, "GET", "/shared/file", "", carolToken, "alice"), 200)
	requireStatus(t, scoped(h, "GET", "/shared/file", "", aliceToken), 200)
}
func expectedSharedStatus(path, method string) int {
	// usage supports only GET/HEAD; unsupported methods remain 405 without data.
	if path == "/-/usage" && method != "GET" && method != "HEAD" {
		return 405
	}
	return 403
}
