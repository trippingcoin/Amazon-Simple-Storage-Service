package main

import (
	"A3S/internal/access"
	"A3S/internal/cluster"
	"A3S/internal/objectstore"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: triple-s-cluster init|sync-users|gc|ready|provision-local|import-local|verify")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	var usersFile, source string
	var limits objectstore.Limits
	var batch int
	switch args[0] {
	case "init":
		flags.StringVar(&usersFile, "users-file", "", "Private initial users registry")
		flags.Int64Var(&limits.MaxObjectBytes, "max-object-bytes", 64<<20, "Maximum upload bytes")
		flags.Int64Var(&limits.MaxStorageBytes, "max-storage-bytes", 0, "Default namespace byte quota")
		flags.Int64Var(&limits.MaxObjects, "max-objects", 0, "Default namespace object quota")
	case "sync-users":
		flags.StringVar(&usersFile, "users-file", "", "Authoritative replacement registry")
	case "import-local":
		flags.StringVar(&source, "dir", "", "Stopped local storage to import without overwrites")
	case "gc":
		flags.IntVar(&batch, "limit", 100, "Maximum blobs per run, retained for at least 48h")
	case "ready", "provision-local", "verify":
	default:
		return errors.New("unknown cluster command")
	}
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	cfg := cluster.FromEnv()
	if args[0] == "provision-local" {
		return cluster.ProvisionLocal(ctx, cfg)
	}
	if args[0] == "init" || args[0] == "sync-users" {
		if usersFile == "" {
			return errors.New("users-file is required")
		}
		users, e := access.Load(usersFile)
		if e != nil {
			return e
		}
		if args[0] == "init" {
			limits.MaxConcurrentUploads = 1
			return cluster.Initialize(ctx, cfg, limits, users)
		}
		s, e := cluster.Open(ctx, cfg)
		if e != nil {
			return e
		}
		defer s.Close()
		return s.SyncUsers(ctx, users)
	}
	s, e := cluster.Open(ctx, cfg)
	if e != nil {
		return e
	}
	defer s.Close()
	if args[0] == "verify" {
		report, e := s.Verify(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if args[0] == "import-local" {
		if source == "" {
			return errors.New("dir is required")
		}
		report, e := s.ImportLocal(ctx, source)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if args[0] == "ready" {
		return s.Ready(ctx)
	}
	n, e := s.Collect(ctx, batch)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]int{"removed": n})
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	duration := 24 * time.Hour
	if len(os.Args) > 1 && os.Args[1] == "ready" {
		duration = 3 * time.Second
	}
	ctx, timeout := context.WithTimeout(ctx, duration)
	defer timeout()
	if e := run(ctx, os.Args[1:]); e != nil && !errors.Is(e, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "cluster operation failed; check configuration and service availability")
		os.Exit(1)
	}
}
