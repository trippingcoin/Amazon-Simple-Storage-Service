package cluster

import (
	"A3S/internal/objectstore"
	"A3S/internal/service"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Repository struct {
	s         *Store
	namespace string
}

var _ service.Repository = (*Repository)(nil)

func (s *Store) Namespace(id string) *Repository { return &Repository{s: s, namespace: id} }
func (r *Repository) lock(ctx context.Context, tx pgx.Tx) (bytes, objects, maxBytes, maxObjects int64, err error) {
	err = tx.QueryRow(ctx, `SELECT stored_bytes,object_count,max_bytes,max_objects FROM ts_namespaces WHERE id=$1 FOR UPDATE`, r.namespace).Scan(&bytes, &objects, &maxBytes, &maxObjects)
	err = sqlError(err)
	return
}
func (r *Repository) Usage(ctx context.Context) (u objectstore.Usage, err error) {
	err = r.s.Pool.QueryRow(ctx, `SELECT stored_bytes,object_count,max_bytes,max_objects,(SELECT count(*) FROM ts_buckets WHERE namespace=$1) FROM ts_namespaces WHERE id=$1`, r.namespace).Scan(&u.StoredBytes, &u.Objects, &u.MaxStorageBytes, &u.MaxObjects, &u.Buckets)
	return u, sqlError(err)
}
func (r *Repository) ListBuckets(ctx context.Context) ([]objectstore.Bucket, error) {
	rows, e := r.s.Pool.Query(ctx, `SELECT b.name,b.created_at,b.modified_at,NOT EXISTS(SELECT 1 FROM ts_objects o WHERE o.namespace=b.namespace AND o.bucket=b.name) FROM ts_buckets b WHERE b.namespace=$1 ORDER BY b.name`, r.namespace)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []objectstore.Bucket{}
	for rows.Next() {
		var b objectstore.Bucket
		if e = rows.Scan(&b.ID, &b.CreationTime, &b.LastModified, &b.Empty); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (r *Repository) CreateBucket(ctx context.Context, id objectstore.BucketID) error {
	if e := id.Validate(); e != nil {
		return e
	}
	tx, e := r.s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, _, _, _, e = r.lock(ctx, tx); e != nil {
		return e
	}
	gen, e := randomID()
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO ts_buckets(namespace,name,generation) VALUES($1,$2,$3)`, r.namespace, string(id), gen)
	var pe *pgconn.PgError
	if errors.As(e, &pe) && pe.Code == "23505" {
		return objectstore.ErrExists
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (r *Repository) DeleteBucket(ctx context.Context, id objectstore.BucketID) error {
	if e := id.Validate(); e != nil {
		return e
	}
	tx, e := r.s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, _, _, _, e = r.lock(ctx, tx); e != nil {
		return e
	}
	var has bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ts_objects WHERE namespace=$1 AND bucket=$2)`, r.namespace, string(id)).Scan(&has); e != nil {
		return e
	}
	if has {
		return objectstore.ErrNotEmpty
	}
	tag, e := tx.Exec(ctx, `DELETE FROM ts_buckets WHERE namespace=$1 AND name=$2`, r.namespace, string(id))
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return objectstore.ErrNotFound
	}
	return tx.Commit(ctx)
}
func (r *Repository) ListObjects(ctx context.Context, bucket objectstore.BucketID, opts objectstore.ListOptions) (page objectstore.ObjectPage, err error) {
	if err = bucket.Validate(); err != nil {
		return
	}
	if err = opts.Validate(); err != nil {
		return
	}
	// Repeatable-read keeps bucket existence and its page in one snapshot.
	tx, err := r.s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer rollback(tx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ts_buckets WHERE namespace=$1 AND name=$2)`, r.namespace, string(bucket)).Scan(&exists)
	if err != nil {
		return page, err
	}
	if !exists {
		return page, objectstore.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT key,size,content_type,sha256,modified_at FROM ts_objects WHERE namespace=$1 AND bucket=$2 AND key>$3 AND starts_with(key,$4) ORDER BY key LIMIT $5`, r.namespace, string(bucket), string(opts.After), opts.Prefix, opts.Limit+1)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		o := objectstore.Object{ID: objectstore.ObjectID{Bucket: bucket}}
		if err = rows.Scan(&o.ID.Key, &o.Size, &o.ContentType, &o.SHA256, &o.LastModified); err != nil {
			rows.Close()
			return page, err
		}
		page.Objects = append(page.Objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Objects) > opts.Limit {
		page.HasMore = true
		page.Objects = page.Objects[:opts.Limit]
		page.NextAfter = page.Objects[len(page.Objects)-1].ID.Key
	}
	return page, tx.Commit(ctx)
}
func (r *Repository) object(ctx context.Context, id objectstore.ObjectID) (o objectstore.Object, blob string, err error) {
	o.ID = id
	if err = id.Validate(); err != nil {
		return
	}
	err = r.s.Pool.QueryRow(ctx, `SELECT size,content_type,sha256,modified_at,blob FROM ts_objects WHERE namespace=$1 AND bucket=$2 AND key=$3`, r.namespace, string(id.Bucket), string(id.Key)).Scan(&o.Size, &o.ContentType, &o.SHA256, &o.LastModified, &blob)
	err = sqlError(err)
	return
}
func (r *Repository) GetObject(ctx context.Context, id objectstore.ObjectID) (objectstore.Object, error) {
	o, _, e := r.object(ctx, id)
	return o, e
}

type ctxReader struct {
	ctx context.Context
	io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.Reader.Read(p)
}
func (r *Repository) PutObject(ctx context.Context, id objectstore.ObjectID, body io.Reader) error {
	return r.put(ctx, id, body, false, nil)
}
func (r *Repository) put(ctx context.Context, id objectstore.ObjectID, body io.Reader, createOnly bool, expected *objectstore.Object) error {
	if e := id.Validate(); e != nil {
		return e
	}
	if body == nil {
		return objectstore.ErrInvalid
	}
	var generation string
	if e := r.s.Pool.QueryRow(ctx, `SELECT generation FROM ts_buckets WHERE namespace=$1 AND name=$2`, r.namespace, string(id.Bucket)).Scan(&generation); e != nil {
		return sqlError(e)
	}
	// Private local scratch is ephemeral, never the source of published state.
	f, e := os.CreateTemp(r.s.config.TempDir, "triple-s-upload-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(ctxReader{ctx, body}, r.s.MaxObjectBytes+1))
	if e != nil {
		return e
	}
	if n > r.s.MaxObjectBytes {
		return objectstore.ErrTooLarge
	}
	sample := make([]byte, 512)
	count, e := f.ReadAt(sample, 0)
	if e != nil && e != io.EOF {
		return e
	}
	contentType := "application/octet-stream"
	if count > 0 {
		contentType = http.DetectContentType(sample[:count])
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	if expected != nil && (n != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256) {
		return errors.New("source object checksum mismatch")
	}

	// Preflight prevents repeatedly uploading blobs for an already-full namespace.
	// The authoritative check still occurs under the publication row lock.
	var preBytes, preObjects, preMaxBytes, preMaxObjects, preOldSize int64
	var preExists bool
	e = r.s.Pool.QueryRow(ctx, `SELECT ns.stored_bytes,ns.object_count,ns.max_bytes,ns.max_objects,COALESCE(o.size,0),o.key IS NOT NULL FROM ts_namespaces ns LEFT JOIN ts_objects o ON o.namespace=ns.id AND o.bucket=$2 AND o.key=$3 WHERE ns.id=$1`, r.namespace, string(id.Bucket), string(id.Key)).Scan(&preBytes, &preObjects, &preMaxBytes, &preMaxObjects, &preOldSize, &preExists)
	if e != nil {
		return sqlError(e)
	}
	if createOnly && preExists {
		return objectstore.ErrExists
	}
	if e = checkQuota(preBytes, preObjects, preMaxBytes, preMaxObjects, preOldSize, n, !preExists); e != nil {
		return e
	}
	nonce, e := randomID()
	if e != nil {
		return e
	}
	blob := r.s.Prefix + "blobs/" + nonce
	if _, e = r.s.Pool.Exec(ctx, `INSERT INTO ts_blobs(key,state) VALUES($1,'pending')`, blob); e != nil {
		return e
	}
	_, e = r.s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String(r.s.config.Bucket), Key: aws.String(blob), Body: f, ContentType: aws.String(contentType)})
	if e != nil {
		return e
	}
	tx, e := r.s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	used, num, maxBytes, maxObjects, e := r.lock(ctx, tx)
	if e != nil {
		return e
	}
	var current string
	if e = tx.QueryRow(ctx, `SELECT generation FROM ts_buckets WHERE namespace=$1 AND name=$2`, r.namespace, string(id.Bucket)).Scan(&current); e != nil {
		return sqlError(e)
	}
	if current != generation {
		return objectstore.ErrNotFound
	}
	var state string
	if e = tx.QueryRow(ctx, `SELECT state FROM ts_blobs WHERE key=$1 FOR UPDATE`, blob).Scan(&state); e != nil {
		return e
	}
	if state != "pending" {
		return errors.New("upload is no longer publishable")
	}
	var old string
	var oldSize int64
	e = tx.QueryRow(ctx, `SELECT blob,size FROM ts_objects WHERE namespace=$1 AND bucket=$2 AND key=$3`, r.namespace, string(id.Bucket), string(id.Key)).Scan(&old, &oldSize)
	fresh := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !fresh {
		return e
	}
	if createOnly && !fresh {
		return objectstore.ErrExists
	}
	if e = checkQuota(used, num, maxBytes, maxObjects, oldSize, n, fresh); e != nil {
		return e
	}
	next := used - oldSize + n
	if fresh {
		num++
	}
	if old != "" {
		if _, e = tx.Exec(ctx, `UPDATE ts_blobs SET state='retired',retired_at=clock_timestamp() WHERE key=$1`, old); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE ts_blobs SET state='live' WHERE key=$1`, blob); e != nil {
		return e
	}
	modified := time.Now().UTC()
	if expected != nil {
		modified = expected.LastModified
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ts_objects(namespace,bucket,key,blob,size,content_type,sha256,modified_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(namespace,bucket,key) DO UPDATE SET blob=EXCLUDED.blob,size=EXCLUDED.size,content_type=EXCLUDED.content_type,sha256=EXCLUDED.sha256,modified_at=EXCLUDED.modified_at`, r.namespace, string(id.Bucket), string(id.Key), blob, n, contentType, hex.EncodeToString(hash.Sum(nil)), modified); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ts_namespaces SET stored_bytes=$2,object_count=$3 WHERE id=$1`, r.namespace, next, num); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ts_buckets SET modified_at=clock_timestamp() WHERE namespace=$1 AND name=$2`, r.namespace, string(id.Bucket)); e != nil {
		return e
	}
	// Never delete a blob on an ambiguous Commit error: SQL may already reference it.
	return tx.Commit(ctx)
}
func (r *Repository) DeleteObject(ctx context.Context, id objectstore.ObjectID) error {
	if e := id.Validate(); e != nil {
		return e
	}
	tx, e := r.s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, _, _, _, e = r.lock(ctx, tx); e != nil {
		return e
	}
	var blob string
	var size int64
	if e = tx.QueryRow(ctx, `DELETE FROM ts_objects WHERE namespace=$1 AND bucket=$2 AND key=$3 RETURNING blob,size`, r.namespace, string(id.Bucket), string(id.Key)).Scan(&blob, &size); e != nil {
		return sqlError(e)
	}
	if _, e = tx.Exec(ctx, `UPDATE ts_blobs SET state='retired',retired_at=clock_timestamp() WHERE key=$1`, blob); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ts_namespaces SET stored_bytes=stored_bytes-$2,object_count=object_count-1 WHERE id=$1`, r.namespace, size); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ts_buckets SET modified_at=clock_timestamp() WHERE namespace=$1 AND name=$2`, r.namespace, string(id.Bucket)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// A finite download lifetime bounds the safe garbage-collection retention.
func (r *Repository) OpenObject(ctx context.Context, id objectstore.ObjectID) (objectstore.Download, error) {
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	o, blob, e := r.object(ctx, id)
	if e != nil {
		cancel()
		return objectstore.Download{}, e
	}
	reader := &remoteReader{ctx: ctx, cancel: cancel, s: r.s, key: blob, size: o.Size}
	// Surface missing blobs / AccessDenied before sending response headers.
	if e = reader.open(); e != nil {
		reader.Close()
		return objectstore.Download{}, e
	}
	return objectstore.Download{Object: o, Content: reader}, nil
}

func checkQuota(used, num, maxBytes, maxObjects, oldSize, size int64, fresh bool) error {
	if size > math.MaxInt64-(used-oldSize) {
		return objectstore.ErrQuotaExceeded
	}
	next := used - oldSize + size
	if (maxBytes > 0 && next > maxBytes && next > used) || (fresh && maxObjects > 0 && num >= maxObjects) {
		return objectstore.ErrQuotaExceeded
	}
	return nil
}
