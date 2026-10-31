package storage

import (
	"A3S/internal/objectstore"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableMetadataReusedAndRebuilt(t *testing.T) {
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	for _, damage := range []string{"none", "missing", "truncated", "digest", "stale-version", "oversized", "metadata-directory-missing"} {
		t.Run(damage, func(t *testing.T) {
			s, dir := openTest(t, testOptions)
			bucket(t, s, "abc")
			put(t, s, "abc", "file", "original contents")
			want, err := s.GetObject(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, objectMetadataPath(id))
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if before.Mode().Perm() != 0600 {
				t.Fatal("metadata permissions", before.Mode())
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing":
				err = os.Remove(path)
			case "metadata-directory-missing":
				err = os.RemoveAll(filepath.Join(dir, objectMetadataDir))
			case "truncated":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "digest":
				err = os.WriteFile(path, bytes.Replace(original, []byte(want.SHA256), []byte(strings.Repeat("0", 64)), 1), 0600)
			case "oversized":
				err = os.WriteFile(path, bytes.Repeat([]byte("x"), 20000), 0600)
			case "stale-version":
				// Same size and original mtime still cannot validate an old inode/ctime.
				blob := filepath.Join(dir, "abc", "file")
				info, e := os.Stat(blob)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(blob, []byte("replaced contents"), 0600)
				if err == nil {
					err = os.Chtimes(blob, info.ModTime(), info.ModTime())
				}
				sum := sha256.Sum256([]byte("replaced contents"))
				want.SHA256 = fmt.Sprintf("%x", sum)
			}
			if err != nil {
				t.Fatal(err)
			}
			next, err := Open(dir, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			got, err := next.GetObject(context.Background(), id)
			if err != nil || got != want {
				t.Fatalf("metadata=%+v want=%+v err=%v", got, want, err)
			}
			fi, err := next.root.Stat("abc/file")
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := next.readObjectMetadata(got, fi)
			if err != nil || persisted != got {
				t.Fatal("record not persisted", err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "none" && (!os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime())) {
				t.Fatal("valid record was unnecessarily rebuilt")
			}
			cleanTemps(t, dir)
		})
	}
}
func TestMetadataPublicationFailureRecoversCommittedBlob(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	failure := errors.New("injected metadata publish failure")
	s.metadataRename = func(string, string) error { return failure }
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	if err := s.PutObject(context.Background(), id, strings.NewReader("new contents")); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "file", "new contents")
	if _, e := s.Usage(context.Background()); !errors.Is(e, objectstore.ErrRecoveryRequired) {
		t.Fatal(e)
	}
	cleanTemps(t, dir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	got, err := next.GetObject(context.Background(), id)
	sum := sha256.Sum256([]byte("new contents"))
	if err != nil || got.SHA256 != fmt.Sprintf("%x", sum) {
		t.Fatal(got, err)
	}
}
func TestMetadataUnsafeAndFutureFormatsFailClosed(t *testing.T) {
	for _, kind := range []string{"symlink-directory", "symlink-record", "unknown-file", "future-version"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := openTest(t, testOptions)
			bucket(t, s, "abc")
			put(t, s, "abc", "file", "data")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
			path := filepath.Join(dir, objectMetadataPath(id))
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink-directory":
				if err := os.RemoveAll(filepath.Join(dir, objectMetadataDir)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, objectMetadataDir)); err != nil {
					t.Fatal(err)
				}
			case "symlink-record":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "unknown-file":
				if err := os.WriteFile(filepath.Join(dir, objectMetadataDir, "unexpected"), []byte("evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			case "future-version":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var envelope metadataEnvelope
				if err = json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				envelope.Version = 2
				raw, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			next, err := Open(dir, testOptions)
			if err == nil {
				next.Close()
				t.Fatal("accepted unsafe/future metadata")
			}
			raw, err := os.ReadFile(outside)
			if err != nil || string(raw) != "untouched" {
				t.Fatal("outside data changed")
			}
			disk(t, dir, "abc", "file", "data")
		})
	}
}
