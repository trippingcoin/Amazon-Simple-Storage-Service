package storage

import (
	"A3S/internal/objectstore"
	"context"
	"slices"
)

// ObjectKeys takes a sorted key snapshot for offline export. Unlike paginated
// listing it scans the index once, with O(number of keys) additional memory.
// It does not freeze subsequent mutations: offline callers must own the Store
// exclusively for the entire export. This is not exposed through the HTTP API.
func (s *Store) ObjectKeys(ctx context.Context, bucket objectstore.BucketID) ([]objectstore.ObjectKey, error) {
	if err := bucket.Validate(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.stateError(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, ok := s.buckets[string(bucket)]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	keys := make([]objectstore.ObjectKey, 0, len(b.objects))
	for key := range b.objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keys = append(keys, objectstore.ObjectKey(key))
	}
	slices.Sort(keys)
	return keys, nil
}
