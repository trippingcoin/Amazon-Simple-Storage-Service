//go:build clusterintegration

package main

import (
	"A3S/internal/access"
	"A3S/internal/cluster"
	"A3S/internal/config"
	"A3S/internal/httpapi"
	"A3S/internal/objectstore"
	"A3S/internal/storage"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/jackc/pgx/v5"
)

func clusterFixture(t *testing.T, maxObjects int64) (*cluster.Store, *cluster.Store, cluster.Config, []access.User) {
	t.Helper()
	dsn, endpoint := os.Getenv("TRIPLE_S_TEST_DATABASE_URL"), os.Getenv("TRIPLE_S_TEST_S3_ENDPOINT")
	if dsn == "" || endpoint == "" {
		t.Skip("set isolated local PostgreSQL and MinIO test endpoints")
	}
	u, e := url.Parse(endpoint)
	if e != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("test S3 endpoint must be loopback")
	}
	dbURL, e := url.Parse(dsn)
	if e != nil || (dbURL.Hostname() != "127.0.0.1" && dbURL.Hostname() != "localhost") {
		t.Fatal("test database must be loopback")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "triples-local-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "triples-local-test-password-only")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	ctx := context.Background()
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	var random [12]byte
	if _, e = rand.Read(random[:]); e != nil {
		t.Fatal(e)
	}
	schema := "test_" + hex.EncodeToString(random[:])
	if _, e = conn.Exec(ctx, `CREATE SCHEMA `+schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, e := conn.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		if e != nil {
			t.Error(e)
		}
		conn.Close(context.Background())
	})
	q := dbURL.Query()
	q.Set("search_path", schema)
	dbURL.RawQuery = q.Encode()
	cfg := cluster.Config{DSN: dbURL.String(), Bucket: "triple-s-cluster-test", Region: "us-east-1", Endpoint: endpoint, AllowInsecure: true, TempDir: t.TempDir()}
	sdk, e := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if e != nil {
		t.Fatal(e)
	}
	client := s3.NewFromConfig(sdk, func(o *s3.Options) { o.BaseEndpoint = aws.String(endpoint); o.UsePathStyle = true })
	if _, e = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}); e != nil {
		var api smithy.APIError
		if !errors.As(e, &api) || (api.ErrorCode() != "BucketAlreadyOwnedByYou" && api.ErrorCode() != "BucketAlreadyExists") {
			t.Fatal(e)
		}
	}
	users := []access.User{identity("operator", testToken, "admin"), identity("alice", aliceToken, "user"), identity("bob", bobToken, "user")}
	users[2].Grants = []access.Grant{{Owner: "alice", Bucket: "shared", Permission: "read"}}
	limits := objectstore.Limits{MaxObjectBytes: 20 << 20, MaxConcurrentUploads: 4, MaxObjects: maxObjects}
	if e = cluster.Initialize(ctx, cfg, limits, users); e != nil {
		t.Fatal(e)
	}
	a, e := cluster.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	b, e := cluster.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(b.Close)
	return a, b, cfg, users
}
func clusterAPI(t *testing.T, s *cluster.Store) http.Handler {
	t.Helper()
	h, e := httpapi.New(nil, httpapi.Options{Authority: clusterAuthority{s}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RequestTimeout: 30 * time.Second, MaxObjectBytes: s.MaxObjectBytes, MaxConcurrentUploads: 8, MaxConcurrentDownloads: 8})
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func TestClusterReplicaVisibilityAndPermissions(t *testing.T) {
	a, b, _, users := clusterFixture(t, 0)
	h1, h2 := clusterAPI(t, a), clusterAPI(t, b)
	requireStatus(t, scoped(h1, "PUT", "/shared", "", aliceToken), 200)
	requireStatus(t, scoped(h1, "PUT", "/shared/file", "original", aliceToken), 200)
	w := scoped(h2, "GET", "/shared/file", "", aliceToken)
	requireStatus(t, w, 200)
	if w.Body.String() != "original" {
		t.Fatal("replica missing write")
	}
	requireStatus(t, scoped(h2, "GET", "/shared/file", "", bobToken, "alice"), 200)
	requireStatus(t, scoped(h2, "PUT", "/shared/file", "denied", bobToken, "alice"), 403)
	requireStatus(t, scoped(h2, "GET", "/-/metrics", "", bobToken), 403)
	// No process restart: revocation is read from the shared authority.
	users[2].Grants = nil
	if e := a.SyncUsers(context.Background(), users); e != nil {
		t.Fatal(e)
	}
	requireStatus(t, scoped(h2, "GET", "/shared/file", "", bobToken, "alice"), 403)
	users[1].Disabled = true
	if e := a.SyncUsers(context.Background(), users); e != nil {
		t.Fatal(e)
	}
	requireStatus(t, scoped(h1, "GET", "/shared/file", "", aliceToken), 401)
	requireStatus(t, scoped(h2, "GET", "/shared/file", "", aliceToken), 401)
	requireStatus(t, scoped(h2, "GET", "/shared/file", "", testToken, "alice"), 200)
}
func TestClusterConcurrentQuotaAndPinnedDownload(t *testing.T) {
	a, b, _, _ := clusterFixture(t, 1)
	ctx := context.Background()
	r1, r2 := a.Namespace("alice"), b.Namespace("alice")
	if e := r1.CreateBucket(ctx, "shared"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := r1
			if i%2 == 0 {
				r = r2
			}
			results <- r.PutObject(ctx, objectstore.ObjectID{Bucket: "shared", Key: objectstore.ObjectKey(fmt.Sprintf("file-%02d", i))}, strings.NewReader("payload"))
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, objectstore.ErrQuotaExceeded) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("quota overshoot: %d", success)
	}
	var before, after int
	if e := a.Pool.QueryRow(ctx, `SELECT count(*) FROM ts_blobs`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e := r2.PutObject(ctx, objectstore.ObjectID{Bucket: "shared", Key: "over-quota"}, strings.NewReader("reject")); !errors.Is(e, objectstore.ErrQuotaExceeded) {
		t.Fatal(e)
	}
	if e := a.Pool.QueryRow(ctx, `SELECT count(*) FROM ts_blobs`).Scan(&after); e != nil || before != after {
		t.Fatal("full quota created new S3 work", e)
	}

	page, e := r2.ListObjects(ctx, "shared", objectstore.ListOptions{Limit: 100})
	if e != nil || len(page.Objects) != 1 {
		t.Fatal(e)
	}
	id := page.Objects[0].ID
	old, e := r1.OpenObject(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	defer old.Content.Close()
	if e = r2.PutObject(ctx, id, strings.NewReader("replacement")); e != nil {
		t.Fatal(e)
	}
	if _, e = old.Content.Seek(0, io.SeekEnd); e != nil {
		t.Fatal(e)
	}
	if _, e = old.Content.Seek(0, io.SeekStart); e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(old.Content)
	if e != nil || string(data) != "payload" {
		t.Fatalf("pinned download changed: %q %v", data, e)
	}
	u, e := r1.Usage(ctx)
	if e != nil || u.Objects != 1 || u.StoredBytes != 11 {
		t.Fatalf("usage: %+v %v", u, e)
	}
	if e = r1.DeleteObject(ctx, id); e != nil {
		t.Fatal(e)
	}
	if _, e = r2.GetObject(ctx, id); !errors.Is(e, objectstore.ErrNotFound) {
		t.Fatal(e)
	}
	// Retired/pending blobs remain protected during the download retention window.
	if n, e := a.Collect(ctx, 100); e != nil || n != 0 {
		t.Fatalf("early collection: %d %v", n, e)
	}
	if _, e = a.Pool.Exec(ctx, `UPDATE ts_blobs SET created_at=clock_timestamp()-interval '3 days',retired_at=clock_timestamp()-interval '3 days'`); e != nil {
		t.Fatal(e)
	}
	if n, e := b.Collect(ctx, 100); e != nil || n < 2 {
		t.Fatalf("garbage collection: %d %v", n, e)
	}
}
func TestClusterMultipartRangeAndReplicaRestart(t *testing.T) {
	a, b, cfg, _ := clusterFixture(t, 0)
	ctx := context.Background()
	r := a.Namespace("alice")
	id := objectstore.ObjectID{Bucket: "shared", Key: "large"}
	if e := r.CreateBucket(ctx, id.Bucket); e != nil {
		t.Fatal(e)
	}
	payload := strings.Repeat("abcdefgh", (9<<20)/8)
	if e := r.PutObject(ctx, id, strings.NewReader(payload)); e != nil {
		t.Fatal(e)
	}
	a.Close()
	reopened, e := cluster.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	d, e := b.Namespace("alice").OpenObject(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Content.Close()
	if _, e = d.Content.Seek(8193, io.SeekStart); e != nil {
		t.Fatal(e)
	}
	data := make([]byte, 17)
	if _, e = io.ReadFull(d.Content, data); e != nil || string(data) != payload[8193:8210] {
		t.Fatalf("range: %v", e)
	}
	got, e := reopened.Namespace("alice").GetObject(ctx, id)
	sum := sha256.Sum256([]byte(payload))
	if e != nil || got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("metadata changed", e)
	}
}
func TestClusterFailedPublicationAndInterruptedBody(t *testing.T) {
	a, b, _, _ := clusterFixture(t, 0)
	ctx := context.Background()
	r := a.Namespace("alice")
	id := objectstore.ObjectID{Bucket: "shared", Key: "file"}
	if e := r.CreateBucket(ctx, id.Bucket); e != nil {
		t.Fatal(e)
	}
	if e := r.PutObject(ctx, id, strings.NewReader("old")); e != nil {
		t.Fatal(e)
	}
	// Fail SQL publication after a successful S3 write. Its blob must not replace old.
	_, e := a.Pool.Exec(ctx, `CREATE FUNCTION fail_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publication fault'; END $$; CREATE TRIGGER fail_publish BEFORE UPDATE ON ts_objects FOR EACH ROW EXECUTE FUNCTION fail_publish()`)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.PutObject(ctx, id, strings.NewReader("new")); e == nil {
		t.Fatal("fault not triggered")
	}
	if _, e = a.Pool.Exec(ctx, `DROP TRIGGER fail_publish ON ts_objects`); e != nil {
		t.Fatal(e)
	}
	d, e := b.Namespace("alice").OpenObject(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(d.Content)
	d.Content.Close()
	if e != nil || string(data) != "old" {
		t.Fatal("uncommitted object visible", e)
	}
	var pending int
	if e = a.Pool.QueryRow(ctx, `SELECT count(*) FROM ts_blobs WHERE state='pending'`).Scan(&pending); e != nil || pending != 1 {
		t.Fatal("lost orphan ledger", e)
	}
	// Removing and recreating a bucket while upload is streaming must not adopt it.
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- r.PutObject(ctx, objectstore.ObjectID{Bucket: "shared", Key: "late"}, reader) }()
	if _, e = writer.Write([]byte("start")); e != nil {
		t.Fatal(e)
	}
	if e = r.DeleteObject(ctx, id); e != nil {
		t.Fatal(e)
	}
	if e = r.DeleteBucket(ctx, "shared"); e != nil {
		t.Fatal(e)
	}
	if e = r.CreateBucket(ctx, "shared"); e != nil {
		t.Fatal(e)
	}
	writer.Close()
	if e = <-done; !errors.Is(e, objectstore.ErrNotFound) {
		t.Fatal("late publication survived bucket recreation", e)
	}
}

func TestClusterServerHelper(t *testing.T) {
	if os.Getenv("TRIPLE_S_TEST_CLUSTER_SERVER") != "1" {
		return
	}
	c, e := config.Parse([]string{"--backend", "cluster"}, "", io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	c.Port, e = strconv.Atoi(os.Getenv("TRIPLE_S_TEST_CLUSTER_PORT"))
	if e != nil {
		t.Fatal(e)
	}
	c.DataDir = os.Getenv("TRIPLE_S_TEST_UNUSED_DIR")
	if e = run(c); e != nil {
		t.Fatal(e)
	}
}
func TestClusterTwoProcessesAndReplicaFailure(t *testing.T) {
	_, _, cfg, _ := clusterFixture(t, 0)
	unused := filepath.Join(t.TempDir(), "must-not-be-created")
	start := func() (string, *exec.Cmd) {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		cmd := exec.Command(os.Args[0], "-test.run=^TestClusterServerHelper$")
		cmd.Env = append(os.Environ(), "TRIPLE_S_TEST_CLUSTER_SERVER=1", "TRIPLE_S_TEST_CLUSTER_PORT="+strconv.Itoa(port), "TRIPLE_S_TEST_UNUSED_DIR="+unused, "TRIPLE_S_DATABASE_URL="+cfg.DSN, "TRIPLE_S_S3_BUCKET="+cfg.Bucket, "AWS_REGION="+cfg.Region, "TRIPLE_S_S3_ENDPOINT="+cfg.Endpoint, "TRIPLE_S_CLUSTER_INSECURE=1")
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		base := "http://127.0.0.1:" + strconv.Itoa(port)
		client := &http.Client{Timeout: time.Second}
		defer client.CloseIdleConnections()
		deadline := time.Now().Add(10 * time.Second)
		for {
			res, e := client.Get(base + "/-/ready")
			if e == nil {
				res.Body.Close()
				if res.StatusCode == 200 {
					return base, cmd
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("replica readiness timed out")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	first, cmd := start()
	second, _ := start()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	call := func(base, method, path, body string, want int) string {
		t.Helper()
		req, e := http.NewRequest(method, base+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != want {
			t.Fatalf("HTTP %d want %d: %v", res.StatusCode, want, e)
		}
		return string(data)
	}
	call(first, "PUT", "/shared", "", 200)
	call(first, "PUT", "/shared/file", "from-first", 200)
	if body := call(second, "GET", "/shared/file", "", 200); body != "from-first" {
		t.Fatal("second process cannot see write")
	}
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	call(second, "PUT", "/shared/file", "after-first-died", 200)
	if body := call(second, "GET", "/shared/file", "", 200); body != "after-first-died" {
		t.Fatal("surviving replica failed")
	}
	if _, e := os.Stat(unused); !os.IsNotExist(e) {
		t.Fatal("cluster used local object storage")
	}
}

func TestClusterS3FailureSimulation(t *testing.T) {
	endpoint := os.Getenv("TRIPLE_S_TEST_S3_FAILURE_ENDPOINT")
	if endpoint == "" {
		t.Skip("start local Postman S3 failure simulation")
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("simulation must be loopback")
	}
	a, _, cfg, _ := clusterFixture(t, 0)
	ctx := context.Background()
	// Change only this isolated schema's provider location to the failure fixture.
	if _, e = a.Pool.Exec(ctx, `UPDATE ts_settings SET endpoint=$1`, endpoint); e != nil {
		t.Fatal(e)
	}
	cfg.Endpoint = endpoint
	failed, e := cluster.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer failed.Close()
	r := failed.Namespace("alice")
	if e = r.CreateBucket(ctx, "shared"); e != nil {
		t.Fatal(e)
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id := objectstore.ObjectID{Bucket: "shared", Key: "failed"}
	if e = r.PutObject(timeout, id, strings.NewReader("not-published")); e == nil {
		t.Fatal("provider failure ignored")
	}
	if _, e = r.GetObject(ctx, id); !errors.Is(e, objectstore.ErrNotFound) {
		t.Fatal("failed upload became visible", e)
	}
	usage, e := r.Usage(ctx)
	if e != nil || usage.Objects != 0 || usage.StoredBytes != 0 {
		t.Fatal("failed upload consumed quota", e)
	}
	var pending int
	if e = a.Pool.QueryRow(ctx, `SELECT count(*) FROM ts_blobs WHERE state='pending'`).Scan(&pending); e != nil || pending != 1 {
		t.Fatal("orphan tracking lost", e)
	}
}

func TestClusterAuthorityOutageFailsClosed(t *testing.T) {
	a, b, _, _ := clusterFixture(t, 0)
	h1, h2 := clusterAPI(t, a), clusterAPI(t, b)
	requireStatus(t, scoped(h1, "PUT", "/shared", "", aliceToken), 200)
	a.Close()
	requireStatus(t, scoped(h1, "GET", "/", "", aliceToken), 503)
	requireStatus(t, scoped(h1, "GET", "/-/metrics", "", testToken), 503)
	requireStatus(t, scoped(h2, "GET", "/", "", aliceToken), 200)
}

func TestClusterLocalImportAndVerification(t *testing.T) {
	a, b, _, _ := clusterFixture(t, 0)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "local")
	src, e := storage.Open(dir, objectstore.Limits{MaxObjectBytes: 1024, MaxConcurrentUploads: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = src.CreateBucket(ctx, "empty"); e != nil {
		t.Fatal(e)
	}
	tenants, e := src.OpenNamespaces([]string{access.Namespace("alice")}, objectstore.Limits{MaxObjectBytes: 1024, MaxConcurrentUploads: 1})
	if e != nil {
		t.Fatal(e)
	}
	alice := tenants[access.Namespace("alice")]
	id := objectstore.ObjectID{Bucket: "shared", Key: "file"}
	if e = alice.CreateBucket(ctx, id.Bucket); e != nil {
		t.Fatal(e)
	}
	if e = alice.PutObject(ctx, id, strings.NewReader("local-original")); e != nil {
		t.Fatal(e)
	}
	alice.Close()
	src.Close()
	report, e := a.ImportLocal(ctx, dir)
	if e != nil || report.Copied != 1 {
		t.Fatalf("import: %+v %v", report, e)
	}
	report, e = b.ImportLocal(ctx, dir)
	if e != nil || report.Skipped != 1 {
		t.Fatalf("restart import: %+v %v", report, e)
	}
	verified, e := b.Verify(ctx)
	if e != nil || verified.Objects != 1 || verified.Buckets != 2 {
		t.Fatalf("verify: %+v %v", verified, e)
	}
	if e = b.Namespace("alice").PutObject(ctx, id, strings.NewReader("destination-different")); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ImportLocal(ctx, dir); e == nil {
		t.Fatal("import overwrote differing destination")
	}
	var blob string
	if e = a.Pool.QueryRow(ctx, `SELECT blob FROM ts_objects WHERE namespace='alice' AND key='file'`).Scan(&blob); e != nil {
		t.Fatal(e)
	}
	if _, e = a.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("triple-s-cluster-test"), Key: aws.String(blob)}); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Verify(ctx); e == nil {
		t.Fatal("missing S3 blob not detected")
	}
}
