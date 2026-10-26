package storage

import (
	"errors"
	"io"
	"net/http"
)

// detectContentType uses only the bytes actually present in the first 512 bytes.
// ReaderAt keeps the file offset unchanged and is shared by upload and recovery.
// Empty objects retain the existing application/octet-stream policy.
func detectContentType(r io.ReaderAt) (string, error) {
	var prefix [512]byte
	n, err := r.ReadAt(prefix[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n == 0 {
		return "application/octet-stream", nil
	}
	return http.DetectContentType(prefix[:n]), nil
}
