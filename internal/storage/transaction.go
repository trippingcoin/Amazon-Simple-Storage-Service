package storage

import (
	"A3S/internal/objectstore"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
)

const transactionDir = privateDir + "/transactions"

var transactionName = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)
var transactionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type objectTransaction struct {
	Version int
	ID      string
	Action  string
	Object  objectstore.Object
}

func (tx objectTransaction) path() string  { return transactionDir + "/" + tx.ID + ".json" }
func (tx objectTransaction) stage() string { return privateDir + "/upload-" + tx.ID }
func (tx objectTransaction) validate() error {
	sum, err := hex.DecodeString(tx.Object.SHA256)
	if tx.Version != 1 || !transactionID.MatchString(tx.ID) || (tx.Action != "put" && tx.Action != "delete") || tx.Object.ID.Validate() != nil || tx.Object.Size < 0 || err != nil || len(sum) != sha256.Size || tx.Object.LastModified.IsZero() {
		return errors.New("invalid object transaction")
	}
	return nil
}
func (s *Store) stateError() error {
	if s.closed {
		return objectstore.ErrClosed
	}
	if s.recoveryRequired {
		return objectstore.ErrRecoveryRequired
	}
	return nil
}
func (s *Store) prepareTransactions() error {
	if err := s.root.Mkdir(transactionDir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := s.root.Lstat(transactionDir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
		return errors.New("transaction directory must be private and regular")
	}
	return s.syncDir(privateDir)
}

// The journal rename plus directory sync is the durable commit decision.
// After a visible decision, a failure is uncertain and must preserve the stage.
func (s *Store) beginTransaction(ctx context.Context, tx objectTransaction) (bool, error) {
	if err := tx.validate(); err != nil {
		return false, err
	}
	record, err := json.Marshal(tx)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(record)
	raw, err := json.Marshal(metadataEnvelope{Version: 1, Digest: hex.EncodeToString(sum[:]), Record: record})
	if err != nil {
		return false, err
	}
	stage := privateDir + "/transaction-" + tx.ID
	f, err := s.root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer func() { f.Close(); s.cleanup(stage) }()
	if _, err = f.Write(raw); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	// Persist the upload directory entry before any decision references it.
	if err = s.syncDir(privateDir); err != nil {
		return false, err
	}
	s.transactionPoint("prepared")
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = s.journalRename(stage, tx.path()); err != nil {
		_, statErr := s.root.Lstat(tx.path())
		return !errors.Is(statErr, fs.ErrNotExist), err
	}
	if err = s.syncDir(transactionDir); err != nil {
		return true, err
	}
	s.transactionPoint("decision")
	return true, nil
}
func (s *Store) transactionPoint(point string) {
	if s.transactionHook != nil {
		s.transactionHook(point)
	}
}

// Complete a committed decision idempotently. Runtime callers hold mu; startup
// replays before indexing files or cleaning any referenced upload staging.
func (s *Store) completeTransaction(tx objectTransaction, replay bool) (objectstore.Object, error) {
	meta := tx.Object
	dest := string(meta.ID.Bucket) + "/" + string(meta.ID.Key)
	bucket, err := s.root.Lstat(string(meta.ID.Bucket))
	if err != nil {
		return meta, err
	}
	if !bucket.IsDir() {
		return meta, errors.New("unsafe transaction bucket")
	}
	if tx.Action == "put" {
		stage, err := s.root.Lstat(tx.stage())
		if err == nil {
			if !stage.Mode().IsRegular() {
				return meta, errors.New("unsafe transaction staging")
			}
			if replay {
				if err = s.verifyTransactionBlob(tx.stage(), meta); err != nil {
					return meta, err
				}
			}
			if fi, e := s.root.Lstat(dest); e == nil {
				if !fi.Mode().IsRegular() {
					return meta, errors.New("unsafe transaction destination")
				}
			} else if !errors.Is(e, fs.ErrNotExist) {
				return meta, e
			}
			if err = s.rename(tx.stage(), dest); err != nil {
				return meta, err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return meta, err
		} else {
			if !replay {
				return meta, errors.New("committed upload staging missing")
			}
			if err = s.verifyTransactionBlob(dest, meta); err != nil {
				return meta, err
			}
		}
		s.transactionPoint("blob")
		if err = errors.Join(s.syncDir(string(meta.ID.Bucket)), s.syncDir(privateDir)); err != nil {
			return meta, err
		}
		info, err := s.root.Stat(dest)
		if err != nil {
			return meta, err
		}
		meta.LastModified = info.ModTime().UTC()
		if err = s.persistObjectMetadata(meta, info); err != nil {
			return meta, err
		}
	} else {
		if fi, e := s.root.Lstat(dest); e == nil {
			if !fi.Mode().IsRegular() {
				return meta, errors.New("unsafe transaction destination")
			}
			if replay {
				if err = s.verifyTransactionBlob(dest, meta); err != nil {
					return meta, err
				}
			}
			if err = s.root.Remove(dest); err != nil {
				return meta, err
			}
		} else if !errors.Is(e, fs.ErrNotExist) {
			return meta, e
		}
		s.transactionPoint("blob")
		if err = s.syncDir(string(meta.ID.Bucket)); err != nil {
			return meta, err
		}
		if err = s.removeObjectMetadata(meta.ID); err != nil {
			return meta, err
		}
	}
	s.transactionPoint("metadata")
	if err = s.root.Remove(tx.path()); err != nil {
		return meta, err
	}
	if err = s.syncDir(transactionDir); err != nil {
		return meta, err
	}
	s.transactionPoint("finished")
	return meta, nil
}
func (s *Store) verifyTransactionBlob(path string, meta objectstore.Object) error {
	info, err := s.root.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != meta.Size {
		return errors.New("transaction blob does not match decision")
	}
	f, err := s.root.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, f)
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != meta.SHA256 {
		return errors.New("transaction blob checksum mismatch")
	}
	return nil
}
func (s *Store) replayTransactions() error {
	entries, err := readDir(s.root, transactionDir)
	if err != nil {
		return err
	}
	// Global publication lock + fail-stop allows at most one unresolved decision.
	// Never guess ordering for an externally modified/corrupt journal.
	if len(entries) > 1 {
		return errors.New("multiple unresolved object transactions")
	}
	for _, entry := range entries {
		if !transactionName.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			return errors.New("unsafe transaction entry")
		}
		f, err := s.root.Open(transactionDir + "/" + entry.Name())
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, (16<<10)+1))
		if err = errors.Join(readErr, f.Close()); err != nil {
			return err
		}
		var envelope metadataEnvelope
		if len(raw) > 16<<10 || json.Unmarshal(raw, &envelope) != nil || envelope.Version != 1 {
			return errors.New("invalid transaction envelope")
		}
		sum := sha256.Sum256(envelope.Record)
		if envelope.Digest != hex.EncodeToString(sum[:]) {
			return errors.New("transaction digest mismatch")
		}
		var tx objectTransaction
		if json.Unmarshal(envelope.Record, &tx) != nil || tx.validate() != nil || entry.Name() != tx.ID+".json" {
			return errors.New("invalid transaction record")
		}
		if _, err = s.completeTransaction(tx, true); err != nil {
			return err
		}
	}
	return nil
}
