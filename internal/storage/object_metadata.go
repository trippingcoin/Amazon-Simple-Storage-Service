package storage

import (
	"A3S/internal/objectstore"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
)

const objectMetadataDir = privateDir + "/metadata"

var metadataNamePattern = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)
var errMetadataInvalid = errors.New("object metadata invalid or stale")

// ctime changes on replacement/content updates even if mtime is preserved.
// These local identities are deliberately invalidated by copy/restore.
type fileIdentity struct {
	Device, Inode                uint64
	ChangedSeconds, ChangedNanos int64
}
type diskObject struct {
	Object   objectstore.Object
	Identity fileIdentity
}
type metadataEnvelope struct {
	Version int
	Digest  string
	Record  json.RawMessage
}

func objectMetadataPath(id objectstore.ObjectID) string {
	sum := sha256.Sum256([]byte(string(id.Bucket) + "\x00" + string(id.Key)))
	return objectMetadataDir + "/" + hex.EncodeToString(sum[:]) + ".json"
}
func (s *Store) prepareObjectMetadata() error {
	if err := s.root.Mkdir(objectMetadataDir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := s.root.Lstat(objectMetadataDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("metadata directory must be private and regular")
	}
	entries, err := readDir(s.root, objectMetadataDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !metadataNamePattern.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			return errors.New("unexpected entry in object metadata directory")
		}
	}
	return s.syncDir(privateDir)
}

// Atomic, checksummed derived record. Call with the publication lock held, or
// during exclusive startup recovery. A failed write never rolls back a blob.
func (s *Store) persistObjectMetadata(meta objectstore.Object, info os.FileInfo) error {
	identity, err := identifyFile(info)
	if err != nil {
		return err
	}
	record, err := json.Marshal(diskObject{Object: meta, Identity: identity})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(record)
	raw, err := json.Marshal(metadataEnvelope{Version: 1, Digest: hex.EncodeToString(digest[:]), Record: record})
	if err != nil {
		return err
	}
	random, err := nonce()
	if err != nil {
		return err
	}
	stage := privateDir + "/metadata-" + random
	f, err := s.root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { f.Close(); s.cleanup(stage) }()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	destination := objectMetadataPath(meta.ID)
	if fi, e := s.root.Lstat(destination); e == nil {
		if !fi.Mode().IsRegular() {
			return errors.New("unsafe object metadata entry")
		}
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if err = s.metadataRename(stage, destination); err != nil {
		return err
	}
	return errors.Join(s.syncDir(objectMetadataDir), s.syncDir(privateDir))
}
func (s *Store) readObjectMetadata(actual objectstore.Object, info os.FileInfo) (objectstore.Object, error) {
	path := objectMetadataPath(actual.ID)
	fi, err := s.root.Lstat(path)
	if err != nil {
		return objectstore.Object{}, err
	}
	if !fi.Mode().IsRegular() {
		return objectstore.Object{}, errors.New("unsafe object metadata entry")
	}
	if fi.Size() > 16<<10 {
		return objectstore.Object{}, errMetadataInvalid
	}
	f, err := s.root.Open(path)
	if err != nil {
		return objectstore.Object{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return objectstore.Object{}, err
	}
	var envelope metadataEnvelope
	if len(raw) > 16<<10 || json.Unmarshal(raw, &envelope) != nil {
		return objectstore.Object{}, errMetadataInvalid
	}
	if envelope.Version > 1 {
		return objectstore.Object{}, errors.New("unsupported object metadata version")
	}
	if envelope.Version != 1 {
		return objectstore.Object{}, errMetadataInvalid
	}
	digest := sha256.Sum256(envelope.Record)
	if envelope.Digest != hex.EncodeToString(digest[:]) {
		return objectstore.Object{}, errMetadataInvalid
	}
	var record diskObject
	if json.Unmarshal(envelope.Record, &record) != nil {
		return objectstore.Object{}, errMetadataInvalid
	}
	identity, err := identifyFile(info)
	if err != nil {
		return objectstore.Object{}, err
	}
	meta := record.Object
	checksum, err := hex.DecodeString(meta.SHA256)
	if err != nil || len(checksum) != sha256.Size || meta.ID != actual.ID || meta.Size != actual.Size || meta.ContentType != actual.ContentType || !meta.LastModified.Equal(actual.LastModified) || record.Identity != identity {
		return objectstore.Object{}, errMetadataInvalid
	}
	return meta, nil
}
func (s *Store) recoverObjectMetadata(meta objectstore.Object, f *os.File, info os.FileInfo) (objectstore.Object, error) {
	cached, err := s.readObjectMetadata(meta, info)
	if err == nil {
		return cached, nil
	}
	if !errors.Is(err, errMetadataInvalid) && !errors.Is(err, fs.ErrNotExist) {
		return objectstore.Object{}, err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return objectstore.Object{}, err
	}
	// Refuse a mixed snapshot if files were edited outside the service at startup.
	after, err := f.Stat()
	if err != nil {
		return objectstore.Object{}, err
	}
	beforeIdentity, err := identifyFile(info)
	if err != nil {
		return objectstore.Object{}, err
	}
	afterIdentity, err := identifyFile(after)
	if err != nil {
		return objectstore.Object{}, err
	}
	if beforeIdentity != afterIdentity || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return objectstore.Object{}, errors.New("object changed during metadata recovery")
	}
	meta.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err = s.persistObjectMetadata(meta, after); err != nil {
		return objectstore.Object{}, fmt.Errorf("rebuild object metadata: %w", err)
	}
	return meta, nil
}
func (s *Store) removeObjectMetadata(id objectstore.ObjectID) error {
	err := s.root.Remove(objectMetadataPath(id))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return s.syncDir(objectMetadataDir)
}
func (s *Store) pruneObjectMetadata() error {
	live := make(map[string]bool)
	for _, b := range s.buckets {
		for _, meta := range b.objects {
			live[objectMetadataPath(meta.ID)] = true
		}
	}
	entries, err := readDir(s.root, objectMetadataDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !metadataNamePattern.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			return errors.New("unexpected entry in object metadata directory")
		}
		path := objectMetadataDir + "/" + entry.Name()
		if !live[path] {
			if err = s.root.Remove(path); err != nil {
				return err
			}
		}
	}
	return s.syncDir(objectMetadataDir)
}
