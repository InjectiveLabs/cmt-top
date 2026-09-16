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
	wsWriteTimeout = 10 * time.Second
	wsReadTimeout  = 60 * time.Second
	wsPingPeriod   = 30 * time.Second
	wsMaxClients   = 32
)

type wsEnvelope struct {
	Type    string `json:"type"`
	Ts      string `json:"ts"`
	Height  int64  `json:"height,omitempty"`
	Round   int64  `json:"round,omitempty"`
	Seq     uint64 `json:"seq"`
	Payload any    `json:"payload"`
}

type wsClient struct {
	conn      *websocket.Conn
	send      chan wsEnvelope
	dropped   atomic.Uint64
	hub       *wsHub
	mu        sync.Mutex // subscription changes and queue sequencing share one owner
	subs      map[string]bool
	seq       uint64
	done      chan struct{}
	closeOnce sync.Once
}

type wsHub struct {
	bus         *events.Bus
	state       *state.State
	tracker     *divergence.Tracker
	log         *slog.Logger
	corsOrigin  string
	explorerURL string
	displayName string
	mu          sync.RWMutex
	clients     map[*wsClient]struct{}
	dispatchMu  sync.Mutex // snapshot capture and delivery cannot overtake broadcasts
	divLastPush time.Time
}

func newWSHub(bus *events.Bus, st *state.State, tracker *divergence.Tracker, log *slog.Logger, corsOrigin string) *wsHub {
	return &wsHub{bus: bus, state: st, tracker: tracker, log: log, corsOrigin: corsOrigin, clients: map[*wsClient]struct{}{}}
}

func (h *wsHub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || h.corsOrigin != "" && origin == h.corsOrigin {
		return true
	}
	return origin == "https://"+r.Host || origin == "http://"+r.Host
}

func (h *wsHub) snapshot() wsEnvelope {
	payload := snapshotJSON(h.state.Snapshot(), h.tracker, h.bus)
	payload["explorerURL"] = h.explorerURL
	payload["displayName"] = h.displayName
	return wsEnvelope{Type: "state.snapshot", Ts: time.Now().UTC().Format(time.RFC3339Nano), Payload: payload}
}

func (h *wsHub) serveWS(w http.ResponseWriter, r *http.Request) {
	// Reserve registration while upgrading so parallel handshakes cannot exceed the cap.
	h.dispatchMu.Lock()
	h.mu.Lock()
	if len(h.clients) >= wsMaxClients {
		h.mu.Unlock()
		h.dispatchMu.Unlock()
		http.Error(w, "too many ws clients", http.StatusServiceUnavailable)
		return
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: h.checkOrigin, HandshakeTimeout: 5 * time.Second}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.mu.Unlock()
		h.dispatchMu.Unlock()
		return
	}
	c := &wsClient{conn: conn, send: make(chan wsEnvelope, 256), hub: h, subs: map[string]bool{"state": true, "blocks": true, "divergence": true, "votes": true}, done: make(chan struct{})}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	// The first message is always a complete baseline, numbered one.
	c.queue(h.snapshot())
	h.dispatchMu.Unlock()
	go c.writePump()
	go c.readPump()
}

func (h *wsHub) run(ctx context.Context) {
	ch, cancel := h.bus.Subscribe("wshub", events.KindNewBlock, events.KindRoundChanged, events.KindVoteReceived,
		events.KindStatusUpdated, events.KindUpgradePlanUpdated, events.KindBlockTimeUpdated, events.KindValidatorSetUpdated,
		events.KindDivergenceDetected, events.KindDivergenceResolved, events.KindConnectionLost, events.KindConnectionRestored, events.KindEndpointAppHashDivergence)
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
			h.broadcastLocked(h.snapshot())
			h.dispatchMu.Unlock()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			h.dispatchMu.Lock()
			if env := h.envelope(ev); env != nil {
				h.broadcastLocked(*env)
			}
			if ev.Kind == events.KindVoteReceived {
				if time.Since(h.divLastPush) >= 150*time.Millisecond {
					h.pushDivergenceLocked()
					h.divLastPush = time.Now()
				}
			} else {
				// Includes explicit null/removal updates and fallback state. Periodic snapshots
				// additionally reconcile errors and dropped events from the lossy internal bus.
				h.broadcastLocked(h.snapshot())
			}
			h.dispatchMu.Unlock()
		}
	}
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
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.mu.Lock()
		if c.subs[channelFor(env.Type)] {
			c.queueLocked(env)
		}
		c.mu.Unlock()
	}
}
func (h *wsHub) resync(c *wsClient) {
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	c.queue(h.snapshot())
}
func (h *wsHub) closeClients() {
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.close()
	}
}

func (c *wsClient) queue(env wsEnvelope) { c.mu.Lock(); defer c.mu.Unlock(); c.queueLocked(env) }
func (c *wsClient) queueLocked(env wsEnvelope) {
	c.seq++
	env.Seq = c.seq
	if env.Ts == "" {
		env.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	select {
	case c.send <- env:
		return
	default:
	}
	// Every dropped message leaves a visible gap in this client's sequence.
	// The browser requests resync and ignores patches until its snapshot arrives.
	select {
	case <-c.send:
		c.dropped.Add(1)
		metrics.RecordWSDrop(1)
	default:
	}
	select {
	case c.send <- env:
	default:
		c.dropped.Add(1)
		metrics.RecordWSDrop(1)
	}
}
func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.hub.mu.Unlock()
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
		case env := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteJSON(env); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
func (c *wsClient) readPump() {
	defer c.close()
	c.conn.SetReadLimit(64 * 1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
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
				case "state", "blocks", "divergence", "votes":
					c.subs[channel] = cmd.Type == "subscribe"
				}
			}
			c.mu.Unlock()
		case "resync":
			c.hub.resync(c)
		case "ping":
			c.queue(wsEnvelope{Type: "pong"})
		}
	}
}
