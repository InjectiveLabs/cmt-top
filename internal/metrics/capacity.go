package metrics

import (
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Capacity instruments downstream browser work independently of upstream chain
// connectivity. A nil registerer builds isolated, unregistered collectors for
// embedders/tests. Each server/process should share one instance.
type Capacity struct {
	active, pending, queued, cacheBytes              prometheus.Gauge
	wsEvents, messages, bytes, encoded, reportEvents *prometheus.CounterVec
	work                                             *prometheus.HistogramVec
	http                                             *prometheus.HistogramVec
}

func NewCapacity(reg prometheus.Registerer) *Capacity {
	gauge := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts{Name: "cmt_top_" + name, Help: help})
	}
	counter := func(name, help, label string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cmt_top_" + name, Help: help}, []string{label})
	}
	c := &Capacity{
		active:       gauge("browser_connections", "Active downstream browser WebSockets."),
		pending:      gauge("browser_handshakes_pending", "Reserved browser connection slots awaiting upgrade."),
		queued:       gauge("browser_queue_bytes", "Logical bytes queued across browser connections; shared bytes may be counted for each recipient."),
		cacheBytes:   gauge("report_cache_bytes", "Bytes retained by the report cache."),
		wsEvents:     counter("browser_events_total", "Browser admission, repair, and queue events.", "event"),
		messages:     counter("browser_messages_total", "Successfully written browser messages.", "type"),
		bytes:        counter("browser_payload_bytes_total", "Successfully written WebSocket payload bytes, excluding framing/TLS.", "type"),
		encoded:      counter("browser_encoded_payload_bytes_total", "Shared encoded payload bytes, before per-client framing.", "type"),
		reportEvents: counter("report_events_total", "Report cache/build/admission events.", "event"),
		work:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cmt_top_capacity_work_seconds", Help: "Build/encode time and observed queue age.", Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5}}, []string{"operation"}),
		http:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cmt_top_http_request_seconds", Help: "HTTP handler duration by bounded route and status.", Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5}}, []string{"route", "status"}),
	}
	if reg != nil {
		reg.MustRegister(c.active, c.pending, c.queued, c.cacheBytes, c.wsEvents, c.messages, c.bytes, c.encoded, c.reportEvents, c.work, c.http)
	}
	return c
}

// ObserveWS accepts a fixed vocabulary. Unknown events are ignored rather than
// introducing unbounded metric series from input or message contents.
func (c *Capacity) ObserveWS(event string, value float64) {
	if c == nil {
		return
	}
	name, kind, _ := strings.Cut(event, "/")
	switch name {
	case "active_delta":
		c.active.Add(value)
	case "pending_delta":
		c.pending.Add(value)
	case "queue_bytes_delta":
		c.queued.Add(value)
	case "sent_bytes":
		c.bytes.WithLabelValues(messageKind(kind)).Add(value)
	case "sent_messages":
		c.messages.WithLabelValues(messageKind(kind)).Add(value)
	case "payload_encoded_bytes":
		c.encoded.WithLabelValues(messageKind(kind)).Add(value)
	case "snapshot_seconds", "encode_seconds", "queue_age_seconds":
		c.work.WithLabelValues(name).Observe(value)
	case "rejected", "dropped", "resync", "resync_coalesced", "slow_client", "command_limited":
		c.wsEvents.WithLabelValues(name).Add(value)
	}
}

func (c *Capacity) ObserveReport(event string, value float64) {
	if c == nil {
		return
	}
	switch event {
	case "cache_bytes":
		c.cacheBytes.Set(value)
	case "build_seconds", "capture_seconds", "archive_lock_seconds":
		c.work.WithLabelValues(event).Observe(value)
	case "hit", "miss", "build", "coalesced", "evicted", "rejected", "capture", "not_modified":
		c.reportEvents.WithLabelValues(event).Add(value)
	}
}

func (c *Capacity) ObserveHTTP(route string, status int, elapsed time.Duration) {
	if c == nil {
		return
	}
	switch route {
	case "/api/session", "/api/state", "/api/validators", "/api/validators/{address}", "/api/divergence", "/api/divergence/history", "/api/blocks", "/api/blocks/{height}/rounds", "/api/chain", "/api/version", "/ws", "/healthz", "/readyz":
	default:
		route = "other"
	}
	if status < 100 || status > 599 {
		status = 200
	}
	c.http.WithLabelValues(route, strconv.Itoa(status)).Observe(elapsed.Seconds())
}

func messageKind(kind string) string {
	switch kind {
	case "state.snapshot", "context.snapshot", "vote.received", "block.committed", "round.changed", "rpc_comparison.updated", "status.updated", "upgrade.changed", "block_time.updated", "validators.changed", "divergence.snapshot", "divergence.detected", "divergence.resolved", "connection.lost", "connection.restored", "pong":
		return kind
	default:
		return "other"
	}
}
