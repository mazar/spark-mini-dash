package metrics

import "testing"

func TestMockSamplerBounds(t *testing.T) {
	s := NewMockSampler("mock-host")
	var last *Snapshot
	for i := 0; i < 200; i++ {
		snap, err := s.Sample()
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		last = snap
		check := func(name string, v *float64, lo, hi float64) {
			t.Helper()
			if v == nil || *v < lo || *v > hi {
				t.Fatalf("%s out of bounds: %v (want [%v,%v])", name, v, lo, hi)
			}
		}
		check("cpu.util_pct", snap.CPU.UtilPct, 0, 100)
		check("gpu.util_pct", snap.GPU.UtilPct, 0, 100)
		if *snap.GPU.TempC < 50 || *snap.GPU.TempC > 86 {
			t.Fatalf("gpu temp out of bounds: %v", *snap.GPU.TempC)
		}
		if *snap.GPU.PowerW < 15 || *snap.GPU.PowerW > 131 {
			t.Fatalf("power out of bounds: %v", *snap.GPU.PowerW)
		}
		check("mem.used_pct", snap.Memory.UsedPct, 0, 100)
		if *snap.Memory.TotalKiB != 127598544 {
			t.Fatalf("mock total = %d", *snap.Memory.TotalKiB)
		}
		if snap.Therm == nil || *snap.Therm.SocMaxC < 50 || *snap.Therm.SocMaxC > 95 {
			t.Fatalf("soc max out of bounds: %v", snap.Therm.SocMaxC)
		}
		if len(snap.CPU.PerCorePct) != 20 || snap.CPU.Cores != 20 {
			t.Fatalf("core shape wrong: %d/%d", snap.CPU.Cores, len(snap.CPU.PerCorePct))
		}
		if !snap.Mock {
			t.Fatal("mock flag not set")
		}
	}
	if last == nil || last.Timestamp == "" {
		t.Fatal("timestamp missing")
	}
}
