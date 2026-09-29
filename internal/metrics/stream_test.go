package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const streamFrame = `{"TelemetryForGPUs":[{"percentage_utilization":42,"memory_available_in_kib":4900456,"memory_total_in_kib":127598544,"gpu_memory_in_use_in_mb":0,"cpu_memory_in_use_in_mb":0,"temperature_in_c":64,"power_draw_in_w":15.38,"power_limit_in_w":0}]}`

func TestDashboardStreamFramingAndAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":"Token validation failed"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/gpu_telemetry/stream" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", streamFrame) // SSE framing
		fl.Flush()
		fmt.Fprintf(w, "\n%s\n", streamFrame) // NDJSON framing
		fl.Flush()
		<-r.Context().Done() // hold the stream open
	}))
	defer srv.Close()

	s := NewDashboardStream(StreamConfig{BaseURL: srv.URL, Token: "tok"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if v, ok := s.Latest(StreamFreshWindow); ok {
			if v.GPUUtilPct == nil || *v.GPUUtilPct != 42 {
				t.Fatalf("util = %v, want 42", v.GPUUtilPct)
			}
			if *v.GPUTempC != 64 || *v.GPUPowerW != 15.38 {
				t.Fatalf("temp/power wrong: %v %v", *v.GPUTempC, *v.GPUPowerW)
			}
			if v.MemTotalKiB == nil || *v.MemTotalKiB != 127598544 || *v.MemAvailKiB != 4900456 {
				t.Fatalf("memory wrong: %v %v", v.MemTotalKiB, v.MemAvailKiB)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no fresh sample; last error: %q", s.LastError())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.LastError(); err != "" {
		t.Fatalf("stream should be healthy, got error %q", err)
	}
}

func TestDashboardStreamAuthRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"Token validation failed"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	s := NewDashboardStream(StreamConfig{BaseURL: srv.URL, Token: "wrong"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for s.LastError() == "" {
		if time.Now().After(deadline) {
			t.Fatal("auth failure never surfaced")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := s.Latest(StreamFreshWindow); ok {
		t.Fatal("no sample must be published on auth failure")
	}
}
