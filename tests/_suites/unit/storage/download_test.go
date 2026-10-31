package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadPinnedVersionAndCapacity(t *testing.T) {
	s, _ := openTest(t, Options{MaxObjectBytes: 1024, MaxConcurrentUploads: 2, MaxConcurrentDownloads: 1})
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	ctx := context.Background()
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	d, err := s.OpenObject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Content.Close()
	if _, err = s.OpenObject(ctx, id); !errors.Is(err, objectstore.ErrBusy) {
		t.Fatal(err)
	}
	if err = s.Close(); !errors.Is(err, objectstore.ErrBusy) {
		t.Fatal(err)
	}
	put(t, s, "abc", "file", "new")
	if err = s.DeleteObject(ctx, id); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(d.Content)
	if err != nil || string(body) != "old" {
		t.Fatalf("%q %v", body, err)
	}
	sum := sha256.Sum256(body)
	if d.Object.SHA256 != fmt.Sprintf("%x", sum) {
		t.Fatal("checksum mismatch")
	}
	d.Content.Close()
	d.Content.Close()
	if _, err = s.OpenObject(ctx, id); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	put(t, s, "abc", "file", "replacement")
	d, err = s.OpenObject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	d.Content.Close()
}
func TestDownloadRecoveryAndCancellation(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "data")
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	ctx := context.Background()
	before, err := s.GetObject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	d, err := recovered.OpenObject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Object.SHA256 != before.SHA256 {
		t.Fatal("ETag changed after restart")
	}
	d.Content.Close()
	canceled, cancel := context.WithCancel(ctx)
	d, err = recovered.OpenObject(canceled, id)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = d.Content.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	d.Content.Close()
	if _, err = recovered.OpenObject(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "abc", "file"), []byte("external modification"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = recovered.OpenObject(ctx, id); err == nil {
		t.Fatal("accepted stale metadata")
	}
	if len(recovered.downloads) != 0 {
		t.Fatal("download slot leaked")
	}
}

func TestConcurrentDownloadOverwrite(t *testing.T) {
	s, _ := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "initial")
	ctx := context.Background()
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if err := s.PutObject(ctx, id, strings.NewReader(fmt.Sprintf("version-%d", i))); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 20; i++ {
		d, err := s.OpenObject(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(d.Content)
		d.Content.Close()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if d.Object.SHA256 != fmt.Sprintf("%x", sum) || d.Object.Size != int64(len(body)) {
			t.Fatal("mixed versions")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
