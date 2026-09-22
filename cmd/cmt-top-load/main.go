// cmt-top-load exercises browser-equivalent HTTP bootstrap, WebSocket delivery
// and conditional investigation polling against an explicitly identified local
// replay app. It deliberately refuses public targets and redirects.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/testutil/replay"
)

type summary struct {
	Target                    string                  `json:"target"`
	Profile                   string                  `json:"profile"`
	Seed                      int64                   `json:"seed"`
	DurationSeconds           float64                 `json:"durationSeconds"`
	Offered                   int                     `json:"offered"`
	Admitted                  int                     `json:"admitted"`
	InvestigatorsIntended     int                     `json:"investigatorsIntended"`
	InvestigatorProfilesReady int                     `json:"investigatorProfilesReady"`
	InvestigatorsWithReports  int                     `json:"investigatorsWithReports"`
	AdmissionTransientErrors  int                     `json:"admissionTransientErrors"`
	AdmissionRetries          int                     `json:"admissionRetries"`
	BaselineReady             int                     `json:"baselineReady"`
	SessionErrors             int                     `json:"sessionErrors"`
	SequenceGaps              int                     `json:"sequenceGaps"`
	Messages                  uint64                  `json:"messages"`
	PayloadBytes              uint64                  `json:"payloadBytes"`
	MessageTypes              map[string]messageStats `json:"messageTypes"`
	Snapshots                 uint64                  `json:"snapshots"`
	Contexts                  uint64                  `json:"contexts"`
	Reports                   uint64                  `json:"reports"`
	NotModified               uint64                  `json:"notModified"`
	ReportBytes               uint64                  `json:"reportBytes"`
	HTTPStatuses              map[string]int          `json:"httpStatuses"`
	LatencyMS                 map[string]float64      `json:"latencyMs"`
	LatencyObservations       map[string]int64        `json:"latencyObservations"`
	Resources                 resourceSummary         `json:"resources"`
	FirstErrors               []string                `json:"firstErrors,omitempty"`
	UpstreamBefore            replay.Stats            `json:"upstreamBefore"`
	UpstreamAfter             replay.Stats            `json:"upstreamAfter"`
	DropMetricsVerified       bool                    `json:"dropMetricsVerified"`
	DropMetrics               map[string]float64      `json:"dropMetrics"`
	OracleVerified            bool                    `json:"oracleVerified"`
	SemanticDigest            string                  `json:"semanticDigest,omitempty"`
	Pass                      bool                    `json:"pass"`
}
type messageStats struct {
	Messages uint64 `json:"messages"`
	Bytes    uint64 `json:"bytes"`
}
type resourceSummary struct {
	ProcessMetricsAvailable bool    `json:"processMetricsAvailable"`
	Samples                 int     `json:"samples"`
	CPUStartSeconds         float64 `json:"cpuStartSeconds"`
	CPUEndSeconds           float64 `json:"cpuEndSeconds"`
	AverageCPUCores         float64 `json:"averageCpuCores"`
	PeakRSSBytes            float64 `json:"peakRssBytes"`
	PeakGoroutines          float64 `json:"peakGoroutines"`
	PeakReportCacheBytes    float64 `json:"peakReportCacheBytes"`
	PeakQueueBytes          float64 `json:"peakQueueBytes"`
	GeneratorPeakHeapBytes  uint64  `json:"generatorPeakHeapBytes"`
}
type run struct {
	mu                       sync.Mutex
	s                        summary
	latencies                map[string][]float64
	random                   *rand.Rand // used only with mu held, for bounded reservoir sampling
	client                   *http.Client
	target, fixture, metrics string
	users, investigate       int
	legacyBootstrap          bool
	investigators            []investigatorOutcome
	ramp, duration           time.Duration
}

func localURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("must be a plain http:// literal loopback URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("non-loopback targets are forbidden")
	}
	return nil
}
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func execute() error {
	target := flag.String("target", "", "required local app URL")
	fixture := flag.String("fixture", "", "required local replay URL")
	metrics := flag.String("metrics", "", "optional local metrics URL origin (port9091)")
	users := flag.Int("users", 150, "concurrent browser-equivalent sessions")
	duration := flag.Duration("duration", 10*time.Second, "hold after ramp")
	ramp := flag.Duration("ramp", 3*time.Second, "evenly spaced session ramp; zero tests simultaneous startup")
	investigate := flag.Int("investigate-percent", 50, "percentage polling investigation pages")
	strict := flag.Bool("strict", false, "exit nonzero on admission, sequence, evidence, or transport failures")
	legacyBootstrap := flag.Bool("legacy-bootstrap", false, "exercise old clients that fetch full HTTP state before WS")
	flag.Parse()
	for _, raw := range []string{*target, *fixture} {
		if err := localURL(raw); err != nil {
			return fmt.Errorf("%q: %w", raw, err)
		}
	}
	if *metrics != "" {
		if err := localURL(*metrics); err != nil {
			return err
		}
	}
	if *users < 1 || *users > 1024 || *duration <= 0 || *duration > 24*time.Hour || *ramp < 0 || *investigate < 0 || *investigate > 100 {
		return errors.New("invalid load parameters")
	}
	r := &run{target: strings.TrimRight(*target, "/"), fixture: strings.TrimRight(*fixture, "/"), metrics: strings.TrimRight(*metrics, "/"), users: *users, investigate: *investigate, ramp: *ramp, duration: *duration, latencies: map[string][]float64{}, s: summary{Target: *target, HTTPStatuses: map[string]int{}, LatencyMS: map[string]float64{}, DropMetrics: map[string]float64{}}, client: &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 512, MaxIdleConnsPerHost: 512}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}}
	setup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	r.s.MessageTypes = make(map[string]messageStats)
	r.legacyBootstrap = *legacyBootstrap
	r.s.LatencyObservations = make(map[string]int64)
	r.random = rand.New(rand.NewSource(1))
	defer cancel()
	if err := r.verifyFixture(setup); err != nil {
		return err
	}
	beforeMetrics, err := r.readDropMetrics(setup)
	if err != nil {
		return fmt.Errorf("initial drop scrape: %w", err)
	}
	r.investigators = make([]investigatorOutcome, r.users)
	for i := range r.investigators {
		r.investigators[i].Intended = i*100/r.users < r.investigate
		if r.investigators[i].Intended {
			r.s.InvestigatorsIntended++
		}
	}
	r.sampleResources(setup)
	started := time.Now()
	ctx, stop := context.WithTimeout(context.Background(), r.ramp+r.duration)
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.sampleResources(ctx)
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < r.users; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			delay := time.Duration(int64(r.ramp) * int64(i) / int64(r.users))
			if !wait(ctx, delay) {
				return
			}
			r.session(ctx, i)
		}(i)
	}
	wg.Wait()
	stop()
	<-sampled
	r.s.DurationSeconds = time.Since(started).Seconds()
	finish, c := context.WithTimeout(context.Background(), 15*time.Second)
	defer c()
	r.sampleResources(finish)
	r.s.Resources.AverageCPUCores = (r.s.Resources.CPUEndSeconds - r.s.Resources.CPUStartSeconds) / r.s.DurationSeconds
	r.finish(finish)
	r.finalizeDropMetrics(finish, beforeMetrics)
	for key, values := range r.latencies {
		sort.Float64s(values)
		for _, q := range []int{50, 95, 99} {
			if len(values) > 0 {
				r.s.LatencyMS[fmt.Sprintf("%sP%d", key, q)] = values[int(math.Ceil(float64(len(values))*float64(q)/100))-1]
			}
		}
	}
	r.s.Pass = r.s.Admitted == r.users && r.s.BaselineReady == r.users && r.s.SessionErrors == 0 && r.s.SequenceGaps == 0 && r.s.OracleVerified && r.s.UpstreamAfter.UnexpectedRequests == 0 && investigatorsComplete(r.investigators) && (r.metrics == "" || r.s.DropMetricsVerified)
	for _, value := range r.s.DropMetrics {
		if value > 0 {
			r.s.Pass = false
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.s); err != nil {
		return err
	}
	if *strict && !r.s.Pass {
		return errors.New("capacity gates failed (see JSON)")
	}
	return nil
}

func (r *run) request(ctx context.Context, path, etag string) (int, []byte, string, error) {
	code, body, headers, err := r.requestWithHeaders(ctx, path, etag)
	return code, body, headers.Get("ETag"), err
}
func (r *run) requestWithHeaders(ctx context.Context, path, etag string) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", r.target+path, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	elapsed := time.Since(start)
	label := "bootstrap"
	if strings.Contains(path, "/rounds") {
		label = "report"
	}
	r.mu.Lock()
	r.s.HTTPStatuses[fmt.Sprintf("%s:%d", label, resp.StatusCode)]++
	r.recordLatencyLocked(label, float64(elapsed.Microseconds())/1000)
	r.mu.Unlock()
	return resp.StatusCode, body, resp.Header, err
}

// Algorithm R keeps a deterministic unbiased reservoir, bounded to256KiB per
// route family, instead of growing one sample per request for a24-hour soak.
func (r *run) recordLatencyLocked(label string, value float64) {
	r.s.LatencyObservations[label]++
	const maxSamples = 32768
	if len(r.latencies[label]) < maxSamples {
		r.latencies[label] = append(r.latencies[label], value)
		return
	}
	index := r.random.Int63n(r.s.LatencyObservations[label])
	if index < maxSamples {
		r.latencies[label][index] = value
	}
}

func (r *run) metricValues(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	if r.metrics == "" {
		return out
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", r.metrics+"/metrics", nil)
	resp, err := r.client.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			if n, e := strconv.ParseFloat(fields[1], 64); e == nil {
				out[fields[0]] = n
			}
		}
	}
	return out
}

func (r *run) sampleResources(ctx context.Context) {
	v := r.metricValues(ctx)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &r.s.Resources
	if memory.HeapAlloc > s.GeneratorPeakHeapBytes {
		s.GeneratorPeakHeapBytes = memory.HeapAlloc
	}
	if len(v) == 0 {
		return
	}
	if s.Samples == 0 {
		s.CPUStartSeconds = v["process_cpu_seconds_total"]
	}
	_, hasCPU := v["process_cpu_seconds_total"]
	_, hasRSS := v["process_resident_memory_bytes"]
	s.ProcessMetricsAvailable = hasCPU && hasRSS
	s.Samples++
	s.CPUEndSeconds = v["process_cpu_seconds_total"]
	s.PeakRSSBytes = math.Max(s.PeakRSSBytes, v["process_resident_memory_bytes"])
	s.PeakGoroutines = math.Max(s.PeakGoroutines, v["go_goroutines"])
	s.PeakReportCacheBytes = math.Max(s.PeakReportCacheBytes, v["cmt_top_report_cache_bytes"])
	s.PeakQueueBytes = math.Max(s.PeakQueueBytes, v["cmt_top_browser_queue_bytes"])
}

func (r *run) errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s.SessionErrors++
	if len(r.s.FirstErrors) < 12 {
		r.s.FirstErrors = append(r.s.FirstErrors, fmt.Sprintf(format, args...))
	}
}
func (r *run) getFixture(ctx context.Context, path string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", r.fixture+path, nil)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("fixture status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}
func (r *run) verifyFixture(ctx context.Context) error {
	for {
		var st replay.Stats
		if err := r.getFixture(ctx, "/_fixture/stats", &st); err != nil {
			if !wait(ctx, 100*time.Millisecond) {
				return err
			}
			continue
		}
		if st.Identity != replay.Identity {
			return errors.New("unrecognized fixture; refusing load")
		}
		code, body, _, err := r.request(ctx, "/api/state", "")
		if err == nil && code == 200 {
			var state struct {
				Chain struct {
					Network string `json:"network"`
				} `json:"chain"`
				ActiveRPC string `json:"activeRPC"`
			}
			if json.Unmarshal(body, &state) == nil && state.Chain.Network == "capacity-replay-1" && strings.TrimRight(state.ActiveRPC, "/") == r.fixture && st.Ready {
				r.s.UpstreamBefore = st
				r.s.Profile = st.Profile
				r.s.Seed = st.Seed
				return nil
			}
		}
		if !wait(ctx, 100*time.Millisecond) {
			return errors.New("app did not become ready with the explicit replay source")
		}
	}
}

type envelope struct {
	Type    string `json:"type"`
	Seq     uint64 `json:"seq"`
	Height  int64  `json:"height"`
	Payload struct {
		Height       int64    `json:"height"`
		ServerEpoch  string   `json:"serverEpoch"`
		Capabilities []string `json:"capabilities"`
	} `json:"payload"`
}

func has(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (r *run) session(ctx context.Context, id int) {
	started := time.Now()
	r.mu.Lock()
	r.s.Offered++
	r.mu.Unlock()
	conn, baselineHeight, err := r.admit(ctx, id)
	if err != nil {
		r.errorf("session %d admission: %v", id, err)
		return
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(started.Add(10 * time.Second))
	r.mu.Lock()
	r.s.Admitted++
	r.mu.Unlock()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	var current atomic.Int64
	current.Store(baselineHeight)
	var previous uint64
	first := true
	profileBarrier, contextOnly, compactProfile := false, false, false
	epoch := ""
	var polling sync.WaitGroup
	sessionCtx, stop := context.WithCancel(ctx)
	defer func() { stop(); polling.Wait() }()
	startPolling := func(compact bool) {
		polling.Add(1)
		r.mu.Lock()
		r.investigators[id].ProfileReady = true
		r.s.InvestigatorProfilesReady++
		r.mu.Unlock()
		go func() { defer polling.Done(); r.poll(sessionCtx, id, &current, compact, epoch) }()
	}
	for {
		_, raw, e := conn.ReadMessage()
		if e != nil {
			if ctx.Err() == nil {
				r.errorf("session %d read: %v", id, e)
			}
			return
		}
		var env envelope
		if json.Unmarshal(raw, &env) != nil {
			r.errorf("session %d invalid envelope", id)
			return
		}
		r.mu.Lock()
		r.s.Messages++
		r.s.PayloadBytes += uint64(len(raw))
		byType := r.s.MessageTypes[env.Type]
		byType.Messages++
		byType.Bytes += uint64(len(raw))
		r.s.MessageTypes[env.Type] = byType
		if env.Type == "state.snapshot" {
			r.s.Snapshots++
		}
		if env.Type == "context.snapshot" {
			r.s.Contexts++
		}
		if env.Seq != previous+1 {
			r.s.SequenceGaps++
		}
		r.mu.Unlock()
		previous = env.Seq
		if contextOnly && env.Type != "context.snapshot" && env.Type != "pong" {
			r.errorf("session %d received %s after context subscription barrier", id, env.Type)
			return
		}
		if profileBarrier && env.Type == "pong" {
			profileBarrier, contextOnly = false, true
			startPolling(compactProfile)
		}
		if env.Height > 0 {
			current.Store(env.Height)
		}
		if env.Payload.Height > 0 {
			current.Store(env.Payload.Height)
		}
		if first {
			if env.Type != "state.snapshot" || env.Seq != 1 {
				r.errorf("session %d lacks initial authoritative baseline", id)
				return
			}
			first = false
			_ = conn.SetReadDeadline(time.Time{})
			r.mu.Lock()
			r.s.BaselineReady++
			r.recordLatencyLocked("baselineReady", float64(time.Since(started).Microseconds())/1000)
			r.mu.Unlock()
			epoch = env.Payload.ServerEpoch
			if r.investigators[id].Intended {
				compactProfile = has(env.Payload.Capabilities, "rounds-compact-v1")
				if has(env.Payload.Capabilities, "context-v1") {
					profileBarrier = true
					for _, cmd := range []any{map[string]any{"type": "subscribe", "channels": []string{"context"}}, map[string]any{"type": "unsubscribe", "channels": []string{"state", "blocks", "divergence", "votes"}}, map[string]any{"type": "ping"}} {
						if conn.WriteJSON(cmd) != nil {
							r.errorf("session %d profile command failed", id)
							return
						}
					}
				} else {
					startPolling(compactProfile)
				}
			}
		}
	}
}

func (r *run) poll(ctx context.Context, id int, height *atomic.Int64, compact bool, epoch string) {
	if !wait(ctx, time.Duration(id%97)*time.Millisecond) {
		return
	}
	etag := ""
	var oldHeight int64
	for {
		h := height.Load()
		if h > 0 {
			if h != oldHeight {
				etag = ""
				oldHeight = h
			}
			path := fmt.Sprintf("/api/blocks/%d/rounds", h)
			if compact {
				path += "?view=compact&round=latest"
			}
			code, body, next, err := r.request(ctx, path, etag)
			if err != nil {
				if ctx.Err() == nil {
					r.errorf("session %d report: %v", id, err)
				}
				return
			}
			if validationErr := validateReportResponse(code, body, next, etag, h, compact, epoch); validationErr != nil {
				r.errorf("session %d report: %v", id, validationErr)
				return
			} else {
				r.mu.Lock()
				r.s.Reports++
				if !r.investigators[id].ValidReport {
					r.investigators[id].ValidReport = true
					r.s.InvestigatorsWithReports++
				}
				r.s.ReportBytes += uint64(len(body))
				if code == 304 {
					r.s.NotModified++
				}
				r.mu.Unlock()
				etag = next
			}
		}
		if !wait(ctx, time.Second+time.Duration(id%101)*time.Millisecond) {
			return
		}
	}
}

func (r *run) readDropMetrics(ctx context.Context) (map[string]float64, error) {
	if r.metrics == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", r.metrics+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseDropMetrics(body)
}
func (r *run) finalizeDropMetrics(ctx context.Context, before map[string]float64) {
	if r.metrics == "" {
		return
	}
	after, err := r.readDropMetrics(ctx)
	if err == nil {
		r.s.DropMetrics, err = dropDeltas(before, after)
	}
	if err != nil {
		r.errorf("final drop scrape: %v", err)
		return
	}
	r.s.DropMetricsVerified = true
}
func (r *run) finish(ctx context.Context) {
	// Stop mutation before comparing source evidence to the app archive. Restore
	// the prior control state even if verification fails.
	var initial replay.Stats
	_ = r.getFixture(ctx, "/_fixture/stats", &initial)
	setPause := func(value bool) error {
		b := []byte(fmt.Sprintf(`{"paused":%t}`, value))
		req, _ := http.NewRequestWithContext(ctx, "POST", r.fixture+"/_fixture/control", bytes.NewReader(b))
		resp, e := r.client.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("control status %d", resp.StatusCode)
		}
		return nil
	}
	if err := setPause(true); err != nil {
		r.errorf("freeze replay: %v", err)
		return
	}
	defer func() { _ = setPause(initial.Paused) }()
	if !wait(ctx, 250*time.Millisecond) {
		return
	}
	if err := r.getFixture(ctx, "/_fixture/stats", &r.s.UpstreamAfter); err != nil {
		r.errorf("fixture stats: %v", err)
		return
	}
	height := r.s.UpstreamAfter.Height
	var expected *replay.ExpectedHeight
	if err := r.getFixture(ctx, fmt.Sprintf("/_fixture/oracle?height=%d", height), &expected); err != nil {
		r.errorf("oracle fetch: %v", err)
		return
	}
	// NewBlock advances the fixture's active height before the next NewRound
	// creates its oracle evidence. A freeze at that boundary must compare the
	// last committed block instead; missing evidence at any other height fails.
	if expected == nil && r.s.UpstreamAfter.CommittedHeight > 0 && height-1 == r.s.UpstreamAfter.CommittedHeight {
		height = r.s.UpstreamAfter.CommittedHeight
		if err := r.getFixture(ctx, fmt.Sprintf("/_fixture/oracle?height=%d", height), &expected); err != nil {
			r.errorf("committed oracle fetch: %v", err)
			return
		}
	}
	if expected == nil {
		r.errorf("source oracle evidence unavailable at height %d", height)
		return
	}
	for attempt := 0; attempt < 20; attempt++ {
		code, body, _, err := r.request(ctx, fmt.Sprintf("/api/blocks/%d/rounds?view=full&capture=1", height), "")
		if err == nil && code == 200 {
			if err = replay.CheckReport(body, expected); err == nil {
				r.s.OracleVerified = true
				r.s.SemanticDigest, _ = replay.SemanticDigest(body)
				return
			}
		}
		if !wait(ctx, 100*time.Millisecond) {
			break
		}
	}
	r.errorf("source evidence did not match captured report at height %d", height)
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
