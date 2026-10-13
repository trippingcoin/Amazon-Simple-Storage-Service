package storage

import (
	"A3S/internal/objectstore"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

func (s *Store) recover() error {
	// The process lock is already held. These names belong exclusively to this
	// implementation and can never be uploaded through the object API.
	entries, err := readDir(s.root, privateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "lock" {
			continue
		}
		if !temporaryPattern.MatchString(name) {
			return fmt.Errorf("unexpected file in private storage: %q", name)
		}
		path := privateDir + "/" + name
		if strings.HasPrefix(name, "upload-") {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("unsafe temporary upload: %q", name)
			}
			if err = s.root.Remove(path); err != nil {
				return err
			}
		} else {
			if !entry.IsDir() {
				return fmt.Errorf("unsafe temporary bucket: %q", name)
			}
			children, e := readDir(s.root, path)
			if e != nil {
				return e
			}
			for _, child := range children {
				if child.Name() != bucketInfo || !child.Type().IsRegular() {
					return fmt.Errorf("unexpected staged bucket content: %q", name)
				}
			}
			for _, child := range children {
				if err = s.root.Remove(path + "/" + child.Name()); err != nil {
					return err
				}
			}
			if err = s.root.Remove(path); err != nil {
				return err
			}
		}
	}
	if err = s.syncDir(privateDir); err != nil {
		return err
	}
	entries, err = readDir(s.root, ".")
	if err != nil {
		return err
	}
	legacy := s.legacyCreationTimes()
	for _, entry := range entries {
		name := entry.Name()
		if name == privateDir {
			continue
		}
		if name == "BucketMetaData.csv" && entry.Type().IsRegular() {
			continue
		}
		if objectstore.BucketID(name).Validate() != nil || !entry.IsDir() {
			return fmt.Errorf("unexpected storage entry %q; inspect it before starting", name)
		}
		info, e := s.root.Lstat(name)
		if e != nil {
			return e
		}
		b := &bucketState{meta: objectstore.Bucket{ID: objectstore.BucketID(name), CreationTime: info.ModTime().UTC(), LastModified: info.ModTime().UTC()}, objects: make(map[string]objectstore.Object)}
		if created, ok := legacy[name]; ok {
			b.meta.CreationTime = created
		}
		files, e := readDir(s.root, name)
		if e != nil {
			return e
		}
		for _, entry := range files {
			key := entry.Name()
			if !entry.Type().IsRegular() {
				return fmt.Errorf("non-regular bucket entry %q/%q", name, key)
			}
			if key == "ObjectMetaData.csv" {
				continue
			} // Legacy CSV is retained, never trusted for object existence.
			if key == bucketInfo {
				created, e := s.readBucketMetadata(name)
				if e != nil {
					return e
				}
				b.meta.CreationTime = created
				continue
			}
			if objectstore.ObjectKey(key).Validate() != nil {
				return fmt.Errorf("unsafe legacy object key %q/%q; migrate it before starting", name, key)
			}
			f, e := s.root.Open(name + "/" + key)
			if e != nil {
				return e
			}
			fi, statErr := f.Stat()
			var prefix [512]byte
			n, readErr := f.Read(prefix[:])
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			e = errors.Join(statErr, readErr, f.Close())
			if e != nil {
				return e
			}
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("non-regular object %q/%q", name, key)
			}
			contentType := "application/octet-stream"
			if n > 0 {
				contentType = http.DetectContentType(prefix[:n])
			}
			b.objects[key] = objectstore.Object{ID: objectstore.ObjectID{Bucket: objectstore.BucketID(name), Key: objectstore.ObjectKey(key)}, Size: fi.Size(), ContentType: contentType, LastModified: fi.ModTime().UTC()}
			if fi.ModTime().After(b.meta.LastModified) {
				b.meta.LastModified = fi.ModTime().UTC()
			}
		}
		s.buckets[name] = b
	}
	return nil
}

// Legacy CSV contributes only optional creation timestamps. Truncated or stale
// CSV cannot hide a real object or resurrect a deleted object.
func (s *Store) legacyCreationTimes() map[string]time.Time {
	result := make(map[string]time.Time)
	info, err := s.root.Lstat("BucketMetaData.csv")
	if errors.Is(err, fs.ErrNotExist) {
		return result
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		log.Print("legacy bucket timestamps ignored: invalid metadata file")
		return result
	}
	f, err := s.root.Open("BucketMetaData.csv")
	if err != nil {
		log.Print("legacy bucket timestamps unavailable")
		return result
	}
	defer f.Close()
	reader := csv.NewReader(io.LimitReader(f, 4<<20))
	reader.FieldsPerRecord = 4
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			log.Print("legacy bucket timestamps ignored: malformed CSV")
			return make(map[string]time.Time)
		}
		if objectstore.BucketID(row[0]).Validate() != nil {
			continue
		}
		created, e := time.Parse(time.RFC3339, row[1])
		if e == nil {
			result[row[0]] = created
		}
	}
	return result
}
