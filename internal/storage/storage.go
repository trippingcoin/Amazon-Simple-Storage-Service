// Package storage implements a single-process, disk-backed store. Object files
// are authoritative; the in-memory index is rebuilt on every start.
package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

const privateDir = ".triple-s"
const bucketInfo = ".bucket.json"

var temporaryPattern = regexp.MustCompile(`^(upload|bucket|metadata)-[0-9a-f]{32}$`)

type Options = objectstore.Limits

type bucketState struct {
	meta    objectstore.Bucket
	objects map[string]objectstore.Object
	uploads int
}
type Store struct {
	mu                          sync.RWMutex
	root                        *os.Root
	lock                        *os.File
	buckets                     map[string]*bucketState
	slots                       chan struct{}
	downloads                   chan struct{}
	maxBytes                    int64
	maxStorageBytes, maxObjects int64
	storedBytes, objectCount    int64
	closed                      bool
	// Injected only by package tests to exercise publication failures.
	rename         func(string, string) error
	metadataRename func(string, string) error
}

func Open(dir string, opts Options) (*Store, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		root.Close()
		return nil, errors.New("storage directory must be private (chmod 700 the configured directory before starting)")
	}
	downloads := opts.MaxConcurrentDownloads
	if downloads == 0 {
		downloads = 16
	}
	s := &Store{maxStorageBytes: opts.MaxStorageBytes, maxObjects: opts.MaxObjects, downloads: make(chan struct{}, downloads), root: root, buckets: make(map[string]*bucketState), slots: make(chan struct{}, opts.MaxConcurrentUploads), maxBytes: opts.MaxObjectBytes, rename: root.Rename, metadataRename: root.Rename}
	ok := false
	defer func() {
		if !ok {
			if s.lock != nil {
				s.lock.Close()
			}
			root.Close()
		}
	}()
	if err = root.Mkdir(privateDir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err = root.Lstat(privateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private storage directory must be a real, owner-only directory")
	}
	if info, err = root.Lstat(privateDir + "/lock"); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("storage lock must be a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	s.lock, err = root.OpenFile(privateDir+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(s.lock); err != nil {
		return nil, fmt.Errorf("exclusive storage lock: %w", err)
	}
	if err = s.prepareObjectMetadata(); err != nil {
		return nil, err
	}
	if err = s.recover(); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// Close must be called after the HTTP server and its handlers have stopped.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if len(s.downloads) != 0 {
		return objectstore.ErrBusy
	}
	for _, b := range s.buckets {
		if b.uploads != 0 {
			return objectstore.ErrBusy
		}
	}
	s.closed = true
	return errors.Join(s.lock.Close(), s.root.Close())
}
func (s *Store) MaxObjectBytes() int64 { return s.maxBytes }

func readDir(root *os.Root, name string) ([]os.DirEntry, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	entries, readErr := f.ReadDir(-1)
	return entries, errors.Join(readErr, f.Close())
}
func (s *Store) syncDir(name string) error {
	f, err := s.root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func (s *Store) cleanup(name string) {
	if err := s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("temporary_cleanup_failed")
	}
}

func nonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Store) ListBuckets(ctx context.Context) ([]objectstore.Bucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, objectstore.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]objectstore.Bucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		m := b.meta
		m.Empty = len(b.objects) == 0
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
