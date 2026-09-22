package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/metrics"
	"github.com/InjectiveLabs/cmt-top/internal/state"
	"github.com/gorilla/websocket"
)

const (
	wsWriteTimeout = 2 * time.Second
	wsReadTimeout  = 60 * time.Second
	wsPingPeriod   = 30 * time.Second
	wsMaxClients   = 256
	wsResyncPeriod = 250 * time.Millisecond
)

type wsEnvelope struct {
	Type     string `json:"type"`
	Ts       string `json:"ts"`
	Height   int64  `json:"height,omitempty"`
	Round    int64  `json:"round,omitempty"`
	Seq      uint64 `json:"seq"`
	Payload  any    `json:"payload"`
	wire     *wsWire
	size     int
	queuedAt time.Time
}

type wsClient struct {
	conn         *websocket.Conn
	hub          *wsHub
	dropped      atomic.Uint64
	mu           sync.Mutex
	subs         map[string]bool
	seq          uint64
	pending      []wsEnvelope
	queuedBytes  int
	queueLimit   int
	overloadedAt time.Time
	wake         chan struct{}
	done         chan struct{}
	closed       bool
	closeOnce    sync.Once
}

type wsHub struct {
	bus         *events.Bus
	state       *state.State
	tracker     *divergence.Tracker
	log         *slog.Logger
	corsOrigin  string
	explorerURL string
	displayName string
	// Configuration is immutable after serving starts.
	maxClients   int
	epoch        string
	capabilities []string
	observe      func(event string, value float64)
	mu           sync.RWMutex
	clients      map[*wsClient]struct{}
	pending      int
	closing      bool
	// Lock order: dispatchMu -> mu -> client.mu. No network operation under
	// these locks. Client close obtains client.mu and mu separately, never nested.
	dispatchMu    sync.Mutex
	resyncMu      sync.Mutex
	resyncPending map[*wsClient]struct{}
	resyncTimer   *time.Timer
	resyncLast    time.Time
	resyncClosing bool
}

func newWSHub(bus *events.Bus, st *state.State, tracker *divergence.Tracker, log *slog.Logger, corsOrigin string) *wsHub {
	return &wsHub{bus: bus, state: st, tracker: tracker, log: log, corsOrigin: corsOrigin, maxClients: wsMaxClients, clients: map[*wsClient]struct{}{}, resyncPending: map[*wsClient]struct{}{}}
}
func (h *wsHub) record(event string, value float64) {
	if h.observe != nil {
		h.observe(event, value)
	}
}
func (h *wsHub) recordDrop() { metrics.RecordWSDrop(1); h.record("dropped", 1) }
func (h *wsHub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || h.corsOrigin != "" && origin == h.corsOrigin || origin == "https://"+r.Host || origin == "http://"+r.Host
}
func (h *wsHub) snapshot() wsEnvelope {
	started := time.Now()
	payload := snapshotJSON(h.state.Snapshot(), h.tracker, h.bus)
	payload["explorerURL"], payload["displayName"] = h.explorerURL, h.displayName
	payload["schemaVersion"], payload["serverEpoch"] = 1, h.epoch
	payload["capabilities"] = append([]string{}, h.capabilities...)
	payload["cadence"] = map[string]int{"snapshotMs": 1000, "activePollMs": 1000, "settledPollMs": 5000}
	h.record("snapshot_seconds", time.Since(started).Seconds())
	return wsEnvelope{Type: "state.snapshot", Ts: time.Now().UTC().Format(time.RFC3339Nano), Payload: payload}
}
func (h *wsHub) contextSnapshot() wsEnvelope {
	s := h.state.Snapshot()
	p := map[string]any{"height": s.Height, "committedHeight": s.LastCommittedHeight, "round": s.Round, "step": s.Step, "chain": nil, "health": s.Health, "upgrade": nil, "errors": errorMap(s), "displayName": h.displayName, "serverEpoch": h.epoch}
	if s.NodeStatus != nil {
		p["chain"] = map[string]any{"network": s.NodeStatus.Network, "cometVersion": s.NodeStatus.CometVersion, "ourValidator": s.NodeStatus.OurValidator, "catchingUp": s.NodeStatus.CatchingUp}
	}
	if s.Upgrade != nil {
		p["upgrade"] = map[string]any{"name": s.Upgrade.Name, "height": s.Upgrade.Height}
	}
	return wsEnvelope{Type: "context.snapshot", Payload: p}
}
func (h *wsHub) serveWS(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	limit := h.maxClients
	if limit <= 0 {
		limit = wsMaxClients
	}
	if h.closing || len(h.clients)+h.pending >= limit {
		h.mu.Unlock()
		h.record("rejected", 1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many ws clients", http.StatusServiceUnavailable)
		return
	}
	h.pending++
	h.record("pending_delta", 1)
	h.mu.Unlock()
	// Upgrade may block writing a handshake. A reservation holds no global lock.
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: h.checkOrigin, HandshakeTimeout: 5 * time.Second}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.mu.Lock()
		h.pending--
		h.record("pending_delta", -1)
		h.mu.Unlock()
		return
	}
	c := &wsClient{conn: conn, hub: h, subs: map[string]bool{"state": true, "blocks": true, "divergence": true, "votes": true}, wake: make(chan struct{}, 1), done: make(chan struct{})}
	h.dispatchMu.Lock()
	h.mu.Lock()
	h.pending--
	h.record("pending_delta", -1)
	if h.closing {
		h.mu.Unlock()
		h.dispatchMu.Unlock()
		_ = conn.Close()
		return
	}
	h.clients[c] = struct{}{}
	h.record("active_delta", 1)
	h.mu.Unlock()
	// Queue an authoritative baseline before any broadcasts can observe c.
	env, err := h.prepare(h.snapshot())
	ok := false
	if err == nil {
		c.mu.Lock()
		ok = c.queueLocked(env)
		c.mu.Unlock()
	}
	h.dispatchMu.Unlock()
	if !ok {
		c.close()
		return
	}
	go c.writePump()
	go c.readPump()
}
func (h *wsHub) run(ctx context.Context) {
	ch, cancel := h.bus.Subscribe("wshub", events.KindNewBlock, events.KindRoundChanged, events.KindVoteReceived, events.KindStatusUpdated, events.KindUpgradePlanUpdated, events.KindBlockTimeUpdated, events.KindValidatorSetUpdated, events.KindDivergenceDetected, events.KindDivergenceResolved, events.KindConnectionLost, events.KindConnectionRestored, events.KindEndpointAppHashDivergence)
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer h.closeClients()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.dispatchMu.Lock()
			// Periodic repair remains authoritative even after internal bus loss.
			if h.hasSubscribers("state") {
				h.broadcastLocked(h.snapshot())
			}
			if h.hasSubscribers("context") {
				h.broadcastLocked(h.contextSnapshot())
			}
			// State snapshots already include the complete divergence report.
			// Only divergence-only subscribers need this separate repair frame.
			if h.hasDivergenceOnlySubscribers() {
				h.pushDivergenceLocked()
			}
			h.dispatchMu.Unlock()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			h.dispatchMu.Lock()
			if env := h.envelope(ev); env != nil {
				h.broadcastLocked(*env)
			}
			h.dispatchMu.Unlock()
		}
	}
}
func (h *wsHub) hasDivergenceOnlySubscribers() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.mu.Lock()
		needed := c.subs["divergence"] && !c.subs["state"]
		c.mu.Unlock()
		if needed {
			return true
		}
	}
	return false
}
func (h *wsHub) hasSubscribers(channel string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.mu.Lock()
		subscribed := c.subs[channel]
		c.mu.Unlock()
		if subscribed {
			return true
		}
	}
	return false
}
func (h *wsHub) envelope(ev events.Event) *wsEnvelope {
	env := &wsEnvelope{Ts: time.Now().UTC().Format(time.RFC3339Nano), Payload: ev.Payload}
	switch ev.Kind {
	case events.KindNewBlock:
		p := ev.Payload.(events.NewBlock)
		env.Type = "block.committed"
		env.Height = p.Height
	case events.KindVoteReceived:
		p := ev.Payload.(events.VoteReceived)
		env.Type = "vote.received"
		env.Height = p.Height
		env.Round = p.Round
	case events.KindRoundChanged:
		p := ev.Payload.(events.RoundChanged)
		env.Type = "round.changed"
		env.Height = p.Height
		env.Round = p.Round
	case events.KindEndpointAppHashDivergence:
		env.Type = "rpc_comparison.updated"
	case events.KindStatusUpdated:
		env.Type = "status.updated"
	case events.KindUpgradePlanUpdated:
		env.Type = "upgrade.changed"
	case events.KindBlockTimeUpdated:
		env.Type = "block_time.updated"
		env.Payload = map[string]any{"blockTime": ev.Payload.(events.BlockTimeUpdated).BlockTime.Milliseconds()}
	case events.KindValidatorSetUpdated:
		env.Type = "validators.changed"
	case events.KindDivergenceDetected:
		env.Type = "divergence.detected"
	case events.KindDivergenceResolved:
		env.Type = "divergence.resolved"
	case events.KindConnectionLost:
		env.Type = "connection.lost"
	case events.KindConnectionRestored:
		env.Type = "connection.restored"
	default:
		return nil
	}
	return env
}

func (h *wsHub) pushDivergenceLocked() {
	if h.tracker == nil {
		return
	}
	rep := h.tracker.CurrentReport()
	h.broadcastLocked(wsEnvelope{Type: "divergence.snapshot", Payload: map[string]any{"live": rep.Live, "history": rep.History}})
}
func channelFor(kind string) string {
	switch {
	case kind == "context.snapshot":
		return "context"
	case kind == "vote.received":
		return "votes"
	case kind == "block.committed":
		return "blocks"
	case strings.HasPrefix(kind, "divergence."):
		return "divergence"
	default:
		return "state"
	}
}
func (h *wsHub) broadcast(env wsEnvelope) {
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	h.broadcastLocked(env)
}
func (h *wsHub) broadcastLocked(env wsEnvelope) {
	if !h.hasSubscribers(channelFor(env.Type)) {
		return
	}
	env, err := h.prepare(env)
	if err != nil {
		h.log.Error("websocket encoding failed", "err", err)
		return
	}
	var closeAfter []*wsClient
	h.mu.RLock()
	for c := range h.clients {
		c.mu.Lock()
		interested := c.subs[channelFor(env.Type)]
		if env.Type == "divergence.snapshot" && c.subs["state"] {
			interested = false
		}
		if interested && !c.queueLocked(env) {
			closeAfter = append(closeAfter, c)
		}
		c.mu.Unlock()
	}
	h.mu.RUnlock()
	// Dispatch ordering can stay locked, but never close under registry/client locks.
	for _, c := range closeAfter {
		h.record("slow_client", 1)
		c.signalClose()
	}
}

// resync is the synchronous internal baseline primitive. Wire commands use
// requestResync, combining all readers' requests into one fresh snapshot build.
func (h *wsHub) resync(c *wsClient) {
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	c.queue(h.snapshot())
}
func (h *wsHub) requestResync(c *wsClient) {
	h.record("resync", 1)
	h.resyncMu.Lock()
	defer h.resyncMu.Unlock()
	if h.resyncClosing {
		return
	}
	if _, exists := h.resyncPending[c]; exists {
		h.record("resync_coalesced", 1)
	}
	h.resyncPending[c] = struct{}{}
	if h.resyncTimer != nil {
		return
	}
	delay := time.Until(h.resyncLast.Add(wsResyncPeriod))
	if delay < 10*time.Millisecond {
		delay = 10 * time.Millisecond
	}
	h.resyncTimer = time.AfterFunc(delay, h.flushResync)
}
func (h *wsHub) flushResync() {
	h.resyncMu.Lock()
	if h.resyncClosing {
		h.resyncMu.Unlock()
		return
	}
	pending := h.resyncPending
	h.resyncPending = make(map[*wsClient]struct{})
	h.resyncLast = time.Now()
	h.resyncTimer = nil
	h.resyncMu.Unlock()
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	h.mu.RLock()
	closing := h.closing
	h.mu.RUnlock()
	if closing {
		return
	}
	if len(pending) == 0 {
		return
	}
	env, err := h.prepare(h.snapshot())
	if err != nil {
		return
	}
	for c := range pending {
		c.queue(env)
	}
}
func (h *wsHub) closeClients() {
	h.resyncMu.Lock()
	h.resyncClosing = true
	if h.resyncTimer != nil {
		h.resyncTimer.Stop()
	}
	h.resyncPending = nil
	h.resyncMu.Unlock()
	h.mu.Lock()
	h.closing = true
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

// signalClose does no network work; the writer wakes and performs connection
// cleanup outside dispatch/registry locks. Both pumps eventually call close.
func (c *wsClient) signalClose() {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.hub.record("queue_bytes_delta", -float64(c.queuedBytes))
		c.pending = nil
		c.queuedBytes = 0
		if c.done != nil {
			close(c.done)
		}
	}
	c.mu.Unlock()
}
func (c *wsClient) close() {
	c.signalClose()
	c.closeOnce.Do(func() {
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.hub.mu.Lock()
		if _, ok := c.hub.clients[c]; ok {
			delete(c.hub.clients, c)
			c.hub.record("active_delta", -1)
		}
		c.hub.mu.Unlock()
		c.hub.resyncMu.Lock()
		delete(c.hub.resyncPending, c)
		c.hub.resyncMu.Unlock()
	})
}
func (c *wsClient) writePump() {
	ping := time.NewTicker(wsPingPeriod)
	defer ping.Stop()
	defer c.close()
	for {
		select {
		case <-c.done:
			return
		default:
		}
		if env, ok := c.dequeue(); ok {
			age := time.Since(env.queuedAt)
			c.hub.record("queue_age_seconds", age.Seconds())
			if age > wsQueueMaxAge {
				c.hub.record("slow_client", 1)
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			n, err := env.writeTo(w)
			if err != nil {
				_ = w.Close()
				return
			}
			if err = w.Close(); err != nil {
				return
			}
			c.hub.record("sent_bytes/"+env.Type, float64(n))
			c.hub.record("sent_messages/"+env.Type, 1)
			// A busy queue must not starve control pings.
			select {
			case <-ping.C:
				if !c.writePing() {
					return
				}
			default:
			}
			continue
		}
		select {
		case <-c.done:
			return
		case <-c.wake:
		case <-ping.C:
			if !c.writePing() {
				return
			}
		}
	}
}
func (c *wsClient) writePing() bool {
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteMessage(websocket.PingMessage, nil) == nil
}
func (c *wsClient) readPump() {
	defer c.close()
	c.conn.SetReadLimit(4 * 1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	budget := newWSCommandBudget(time.Now())
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if !budget.allow(time.Now()) {
			c.hub.record("command_limited", 1)
			return
		}
		var cmd struct {
			Type     string   `json:"type"`
			Channels []string `json:"channels"`
		}
		if json.Unmarshal(msg, &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "subscribe", "unsubscribe":
			c.mu.Lock()
			for _, channel := range cmd.Channels {
				switch channel {
				case "state", "blocks", "divergence", "votes", "context":
					c.subs[channel] = cmd.Type == "subscribe"
				}
			}
			c.mu.Unlock()
		case "resync":
			h := c.hub
			h.requestResync(c)
		case "ping":
			c.queue(wsEnvelope{Type: "pong"})
		}
	}
}
