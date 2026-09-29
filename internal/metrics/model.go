// Package metrics collects one node's resource telemetry into a Snapshot.
//
// Every metric field is a pointer so an unavailable source serializes as JSON
// null and the wire schema stays stable across platforms: a Windows host has
// no /proc, a Pi has no nvidia-smi, a Spark briefly fails a poll. Null means
// "unknown this tick", never zero.
package metrics

// SchemaVer is the current Snapshot schema version.
const SchemaVer = 1

// Snapshot is the complete agent observation for one tick.
type Snapshot struct {
	Schema      int          `json:"schema"`
	Hostname    string       `json:"hostname"`
	Version     string       `json:"version"`
	Mock        bool         `json:"mock"`
	Timestamp   string       `json:"timestamp"`              // RFC3339 UTC, agent wall clock; display only
	StreamError string       `json:"stream_error,omitempty"` // forced-stream source errors (empty when healthy)
	UptimeS     *int64       `json:"uptime_s"`               // null on non-Linux
	CPU         *CPUStats    `json:"cpu"`
	Memory      *MemoryStats `json:"memory"`
	GPU         *GPUStats    `json:"gpu"`
	Therm       *ThermStats  `json:"therm"`
}

// CPUStats holds aggregate and per-core utilization since the previous tick.
type CPUStats struct {
	UtilPct    *float64   `json:"util_pct"` // null on the first tick after start (no delta window yet)
	Cores      int        `json:"cores"`
	PerCorePct []*float64 `json:"per_core_pct"` // index == core id; nil entry = core missing/offline
}

// MemoryStats reports the unified CPU+GPU memory pool. Swap is a secondary
// indicator and is never folded into the used numbers.
type MemoryStats struct {
	TotalKiB     *int64   `json:"total_kib"`
	AvailableKiB *int64   `json:"available_kib"` // MemAvailable (or the DGX stream's equivalent)
	UsedKiB      *int64   `json:"used_kib"`      // total - available, clamped >= 0
	UsedPct      *float64 `json:"used_pct"`
	SwapTotalKiB *int64   `json:"swap_total_kib"`
	SwapUsedKiB  *int64   `json:"swap_used_kib"`
}

// GPUStats reports the single GPU. Memory fields are intentionally absent:
// on GB10 unified-memory systems every GPU memory query returns Not Supported.
type GPUStats struct {
	Name       string   `json:"name"`
	UtilPct    *float64 `json:"util_pct"`
	TempC      *float64 `json:"temp_c"`
	PowerW     *float64 `json:"power_w"`
	SMClockMHz *int64   `json:"sm_clock_mhz"`
}

// ThermStats reports thermal zones. On DGX Spark the zones are unlabeled
// acpitz entries; SocMaxC is the max across them, labeled honestly in the UI
// (configurable soc_label) rather than presented as a CPU die temperature.
type ThermStats struct {
	SocMaxC      *float64    `json:"soc_max_c"`
	ZoneCount    int         `json:"zone_count"`
	ZoneMaxIndex *int        `json:"zone_max_index"`
	Zones        []ThermZone `json:"zones"`
}

// ThermZone is one thermal zone reading.
type ThermZone struct {
	Index int      `json:"index"`
	Type  string   `json:"type"`
	TempC *float64 `json:"temp_c"` // nil when the zone was unreadable
}
