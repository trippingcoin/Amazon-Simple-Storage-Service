//go:build darwin

package storage

import (
	"errors"
	"os"
	"syscall"
)

func identifyFile(info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, errors.New("file identity unavailable")
	}
	return fileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), ChangedSeconds: int64(stat.Ctimespec.Sec), ChangedNanos: int64(stat.Ctimespec.Nsec)}, nil
}
