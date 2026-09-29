// Package config loads the dashboard configuration from flags plus an
// optional JSON file (flags win). Zero dependencies: encoding/json only.
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Thresholds drive the UI's severity colors (temp values) — all tunable.
type Thresholds struct {
	GPUTempWarnC     float64 `json:"gpu_temp_warn_c"`
	GPUTempSeriousC  float64 `json:"gpu_temp_serious_c"`
	GPUTempCriticalC float64 `json:"gpu_temp_critical_c"`
	SocTempWarnC     float64 `json:"soc_temp_warn_c"`
	SocTempSeriousC  float64 `json:"soc_temp_serious_c"`
	SocTempCriticalC float64 `json:"soc_temp_critical_c"`
	MemWarnPct       float64 `json:"mem_warn_pct"`
	MemCritPct       float64 `json:"mem_crit_pct"`
}

// DefaultNodeColors is the CVD-validated hue order for node identity (bars
// follow the node, like NVIDIA Sync's Resource Monitor). All pairs pass the
// perceptual gates on the dark surface; do not shuffle casually.
var DefaultNodeColors = []string{
	"#3987e5", // blue
	"#d95926", // orange
	"#9085e9", // violet
	"#199e70", // aqua
	"#c98500", // yellow
	"#d55181", // magenta
}

// Config is the full dashboard configuration.
type Config struct {
	Addr           string     `json:"addr"`
	PollIntervalMS int        `json:"poll_interval_ms"`
	TimeoutMS      int        `json:"timeout_ms"`
	HistoryLen     int        `json:"history_len"`
	StaleAfterS    float64    `json:"stale_after_s"`
	OfflineAfterS  float64    `json:"offline_after_s"`
	SocLabel       string     `json:"soc_label"`
	Nodes          []NodeCfg  `json:"nodes"`
	NodeColors     []string   `json:"node_colors"`
	Thresholds     Thresholds `json:"thresholds"`
}

// NodeCfg is a node entry in the config file.
type NodeCfg struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Color string `json:"color,omitempty"`
}

func Defaults() *Config {
	return &Config{
		Addr:           ":8080",
		PollIntervalMS: 2000,
		TimeoutMS:      1500,
		HistoryLen:     300, // 300 pts @ 2s = 10 min
		StaleAfterS:    3,
		OfflineAfterS:  15,
		SocLabel:       "SoC",
		NodeColors:     DefaultNodeColors,
		Thresholds: Thresholds{
			GPUTempWarnC:     80,
			GPUTempSeriousC:  90,
			GPUTempCriticalC: 100,
			SocTempWarnC:     95,
			SocTempSeriousC:  105,
			SocTempCriticalC: 115,
			MemWarnPct:       85,
			MemCritPct:       95,
		},
		Nodes: []NodeCfg{{URL: "http://127.0.0.1:9105"}}, // default: local agent
	}
}

// Load parses flags (args) and merges the optional JSON config file; flag
// values override file values where both are set. Flags are the interface;
// the file carries per-node detail and thresholds.
func Load(args []string) (*Config, error) {
	cfg := Defaults()
	fs := flag.NewFlagSet("spark-dash", flag.ContinueOnError)
	addr := fs.String("addr", cfg.Addr, "listen address")
	configPath := fs.String("config", "", "JSON config file path")
	nodesFlag := fs.String("nodes", "", "comma-separated name=URL list (overrides file nodes), e.g. spark-1=http://10.0.0.11:9105,spark-2=http://10.0.0.12:9105")
	historyLen := fs.Int("history-len", 0, "history points kept per metric (0 = default)")
	pollMS := fs.Int("poll-ms", 0, "poll interval in ms (0 = default)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			return nil, fmt.Errorf("config file: %w", err)
		}
		fileCfg := Defaults()
		if err := json.Unmarshal(data, fileCfg); err != nil {
			return nil, fmt.Errorf("config file %s: %w", *configPath, err)
		}
		// File wins over defaults; explicit flags win over the file.
		fileCfg.Addr = cfg.Addr
		if *addr != cfg.Addr {
			fileCfg.Addr = *addr
		}
		if *historyLen > 0 {
			fileCfg.HistoryLen = *historyLen
		}
		if *pollMS > 0 {
			fileCfg.PollIntervalMS = *pollMS
		}
		if fileCfg.NodeColors == nil || len(fileCfg.NodeColors) == 0 {
			fileCfg.NodeColors = DefaultNodeColors
		}
		if fileCfg.Nodes == nil {
			fileCfg.Nodes = cfg.Nodes
		}
		cfg = fileCfg
	} else {
		if *addr != cfg.Addr {
			cfg.Addr = *addr
		}
		if *historyLen > 0 {
			cfg.HistoryLen = *historyLen
		}
		if *pollMS > 0 {
			cfg.PollIntervalMS = *pollMS
		}
	}

	if *nodesFlag != "" {
		cfg.Nodes = parseNodes(*nodesFlag)
	}
	if len(cfg.NodeColors) == 0 {
		cfg.NodeColors = DefaultNodeColors
	}
	for i := range cfg.Nodes {
		n := &cfg.Nodes[i]
		if n.Color == "" {
			n.Color = cfg.NodeColors[i%len(cfg.NodeColors)]
		}
		if n.URL != "" && !strings.HasSuffix(n.URL, "/") {
			n.URL = strings.TrimSuffix(n.URL, "/")
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// parseNodes parses "name=url,other=url2"; a bare URL is allowed (name
// empty, later taken from the agent hostname).
func parseNodes(s string) []NodeCfg {
	var out []NodeCfg
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var n NodeCfg
		if name, u, ok := strings.Cut(part, "="); ok && strings.Contains(u, "://") {
			n.Name, n.URL = strings.TrimSpace(name), strings.TrimSpace(u)
		} else {
			n.URL = part
		}
		out = append(out, n)
	}
	return out
}

func (c *Config) validate() error {
	if c.PollIntervalMS < 500 || c.PollIntervalMS > 60000 {
		return fmt.Errorf("poll_interval_ms %d out of range [500,60000]", c.PollIntervalMS)
	}
	if c.HistoryLen < 10 || c.HistoryLen > 3600 {
		return fmt.Errorf("history_len %d out of range [10,3600]", c.HistoryLen)
	}
	if c.TimeoutMS < 200 || c.TimeoutMS >= c.PollIntervalMS {
		return fmt.Errorf("timeout_ms %d must be in [200, poll_interval_ms)", c.TimeoutMS)
	}
	if c.StaleAfterS <= 0 || c.OfflineAfterS <= c.StaleAfterS {
		return fmt.Errorf("stale_after_s/offline_after_s must satisfy 0 < stale < offline")
	}
	if len(c.Nodes) == 0 {
		return fmt.Errorf("no nodes configured (use --nodes or the config file)")
	}
	for i, n := range c.Nodes {
		if n.URL == "" {
			return fmt.Errorf("node %d: empty url", i)
		}
		u, err := url.Parse(n.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("node %s: url %q must be http(s)", n.Name, n.URL)
		}
	}
	return nil
}
