package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the documented offline tar procedure, not a mock backup adapter.
func TestOfflineBackupRestore(t *testing.T) {
	s, dir := openTest(t, testOptions)
	ctx := context.Background()
	bucket(t, s, "abc")
	bucket(t, s, "empty")
	put(t, s, "abc", "данные.txt", "backup contents\x00\xff")
	put(t, s, "abc", "zero", "")
	put(t, s, "abc", "deleted", "gone")
	if err := s.DeleteObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "deleted"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListBuckets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type snapshot struct {
		meta objectstore.Object
		hash [32]byte
	}
	objects := map[string]snapshot{}
	for _, key := range []string{"данные.txt", "zero"} {
		d, e := s.OpenObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)})
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(d.Content)
		d.Content.Close()
		if e != nil {
			t.Fatal(e)
		}
		objects[key] = snapshot{d.Object, sha256.Sum256(data)}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar")
	run := func(args ...string) {
		t.Helper()
		if out, e := exec.Command("tar", args...).CombinedOutput(); e != nil {
			t.Fatalf("tar: %v %s", e, out)
		}
	}
	run("-cpf", archive, "-C", dir, ".")
	// Mutate the source after the backup; restored state must be independent.
	if err = os.WriteFile(filepath.Join(dir, "abc", "данные.txt"), []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err = os.Mkdir(restoredDir, 0700); err != nil {
		t.Fatal(err)
	}
	run("-xpf", archive, "-C", restoredDir)
	restored, err := Open(restoredDir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after, err := restored.ListBuckets(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatalf("buckets: %v %v", after, err)
	}
	for i := range before {
		if before[i].ID != after[i].ID || !before[i].CreationTime.Equal(after[i].CreationTime) || before[i].Empty != after[i].Empty {
			t.Fatalf("bucket changed: %+v %+v", before[i], after[i])
		}
	}
	for key, want := range objects {
		d, e := restored.OpenObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)})
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(d.Content)
		d.Content.Close()
		if e != nil {
			t.Fatal(e)
		}
		if sha256.Sum256(data) != want.hash || d.Object.SHA256 != want.meta.SHA256 || d.Object.Size != want.meta.Size || d.Object.ContentType != want.meta.ContentType {
			t.Fatalf("restored object differs: %s", key)
		}
		// Portable tar may round mtime to seconds; ETags remain byte-exact.
		if d.Object.LastModified.Unix() != want.meta.LastModified.Unix() {
			t.Fatal("mtime differs")
		}
		info, e := os.Stat(filepath.Join(restoredDir, "abc", key))
		if e != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("unsafe restored permissions", e)
		}
	}
	if _, err = restored.GetObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "deleted"}); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal("deleted object resurrected", err)
	}
	assertUsage(t, restored, int64(len("backup contents\x00\xff")), 2)
	put(t, restored, "abc", "after-restore", "writable")
	cleanTemps(t, restoredDir)
}
