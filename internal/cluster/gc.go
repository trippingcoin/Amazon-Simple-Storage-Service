package cluster

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Collect removes at most limit unreferenced blobs older than 48 hours. DB state
// fences late upload publication. The retention exceeds the 24h reader lifetime.
func (s *Store) Collect(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 10000 {
		limit = 100
	}
	rows, e := s.Pool.Query(ctx, `UPDATE ts_blobs SET state='deleting' WHERE key IN (
 SELECT key FROM ts_blobs b WHERE (state='deleting' OR (state='pending' AND created_at<clock_timestamp()-interval '48 hours') OR (state='retired' AND retired_at<clock_timestamp()-interval '48 hours'))
 AND NOT EXISTS(SELECT 1 FROM ts_objects o WHERE o.blob=b.key) ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING key`, limit)
	if e != nil {
		return 0, e
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if e = rows.Scan(&key); e != nil {
			rows.Close()
			return 0, e
		}
		keys = append(keys, key)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	removed := 0
	for _, key := range keys {
		if _, e = s.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.config.Bucket), Key: aws.String(key)}); e != nil {
			return removed, e
		}
		if _, e = s.Pool.Exec(ctx, `DELETE FROM ts_blobs WHERE key=$1 AND state='deleting'`, key); e != nil {
			return removed, e
		}
		removed++
	}
	return removed, nil
}
