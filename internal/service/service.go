// Package service exposes storage operations independently of HTTP. It validates
// logical input and cancellation before invoking an injected repository.
package service

import (
	"A3S/internal/objectstore"
	"context"
	"fmt"
	"io"
)

// Repository owns complete operations: blob publication/deletion and metadata
// visibility must share a commit point. Splitting these into independent writes
// would break the crash-recovery guarantee. Close belongs to the application.
type Repository interface {
	Usage(context.Context) (objectstore.Usage, error)
	ListObjects(context.Context, objectstore.BucketID, objectstore.ListOptions) (objectstore.ObjectPage, error)
	ListBuckets(context.Context) ([]objectstore.Bucket, error)
	CreateBucket(context.Context, objectstore.BucketID) error
	DeleteBucket(context.Context, objectstore.BucketID) error
	OpenObject(context.Context, objectstore.ObjectID) (objectstore.Download, error)
	GetObject(context.Context, objectstore.ObjectID) (objectstore.Object, error)
	PutObject(context.Context, objectstore.ObjectID, io.Reader) error
	DeleteObject(context.Context, objectstore.ObjectID) error
}

type Service struct{ repo Repository }

func New(repo Repository) *Service { return &Service{repo: repo} }
func (s *Service) ListBuckets(ctx context.Context) ([]objectstore.Bucket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := s.repo.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	return result, nil
}
func (s *Service) CreateBucket(ctx context.Context, name string) error {
	id := objectstore.BucketID(name)
	if err := id.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.repo.CreateBucket(ctx, id); err != nil {
		return fmt.Errorf("create bucket: %w", err)
	}
	return nil
}
func (s *Service) DeleteBucket(ctx context.Context, name string) error {
	id := objectstore.BucketID(name)
	if err := id.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.repo.DeleteBucket(ctx, id); err != nil {
		return fmt.Errorf("delete bucket: %w", err)
	}
	return nil
}
func (s *Service) GetObject(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	id := objectstore.ObjectID{Bucket: objectstore.BucketID(bucket), Key: objectstore.ObjectKey(key)}
	if err := id.Validate(); err != nil {
		return objectstore.Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	result, err := s.repo.GetObject(ctx, id)
	if err != nil {
		return objectstore.Object{}, fmt.Errorf("get object: %w", err)
	}
	return result, nil
}
func (s *Service) PutObject(ctx context.Context, bucket, key string, body io.Reader) error {
	id := objectstore.ObjectID{Bucket: objectstore.BucketID(bucket), Key: objectstore.ObjectKey(key)}
	if err := id.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if body == nil {
		return objectstore.ErrInvalid
	}
	if err := s.repo.PutObject(ctx, id, body); err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}
func (s *Service) DeleteObject(ctx context.Context, bucket, key string) error {
	id := objectstore.ObjectID{Bucket: objectstore.BucketID(bucket), Key: objectstore.ObjectKey(key)}
	if err := id.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.repo.DeleteObject(ctx, id); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// OpenObject returns a pinned stream; ownership of Content passes to the caller.
func (s *Service) OpenObject(ctx context.Context, bucket, key string) (objectstore.Download, error) {
	id := objectstore.ObjectID{Bucket: objectstore.BucketID(bucket), Key: objectstore.ObjectKey(key)}
	if err := id.Validate(); err != nil {
		return objectstore.Download{}, err
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Download{}, err
	}
	result, err := s.repo.OpenObject(ctx, id)
	if err != nil {
		return objectstore.Download{}, fmt.Errorf("open object: %w", err)
	}
	return result, nil
}

func (s *Service) Usage(ctx context.Context) (objectstore.Usage, error) {
	if err := ctx.Err(); err != nil {
		return objectstore.Usage{}, err
	}
	result, err := s.repo.Usage(ctx)
	if err != nil {
		return objectstore.Usage{}, fmt.Errorf("storage usage: %w", err)
	}
	return result, nil
}
func (s *Service) ListObjects(ctx context.Context, bucket string, opts objectstore.ListOptions) (objectstore.ObjectPage, error) {
	id := objectstore.BucketID(bucket)
	if err := id.Validate(); err != nil {
		return objectstore.ObjectPage{}, err
	}
	if err := opts.Validate(); err != nil {
		return objectstore.ObjectPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return objectstore.ObjectPage{}, err
	}
	result, err := s.repo.ListObjects(ctx, id, opts)
	if err != nil {
		return objectstore.ObjectPage{}, fmt.Errorf("list objects: %w", err)
	}
	return result, nil
}
