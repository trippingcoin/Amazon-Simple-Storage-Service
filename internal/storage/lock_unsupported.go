//go:build !linux && !darwin

package storage

import (
	"errors"
	"os"
)

func lockFile(*os.File) error { return errors.New("storage currently supports Linux and macOS only") }
