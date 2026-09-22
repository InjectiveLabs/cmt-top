package web

import (
	"bytes"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Admission promises an authoritative first message. It is safer to reject a
// reader that cannot receive it than to deliver an initial patch after eviction.
func TestCapacityOverflowCannotEvictInitialBaseline(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	c := queueTestClient(srv.hub)
	c.queueLimit = 2
	c.queue(srv.hub.snapshot())
	c.queue(wsEnvelope{Type: "vote.received", Payload: map[string]any{"height": 1}})
	c.queue(wsEnvelope{Type: "vote.received", Payload: map[string]any{"height": 2}})
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed && (len(c.pending) == 0 || c.pending[0].Seq != 1 || c.pending[0].Type != "state.snapshot") {
		t.Fatal("queue overflow discarded the authoritative first baseline without closing the connection")
	}
}

type blockedDataConn struct {
	net.Conn
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	deadline time.Time
}

func (c *blockedDataConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}
func (c *blockedDataConn) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("HTTP/")) {
		return c.Conn.Write(p)
	}
	c.once.Do(func() { close(c.entered) })
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-c.release:
		return c.Conn.Write(p)
	case <-timer.C:
		return 0, os.ErrDeadlineExceeded
	}
}

type firstSlowListener struct {
	net.Listener
	mu               sync.Mutex
	first            bool
	entered, release chan struct{}
}

func (l *firstSlowListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	l.mu.Lock()
	first := !l.first
	l.first = true
	l.mu.Unlock()
	if first {
		return &blockedDataConn{Conn: c, entered: l.entered, release: l.release}, nil
	}
	return c, nil
}

func TestCapacitySlowNetworkWriterDoesNotBlockOtherReaders(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Listener = &firstSlowListener{Listener: ts.Listener, entered: entered, release: release}
	ts.Start()
	defer ts.Close()
	defer close(release)
	defer srv.hub.closeClients()
	slow, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer never blocked")
	}
	start := time.Now()
	fast, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	_ = fast.SetReadDeadline(time.Now().Add(time.Second))
	var baseline wsEnvelope
	if err = fast.ReadJSON(&baseline); err != nil {
		t.Fatalf("healthy baseline delayed by slow reader: %v", err)
	}
	srv.hub.broadcast(wsEnvelope{Type: "vote.received", Payload: map[string]any{"height": 123}})
	var vote wsEnvelope
	if err = fast.ReadJSON(&vote); err != nil {
		t.Fatalf("healthy delivery delayed by slow reader: %v", err)
	}
	if baseline.Seq != 1 || vote.Seq != 2 || vote.Type != "vote.received" || time.Since(start) > time.Second {
		t.Fatal("healthy reader lost ordering or latency")
	}
	waitWS(t, func() bool { srv.hub.mu.RLock(); defer srv.hub.mu.RUnlock(); return len(srv.hub.clients) == 1 })
}
