package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

const procStatFirst = `cpu  100 0 100 700 0 0 0 0 0 0
cpu0 10 0 10 80 0 0 0 0 0 0
cpu1 20 0 20 60 0 0 0 0 0 0
intr 1
`

const procStatNext = `cpu  160 0 160 780 0 0 0 0 0 0
cpu0 30 0 30 140 0 0 0 0 0 0
cpu1 90 0 90 20 0 0 0 0 0 0
intr 2
`

func readStat(t *testing.T, s string) *statLines {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	sl, err := readProcStat(path)
	if err != nil {
		t.Fatalf("readProcStat: %v", err)
	}
	return sl
}

func TestProcStatDelta(t *testing.T) {
	first := readStat(t, procStatFirst)
	next := readStat(t, procStatNext)

	if len(first.idle) != 3 { // aggregate + 2 cores
		t.Fatalf("expected 3 lines, got %d", len(first.idle))
	}

	// aggregate: busy 200→320, idle 700→780 → dBusy 120 / dTotal 200 = 60%
	cs := cpuStats(first, next)
	if cs.UtilPct == nil || *cs.UtilPct != 60 {
		t.Fatalf("aggregate util = %v, want 60", cs.UtilPct)
	}
	if cs.Cores != 2 {
		t.Fatalf("cores = %d, want 2", cs.Cores)
	}
	// cpu0: busy 20→60, idle 80→140 → 40/100 = 40%
	if cs.PerCorePct[0] == nil || *cs.PerCorePct[0] != 40 {
		t.Fatalf("cpu0 util = %v, want 40", cs.PerCorePct[0])
	}
	// cpu1: busy 40→180, idle 60→20 → dBusy 140 / dTotal 100 → clamp to 100
	if cs.PerCorePct[1] == nil || *cs.PerCorePct[1] != 100 {
		t.Fatalf("cpu1 util = %v, want clamped 100", cs.PerCorePct[1])
	}
}

func TestProcStatFirstSampleNull(t *testing.T) {
	first := readStat(t, procStatFirst)
	cs := cpuStats(nil, first)
	if cs.UtilPct != nil {
		t.Fatalf("first-tick util = %v, want nil", cs.UtilPct)
	}
	for i, v := range cs.PerCorePct {
		if v != nil {
			t.Fatalf("first-tick per-core[%d] = %v, want nil", i, v)
		}
	}
}

func TestProcStatCoreVanished(t *testing.T) {
	first := readStat(t, procStatFirst)
	shrunk := readStat(t, `cpu  160 0 160 780 0 0 0 0 0 0
cpu0 30 0 30 140 0 0 0 0 0 0
`)
	cs := cpuStats(first, shrunk)
	if cs.Cores != 1 {
		t.Fatalf("cores = %d, want 1", cs.Cores)
	}
	if cs.PerCorePct[0] == nil || *cs.PerCorePct[0] != 40 {
		t.Fatalf("cpu0 util = %v, want 40", cs.PerCorePct[0])
	}
}

func TestProcStatRealFixtureShape(t *testing.T) {
	sl, err := readProcStat(filepath.Join("testdata", "proc_stat_real.txt"))
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	// This machine (a DGX Spark) has 20 cores → 21 lines (aggregate + 20).
	if len(sl.idle) != 21 {
		t.Fatalf("lines = %d, want 21", len(sl.idle))
	}
	cs := cpuStats(nil, sl)
	if cs.Cores != 20 {
		t.Fatalf("cores = %d, want 20", cs.Cores)
	}
}

func TestClampPct(t *testing.T) {
	if *clampPct(-5) != 0 {
		t.Fatal("negative not clamped to 0")
	}
	if *clampPct(140) != 100 {
		t.Fatal("over 100 not clamped")
	}
}
