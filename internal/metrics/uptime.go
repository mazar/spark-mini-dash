package metrics

import (
	"os"
	"strconv"
	"strings"
)

// readUptime parses /proc/uptime (first field: seconds with decimals).
// Returns nil on non-Linux systems.
func readUptime(path string) *int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return nil
	}
	s := int64(v)
	return &s
}
