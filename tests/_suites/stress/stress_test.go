//go:build stress

package main

import (
	"A3S/internal/storage"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

type generatedBytes struct{}

func (generatedBytes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0x5a
	}
	return len(p), nil
}
func stressServer(t *testing.T, max int64) (string, *http.Client) {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "data"), storage.Options{MaxObjectBytes: max, MaxConcurrentUploads: 8, MaxConcurrentDownloads: 8})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(testAPI(t, s))
	transport := &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 16}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		server.Close()
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return server.URL, &http.Client{Transport: transport, Timeout: time.Minute}
}
func stressRequest(client *http.Client, base, method, path string, body io.Reader) (*http.Response, error) {
	r, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+testToken)
	return client.Do(r)
}
func stressStatus(client *http.Client, base, method, path string, body io.Reader, want int) error {
	response, err := stressRequest(client, base, method, path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	if response.StatusCode != want {
		return fmt.Errorf("%s %s: status %d, want %d", method, path, response.StatusCode, want)
	}
	return err
}
func TestStressLargeFile(t *testing.T) {
	size := int64(128 << 20)
	if value := os.Getenv("TRIPLE_S_TEST_LARGE_MIB"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 8 || n > 1024 {
			t.Fatal("TRIPLE_S_TEST_LARGE_MIB must be 8..1024")
		}
		size = int64(n) << 20
	}
	base, client := stressServer(t, size)
	if err := stressStatus(client, base, "PUT", "/abc", nil, 200); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	// Unknown content length exercises chunked upload and streaming size limits.
	if err := stressStatus(client, base, "PUT", "/abc/large", io.LimitReader(generatedBytes{}, size), 200); err != nil {
		t.Fatal(err)
	}
	upload := time.Since(started)
	response, err := stressRequest(client, base, "GET", "/abc/large", nil)
	if err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	hash := sha256.New()
	n, err := io.Copy(hash, response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || n != size {
		t.Fatalf("download: n=%d status=%d err=%v", n, response.StatusCode, err)
	}
	expected := sha256.New()
	if _, err = io.Copy(expected, io.LimitReader(generatedBytes{}, size)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash.Sum(nil), expected.Sum(nil)) || response.Header.Get("ETag") != fmt.Sprintf(`"%x"`, expected.Sum(nil)) {
		t.Fatal("large object checksum mismatch")
	}
	t.Logf("bytes=%d upload=%s download+verification=%s", size, upload, time.Since(started))
	// One byte above the limit must preserve the previous published version.
	if err = stressStatus(client, base, "PUT", "/abc/large", io.LimitReader(generatedBytes{}, size+1), 413); err != nil {
		t.Fatal(err)
	}
	head, err := stressRequest(client, base, "HEAD", "/abc/large", nil)
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()
	if head.StatusCode != 200 || head.Header.Get("ETag") != response.Header.Get("ETag") {
		t.Fatal("oversized overwrite changed object")
	}
}
func TestStressConcurrentHTTP(t *testing.T) {
	base, client := stressServer(t, 1<<20)
	if err := stressStatus(client, base, "PUT", "/abc", nil, 200); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	const rounds = 12
	failures := make(chan error, workers)
	durations := make(chan time.Duration, workers*rounds)
	var wg sync.WaitGroup
	started := time.Now()
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte(worker)}, 256<<10)
			expected := sha256.Sum256(payload)
			for i := 0; i < rounds; i++ {
				cycle := time.Now()
				path := fmt.Sprintf("/abc/worker-%d-%d", worker, i)
				if err := stressStatus(client, base, "PUT", path, bytes.NewReader(payload), 200); err != nil {
					failures <- err
					return
				}
				response, err := stressRequest(client, base, "GET", path, nil)
				if err != nil {
					failures <- err
					return
				}
				hash := sha256.New()
				n, err := io.Copy(hash, response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 || n != int64(len(payload)) || !bytes.Equal(hash.Sum(nil), expected[:]) {
					failures <- fmt.Errorf("worker %d read failed: %v status=%d size=%d", worker, err, response.StatusCode, n)
					return
				}
				if err = stressStatus(client, base, "DELETE", path, nil, 204); err != nil {
					failures <- err
					return
				}
				durations <- time.Since(cycle)
			}
		}(worker)
	}
	wg.Wait()
	close(failures)
	close(durations)
	for err := range failures {
		t.Error(err)
	}
	samples := make([]time.Duration, 0, workers*rounds)
	for d := range durations {
		samples = append(samples, d)
	}
	if len(samples) != workers*rounds {
		t.Fatalf("completed %d/%d cycles", len(samples), workers*rounds)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("workers=%d cycles=%d requests=%d elapsed=%s cycle_p50=%s cycle_p95=%s", workers, len(samples), 3*len(samples), time.Since(started), samples[len(samples)/2], samples[(len(samples)*95-1)/100])
	if err := stressStatus(client, base, "DELETE", "/abc", nil, 200); err != nil {
		t.Fatal("objects leaked:", err)
	}
}
