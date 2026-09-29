package metrics

import (
	"os"
	"strconv"
	"strings"
)

// statLines holds raw CPU jiffies from one read of /proc/stat.
// Index 0 is the aggregate "cpu" line; index i>0 is core i-1.
type statLines struct {
	idle []uint64 // idle + iowait
	busy []uint64 // user + nice + system + irq + softirq + steal
}

// readProcStat parses /proc/stat (or any path with the same shape).
// guest/guest_nice are excluded from busy: the kernel already counts them
// inside user/nice.
func readProcStat(path string) (*statLines, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s statLines
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		var f [8]uint64
		for i := 0; i < 8; i++ {
			v, err := strconv.ParseUint(fields[1+i], 10, 64)
			if err != nil {
				f[i] = 0
				continue
			}
			f[i] = v
		}
		idle := f[3] + f[4] // idle + iowait
		busy := f[0] + f[1] + f[2] + f[5] + f[6] + f[7]
		idx := 0
		if fields[0] != "cpu" {
			n, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu"))
			if err != nil || n < 0 {
				continue
			}
			idx = n + 1
		}
		for len(s.idle) <= idx {
			s.idle = append(s.idle, 0)
			s.busy = append(s.busy, 0)
		}
		s.idle[idx] = idle
		s.busy[idx] = busy
	}
	if len(s.idle) == 0 {
		return nil, os.ErrInvalid
	}
	return &s, nil
}

// utilBetween computes utilization % between two reads for line index i.
// Returns nil when there is no usable delta (first sample, zero window).
func utilBetween(prev, cur *statLines, i int) *float64 {
	if prev == nil || i >= len(prev.idle) || i >= len(cur.idle) {
		return nil
	}
	dTotal := (cur.busy[i] + cur.idle[i]) - (prev.busy[i] + prev.idle[i])
	dBusy := cur.busy[i] - prev.busy[i]
	if dTotal <= 0 {
		return nil
	}
	return clampPct(100 * float64(dBusy) / float64(dTotal))
}

func clampPct(v float64) *float64 {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return &v
}

// cpuStats builds CPUStats from the current read and the previous read.
func cpuStats(prev, cur *statLines) *CPUStats {
	cores := len(cur.idle) - 1
	if cores < 0 {
		cores = 0
	}
	cs := &CPUStats{Cores: cores, PerCorePct: make([]*float64, cores)}
	cs.UtilPct = utilBetween(prev, cur, 0)
	for c := 0; c < cores; c++ {
		if prev != nil && (c+1) >= len(prev.idle) {
			cs.PerCorePct[c] = nil // core appeared since last read: no delta window
			continue
		}
		cs.PerCorePct[c] = utilBetween(prev, cur, c+1)
	}
	return cs
}
