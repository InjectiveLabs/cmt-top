package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
	"github.com/gorilla/websocket"
)

func waitWS(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(until) {
			t.Fatal("websocket condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func queueTestClient(h *wsHub) *wsClient {
	return &wsClient{hub: h, subs: map[string]bool{"state": true, "votes": true}, wake: make(chan struct{}, 1), done: make(chan struct{})}
}
func TestWebSocketCapacityCountsConcurrentAdmissions(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	const count = 256
	srv.hub.maxClients = count
	type result struct {
		conn *websocket.Conn
		err  error
	}
	results := make(chan result, count)
	handshakes := make(chan struct{}, 16) // avoid host listen-backlog limits
	for i := 0; i < count; i++ {
		go func() {
			handshakes <- struct{}{}
			defer func() { <-handshakes }()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
			if err == nil {
				var env wsEnvelope
				err = c.ReadJSON(&env)
				if err == nil && (env.Type != "state.snapshot" || env.Seq != 1) {
					err = fmt.Errorf("invalid first frame: %+v", env)
				}
			}
			results <- result{c, err}
		}()
	}
	var conns []*websocket.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < count; i++ {
		r := <-results
		if r.conn != nil {
			conns = append(conns, r.conn)
		}
		if r.err != nil {
			t.Fatalf("admission %d: %v", i, r.err)
		}
	}
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err == nil || resp == nil || resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over-cap admission: response=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()
	_ = conns[0].Close()
	waitWS(t, func() bool {
		srv.hub.mu.RLock()
		defer srv.hub.mu.RUnlock()
		return len(srv.hub.clients) == count-1 && srv.hub.pending == 0
	})
	_ = dialTestWS(t, ts)
}
func TestFailedHandshakeReleasesReservation(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	srv.hub.maxClients = 1
	response, err := http.Get(ts.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	srv.hub.mu.RLock()
	pending := srv.hub.pending
	srv.hub.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("failed handshake left %d reservations", pending)
	}
	c := dialTestWS(t, ts)
	_ = readTestEnvelope(t, c)
}

type delayedHandshakeConn struct {
	net.Conn
	entered, release chan struct{}
	once             sync.Once
}

func (c *delayedHandshakeConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Write(p)
}

type delayedHandshakeWriter struct {
	http.ResponseWriter
	entered, release chan struct{}
}

func (w delayedHandshakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &delayedHandshakeConn{Conn: c, entered: w.entered, release: w.release}, b, nil
}
func TestPendingHandshakeDoesNotHoldDispatchOrPreventShutdown(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.hub.serveWS(delayedHandshakeWriter{w, entered, release}, r)
	}))
	defer ts.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, _, _ := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
		if c != nil {
			_ = c.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handshake never reached delayed write")
	}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	broadcast := make(chan struct{})
	go func() { srv.hub.broadcast(wsEnvelope{Type: "status.updated"}); close(broadcast) }()
	select {
	case <-broadcast:
	case <-time.After(time.Second):
		t.Fatal("pending network handshake held dispatch")
	}
	srv.hub.closeClients()
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake did not finish")
	}
	waitWS(t, func() bool {
		srv.hub.mu.RLock()
		defer srv.hub.mu.RUnlock()
		return srv.hub.pending == 0 && len(srv.hub.clients) == 0
	})
}

type countingPayload struct{ calls *atomic.Int64 }

func (p countingPayload) MarshalJSON() ([]byte, error) {
	p.calls.Add(1)
	return []byte(`{"text":"<unicode> 東京","n":42}`), nil
}
func TestBroadcastEncodesPayloadOnceAndFramesValidSequences(t *testing.T) {
	for _, n := range []int{1, 100, 150} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			srv, _ := newWSTestServer(t, "")
			var calls atomic.Int64
			clients := make([]*wsClient, n)
			for i := range clients {
				c := queueTestClient(srv.hub)
				clients[i] = c
				srv.hub.clients[c] = struct{}{}
			}
			srv.hub.broadcast(wsEnvelope{Type: "vote.received", Height: 3, Payload: countingPayload{&calls}})
			if calls.Load() != 1 {
				t.Fatalf("encoded payload %d times for %d clients", calls.Load(), n)
			}
			for _, c := range clients {
				env, ok := c.dequeue()
				if !ok {
					t.Fatal("missing broadcast")
				}
				var b bytes.Buffer
				_, err := env.writeTo(&b)
				if err != nil {
					t.Fatal(err)
				}
				var decoded wsEnvelope
				if err = json.Unmarshal(b.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.Seq != 1 || decoded.Height != 3 || decoded.Payload.(map[string]any)["n"] != float64(42) {
					t.Fatalf("bad wire envelope: %s", b.Bytes())
				}
				delete(srv.hub.clients, c)
			}
		})
	}
}
func TestQueueBytesAgeAndSnapshotSupersession(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	c := queueTestClient(srv.hub)
	c.queue(srv.hub.snapshot())
	first, _ := c.dequeue()
	c.queue(srv.hub.snapshot())
	srv.opts.State.Mutate(func(s *state.StateData) { s.Height = 99 })
	c.queue(srv.hub.snapshot())
	if len(c.pending) != 1 || c.seq != first.Seq+1 {
		t.Fatalf("adjacent snapshot was not superseded: len=%d seq=%d", len(c.pending), c.seq)
	}
	latest, _ := c.dequeue()
	if latest.Payload.(map[string]any)["height"] != int64(99) {
		t.Fatal("snapshot replacement kept old state")
	}
	c.queue(wsEnvelope{Type: "vote.received", Payload: strings.Repeat("x", wsQueueBytes/2)})
	c.queue(wsEnvelope{Type: "vote.received", Payload: strings.Repeat("y", wsQueueBytes/2)})
	if c.queuedBytes > wsQueueBytes || c.dropped.Load() == 0 {
		t.Fatal("byte bound not enforced")
	}
	c.mu.Lock()
	c.pending[0].queuedAt = time.Now().Add(-wsQueueMaxAge - time.Millisecond)
	c.mu.Unlock()
	c.queue(wsEnvelope{Type: "vote.received"})
	if !c.closed || c.queuedBytes != 0 {
		t.Fatal("stale queue was not stopped and released")
	}
}
func TestResyncStormSharesFreshBuild(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	var builds atomic.Int64
	srv.hub.observe = func(event string, _ float64) {
		if event == "payload_encoded_bytes/state.snapshot" {
			builds.Add(1)
		}
	}
	srv.hub.resyncLast = time.Now()
	clients := make([]*wsClient, 150)
	for i := range clients {
		clients[i] = queueTestClient(srv.hub)
		srv.hub.requestResync(clients[i])
		srv.hub.requestResync(clients[i])
	}
	srv.opts.State.Mutate(func(s *state.StateData) { s.Height = 123 })
	waitWS(t, func() bool {
		for _, c := range clients {
			c.mu.Lock()
			ready := len(c.pending) > 0
			c.mu.Unlock()
			if !ready {
				return false
			}
		}
		return true
	})
	if builds.Load() != 1 {
		t.Fatalf("resync burst built %d snapshots", builds.Load())
	}
	for _, c := range clients {
		env, ok := c.dequeue()
		if !ok || env.Type != "state.snapshot" || env.Payload.(map[string]any)["height"] != int64(123) {
			t.Fatal("resync baseline was missing/stale")
		}
	}
	srv.hub.closeClients()
}
func TestContextSubscriptionBarrierAndExplicitClears(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.hub.run(ctx)
	c := dialTestWS(t, ts)
	baseline := readTestEnvelope(t, c)
	p := baseline.Payload.(map[string]any)
	if p["schemaVersion"] != float64(1) || p["serverEpoch"] != srv.hub.epoch {
		t.Fatal("missing negotiation baseline")
	}
	sendTestCommand(t, c, "subscribe", "context")
	sendTestCommand(t, c, "unsubscribe", "state", "blocks", "divergence", "votes")
	sendTestCommand(t, c, "ping")
	ack := readTestEnvelope(t, c)
	if ack.Type != "pong" {
		t.Fatalf("missing subscription barrier: %s", ack.Type)
	}
	env := readTestEnvelope(t, c)
	if env.Type != "context.snapshot" || env.Seq != ack.Seq+1 {
		t.Fatalf("context stream: %+v", env)
	}
	p = env.Payload.(map[string]any)
	for _, field := range []string{"chain", "upgrade"} {
		value, ok := p[field]
		if !ok || value != nil {
			t.Fatalf("missing explicit clear for %s", field)
		}
	}
	for _, field := range []string{"validators", "blocks", "divergence"} {
		if _, ok := p[field]; ok {
			t.Fatalf("context contains heavy %s", field)
		}
	}
	sendTestCommand(t, c, "resync")
	if env = readTestEnvelope(t, c); env.Type != "state.snapshot" {
		t.Fatalf("context resync must be authoritative full snapshot: %s", env.Type)
	}
}
func TestNonVoteBurstDoesNotProduceExtraFullSnapshots(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.hub.run(ctx)
	waitWS(t, func() bool { _, ok := srv.opts.Bus.Stats()["wshub"]; return ok })
	c := dialTestWS(t, ts)
	_ = readTestEnvelope(t, c)
	for i := 0; i < 20; i++ {
		srv.opts.Bus.Publish(events.Event{Kind: events.KindStatusUpdated, Payload: events.StatusUpdated{}})
	}
	_ = c.SetReadDeadline(time.Now().Add(2200 * time.Millisecond))
	snapshots := 0
	for {
		var env wsEnvelope
		if err := c.ReadJSON(&env); err != nil {
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				t.Fatal(err)
			}
			break
		}
		if env.Type == "state.snapshot" {
			snapshots++
		}
	}
	if snapshots < 1 || snapshots > 3 {
		t.Fatalf("ordinary snapshot cadence produced %d snapshots", snapshots)
	}
}

func TestCommandParsingBudgetIsBoundedAndRefills(t *testing.T) {
	now := time.Now()
	b := newWSCommandBudget(now)
	for i := 0; i < wsCommandBurst; i++ {
		if !b.allow(now) {
			t.Fatal("initial command burst rejected early")
		}
	}
	if b.allow(now) {
		t.Fatal("command flood escaped budget")
	}
	if !b.allow(now.Add(time.Second / wsCommandsPerSecond)) {
		t.Fatal("command budget did not refill")
	}
}
