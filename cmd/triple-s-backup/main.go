// triple-s-backup operates on stopped local storage, never on HTTP endpoints.
package main

import (
	"A3S/internal/backup"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: triple-s-backup create --dir DATA --out NEW_BACKUP | verify --backup BACKUP | restore --backup BACKUP --to NEW_DATA")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	var source, destination, bundle, work string
	switch args[0] {
	case "create":
		flags.StringVar(&source, "dir", "", "Existing stopped storage directory")
		flags.StringVar(&destination, "out", "", "New backup directory outside storage")
		flags.StringVar(&work, "work-dir", "", "Parent directory for temporary restore verification")
	case "verify":
		flags.StringVar(&bundle, "backup", "", "Backup directory")
		flags.StringVar(&work, "work-dir", "", "Parent directory for temporary restore verification")
	case "restore":
		flags.StringVar(&bundle, "backup", "", "Backup directory")
		flags.StringVar(&destination, "to", "", "New destination directory")
	default:
		return errors.New("unknown backup command")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	var m backup.Manifest
	var err error
	switch args[0] {
	case "create":
		if source == "" || destination == "" {
			return errors.New("--dir and --out are required")
		}
		m, err = backup.Create(ctx, source, destination, work)
	case "verify":
		if bundle == "" {
			return errors.New("--backup is required")
		}
		m, err = backup.Verify(ctx, bundle, work)
	case "restore":
		if bundle == "" || destination == "" {
			return errors.New("--backup and --to are required")
		}
		m, err = backup.Restore(ctx, bundle, destination)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Status   string          `json:"status"`
		Manifest backup.Manifest `json:"manifest"`
	}{"verified", m})
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "backup failed:", err)
		os.Exit(1)
	}
}
