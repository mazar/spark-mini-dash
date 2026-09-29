// Package poller fetches /metrics from each node's agent on a fixed
// interval, maintains that node's history rings, and derives its liveness
// status from the SERVER's receive clock — the agent's timestamp is display
// information only, so clock skew between machines can never flip a live
// node to "stale".
package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"spark-mini-dash/internal/metrics"
	"spark-mini-dash/internal/store"
)

// NodeConfig is one monitored node.
type NodeConfig struct {
	Name  string `json:"name"`  // display name; empty = taken from the agent hostname
	URL   string `json:"url"`   // agent base URL, e.g. http://10.0.0.11:9105
	Color string `json:"color"` // node hue; empty = assigned by position
}

// NodeState is what /api/state reports for one node.
type NodeState struct {
	Name         string                `json:"name"`
	URL          string                `json:"url"`
	Color        string                `json:"color"`
	Status       string                `json:"status"` // live | stale | offline
	LastGoodAgeS float64               `json:"last_good_age_s"`
	Failures     int                   `json:"failures"`
	LastError    string                `json:"last_error"`
	Latest       *metrics.Snapshot     `json:"latest"`
	History      map[string][]*float32 `json:"history"`
}

const (
	StatusLive    = "live"
	StatusStale   = "stale"
	StatusOffline = "offline"
)

// Node polls one agent endpoint.
type Node struct {
	cfg          NodeConfig
	interval     time.Duration
	timeout      time.Duration
	staleAfter   time.Duration
	offlineAfter time.Duration
	client       *http.Client
	hist         *store.NodeHistory
	now          func() time.Time

	mu       sync.RWMutex
	latest   *metrics.Snapshot
	lastGood time.Time
	failures int
	lastErr  string
}

func NewNode(cfg NodeConfig, client *http.Client, interval, timeout, staleAfter, offlineAfter time.Duration, historyLen int, now func() time.Time) *Node {
	if now == nil {
		now = time.Now
	}
	return &Node{
		cfg:          cfg,
		interval:     interval,
		timeout:      timeout,
		staleAfter:   staleAfter,
		offlineAfter: offlineAfter,
		client:       client,
		hist:         store.NewNodeHistory(historyLen),
		now:          now,
	}
}

// Run polls until ctx is cancelled. A per-request timeout shorter than the
// interval plus an in-flight guard means a hung agent can never stack
// requests.
func (n *Node) Run(ctx context.Context) {
	t := time.NewTicker(n.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.pollOnce(ctx)
		}
	}
}

func (n *Node) pollOnce(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	snap, err := fetch(pctx, n.client, n.cfg.URL+"/metrics")
	if err != nil {
		n.mu.Lock()
		n.failures++
		n.lastErr = err.Error()
		n.mu.Unlock()
		n.hist.PushGaps()
		return
	}
	n.mu.Lock()
	n.latest = snap
	n.lastGood = n.now()
	n.failures = 0
	n.lastErr = ""
	n.mu.Unlock()
	n.hist.PushAll(snap)
}

func fetch(ctx context.Context, client *http.Client, url string) (*metrics.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, text(b))
	}
	var snap metrics.Snapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&snap); err != nil {
		return nil, fmt.Errorf("bad metrics payload: %w", err)
	}
	return &snap, nil
}

func text(b []byte) string {
	s := string(b)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// State returns the node's current view for /api/state.
func (n *Node) State() NodeState {
	n.mu.RLock()
	defer n.mu.RUnlock()
	st := NodeState{
		Name:      n.cfg.Name,
		URL:       n.cfg.URL,
		Color:     n.cfg.Color,
		Failures:  n.failures,
		LastError: n.lastErr,
		Latest:    n.latest,
		History:   n.hist.Map(),
	}
	age := n.now().Sub(n.lastGood).Seconds()
	st.LastGoodAgeS = age
	switch {
	case n.lastGood.IsZero():
		st.Status = StatusOffline
	case age <= n.staleAfter.Seconds():
		st.Status = StatusLive
	case age <= n.offlineAfter.Seconds():
		st.Status = StatusStale
	default:
		st.Status = StatusOffline
	}
	// Prefer the agent's own hostname as the display name until one is set.
	if st.Name == "" && st.Latest != nil {
		st.Name = st.Latest.Hostname
	}
	if st.Name == "" {
		st.Name = n.cfg.URL
	}
	return st
}
