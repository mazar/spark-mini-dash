// spark-dash polls the agents of a DGX Spark cluster and serves the
// embedded 1920x480 wall dashboard plus its /api/state feed.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"spark-mini-dash/internal/config"
	"spark-mini-dash/internal/poller"
	"spark-mini-dash/internal/version"
	"spark-mini-dash/web"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	interval := time.Duration(cfg.PollIntervalMS) * time.Millisecond
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	staleAfter := time.Duration(cfg.StaleAfterS * float64(time.Second))
	offlineAfter := time.Duration(cfg.OfflineAfterS * float64(time.Second))

	client := &http.Client{Timeout: timeout}
	nodes := make([]*poller.Node, 0, len(cfg.Nodes))
	for _, nc := range cfg.Nodes {
		n := poller.NewNode(poller.NodeConfig{Name: nc.Name, URL: nc.URL, Color: nc.Color},
			client, interval, timeout, staleAfter, offlineAfter, cfg.HistoryLen, nil)
		nodes = append(nodes, n)
		go n.Run(ctx)
	}
	log.Printf("spark-dash %s on %s — polling %d node(s) every %s", version.Version, cfg.Addr, len(nodes), interval)

	mux := http.NewServeMux()
	mux.Handle("/", web.Handler())
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		st := struct {
			Schema int                `json:"schema"`
			Now    string             `json:"now"`
			Config outConfig          `json:"config"`
			Nodes  []poller.NodeState `json:"nodes"`
		}{
			Schema: 1,
			Now:    time.Now().UTC().Format(time.RFC3339),
			Config: outConfig{
				PollIntervalMS: cfg.PollIntervalMS,
				HistoryLen:     cfg.HistoryLen,
				SocLabel:       cfg.SocLabel,
				Thresholds:     cfg.Thresholds,
			},
		}
		for _, n := range nodes {
			st.Nodes = append(st.Nodes, n.State())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})

	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		log.Fatalf("http server: %v", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

type outConfig struct {
	PollIntervalMS int               `json:"poll_interval_ms"`
	HistoryLen     int               `json:"history_len"`
	SocLabel       string            `json:"soc_label"`
	Thresholds     config.Thresholds `json:"thresholds"`
}
