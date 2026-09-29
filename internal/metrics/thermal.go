package metrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const thermalBaseDefault = "/sys/class/thermal"

// readThermal reads /sys/class/thermal/thermal_zone*/{type,temp}.
//
// On DGX Spark there are seven zones and every one reports the generic type
// "acpitz" with no CPU/GPU mapping documented anywhere, so the honest
// aggregate is the max across zones (displayed as "SoC max" in the UI, label
// configurable). Zones that are unreadable or physically impossible
// (< -50 C or > 150 C) are skipped. A pinned zone (pinned >= 0) reports that
// zone alone; if it cannot be read the whole section is nil.
func readThermal(base string, pinned int) *ThermStats {
	if base == "" {
		base = thermalBaseDefault
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil // no sysfs thermal (Windows, restricted container)
	}
	var zones []ThermZone
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "thermal_zone") {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(name, "thermal_zone"))
		if err != nil {
			continue
		}
		if pinned >= 0 && idx != pinned {
			continue
		}
		z := ThermZone{Index: idx}
		if b, err := os.ReadFile(filepath.Join(base, name, "type")); err == nil {
			z.Type = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile(filepath.Join(base, name, "temp")); err == nil {
			if v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
				c := v / 1000
				z.TempC = &c
			}
		}
		if z.TempC == nil || *z.TempC <= -50 || *z.TempC > 150 {
			continue
		}
		zones = append(zones, z)
	}
	if len(zones) == 0 {
		return nil
	}
	ts := &ThermStats{ZoneCount: len(zones), Zones: zones}
	maxI := 0
	for i, z := range zones {
		if *z.TempC > *zones[maxI].TempC {
			maxI = i
		}
	}
	ts.SocMaxC = zones[maxI].TempC
	idx := zones[maxI].Index
	ts.ZoneMaxIndex = &idx
	return ts
}
