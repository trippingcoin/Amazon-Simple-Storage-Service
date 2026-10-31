package config

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const testToken = "test-only-token-not-a-real-secret-123456789"

func TestConfigSecurityDefaults(t *testing.T) {
	c, err := Parse(nil, testToken, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "127.0.0.1" || c.Limits.MaxObjectBytes != 64<<20 || c.Limits.MaxConcurrentUploads != 8 {
		t.Fatalf("bad defaults: host=%s", c.Host)
	}
	for _, args := range [][]string{{"--max-storage-bytes", "-1"}, {"--max-objects", "-1"}, {"--log-level", "invalid"}, {"--host", "0.0.0.0"}, {"--max-object-bytes", "0"}, {"--max-uploads", "0"}, {"--request-timeout", "0s"}, {"--tls-cert", "cert.pem"}, {"--dir", ""}, {"--port", "0"}, {"extra"}} {
		if _, err = Parse(args, testToken, io.Discard); err == nil {
			t.Errorf("accepted invalid config %v", args)
		}
	}
	if _, err = Parse(nil, "", io.Discard); err == nil {
		t.Fatal("accepted missing token")
	}
	if _, err = Parse([]string{"--host", "0.0.0.0", "--allow-insecure-http"}, testToken, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err = Parse([]string{"--help"}, "", io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "objects")
	c, err = Parse([]string{"--dir", custom}, testToken, io.Discard)
	if err != nil || c.DataDir != custom {
		t.Fatal("custom data directory ignored", err)
	}
}

func TestDataDirectoryIsResolvedOnce(t *testing.T) {
	start := t.TempDir()
	t.Chdir(start)
	c, err := Parse([]string{"--dir", "./storage/../objects"}, testToken, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(start, "objects")
	if c.DataDir != want || !filepath.IsAbs(c.DataDir) {
		t.Fatalf("directory=%q want=%q", c.DataDir, want)
	}
	t.Chdir(t.TempDir())
	if c.DataDir != want {
		t.Fatal("configured directory changed with working directory")
	}
}

func TestUsersFileCanReplaceLegacyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	hash := sha256.Sum256([]byte(testToken))
	body := fmt.Sprintf(`{"version":1,"users":[{"id":"alice","role":"user","token_sha256":"%x"}]}`, hash)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]string{"--users-file", path}, "", io.Discard)
	if err != nil || len(c.Users) != 1 || c.Token != "" {
		t.Fatalf("registry-only configuration failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"users":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]string{"--users-file", path}, "", io.Discard); err == nil {
		t.Fatal("empty auth accepted")
	}
}

func TestClusterConfigSeparatesCredentialsAndStorage(t *testing.T) {
	if _, e := Parse([]string{"--backend", "cluster"}, "", io.Discard); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"--backend", "other"}, {"--backend", "cluster", "--dir", "data"}, {"--backend", "cluster", "--max-objects", "10"}} {
		if _, e := Parse(args, "", io.Discard); e == nil {
			t.Fatal("ambiguous cluster config accepted")
		}
	}
	if _, e := Parse([]string{"--backend", "cluster"}, testToken, io.Discard); e == nil {
		t.Fatal("legacy token accepted in cluster")
	}
}
