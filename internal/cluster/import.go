package cluster

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"A3S/internal/storage"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type ImportResult struct{ Copied, Skipped int64 }

// ImportLocal is an explicit, restartable, create-only migration. Source locks
// prevent concurrent local writers; operators must quiesce destination writes
// until verification and cutover so clients do not observe a partial migration.
func (s *Store) ImportLocal(ctx context.Context, dir string) (result ImportResult, err error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return result, errors.New("existing local directory required")
	}
	source, err := storage.Open(dir, objectstore.Limits{MaxObjectBytes: 1 << 40, MaxConcurrentUploads: 1})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	sources := map[string]*storage.Store{"": source}
	if _, e := os.Lstat(filepath.Join(dir, ".triple-s", "tenants")); e == nil {
		tenants, e := source.OpenNamespaces(nil, objectstore.Limits{MaxObjectBytes: 1 << 40, MaxConcurrentUploads: 1})
		if e != nil {
			return result, e
		}
		defer func() {
			for _, ns := range tenants {
				err = errors.Join(err, ns.Close())
			}
		}()
		ids := map[string]string{}
		rows, e := s.Pool.Query(ctx, `SELECT id FROM ts_namespaces WHERE id<>''`)
		if e != nil {
			return result, e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return result, e
			}
			ids[access.Namespace(id)] = id
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return result, e
		}
		for hash, ns := range tenants {
			id, exists := ids[hash]
			if !exists {
				return result, errors.New("source namespace has no registered permanent user ID; import original IDs first")
			}
			sources[id] = ns
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, e
	}
	names := []string{}
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		src, repo := sources[name], s.Namespace(name)
		buckets, e := src.ListBuckets(ctx)
		if e != nil {
			return result, e
		}
		for _, b := range buckets {
			generation, e := randomID()
			if e != nil {
				return result, e
			}
			if _, e = s.Pool.Exec(ctx, `INSERT INTO ts_buckets(namespace,name,generation,created_at,modified_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(namespace,name) DO NOTHING`, name, string(b.ID), generation, b.CreationTime, b.LastModified); e != nil {
				return result, e
			}
			keys, e := src.ObjectKeys(ctx, b.ID)
			if e != nil {
				return result, e
			}
			for _, key := range keys {
				id := objectstore.ObjectID{Bucket: b.ID, Key: key}
				d, e := src.OpenObject(ctx, id)
				if e != nil {
					return result, e
				}
				existing, getErr := repo.GetObject(ctx, id)
				if getErr == nil {
					d.Content.Close()
					if existing.SHA256 != d.Object.SHA256 || existing.Size != d.Object.Size {
						return result, errors.New("destination object differs; refusing overwrite")
					}
					result.Skipped++
					continue
				}
				if !errors.Is(getErr, objectstore.ErrNotFound) {
					d.Content.Close()
					return result, getErr
				}
				e = repo.put(ctx, id, d.Content, true, &d.Object)
				e = errors.Join(e, d.Content.Close())
				if e != nil {
					return result, fmt.Errorf("import stopped without overwriting existing objects: %w", e)
				}
				result.Copied++
			}
		}
	}
	return result, nil
}
