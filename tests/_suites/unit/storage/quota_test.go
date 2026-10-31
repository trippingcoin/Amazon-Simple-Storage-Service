package storage

import (
	"A3S/internal/objectstore"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func assertUsage(t *testing.T, s *Store, bytes, objects int64) {
	t.Helper()
	u, err := s.Usage(context.Background())
	if err != nil || u.StoredBytes != bytes || u.Objects != objects {
		t.Fatalf("usage=%+v err=%v want bytes=%d objects=%d", u, err, bytes, objects)
	}
}
func TestQuotaOverwriteDeleteAndFailure(t *testing.T) {
	opts := testOptions
	opts.MaxStorageBytes = 6
	opts.MaxObjects = 2
	s, dir := openTest(t, opts)
	bucket(t, s, "abc")
	bucket(t, s, "def")
	ctx := context.Background()
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	put(t, s, "abc", "file", "1234")
	put(t, s, "def", "other", "12")
	assertUsage(t, s, 6, 2)
	if err := s.PutObject(ctx, id, strings.NewReader("12345")); !errors.Is(err, objectstore.ErrQuotaExceeded) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "file", "1234")
	assertUsage(t, s, 6, 2)
	cleanTemps(t, dir)
	if err := s.PutObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "empty"}, strings.NewReader("")); !errors.Is(err, objectstore.ErrQuotaExceeded) {
		t.Fatal("empty files must count", err)
	}
	put(t, s, "abc", "file", "1")
	assertUsage(t, s, 3, 2)
	originalRename := s.journalRename
	s.journalRename = func(string, string) error { return io.ErrUnexpectedEOF }
	if err := s.PutObject(ctx, id, strings.NewReader("123")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	s.journalRename = originalRename
	assertUsage(t, s, 3, 2)
	disk(t, dir, "abc", "file", "1")
	if err := s.DeleteObject(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, s, 2, 1)
	if err := s.DeleteObject(ctx, id); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	assertUsage(t, s, 2, 1)
	put(t, s, "abc", "empty", "")
	assertUsage(t, s, 2, 2)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	assertUsage(t, next, 2, 2)
}
func TestLoweredQuotaAllowsRecoveryAndShrink(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "one", "123456")
	put(t, s, "abc", "two", "1234")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opts := testOptions
	opts.MaxStorageBytes = 3
	opts.MaxObjects = 1
	next, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	assertUsage(t, next, 10, 2)
	ctx := context.Background()
	id := objectstore.ObjectID{Bucket: "abc", Key: "one"}
	if err = next.PutObject(ctx, id, strings.NewReader("1234567")); !errors.Is(err, objectstore.ErrQuotaExceeded) {
		t.Fatal(err)
	}
	put(t, next, "abc", "one", "abcde")
	assertUsage(t, next, 9, 2)
	put(t, next, "abc", "one", "ABCDE")
	assertUsage(t, next, 9, 2)
	if err = next.DeleteObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "two"}); err != nil {
		t.Fatal(err)
	}
	put(t, next, "abc", "one", "ab")
	assertUsage(t, next, 2, 1)
}

type quotaBarrier struct {
	io.Reader
	ready   chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *quotaBarrier) Read(p []byte) (int, error) {
	r.once.Do(func() { r.ready <- struct{}{}; <-r.release })
	return r.Reader.Read(p)
}
func TestConcurrentQuotaPublication(t *testing.T) {
	for _, tc := range []struct {
		name           string
		bytes, objects int64
		sameKey        bool
		wantSuccess    int
	}{
		{"bytes", 8, 0, false, 2}, {"objects", 0, 2, false, 2}, {"same-key", 4, 1, true, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions
			opts.MaxStorageBytes = tc.bytes
			opts.MaxObjects = tc.objects
			s, dir := openTest(t, opts)
			bucket(t, s, "abc")
			ready := make(chan struct{}, 8)
			release := make(chan struct{})
			results := make(chan error, 8)
			for i := 0; i < 8; i++ {
				go func(i int) {
					key := fmt.Sprintf("file-%d", i)
					if tc.sameKey {
						key = "shared"
					}
					results <- s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}, &quotaBarrier{Reader: strings.NewReader("data"), ready: ready, release: release})
				}(i)
			}
			for i := 0; i < 8; i++ {
				<-ready
			}
			close(release)
			successes := 0
			for i := 0; i < 8; i++ {
				err := <-results
				if err == nil {
					successes++
				} else if !errors.Is(err, objectstore.ErrQuotaExceeded) {
					t.Error(err)
				}
			}
			if successes != tc.wantSuccess {
				t.Fatalf("successes=%d", successes)
			}
			count := int64(successes)
			if tc.sameKey {
				count = 1
			}
			assertUsage(t, s, count*4, count)
			cleanTemps(t, dir)
		})
	}
}
