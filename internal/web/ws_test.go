package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func newWSTestServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	srv := New(Options{
		Token: token, State: state.New(), Bus: events.NewBus(32),
		Tracker: divergence.New(divergence.DefaultConfig()),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.middleware(http.HandlerFunc(srv.hub.serveWS)))
	t.Cleanup(func() {
		ts.Close()
		srv.hub.mu.RLock()
		for c := range srv.hub.clients {
			_ = c.conn.Close()
		}
		srv.hub.mu.RUnlock()
	})
	return srv, ts
}

func dialTestWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readTestEnvelope(t *testing.T, c *websocket.Conn) wsEnvelope {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var env wsEnvelope
	if err := c.ReadJSON(&env); err != nil {
		t.Fatal(err)
	}
	return env
}

func sendTestCommand(t *testing.T, c *websocket.Conn, typ string, channels ...string) {
	t.Helper()
	if err := c.WriteJSON(map[string]any{"type": typ, "channels": channels}); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketHandshakeAuthenticationAndOrigin(t *testing.T) {
	_, ts := newWSTestServer(t, "secret")
	for _, tc := range []struct {
		name, query, origin string
		want                int
	}{
		{"missing credentials", "", ts.URL, http.StatusUnauthorized},
		{"wrong credentials", "?token=wrong", ts.URL, http.StatusUnauthorized},
		{"foreign browser origin", "?token=secret", "https://untrusted.example", http.StatusForbidden},
		{"same origin", "?token=secret", ts.URL, http.StatusSwitchingProtocols},
		{"native client", "?token=secret", "", http.StatusSwitchingProtocols},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			c, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws"+tc.query, headers)
			if response == nil {
				t.Fatalf("handshake returned no HTTP response: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("handshake status = %d, want %d", response.StatusCode, tc.want)
			}
			if tc.want == http.StatusSwitchingProtocols {
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if env := readTestEnvelope(t, c); env.Type != "state.snapshot" || env.Seq != 1 {
					t.Fatalf("first envelope = %+v; want snapshot with seq 1", env)
				}
			}
		})
	}
}

func TestWebSocketFiltersDoNotCreateSequenceGaps(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	c := dialTestWS(t, ts)
	first := readTestEnvelope(t, c)
	if first.Type != "state.snapshot" || first.Seq != 1 {
		t.Fatalf("first envelope = %+v", first)
	}
	sendTestCommand(t, c, "unsubscribe", "votes", "blocks", "divergence")
	sendTestCommand(t, c, "ping")
	ack := readTestEnvelope(t, c)
	if ack.Type != "pong" || ack.Seq != first.Seq+1 {
		t.Fatalf("subscription barrier = %+v", ack)
	}
	for _, typ := range []string{"vote.received", "block.committed", "divergence.snapshot", "divergence.detected", "divergence.resolved"} {
		srv.hub.broadcast(wsEnvelope{Type: typ, Payload: map[string]any{}})
	}
	srv.hub.broadcast(wsEnvelope{Type: "status.updated", Payload: map[string]any{}})
	marker := readTestEnvelope(t, c)
	if marker.Type != "status.updated" || marker.Seq != ack.Seq+1 {
		t.Fatalf("filtered stream produced event or sequence gap: %+v after %+v", marker, ack)
	}
	sendTestCommand(t, c, "unsubscribe", "state")
	sendTestCommand(t, c, "ping")
	ack = readTestEnvelope(t, c)
	srv.hub.broadcast(wsEnvelope{Type: "status.updated", Payload: map[string]any{}})
	sendTestCommand(t, c, "ping")
	marker = readTestEnvelope(t, c)
	if marker.Type != "pong" || marker.Seq != ack.Seq+1 {
		t.Fatalf("state filter was ignored: %+v after %+v", marker, ack)
	}
}

func TestWebSocketResyncReturnsAuthoritativeSnapshotDespiteFilters(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	c := dialTestWS(t, ts)
	_ = readTestEnvelope(t, c)
	sendTestCommand(t, c, "unsubscribe", "state", "votes", "blocks", "divergence")
	sendTestCommand(t, c, "ping")
	ack := readTestEnvelope(t, c)
	srv.opts.State.Mutate(func(s *state.StateData) {
		s.Height = 123
		s.Round = 4
	})
	sendTestCommand(t, c, "resync")
	snap := readTestEnvelope(t, c)
	if snap.Type != "state.snapshot" || snap.Seq != ack.Seq+1 {
		t.Fatalf("resync response = %+v after %+v", snap, ack)
	}
	payload, ok := snap.Payload.(map[string]any)
	if !ok || payload["height"] != float64(123) || payload["round"] != float64(4) {
		t.Fatalf("resync did not return latest state: %#v", snap.Payload)
	}
}

func TestConcurrentWebSocketSubscriptionsAndBroadcasts(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	c := dialTestWS(t, ts)
	_ = readTestEnvelope(t, c)
	var wg sync.WaitGroup
	ready := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ready
		for i := 0; i < 1000; i++ {
			srv.hub.broadcast(wsEnvelope{Type: "vote.received", Payload: map[string]any{"n": i}})
		}
	}()
	close(ready)
	for i := 0; i < 250; i++ {
		sendTestCommand(t, c, "unsubscribe", "votes", "blocks", "divergence", "state")
		sendTestCommand(t, c, "subscribe", "votes", "blocks", "divergence", "state")
	}
	wg.Wait()
	sendTestCommand(t, c, "ping")
	var previous uint64
	for {
		env := readTestEnvelope(t, c)
		if env.Seq <= previous {
			t.Fatalf("non-monotonic client sequence: %d after %d", env.Seq, previous)
		}
		previous = env.Seq
		if env.Type == "pong" {
			break
		}
	}
}

func TestQueueOverflowIsObservableAndResyncRestoresBaseline(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	c := &wsClient{hub: srv.hub, queueLimit: 2, wake: make(chan struct{}, 1)}
	c.queue(srv.hub.snapshot())
	baseline, _ := c.dequeue()
	for i := 0; i < 3; i++ {
		c.queue(wsEnvelope{Type: "vote.received"})
	}
	firstRemaining, _ := c.dequeue()
	if firstRemaining.Seq != baseline.Seq+2 {
		t.Fatalf("overflow did not leave observable sequence gap: %d after %d", firstRemaining.Seq, baseline.Seq)
	}
	if got := c.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	srv.opts.State.Mutate(func(s *state.StateData) { s.Height = 456 })
	srv.hub.resync(c)
	_, _ = c.dequeue() // Last queued patch precedes the replacement baseline.
	snapshot, _ := c.dequeue()
	if snapshot.Type != "state.snapshot" || snapshot.Seq != 5 {
		t.Fatalf("replacement baseline = %+v", snapshot)
	}
	payload := snapshot.Payload.(map[string]any)
	if payload["height"] != int64(456) {
		t.Fatalf("resync captured stale state: %#v", payload["height"])
	}
}

func TestBlockTimeUsesMillisecondsInSnapshotAndEvents(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	srv.opts.State.Mutate(func(s *state.StateData) { s.BlockTime = 1250 * time.Millisecond })
	snap := srv.hub.snapshot().Payload.(map[string]any)
	env := srv.hub.envelope(events.Event{Kind: events.KindBlockTimeUpdated, Payload: events.BlockTimeUpdated{BlockTime: 1250 * time.Millisecond}})
	update := env.Payload.(map[string]any)
	if snap["blockTime"] != int64(1250) || update["blockTime"] != snap["blockTime"] {
		t.Fatalf("block time units differ: snapshot=%v event=%v", snap["blockTime"], update["blockTime"])
	}
}

func TestPeriodicSnapshotRepairsStateWithoutBusEvent(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.hub.run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("hub did not stop with its context")
		}
	})
	c := dialTestWS(t, ts)
	_ = readTestEnvelope(t, c)
	srv.opts.State.Mutate(func(s *state.StateData) { s.Height = 345; s.Upgrade = nil })
	// No bus event is published: this models lost internal notifications.
	env := readTestEnvelope(t, c)
	if env.Type != "state.snapshot" {
		t.Fatalf("periodic repair = %+v", env)
	}
	payload := env.Payload.(map[string]any)
	if payload["height"] != float64(345) {
		t.Fatalf("periodic snapshot did not reconcile state: %#v", payload)
	}
	if value, exists := payload["upgrade"]; !exists || value != nil {
		t.Fatal("periodic snapshot cannot clear removed upgrade")
	}
}
