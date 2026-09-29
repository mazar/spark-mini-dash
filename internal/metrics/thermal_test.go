package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeThermal builds a sysfs-like tree: 7 acpitz zones like a DGX Spark,
// with one zone missing its temp file and one reading out of physical range.
func fakeThermal(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	mk := func(name, typ, temp string) {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "type"), []byte(typ+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if temp != "" {
			if err := os.WriteFile(filepath.Join(dir, "temp"), []byte(temp+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Real DGX Spark pattern: two hot zones (0 and 4), the rest cooler.
	mk("thermal_zone0", "acpitz", "89200")
	mk("thermal_zone1", "acpitz", "67700")
	mk("thermal_zone2", "acpitz", "81200")
	mk("thermal_zone3", "acpitz", "67800")
	mk("thermal_zone4", "acpitz", "89200")
	mk("thermal_zone5", "acpitz", "200000") // out of range → filtered
	mk("thermal_zone6", "acpitz", "")       // unreadable → filtered
	return base
}

func TestReadThermalMax(t *testing.T) {
	ts := readThermal(fakeThermal(t), -1)
	if ts == nil {
		t.Fatal("nil therm")
	}
	if ts.ZoneCount != 5 { // 7 zones minus the two filtered
		t.Fatalf("zone_count = %d, want 5", ts.ZoneCount)
	}
	if ts.SocMaxC == nil || *ts.SocMaxC != 89.2 {
		t.Fatalf("soc_max = %v, want 89.2", ts.SocMaxC)
	}
	if ts.ZoneMaxIndex == nil || *ts.ZoneMaxIndex != 0 {
		t.Fatalf("zone_max_index = %v, want 0", ts.ZoneMaxIndex)
	}
	if len(ts.Zones) != 5 || ts.Zones[0].Type != "acpitz" {
		t.Fatalf("zones = %+v", ts.Zones)
	}
}

func TestReadThermalPinned(t *testing.T) {
	ts := readThermal(fakeThermal(t), 2)
	if ts == nil || ts.SocMaxC == nil || *ts.SocMaxC != 81.2 {
		t.Fatalf("pinned soc_max = %+v, want 81.2", ts)
	}
	if ts.ZoneCount != 1 {
		t.Fatalf("pinned zone_count = %d, want 1", ts.ZoneCount)
	}
}

func TestReadThermalPinnedBrokenZone(t *testing.T) {
	// Pin to zone 6 (unreadable) → the whole section is nil.
	if ts := readThermal(fakeThermal(t), 6); ts != nil {
		t.Fatalf("pinned broken zone: want nil, got %+v", ts)
	}
}

func TestReadThermalMissingDir(t *testing.T) {
	if ts := readThermal(filepath.Join(t.TempDir(), "nope"), -1); ts != nil {
		t.Fatalf("missing dir: want nil, got %+v", ts)
	}
}
