package store

import (
	"testing"

	"spark-mini-dash/internal/metrics"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

var metricsSnapshotFixture = metrics.Snapshot{
	Hostname: "spark-5a4e",
	CPU:      &metrics.CPUStats{UtilPct: f64(34.2), Cores: 20, PerCorePct: []*float64{f64(34.2)}},
	Memory: &metrics.MemoryStats{
		TotalKiB: i64(127598544), AvailableKiB: i64(4786812),
		UsedKiB: i64(122811732), UsedPct: f64(96.2),
		SwapTotalKiB: i64(16777212), SwapUsedKiB: i64(3550400),
	},
	GPU:   &metrics.GPUStats{Name: "NVIDIA GB10", UtilPct: f64(96), TempC: f64(67), PowerW: f64(30.33), SMClockMHz: i64(2398)},
	Therm: &metrics.ThermStats{SocMaxC: f64(89.2), ZoneCount: 7},
}

var metricsSnapshotNoGPU = metrics.Snapshot{
	Hostname: "spark-5a4e",
	CPU:      &metrics.CPUStats{UtilPct: f64(10), Cores: 20, PerCorePct: []*float64{f64(10)}},
	Memory: &metrics.MemoryStats{
		TotalKiB: i64(127598544), AvailableKiB: i64(4786812),
		UsedKiB: i64(122811732), UsedPct: f64(96.2),
	},
}

func TestRingWrapAround(t *testing.T) {
	r := NewRing(3)
	for i := 1; i <= 5; i++ {
		r.Push(float64(i * 10))
	}
	got := r.Snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []float32{30, 40, 50} {
		if got[i] == nil || *got[i] != want {
			t.Fatalf("got[%d] = %v, want %v (oldest evicted)", i, got[i], want)
		}
	}
}

func TestRingGapsRenderAsNull(t *testing.T) {
	r := NewRing(4)
	r.Push(10)
	r.PushGap()
	r.Push(30)
	r.PushGap()
	got := r.Snapshot()
	if got[0] == nil || *got[0] != 10 || got[1] != nil || got[2] == nil || *got[2] != 30 || got[3] != nil {
		t.Fatalf("gap serialization wrong: %v", got)
	}
}

func TestNodeHistoryPushAll(t *testing.T) {
	h := NewNodeHistory(4)
	h.PushAll(nil) // failed decode → all gaps
	for i, v := range h.CPUUtil.Snapshot() {
		if v != nil {
			t.Fatalf("cpu[%d] = %v, want nil gap", i, v)
		}
	}
	h.PushAll(&metricsSnapshotFixture)
	got := h.GPUTemp.Snapshot()
	if len(got) == 0 || got[len(got)-1] == nil || *got[len(got)-1] != 67 {
		t.Fatalf("gpu temp ring tail = %v, want 67", got)
	}
	if tail := h.SocTemp.Snapshot(); tail[len(tail)-1] == nil || *tail[len(tail)-1] != 89.2 {
		t.Fatalf("soc temp ring tail wrong: %v", tail)
	}
	// GPU null in the snapshot → gap in the gpu rings
	h.PushAll(&metricsSnapshotNoGPU)
	if tail := h.GPUUtil.Snapshot(); tail[len(tail)-1] != nil {
		t.Fatalf("gpu util gap expected, got %v", tail[len(tail)-1])
	}
}

func TestNodeHistoryMap(t *testing.T) {
	h := NewNodeHistory(2)
	h.PushAll(&metricsSnapshotFixture)
	m := h.Map()
	for _, key := range []string{"cpu_util_pct", "gpu_util_pct", "mem_used_pct", "gpu_temp_c", "soc_temp_c", "gpu_power_w"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing series %q", key)
		}
	}
}
