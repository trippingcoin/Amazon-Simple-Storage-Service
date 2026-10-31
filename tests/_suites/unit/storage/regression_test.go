package storage

import (
	"A3S/internal/objectstore"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMIMEUploadAndRecoveryRegression(t *testing.T) {
	cases := []struct{ key, body, mime string }{
		{"empty.txt", "", "application/octet-stream"},
		{"short.jpg", "a", "text/plain; charset=utf-8"},
		{"unicode", "Привет мир", "text/plain; charset=utf-8"},
		{"image.txt", "\x89PNG\r\n\x1a\n", "image/png"},
		{"jpeg.bin", "\xff\xd8\xff\xe0", "image/jpeg"},
		{"document.bin", "%PDF-1.7\n", "application/pdf"},
		{"binary.txt", "hello\x00world", "application/octet-stream"},
		{"sniff-boundary", strings.Repeat("a", 512) + "\x00", "text/plain; charset=utf-8"},
		{"inside-boundary", strings.Repeat("a", 511) + "\x00", "application/octet-stream"},
	}
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	for _, tc := range cases {
		put(t, s, "abc", tc.key, tc.body)
	}
	check := func(store *Store) {
		t.Helper()
		for _, tc := range cases {
			id := objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(tc.key)}
			meta, err := store.GetObject(context.Background(), id)
			if err != nil || meta.ContentType != tc.mime || meta.Size != int64(len(tc.body)) {
				t.Fatalf("%s: metadata=%+v err=%v", tc.key, meta, err)
			}
			d, err := store.OpenObject(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(d.Content)
			d.Content.Close()
			if err != nil || string(data) != tc.body {
				t.Fatalf("MIME detection changed bytes of %s: %v", tc.key, err)
			}
		}
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	check(next)
}

type failedMIMEReader struct{ err error }

func (r failedMIMEReader) ReadAt(p []byte, _ int64) (int, error) { return copy(p, "partial"), r.err }
func TestMIMEReadErrorIsNotHidden(t *testing.T) {
	failure := errors.New("test read failure")
	if mime, err := detectContentType(failedMIMEReader{failure}); !errors.Is(err, failure) || mime != "" {
		t.Fatalf("mime=%q err=%v", mime, err)
	}
}
func TestNilUploadBodyPreservesObject(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "original")
	if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, nil); !errors.Is(err, objectstore.ErrInvalid) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "file", "original")
	assertUsage(t, s, 8, 1)
	cleanTemps(t, dir)
	put(t, s, "abc", "file", "next") // No upload slot or bucket activity leaked.
}

// Legacy CSVs may contain quoted commas and stale rows. Deleting any position
// must preserve every other object, and stale CSV must not resurrect deletions.
func TestLegacyCSVDeletePositionsRegression(t *testing.T) {
	keys := []string{"first", `middle,"quoted"`, "последний"}
	for _, deleted := range keys {
		t.Run(deleted, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "custom directory")
			if err := os.MkdirAll(filepath.Join(dir, "abc"), 0700); err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			csvWriter := csv.NewWriter(&text)
			if err := csvWriter.Write([]string{"ObjectKey", "Size", "ContentType", "LastModifiedTime"}); err != nil {
				t.Fatal(err)
			}
			for _, key := range keys {
				if err := os.WriteFile(filepath.Join(dir, "abc", key), []byte("content:"+key), 0600); err != nil {
					t.Fatal(err)
				}
				if err := csvWriter.Write([]string{"data/abc/" + key, "999", "incorrect/type", "stale"}); err != nil {
					t.Fatal(err)
				}
			}
			csvWriter.Flush()
			if err := csvWriter.Error(); err != nil {
				t.Fatal(err)
			}
			csvPath := filepath.Join(dir, "abc", "ObjectMetaData.csv")
			if err := os.WriteFile(csvPath, []byte(text.String()), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(deleted)}); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			next, err := Open(dir, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			for _, key := range keys {
				_, err = next.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)})
				if key == deleted {
					if !errors.Is(err, objectstore.ErrNotFound) {
						t.Fatal("deleted object resurrected", err)
					}
					continue
				}
				if err != nil {
					t.Fatal("remaining object lost", err)
				}
				disk(t, dir, "abc", key, "content:"+key)
			}
			raw, err := os.ReadFile(csvPath)
			if err != nil || string(raw) != text.String() {
				t.Fatal("legacy CSV was unexpectedly rewritten", err)
			}
			// Stale CSV rows do not keep a logically empty bucket alive.
			for _, key := range keys {
				if key != deleted {
					if err = next.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = next.DeleteBucket(context.Background(), "abc"); err != nil {
				t.Fatal(err)
			}
			if err = next.Close(); err != nil {
				t.Fatal(err)
			}
			final, err := Open(dir, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer final.Close()
			list, err := final.ListBuckets(context.Background())
			if err != nil || len(list) != 0 {
				t.Fatal("deleted bucket resurrected", list, err)
			}
		})
	}
}

func TestMIMEDetectionDoesNotMoveOffset(t *testing.T) {
	reader := strings.NewReader("hello")
	if _, err := reader.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	mime, err := detectContentType(reader)
	if err != nil || mime != "text/plain; charset=utf-8" {
		t.Fatal(mime, err)
	}
	position, err := reader.Seek(0, io.SeekCurrent)
	if err != nil || position != 3 {
		t.Fatal("MIME detection changed offset", position, err)
	}
}
