package metrics

import (
	"context"
	"log"
	"os"
	"sync/atomic"
	"time"

	"spark-mini-dash/internal/version"
)

// Sampler produces one Snapshot per call.
type Sampler interface {
	Sample() (*Snapshot, error)
}

// Collector runs a sampler on a fixed interval and always exposes the last
// finished snapshot. Collection happens on its own clock, so the request
// path serves a cached snapshot and any number of concurrent pollers (the
// dashboard, a human curling /metrics) can never distort the CPU delta
// windows.
type Collector struct {
	sampler  Sampler
	interval time.Duration
	cur      atomic.Value // *Snapshot
}

func NewCollector(s Sampler, interval time.Duration) *Collector {
	return &Collector{sampler: s, interval: interval}
}

// Run samples until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	logged := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		snap, err := c.sampler.Sample()
		if err != nil {
			if !logged {
				log.Printf("metrics: sampling error (continuing, will not repeat): %v", err)
				logged = true
			}
			continue
		}
		if snap != nil {
			c.cur.Store(snap)
		}
	}
}

// Snapshot returns the last finished snapshot, or nil before the first tick.
func (c *Collector) Snapshot() *Snapshot {
	s, _ := c.cur.Load().(*Snapshot)
	return s
}

// RealSampler collects from the actual node: /proc (CPU, memory, uptime),
// sysfs thermal zones, and GPU telemetry from the DGX Dashboard stream with
// nvidia-smi as fallback.
type RealSampler struct {
	procStatPath string
	meminfoPath  string
	uptimePath   string
	thermalBase  string
	pinnedZone   int

	smi    *gpuReader
	stream *DashboardStream // nil when source is "smi"

	hostname string
	prev     *statLines
	smiName  string // cached GPU name for stream mode (stream carries none)
}

// Source selection for the GPU/memory path.
const (
	SourceAuto   = "auto"
	SourceStream = "stream"
	SourceSMI    = "smi"
)

// NewRealSampler builds the node sampler. When source is auto or stream a
// background stream client is created; call Start once before Run.
func NewRealSampler(pinnedZone int, source, telemetryURL, token, user, password string) *RealSampler {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "localhost"
	}
	s := &RealSampler{
		procStatPath: "/proc/stat",
		meminfoPath:  "/proc/meminfo",
		uptimePath:   "/proc/uptime",
		pinnedZone:   pinnedZone,
		smi:          newGPUReader(2 * time.Second),
		hostname:     hostname,
	}
	if source != SourceSMI {
		s.stream = NewDashboardStream(StreamConfig{
			BaseURL:  telemetryURL,
			Token:    token,
			User:     user,
			Password: password,
		})
	}
	return s
}

// Start launches the stream client if one is configured.
func (s *RealSampler) Start(ctx context.Context) {
	if s.stream != nil {
		go s.stream.Run(ctx)
	}
}

// Sample assembles one Snapshot. Readers degrade independently: a missing
// source yields a nil section (JSON null), never an error, so the agent
// keeps serving on any host.
func (s *RealSampler) Sample() (*Snapshot, error) {
	snap := &Snapshot{
		Schema:    SchemaVer,
		Hostname:  s.hostname,
		Version:   version.Version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// CPU utilization needs two reads of /proc/stat; the first tick warms up.
	if cur, err := readProcStat(s.procStatPath); err == nil {
		snap.CPU = cpuStats(s.prev, cur)
		s.prev = cur
	}

	// GPU + memory: DGX Dashboard stream first (it is the same source the
	// first-party tools consume), nvidia-smi + /proc/meminfo as fallback.
	if s.stream != nil {
		if v, ok := s.stream.Latest(StreamFreshWindow); ok {
			snap.GPU = &GPUStats{
				Name:    s.gpuName(),
				UtilPct: v.GPUUtilPct,
				TempC:   v.GPUTempC,
				PowerW:  v.GPUPowerW,
			}
			snap.Memory = memoryFromStream(v)
		}
	}
	if snap.Memory == nil {
		snap.Memory = readMeminfo(s.meminfoPath)
	}
	if snap.GPU == nil {
		if g, err := s.smi.sample(); err == nil {
			snap.GPU = g
		}
	}
	if s.stream != nil && s.stream.LastError() != "" {
		snap.StreamError = s.stream.LastError()
	}

	snap.Therm = readThermal(s.thermalBase, s.pinnedZone)
	snap.UptimeS = readUptime(s.uptimePath)
	return snap, nil
}

// gpuName returns the cached GPU display name, querying nvidia-smi once for
// it if needed (the stream payload carries no name). Empty string when the
// name is unknown; the UI then shows the metric values without a model
// caption.
func (s *RealSampler) gpuName() string {
	if s.smiName != "" {
		return s.smiName
	}
	if g, err := s.smi.sample(); err == nil && g.Name != "" {
		s.smiName = g.Name
	}
	return s.smiName
}

// memoryFromStream builds MemoryStats from the stream's unified-memory
// fields (NVIDIA's intended semantics, the same numbers the DGX Dashboard
// and Sync's Resource Monitor display).
func memoryFromStream(v StreamSample) *MemoryStats {
	if v.MemTotalKiB == nil || *v.MemTotalKiB <= 0 || v.MemAvailKiB == nil {
		return nil
	}
	m := &MemoryStats{TotalKiB: v.MemTotalKiB, AvailableKiB: v.MemAvailKiB}
	used := *m.TotalKiB - *m.AvailableKiB
	if used < 0 {
		used = 0
	}
	m.UsedKiB = &used
	m.UsedPct = clampPct(100 * float64(used) / float64(*m.TotalKiB))
	return m
}
