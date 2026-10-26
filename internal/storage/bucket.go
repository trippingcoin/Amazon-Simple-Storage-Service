package storage

import (
	"A3S/internal/objectstore"
	"context"
	"errors"
	"io/fs"
	"time"
)

func (s *Store) CreateBucket(ctx context.Context, id objectstore.BucketID) error {
	name := string(id)
	if err := id.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stateError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.buckets[name]; ok {
		return objectstore.ErrExists
	}
	if _, err := s.root.Lstat(name); err == nil {
		return objectstore.ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	randomID, err := nonce()
	if err != nil {
		return err
	}
	stage := privateDir + "/bucket-" + randomID
	if err = s.root.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer func() { s.cleanup(stage + "/" + bucketInfo); s.cleanup(stage) }()
	now := time.Now().UTC()
	m := objectstore.Bucket{ID: id, CreationTime: now, LastModified: now}
	if err = s.writeBucketMetadata(stage, m); err != nil {
		return err
	}
	if err = s.syncDir(stage); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = s.rename(stage, name); err != nil {
		return err
	}
	s.buckets[name] = &bucketState{meta: m, objects: make(map[string]objectstore.Object)}
	return errors.Join(s.syncDir("."), s.syncDir(privateDir))
}
func (s *Store) DeleteBucket(ctx context.Context, id objectstore.BucketID) error {
	name := string(id)
	if err := id.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stateError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := s.buckets[name]
	if !ok {
		return objectstore.ErrNotFound
	}
	if len(b.objects) > 0 || b.uploads > 0 {
		return objectstore.ErrNotEmpty
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return objectstore.ErrInvalid
	}
	entries, err := readDir(s.root, name)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if (e.Name() != bucketInfo && e.Name() != "ObjectMetaData.csv") || !e.Type().IsRegular() {
			return objectstore.ErrNotEmpty
		}
	}
	// Never recursively delete a bucket. Even an unindexed file blocks deletion.
	for _, e := range entries {
		if err = s.root.Remove(name + "/" + e.Name()); err != nil {
			return err
		}
	}
	if err = s.root.Remove(name); err != nil {
		return err
	}
	delete(s.buckets, name)
	return s.syncDir(".")
}
