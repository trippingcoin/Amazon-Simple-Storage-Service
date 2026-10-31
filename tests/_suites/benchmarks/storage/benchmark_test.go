package storage

import (
	"A3S/internal/objectstore"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func BenchmarkPutObject(b *testing.B) {
	for _, size := range []int64{1 << 20, 16 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "data"), Options{MaxObjectBytes: 32 << 20, MaxConcurrentUploads: 2})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			if err = s.CreateBucket(ctx, "abc"); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err = s.PutObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "file"}, io.LimitReader(zeroReader{}, size)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDownload(b *testing.B) {
	for _, size := range []int64{1 << 20, 16 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "data"), Options{MaxObjectBytes: 32 << 20, MaxConcurrentUploads: 2})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
			if err = s.CreateBucket(ctx, "abc"); err != nil {
				b.Fatal(err)
			}
			if err = s.PutObject(ctx, id, io.LimitReader(zeroReader{}, size)); err != nil {
				b.Fatal(err)
			}
			buffer := make([]byte, 32<<10)
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, e := s.OpenObject(ctx, id)
				if e != nil {
					b.Fatal(e)
				}
				_, e = io.CopyBuffer(discardWriter{}, d.Content, buffer)
				d.Content.Close()
				if e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// Avoid io.Discard.ReaderFrom's pooled buffer to measure the explicit buffer path.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Synthetic index isolates listing cost from filesystem setup and upload fsync.
func BenchmarkListObjects(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		b.Run(fmt.Sprintf("%dObjects", count), func(b *testing.B) {
			objects := make(map[string]objectstore.Object, count)
			for i := 0; i < count; i++ {
				key := fmt.Sprintf("file-%06d", i)
				objects[key] = objectstore.Object{ID: objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}, Size: 100}
			}
			s := &Store{buckets: map[string]*bucketState{"abc": {objects: objects}}}
			ctx := context.Background()
			opts := objectstore.ListOptions{Limit: 100}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, err := s.ListObjects(ctx, "abc", opts)
				if err != nil || len(page.Objects) != 100 {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMetadataRecovery(b *testing.B) {
	for _, cold := range []bool{false, true} {
		name := "warm"
		if cold {
			name = "rebuild"
		}
		b.Run(name, func(b *testing.B) {
			dir := filepath.Join(b.TempDir(), "data")
			opts := Options{MaxObjectBytes: 32 << 20, MaxConcurrentUploads: 2}
			s, err := Open(dir, opts)
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
			if err = s.CreateBucket(ctx, "abc"); err != nil {
				b.Fatal(err)
			}
			if err = s.PutObject(ctx, id, io.LimitReader(zeroReader{}, 16<<20)); err != nil {
				b.Fatal(err)
			}
			if err = s.Close(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if cold {
					b.StopTimer()
					if err = os.Remove(filepath.Join(dir, objectMetadataPath(id))); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				reopened, e := Open(dir, opts)
				if e != nil {
					b.Fatal(e)
				}
				meta, e := reopened.GetObject(ctx, id)
				if e != nil || meta.SHA256 == "" {
					b.Fatal("metadata not recovered", e)
				}
				if e = reopened.Close(); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
