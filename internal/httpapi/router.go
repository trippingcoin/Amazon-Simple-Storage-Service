// Package httpapi owns routing, authentication, XML and HTTP error mapping.
package httpapi

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Operations is the transport's application boundary. It does not expose a
// storage path, process lock, Close method, or a concrete filesystem backend.
type Operations interface {
	ListBuckets(context.Context) ([]objectstore.Bucket, error)
	CreateBucket(context.Context, string) error
	DeleteBucket(context.Context, string) error
	GetObject(context.Context, string, string) (objectstore.Object, error)
	PutObject(context.Context, string, string, io.Reader) error
	DeleteObject(context.Context, string, string) error
}
type Options struct {
	Token          string
	RequestTimeout time.Duration
	MaxObjectBytes int64
}

func (o Options) Validate() error {
	if len(o.Token) < 32 || strings.ContainsAny(o.Token, " \t\r\n") {
		return errors.New("invalid authentication configuration")
	}
	if o.RequestTimeout <= 0 || o.RequestTimeout > 24*time.Hour {
		return errors.New("invalid request timeout")
	}
	return (objectstore.Limits{MaxObjectBytes: o.MaxObjectBytes, MaxConcurrentUploads: 1}).Validate()
}

func New(s Operations, opts Options) (http.Handler, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	token, timeout := opts.Token, opts.RequestTimeout
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", rootHandler(s))
	mux.HandleFunc("/{bucket}", bucketHandler(s))
	mux.HandleFunc("/{bucket}/{$}", bucketHandler(s))
	mux.HandleFunc("/{bucket}/{object}", objectHandler(s, opts.MaxObjectBytes))
	mux.HandleFunc("/{bucket}/{object}/{$}", objectHandler(s, opts.MaxObjectBytes))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeXMLError(w, "Unsupported object path", http.StatusBadRequest)
	})
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		auth := r.Header.Get("Authorization")
		supplied := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		if len(token) < 32 || len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="triple-s"`)
			writeXMLError(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}
