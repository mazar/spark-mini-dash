// Package agent exposes the per-node metrics API: /metrics (the last
// collected Snapshot) and /healthz.
package agent

import (
	"encoding/json"
	"net/http"
	"time"

	"spark-mini-dash/internal/metrics"
	"spark-mini-dash/internal/version"
)

// Server serves the agent's HTTP API off a collector. The handler only reads
// the collector's cached snapshot — zero computation on the request path.
type Server struct {
	col      *metrics.Collector
	hostname string
	mock     bool
}

func New(col *metrics.Collector, hostname string, mock bool) *Server {
	return &Server{col: col, hostname: hostname, mock: mock}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/metrics", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// handleMetrics serves the last snapshot. Before the first tick completes it
// serves the schema skeleton with null sections so pollers always get a
// well-formed body.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	snap := s.col.Snapshot()
	if snap == nil {
		snap = &metrics.Snapshot{
			Schema:    metrics.SchemaVer,
			Hostname:  s.hostname,
			Version:   version.Version,
			Mock:      s.mock,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(snap); err != nil {
		// Headers already sent; nothing better to do than log-grade silence.
		return
	}
}
