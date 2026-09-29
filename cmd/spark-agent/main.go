// spark-agent collects this machine's resource telemetry (CPU, unified
// memory, GPU, thermal) and serves it as JSON on /metrics for the dashboard.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"spark-mini-dash/internal/agent"
	"spark-mini-dash/internal/metrics"
	"spark-mini-dash/internal/version"
)

// readSecretFile loads a secret from a file (first value, trimmed). Secrets
// belong in files, not command-line flags: flags are world-readable via
// /proc/<pid>/cmdline on multi-user machines.
func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func main() {
	addr := flag.String("addr", ":9105", "listen address")
	interval := flag.Duration("interval", 2*time.Second, "sampling interval")
	mock := flag.Bool("mock", false, "serve synthetic values (no system reads)")
	thermalZone := flag.Int("thermal-zone", -1, "pin SoC temperature to one thermal zone index (-1 = max across zones)")
	source := flag.String("source", "auto", "GPU/memory source: auto | stream | smi")
	telemetryURL := flag.String("telemetry-url", "http://127.0.0.1:11000", "dgx-dashboard-service base URL (telemetry stream source)")
	dashToken := flag.String("dashboard-token", "", "pre-minted dgx-dashboard JWT (prefer --dashboard-token-file: flags are readable in /proc)")
	dashTokenFile := flag.String("dashboard-token-file", "", "file containing the dgx-dashboard JWT (recommended; e.g. 0600 root-owned)")
	dashUser := flag.String("dashboard-user", "", "dgx-dashboard login username (optional)")
	dashPass := flag.String("dashboard-password", "", "dgx-dashboard login password (prefer --dashboard-password-file)")
	dashPassFile := flag.String("dashboard-password-file", "", "file containing the dgx-dashboard password")
	flag.Parse()

	switch *source {
	case metrics.SourceAuto, metrics.SourceStream, metrics.SourceSMI:
	default:
		log.Fatalf("invalid --source %q (want auto, stream or smi)", *source)
	}

	token := *dashToken
	if token == "" && *dashTokenFile != "" {
		t, err := readSecretFile(*dashTokenFile)
		if err != nil {
			log.Fatalf("dashboard-token-file: %v", err)
		}
		token = t
	}
	password := *dashPass
	if password == "" && *dashPassFile != "" {
		p, err := readSecretFile(*dashPassFile)
		if err != nil {
			log.Fatalf("dashboard-password-file: %v", err)
		}
		password = p
	}

	hostname, _ := os.Hostname()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sampler metrics.Sampler
	if *mock {
		log.Printf("mock mode: serving synthetic values")
		sampler = metrics.NewMockSampler(hostname)
	} else {
		rs := metrics.NewRealSampler(*thermalZone, *source, *telemetryURL, token, *dashUser, password)
		rs.Start(ctx)
		sampler = rs
	}
	col := metrics.NewCollector(sampler, *interval)
	go col.Run(ctx)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           agent.New(col, hostname, *mock).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("spark-agent %s listening on %s (source=%s mock=%v)", version.Version, *addr, *source, *mock)

	select {
	case err := <-errCh:
		log.Fatalf("http server: %v", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
