package storage

import (
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
)

// OpenNamespaces runs while the legacy store owns the top-level process lock.
// Every existing namespace is recovered, including accounts removed from the
// credential registry. Revocation never deletes stored data.
func (s *Store) OpenNamespaces(names []string, opts Options) (map[string]*Store, error) {
	const dir = privateDir + "/tenants"
	valid := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, name := range names {
		if !valid.MatchString(name) {
			return nil, errors.New("invalid namespace")
		}
	}
	if err := s.root.Mkdir(dir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := s.root.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe tenant directory")
	}
	if err = s.syncDir(privateDir); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err = s.root.Mkdir(dir+"/"+name, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	if err = s.syncDir(dir); err != nil {
		return nil, err
	}
	entries, err := readDir(s.root, dir)
	if err != nil {
		return nil, err
	}
	stores := map[string]*Store{}
	ok := false
	defer func() {
		if !ok {
			for _, store := range stores {
				_ = store.Close()
			}
		}
	}()
	for _, entry := range entries {
		info, e := s.root.Lstat(dir + "/" + entry.Name())
		if e != nil || !valid.MatchString(entry.Name()) || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("unsafe tenant namespace")
		}
		store, e := Open(filepath.Join(s.root.Name(), dir, entry.Name()), opts)
		if e != nil {
			return nil, e
		}
		stores[entry.Name()] = store
	}
	ok = true
	return stores, nil
}
