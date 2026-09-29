// Package store keeps per-node history rings for the dashboard's
// sparklines. Rings append every poll tick — successful or not — so the time
// axis stays uniform and gaps render as gaps, never as fake zeros.
package store

import (
	"spark-mini-dash/internal/metrics"
)

type point struct {
	V  float32
	Ok bool
}

// Ring is a fixed-capacity circular buffer, oldest first on Snapshot.
type Ring struct {
	pts  []point
	head int // next write slot
	n    int // valid points (<= capacity)
}

func NewRing(capacity int) *Ring {
	return &Ring{pts: make([]point, capacity)}
}

func (r *Ring) Push(v float64) {
	r.pts[r.head] = point{V: float32(v), Ok: true}
	r.advance()
}

func (r *Ring) PushGap() {
	r.pts[r.head] = point{}
	r.advance()
}

func (r *Ring) advance() {
	r.head = (r.head + 1) % len(r.pts)
	if r.n < len(r.pts) {
		r.n++
	}
}

// Snapshot serializes oldest→newest; not-ok points become null.
func (r *Ring) Snapshot() []*float32 {
	if r.n == 0 {
		return nil
	}
	out := make([]*float32, 0, r.n)
	start := (r.head - r.n + len(r.pts)) % len(r.pts)
	for i := 0; i < r.n; i++ {
		p := r.pts[(start+i)%len(r.pts)]
		if p.Ok {
			v := p.V
			out = append(out, &v)
		} else {
			out = append(out, nil)
		}
	}
	return out
}

// NodeHistory bundles the rings the dashboard plots, one per series.
type NodeHistory struct {
	CPUUtil  *Ring
	GPUUtil  *Ring
	MemUsed  *Ring
	GPUTemp  *Ring
	SocTemp  *Ring
	GPUPower *Ring
}

func NewNodeHistory(capacity int) *NodeHistory {
	return &NodeHistory{
		CPUUtil:  NewRing(capacity),
		GPUUtil:  NewRing(capacity),
		MemUsed:  NewRing(capacity),
		GPUTemp:  NewRing(capacity),
		SocTemp:  NewRing(capacity),
		GPUPower: NewRing(capacity),
	}
}

// PushAll extracts the plotted series from a snapshot; an unavailable value
// records a gap.
func (h *NodeHistory) PushAll(s *metrics.Snapshot) {
	if s == nil {
		h.PushGaps()
		return
	}
	push := func(r *Ring, v *float64) {
		if v == nil {
			r.PushGap()
			return
		}
		r.Push(*v)
	}
	push(h.CPUUtil, cpuUtil(s))
	push(h.GPUUtil, gpuUtil(s))
	push(h.MemUsed, memUsed(s))
	push(h.GPUTemp, gpuTemp(s))
	push(h.SocTemp, socTemp(s))
	push(h.GPUPower, gpuPower(s))
}

// Safe accessors: a nil section is a gap, never a panic.
func cpuUtil(s *metrics.Snapshot) *float64 {
	if s.CPU == nil {
		return nil
	}
	return s.CPU.UtilPct
}

func gpuUtil(s *metrics.Snapshot) *float64 {
	if s.GPU == nil {
		return nil
	}
	return s.GPU.UtilPct
}

func memUsed(s *metrics.Snapshot) *float64 {
	if s.Memory == nil {
		return nil
	}
	return s.Memory.UsedPct
}

func gpuTemp(s *metrics.Snapshot) *float64 {
	if s.GPU == nil {
		return nil
	}
	return s.GPU.TempC
}

func socTemp(s *metrics.Snapshot) *float64 {
	if s.Therm == nil {
		return nil
	}
	return s.Therm.SocMaxC
}

func gpuPower(s *metrics.Snapshot) *float64 {
	if s.GPU == nil {
		return nil
	}
	return s.GPU.PowerW
}

// PushGaps records a failed tick on every ring.
func (h *NodeHistory) PushGaps() {
	for _, r := range []*Ring{h.CPUUtil, h.GPUUtil, h.MemUsed, h.GPUTemp, h.SocTemp, h.GPUPower} {
		r.PushGap()
	}
}

// Map serializes the rings for /api/state.
func (h *NodeHistory) Map() map[string][]*float32 {
	return map[string][]*float32{
		"cpu_util_pct": h.CPUUtil.Snapshot(),
		"gpu_util_pct": h.GPUUtil.Snapshot(),
		"mem_used_pct": h.MemUsed.Snapshot(),
		"gpu_temp_c":   h.GPUTemp.Snapshot(),
		"soc_temp_c":   h.SocTemp.Snapshot(),
		"gpu_power_w":  h.GPUPower.Snapshot(),
	}
}
