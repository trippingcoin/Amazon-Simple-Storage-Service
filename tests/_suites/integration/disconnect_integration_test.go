//go:build integration

package main

import (
	"A3S/internal/objectstore"
	"A3S/internal/storage"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerTLSDownloadDisconnect(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "data"), storage.Options{MaxObjectBytes: 16 << 20, MaxConcurrentUploads: 1, MaxConcurrentDownloads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	if err = s.CreateBucket(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if err = s.PutObject(ctx, id, strings.NewReader(strings.Repeat("x", 8<<20))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(testAPI(t, s))
	defer server.Close()
	client := server.Client()
	client.Timeout = 10 * time.Second
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL + "/abc/file")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("TLS endpoint bypassed auth")
	}
	r, _ := http.NewRequest("GET", server.URL+"/abc/file", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		t.Fatal(response.StatusCode)
	}
	one := make([]byte, 1)
	_, err = io.ReadFull(response.Body, one)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Closing mid-transfer must eventually release the sole descriptor slot.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, e := s.OpenObject(ctx, id)
		if e == nil {
			d.Content.Close()
			return
		}
		if !errors.Is(e, objectstore.ErrBusy) {
			t.Fatal(e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("download slot leaked after client disconnect")
}
