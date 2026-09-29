package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"spark-mini-dash/internal/metrics"
)

type stubSampler struct {
	snap *metrics.Snapshot
	err  error
}

func (s stubSampler) Sample() (*metrics.Snapshot, error) { return s.snap, s.err }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestHandlerServesSnapshot(t *testing.T) {
	snap := &metrics.Snapshot{
		Schema:   metrics.SchemaVer,
		Hostname: "spark-5a4e",
		CPU:      &metrics.CPUStats{UtilPct: fptr(34.2), Cores: 20},
		GPU:      &metrics.GPUStats{Name: "NVIDIA GB10", UtilPct: fptr(96)},
	}
	col := metrics.NewCollector(stubSampler{snap: snap}, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.Run(ctx)
	waitFor(t, func() bool { return col.Snapshot() != nil })

	srv := httptest.NewServer(New(col, "spark-5a4e", false).Handler())
	defer srv.Close()

	res, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got metrics.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Hostname != "spark-5a4e" || got.Schema != metrics.SchemaVer {
		t.Fatalf("bad snapshot: %+v", got)
	}
	if got.CPU == nil || *got.CPU.UtilPct != 34.2 || got.CPU.Cores != 20 {
		t.Fatalf("bad cpu: %+v", got.CPU)
	}
	if got.Memory != nil {
		t.Fatal("unset sections must be null")
	}

	// healthz
	hres, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil || hres.StatusCode != 200 {
		t.Fatalf("healthz: %v %d", err, hres.StatusCode)
	}
	hres.Body.Close()
}

func TestHandlerPlaceholderBeforeFirstTick(t *testing.T) {
	col := metrics.NewCollector(stubSampler{err: errStub("nope")}, time.Hour)
	srv := httptest.NewServer(New(col, "fresh-node", true).Handler())
	defer srv.Close()
	res, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got metrics.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Hostname != "fresh-node" || !got.Mock || got.CPU != nil {
		t.Fatalf("placeholder wrong: %+v", got)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

func fptr(v float64) *float64 { return &v }
