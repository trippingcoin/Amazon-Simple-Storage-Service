package backup

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"A3S/internal/storage"
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, e := storage.Open(dir, limits)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	for _, bucket := range []string{"empty", "files"} {
		if e = s.CreateBucket(ctx, objectstore.BucketID(bucket)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.PutObject(ctx, objectstore.ObjectID{Bucket: "files", Key: "данные.txt"}, strings.NewReader("legacy content")); e != nil {
		t.Fatal(e)
	}
	names, e := s.OpenNamespaces([]string{access.Namespace("alice"), access.Namespace("revoked")}, limits)
	if e != nil {
		t.Fatal(e)
	}
	for _, ns := range names {
		if e = ns.CreateBucket(ctx, "shared"); e != nil {
			t.Fatal(e)
		}
		if e = ns.PutObject(ctx, objectstore.ObjectID{Bucket: "shared", Key: "zero"}, strings.NewReader("")); e != nil {
			t.Fatal(e)
		}
		if e = ns.Close(); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	return dir, filepath.Join(t.TempDir(), "backup")
}
func TestCreateVerifyRestore(t *testing.T) {
	source, bundle := fixture(t)
	ctx := context.Background()
	m, e := Create(ctx, source, bundle, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if m.Namespaces != 3 || m.Buckets != 4 || m.Objects != 3 || m.ObjectBytes != 14 {
		t.Fatalf("unexpected inventory: %+v", m)
	}
	// Independent copy: a subsequent source edit does not affect verification.
	if e = os.WriteFile(filepath.Join(source, "files", "данные.txt"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, bundle, t.TempDir()); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, e = Restore(ctx, bundle, restored); e != nil {
		t.Fatal(e)
	}
	content, e := os.ReadFile(filepath.Join(restored, "files", "данные.txt"))
	if e != nil || string(content) != "legacy content" {
		t.Fatalf("restore: %q %v", content, e)
	}
	if _, e = Restore(ctx, bundle, restored); e == nil {
		t.Fatal("existing destination overwritten")
	}
	if _, e = Create(ctx, source, bundle, t.TempDir()); e == nil {
		t.Fatal("existing backup overwritten")
	}
	for _, path := range []string{restored, filepath.Join(restored, "files"), filepath.Join(restored, "files", "данные.txt"), bundle, filepath.Join(bundle, "data.tar"), filepath.Join(bundle, "manifest.json")} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("unsafe permissions", e)
		}
	}
}
func TestRejectRunningSourceAndNestedDestination(t *testing.T) {
	source, bundle := fixture(t)
	s, e := storage.Open(source, limits)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = Create(context.Background(), source, bundle, ""); e == nil {
		t.Fatal("live storage backed up")
	}
	if _, e = os.Stat(bundle); !os.IsNotExist(e) {
		t.Fatal("failed backup left destination")
	}
	if _, e = Create(context.Background(), source, filepath.Join(source, "backup"), ""); e == nil {
		t.Fatal("nested backup accepted")
	}
}
func TestCorruptionAndCancellation(t *testing.T) {
	for _, kind := range []string{"archive", "truncated", "inventory", "missing", "version"} {
		t.Run(kind, func(t *testing.T) {
			source, bundle := fixture(t)
			ctx := context.Background()
			if _, e := Create(ctx, source, bundle, ""); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "archive":
				f, e := os.OpenFile(filepath.Join(bundle, "data.tar"), os.O_RDWR, 0)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.WriteAt([]byte("damage"), 512); e != nil {
					t.Fatal(e)
				}
				f.Close()
			case "truncated":
				if e := os.Truncate(filepath.Join(bundle, "data.tar"), 1024); e != nil {
					t.Fatal(e)
				}
			case "missing":
				if e := os.Remove(filepath.Join(bundle, "manifest.json")); e != nil {
					t.Fatal(e)
				}
			default:
				path := filepath.Join(bundle, "manifest.json")
				data, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				var m Manifest
				if e = json.Unmarshal(data, &m); e != nil {
					t.Fatal(e)
				}
				if kind == "inventory" {
					m.InventorySHA256 = strings.Repeat("0", 64)
				} else {
					m.Version = 99
				}
				data, _ = json.Marshal(m)
				if e = os.WriteFile(path, data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			work := t.TempDir()
			if _, e := Verify(ctx, bundle, work); e == nil {
				t.Fatal("bad backup verified")
			}
			entries, e := os.ReadDir(work)
			if e != nil || len(entries) != 0 {
				t.Fatal("verification scratch leaked", e)
			}
			dest := filepath.Join(t.TempDir(), "restore")
			if _, e := Restore(ctx, bundle, dest); e == nil {
				t.Fatal("bad restore succeeded")
			}
			if _, e := os.Stat(dest); !os.IsNotExist(e) {
				t.Fatal("partial restore left behind")
			}
		})
	}
	source, bundle := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Create(ctx, source, bundle, ""); e == nil {
		t.Fatal("cancellation ignored")
	}
	if _, e := os.Stat(bundle); !os.IsNotExist(e) {
		t.Fatal("cancelled backup left behind")
	}
}
func TestUnsafeArchiveEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
		link string
	}{{"../escape", tar.TypeReg, ""}, {"/absolute", tar.TypeReg, ""}, {"link", tar.TypeSymlink, "../escape"}, {"hard", tar.TypeLink, "other"}, {"fifo", tar.TypeFifo, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if e := tw.WriteHeader(&tar.Header{Name: tc.name, Typeflag: tc.kind, Linkname: tc.link, Mode: 0600}); e != nil {
				t.Fatal(e)
			}
			tw.Close()
			bundle := t.TempDir()
			sum := sha256.Sum256(buf.Bytes())
			m := Manifest{Version: 1, CreatedAt: time.Now().UTC(), ArchiveSHA256: hex.EncodeToString(sum[:]), InventorySHA256: strings.Repeat("0", 64), ArchiveBytes: int64(buf.Len()), Namespaces: 1}
			data, _ := json.Marshal(m)
			if e := os.WriteFile(filepath.Join(bundle, "manifest.json"), data, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(bundle, "data.tar"), buf.Bytes(), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := Verify(context.Background(), bundle, t.TempDir()); e == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestStreamingArchive(t *testing.T) {
	source, bundle := fixture(t)
	s, e := storage.Open(source, limits)
	if e != nil {
		t.Fatal(e)
	}
	const size = 8 << 20
	if e = s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "files", Key: "large"}, io.LimitReader(zeroReader{}, size)); e != nil {
		t.Fatal(e)
	}
	s.Close()
	m, e := Create(context.Background(), source, bundle, "")
	if e != nil {
		t.Fatal(e)
	}
	if m.ObjectBytes != size+14 {
		t.Fatal("large file not included")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestUnsafeScratchAndDuplicateEntries(t *testing.T) {
	source, bundle := fixture(t)
	if _, e := Create(context.Background(), source, bundle, source); e == nil {
		t.Fatal("scratch inside live storage allowed")
	}
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	for i := 0; i < 2; i++ {
		if e := tw.WriteHeader(&tar.Header{Name: "duplicate", Typeflag: tar.TypeReg, Mode: 0600}); e != nil {
			t.Fatal(e)
		}
	}
	tw.Close()
	if e := extract(context.Background(), bytes.NewReader(data.Bytes()), t.TempDir(), int64(data.Len())); e == nil {
		t.Fatal("duplicate entry accepted")
	}
}
