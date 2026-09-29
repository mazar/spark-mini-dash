package metrics

import (
	"math"
	"math/rand"
	"time"

	"spark-mini-dash/internal/version"
)

// mockSampler produces plausible synthetic values via seeded random walks.
// It performs no system reads, so the full dashboard can be demoed on
// machines with no DGX hardware (Windows, Pi, CI) or without a GPU.
type mockSampler struct {
	rnd      *rand.Rand
	hostname string
	start    time.Time
	tick     int64

	cpu     float64
	gpuUtil float64
	gpuTemp float64
	power   float64
	memPct  float64
	hot     float64
	cool    float64
	swap    float64
}

func NewMockSampler(hostname string) Sampler {
	return &mockSampler{
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
		hostname: hostname,
		start:    time.Now().Add(-17 * time.Hour),
		cpu:      20, gpuUtil: 30, gpuTemp: 60, power: 40,
		memPct: 55, hot: 60, cool: 45, swap: 1e9,
	}
}

func (m *mockSampler) Sample() (*Snapshot, error) {
	m.tick++
	r := m.rnd
	walk := func(v, lo, hi, step float64) float64 {
		v += (r.Float64() - 0.5) * step
		if v < lo {
			v = lo
		}
		if v > hi {
			v = hi
		}
		return v
	}

	m.cpu = walk(m.cpu, 2, 95, 14)
	m.gpuUtil = walk(m.gpuUtil, 2, 98, 18)
	m.gpuTemp = walk(m.gpuTemp, 52, 84, 2.4)
	m.power = walk(m.power, 15, 130, 16)
	m.memPct = walk(m.memPct, 35, 92, 3.5)
	m.hot = walk(m.hot, 55, 93, 2.2)
	m.cool = walk(m.cool, 45, 72, 1.8)
	m.swap = walk(m.swap, 0, 6e9, 2e8)

	cores := 20
	perCore := make([]*float64, cores)
	for c := 0; c < cores; c++ {
		v := m.cpu + math.Sin(float64(m.tick+int64(c*7))/13)*18 + (r.Float64()-0.5)*10
		perCore[c] = clampPct(v)
	}

	const totalKiB = int64(127598544) // real Spark MemTotal
	used := int64(float64(totalKiB) * m.memPct / 100)

	zones := make([]ThermZone, 7)
	socMax := m.cool
	hotIdx := 0
	for i := range zones {
		v := m.cool + (r.Float64()-0.5)*4
		if i == 0 || i == 4 {
			v = m.hot + (r.Float64()-0.5)*2
		}
		zones[i] = ThermZone{Index: i, Type: "acpitz", TempC: &v}
		if v > socMax {
			socMax = v
			hotIdx = i
		}
	}

	up := int64(time.Since(m.start).Seconds()) + m.tick*2
	clock := int64(1200 + m.gpuUtil/100*1200)

	return &Snapshot{
		Schema:    SchemaVer,
		Hostname:  m.hostname,
		Version:   version.Version,
		Mock:      true,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		UptimeS:   &up,
		CPU: &CPUStats{
			UtilPct:    clampPct(m.cpu),
			Cores:      cores,
			PerCorePct: perCore,
		},
		Memory: &MemoryStats{
			TotalKiB:     int64Ptr(totalKiB),
			AvailableKiB: int64Ptr(totalKiB - used),
			UsedKiB:      int64Ptr(used),
			UsedPct:      clampPct(m.memPct),
			SwapTotalKiB: int64Ptr(16777212),
			SwapUsedKiB:  int64Ptr(int64(m.swap)),
		},
		GPU: &GPUStats{
			Name:       "NVIDIA GB10 (mock)",
			UtilPct:    clampPct(m.gpuUtil),
			TempC:      clampTemp(m.gpuTemp),
			PowerW:     clampTemp(m.power),
			SMClockMHz: &clock,
		},
		Therm: &ThermStats{
			SocMaxC:      clampTemp(socMax),
			ZoneCount:    len(zones),
			ZoneMaxIndex: &hotIdx,
			Zones:        zones,
		},
	}, nil
}

func clampTemp(v float64) *float64 { return &v }

func int64Ptr(v int64) *int64 { return &v }
