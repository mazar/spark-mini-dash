// Event paths: workload-manager stdout consumption (the actual receive path)
// and node-scanner service registration (how peers find our listener).
package pairbridge

import (
	"context"
	"encoding/json"
	"time"
)

// scSetup registers this bridge's two inter-node services with the discovery
// daemon so they land in the host's single _nvpair-node mDNS record: peers'
// scanners browse that record, enrich their directories with our `wl` port,
// and their workload-managers dial it to push lifecycle events to us. It also
// seeds the directory snapshot the UI's machine column renders from.
func (b *Bridge) scSetup(ctx context.Context, c *rpcConn) error {
	if err := c.Call(ctx, "discovery:register", map[string]any{
		"service": "wl", "port": b.cfg.WorkloadPort,
	}, nil); err != nil {
		return err
	}
	if err := c.Call(ctx, "discovery:register", map[string]any{
		"service": "cl", "port": b.cfg.ClusterPort,
	}, nil); err != nil {
		return err
	}
	return b.seedDirectory(ctx, c)
}

// seedDirectory pulls the scanner's full directory once (discovery:get-nodes).
func (b *Bridge) seedDirectory(ctx context.Context, c *rpcConn) error {
	var snap struct {
		Nodes []DirNode `json:"nodes"`
	}
	if err := c.Call(ctx, "discovery:get-nodes", map[string]any{}, &snap); err != nil {
		return err
	}
	b.mu.Lock()
	for _, n := range snap.Nodes {
		b.directory[n.HostUUID] = n
	}
	b.mu.Unlock()
	return nil
}

// scNotify folds the daemon's per-node deltas into the directory. The UI
// derives "ready" machines from this: an entry's Models inventory is what
// makes a machine visible ("serving models"); entries without models and
// without active jobs stay off the flow view. Deltas arrive wrapped in a
// NodeEvent envelope (noderec.NodeEvent — {"node": {…}}), matching the
// broker's own handleNotify.
func (b *Bridge) scNotify(method string, params json.RawMessage) {
	switch method {
	case "discovery:node-discovered", "discovery:node-updated", "discovery:node-removed":
		var ev struct {
			Node DirNode `json:"node"`
		}
		if err := json.Unmarshal(params, &ev); err != nil || ev.Node.HostUUID == "" {
			return
		}
		b.mu.Lock()
		if method == "discovery:node-removed" {
			delete(b.directory, ev.Node.HostUUID)
		} else {
			b.directory[ev.Node.HostUUID] = ev.Node
		}
		b.mu.Unlock()
	case "ready":
		// daemon restarted — its registrations were replayed by the supervisor,
		// so just re-seed the directory snapshot
		b.mu.Lock()
		sc := b.sc
		b.mu.Unlock()
		if sc != nil && sc.conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.seedDirectory(ctx, sc.conn)
		}
	}
}

// wlNotify consumes the workload-manager's stdout — the bridge's only live
// data path. Lifecycle events are peer-origin (POSTed to our wl port over
// cluster mTLS) and relayed here as notifications.
func (b *Bridge) wlNotify(method string, params json.RawMessage) {
	switch method {
	case "workloads:upsert":
		var p struct {
			WorkloadInfo *Workload `json:"workloadInfo"`
		}
		if json.Unmarshal(params, &p) != nil || p.WorkloadInfo == nil || p.WorkloadInfo.ID == "" {
			return
		}
		w := p.WorkloadInfo
		k := w.OriginatedFrom + "/" + w.ID
		b.mu.Lock()
		switch w.State {
		case "queued", "running":
			cp := *w
			b.inflight[k] = &cp
		default: // completed | failed | cancelled — terminal
			delete(b.inflight, k)
			b.recent = append(b.recent, w)
			if len(b.recent) > recentCap {
				b.recent = b.recent[len(b.recent)-recentCap:]
			}
		}
		b.mu.Unlock()

	case "workloads:remove":
		// A peer dropped a workload without a terminal lifecycle event; treat
		// it as cancelled so the UI stops listing it.
		var p struct {
			WorkloadID     string `json:"workloadId"`
			OriginatedFrom string `json:"originatedFrom,omitempty"`
		}
		if json.Unmarshal(params, &p) != nil || p.WorkloadID == "" {
			return
		}
		b.mu.Lock()
		for k, w := range b.inflight {
			if w.ID == p.WorkloadID && (p.OriginatedFrom == "" || w.OriginatedFrom == p.OriginatedFrom) {
				w.State = "cancelled"
				b.recent = append(b.recent, w)
				delete(b.inflight, k)
			}
		}
		if len(b.recent) > recentCap {
			b.recent = b.recent[len(b.recent)-recentCap:]
		}
		b.mu.Unlock()

	case "discovery:subscribe":
		// The manager asks for a peer set to push local-origin events to.
		// This bridge originates nothing, so it gets an empty set.
		b.mu.Lock()
		wl := b.wl
		b.mu.Unlock()
		if wl != nil && wl.conn != nil {
			_ = wl.conn.Notify("discovery:nodes", map[string]any{"nodes": []any{}})
		}
	}
}
