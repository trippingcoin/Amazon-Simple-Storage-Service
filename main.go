package main

import (
	"A3S/internal/config"
	"A3S/internal/httpapi"
	"A3S/internal/service"
	"A3S/internal/storage"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
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

func run(c config.Config) error {
	store, err := storage.Open(c.DataDir, c.Limits)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	api, err := httpapi.New(service.New(store), httpapi.Options{Token: c.Token, RequestTimeout: c.RequestTimeout, MaxObjectBytes: c.Limits.MaxObjectBytes})
	if err != nil {
		return err
	}
	handler := &trackedHandler{next: api}
	server := &http.Server{Addr: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: c.RequestTimeout, WriteTimeout: c.RequestTimeout + 10*time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return base }}
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
	log.Printf("Starting storage server on %s", server.Addr)
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
			log.Print("Graceful shutdown deadline reached; interrupting active requests")
			server.Close()
		}
		handler.wait()
		serveErr := <-result
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return store.Close()
	}
}
func main() {
	c, err := config.Parse(os.Args[1:], os.Getenv("TRIPLE_S_TOKEN"), os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	if err = run(c); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
