package service

import (
	"A3S/internal/objectstore"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type repositorySpy struct {
	listOptions objectstore.ListOptions
	calls       int
	ctx         context.Context
	bucket      objectstore.BucketID
	id          objectstore.ObjectID
	body        io.Reader
	err         error
}

func (r *repositorySpy) record(ctx context.Context) { r.calls++; r.ctx = ctx }
func (r *repositorySpy) ListBuckets(ctx context.Context) ([]objectstore.Bucket, error) {
	r.record(ctx)
	return nil, r.err
}
func (r *repositorySpy) CreateBucket(ctx context.Context, id objectstore.BucketID) error {
	r.record(ctx)
	r.bucket = id
	return r.err
}
func (r *repositorySpy) DeleteBucket(ctx context.Context, id objectstore.BucketID) error {
	r.record(ctx)
	r.bucket = id
	return r.err
}
func (r *repositorySpy) OpenObject(ctx context.Context, id objectstore.ObjectID) (objectstore.Download, error) {
	r.record(ctx)
	r.id = id
	return objectstore.Download{}, r.err
}
func (r *repositorySpy) GetObject(ctx context.Context, id objectstore.ObjectID) (objectstore.Object, error) {
	r.record(ctx)
	r.id = id
	return objectstore.Object{ID: id}, r.err
}
func (r *repositorySpy) PutObject(ctx context.Context, id objectstore.ObjectID, body io.Reader) error {
	r.record(ctx)
	r.id = id
	r.body = body
	return r.err
}
func (r *repositorySpy) DeleteObject(ctx context.Context, id objectstore.ObjectID) error {
	r.record(ctx)
	r.id = id
	return r.err
}

func TestCanceledOperationsDoNotReachRepository(t *testing.T) {
	repo := &repositorySpy{}
	s := New(repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, listErr := s.ListBuckets(ctx)
	_, getErr := s.GetObject(ctx, "abc", "file")
	for _, err := range []error{listErr, getErr, s.CreateBucket(ctx, "abc"), s.DeleteBucket(ctx, "abc"), s.PutObject(ctx, "abc", "file", strings.NewReader("data")), s.DeleteObject(ctx, "abc", "file")} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected cancellation, got %v", err)
		}
	}
	if repo.calls != 0 {
		t.Fatal("canceled operation reached repository")
	}
}
func TestInvalidNamesDoNotReachRepository(t *testing.T) {
	repo := &repositorySpy{}
	s := New(repo)
	ctx := context.Background()
	_, getErr := s.GetObject(ctx, "abc", "../outside")
	for _, err := range []error{getErr, s.CreateBucket(ctx, "../abc"), s.DeleteBucket(ctx, "../abc"), s.PutObject(ctx, "abc", ".bucket.json", strings.NewReader("data")), s.DeleteObject(ctx, "abc", "nested/key"), s.PutObject(ctx, "abc", "file", nil)} {
		if !errors.Is(err, objectstore.ErrInvalid) {
			t.Errorf("expected invalid input, got %v", err)
		}
	}
	if repo.calls != 0 {
		t.Fatal("invalid operation reached repository")
	}
}
func TestContextIdentityAndStreamPropagate(t *testing.T) {
	repo := &repositorySpy{}
	s := New(repo)
	type traceKey struct{}
	parent := context.WithValue(context.Background(), traceKey{}, "request-123")
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	body := strings.NewReader("streamed body")
	if err := s.PutObject(ctx, "abc", "report.txt", body); err != nil {
		t.Fatal(err)
	}
	if repo.id != (objectstore.ObjectID{Bucket: "abc", Key: "report.txt"}) {
		t.Fatalf("unexpected identity: %+v", repo.id)
	}
	if repo.ctx != ctx || repo.ctx.Value(traceKey{}) != "request-123" {
		t.Fatal("request context replaced")
	}
	if repo.body != body || body.Len() != len("streamed body") {
		t.Fatal("service buffered or consumed stream")
	}
	object, err := s.GetObject(ctx, "xyz", "report.txt")
	if err != nil || object.ID.Bucket != "xyz" || object.ID.Key != "report.txt" {
		t.Fatalf("bucket identity lost: %+v %v", object, err)
	}
	if repo.ctx != ctx {
		t.Fatal("read context lost")
	}
}
func TestRepositoryErrorsRetainIdentity(t *testing.T) {
	cause := fmt.Errorf("backend context: %w", objectstore.ErrNotFound)
	repo := &repositorySpy{err: cause}
	s := New(repo)
	ctx := context.Background()
	_, getErr := s.GetObject(ctx, "abc", "file")
	_, listErr := s.ListBuckets(ctx)
	for _, err := range []error{getErr, listErr, s.CreateBucket(ctx, "abc"), s.DeleteBucket(ctx, "abc"), s.PutObject(ctx, "abc", "file", strings.NewReader("x")), s.DeleteObject(ctx, "abc", "file")} {
		if !errors.Is(err, cause) || !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("error identity lost: %v", err)
		}
	}
}

func TestOpenObjectValidationAndErrorIdentity(t *testing.T) {
	repo := &repositorySpy{err: objectstore.ErrNotFound}
	s := New(repo)
	if _, err := s.OpenObject(context.Background(), "abc", "../file"); !errors.Is(err, objectstore.ErrInvalid) || repo.calls != 0 {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.OpenObject(ctx, "abc", "file"); !errors.Is(err, context.Canceled) || repo.calls != 0 {
		t.Fatal(err)
	}
	ctx = context.Background()
	if _, err := s.OpenObject(ctx, "abc", "file"); !errors.Is(err, objectstore.ErrNotFound) || repo.ctx != ctx || repo.id.Key != "file" {
		t.Fatal(err)
	}
}

func (r *repositorySpy) Usage(ctx context.Context) (objectstore.Usage, error) {
	r.record(ctx)
	return objectstore.Usage{}, r.err
}
func (r *repositorySpy) ListObjects(ctx context.Context, id objectstore.BucketID, opts objectstore.ListOptions) (objectstore.ObjectPage, error) {
	r.record(ctx)
	r.bucket = id
	r.listOptions = opts
	return objectstore.ObjectPage{}, r.err
}

func TestListingAndUsageServiceBoundary(t *testing.T) {
	repo := &repositorySpy{}
	s := New(repo)
	ctx := context.Background()
	opts := objectstore.ListOptions{Prefix: "photo", After: "photo-1", Limit: 20}
	if _, err := s.ListObjects(ctx, "abc", opts); err != nil || repo.listOptions != opts || repo.ctx != ctx || repo.bucket != "abc" {
		t.Fatal(err)
	}
	before := repo.calls
	if _, err := s.ListObjects(ctx, "abc", objectstore.ListOptions{}); !errors.Is(err, objectstore.ErrInvalidList) || repo.calls != before {
		t.Fatal(err)
	}
	if _, err := s.ListObjects(ctx, "..", opts); !errors.Is(err, objectstore.ErrInvalid) || repo.calls != before {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ListObjects(canceled, "abc", opts); !errors.Is(err, context.Canceled) || repo.calls != before {
		t.Fatal(err)
	}
	if _, err := s.Usage(canceled); !errors.Is(err, context.Canceled) || repo.calls != before {
		t.Fatal(err)
	}
	repo.err = objectstore.ErrClosed
	if _, err := s.Usage(ctx); !errors.Is(err, objectstore.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := s.ListObjects(ctx, "abc", opts); !errors.Is(err, objectstore.ErrClosed) {
		t.Fatal(err)
	}
}
