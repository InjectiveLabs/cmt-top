package cometws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSubscriptionURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:26657", "ws://localhost:26657/websocket"},
		{"https://example.test/rpc/", "wss://example.test/rpc/websocket"},
		{"wss://user:pass@example.test/base?token=synthetic", "wss://user:pass@example.test/base/websocket?token=synthetic"},
	} {
		got, err := subscriptionURL(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("subscriptionURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, raw := range []string{"", "file:///tmp/socket", "example.test", "ftp://example.test"} {
		if _, err := subscriptionURL(raw); err == nil {
			t.Errorf("accepted invalid endpoint %q", raw)
		}
	}
}

func TestCancellationClosesIdleSubscription(t *testing.T) {
	acknowledged := make(chan struct{})
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc/websocket" {
			t.Errorf("path = %q", r.URL.Path)
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request struct {
			ID     int               `json:"id"`
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method != "subscribe" || request.Params["query"] != "tm.event='Vote'" {
			t.Errorf("unexpected subscription %+v", request)
		}
		_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": json.RawMessage(`{}`)})
		close(acknowledged)
		_, _, _ = conn.ReadMessage()
		close(closed)
	}))
	defer srv.Close()
	c, err := New(Options{Endpoints: []string{srv.URL + "/rpc"}, Queries: []string{"tm.event='Vote'"}, Handler: func(Event) {}, IdleTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-acknowledged:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription missing")
	}
	cancel()
	for _, ch := range []chan struct{}{done, closed} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("cancellation left idle transport running")
		}
	}
}
