//go:build !linux && !darwin

package storage

import (
	"errors"
	"os"
)

func identifyFile(os.FileInfo) (fileIdentity, error) {
	return fileIdentity{}, errors.New("file identity unsupported on this platform")
}
