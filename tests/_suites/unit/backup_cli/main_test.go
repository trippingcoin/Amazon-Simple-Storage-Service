package main

import (
	"A3S/internal/objectstore"
	"A3S/internal/storage"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
)

func TestCommands(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "data")
	bundle := filepath.Join(t.TempDir(), "backup")
	s, e := storage.Open(source, objectstore.Limits{MaxObjectBytes: 1024, MaxConcurrentUploads: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CreateBucket(ctx, "empty"); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"create", "--dir", source, "--out", bundle}, {"verify", "--backup", bundle}, {"restore", "--backup", bundle, "--to", filepath.Join(t.TempDir(), "restored")}} {
		var out bytes.Buffer
		if e = run(ctx, args, &out, io.Discard); e != nil {
			t.Fatal(e)
		}
		var result struct{ Status string }
		if e = json.Unmarshal(out.Bytes(), &result); e != nil || result.Status != "verified" {
			t.Fatalf("missing verification result: %v", e)
		}
	}
	for _, args := range [][]string{nil, {"wrong"}, {"create"}, {"verify"}, {"restore", "--backup", bundle}, {"verify", "--backup", bundle, "extra"}} {
		var out bytes.Buffer
		if e = run(ctx, args, &out, io.Discard); e == nil {
			t.Fatal("invalid invocation accepted")
		}
		if out.Len() != 0 {
			t.Fatal("failure reported success")
		}
	}
}
