// Package observatory provides bounded, privacy-preserving operational snapshots.
package observatory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Event struct {
	Sequence      uint64    `json:"sequence"`
	At            time.Time `json:"at"`
	RequestID     string    `json:"request_id"`
	Operation     string    `json:"operation"`
	Status        int       `json:"status"`
	Outcome       string    `json:"outcome"`
	DurationMS    float64   `json:"duration_ms"`
	RequestBytes  int64     `json:"request_bytes"`
	ResponseBytes int64     `json:"response_bytes"`
}
type Node struct {
	ID            string    `json:"id"`
	StartedAt     time.Time `json:"started_at"`
	AgeSeconds    float64   `json:"age_seconds"`
	State         string    `json:"state"`
	RPS           float64   `json:"rps"`
	LatencyMS     float64   `json:"latency_ms"`
	Inflight      int64     `json:"inflight"`
	Connections   int64     `json:"connections"`
	Requests      uint64    `json:"requests"`
	Errors        uint64    `json:"errors"`
	UploadBytes   uint64    `json:"upload_bytes"`
	DownloadBytes uint64    `json:"download_bytes"`
	Events        []Event   `json:"events"`
}
type Dependency struct {
	State     string  `json:"state"`
	LatencyMS float64 `json:"latency_ms"`
}
type Storage struct {
	Available bool  `json:"available"`
	Bytes     int64 `json:"bytes"`
	Objects   int64 `json:"objects"`
	Buckets   int64 `json:"buckets"`
	Pending   int64 `json:"pending"`
	Retired   int64 `json:"retired"`
	Deleting  int64 `json:"deleting"`
}
type Report struct {
	Version     int        `json:"version"`
	Mode        string     `json:"mode"`
	At          time.Time  `json:"at"`
	SampledAt   time.Time  `json:"sampled_at"`
	Registry    bool       `json:"registry"`
	Warning     string     `json:"warning,omitempty"`
	Nodes       []Node     `json:"nodes"`
	Database    Dependency `json:"database"`
	ObjectStore Dependency `json:"object_store"`
	Storage     Storage    `json:"storage"`
	Replication string     `json:"replication"`
	Backup      string     `json:"backup"`
}
type Provider interface {
	Sample(context.Context, Node) (Report, error)
}
type ProviderFunc func(context.Context, Node) (Report, error)

func (f ProviderFunc) Sample(c context.Context, n Node) (Report, error) { return f(c, n) }

type window struct {
	second   int64
	count    uint64
	duration float64
}

type Monitor struct {
	mu          sync.Mutex
	sampleMu    sync.Mutex
	node        Node
	windows     [10]window
	connections atomic.Int64
	mode        string
	provider    Provider
	report      Report
}

func New(mode string, provider Provider) (*Monitor, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	m := &Monitor{mode: mode, provider: provider, node: Node{ID: hex.EncodeToString(id[:]), StartedAt: now, State: "healthy", Events: []Event{}}}
	m.report = Report{Version: 1, Mode: mode, Warning: "waiting_for_sample", Nodes: []Node{}, Replication: "unobserved", Backup: "unobserved", Database: Dependency{State: "unknown"}, ObjectStore: Dependency{State: "unknown"}}
	return m, nil
}
func (m *Monitor) Begin() { m.mu.Lock(); m.node.Inflight++; m.mu.Unlock() }
func (m *Monitor) Finish(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.node.Inflight--
	m.node.Requests++
	e.Sequence = m.node.Requests
	if e.Outcome != "success" {
		m.node.Errors++
	}
	if e.Operation == "upload" && e.RequestBytes > 0 {
		m.node.UploadBytes += uint64(e.RequestBytes)
	}
	if e.Operation == "download" && (e.Status == 200 || e.Status == 206) && e.ResponseBytes > 0 {
		m.node.DownloadBytes += uint64(e.ResponseBytes)
	}
	e.At = time.Now().UTC()
	w := &m.windows[e.At.Unix()%10]
	if w.second != e.At.Unix() {
		*w = window{second: e.At.Unix()}
	}
	w.count++
	w.duration += e.DurationMS
	if len(m.node.Events) == 24 {
		copy(m.node.Events, m.node.Events[1:])
		m.node.Events = m.node.Events[:23]
	}
	m.node.Events = append(m.node.Events, e)
}

// ConnState counts real TCP connections, including idle keep-alive connections.
func (m *Monitor) ConnState(_ net.Conn, s http.ConnState) {
	switch s {
	case http.StateNew:
		m.connections.Add(1)
	case http.StateClosed, http.StateHijacked:
		m.connections.Add(-1)
	}
}
func (m *Monitor) Local() Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.node
	n.Events = append([]Event{}, n.Events...)
	n.Connections = m.connections.Load()
	now := time.Now()
	var count uint64
	var duration float64
	for _, w := range m.windows {
		if w.second > now.Unix()-10 && w.second <= now.Unix() {
			count += w.count
			duration += w.duration
		}
	}
	seconds := now.Sub(n.StartedAt).Seconds()
	if seconds > 10 {
		seconds = 10
	}
	if seconds < 1 {
		seconds = 1
	}
	n.RPS = float64(count) / seconds
	if count > 0 {
		n.LatencyMS = duration / float64(count)
	}
	return n
}

// Sampling never occurs in a storage request. A slow dependency cannot block PUT/GET.
func (m *Monitor) Sample(ctx context.Context) {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	c, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	n := m.Local()
	r, err := m.provider.Sample(c, n)
	if err != nil {
		n.State = "unknown"
		r = Report{Nodes: []Node{n}, Warning: "sampling_failed", Database: Dependency{State: "unknown"}, ObjectStore: Dependency{State: "unknown"}}
	}
	r.Version = 1
	r.Mode = m.mode
	r.SampledAt = time.Now().UTC()
	r.Replication = "unobserved"
	r.Backup = "unobserved"
	m.mu.Lock()
	m.report = r
	m.mu.Unlock()
}
func (m *Monitor) Run(ctx context.Context) {
	m.Sample(ctx)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sample(ctx)
		}
	}
}
func (m *Monitor) Snapshot() Report {
	local := m.Local()
	m.mu.Lock()
	r := m.report
	r.Nodes = append([]Node{}, r.Nodes...)
	m.mu.Unlock()
	r.At = time.Now().UTC()
	found := false
	for i, n := range r.Nodes {
		if !r.SampledAt.IsZero() && r.Registry {
			n.AgeSeconds += r.At.Sub(r.SampledAt).Seconds()
			if n.AgeSeconds > 15 {
				n.State = "stale"
				n.RPS = 0
				n.Inflight = 0
				n.Connections = 0
			}
			r.Nodes[i] = n
		}
		if n.ID == local.ID {
			local.State = n.State
			local.AgeSeconds = n.AgeSeconds
			r.Nodes[i] = local
			found = true
		}
	}
	if !found {
		r.Nodes = append(r.Nodes, local)
	}
	if r.SampledAt.IsZero() || r.At.Sub(r.SampledAt) > 15*time.Second {
		r.Warning = "sample_stale"
		r.Database.State = "unknown"
		r.ObjectStore.State = "unknown"
		r.Storage.Available = false
		for i := range r.Nodes {
			r.Nodes[i].State = "unknown"
		}
	}
	return r
}
