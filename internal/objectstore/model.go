// Package objectstore defines storage identities, metadata and errors without
// depending on HTTP, serialization formats or a filesystem implementation.
package objectstore

import "time"

// BucketID is the logical bucket name, not a directory path.
type BucketID string
type ObjectKey string

// ObjectID identifies an object within a bucket; equal keys in different
// buckets are different objects. Physical paths belong to storage adapters.
type ObjectID struct {
	Bucket BucketID
	Key    ObjectKey
}

type Bucket struct {
	ID           BucketID
	CreationTime time.Time
	LastModified time.Time
	Empty        bool
}
type Object struct {
	ID           ObjectID
	Size         int64
	ContentType  string
	LastModified time.Time
}
