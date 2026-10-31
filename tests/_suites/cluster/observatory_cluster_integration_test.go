//go:build clusterintegration

package main

import (
	"A3S/internal/httpapi"
	"A3S/internal/observatory"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestClusterObservatoryReplicaAggregationAndStale(t *testing.T) {
	a, b, _, users := clusterFixture(t, 0)
	ctx := context.Background()
	m1, e := observatory.New("cluster", a)
	if e != nil {
		t.Fatal(e)
	}
	m2, e := observatory.New("cluster", b)
	if e != nil {
		t.Fatal(e)
	}
	h, e := httpapi.New(nil, httpapi.Options{Authority: clusterAuthority{a}, Observatory: m1, RequestTimeout: time.Minute, MaxObjectBytes: a.MaxObjectBytes})
	if e != nil {
		t.Fatal(e)
	}
	requireStatus(t, request(h, "PUT", "/observatory-bucket", "", testToken), 200)
	requireStatus(t, request(h, "PUT", "/observatory-bucket/private-file", "payload", testToken), 200)
	m1.Sample(ctx)
	m2.Sample(ctx)
	m1.Sample(ctx)
	w := request(h, "GET", "/-/observatory/state", "", testToken)
	requireStatus(t, w, 200)
	var r observatory.Report
	if e = json.Unmarshal(w.Body.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	if !r.Registry || len(r.Nodes) != 2 || r.Storage.Bytes != 7 || r.Storage.Objects != 1 || r.Database.State != "healthy" || r.ObjectStore.State != "healthy" {
		t.Fatalf("%+v", r)
	}
	id := m2.Local().ID
	if _, e = a.Pool.Exec(ctx, `UPDATE ts_observatory_nodes SET seen_at=clock_timestamp()-interval '20 seconds' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	m1.Sample(ctx)
	r = m1.Snapshot()
	found := false
	for _, n := range r.Nodes {
		if n.ID == id {
			found = true
			if n.State != "stale" || n.RPS != 0 || n.Connections != 0 {
				t.Fatalf("stale peer %+v", n)
			}
		}
	}
	if !found {
		t.Fatal("offline replica disappeared immediately")
	}
	m2.Sample(ctx)
	m1.Sample(ctx)
	for _, n := range m1.Snapshot().Nodes {
		if n.ID == id && n.State != "healthy" {
			t.Fatal("heartbeat did not recover")
		}
	}
	requireStatus(t, request(h, "GET", "/-/observatory/state", "", aliceToken), 403)
	// Older initialized clusters remain readable and report a migration requirement.
	if _, e = a.Pool.Exec(ctx, `DROP TABLE ts_observatory_nodes`); e != nil {
		t.Fatal(e)
	}
	m1.Sample(ctx)
	if m1.Snapshot().Warning != "registry_migration_required" {
		t.Fatal("missing migration not reported")
	}
	if e = a.EnableObservatory(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.EnableObservatory(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	m1.Sample(ctx)
	if !m1.Snapshot().Registry {
		t.Fatal("registry not restored")
	}
	users = append(users, identity("secondoperator", strings.Repeat("f", 40), "admin"))
	users[0].Disabled = true
	if e = b.SyncUsers(ctx, users); e != nil {
		t.Fatal(e)
	}
	requireStatus(t, request(h, "GET", "/-/observatory/state", "", testToken), 401)
	a.Pool.Close()
	requireStatus(t, request(h, "GET", "/-/observatory/state", "", testToken), 503)
}

func TestClusterObservatoryS3Unavailable(t *testing.T) {
	a, _, _, _ := clusterFixture(t, 0)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	cfg, e := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"), awsconfig.WithRetryMaxAttempts(1))
	if e != nil {
		t.Fatal(e)
	}
	a.S3 = s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(dead.URL); o.UsePathStyle = true })
	m, e := observatory.New("cluster", a)
	if e != nil {
		t.Fatal(e)
	}
	m.Sample(context.Background())
	r := m.Snapshot()
	if r.Database.State != "healthy" || r.ObjectStore.State != "unavailable" || !r.Registry || r.Nodes[0].State != "degraded" {
		t.Fatalf("dependency failure hidden: %+v", r)
	}
}
