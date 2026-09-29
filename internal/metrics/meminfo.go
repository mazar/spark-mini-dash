package metrics

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// readMeminfo parses /proc/meminfo. Despite the "kB" label the values are
// kiB; we keep raw kiB everywhere and convert to decimal GB only at render
// time, using one consistent unit (the official DGX Dashboard's documented
// bug is mixing GiB values with GB totals).
//
// "Used" means total - MemAvailable (reclaimable cache counted as usable),
// which is the same semantics the DGX Dashboard stream exposes. On old
// kernels without MemAvailable (pre-3.14, e.g. some Pi images) fall back to
// MemFree + Buffers + Cached. Swap is reported as-is and never folded in.
func readMeminfo(path string) *MemoryStats {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var m MemoryStats
	var memFree, buffers, cached, swapFree *int64
	haveAvail := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		val := v
		switch key {
		case "MemTotal":
			m.TotalKiB = &val
		case "MemAvailable":
			m.AvailableKiB = &val
			haveAvail = true
		case "MemFree":
			memFree = &val
		case "Buffers":
			buffers = &val
		case "Cached":
			cached = &val
		case "SwapTotal":
			m.SwapTotalKiB = &val
		case "SwapFree":
			swapFree = &val
		}
	}
	if m.TotalKiB == nil || *m.TotalKiB <= 0 {
		return nil
	}
	if !haveAvail {
		if memFree == nil {
			return nil
		}
		est := *memFree
		if buffers != nil {
			est += *buffers
		}
		if cached != nil {
			est += *cached
		}
		m.AvailableKiB = &est
	}
	if *m.AvailableKiB < 0 {
		zero := int64(0)
		m.AvailableKiB = &zero
	}
	used := *m.TotalKiB - *m.AvailableKiB
	if used < 0 {
		used = 0
	}
	m.UsedKiB = &used
	pct := 100 * float64(used) / float64(*m.TotalKiB)
	m.UsedPct = clampPct(pct)
	if m.SwapTotalKiB != nil && swapFree != nil {
		sw := *m.SwapTotalKiB - *swapFree
		if sw < 0 {
			sw = 0
		}
		m.SwapUsedKiB = &sw
	}
	return &m
}
