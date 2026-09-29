package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsValid(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if cfg.Addr != ":8080" || cfg.PollIntervalMS != 2000 || cfg.HistoryLen != 300 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if len(cfg.Nodes) != 1 || cfg.Nodes[0].URL != "http://127.0.0.1:9105" {
		t.Fatalf("default node wrong: %+v", cfg.Nodes)
	}
	if cfg.Nodes[0].Color != DefaultNodeColors[0] {
		t.Fatalf("color not assigned: %q", cfg.Nodes[0].Color)
	}
}

func TestNodesFlag(t *testing.T) {
	cfg, err := Load([]string{
		"--nodes", "spark-1=http://10.0.0.11:9105,http://10.0.0.12:9105,spark-3=http://10.0.0.13:9105",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(cfg.Nodes))
	}
	if cfg.Nodes[0].Name != "spark-1" || cfg.Nodes[1].Name != "" {
		t.Fatalf("names wrong: %+v", cfg.Nodes)
	}
	want := []string{DefaultNodeColors[0], DefaultNodeColors[1], DefaultNodeColors[2]}
	for i, c := range want {
		if cfg.Nodes[i].Color != c {
			t.Fatalf("node %d color = %q, want %q", i, cfg.Nodes[i].Color, c)
		}
	}
}

func TestConfigFileMergeAndFlagPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{
	  "poll_interval_ms": 5000,
	  "history_len": 120,
	  "soc_label": "Package",
	  "thresholds": { "gpu_temp_warn_c": 70 },
	  "nodes": [ {"name":"spark-5a4e","url":"http://10.0.0.11:9105"},
	             {"name":"spark-8a27","url":"http://10.0.0.12:9105","color":"#ff0000"} ]
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollIntervalMS != 5000 || cfg.HistoryLen != 120 {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	if cfg.SocLabel != "Package" {
		t.Fatalf("soc_label = %q", cfg.SocLabel)
	}
	if cfg.Thresholds.GPUTempWarnC != 70 || cfg.Thresholds.GPUTempSeriousC != 90 {
		t.Fatalf("threshold merge wrong: %+v", cfg.Thresholds)
	}
	if cfg.Nodes[1].Color != "#ff0000" || cfg.Nodes[0].Color != DefaultNodeColors[0] {
		t.Fatalf("color assignment wrong: %+v", cfg.Nodes)
	}

	// Flag overrides file.
	cfg, err = Load([]string{"--config", path, "--poll-ms", "3000", "--addr", ":9090"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollIntervalMS != 3000 || cfg.Addr != ":9090" {
		t.Fatalf("flags lost to file: %+v", cfg)
	}
}

func TestBadConfigs(t *testing.T) {
	if _, err := Load([]string{"--config", filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Fatal("missing config file must error")
	}
	bad := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	if _, err := Load([]string{"--config", bad}); err == nil {
		t.Fatal("bad JSON must error")
	}
	if _, err := Load([]string{"--poll-ms", "100"}); err == nil {
		t.Fatal("poll below range must error")
	}
	if _, err := Load([]string{"--nodes", "ftp://x"}); err == nil {
		t.Fatal("non-http URL must error")
	}
}
