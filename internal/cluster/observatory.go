package cluster

import (
	"A3S/internal/observatory"
	"context"
	_ "embed"
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

//go:embed observatory.sql
var observatorySchema string

// EnableObservatory is an explicit, idempotent operator migration; API roles need no DDL.
func (s *Store) EnableObservatory(ctx context.Context) error {
	_, e := s.Pool.Exec(ctx, observatorySchema)
	return e
}

func (s *Store) Sample(ctx context.Context, n observatory.Node) (observatory.Report, error) {
	r := observatory.Report{Nodes: []observatory.Node{n}, Database: observatory.Dependency{State: "unknown"}, ObjectStore: observatory.Dependency{State: "unknown"}}
	start := time.Now()
	var writable bool
	e := s.Pool.QueryRow(ctx, `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'`).Scan(&writable)
	r.Database.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if e != nil || !writable {
		r.Nodes[0].State = "degraded"
		r.Database.State = "unavailable"
		r.Warning = "database_unavailable"
		return r, nil
	}
	r.Database.State = "healthy"
	probe, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	start = time.Now()
	_, e = s.S3.HeadBucket(probe, &s3.HeadBucketInput{Bucket: aws.String(s.config.Bucket)})
	cancel()
	r.ObjectStore.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if e != nil {
		r.ObjectStore.State = "unavailable"
		r.Nodes[0].State = "degraded"
	} else {
		r.ObjectStore.State = "healthy"
	}
	// These are logical published bytes, never an estimate of provider disk capacity.
	e = s.Pool.QueryRow(ctx, `SELECT COALESCE(sum(stored_bytes),0),COALESCE(sum(object_count),0),(SELECT count(*) FROM ts_buckets),(SELECT count(*) FROM ts_blobs WHERE state='pending'),(SELECT count(*) FROM ts_blobs WHERE state='retired'),(SELECT count(*) FROM ts_blobs WHERE state='deleting') FROM ts_namespaces`).Scan(&r.Storage.Bytes, &r.Storage.Objects, &r.Storage.Buckets, &r.Storage.Pending, &r.Storage.Retired, &r.Storage.Deleting)
	if e == nil {
		r.Storage.Available = true
	}
	var exists bool
	if e = s.Pool.QueryRow(ctx, `SELECT to_regclass('ts_observatory_nodes') IS NOT NULL`).Scan(&exists); e != nil || !exists {
		r.Warning = "registry_migration_required"
		return r, nil
	}
	n.State = "healthy"
	if r.ObjectStore.State != "healthy" {
		n.State = "degraded"
	}
	data, e := json.Marshal(n)
	if e != nil {
		return r, e
	}
	if _, e = s.Pool.Exec(ctx, `INSERT INTO ts_observatory_nodes(id,payload) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload,seen_at=clock_timestamp()`, n.ID, data); e != nil {
		r.Warning = "registry_unavailable"
		return r, nil
	}
	// Retain offline instances for an hour, bounded to 64 displayed entries. Never
	// evict a healthy peer when a dead node is left over after a restart.
	_, _ = s.Pool.Exec(ctx, `DELETE FROM ts_observatory_nodes WHERE seen_at<clock_timestamp()-interval '1 hour'`)
	rows, e := s.Pool.Query(ctx, `SELECT payload,GREATEST(0,extract(epoch FROM clock_timestamp()-seen_at))::double precision FROM ts_observatory_nodes WHERE seen_at>clock_timestamp()-interval '1 hour' ORDER BY seen_at DESC LIMIT 64`)
	if e != nil {
		r.Warning = "registry_unavailable"
		return r, nil
	}
	defer rows.Close()
	list := []observatory.Node{}
	for rows.Next() {
		var data []byte
		var age float64
		if e = rows.Scan(&data, &age); e != nil {
			return r, e
		}
		var peer observatory.Node
		if e = json.Unmarshal(data, &peer); e != nil {
			return r, e
		}
		peer.AgeSeconds = age
		if age > 15 {
			peer.State = "stale"
			peer.RPS = 0
			peer.Inflight = 0
			peer.Connections = 0
		}
		list = append(list, peer)
	}
	if e = rows.Err(); e != nil {
		return r, e
	}
	r.Nodes = list
	r.Registry = true
	return r, nil
}
