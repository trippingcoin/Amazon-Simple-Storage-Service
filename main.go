package main

import (
	"A3S/internal/access"
	"A3S/internal/cluster"
	"A3S/internal/config"
	"A3S/internal/httpapi"
	"A3S/internal/observatory"
	"A3S/internal/service"
	"A3S/internal/storage"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// After HTTP shutdown/close, seal admission before waiting for active handlers.
// This prevents closing storage while an interrupted upload is cleaning up.
type trackedHandler struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
	next   http.Handler
}

func (h *trackedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "Shutting down", http.StatusServiceUnavailable)
		return
	}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()
	h.next.ServeHTTP(w, r)
}
func (h *trackedHandler) wait() { h.mu.Lock(); h.closed = true; h.mu.Unlock(); h.wg.Wait() }

// Transport errors can contain peer-controlled text; keep the event structured
// without writing the raw net/http message or panic payload.
type transportLogWriter struct{}

func (transportLogWriter) Write(p []byte) (int, error) {
	slog.Error("http_transport_error")
	return len(p), nil
}

func run(c config.Config) (resultErr error) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer slog.Info("server_stopped")
	var api http.Handler
	var err error
	var monitor *observatory.Monitor
	opts := httpapi.Options{Token: c.Token, Users: c.Users, RequestTimeout: c.RequestTimeout, MaxObjectBytes: c.Limits.MaxObjectBytes, MaxConcurrentUploads: c.Limits.MaxConcurrentUploads, MaxConcurrentDownloads: c.Limits.MaxConcurrentDownloads}
	if c.Backend == "cluster" {
		startup, done := context.WithTimeout(base, 30*time.Second)
		shared, e := cluster.Open(startup, cluster.FromEnv())
		done()
		if e != nil {
			return fmt.Errorf("open cluster: %w", e)
		}
		defer shared.Close()
		monitor, err = observatory.New("cluster", shared)
		if err != nil {
			return err
		}
		opts.Observatory = monitor
		opts.Authority = clusterAuthority{shared}
		opts.MaxObjectBytes = shared.MaxObjectBytes
		api, err = httpapi.New(nil, opts)
		if err == nil {
			api = withClusterReadiness(api, shared)
		}
	} else {
		store, e := storage.Open(c.DataDir, c.Limits)
		if e != nil {
			return fmt.Errorf("open storage: %w", e)
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
		names := make([]string, 0, len(c.Users))
		for _, u := range c.Users {
			names = append(names, access.Namespace(u.ID))
		}
		opts.Tenants = make(map[string]httpapi.Operations)
		if len(names) > 0 {
			namespaces, e := store.OpenNamespaces(names, c.Limits)
			if e != nil {
				return e
			}
			defer func() {
				for _, ns := range namespaces {
					resultErr = errors.Join(resultErr, ns.Close())
				}
			}()
			for _, u := range c.Users {
				opts.Tenants[u.ID] = service.New(namespaces[access.Namespace(u.ID)])
			}
		}
		local := service.New(store)
		all := []httpapi.Operations{local}
		for _, tenant := range opts.Tenants {
			all = append(all, tenant)
		}
		monitor, err = observatory.New("local", localObservatory(all))
		if err != nil {
			return err
		}
		opts.Observatory = monitor
		api, err = httpapi.New(local, opts)
	}
	if err != nil {
		return err
	}
	observeCtx, observeCancel := context.WithCancel(base)
	observeDone := make(chan struct{})
	go func() { defer close(observeDone); monitor.Run(observeCtx) }()
	defer func() { observeCancel(); <-observeDone }()

	handler := &trackedHandler{next: api}
	server := &http.Server{Addr: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Handler: handler, ErrorLog: log.New(transportLogWriter{}, "", 0),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: c.RequestTimeout, WriteTimeout: c.RequestTimeout + 10*time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return base }, ConnState: monitor.ConnState}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() {
		if c.TLSCert != "" {
			result <- server.ListenAndServeTLS(c.TLSCert, c.TLSKey)
		} else {
			result <- server.ListenAndServe()
		}
	}()
	slog.Info("server_starting", "address", server.Addr)
	select {
	case err = <-result:
		cancel()
		server.Close()
		handler.wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-sigCtx.Done():
		ctx, done := context.WithTimeout(context.Background(), c.ShutdownTimeout)
		defer done()
		err = server.Shutdown(ctx)
		cancel()
		if err != nil {
			slog.Warn("shutdown_deadline_reached")
			server.Close()
		}
		handler.wait()
		serveErr := <-result
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	c, err := config.Parse(os.Args[1:], os.Getenv("TRIPLE_S_TOKEN"), os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("configuration_invalid")
		os.Exit(1)
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel)) // Validated by config.Parse.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	if err = run(c); err != nil {
		slog.Error("server_failed")
		os.Exit(1)
	}
}

// Cluster authority resolves current grants and namespace state from PostgreSQL.
type clusterAuthority struct{ s *cluster.Store }

func (a clusterAuthority) Authenticate(ctx context.Context, token string) (access.User, bool, error) {
	return a.s.Authenticate(ctx, token)
}
func (a clusterAuthority) Namespace(ctx context.Context, id string) (httpapi.Operations, bool, error) {
	exists, e := a.s.HasNamespace(ctx, id)
	return service.New(a.s.Namespace(id)), exists, e
}
func withClusterReadiness(next http.Handler, s *cluster.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/ready" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if e := s.Ready(ctx); e != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
