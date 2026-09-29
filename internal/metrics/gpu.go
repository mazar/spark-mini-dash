package metrics

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// gpuSmiQuery is the nvidia-smi field list. Never query memory.* here: on
// GB10 unified-memory systems every GPU memory query reports "[N/A]".
const gpuSmiQuery = "name,utilization.gpu,temperature.gpu,power.draw,clocks.sm"

var numField = regexp.MustCompile(`^-?[0-9]+([.,][0-9]+)?$`)

// gpuReader shells out to nvidia-smi once per tick. This is exactly what the
// DGX Dashboard service does internally; exec (rather than NVML) keeps the
// build cgo-free so the same binary cross-compiles to every target.
type gpuReader struct {
	bin     string
	missing bool
	timeout time.Duration
}

func newGPUReader(timeout time.Duration) *gpuReader {
	p, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return &gpuReader{missing: true, timeout: timeout}
	}
	return &gpuReader{bin: p, timeout: timeout}
}

// sample runs one query. A failed, timed-out or empty run returns an error
// (the whole GPU section becomes null); a field the driver could not report
// becomes a nil field while the rest of the section is kept.
func (g *gpuReader) sample() (*GPUStats, error) {
	if g.missing {
		return nil, errNoSMI
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.bin,
		"--query-gpu="+gpuSmiQuery, "--format=csv,noheader,nounits")
	// nvidia-smi formats decimals per the active locale (verified: "30,33"
	// under a comma locale). Force C and normalize in the parser regardless.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseSMI(string(out))
}

var errNoSMI = errStr("nvidia-smi not found")

type errStr string

func (e errStr) Error() string { return string(e) }

// parseSMI parses the first non-empty CSV line:
// "NVIDIA GB10, 96, 67, 30.33, 2398". Under a comma locale the power value
// arrives as "30,33", which adds a CSV field; the layout is therefore
// parsed positionally: name | util | temp | power (possibly comma-split) |
// clock (always the last field).
func parseSMI(out string) (*GPUStats, error) {
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		return nil, errStr("nvidia-smi output empty")
	}
	// GB10 is single-GPU; if several lines ever appear, the first is taken
	// and the rest ignored (documented limitation).
	parts := strings.Split(line, ",")
	if len(parts) < 5 {
		return nil, errStr("nvidia-smi output malformed")
	}
	last := len(parts) - 1
	g := &GPUStats{Name: strings.TrimSpace(parts[0])}
	g.UtilPct = parseSMINum(parts[1])
	g.TempC = parseSMINum(parts[2])
	// Power is everything between temp and the final clock field; a
	// comma-decimal value split across two fields rejoins with ".".
	powerParts := parts[3:last]
	power := strings.Join(powerParts, ".")
	g.PowerW = parseSMINum(power)
	if c := parseSMINum(parts[last]); c != nil {
		mhz := int64(*c)
		g.SMClockMHz = &mhz
	}
	return g, nil
}

// parseSMINum accepts ints or decimals with "." or "," separators; "[N/A]",
// "N/A" and anything unparseable become nil.
func parseSMINum(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" || s == "[N/A]" {
		return nil
	}
	if !numField.MatchString(s) {
		return nil
	}
	s = strings.Replace(s, ",", ".", 1)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
