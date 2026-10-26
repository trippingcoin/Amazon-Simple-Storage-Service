package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
)

// OpenObject pins the descriptor and metadata under the publication lock, then
// releases the lock before any bulk IO. Rename/delete cannot alter that version.
func (s *Store) OpenObject(ctx context.Context, id objectstore.ObjectID) (objectstore.Download, error) {
	var out objectstore.Download
	if err := id.Validate(); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return out, objectstore.ErrClosed
	}
	select {
	case s.downloads <- struct{}{}:
	default:
		s.mu.RUnlock()
		return out, objectstore.ErrBusy
	}
	success := false
	defer func() {
		if !success {
			<-s.downloads
		}
	}()
	b := s.buckets[string(id.Bucket)]
	if b == nil {
		s.mu.RUnlock()
		return out, objectstore.ErrNotFound
	}
	meta, ok := b.objects[string(id.Key)]
	if !ok {
		s.mu.RUnlock()
		return out, objectstore.ErrNotFound
	}
	if err := s.regularObject(string(id.Bucket), string(id.Key)); err != nil {
		s.mu.RUnlock()
		return out, err
	}
	path := string(id.Bucket) + "/" + string(id.Key)
	f, err := s.root.Open(path)
	s.mu.RUnlock()
	if err != nil {
		return out, err
	}
	defer func() {
		if !success {
			f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Size() != meta.Size || !info.ModTime().Equal(meta.LastModified) {
		return out, errors.New("object changed outside storage service")
	}
	stream := &downloadStream{ctx: ctx, file: f, release: func() { <-s.downloads }}
	// Normally populated by durable metadata/recovery. Retain the bounded
	// fallback for an in-memory entry without a checksum.
	if meta.SHA256 == "" {
		hash := sha256.New()
		if _, err = io.Copy(hash, stream); err != nil {
			return out, err
		}
		if _, err = stream.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		meta.SHA256 = hex.EncodeToString(hash.Sum(nil))
		s.mu.Lock()
		if current, e := s.root.Stat(path); e == nil && os.SameFile(info, current) {
			if bucket := s.buckets[string(id.Bucket)]; bucket != nil {
				bucket.objects[string(id.Key)] = meta
				if e = s.persistObjectMetadata(meta, current); e != nil {
					s.mu.Unlock()
					return out, e
				}
			}
		}
		s.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	success = true
	return objectstore.Download{Object: meta, Content: stream}, nil
}

type downloadStream struct {
	ctx      context.Context
	file     *os.File
	once     sync.Once
	release  func()
	closeErr error
}

func (d *downloadStream) Read(p []byte) (int, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	return d.file.Read(p)
}
func (d *downloadStream) Seek(n int64, whence int) (int64, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	return d.file.Seek(n, whence)
}
func (d *downloadStream) Close() error {
	d.once.Do(func() { d.closeErr = d.file.Close(); d.release() })
	return d.closeErr
}
