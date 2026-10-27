package httpapi

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"context"
	"io"
)

// sharedOperations is the least-privilege application boundary for one grantee
// and one owner's namespace. Every operation is explicitly implemented: adding
// a method to Operations must not accidentally inherit unrestricted access.
type sharedOperations struct {
	base   Operations
	grants map[string]string
}

var _ Operations = (*sharedOperations)(nil)

func (s *sharedOperations) allowed(bucket string, write bool) bool {
	permission := s.grants[bucket]
	return permission == "write" || (!write && permission == "read")
}
func (s *sharedOperations) Usage(context.Context) (objectstore.Usage, error) {
	// Namespace totals include private buckets and must never reach a grantee.
	return objectstore.Usage{}, access.ErrDenied
}
func (s *sharedOperations) ListBuckets(ctx context.Context) ([]objectstore.Bucket, error) {
	buckets, err := s.base.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]objectstore.Bucket, 0)
	for _, b := range buckets {
		if s.allowed(string(b.ID), false) {
			visible = append(visible, b)
		}
	}
	return visible, nil
}
func (s *sharedOperations) ListObjects(ctx context.Context, bucket string, opts objectstore.ListOptions) (objectstore.ObjectPage, error) {
	if !s.allowed(bucket, false) {
		return objectstore.ObjectPage{}, access.ErrDenied
	}
	return s.base.ListObjects(ctx, bucket, opts)
}
func (s *sharedOperations) CreateBucket(context.Context, string) error { return access.ErrDenied }
func (s *sharedOperations) DeleteBucket(context.Context, string) error { return access.ErrDenied }
func (s *sharedOperations) OpenObject(ctx context.Context, bucket, key string) (objectstore.Download, error) {
	if !s.allowed(bucket, false) {
		return objectstore.Download{}, access.ErrDenied
	}
	return s.base.OpenObject(ctx, bucket, key)
}
func (s *sharedOperations) GetObject(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	if !s.allowed(bucket, false) {
		return objectstore.Object{}, access.ErrDenied
	}
	return s.base.GetObject(ctx, bucket, key)
}
func (s *sharedOperations) PutObject(ctx context.Context, bucket, key string, body io.Reader) error {
	if !s.allowed(bucket, true) {
		return access.ErrDenied
	}
	return s.base.PutObject(ctx, bucket, key, body)
}
func (s *sharedOperations) DeleteObject(ctx context.Context, bucket, key string) error {
	if !s.allowed(bucket, true) {
		return access.ErrDenied
	}
	return s.base.DeleteObject(ctx, bucket, key)
}
