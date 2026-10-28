package cluster

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

type Verification struct {
	Status                              string `json:"status"`
	Namespaces, Buckets, Objects, Bytes int64
}

// Verify checks the current database references against S3 contents. It is not
// an archive: run with writes and garbage collection stopped for a stable audit.
func (s *Store) Verify(ctx context.Context) (v Verification, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM ts_namespaces ORDER BY id`)
	if err != nil {
		return v, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return v, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	for _, name := range names {
		v.Namespaces++
		r := s.Namespace(name)
		buckets, e := r.ListBuckets(ctx)
		if e != nil {
			return v, e
		}
		var bytes, objects int64
		for _, b := range buckets {
			v.Buckets++
			opts := objectstore.ListOptions{Limit: 1000}
			for {
				page, e := r.ListObjects(ctx, b.ID, opts)
				if e != nil {
					return v, e
				}
				for _, o := range page.Objects {
					d, e := r.OpenObject(ctx, o.ID)
					if e != nil {
						return v, e
					}
					h := sha256.New()
					n, e := io.Copy(h, d.Content)
					e = errors.Join(e, d.Content.Close())
					if e != nil {
						return v, e
					}
					if n != o.Size || hex.EncodeToString(h.Sum(nil)) != o.SHA256 {
						return v, errors.New("S3 object checksum mismatch")
					}
					bytes += n
					objects++
				}
				if !page.HasMore {
					break
				}
				opts.After = page.NextAfter
			}
		}
		usage, e := r.Usage(ctx)
		if e != nil {
			return v, e
		}
		if usage.StoredBytes != bytes || usage.Objects != objects {
			return v, errors.New("namespace counters differ from object inventory")
		}
		v.Objects += objects
		v.Bytes += bytes
	}
	v.Status = "verified"
	return v, nil
}
