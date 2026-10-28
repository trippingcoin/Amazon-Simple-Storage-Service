// Package backup implements offline, streamed backups with a restore drill.
package backup

import (
	"A3S/internal/objectstore"
	"A3S/internal/storage"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Manifest struct {
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	ArchiveSHA256   string    `json:"archive_sha256"`
	InventorySHA256 string    `json:"inventory_sha256"`
	ArchiveBytes    int64     `json:"archive_bytes"`
	Namespaces      int       `json:"namespaces"`
	Buckets         int64     `json:"buckets"`
	Objects         int64     `json:"objects"`
	ObjectBytes     int64     `json:"object_bytes"`
}

var limits = objectstore.Limits{MaxObjectBytes: 1 << 40, MaxConcurrentUploads: 1, MaxConcurrentDownloads: 1}

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
func digestOK(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}

// openSet takes the same locks as the server, performs recovery and opens revoked
// namespaces too. No network or authentication credentials are needed.
func openSet(dir string) (map[string]*storage.Store, func() error, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil, errors.New("source must be an existing real storage directory")
	}
	root, err := storage.Open(dir, limits)
	if err != nil {
		return nil, nil, err
	}
	stores := map[string]*storage.Store{"": root}
	close := func() error {
		var err error
		for name, s := range stores {
			if name != "" {
				err = errors.Join(err, s.Close())
			}
		}
		return errors.Join(err, root.Close())
	}
	if _, err = os.Lstat(filepath.Join(dir, ".triple-s", "tenants")); err == nil {
		tenants, e := root.OpenNamespaces(nil, limits)
		if e != nil {
			close()
			return nil, nil, e
		}
		for name, s := range tenants {
			stores[name] = s
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		close()
		return nil, nil, err
	}
	return stores, close, nil
}
func inventory(ctx context.Context, stores map[string]*storage.Store) (Manifest, error) {
	result := Manifest{Namespaces: len(stores)}
	h := sha256.New()
	enc := json.NewEncoder(h)
	names := make([]string, 0, len(stores))
	for name := range stores {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := enc.Encode(struct{ Namespace string }{name}); err != nil {
			return result, err
		}
		s := stores[name]
		buckets, err := s.ListBuckets(ctx)
		if err != nil {
			return result, err
		}
		for _, b := range buckets {
			result.Buckets++
			if err = enc.Encode(struct {
				Bucket  objectstore.BucketID
				Created time.Time
			}{b.ID, b.CreationTime}); err != nil {
				return result, err
			}

			keys, e := s.ObjectKeys(ctx, b.ID)
			if e != nil {
				return result, e
			}
			for _, key := range keys {
				d, e := s.OpenObject(ctx, objectstore.ObjectID{Bucket: b.ID, Key: key})
				if e != nil {
					return result, e
				}
				obj := d.Object
				hash := sha256.New()
				n, e := io.Copy(hash, contextReader{ctx, d.Content})
				e = errors.Join(e, d.Content.Close())
				if e != nil {
					return result, e
				}
				if n != obj.Size || hex.EncodeToString(hash.Sum(nil)) != obj.SHA256 {
					return result, errors.New("object content does not match stored checksum")
				}
				if e = enc.Encode(obj); e != nil {
					return result, e
				}
				result.Objects++
				result.ObjectBytes += n
			}
		}
	}
	result.InventorySHA256 = hex.EncodeToString(h.Sum(nil))
	return result, nil
}
func canonicalNew(path string) (string, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(abs))
	if e != nil {
		return "", e
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
func inside(path, root string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

// Create recovers the stopped source, snapshots it while holding all its locks,
// then verifies the backup in an independent temporary restore. out must be new.
func Create(ctx context.Context, source, out, workDir string) (m Manifest, err error) {
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return m, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return m, err
	}
	out, err = canonicalNew(out)
	if err != nil {
		return m, err
	}
	if inside(out, source) {
		return m, errors.New("backup destination must be outside storage")
	}
	scratchParent := workDir
	if scratchParent == "" {
		scratchParent = os.TempDir()
	}
	scratchParent, err = filepath.EvalSymlinks(scratchParent)
	if err != nil {
		return m, err
	}
	scratchParent, err = filepath.Abs(scratchParent)
	if err != nil {
		return m, err
	}
	if inside(scratchParent, source) {
		return m, errors.New("verification work directory must be outside storage")
	}
	workDir = scratchParent
	stores, closeSet, err := openSet(source)
	if err != nil {
		return m, err
	}
	defer func() { err = errors.Join(err, closeSet()) }()
	if err = os.Mkdir(out, 0700); err != nil {
		return m, err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, os.RemoveAll(out))
		}
	}()
	m, err = inventory(ctx, stores)
	if err != nil {
		return m, err
	}
	m.Version = 1
	m.CreatedAt = time.Now().UTC()
	m.ArchiveSHA256, m.ArchiveBytes, err = archive(ctx, source, filepath.Join(out, "data.tar"))
	if err != nil {
		return m, err
	}
	if err = closeSet(); err != nil {
		return m, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	f, err := os.OpenFile(filepath.Join(out, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return m, err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return m, err
	}
	if err = syncDir(out); err != nil {
		return m, err
	}
	if _, err = Verify(ctx, out, workDir); err != nil {
		return m, fmt.Errorf("backup restore verification: %w", err)
	}
	if err = syncDir(filepath.Dir(out)); err != nil {
		return m, err
	}
	complete = true
	return m, nil
}
func Verify(ctx context.Context, bundle, workDir string) (m Manifest, err error) {
	dir, err := os.MkdirTemp(workDir, "triple-s-verify-")
	if err != nil {
		return Manifest{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	return restoreInto(ctx, bundle, dir)
}

// Restore never overwrites an existing destination and removes its own partial
// directory on failure. It never switches a running server to restored data.
func Restore(ctx context.Context, bundle, destination string) (m Manifest, err error) {
	destination, err = canonicalNew(destination)
	if err != nil {
		return m, err
	}
	src, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		return m, err
	}
	src, err = filepath.Abs(src)
	if err != nil {
		return m, err
	}
	if inside(destination, src) {
		return m, errors.New("restore destination must be outside backup")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return m, err
	}
	ok := false
	defer func() {
		if !ok {
			err = errors.Join(err, os.RemoveAll(destination))
		}
	}()
	m, err = restoreInto(ctx, bundle, destination)
	if err != nil {
		return m, err
	}
	if err = syncDir(destination); err != nil {
		return m, err
	}
	if err = syncDir(filepath.Dir(destination)); err != nil {
		return m, err
	}
	ok = true
	return m, nil
}
func restoreInto(ctx context.Context, bundle, destination string) (m Manifest, err error) {
	root, err := os.OpenRoot(bundle)
	if err != nil {
		return m, err
	}
	defer root.Close()
	f, err := regular(root, "manifest.json")
	if err != nil {
		return m, err
	}
	dec := json.NewDecoder(io.LimitReader(f, 16385))
	dec.DisallowUnknownFields()
	err = dec.Decode(&m)
	if err != nil || dec.Decode(new(any)) != io.EOF || m.Version != 1 || m.CreatedAt.IsZero() || !digestOK(m.ArchiveSHA256) || !digestOK(m.InventorySHA256) || m.ArchiveBytes < 1024 || m.Namespaces < 1 || m.Buckets < 0 || m.Objects < 0 || m.ObjectBytes < 0 {
		f.Close()
		return m, errors.New("invalid backup manifest")
	}
	if err = f.Close(); err != nil {
		return m, err
	}
	tarFile, err := regular(root, "data.tar")
	if err != nil {
		return m, err
	}
	defer tarFile.Close()
	info, err := tarFile.Stat()
	if err != nil {
		return m, err
	}
	if info.Size() != m.ArchiveBytes {
		return m, errors.New("archive size mismatch")
	}
	h := sha256.New()
	if _, err = io.Copy(h, contextReader{ctx, tarFile}); err != nil {
		return m, err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.ArchiveSHA256 {
		return m, errors.New("archive checksum mismatch")
	}
	if _, err = tarFile.Seek(0, io.SeekStart); err != nil {
		return m, err
	}
	// Hash the same bytes used by extraction as well: do not trust a file
	// which changes between the initial checksum and the restore pass.
	secondHash := sha256.New()
	stream := io.TeeReader(contextReader{ctx, tarFile}, secondHash)
	if err = extract(ctx, stream, destination, m.ArchiveBytes); err != nil {
		return m, err
	}
	if _, err = io.Copy(io.Discard, stream); err != nil {
		return m, err
	}
	if hex.EncodeToString(secondHash.Sum(nil)) != m.ArchiveSHA256 {
		return m, errors.New("archive changed during extraction")
	}
	stores, closeSet, err := openSet(destination)
	if err != nil {
		return m, fmt.Errorf("restored storage recovery: %w", err)
	}
	actual, e := inventory(ctx, stores)
	err = errors.Join(e, closeSet())
	if err != nil {
		return m, err
	}
	if actual.InventorySHA256 != m.InventorySHA256 || actual.Namespaces != m.Namespaces || actual.Buckets != m.Buckets || actual.Objects != m.Objects || actual.ObjectBytes != m.ObjectBytes {
		return m, errors.New("restored object inventory mismatch")
	}
	return m, nil
}
func regular(root *os.Root, name string) (*os.File, error) {
	before, e := root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("backup entry must be a regular file")
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, errors.New("backup entry changed")
	}
	return f, nil
}
