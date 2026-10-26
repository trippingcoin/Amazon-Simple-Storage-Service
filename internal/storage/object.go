package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"time"
)

func (s *Store) GetObject(ctx context.Context, id objectstore.ObjectID) (objectstore.Object, error) {
	bucket, key := string(id.Bucket), string(id.Key)
	if id.Validate() != nil {
		return objectstore.Object{}, objectstore.ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return objectstore.Object{}, objectstore.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Object{}, err
	}
	b, ok := s.buckets[bucket]
	if !ok {
		return objectstore.Object{}, objectstore.ErrNotFound
	}
	o, ok := b.objects[key]
	if !ok {
		return objectstore.Object{}, objectstore.ErrNotFound
	}
	return o, nil
}
func (s *Store) DeleteObject(ctx context.Context, id objectstore.ObjectID) error {
	bucket, key := string(id.Bucket), string(id.Key)
	if id.Validate() != nil {
		return objectstore.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return objectstore.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := s.buckets[bucket]
	if !ok {
		return objectstore.ErrNotFound
	}
	if _, ok = b.objects[key]; !ok {
		return objectstore.ErrNotFound
	}
	if err := s.regularObject(bucket, key); err != nil {
		return err
	}
	if err := s.root.Remove(bucket + "/" + key); err != nil {
		return err
	}
	s.storedBytes -= b.objects[key].Size
	s.objectCount--
	delete(b.objects, key)
	b.meta.LastModified = time.Now().UTC()
	return s.syncDir(bucket)
}
func (s *Store) regularObject(bucket, key string) error {
	info, err := s.root.Lstat(bucket)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return objectstore.ErrInvalid
	}
	info, err = s.root.Lstat(bucket + "/" + key)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return objectstore.ErrInvalid
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *Store) PutObject(ctx context.Context, id objectstore.ObjectID, body io.Reader) error {
	bucket, key := string(id.Bucket), string(id.Key)
	if id.Validate() != nil || body == nil {
		return objectstore.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return objectstore.ErrBusy
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return objectstore.ErrClosed
	}
	b, ok := s.buckets[bucket]
	if !ok {
		s.mu.Unlock()
		return objectstore.ErrNotFound
	}
	b.uploads++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); b.uploads--; s.mu.Unlock() }()
	randomID, err := nonce()
	if err != nil {
		return err
	}
	stage := privateDir + "/upload-" + randomID
	f, err := s.root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { f.Close(); s.cleanup(stage) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(contextReader{ctx, body}, s.maxBytes+1))
	if err != nil {
		return err
	}
	if n > s.maxBytes {
		return objectstore.ErrTooLarge
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	contentType, err := detectContentType(f)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	// Reject symlinks and unexpected destination types even if introduced locally.
	bi, err := s.root.Lstat(bucket)
	if err != nil {
		return err
	}
	if !bi.IsDir() {
		return objectstore.ErrInvalid
	}
	dest := bucket + "/" + key
	if oi, e := s.root.Lstat(dest); e == nil {
		if !oi.Mode().IsRegular() {
			return objectstore.ErrInvalid
		}
		// On case-insensitive/normalizing filesystems, a different spelling
		// must not silently overwrite another logical key.
		if _, known := b.objects[key]; !known {
			return objectstore.ErrInvalid
		}
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	old, exists := b.objects[key]
	// Evaluate against the current published version under the publication lock.
	// A lowered quota must still permit deletion and non-growing replacement.
	if info.Size() > math.MaxInt64-(s.storedBytes-old.Size) {
		return objectstore.ErrQuotaExceeded
	}
	nextBytes := s.storedBytes - old.Size + info.Size()
	if (s.maxStorageBytes > 0 && nextBytes > s.maxStorageBytes && info.Size() > old.Size) ||
		(!exists && s.maxObjects > 0 && s.objectCount >= s.maxObjects) {
		return objectstore.ErrQuotaExceeded
	}
	if err = s.rename(stage, dest); err != nil {
		return err
	}
	// Rename is the commit point. A later directory-sync failure is an uncertain
	// durability result, not permission to roll back an already visible object.
	s.storedBytes = nextBytes
	if !exists {
		s.objectCount++
	}
	b.objects[key] = objectstore.Object{SHA256: hex.EncodeToString(hash.Sum(nil)), ID: id, Size: info.Size(), ContentType: contentType, LastModified: info.ModTime().UTC()}
	b.meta.LastModified = time.Now().UTC()
	return errors.Join(s.syncDir(bucket), s.syncDir(privateDir))
}
