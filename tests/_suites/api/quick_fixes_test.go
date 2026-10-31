package main

import (
	"A3S/internal/config"
	"A3S/internal/httpapi"
	"A3S/internal/storage"
	"encoding/xml"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomDirectoryEndToEndRegression(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	t.Chdir(first)
	c, err := config.Parse([]string{"--dir", "./parent/../custom хранилище"}, testToken, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(first, "custom хранилище")
	if c.DataDir != want {
		t.Fatal(c.DataDir)
	}
	// Working-directory changes after parsing must not redirect filesystem IO.
	t.Chdir(second)
	s, err := storage.Open(c.DataDir, c.Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := testAPI(t, s)
	requireStatus(t, request(h, "PUT", "/abc", "", testToken), 200)
	upload := httptest.NewRequest("PUT", "/abc/report.txt", strings.NewReader("tiny"))
	upload.Header.Set("Authorization", "Bearer "+testToken)
	upload.Header.Set("Content-Type", "image/png") // Untrusted declaration must not override sniffing.
	uploaded := httptest.NewRecorder()
	h.ServeHTTP(uploaded, upload)
	requireStatus(t, uploaded, 200)
	raw, err := os.ReadFile(filepath.Join(want, "abc", "report.txt"))
	if err != nil || string(raw) != "tiny" {
		t.Fatal("wrong physical directory", err)
	}
	for _, cwd := range []string{first, second} {
		if _, err = os.Stat(filepath.Join(cwd, "data")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("default data directory was used", err)
		}
	}
	meta := request(h, "GET", "/abc/report.txt?metadata=1", "", testToken)
	requireStatus(t, meta, 200)
	var dto httpapi.ObjectResponse
	if err = xml.Unmarshal(meta.Body.Bytes(), &dto); err != nil || dto.ObjectKey != "data/abc/report.txt" || dto.ContentType != "text/plain; charset=utf-8" {
		t.Fatal("wire metadata changed", dto, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := storage.Open(c.DataDir, c.Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	h = testAPI(t, next)
	download := request(h, "GET", "/abc/report.txt", "", testToken)
	requireStatus(t, download, 200)
	if download.Body.String() != "tiny" || download.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal("bad restored download")
	}
	requireStatus(t, request(h, "DELETE", "/abc/report.txt", "", testToken), 204)
	requireStatus(t, request(h, "DELETE", "/abc", "", testToken), 200)
	if _, err = os.Stat(filepath.Join(want, "abc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delete used wrong directory", err)
	}
}
