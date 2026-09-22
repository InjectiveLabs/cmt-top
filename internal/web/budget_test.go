package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/metrics"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
)

func TestForwardingTrustWalksFromSocketPeer(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for _, tc := range []struct{ peer, xff, want string }{
		{"192.0.2.1:42", "198.51.100.10", "192.0.2.1"},
		{"10.0.0.1:42", "198.51.100.10", "198.51.100.10"},
		{"10.0.0.1:42", "203.0.113.1, 198.51.100.10, 10.0.0.2", "198.51.100.10"},
		{"10.0.0.1:42", "bad, 10.0.0.2", "10.0.0.1"},
		{"10.0.0.1:42", "", "10.0.0.1"},
		{"[::ffff:192.0.2.1]:42", "198.51.100.10", "192.0.2.1"},
		{"[2001:db8:1:2::1234]:42", "198.51.100.10", "2001:db8:1:2::/64"},
	} {
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		if got := clientIP(r, trusted); got != tc.want {
			t.Errorf("peer=%s xff=%s: %s, want %s", tc.peer, tc.xff, got, tc.want)
		}
	}
}

func TestHTTPMetricsPreserveWebSocketUpgrade(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	reg := prometheus.NewRegistry()
	srv.opts.Capacity = metrics.NewCapacity(reg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if env := readTestEnvelope(t, c); env.Type != "state.snapshot" {
		t.Fatal("missing baseline through observation middleware")
	}
	deadline := time.Now().Add(time.Second)
	for {
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "cmt_top_http_request_seconds" {
				continue
			}
			for _, metric := range family.Metric {
				labels := map[string]string{}
				for _, label := range metric.Label {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["route"] == "/ws" && labels["status"] == "101" && metric.GetHistogram().GetSampleCount() == 1 {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("missing measured websocket upgrade")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAPIAllowsSharedNATAndSeparatesProbeBudget(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	handler := srv.Handler()
	for i := 0; i < 450; i++ {
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.RemoteAddr = "192.0.2.1:23456"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("normal shared-NAT request %d: %d", i, w.Code)
		}
	}
	srv.opts.APIRateLimit = 5
	handler = srv.Handler()
	var rejected int
	for i := 0; i < 20; i++ {
		r := httptest.NewRequest("GET", "/api/state", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests {
			rejected++
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry guidance")
			}
		}
	}
	if rejected == 0 {
		t.Fatal("excess API traffic was not bounded")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/session", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ordinary API traffic starved session probe: %d", w.Code)
	}
}

func TestSessionIsAuthenticatedAndConditionalHeadersAreAllowed(t *testing.T) {
	srv, _ := newWSTestServer(t, "secret")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/session", nil))
	if w.Code != 401 {
		t.Fatalf("session auth=%d", w.Code)
	}
	payload := requestJSON(t, srv.Handler(), "/api/session?token=secret")
	if payload["schemaVersion"] != float64(1) || payload["serverEpoch"] == "" {
		t.Fatalf("missing contract metadata: %#v", payload)
	}
	srv.opts.CORSOrigin = "https://dashboard.example"
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("OPTIONS", "/api/session", nil))
	if w.Header().Get("Access-Control-Expose-Headers") != "ETag, Retry-After" {
		t.Fatal("missing conditional/retry response exposure")
	}
}
