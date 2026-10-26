// Package objectstore defines storage identities, metadata and errors without
// depending on HTTP, serialization formats or a filesystem implementation.
package objectstore

import (
	"io"
	"time"
)

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
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Download pins one immutable object version. The caller must close Content.
type Download struct {
	Object  Object
	Content ReadSeekCloser
}

type Object struct {
	SHA256       string
	ID           ObjectID
	Size         int64
	ContentType  string
	LastModified time.Time
}

// Usage counts published objects, not staging files or retained open versions.
type Usage struct {
	StoredBytes     int64
	Objects         int64
	Buckets         int
	MaxStorageBytes int64
	MaxObjects      int64
}
type ListOptions struct {
	Prefix string
	After  ObjectKey
	Limit  int
}
type ObjectPage struct {
	Objects   []Object
	NextAfter ObjectKey
	HasMore   bool
}
