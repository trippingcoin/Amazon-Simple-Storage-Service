// Package httpapi owns routing, authentication, XML and HTTP error mapping.
package httpapi

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Operations is the transport's application boundary. It does not expose a
// storage path, process lock, Close method, or a concrete filesystem backend.
type Operations interface {
	Usage(context.Context) (objectstore.Usage, error)
	ListObjects(context.Context, string, objectstore.ListOptions) (objectstore.ObjectPage, error)
	ListBuckets(context.Context) ([]objectstore.Bucket, error)
	CreateBucket(context.Context, string) error
	DeleteBucket(context.Context, string) error
	OpenObject(context.Context, string, string) (objectstore.Download, error)
	GetObject(context.Context, string, string) (objectstore.Object, error)
	PutObject(context.Context, string, string, io.Reader) error
	DeleteObject(context.Context, string, string) error
}
type Options struct {
	Authority                                    Authority
	Logger                                       *slog.Logger
	Token                                        string
	Users                                        []access.User
	Tenants                                      map[string]Operations
	RequestTimeout                               time.Duration
	MaxObjectBytes                               int64
	MaxConcurrentUploads, MaxConcurrentDownloads int
}

func (o Options) Validate() error {
	if o.Token != "" && (len(o.Token) < 32 || strings.ContainsAny(o.Token, " \t\r\n")) {
		return errors.New("invalid authentication configuration")
	}
	if err := access.Validate(o.Users); err != nil {
		return err
	}
	if o.Authority != nil && (o.Token != "" || len(o.Users) != 0 || len(o.Tenants) != 0) {
		return errors.New("shared authority cannot be combined with local credentials")
	}
	active := o.Token != "" || o.Authority != nil
	for _, u := range o.Users {
		active = active || !u.Disabled
		if o.Tenants[u.ID] == nil {
			return errors.New("missing user namespace")
		}
		if o.Token != "" {
			sum := sha256.Sum256([]byte(o.Token))
			if hex.EncodeToString(sum[:]) == u.TokenSHA256 {
				return errors.New("duplicate administrator credential")
			}
		}
	}
	if !active {
		return errors.New("authentication configuration is empty")
	}
	if o.MaxConcurrentUploads < 0 || o.MaxConcurrentUploads > 1024 || o.MaxConcurrentDownloads < 0 || o.MaxConcurrentDownloads > 1024 {
		return errors.New("invalid shared concurrency limit")
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
	timeout := opts.RequestTimeout
	var uploads, downloads chan struct{}
	if opts.MaxConcurrentUploads > 0 {
		uploads = make(chan struct{}, opts.MaxConcurrentUploads)
	}
	if opts.MaxConcurrentDownloads > 0 {
		downloads = make(chan struct{}, opts.MaxConcurrentDownloads)
	}
	metrics := newTelemetry()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	makeMux := func(s Operations) *http.ServeMux {
		mux := http.NewServeMux()
		mux.HandleFunc("/-/metrics", metrics.serve)
		mux.HandleFunc("/-/usage", usageHandler(s))
		mux.HandleFunc("/{$}", rootHandler(s))
		mux.HandleFunc("/{bucket}", bucketHandler(s))
		mux.HandleFunc("/{bucket}/{$}", bucketHandler(s))
		mux.HandleFunc("/{bucket}/{object}", objectHandler(s, opts.MaxObjectBytes))
		mux.HandleFunc("/{bucket}/{object}/{$}", objectHandler(s, opts.MaxObjectBytes))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			writeXMLError(w, "Unsupported object path", http.StatusBadRequest)
		})
		return mux
	}
	legacy := makeMux(s)
	dynamic := makeMux(requestOperations{})
	tenants := make(map[string]*http.ServeMux, len(opts.Users))
	users := append([]access.User(nil), opts.Users...)
	shared := make(map[string]map[string]*http.ServeMux, len(users))
	for i, u := range users {
		// Freeze configuration: later edits to the caller's slices cannot change ACLs.
		users[i].Grants = append([]access.Grant(nil), u.Grants...)
		tenants[u.ID] = makeMux(opts.Tenants[u.ID])
		owners := map[string]map[string]string{}
		for _, g := range u.Grants {
			if owners[g.Owner] == nil {
				owners[g.Owner] = map[string]string{}
			}
			owners[g.Owner][g.Bucket] = g.Permission
		}
		shared[u.ID] = map[string]*http.ServeMux{}
		for owner, grants := range owners {
			shared[u.ID][owner] = makeMux(&sharedOperations{base: opts.Tenants[owner], grants: grants})
		}
	}
	if opts.Token != "" {
		sum := sha256.Sum256([]byte(opts.Token))
		users = append(users, access.User{Role: "admin", TokenSHA256: hex.EncodeToString(sum[:])})
	}

	return metrics.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w = headWriter{w}
		}
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		auth := r.Header.Get("Authorization")
		principal, authenticated := access.Match(users, strings.TrimPrefix(auth, "Bearer "))
		if opts.Authority != nil {
			var err error
			principal, authenticated, err = opts.Authority.Authenticate(ctx, strings.TrimPrefix(auth, "Bearer "))
			if err != nil {
				writeXMLError(w, "Authorization service unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || !authenticated {
			w.Header().Set("WWW-Authenticate", `Bearer realm="triple-s"`)
			writeXMLError(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		mux := legacy
		scopes, hasScope := r.Header[http.CanonicalHeaderKey("X-Storage-User")]

		if opts.Authority != nil {
			owner := principal.ID
			if principal.Role == "admin" {
				owner = ""
			}
			var grants map[string]string
			if hasScope {
				if len(scopes) != 1 || scopes[0] == "" {
					writeXMLError(w, "Invalid namespace selection", http.StatusBadRequest)
					return
				}
				owner = scopes[0]
				if principal.Role != "admin" {
					grants = map[string]string{}
					for _, g := range principal.Grants {
						if g.Owner == owner {
							grants[g.Bucket] = g.Permission
						}
					}
					if len(grants) == 0 {
						writeStorageError(w, access.ErrDenied)
						return
					}
				}
			}
			if principal.Role != "admin" && r.URL.Path == "/-/metrics" {
				writeStorageError(w, access.ErrDenied)
				return
			}
			ops, exists, err := opts.Authority.Namespace(ctx, owner)
			if err != nil {
				writeXMLError(w, "Storage service unavailable", http.StatusServiceUnavailable)
				return
			}
			if !exists {
				writeXMLError(w, "Unknown namespace", http.StatusBadRequest)
				return
			}
			if grants != nil {
				ops = &sharedOperations{base: ops, grants: grants}
			}
			ctx = context.WithValue(ctx, operationsKey{}, ops)
			mux = dynamic
		} else {
			if principal.Role != "admin" {
				if r.URL.Path == "/-/metrics" {
					writeXMLError(w, "Access denied", http.StatusForbidden)
					return
				}
				mux = tenants[principal.ID]
				if hasScope {
					if len(scopes) != 1 || shared[principal.ID][scopes[0]] == nil {
						writeXMLError(w, "Access denied", http.StatusForbidden)
						return
					}
					mux = shared[principal.ID][scopes[0]]
				}
			} else if hasScope {
				if len(scopes) != 1 || tenants[scopes[0]] == nil {
					writeXMLError(w, "Unknown user namespace", http.StatusBadRequest)
					return
				}
				mux = tenants[scopes[0]]
			}
		}

		// Shared admission bounds resources across all mounted namespaces. The store
		// retains its own limits for direct callers and pinned download descriptors.
		var admission chan struct{}
		if r.Method == http.MethodPut {
			admission = uploads
		}
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path != "/-/metrics" {
			admission = downloads
		}
		if admission != nil {
			select {
			case admission <- struct{}{}:
				defer func() { <-admission }()
			default:
				writeStorageError(w, objectstore.ErrBusy)
				return
			}
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	}), logger), nil
}
