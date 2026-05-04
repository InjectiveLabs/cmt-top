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

	"github.com/gorilla/websocket"

	"github.com/Ri-go/cmt-top/internal/divergence"
	"github.com/Ri-go/cmt-top/internal/events"
	"github.com/Ri-go/cmt-top/internal/obs"
	"github.com/Ri-go/cmt-top/internal/state"
)

const (
	wsWriteTimeout = 10 * time.Second
	wsReadTimeout  = 60 * time.Second
	wsPingPeriod   = 30 * time.Second
	// wsMaxClients caps concurrent connections so a single attacker can't open
	// thousands of WS clients (each ~10KB/s of vote stream) and OOM the host.
	wsMaxClients = 32
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
	conn    *websocket.Conn
	send    chan wsEnvelope
	dropped atomic.Uint64
	hub     *wsHub
	subs    map[string]bool
}

type wsHub struct {
	bus        *events.Bus
	state      *state.State
	tracker    *divergence.Tracker
	log        *slog.Logger
	corsOrigin string // optional same-origin override; "" = match Host

	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	seq     atomic.Uint64

	divLastPush atomic.Int64  // unix nanos of last divergence.snapshot
	divLastKey  atomic.Uint64 // packed (height, round, type) of most recent vote
}

func newWSHub(bus *events.Bus, st *state.State, tracker *divergence.Tracker, log *slog.Logger, corsOrigin string) *wsHub {
	return &wsHub{
		bus:        bus,
		state:      st,
		tracker:    tracker,
		log:        log,
		corsOrigin: corsOrigin,
		clients:    map[*wsClient]struct{}{},
	}
}

// checkOrigin enforces same-origin (Origin host == Host) by default. Operators
// can opt in to a specific cross-origin via the CORSOrigin option, mirroring
// the REST CORS handling.
func (h *wsHub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (curl, native) typically omit Origin; allow.
		return true
	}
	if h.corsOrigin != "" && origin == h.corsOrigin {
		return true
	}
	// Same-origin: scheme://host of Origin must match the request's Host.
	// Strip scheme prefix on Origin to compare host:port.
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(origin, p) {
			if origin[len(p):] == r.Host {
				return true
			}
		}
	}
	return false
}

func (h *wsHub) serveWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	// Cap concurrent clients before upgrading so over-limit attempts don't
	// even allocate a connection.
	h.mu.RLock()
	full := len(h.clients) >= wsMaxClients
	h.mu.RUnlock()
	if full {
		http.Error(w, "too many ws clients", http.StatusServiceUnavailable)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", "err", err)
		return
	}
	c := &wsClient{
		conn: conn,
		send: make(chan wsEnvelope, 256),
		hub:  h,
		subs: map[string]bool{"state": true, "blocks": true, "divergence": true, "votes": true},
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go c.writePump()
	go c.readPump()

	// Push cold-load snapshot.
	c.queue(wsEnvelope{Type: "state.snapshot", Payload: snapshotJSON(h.state.Snapshot(), h.tracker, h.bus)})
}

// run subscribes to the bus and fans relevant events to clients.
func (h *wsHub) run(ctx context.Context) {
	defer obs.Recover("wshub")
	ch, cancel := h.bus.Subscribe("wshub",
		events.KindNewBlock, events.KindRoundChanged, events.KindVoteReceived,
		events.KindStatusUpdated, events.KindUpgradePlanUpdated, events.KindBlockTimeUpdated,
		events.KindValidatorSetUpdated,
		events.KindDivergenceDetected, events.KindDivergenceResolved,
		events.KindConnectionLost, events.KindConnectionRestored,
	)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			env := h.envelope(ev)
			if env == nil {
				continue
			}
			h.broadcast(*env)
		}
	}
}

func (h *wsHub) envelope(ev events.Event) *wsEnvelope {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch ev.Kind {
	case events.KindNewBlock:
		p := ev.Payload.(events.NewBlock)
		return &wsEnvelope{Type: "block.committed", Ts: now, Height: p.Height, Payload: p}
	case events.KindVoteReceived:
		p := ev.Payload.(events.VoteReceived)
		// Push the live tracker view: throttled, but always force a push when
		// the vote opens a new (height, round, type) bucket. Without this, a
		// short precommit phase falls between two throttle windows on every
		// block and never makes it into the snapshot stream.
		key := packVoteKey(p.Height, p.Round, int64(p.Type))
		force := key != h.divLastKey.Swap(key)
		h.maybePushDivergenceSnapshot(force)
		return &wsEnvelope{Type: "vote.received", Ts: now, Height: p.Height, Round: p.Round, Payload: p}
	case events.KindRoundChanged:
		p := ev.Payload.(events.RoundChanged)
		return &wsEnvelope{Type: "round.changed", Ts: now, Height: p.Height, Round: p.Round, Payload: p}
	case events.KindStatusUpdated:
		return &wsEnvelope{Type: "status.updated", Ts: now, Payload: ev.Payload}
	case events.KindUpgradePlanUpdated:
		return &wsEnvelope{Type: "upgrade.changed", Ts: now, Payload: ev.Payload}
	case events.KindBlockTimeUpdated:
		return &wsEnvelope{Type: "block_time.updated", Ts: now, Payload: ev.Payload}
	case events.KindValidatorSetUpdated:
		return &wsEnvelope{Type: "validators.changed", Ts: now, Payload: ev.Payload}
	case events.KindDivergenceDetected:
		return &wsEnvelope{Type: "divergence.detected", Ts: now, Payload: ev.Payload}
	case events.KindDivergenceResolved:
		return &wsEnvelope{Type: "divergence.resolved", Ts: now, Payload: ev.Payload}
	case events.KindConnectionLost:
		return &wsEnvelope{Type: "connection.lost", Ts: now, Payload: ev.Payload}
	case events.KindConnectionRestored:
		return &wsEnvelope{Type: "connection.restored", Ts: now, Payload: ev.Payload}
	}
	return nil
}

// maybePushDivergenceSnapshot pushes the tracker's current report. With
// force=true (e.g. a new (height,round,type) bucket just opened) the push
// bypasses the time throttle; otherwise it fires at most every 150ms.
func (h *wsHub) maybePushDivergenceSnapshot(force bool) {
	if h.tracker == nil {
		return
	}
	const minIntervalNs = int64(150 * time.Millisecond)
	now := time.Now().UnixNano()
	if !force {
		last := h.divLastPush.Load()
		if now-last < minIntervalNs {
			return
		}
		if !h.divLastPush.CompareAndSwap(last, now) {
			return
		}
	} else {
		h.divLastPush.Store(now)
	}
	rep := h.tracker.CurrentReport()
	h.broadcast(wsEnvelope{
		Type:    "divergence.snapshot",
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Payload: map[string]any{"live": rep.Live, "history": rep.History},
	})
}

// packVoteKey collapses (height, round, type) into a single uint64 for cheap
// atomic comparison.
func packVoteKey(height, round, typ int64) uint64 {
	return uint64(height)<<16 | uint64(round&0xFF)<<8 | uint64(typ&0xFF)
}

func (h *wsHub) broadcast(env wsEnvelope) {
	env.Seq = h.seq.Add(1)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		// Filter by subscription.
		switch env.Type {
		case "vote.received":
			if !c.subs["votes"] {
				continue
			}
		}
		c.queue(env)
	}
}

func (c *wsClient) queue(env wsEnvelope) {
	select {
	case c.send <- env:
	default:
		// Drop oldest, push new. If still full, drop new and bump counter.
		select {
		case <-c.send:
		default:
		}
		select {
		case c.send <- env:
			c.dropped.Add(1)
		default:
			c.dropped.Add(1)
		}
	}
}

func (c *wsClient) writePump() {
	defer obs.Recover("ws.writePump")
	pingT := time.NewTicker(wsPingPeriod)
	defer func() {
		pingT.Stop()
		_ = c.conn.Close()
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.hub.mu.Unlock()
	}()
	for {
		select {
		case env, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteJSON(env); err != nil {
				return
			}
		case <-pingT.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsClient) readPump() {
	defer obs.Recover("ws.readPump")
	c.conn.SetReadLimit(64 * 1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		return nil
	})
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var cmd struct {
			Type     string   `json:"type"`
			Channels []string `json:"channels"`
			Address  string   `json:"address"`
		}
		if err := json.Unmarshal(msg, &cmd); err != nil {
			continue
		}
		switch cmd.Type {
		case "subscribe":
			for _, ch := range cmd.Channels {
				c.subs[ch] = true
			}
		case "unsubscribe":
			for _, ch := range cmd.Channels {
				delete(c.subs, ch)
			}
		case "ping":
			c.queue(wsEnvelope{Type: "pong", Ts: time.Now().UTC().Format(time.RFC3339Nano)})
		}
	}
}
