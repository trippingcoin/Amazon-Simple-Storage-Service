package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type crashReader struct{ read bool }

func (r *crashReader) Read(p []byte) (int, error) {
	if r.read {
		os.Exit(23)
	}
	r.read = true
	return copy(p, "partial"), nil
}
func TestCrashHelper(t *testing.T) {
	mode := os.Getenv("TRIPLE_S_TEST_CRASH_MODE")
	if mode == "" {
		return
	}
	s, err := Open(os.Getenv("TRIPLE_S_TEST_CRASH_DIR"), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "upload" {
		s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, &crashReader{})
		t.Fatal("expected process exit")
	}
	if mode == "commit" {
		s.rename = func(from, to string) error {
			if err := s.root.Rename(from, to); err != nil {
				return err
			}
			os.Exit(24)
			return nil
		}
		s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("committed"))
		t.Fatal("expected process exit")
	}
	if mode == "metadata_before" || mode == "metadata_after" {
		s.metadataRename = func(from, to string) error {
			if mode == "metadata_before" {
				os.Exit(26)
			}
			if err := s.root.Rename(from, to); err != nil {
				return err
			}
			os.Exit(27)
			return nil
		}
		s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("committed"))
		t.Fatal("expected process exit")
	}
	if mode == "delete_commit" {
		if err := s.root.Remove("abc/file"); err != nil {
			t.Fatal(err)
		}
		os.Exit(25)
	}
	t.Fatal("unknown crash mode")
}
func TestProcessCrashRecovery(t *testing.T) {
	for _, mode := range []string{"upload", "commit", "metadata_before", "metadata_after", "delete_commit"} {
		t.Run(mode, func(t *testing.T) {
			s, dir := openTest(t, testOptions)
			bucket(t, s, "abc")
			put(t, s, "abc", "file", "original")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashHelper$")
			cmd.Env = append(os.Environ(), "TRIPLE_S_TEST_CRASH_MODE="+mode, "TRIPLE_S_TEST_CRASH_DIR="+dir)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			wantExit := map[string]int{"upload": 23, "commit": 24, "delete_commit": 25, "metadata_before": 26, "metadata_after": 27}[mode]
			if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
				t.Fatalf("helper: %v %s", err, output)
			}
			next, err := Open(dir, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			if mode == "delete_commit" {
				if _, err = next.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}); !errors.Is(err, objectstore.ErrNotFound) {
					t.Fatal(err)
				}
				if _, err = next.root.Stat(objectMetadataPath(objectstore.ObjectID{Bucket: "abc", Key: "file"})); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("orphan metadata retained", err)
				}
				assertUsage(t, next, 0, 0)
				cleanTemps(t, dir)
				return
			}
			want := "original"
			if mode != "upload" {
				want = "committed"
			}
			disk(t, dir, "abc", "file", want)
			o, err := next.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"})
			sum := sha256.Sum256([]byte(want))
			if err != nil || o.Size != int64(len(want)) || o.SHA256 != fmt.Sprintf("%x", sum) {
				t.Fatalf("recovery: %+v %v", o, err)
			}
			assertUsage(t, next, int64(len(want)), 1)
			cleanTemps(t, dir)
		})
	}
}
