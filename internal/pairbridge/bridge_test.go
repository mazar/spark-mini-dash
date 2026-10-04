package pairbridge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The bridge is exercised against fake PAIR children (real JSON-RPC over
// stdio, canned behavior) — the same approach PAIR's own tests use.

func buildFake(t *testing.T, bin, name string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(bin, name), name+".go")
	cmd.Dir = "testdata" // fake sources live there, standalone (no module)
	cmd.Env = append(os.Environ(), "GO111MODULE=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake %s: %v\n%s", name, err, out)
	}
}

func TestBridgeReceiveFlow(t *testing.T) {
	bin := t.TempDir()
	buildFake(t, bin, "nvpair-cluster-manager")
	buildFake(t, bin, "nvpair-node-scanner")
	buildFake(t, bin, "nvpair-workload-manager")

	bridge := New(Config{
		BinariesDir:  bin,
		StateDir:     t.TempDir(),
		ClusterPort:  0, // fakes never bind; free ports irrelevant in tests
		WorkloadPort: 0,
	})
	defer bridge.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = bridge.Run(ctx) }()

	// 1. fake cluster-manager emits an invite shortly after spawn
	waitUntil(t, bridge, "invite", func() bool {
		return bridge.Snapshot().Invite != nil
	})
	if got := bridge.Snapshot().Status; got != "pairing" {
		t.Fatalf("status after invite = %q, want pairing", got)
	}

	// 2. the user-approved PIN pairing lands as joined
	if err := bridge.RespondToInvite(ctx, "654321"); err != nil {
		t.Fatalf("RespondToInvite: %v", err)
	}
	if got := bridge.Snapshot().Status; got != "joined" {
		t.Fatalf("status after pair = %q, want joined", got)
	}

	// 3. workload lifecycle: queued → running → removed ends in Recent
	waitUntil(t, bridge, "running workload in flight", func() bool {
		in := bridge.Snapshot().Inflight
		return len(in) == 1 && in[0].State == "running" && in[0].ScheduledOn == "uuid-target"
	})
	waitUntil(t, bridge, "workload removal lands in recent", func() bool {
		rec := bridge.Snapshot().Recent
		in := bridge.Snapshot().Inflight
		return len(in) == 0 && len(rec) >= 1
	})
	if got := bridge.Snapshot().Recent[0].State; got != "cancelled" {
		t.Fatalf("removed workload state = %q, want cancelled", got)
	}

	// 4. RespondToInvite without a pending invite errors
	if err := bridge.RespondToInvite(ctx, "111111"); err == nil {
		t.Fatal("RespondToInvite with no pending invite should fail")
	}
}

func waitUntil(t *testing.T, b *Bridge, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	data, _ := json.Marshal(b.Snapshot())
	t.Fatalf("timed out waiting for %s; state: %s", what, data)
}
