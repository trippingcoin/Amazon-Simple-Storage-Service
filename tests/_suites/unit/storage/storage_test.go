package storage

import (
	"A3S/internal/objectstore"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var testOptions = Options{MaxObjectBytes: 1 << 20, MaxConcurrentUploads: 32}

func openTest(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, dir
}
func bucket(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.CreateBucket(context.Background(), objectstore.BucketID(name)); err != nil {
		t.Fatal(err)
	}
}
func put(t *testing.T, s *Store, b, k, v string) {
	t.Helper()
	if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: objectstore.BucketID(b), Key: objectstore.ObjectKey(k)}, strings.NewReader(v)); err != nil {
		t.Fatal(err)
	}
}
func disk(t *testing.T, dir, b, k, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, b, k))
	if err != nil || string(got) != want {
		t.Fatalf("disk=%q err=%v want=%q", got, err, want)
	}
}
func cleanTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, privateDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "lock" && e.Name() != "metadata" && e.Name() != "transactions" {
			t.Errorf("temporary file leaked: %s", e.Name())
		}
	}
}

type brokenReader struct{ done bool }

func (r *brokenReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read(p []byte) (int, error) { r.cancel(); return copy(p, "partial"), nil }

func TestFailedOverwritePreservesObject(t *testing.T) {
	for _, kind := range []string{"interrupted", "too-large", "canceled", "publish-error"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := openTest(t, Options{MaxObjectBytes: 16, MaxConcurrentUploads: 1})
			bucket(t, s, "abc")
			put(t, s, "abc", "file", "original")
			before, _ := s.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var r io.Reader
			var want error
			switch kind {
			case "interrupted":
				r = &brokenReader{}
				want = io.ErrUnexpectedEOF
			case "too-large":
				r = strings.NewReader(strings.Repeat("x", 17))
				want = objectstore.ErrTooLarge
			case "canceled":
				r = cancelReader{cancel}
				want = context.Canceled
			case "publish-error":
				r = strings.NewReader("new")
				want = syscall.ENOSPC
				s.journalRename = func(string, string) error { return syscall.ENOSPC }
			}
			err := s.PutObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "file"}, r)
			if !errors.Is(err, want) {
				t.Fatalf("err=%v want=%v", err, want)
			}
			disk(t, dir, "abc", "file", "original")
			after, e := s.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"})
			if e != nil || after != before {
				t.Fatalf("metadata changed: %v %v", after, e)
			}
			cleanTemps(t, dir)
			s.rename = s.root.Rename
			s.journalRename = s.root.Rename
			put(t, s, "abc", "file", "success") // slots and active-upload counters were released
		})
	}
}
func TestRestartRecoversAndDeletionStaysDeleted(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "one", "hello")
	put(t, s, "abc", "two", "")
	before, _ := s.ListBuckets(context.Background())
	if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err = s2.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "one"}); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	o, err := s2.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "two"})
	if err != nil || o.Size != 0 || o.ContentType != "application/octet-stream" {
		t.Fatalf("%+v %v", o, err)
	}
	after, _ := s2.ListBuckets(context.Background())
	if len(after) != 1 || !after[0].CreationTime.Equal(before[0].CreationTime) || after[0].Empty {
		t.Fatalf("bucket=%+v", after)
	}
}
func TestLegacyRecoveryIgnoresStaleCSV(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dir, "abc"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"abc/real": "hello", "abc/ObjectMetaData.csv": "ObjectKey,Size,ContentType,LastModifiedTime\ndata/abc/deleted,999,text/plain,broken\n", "BucketMetaData.csv": "Name,CreationTime,LastModifiedTime,Status\nabc,2020-01-01T00:00:00Z,2020-01-01T00:00:00Z,Marked for deletion\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o, err := s.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "real"})
	if err != nil || o.Size != 5 || o.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("%+v %v", o, err)
	}
	if _, err = s.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "deleted"}); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	list, _ := s.ListBuckets(context.Background())
	if list[0].CreationTime.Year() != 2020 || list[0].Empty {
		t.Fatalf("%+v", list)
	}
	if err = s.DeleteBucket(context.Background(), "abc"); !errors.Is(err, objectstore.ErrNotEmpty) {
		t.Fatal(err)
	}
	if err = s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "real"}); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBucket(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "abc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
func TestDeleteNonemptyBucketPreservesRemainingObject(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "one", "one")
	put(t, s, "abc", "two", "two")
	if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBucket(context.Background(), "abc"); !errors.Is(err, objectstore.ErrNotEmpty) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "two", "two")
	if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "two"}); err != nil {
		t.Fatal(err)
	}
	// The final filesystem check also protects files not present in the index.
	if err := os.WriteFile(filepath.Join(dir, "abc", "unexpected"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBucket(context.Background(), "abc"); !errors.Is(err, objectstore.ErrNotEmpty) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "unexpected", "safe")
}

type gateReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gateReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}
func TestActiveUploadProtectsBucketAndLimitsConcurrency(t *testing.T) {
	s, dir := openTest(t, Options{MaxObjectBytes: 16, MaxConcurrentUploads: 1})
	bucket(t, s, "abc")
	r := &gateReader{started: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		result <- s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "one"}, r)
	}()
	<-r.started
	// Always release the goroutine, including on a failed assertion.
	defer func() {
		close(r.release)
		if err := <-result; err != nil {
			t.Error(err)
		}
	}()
	if err := s.DeleteBucket(context.Background(), "abc"); !errors.Is(err, objectstore.ErrNotEmpty) {
		t.Error(err)
	}
	if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "two"}, strings.NewReader("two")); !errors.Is(err, objectstore.ErrBusy) {
		t.Error(err)
	}
	if err := s.Close(); !errors.Is(err, objectstore.ErrBusy) {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "abc")); err != nil {
		t.Error(err)
	}
}
func TestConcurrentOperations(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("file-%02d", i)
			if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}, strings.NewReader(key)); err != nil {
				t.Error(err)
				return
			}
			if _, err := s.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}); err != nil {
				t.Error(err)
			}
			if _, err := s.ListBuckets(context.Background()); err != nil {
				t.Error(err)
			}
			if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}); err != nil {
				t.Error(err)
			}
			name := fmt.Sprintf("bucket-%02d", i)
			if err := s.CreateBucket(context.Background(), objectstore.BucketID(name)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	cleanTemps(t, dir)
	if err := s.DeleteBucket(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentOverwritePublishesWholeObject(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	a := strings.Repeat("a", 64<<10)
	b := strings.Repeat("b", 64<<10)
	var wg sync.WaitGroup
	for _, v := range []string{a, b} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "same"}, strings.NewReader(v)); err != nil {
				t.Error(err)
			}
		}(v)
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(dir, "abc", "same"))
	if err != nil || (string(data) != a && string(data) != b) {
		t.Fatal("mixed or missing object", err)
	}
	cleanTemps(t, dir)
}
func TestProcessLock(t *testing.T) {
	s, dir := openTest(t, testOptions)
	s2, err := Open(dir, testOptions)
	if err == nil {
		s2.Close()
		t.Fatal("second store acquired same directory")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err = Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}
func TestInvalidKeysAndSymlinks(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", ".", "..", "../../sentinel", "a/b", "a\\b", "ObjectMetaData.csv", "OBJECTMETADATA.CSV", ".bucket.json", "bad\x00key", "bad\nkey", strings.Repeat("a", 256)} {
		if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}, strings.NewReader("bad")); !errors.Is(err, objectstore.ErrInvalid) {
			t.Errorf("%q: %v", key, err)
		}
		if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: objectstore.ObjectKey(key)}); !errors.Is(err, objectstore.ErrInvalid) {
			t.Errorf("delete %q: %v", key, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "abc", "link")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "link"}, strings.NewReader("bad")); !errors.Is(err, objectstore.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "link"}); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "safe" {
		t.Fatal("outside file changed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := Open(dir, testOptions); err == nil {
		next.Close()
		t.Fatal("startup accepted symlink")
	}
}
func TestCaseInsensitiveAliasCannotOverwrite(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "File", "original")
	_, err := os.Stat(filepath.Join(dir, "abc", "file"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("case-sensitive filesystem")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("replacement")); !errors.Is(err, objectstore.ErrInvalid) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "File", "original")
}
func TestInterruptedStagingCleanup(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "committed")
	s.Close()
	stage := filepath.Join(dir, privateDir, "upload-"+strings.Repeat("a", 32))
	if err := os.WriteFile(stage, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	stagedBucket := filepath.Join(dir, privateDir, "bucket-"+strings.Repeat("b", 32))
	if err := os.Mkdir(stagedBucket, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedBucket, bucketInfo), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	cleanTemps(t, dir)
	disk(t, dir, "abc", "file", "committed")
}
func TestFailedBucketPublishDoesNotCreateBucket(t *testing.T) {
	s, dir := openTest(t, testOptions)
	s.rename = func(string, string) error { return os.ErrPermission }
	if err := s.CreateBucket(context.Background(), "abc"); !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	list, err := s.ListBuckets(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
	cleanTemps(t, dir)
}

func TestRejectsSharedDirectoryPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, testOptions)
	if err == nil {
		s.Close()
		t.Fatal("accepted shared storage directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
func TestExistingObjectReplacedBySymlinkCannotBeDeleted(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "abc", "file")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}); !errors.Is(err, objectstore.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("bad")); !errors.Is(err, objectstore.ErrInvalid) {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "safe" {
		t.Fatalf("outside changed: %q %v", got, err)
	}
}

func TestFilesystemPermissionFailureRequiresRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission checks")
	}
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "original")
	bucketDir := filepath.Join(dir, "abc")
	if err := os.Chmod(bucketDir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(bucketDir, 0700)
	if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("new")); !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	if err := s.DeleteObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}); !errors.Is(err, objectstore.ErrRecoveryRequired) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "file", "original")
	if err := os.Chmod(bucketDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	disk(t, dir, "abc", "file", "new")
	cleanTemps(t, dir)
	put(t, next, "abc", "file", "recovered")
}

func TestPhaseOneMetadataCompatibility(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dir, "abc"), 0700); err != nil {
		t.Fatal(err)
	}
	oldJSON := `{"XMLName":{"Space":"","Local":""},"Name":"abc","CreationTime":"2026-01-02T03:04:05Z","LastModified":"2026-01-02T03:04:05Z","Status":""}`
	metaPath := filepath.Join(dir, "abc", bucketInfo)
	if err := os.WriteFile(metaPath, []byte(oldJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc", "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.ListBuckets(context.Background())
	if err != nil || len(list) != 1 || list[0].CreationTime.Format("2006-01-02T15:04:05Z") != "2026-01-02T03:04:05Z" {
		t.Fatalf("metadata changed: %+v %v", list, err)
	}
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	o, err := s.GetObject(context.Background(), id)
	if err != nil || o.ID != id || o.Size != 8 {
		t.Fatalf("bad domain identity: %+v %v", o, err)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil || string(after) != oldJSON {
		t.Fatal("opening storage rewrote legacy metadata", err)
	}
	disk(t, dir, "abc", "file", "original")
	// A newly written record still exposes fields consumed by the Phase 1 reader.
	bucket(t, s, "xyz")
	raw, err := os.ReadFile(filepath.Join(dir, "xyz", bucketInfo))
	if err != nil {
		t.Fatal(err)
	}
	var phaseOne struct {
		Name         string
		CreationTime time.Time
	}
	if err = json.Unmarshal(raw, &phaseOne); err != nil || phaseOne.Name != "xyz" || phaseOne.CreationTime.IsZero() {
		t.Fatalf("incompatible disk format: %s %v", raw, err)
	}
}
func TestCanceledReads(t *testing.T) {
	s, _ := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "data")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ListBuckets(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.GetObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "file"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestSameKeyInDifferentBuckets(t *testing.T) {
	s, _ := openTest(t, testOptions)
	bucket(t, s, "abc")
	bucket(t, s, "xyz")
	put(t, s, "abc", "same", "one")
	put(t, s, "xyz", "same", "second")
	first := objectstore.ObjectID{Bucket: "abc", Key: "same"}
	second := objectstore.ObjectID{Bucket: "xyz", Key: "same"}
	if err := s.DeleteObject(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetObject(context.Background(), second)
	if err != nil || o.ID != second || o.Size != 6 {
		t.Fatalf("identity collision: %+v %v", o, err)
	}
}
