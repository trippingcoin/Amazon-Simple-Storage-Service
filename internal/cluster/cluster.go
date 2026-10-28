// Package cluster implements a PostgreSQL publication point over immutable S3 blobs.
package cluster

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/logging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Config struct {
	DSN, Bucket, Region, Endpoint, TempDir string
	AllowInsecure                          bool
}

func FromEnv() Config {
	return Config{DSN: os.Getenv("TRIPLE_S_DATABASE_URL"), Bucket: os.Getenv("TRIPLE_S_S3_BUCKET"), Region: os.Getenv("AWS_REGION"), Endpoint: os.Getenv("TRIPLE_S_S3_ENDPOINT"), TempDir: os.Getenv("TRIPLE_S_TEMP_DIR"), AllowInsecure: os.Getenv("TRIPLE_S_CLUSTER_INSECURE") == "1"}
}
func (c Config) Validate() error {
	if c.DSN == "" || objectstore.BucketID(c.Bucket).Validate() != nil || c.Region == "" {
		return errors.New("database URL, S3 bucket and AWS region are required")
	}
	u, e := url.Parse(c.DSN)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" {
		return errors.New("invalid database URL")
	}
	if !c.AllowInsecure && u.Query().Get("sslmode") != "verify-full" {
		return errors.New("database requires sslmode=verify-full; insecure mode is for local testing only")
	}
	if c.Endpoint != "" {
		u, e = url.Parse(c.Endpoint)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.AllowInsecure && u.Scheme == "http")) {
			return errors.New("invalid S3 endpoint or TLS disabled")
		}
	}
	return nil
}

type Store struct {
	Pool                                         *pgxpool.Pool
	S3                                           *s3.Client
	uploader                                     *transfermanager.Client
	transport                                    *http.Transport
	config                                       Config
	Prefix                                       string
	MaxObjectBytes, defaultBytes, defaultObjects int64
}

func connect(ctx context.Context, c Config) (*Store, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	pc, e := pgxpool.ParseConfig(c.DSN)
	if e != nil {
		return nil, errors.New("invalid database configuration")
	}
	pc.MaxConns = 16
	pc.MinConns = 0
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = "triple-s"
	pc.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		return nil, e
	}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, e
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 64
	transport.MaxConnsPerHost = 128
	cfg, e := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.Region), awsconfig.WithLogger(logging.Nop{}), awsconfig.WithRetryMaxAttempts(3), awsconfig.WithHTTPClient(&http.Client{Transport: transport}))
	if e != nil {
		pool.Close()
		return nil, e
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
			o.UsePathStyle = true
		}
	})
	s := &Store{Pool: pool, S3: client, config: c, transport: transport}
	s.uploader = transfermanager.New(client, func(o *transfermanager.Options) {
		o.Concurrency = 2
		o.PartSizeBytes = 8 << 20
		o.MultipartUploadThreshold = 8 << 20
		o.FailTimeout = 10 * time.Second
	})
	return s, nil
}
func Open(ctx context.Context, c Config) (*Store, error) {
	s, e := connect(ctx, c)
	if e != nil {
		return nil, e
	}
	if e = s.loadSettings(ctx); e != nil {
		s.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() { s.Pool.Close(); s.transport.CloseIdleConnections() }
func (s *Store) loadSettings(ctx context.Context) error {
	var version int
	var bucket, region, endpoint string
	e := s.Pool.QueryRow(ctx, `SELECT version,bucket,region,endpoint,prefix,max_object_bytes,default_bytes,default_objects FROM ts_settings WHERE singleton`).Scan(&version, &bucket, &region, &endpoint, &s.Prefix, &s.MaxObjectBytes, &s.defaultBytes, &s.defaultObjects)
	if e != nil {
		return fmt.Errorf("cluster must be initialized first: %w", e)
	}
	if version != 1 || bucket != s.config.Bucket || region != s.config.Region || endpoint != s.config.Endpoint {
		return errors.New("replica storage configuration differs from cluster")
	}
	return nil
}
func Initialize(ctx context.Context, c Config, limits objectstore.Limits, users []access.User) error {
	if e := limits.Validate(); e != nil {
		return e
	}
	if e := access.Validate(users); e != nil {
		return e
	}
	activeAdmin := false
	for _, u := range users {
		activeAdmin = activeAdmin || (u.Role == "admin" && !u.Disabled)
	}
	if !activeAdmin {
		return errors.New("cluster requires an active administrator")
	}
	s, e := connect(ctx, c)
	if e != nil {
		return e
	}
	defer s.Close()
	// The physical bucket is provisioned externally with private access/lifecycle.
	if _, e = s.S3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.Bucket)}); e != nil {
		return e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(773143811)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, schema); e != nil {
		return e
	}
	prefix, e := randomID()
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ts_settings(singleton,version,bucket,region,endpoint,prefix,max_object_bytes,default_bytes,default_objects) VALUES(true,1,$1,$2,$3,$4,$5,$6,$7)`, c.Bucket, c.Region, c.Endpoint, "triple-s/"+prefix+"/", limits.MaxObjectBytes, limits.MaxStorageBytes, limits.MaxObjects); e != nil {
		return errors.New("cluster already initialized or schema unavailable")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ts_namespaces(id,max_bytes,max_objects) VALUES('',$1,$2)`, limits.MaxStorageBytes, limits.MaxObjects); e != nil {
		return e
	}
	if e = writeUsers(ctx, tx, users, limits.MaxStorageBytes, limits.MaxObjects); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func writeUsers(ctx context.Context, tx pgx.Tx, users []access.User, maxBytes, maxObjects int64) error {
	if _, e := tx.Exec(ctx, `DELETE FROM ts_users`); e != nil {
		return e
	}
	for _, u := range users {
		if _, e := tx.Exec(ctx, `INSERT INTO ts_namespaces(id,max_bytes,max_objects) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, u.ID, maxBytes, maxObjects); e != nil {
			return e
		}
		grants := u.Grants
		if grants == nil {
			grants = []access.Grant{}
		}
		data, e := json.Marshal(grants)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO ts_users(id,token_sha256,role,disabled,grants) VALUES($1,$2,$3,$4,$5)`, u.ID, u.TokenSHA256, u.Role, u.Disabled, data); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) SyncUsers(ctx context.Context, users []access.User) error {
	if e := access.Validate(users); e != nil {
		return e
	}
	admin := false
	for _, u := range users {
		admin = admin || (u.Role == "admin" && !u.Disabled)
	}
	if !admin {
		return errors.New("active administrator required")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(773143811)`); e != nil {
		return e
	}
	if e = writeUsers(ctx, tx, users, s.defaultBytes, s.defaultObjects); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Authenticate(ctx context.Context, token string) (access.User, bool, error) {
	var u access.User
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return u, false, nil
	}
	sum := sha256.Sum256([]byte(token))
	var grants []byte
	e := s.Pool.QueryRow(ctx, `SELECT id,role,disabled,grants FROM ts_users WHERE token_sha256=$1`, hex.EncodeToString(sum[:])).Scan(&u.ID, &u.Role, &u.Disabled, &grants)
	if errors.Is(e, pgx.ErrNoRows) {
		return u, false, nil
	}
	if e != nil {
		return u, false, e
	}
	if e = json.Unmarshal(grants, &u.Grants); e != nil {
		return u, false, e
	}
	return u, !u.Disabled, nil
}
func (s *Store) HasNamespace(ctx context.Context, id string) (bool, error) {
	var ok bool
	e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ts_namespaces WHERE id=$1)`, id).Scan(&ok)
	return ok, e
}
func (s *Store) Ready(ctx context.Context) error {
	var writable bool
	if e := s.Pool.QueryRow(ctx, `SELECT NOT pg_is_in_recovery() AND version=1 FROM ts_settings WHERE singleton`).Scan(&writable); e != nil {
		return e
	}
	if !writable {
		return errors.New("database is not a writable primary")
	}
	_, e := s.S3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.config.Bucket)})
	return e
}
func randomID() (string, error) {
	var b [24]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}
func sqlError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return objectstore.ErrNotFound
	}
	return e
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
