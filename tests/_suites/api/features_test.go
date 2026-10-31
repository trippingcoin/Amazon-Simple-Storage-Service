package main

import (
	"A3S/internal/storage"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeaturesHTTP(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "data"), storage.Options{MaxObjectBytes: 1024, MaxConcurrentUploads: 2, MaxStorageBytes: 5, MaxObjects: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := testAPI(t, s)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/abc/a", "abc", testToken), 200)
	requireStatus(t, request(h, "PUT", "/abc/b", "de", testToken), 200)
	quota := request(h, "PUT", "/abc/a", "abcd", testToken)
	requireStatus(t, quota, 409)
	if !strings.Contains(quota.Body.String(), "storage quota exceeded") {
		t.Fatal(quota.Body.String())
	}
	requireStatus(t, request(h, "PUT", "/abc/empty", "", testToken), 409)
	if old := request(h, "GET", "/abc/a", "", testToken); old.Body.String() != "abc" {
		t.Fatal("overwrite damaged old data")
	}
	list := request(h, "GET", "/abc?prefix=a&limit=1", "", testToken)
	requireStatus(t, list, 200)
	var result struct {
		XMLName     xml.Name
		IsTruncated bool
		NextAfter   string
		Contents    []struct {
			Key  string
			Size int64
		}
	}
	if err = xml.Unmarshal(list.Body.Bytes(), &result); err != nil || result.XMLName.Local != "ListObjectsResult" || len(result.Contents) != 1 || result.Contents[0].Key != "a" || result.IsTruncated {
		t.Fatal(result, err)
	}
	list = request(h, "GET", "/abc?limit=1", "", testToken)
	requireStatus(t, list, 200)
	result.Contents = nil
	if err = xml.Unmarshal(list.Body.Bytes(), &result); err != nil || !result.IsTruncated || result.NextAfter != "a" {
		t.Fatal(result, err)
	}
	next := request(h, "GET", "/abc/?after=a&limit=1", "", testToken)
	requireStatus(t, next, 200)
	if !strings.Contains(next.Body.String(), "<Key>b</Key>") {
		t.Fatal(next.Body.String())
	}
	for _, path := range []string{"/abc?limit=0", "/abc?limit=1001", "/abc?limit=1&limit=2", "/abc?prefix=../", "/abc?after=..", "/abc?unknown=1", "/abc?prefix=%FF", "/abc?prefix=%00", "/abc?prefix=%zz"} {
		requireStatus(t, request(h, "GET", path, "", testToken), 400)
	}
	requireStatus(t, request(h, "GET", "/missing", "", testToken), 404)
	usage := request(h, "GET", "/-/usage", "", testToken)
	requireStatus(t, usage, 200)
	var u struct {
		XMLName                                           xml.Name
		StoredBytes, Objects, MaxStorageBytes, MaxObjects int64
		Buckets                                           int
	}
	if err = xml.Unmarshal(usage.Body.Bytes(), &u); err != nil || u.StoredBytes != 5 || u.Objects != 2 || u.Buckets != 1 || u.MaxStorageBytes != 5 || u.MaxObjects != 2 {
		t.Fatal(u, err)
	}
	for _, path := range []string{"/abc?limit=1", "/-/usage"} {
		requireStatus(t, request(h, "GET", path, "", ""), 401)
		head := request(h, "HEAD", path, "", testToken)
		requireStatus(t, head, 200)
		if head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
			t.Fatal("HEAD", head.Header())
		}
	}
	requireStatus(t, request(h, "PUT", "/-/usage", "", testToken), 405)
	metrics := request(h, "GET", "/-/metrics", "", testToken)
	for _, label := range []string{`operation="list_objects"`, `operation="usage"`, `status="409"`} {
		if !strings.Contains(metrics.Body.String(), label) {
			t.Fatal("missing telemetry", label)
		}
	}
}
