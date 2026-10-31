//go:build integration

package main

import (
	"A3S/internal/config"
	"bytes"
	"context"
	"fmt"
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

// Run with: go run ./tests -tags=integration -run '^TestServer' .
// This suite binds loopback sockets and uses no external services.
func TestServerHelper(t *testing.T) {
	if os.Getenv("TRIPLE_S_TEST_SERVER") != "1" {
		return
	}
	args := []string{}
	if path := os.Getenv("TRIPLE_S_TEST_USERS_FILE"); path != "" {
		args = append(args, "--users-file", path)
	}
	c, err := config.Parse(args, testToken, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c.DataDir = os.Getenv("TRIPLE_S_TEST_DIR")
	c.Port, err = strconv.Atoi(os.Getenv("TRIPLE_S_TEST_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	c.RequestTimeout = 2 * time.Second
	c.ShutdownTimeout = 300 * time.Millisecond
	if err = run(c); err != nil {
		t.Fatal(err)
	}
}
func TestServerLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	base := "http://" + addr
	dir := filepath.Join(t.TempDir(), "data")
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	call := func(method, path, body string, auth bool) (int, []byte, error) {
		r, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		if auth {
			r.Header.Set("Authorization", "Bearer "+testToken)
		}
		response, err := client.Do(r)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return response.StatusCode, data, err
	}
	type process struct {
		cmd  *exec.Cmd
		done chan error
	}
	start := func() *process {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerHelper$")
		cmd.Env = append(os.Environ(), "TRIPLE_S_TEST_SERVER=1", "TRIPLE_S_TEST_DIR="+dir, "TRIPLE_S_TEST_PORT="+strconv.Itoa(port))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() })
		p := &process{cmd: cmd, done: make(chan error, 1)}
		go func() { p.done <- cmd.Wait() }()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-p.done:
				t.Fatalf("server exited at startup: %v", err)
			default:
			}
			if code, _, err := call("GET", "/", "", true); err == nil && code == 200 {
				return p
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("server readiness timed out")
		return nil
	}
	stop := func(p *process) {
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-p.done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("server failed to shut down")
		}
	}
	check := func(method, path, body string, auth bool, want int) []byte {
		t.Helper()
		code, data, err := call(method, path, body, auth)
		if err != nil || code != want {
			t.Fatalf("%s %s: code=%d want=%d err=%v body=%s", method, path, code, want, err, data)
		}
		return data
	}
	stalledUpload := func() net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, 4*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = fmt.Fprintf(conn, "PUT /abc/file HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nContent-Length: 20\r\nConnection: close\r\n\r\npartial", testToken)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	original := func() {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, "abc", "file"))
		if err != nil || string(data) != "hello" {
			t.Fatalf("original overwritten: %q %v", data, err)
		}
	}
	p := start()
	check("PUT", "/abc", "", false, 401)
	check("PUT", "/abc", "", true, 200)
	check("PUT", "/abc/file", "hello", true, 200)
	check("DELETE", "/abc", "", true, 409)
	if data := check("GET", "/abc/file", "", true, 200); string(data) != "hello" {
		t.Fatalf("download=%q", data)
	}
	if data := check("HEAD", "/abc/file", "", true, 200); len(data) != 0 {
		t.Fatal("HEAD body")
	}
	rangeRequest, _ := http.NewRequest("GET", base+"/abc/file", nil)
	rangeRequest.Header.Set("Authorization", "Bearer "+testToken)
	rangeRequest.Header.Set("Range", "bytes=1-3")
	ranged, e := client.Do(rangeRequest)
	if e != nil {
		t.Fatal(e)
	}
	rangeBody, e := io.ReadAll(ranged.Body)
	ranged.Body.Close()
	if e != nil || ranged.StatusCode != 206 || string(rangeBody) != "ell" {
		t.Fatalf("range=%q error=%v", rangeBody, e)
	}
	conn := stalledUpload()
	var response [4096]byte
	n, err := conn.Read(response[:])
	conn.Close()
	if err != nil || !bytes.Contains(bytes.SplitN(response[:n], []byte("\r\n"), 2)[0], []byte("408")) {
		t.Fatalf("stalled upload: %s %v", response[:n], err)
	}
	original()
	stop(p)
	p = start()
	if data := check("GET", "/abc/file?metadata=1", "", true, 200); !bytes.Contains(data, []byte("<Size>5</Size>")) {
		t.Fatalf("bad restored metadata: %s", data)
	}
	conn = stalledUpload()
	// Wait for staging so shutdown definitely interrupts an active upload.
	deadline := time.Now().Add(2 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		entries, e := os.ReadDir(filepath.Join(dir, ".triple-s"))
		if e != nil {
			t.Fatal(e)
		}
		if len(entries) > 3 {
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		t.Fatal("upload never started")
	}
	stop(p)
	conn.Close()
	original()
	entries, err := os.ReadDir(filepath.Join(dir, ".triple-s"))
	if err != nil || len(entries) != 3 || entries[0].Name() != "lock" || entries[1].Name() != "metadata" || entries[2].Name() != "transactions" {
		t.Fatalf("staging cleanup failed: %v %v", entries, err)
	}
}
