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
	"spark-mini-dash/internal/pairbridge"
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

	// Optional read-only PAIR bridge: join the cluster as a passive member to
	// display live request routing. It never controls anything.
	var bridge *pairbridge.Bridge
	if cfg.Pair.Enabled {
		bridge = pairbridge.New(pairbridge.Config{
			BinariesDir:  cfg.Pair.BinariesDir,
			StateDir:     cfg.Pair.StateDir,
			ClusterPort:  cfg.Pair.ClusterPort,
			WorkloadPort: cfg.Pair.WorkloadPort,
		})
		go func() {
			if err := bridge.Run(ctx); err != nil {
				log.Printf("pair bridge: %v", err)
			}
		}()
		log.Printf("pair bridge enabled — cluster :%d, workload :%d, state %s",
			cfg.Pair.ClusterPort, cfg.Pair.WorkloadPort, cfg.Pair.StateDir)
		defer bridge.Stop()
	}

	mux := http.NewServeMux()
	mux.Handle("/", web.Handler())
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		st := struct {
			Schema int                `json:"schema"`
			Now    string             `json:"now"`
			Config outConfig          `json:"config"`
			Nodes  []poller.NodeState `json:"nodes"`
			Pair   *pairbridge.State  `json:"pair,omitempty"`
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
		if bridge != nil {
			s := bridge.Snapshot()
			st.Pair = &s
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("POST /api/pair/respond", func(w http.ResponseWriter, r *http.Request) {
		if bridge == nil {
			http.Error(w, "pair bridge not enabled", http.StatusConflict)
			return
		}
		var req struct {
			InviteID string `json:"inviteId"`
			Pin      string `json:"pin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Pin == "" {
			http.Error(w, "expected {inviteId, pin}", http.StatusBadRequest)
			return
		}
		if err := bridge.RespondToInvite(r.Context(), req.Pin); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
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
