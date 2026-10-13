package objectstore

import (
	"errors"
	"net"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("invalid bucket or object name")
	ErrNotFound = errors.New("bucket or object not found")
	ErrExists   = errors.New("bucket already exists")
	ErrNotEmpty = errors.New("bucket is not empty or has active uploads")
	ErrTooLarge = errors.New("object exceeds size limit")
	ErrBusy     = errors.New("upload capacity exhausted")
	ErrClosed   = errors.New("storage is closed")
)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (id BucketID) Validate() error {
	name := string(id)
	if !bucketPattern.MatchString(name) || strings.Contains(name, "..") || net.ParseIP(name) != nil {
		return ErrInvalid
	}
	return nil
}
func (key ObjectKey) Validate() error {
	value := string(key)
	if value == "" || len(value) > 255 || !utf8.ValidString(value) || value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return ErrInvalid
	}
	// These names are reserved by the existing local storage contract.
	switch strings.ToLower(value) {
	case "objectmetadata.csv", "bucketmetadata.csv", ".bucket.json", ".triple-s":
		return ErrInvalid
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	return nil
}
func (id ObjectID) Validate() error {
	if err := id.Bucket.Validate(); err != nil {
		return err
	}
	return id.Key.Validate()
}

type Limits struct {
	MaxObjectBytes       int64
	MaxConcurrentUploads int
}

func (l Limits) Validate() error {
	if l.MaxObjectBytes <= 0 || l.MaxObjectBytes > 1<<40 || l.MaxConcurrentUploads <= 0 || l.MaxConcurrentUploads > 1024 {
		return errors.New("invalid upload limits")
	}
	return nil
}
