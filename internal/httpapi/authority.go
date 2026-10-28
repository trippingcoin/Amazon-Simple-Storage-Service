package httpapi

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"context"
	"io"
)

// Authority supplies shared identity state. Implementations must fail closed
// when that state is unavailable; no replica-local credential fallback is used.
type Authority interface {
	Authenticate(context.Context, string) (access.User, bool, error)
	Namespace(context.Context, string) (Operations, bool, error)
}
type operationsKey struct{}
type requestOperations struct{}

func selected(ctx context.Context) Operations { return ctx.Value(operationsKey{}).(Operations) }
func (requestOperations) Usage(ctx context.Context) (objectstore.Usage, error) {
	return selected(ctx).Usage(ctx)
}
func (requestOperations) ListBuckets(ctx context.Context) ([]objectstore.Bucket, error) {
	return selected(ctx).ListBuckets(ctx)
}
func (requestOperations) CreateBucket(ctx context.Context, b string) error {
	return selected(ctx).CreateBucket(ctx, b)
}
func (requestOperations) DeleteBucket(ctx context.Context, b string) error {
	return selected(ctx).DeleteBucket(ctx, b)
}
func (requestOperations) ListObjects(ctx context.Context, b string, o objectstore.ListOptions) (objectstore.ObjectPage, error) {
	return selected(ctx).ListObjects(ctx, b, o)
}
func (requestOperations) GetObject(ctx context.Context, b, k string) (objectstore.Object, error) {
	return selected(ctx).GetObject(ctx, b, k)
}
func (requestOperations) OpenObject(ctx context.Context, b, k string) (objectstore.Download, error) {
	return selected(ctx).OpenObject(ctx, b, k)
}
func (requestOperations) PutObject(ctx context.Context, b, k string, r io.Reader) error {
	return selected(ctx).PutObject(ctx, b, k, r)
}
func (requestOperations) DeleteObject(ctx context.Context, b, k string) error {
	return selected(ctx).DeleteObject(ctx, b, k)
}
