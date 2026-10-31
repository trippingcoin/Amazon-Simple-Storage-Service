package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transactionEmpty(t *testing.T, s *Store) {
	t.Helper()
	entries, err := readDir(s.root, transactionDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("journal not empty", entries, err)
	}
}
func TestTransactionRenameFailureRequiresRecovery(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	failure := errors.New("injected blob publication failure")
	s.rename = func(string, string) error { return failure }
	ctx := context.Background()
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	err := s.PutObject(ctx, id, strings.NewReader("committed"))
	if !errors.Is(err, failure) || !errors.Is(err, objectstore.ErrRecoveryRequired) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "file", "old")
	_, getErr := s.GetObject(ctx, id)
	_, openErr := s.OpenObject(ctx, id)
	_, listErr := s.ListObjects(ctx, "abc", objectstore.ListOptions{Limit: 1})
	_, bucketsErr := s.ListBuckets(ctx)
	_, usageErr := s.Usage(ctx)
	for _, e := range []error{getErr, openErr, listErr, bucketsErr, usageErr, s.CreateBucket(ctx, "def"), s.DeleteBucket(ctx, "abc"), s.DeleteObject(ctx, id), s.PutObject(ctx, id, strings.NewReader("later"))} {
		if !errors.Is(e, objectstore.ErrRecoveryRequired) {
			t.Fatal("operation accepted unresolved transaction", e)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	disk(t, dir, "abc", "file", "committed")
	assertUsage(t, next, 9, 1)
	cleanTemps(t, dir)
	transactionEmpty(t, next)
}
func TestTransactionReadersAndCancellation(t *testing.T) {
	s, _ := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pinned, err := s.OpenObject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Content.Close()
	reached, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	s.transactionHook = func(point string) {
		if point == "decision" {
			cancel()
		}
		if point == "blob" {
			close(reached)
			<-release
		}
	}
	go func() { done <- s.PutObject(ctx, id, strings.NewReader("new")) }()
	<-reached
	data, err := io.ReadAll(pinned.Content)
	if err != nil || string(data) != "old" {
		t.Error("pinned version damaged", err)
	}
	readDone := make(chan objectstore.Object, 1)
	readErr := make(chan error, 1)
	go func() { o, e := s.GetObject(context.Background(), id); readDone <- o; readErr <- e }()
	select {
	case <-readDone:
		t.Error("reader saw partially applied commit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal("cancellation after decision rolled back operation", err)
	}
	// If a broken implementation returned early above, don't hang the test.
	select {
	case meta := <-readDone:
		if err = <-readErr; err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("new"))
		if meta.SHA256 != fmt.Sprintf("%x", sum) {
			t.Fatal(meta)
		}
		fi, e := s.root.Stat("abc/file")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.readObjectMetadata(meta, fi); e != nil {
			t.Fatal("metadata not committed before visibility", e)
		}
	case <-time.After(time.Second):
		if !t.Failed() {
			t.Fatal("reader did not resume")
		}
	}
	transactionEmpty(t, s)
}

func TestTransactionCrashHelper(t *testing.T) {
	point := os.Getenv("TRIPLE_S_TX_POINT")
	if point == "" {
		return
	}
	s, err := Open(os.Getenv("TRIPLE_S_TX_DIR"), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s.transactionHook = func(at string) {
		if at == point {
			os.Exit(41)
		}
	}
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	if os.Getenv("TRIPLE_S_TX_ACTION") == "put" {
		err = s.PutObject(context.Background(), id, strings.NewReader("new"))
	} else {
		err = s.DeleteObject(context.Background(), id)
	}
	t.Fatal("expected crash", err)
}
func TestTransactionCrashMatrix(t *testing.T) {
	for _, action := range []string{"put", "delete"} {
		for _, point := range []string{"prepared", "decision", "blob", "metadata", "finished"} {
			t.Run(action+"/"+point, func(t *testing.T) {
				s, dir := openTest(t, testOptions)
				bucket(t, s, "abc")
				put(t, s, "abc", "file", "old")
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTransactionCrashHelper$")
				cmd.Env = append(os.Environ(), "TRIPLE_S_TX_POINT="+point, "TRIPLE_S_TX_ACTION="+action, "TRIPLE_S_TX_DIR="+dir)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 41 {
					t.Fatalf("helper=%v %s", err, output)
				}
				next, err := Open(dir, testOptions)
				if err != nil {
					t.Fatal(err)
				}
				id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
				meta, err := next.GetObject(context.Background(), id)
				if action == "delete" && point != "prepared" {
					if !errors.Is(err, objectstore.ErrNotFound) {
						t.Fatal("delete not replayed", err)
					}
					assertUsage(t, next, 0, 0)
					if _, err = next.root.Stat(objectMetadataPath(id)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("metadata survived delete", err)
					}
				} else {
					want := "old"
					if action == "put" && point != "prepared" {
						want = "new"
					}
					sum := sha256.Sum256([]byte(want))
					if err != nil || meta.SHA256 != fmt.Sprintf("%x", sum) {
						t.Fatal("wrong recovered version", meta, err)
					}
					disk(t, dir, "abc", "file", want)
					assertUsage(t, next, 3, 1)
				}
				transactionEmpty(t, next)
				cleanTemps(t, dir)
				if err = next.Close(); err != nil {
					t.Fatal(err)
				}
				// Completed journal cleanup must be stable across another restart.
				again, err := Open(dir, testOptions)
				if err != nil {
					t.Fatal(err)
				}
				transactionEmpty(t, again)
				if err = again.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestCorruptJournalPreservesEvidence(t *testing.T) {
	for _, damage := range []string{"truncated", "digest", "future", "bad-path", "staging", "missing-staging", "symlink", "multiple"} {
		t.Run(damage, func(t *testing.T) {
			s, dir := openTest(t, testOptions)
			bucket(t, s, "abc")
			put(t, s, "abc", "file", "old")
			s.rename = func(string, string) error { return errors.New("stop after decision") }
			if err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("new")); !errors.Is(err, objectstore.ErrRecoveryRequired) {
				t.Fatal(err)
			}
			records, err := readDir(s.root, transactionDir)
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			name := records[0].Name()
			recordPath := filepath.Join(dir, transactionDir, name)
			stagePath := filepath.Join(dir, privateDir, "upload-"+strings.TrimSuffix(name, ".json"))
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "truncated":
				err = os.WriteFile(recordPath, []byte("broken"), 0600)
			case "digest", "future", "bad-path":
				raw, e := os.ReadFile(recordPath)
				if e != nil {
					t.Fatal(e)
				}
				var envelope metadataEnvelope
				if e = json.Unmarshal(raw, &envelope); e != nil {
					t.Fatal(e)
				}
				if damage == "digest" {
					envelope.Digest = strings.Repeat("0", 64)
				}
				if damage == "future" {
					envelope.Version = 2
				}
				if damage == "bad-path" {
					var tx objectTransaction
					if e = json.Unmarshal(envelope.Record, &tx); e != nil {
						t.Fatal(e)
					}
					tx.Object.ID.Key = "../outside"
					envelope.Record, e = json.Marshal(tx)
					if e != nil {
						t.Fatal(e)
					}
					sum := sha256.Sum256(envelope.Record)
					envelope.Digest = fmt.Sprintf("%x", sum)
				}
				raw, e = json.Marshal(envelope)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(recordPath, raw, 0600)
			case "missing-staging":
				err = os.Remove(stagePath)
			case "staging":
				err = os.WriteFile(stagePath, []byte("bad"), 0600)
			case "symlink":
				err = os.Remove(recordPath)
				if err == nil {
					err = os.Symlink(filepath.Join(dir, "abc", "file"), recordPath)
				}
			case "multiple":
				err = os.WriteFile(filepath.Join(dir, transactionDir, strings.Repeat("f", 32)+".json"), []byte("evidence"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir, testOptions)
			if err == nil {
				reopened.Close()
				t.Fatal("unsafe journal accepted")
			}
			if _, err = os.Lstat(recordPath); err != nil {
				t.Fatal("journal evidence removed", err)
			}
			if _, err = os.Lstat(stagePath); err != nil && damage != "missing-staging" {
				t.Fatal("staging evidence removed", err)
			}
			disk(t, dir, "abc", "file", "old")
		})
	}
}

func TestPendingTransactionBackupRestore(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	s.rename = func(string, string) error { return errors.New("publication unavailable") }
	id := objectstore.ObjectID{Bucket: "abc", Key: "file"}
	if err := s.PutObject(context.Background(), id, strings.NewReader("new")); !errors.Is(err, objectstore.ErrRecoveryRequired) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "pending.tar")
	restored := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(restored, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-cpf", archive, "-C", dir, "."}, {"-xpf", archive, "-C", restored}} {
		if out, err := exec.Command("tar", args...).CombinedOutput(); err != nil {
			t.Fatalf("tar: %v %s", err, out)
		}
	}
	next, err := Open(restored, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	disk(t, restored, "abc", "file", "new")
	assertUsage(t, next, 3, 1)
	transactionEmpty(t, next)
	cleanTemps(t, restored)
	// Recovery in the copy must not touch the original snapshot.
	disk(t, dir, "abc", "file", "old")
}

func TestCancellationBeforeDecisionLeavesOldVersion(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.transactionHook = func(point string) {
		if point == "prepared" {
			cancel()
		}
	}
	if err := s.PutObject(ctx, objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("new")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	disk(t, dir, "abc", "file", "old")
	assertUsage(t, s, 3, 1)
	transactionEmpty(t, s)
	cleanTemps(t, dir)
}
func TestInflightUploadCannotFollowUnresolvedDecision(t *testing.T) {
	s, dir := openTest(t, testOptions)
	bucket(t, s, "abc")
	put(t, s, "abc", "file", "old")
	ready, release := make(chan struct{}, 1), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "later"}, &quotaBarrier{Reader: strings.NewReader("later"), ready: ready, release: release})
	}()
	<-ready
	s.rename = func(string, string) error { return errors.New("publication failed") }
	err := s.PutObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "file"}, strings.NewReader("new"))
	close(release)
	if !errors.Is(err, objectstore.ErrRecoveryRequired) {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, objectstore.ErrRecoveryRequired) {
		t.Fatal("inflight upload bypassed fail-stop", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	disk(t, dir, "abc", "file", "new")
	if _, err = next.GetObject(context.Background(), objectstore.ObjectID{Bucket: "abc", Key: "later"}); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatal("later upload committed", err)
	}
	assertUsage(t, next, 3, 1)
	transactionEmpty(t, next)
	cleanTemps(t, dir)
}
