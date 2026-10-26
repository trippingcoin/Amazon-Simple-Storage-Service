package storage

import (
	"A3S/internal/objectstore"
	"container/heap"
	"context"
	"sort"
	"strings"
)

func (s *Store) Usage(ctx context.Context) (objectstore.Usage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return objectstore.Usage{}, err
	}
	if s.closed {
		return objectstore.Usage{}, objectstore.ErrClosed
	}
	return objectstore.Usage{StoredBytes: s.storedBytes, Objects: s.objectCount, Buckets: len(s.buckets), MaxStorageBytes: s.maxStorageBytes, MaxObjects: s.maxObjects}, nil
}

// ListObjects scans the current index with O(limit) extra memory. Each page is
// consistent; pagination across requests is not a multi-request snapshot.
func (s *Store) ListObjects(ctx context.Context, bucket objectstore.BucketID, opts objectstore.ListOptions) (objectstore.ObjectPage, error) {
	var page objectstore.ObjectPage
	if err := bucket.Validate(); err != nil {
		return page, err
	}
	if err := opts.Validate(); err != nil {
		return page, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return page, objectstore.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	b, ok := s.buckets[string(bucket)]
	if !ok {
		return page, objectstore.ErrNotFound
	}
	selected := make(objectHeap, 0, opts.Limit+1)
	for key, meta := range b.objects {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if key <= string(opts.After) || !strings.HasPrefix(key, opts.Prefix) {
			continue
		}
		if len(selected) < opts.Limit+1 {
			heap.Push(&selected, meta)
		} else if key < string(selected[0].ID.Key) {
			selected[0] = meta
			heap.Fix(&selected, 0)
		}
	}
	if len(selected) > opts.Limit {
		heap.Pop(&selected)
		page.HasMore = true
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID.Key < selected[j].ID.Key })
	page.Objects = []objectstore.Object(selected)
	if page.HasMore {
		page.NextAfter = selected[len(selected)-1].ID.Key
	}
	return page, nil
}

// Max heap retains only the lexicographically smallest limit+1 keys.
type objectHeap []objectstore.Object

func (h objectHeap) Len() int           { return len(h) }
func (h objectHeap) Less(i, j int) bool { return h[i].ID.Key > h[j].ID.Key }
func (h objectHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *objectHeap) Push(x any)        { *h = append(*h, x.(objectstore.Object)) }
func (h *objectHeap) Pop() any {
	old := *h
	n := len(old)
	v := old[n-1]
	old[n-1] = objectstore.Object{}
	*h = old[:n-1]
	return v
}
