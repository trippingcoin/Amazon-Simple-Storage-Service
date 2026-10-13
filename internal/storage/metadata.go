package storage

import (
	"A3S/internal/objectstore"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// diskBucket preserves the Phase 1 JSON format, independently of domain/HTTP
// structs. Unknown JSON fields in older files (XMLName, Status) are harmless.
type diskBucket struct {
	Name         string
	CreationTime time.Time
}

func (s *Store) writeBucketMetadata(stage string, bucket objectstore.Bucket) error {
	raw, err := json.Marshal(diskBucket{Name: string(bucket.ID), CreationTime: bucket.CreationTime})
	if err != nil {
		return err
	}
	f, err := s.root.OpenFile(stage+"/"+bucketInfo, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func (s *Store) readBucketMetadata(name string) (time.Time, error) {
	f, err := s.root.Open(name + "/" + bucketInfo)
	if err != nil {
		return time.Time{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, 16*1024+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return time.Time{}, err
	}
	var meta diskBucket
	if len(raw) > 16*1024 || json.Unmarshal(raw, &meta) != nil || meta.Name != name || meta.CreationTime.IsZero() {
		return time.Time{}, fmt.Errorf("invalid bucket metadata for %q", name)
	}
	return meta.CreationTime, nil
}
