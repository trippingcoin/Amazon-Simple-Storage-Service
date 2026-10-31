//go:build integration

package main

import (
	"A3S/internal/access"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMultiuserServerLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	usersFile := filepath.Join(t.TempDir(), "users.json")
	bob := identity("bob", bobToken, "user")
	bob.Grants = []access.Grant{{Owner: "alice", Bucket: "same", Permission: "read"}}
	data, _ := json.Marshal(struct {
		Version int           `json:"version"`
		Users   []access.User `json:"users"`
	}{1, []access.User{identity("alice", aliceToken, "user"), bob}})
	if err := os.WriteFile(usersFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "data")
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	call := func(method, path, body, token, scope string) (int, string, error) {
		r, e := http.NewRequest(method, base+path, strings.NewReader(body))
		if e != nil {
			return 0, "", e
		}
		r.Header.Set("Authorization", "Bearer "+token)
		if scope != "" {
			r.Header.Set("X-Storage-User", scope)
		}
		response, e := client.Do(r)
		if e != nil {
			return 0, "", e
		}
		defer response.Body.Close()
		b, e := io.ReadAll(response.Body)
		return response.StatusCode, string(b), e
	}
	for restart := 0; restart < 2; restart++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerHelper$")
		cmd.Env = append(os.Environ(), "TRIPLE_S_TEST_SERVER=1", "TRIPLE_S_TEST_DIR="+dir, "TRIPLE_S_TEST_PORT="+strconv.Itoa(port), "TRIPLE_S_TEST_USERS_FILE="+usersFile)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		deadline := time.Now().Add(8 * time.Second)
		for {
			code, _, e := call("GET", "/", "", aliceToken, "")
			if e == nil && code == 200 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("server not ready")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if restart == 0 {
			for _, token := range []string{aliceToken, bobToken} {
				if code, _, e := call("PUT", "/same", "", token, ""); e != nil || code != 200 {
					t.Fatalf("bucket: %d %v", code, e)
				}
				if code, _, e := call("PUT", "/same/file", token, token, ""); e != nil || code != 200 {
					t.Fatalf("upload: %d %v", code, e)
				}
			}
		}
		for _, token := range []string{aliceToken, bobToken} {
			if code, body, e := call("GET", "/same/file", "", token, ""); e != nil || code != 200 || body != token {
				t.Fatalf("isolation: %d %v", code, e)
			}
		}
		if code, body, e := call("GET", "/same/file", "", testToken, "alice"); e != nil || code != 200 || body != aliceToken {
			t.Fatalf("admin scope: %d %v", code, e)
		}
		if code, body, e := call("GET", "/same/file", "", bobToken, "alice"); e != nil || code != 200 || body != aliceToken {
			t.Fatalf("shared read: %d %v", code, e)
		}
		if code, _, e := call("PUT", "/same/file", "denied", bobToken, "alice"); e != nil || code != 403 {
			t.Fatalf("read-only grant: %d %v", code, e)
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}
