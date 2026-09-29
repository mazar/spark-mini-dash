package metrics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// StreamFreshWindow is how long after the last pushed message the stream
// sample is considered usable before the collector falls back to nvidia-smi.
const StreamFreshWindow = 5 * time.Second

// StreamSample is the latest normalized observation from the DGX Dashboard
// telemetry stream.
type StreamSample struct {
	GPUUtilPct  *float64
	GPUTempC    *float64
	GPUPowerW   *float64
	MemTotalKiB *int64
	MemAvailKiB *int64
	At          time.Time
}

// StreamConfig selects the DGX Dashboard telemetry stream source.
type StreamConfig struct {
	BaseURL  string // e.g. http://127.0.0.1:11000
	Token    string // pre-minted JWT; wins over user/password
	User     string // dashboard login username (optional)
	Password string // dashboard login password (optional)
}

// dashboardPayload mirrors the dgx-dashboard-service stream frame. The
// service is closed source, so the field names come from its own embedded
// frontend (verified in the binary) and from a live capture of the stream:
// {"TelemetryForGPUs":[{"percentage_utilization":...,"memory_available_in_kib":
// ...,"memory_total_in_kib":...,"temperature_in_c":...,"power_draw_in_w":...}]}
type streamGPU struct {
	Utilization *float64 `json:"percentage_utilization"`
	TempC       *float64 `json:"temperature_in_c"`
	PowerW      *float64 `json:"power_draw_in_w"`
	MemAvailKiB *int64   `json:"memory_available_in_kib"`
	MemTotalKiB *int64   `json:"memory_total_in_kib"`
}

type streamPayload struct {
	TelemetryForGPUs []streamGPU `json:"TelemetryForGPUs"`
	Error            string      `json:"error"`
}

// DashboardStream maintains a long-lived connection to the preinstalled
// dgx-dashboard-service telemetry stream (GET /api/v1/gpu_telemetry/stream).
//
// The service requires a JWT. Either supply a pre-minted token (preferred)
// or username/password, which are exchanged once via POST /api/v1/login.
//
// Framing is handled tolerantly: every non-empty line is stripped of an
// optional SSE "data:" prefix and parsed as JSON, so both SSE and NDJSON
// framing work. Reconnects use capped exponential backoff. Latest() exposes
// the freshest sample so the collector can fall back to nvidia-smi whenever
// the stream is absent, refused (auth changed) or stale.
type DashboardStream struct {
	cfg    StreamConfig
	client *http.Client

	mu      sync.Mutex
	cur     StreamSample
	have    bool
	lastErr string
}

func NewDashboardStream(cfg StreamConfig) *DashboardStream {
	return &DashboardStream{
		cfg: cfg,
		client: &http.Client{
			Timeout: 0, // streaming; per-read deadlines below
			// Do not follow redirects: a login that bounces to the UI page is
			// an auth failure, not a success to chase.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Run keeps the stream alive until ctx is cancelled. Each cycle holds one
// connection until it ends (EOF, error or drop), then reconnects with
// capped exponential backoff.
func (s *DashboardStream) Run(ctx context.Context) {
	backoff := 2 * time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		done, err := s.connect(ctx)
		if err != nil {
			s.setErr(err.Error())
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = 2 * time.Second // healthy connection: reset
		select {
		case <-ctx.Done():
			return
		case <-done:
		}
	}
}

// connect opens the stream and pumps messages until the connection ends;
// the returned channel closes when the pump exits.
func (s *DashboardStream) connect(ctx context.Context) (chan struct{}, error) {
	token := s.cfg.Token
	if token == "" && s.cfg.User != "" {
		t, err := s.login()
		if err != nil {
			return nil, err
		}
		token = t
	}
	if token == "" && s.cfg.User == "" {
		return nil, fmt.Errorf("no dashboard token or credentials configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(s.cfg.BaseURL, "/")+"/api/v1/gpu_telemetry/stream", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	notify := make(chan struct{})
	go func() {
		defer close(notify)
		s.pump(resp.Body)
	}()
	return notify, nil
}

// pump reads the stream body line by line. Both SSE ("data: {...}") and
// NDJSON ("{...}") framing are accepted; anything else is skipped.
func (s *DashboardStream) pump(body io.ReadCloser) {
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(line[5:])
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var p streamPayload
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			continue
		}
		if p.Error != "" {
			s.setErr(p.Error)
			continue
		}
		if len(p.TelemetryForGPUs) == 0 {
			continue
		}
		g := p.TelemetryForGPUs[0]
		s.publish(StreamSample{
			GPUUtilPct:  clampRef(g.Utilization),
			GPUTempC:    g.TempC,
			GPUPowerW:   g.PowerW,
			MemTotalKiB: g.MemTotalKiB,
			MemAvailKiB: g.MemAvailKiB,
			At:          time.Now(),
		})
	}
}

// login exchanges username/password for a JWT via POST /api/v1/login. The
// exact success shape is not documented; both a JSON body with "token" or
// "access_token" and a 2xx-with-Location redirect flow are handled. Bad
// credentials surface as an error (302 to the login page, non-2xx, ...).
func (s *DashboardStream) login() (string, error) {
	body, _ := json.Marshal(map[string]string{"username": s.cfg.User, "password": s.cfg.Password})
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimSuffix(s.cfg.BaseURL, "/")+"/api/v1/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("dashboard login HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("dashboard login: unexpected response")
	}
	if out.Token != "" {
		return out.Token, nil
	}
	if out.AccessToken != "" {
		return out.AccessToken, nil
	}
	return "", fmt.Errorf("dashboard login: no token in response")
}

func (s *DashboardStream) publish(v StreamSample) {
	s.mu.Lock()
	s.cur = v
	s.have = true
	s.mu.Unlock()
}

func (s *DashboardStream) setErr(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

// Latest returns the freshest sample if it arrived within maxAge.
func (s *DashboardStream) Latest(maxAge time.Duration) (StreamSample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.have || time.Since(s.cur.At) > maxAge {
		return StreamSample{}, false
	}
	return s.cur, true
}

// LastError returns the most recent stream error text ("" when healthy).
func (s *DashboardStream) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func clampRef(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	if c < 0 {
		c = 0
	}
	if c > 100 {
		c = 100
	}
	return &c
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
