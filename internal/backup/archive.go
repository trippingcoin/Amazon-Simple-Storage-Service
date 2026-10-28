package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
)

func archive(ctx context.Context, source, destination string) (string, int64, error) {
	root, err := os.OpenRoot(source)
	if err != nil {
		return "", 0, err
	}
	defer root.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(file, hash))
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if name == "." {
			return nil
		}
		info, e := root.Lstat(name)
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("backup refuses non-regular storage entries")
		}
		h := &tar.Header{Name: name, ModTime: info.ModTime(), Format: tar.FormatPAX, Typeflag: tar.TypeReg, Mode: 0600, Size: info.Size()}
		if info.IsDir() {
			h.Typeflag = tar.TypeDir
			h.Mode = 0700
			h.Size = 0
		}
		if e = writer.WriteHeader(h); e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		f, e := regular(root, name)
		if e != nil {
			return e
		}
		n, e := io.Copy(writer, contextReader{ctx, f})
		e = errors.Join(e, f.Close())
		if e != nil {
			return e
		}
		if n != info.Size() {
			return errors.New("source changed while archiving")
		}
		return nil
	})
	err = errors.Join(err, writer.Close(), file.Sync())
	if err != nil {
		return "", 0, err
	}
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if err = file.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

func extract(ctx context.Context, reader io.Reader, destination string, maxBytes int64) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(contextReader{ctx, reader})
	type dirTime struct {
		name     string
		modified time.Time
	}
	dirs := []dirTime{}
	seen := map[string]bool{}
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		h, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
		name := h.Name
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, "\\") || seen[name] || len(seen) >= 1000000 || h.Linkname != "" {
			return errors.New("unsafe or duplicate archive path")
		}
		seen[name] = true
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Size != 0 {
				return errors.New("invalid directory size")
			}
			if e = root.Mkdir(name, 0700); e != nil {
				return e
			}
			dirs = append(dirs, dirTime{name, h.ModTime})
		case tar.TypeReg:
			if h.Size < 0 || h.Size > maxBytes-total {
				return errors.New("archive extraction size limit exceeded")
			}
			total += h.Size
			f, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			n, e := io.Copy(f, contextReader{ctx, tr})
			e = errors.Join(e, root.Chtimes(name, h.ModTime, h.ModTime), f.Sync(), f.Close())
			if e != nil {
				return e
			}
			if n != h.Size {
				return errors.New("truncated archive entry")
			}
		default:
			return errors.New("archive entry type is not supported")
		}
	}
	// Children change parent mtime. Restore directories last, deepest first.
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err = root.Chtimes(d.name, d.modified, d.modified); err != nil {
			return err
		}
		f, e := root.Open(d.name)
		if e != nil {
			return e
		}
		if e = errors.Join(f.Sync(), f.Close()); e != nil {
			return e
		}
	}
	return syncDir(destination)
}
