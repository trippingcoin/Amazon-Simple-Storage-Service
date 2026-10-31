package observatory

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestLiveSnapshotAndFailure(t *testing.T) {
	var fail bool
	m, e := New("local", ProviderFunc(func(_ context.Context, n Node) (Report, error) {
		if fail {
			return Report{}, errors.New("secret-connection-string")
		}
		return Report{Nodes: []Node{n}, Storage: Storage{Available: true, Bytes: 42}, ObjectStore: Dependency{State: "healthy"}}, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	m.ConnState(nil, http.StateNew)
	m.Begin()
	if m.Local().Inflight != 1 {
		t.Fatal("missing active request")
	}
	m.Finish(Event{Operation: "upload", Status: 200, Outcome: "success", DurationMS: 20, RequestBytes: 42})
	m.Sample(context.Background())
	r := m.Snapshot()
	n := r.Nodes[0]
	if n.Requests != 1 || n.Inflight != 0 || n.Connections != 1 || n.UploadBytes != 42 || n.LatencyMS != 20 || n.RPS <= 0 || r.Storage.Bytes != 42 {
		t.Fatalf("%+v", r)
	}
	for i := 0; i < 80; i++ {
		m.Begin()
		m.Finish(Event{Operation: "download", Status: 500, Outcome: "error"})
	}
	if len(m.Local().Events) != 24 || m.Local().Errors != 80 {
		t.Fatal("events not bounded")
	}
	m.ConnState(nil, http.StateClosed)
	fail = true
	m.Sample(context.Background())
	r = m.Snapshot()
	if r.Warning != "sampling_failed" || r.Storage.Available || r.Database.State != "unknown" {
		t.Fatalf("failure disguised: %+v", r)
	}
	m.mu.Lock()
	m.report.SampledAt = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	if m.Snapshot().Nodes[0].State != "unknown" {
		t.Fatal("stale node appears healthy")
	}
}
func TestConcurrentRecordAndRead(t *testing.T) {
	m, e := New("local", ProviderFunc(func(_ context.Context, n Node) (Report, error) { return Report{Nodes: []Node{n}}, nil }))
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.Begin()
				m.Finish(Event{Operation: "upload", Outcome: "success", Status: 200})
				_ = m.Snapshot()
				m.Sample(context.Background())
			}
		}()
	}
	wg.Wait()
	if n := m.Local(); n.Requests != 1000 || n.Inflight != 0 {
		t.Fatalf("lost events %+v", n)
	}
}
