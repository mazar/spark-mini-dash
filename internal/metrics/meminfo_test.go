package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMeminfo(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMeminfoSparkFixture(t *testing.T) {
	// Real values captured from this DGX Spark.
	m := readMeminfo(filepath.Join("testdata", "meminfo_spark.txt"))
	if m == nil {
		t.Fatal("fixture failed to parse")
	}
	if *m.TotalKiB != 127598544 {
		t.Fatalf("total = %d, want 127598544", *m.TotalKiB)
	}
	if *m.AvailableKiB != 4643056 {
		t.Fatalf("available = %d, want 4643056", *m.AvailableKiB)
	}
	if *m.UsedKiB != 127598544-4643056 {
		t.Fatalf("used = %d, want %d", *m.UsedKiB, 127598544-4643056)
	}
	wantPct := 100 * float64(127598544-4643056) / 127598544
	if *m.UsedPct < wantPct-0.01 || *m.UsedPct > wantPct+0.01 {
		t.Fatalf("used_pct = %v, want ~%f", *m.UsedPct, wantPct)
	}
	if *m.SwapTotalKiB != 16777212 {
		t.Fatalf("swap_total = %d", *m.SwapTotalKiB)
	}
	if *m.SwapUsedKiB != 16777212-13226812 {
		t.Fatalf("swap_used = %d", *m.SwapUsedKiB)
	}
}

func TestMeminfoFallbackNoAvailable(t *testing.T) {
	// Pre-3.14 kernels (some Pi images) lack MemAvailable.
	m := readMeminfo(writeMeminfo(t, `MemTotal:        1000 kB
MemFree:         100 kB
Buffers:          50 kB
Cached:          200 kB
SwapTotal:            0 kB
SwapFree:             0 kB
`))
	if m == nil {
		t.Fatal("failed to parse")
	}
	if *m.AvailableKiB != 350 {
		t.Fatalf("available = %d, want 350 (free+buffers+cached)", *m.AvailableKiB)
	}
	if *m.UsedKiB != 650 || m.UsedPct == nil || *m.UsedPct != 65 {
		t.Fatalf("used = %d/%v, want 650/65", *m.UsedKiB, *m.UsedPct)
	}
}

func TestMeminfoClampAvailableAboveTotal(t *testing.T) {
	m := readMeminfo(writeMeminfo(t, `MemTotal:        1000 kB
MemAvailable:   2000 kB
MemFree:        2000 kB
`))
	if m == nil {
		t.Fatal("failed to parse")
	}
	if *m.UsedKiB != 0 || m.UsedPct == nil || *m.UsedPct != 0 {
		t.Fatalf("used = %d/%v, want 0/0", *m.UsedKiB, *m.UsedPct)
	}
}

func TestMeminfoMissingFile(t *testing.T) {
	if m := readMeminfo(filepath.Join(t.TempDir(), "nope")); m != nil {
		t.Fatalf("expected nil, got %+v", m)
	}
}
