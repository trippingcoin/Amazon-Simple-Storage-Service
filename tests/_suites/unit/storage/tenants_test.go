package storage

import (
	"A3S/internal/access"
	"A3S/internal/objectstore"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTenantJournalRecoveryIsIndependent(t *testing.T) {
	root, dir := openTest(t, testOptions)
	a, b := access.Namespace("alice"), access.Namespace("bob")
	tenants, err := root.OpenNamespaces([]string{a, b}, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tenants {
		bucket(t, s, "same")
		put(t, s, "same", "file", "old")
	}
	tenants[a].rename = func(string, string) error { return errors.New("injected publication failure") }
	id := objectstore.ObjectID{Bucket: "same", Key: "file"}
	if err := tenants[a].PutObject(context.Background(), id, strings.NewReader("new-alice")); !errors.Is(err, objectstore.ErrRecoveryRequired) {
		t.Fatal(err)
	}
	put(t, tenants[b], "same", "file", "new-bob")
	for _, s := range tenants {
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	root, err = Open(dir, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Recover existing namespaces even if they are no longer in the registry.
	tenants, err = root.OpenNamespaces(nil, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{a: "new-alice", b: "new-bob"} {
		s := tenants[name]
		defer s.Close()
		d, e := s.OpenObject(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(d.Content)
		d.Content.Close()
		if e != nil || string(data) != want {
			t.Fatalf("namespace recovery: %q %v", data, e)
		}
	}
}
func TestTenantDirectoriesRejectSymlinks(t *testing.T) {
	root, dir := openTest(t, testOptions)
	tenantDir := filepath.Join(dir, privateDir, "tenants")
	if err := os.Mkdir(tenantDir, 0700); err != nil {
		t.Fatal(err)
	}
	name := access.Namespace("alice")
	if err := os.Symlink(t.TempDir(), filepath.Join(tenantDir, name)); err != nil {
		t.Fatal(err)
	}
	if stores, err := root.OpenNamespaces([]string{name}, testOptions); err == nil {
		for _, s := range stores {
			_ = s.Close()
		}
		t.Fatal("symlink namespace accepted")
	}
	if _, err := root.OpenNamespaces([]string{"../escape"}, testOptions); err == nil {
		t.Fatal("invalid namespace accepted")
	}
}
