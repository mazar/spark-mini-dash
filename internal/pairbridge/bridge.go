// Package pairbridge joins a PAIR (NVIDIA Personal-AI-Router) cluster as a
// passive, read-only member so spark-dash can display live request routing.
//
// Hard constraint: this bridge never controls the cluster. It spawns PAIR's
// own service binaries (cluster-manager, node-scanner, workload-manager —
// each a JSON-RPC-over-stdio child), performs the one user-approved PIN
// pairing, and thereafter only listens: workload lifecycle events arrive on
// the workload-manager's stdout and are surfaced through Snapshot(). None of
// the mutating cluster methods (invite/create/remove/leave) are ever called,
// and no local jobs are submitted.
package pairbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Config locates the PAIR child binaries and this bridge's state. Ports
// default away from the real PAIR app's 14321/14320.
type Config struct {
	BinariesDir  string
	StateDir     string
	ClusterPort  int
	WorkloadPort int
}

// Workload mirrors PAIR's spec §6 object (nvpair-workload-manager/workload.go).
type Workload struct {
	ID             string  `json:"id"`
	Model          string  `json:"model"`
	Engine         string  `json:"engine"`
	RunID          string  `json:"runId,omitempty"`
	State          string  `json:"state"`
	OriginatedFrom string  `json:"originatedFrom"`
	ScheduledOn    string  `json:"scheduledOn,omitempty"`
	CreatedAt      int64   `json:"createdAt"`
	StartedAt      *int64  `json:"startedAt"`
	CompletedAt    *int64  `json:"completedAt"`
	Error          *string `json:"error"`
	RequesterID    *string `json:"requesterId"`
	Seq            int64   `json:"seq,omitempty"`
}

// Member is a cluster member as the UI shows it (cluster-manager ClusterNode).
type Member struct {
	ID        string `json:"id"`
	NodeUUID  string `json:"nodeUuid"`
	Name      string `json:"name"`
	IPAddress string `json:"ipAddress"`
	State     string `json:"state"`
}

// GPUInfo is the display-relevant slice of noderec.GPUInfo.
type GPUInfo struct {
	Name string `json:"name"`
}

// DirNode mirrors the display-relevant fields of PAIR's noderec.DirectoryNode
// (the node-scanner's enriched discovery entry). Models is the machine's
// installed-model inventory — non-empty means the machine is serving.
type DirNode struct {
	HostUUID string    `json:"hostUuid"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	Trusted  bool      `json:"trusted"`
	Models   []string  `json:"models,omitempty"`
	GPUs     []GPUInfo `json:"gpus,omitempty"`
	LastSeen int64     `json:"lastSeen"` // unix seconds
}

// Invite is a pending inbound pairing (the user must carry the PIN over).
type Invite struct {
	InviteID     string `json:"inviteId"`
	FromNodeName string `json:"fromNodeName"`
}

// State is the bridge's read-only view, served under /api/state as "pair".
type State struct {
	Status   string     `json:"status"` // unpaired | pairing | joined | error
	Error    string     `json:"error,omitempty"`
	Invite   *Invite    `json:"invite,omitempty"`
	Members  []Member   `json:"members"`
	Nodes    []DirNode  `json:"nodes"` // discovery directory (liveness + model inventory)
	Inflight []Workload `json:"inflight"`
	Recent   []Workload `json:"recent"`
}

const recentCap = 12

// settings persists the cluster identity the manager reports, so a restart
// can re-feed it via cluster:set-identity (the manager never writes
// node-settings itself — that's the documented integration contract).
type settings struct {
	ClusterID           string `json:"cluster_id"`
	ClusterFriendlyName string `json:"cluster_friendly_name"`
}

type childProc struct {
	conn  *rpcConn
	setup func(ctx context.Context, c *rpcConn) error // post-spawn wiring, rerun on respawn
}

// Bridge owns the supervised children and the read-only event store.
type Bridge struct {
	cfg Config

	mu        sync.Mutex
	status    string // unpaired | pairing | joined | error
	errStr    string
	invite    *Invite
	members   []Member
	directory map[string]DirNode // hostUuid -> scanner entry (liveness + models)
	selfUUID  string
	settings  settings
	inflight  map[string]*Workload // key: originatedFrom + "/" + id
	recent    []*Workload          // newest last, capped

	cm   *childProc // cluster-manager
	sc   *childProc // node-scanner
	wl   *childProc // workload-manager

	cmReady chan struct{} // closed once the cluster-manager answered get-node-id
}

// New creates a bridge; Run actually starts the children.
func New(cfg Config) *Bridge {
	return &Bridge{
		cfg:       cfg,
		status:    "unpaired",
		inflight:  make(map[string]*Workload),
		directory: make(map[string]DirNode),
		cmReady:   make(chan struct{}),
	}
}

// Snapshot copies the current state for /api/state.
func (b *Bridge) Snapshot() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := State{
		Status:  b.status,
		Error:   b.errStr,
		Invite:  b.invite,
		Members: append([]Member(nil), b.members...),
	}
	if st.Members == nil {
		st.Members = []Member{}
	}
	for _, n := range b.directory {
		st.Nodes = append(st.Nodes, n)
	}
	sort.Slice(st.Nodes, func(i, j int) bool { return st.Nodes[i].HostUUID < st.Nodes[j].HostUUID })
	if st.Nodes == nil {
		st.Nodes = []DirNode{}
	}
	for _, w := range b.inflight {
		st.Inflight = append(st.Inflight, *w)
	}
	sort.Slice(st.Inflight, func(i, j int) bool {
		return st.Inflight[i].CreatedAt < st.Inflight[j].CreatedAt
	})
	for i := len(b.recent) - 1; i >= 0; i-- { // newest first
		st.Recent = append(st.Recent, *b.recent[i])
	}
	if st.Inflight == nil {
		st.Inflight = []Workload{}
	}
	if st.Recent == nil {
		st.Recent = []Workload{}
	}
	return st
}

// RespondToInvite answers the pending inbound pairing with the user-carried
// PIN. This is the bridge's single privileged call, and it only ever *accepts*
// an invite the operator initiated from a real PAIR GUI.
func (b *Bridge) RespondToInvite(ctx context.Context, pin string) error {
	b.mu.Lock()
	invite := b.invite
	b.mu.Unlock()
	if invite == nil {
		return fmt.Errorf("no pending invite")
	}

	b.mu.Lock()
	cm := b.cm
	b.mu.Unlock()
	if cm == nil || cm.conn == nil {
		return fmt.Errorf("cluster-manager not running")
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second) // pairing blocks on network I/O
	defer cancel()
	var res struct {
		InviteID string `json:"inviteId"`
		State    string `json:"state"`
		Reason   string `json:"reason"`
	}
	err := cm.conn.Call(ctx, "cluster:respond-to-invite", map[string]any{
		"inviteId": invite.InviteID,
		"accept":   true,
		"pin":      pin,
	}, &res)
	if err != nil {
		return err
	}
	if res.State != "paired" {
		// negative outcomes are successful results with a terminal state
		b.clearInvite()
		if res.Reason != "" {
			return fmt.Errorf("pairing %s (%s)", res.State, res.Reason)
		}
		return fmt.Errorf("pairing %s", res.State)
	}
	b.mu.Lock()
	b.invite = nil
	b.status = "joined"
	b.mu.Unlock()
	return nil
}

// Stop shuts every child down cleanly (stdin EOF is their documented exit).
func (b *Bridge) Stop() {
	b.mu.Lock()
	children := []*childProc{b.wl, b.sc, b.cm}
	b.mu.Unlock()
	for _, c := range children {
		if c != nil && c.conn != nil {
			c.conn.Stop()
		}
	}
}

/* ---------- run / supervision ---------- */

func (b *Bridge) Run(ctx context.Context) error {
	if err := os.MkdirAll(b.cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	b.loadSettings()

	// cluster-manager first: it mints the node identity (cluster/identity.json)
	// that the scanner and workload-manager resolve from the same dir. Those
	// two only start once that file exists.
	b.spawnSupervised(ctx, &b.cm, "nvpair-cluster-manager", b.cmSetup, b.cmNotify)
	select {
	case <-b.cmReady:
	case <-time.After(10 * time.Second):
		log.Printf("[pairbridge] cluster-manager not ready after 10s; starting scanner/workload-manager anyway")
	case <-ctx.Done():
		return ctx.Err()
	}

	b.spawnSupervised(ctx, &b.sc, "nvpair-node-scanner", b.scSetup, b.scNotify)
	b.spawnSupervised(ctx, &b.wl, "nvpair-workload-manager", b.wlSetup, b.wlNotify)

	<-ctx.Done()
	return nil
}

// spawnSupervised starts a child and restarts it with capped backoff until
// ctx is done. setup reruns on every (re)spawn — service registrations and
// readiness probes are replayed, matching the broker's own behavior.
func (b *Bridge) spawnSupervised(ctx context.Context, slot **childProc, binary string,
	setup func(context.Context, *rpcConn) error, notify func(string, json.RawMessage)) {

	bin := filepath.Join(b.cfg.BinariesDir, binary)
	if _, err := os.Stat(bin); err != nil {
		b.setError(fmt.Sprintf("%s not found in %s (build it from the PAIR source or point --pair-binaries-dir at the PAIR install)", binary, b.cfg.BinariesDir))
		return
	}

	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			c, err := spawnRPC(bin, b.childArgs(binary), notify, nil)
			if err != nil {
				log.Printf("[pairbridge] spawn %s: %v", binary, err)
				b.setError(binary + ": " + err.Error())
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
					backoff = min(backoff*2, 10*time.Second)
					continue
				}
			}
			backoff = time.Second
			b.clearError()
			b.mu.Lock()
			*slot = &childProc{conn: c, setup: setup}
			b.mu.Unlock()

			if err := setup(ctx, c); err != nil && ctx.Err() == nil {
				log.Printf("[pairbridge] %s setup: %v", binary, err)
			}

			// The child runs until it exits or ctx dies; Stop() closes stdin
			// for a clean EOF shutdown, so just wait here.
			<-c.closed
			if ctx.Err() != nil {
				return
			}
			log.Printf("[pairbridge] %s exited; restarting in %s", binary, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, 10*time.Second)
			}
		}
	}()
}

func (b *Bridge) childArgs(binary string) []string {
	switch binary {
	case "nvpair-cluster-manager":
		return []string{"--config-dir", b.cfg.StateDir, "--port", fmt.Sprint(b.cfg.ClusterPort)}
	case "nvpair-node-scanner":
		return []string{"--cluster-dir", filepath.Join(b.cfg.StateDir, "cluster")}
	case "nvpair-workload-manager":
		return []string{"--cluster-dir", filepath.Join(b.cfg.StateDir, "cluster"), "--port", fmt.Sprint(b.cfg.WorkloadPort)}
	}
	return nil
}

/* ---------- cluster-manager wiring (pairing + membership) ---------- */

// cmSetup probes readiness (first successful get-node-id means the identity
// is minted), feeds back the persisted cluster identity, and — read-only —
// seeds membership. It NEVER calls invite/create/remove/leave.
func (b *Bridge) cmSetup(ctx context.Context, c *rpcConn) error {
	var id struct {
		NodeUUID string `json:"nodeUuid"`
		ClusterID string `json:"clusterId"`
	}
	if err := c.Call(ctx, "cluster:get-node-id", nil, &id); err != nil {
		return err
	}
	b.mu.Lock()
	b.selfUUID = id.NodeUUID
	b.mu.Unlock()
	select {
	case <-b.cmReady: // already closed
	default:
		close(b.cmReady)
	}

	if err := c.Call(ctx, "cluster:set-identity", map[string]string{
		"clusterId":           b.settings.ClusterID,
		"clusterFriendlyName": b.settings.ClusterFriendlyName,
	}, nil); err != nil {
		return err
	}
	// Seed membership: nodes:changed only fires on changes, so a restart
	// would otherwise report zero members until the next join/leave.
	var snap struct {
		Nodes []Member `json:"nodes"`
	}
	if err := c.Call(ctx, "nodes:get-initial", nil, &snap); err != nil {
		return err
	}
	b.mu.Lock()
	b.members = snap.Nodes
	b.mu.Unlock()
	if b.settings.ClusterID != "" {
		b.setStatus("joined")
	}
	return nil
}

func (b *Bridge) cmNotify(method string, params json.RawMessage) {
	switch method {
	case "cluster:invite-received":
		var inv struct {
			InviteID     string `json:"inviteId"`
			FromNodeName string `json:"fromNodeName"`
		}
		if json.Unmarshal(params, &inv) != nil || inv.InviteID == "" {
			return
		}
		b.mu.Lock()
		b.invite = &Invite{InviteID: inv.InviteID, FromNodeName: inv.FromNodeName}
		b.status = "pairing"
		b.mu.Unlock()
		log.Printf("[pairbridge] invite received from %s — enter the PIN in the dashboard", inv.FromNodeName)
	case "cluster:invite-canceled", "cluster:invite-expired", "cluster:invite-failed", "cluster:invite-declined":
		b.clearInvite()
		b.setStatus("unpaired")
	case "cluster:identity-changed":
		var id struct {
			ClusterID           string `json:"clusterId"`
			ClusterFriendlyName string `json:"clusterFriendlyName"`
		}
		if json.Unmarshal(params, &id) != nil {
			return
		}
		b.mu.Lock()
		b.settings = settings{ClusterID: id.ClusterID, ClusterFriendlyName: id.ClusterFriendlyName}
		b.mu.Unlock()
		b.saveSettings()
		if id.ClusterID == "" {
			b.setStatus("unpaired")
		} else {
			b.setStatus("joined")
			b.reloadTrust()
		}
	case "nodes:changed":
		var snap struct {
			Nodes []Member `json:"nodes"`
		}
		if json.Unmarshal(params, &snap) != nil {
			return
		}
		b.mu.Lock()
		b.members = snap.Nodes
		b.mu.Unlock()
	}
}

// reloadTrust tells the scanner to re-derive `trusted` annotations and our
// cluster-uuid after a pin-set change (the broker does the same).
func (b *Bridge) reloadTrust() {
	b.mu.Lock()
	sc := b.sc
	b.mu.Unlock()
	if sc != nil && sc.conn != nil {
		_ = sc.conn.Notify("discovery:reload-trust", nil)
		_ = sc.conn.Notify("discovery:reload-identity", nil)
	}
}

func (b *Bridge) clearInvite() {
	b.mu.Lock()
	b.invite = nil
	b.mu.Unlock()
}

func (b *Bridge) setStatus(s string) {
	b.mu.Lock()
	b.status = s
	b.mu.Unlock()
}

func (b *Bridge) setError(msg string) {
	b.mu.Lock()
	b.status = "error"
	b.errStr = msg
	b.mu.Unlock()
}

func (b *Bridge) clearError() {
	b.mu.Lock()
	if b.status == "error" {
		b.status = "unpaired"
		b.errStr = ""
	}
	b.mu.Unlock()
}

// wlSetup: the workload-manager needs no post-spawn wiring — a read-only
// member originates no events, so it keeps an empty peer set (its
// discovery:subscribe is answered in wlNotify).
func (b *Bridge) wlSetup(ctx context.Context, c *rpcConn) error { return nil }

/* ---------- settings persistence ---------- */

func (b *Bridge) settingsPath() string { return filepath.Join(b.cfg.StateDir, "bridge-settings.json") }

func (b *Bridge) loadSettings() {
	data, err := os.ReadFile(b.settingsPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &b.settings)
}

func (b *Bridge) saveSettings() {
	data, err := json.MarshalIndent(b.settings, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(b.settingsPath(), data, 0o600)
}

/* ---------- small helpers ---------- */

func waitFor(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
