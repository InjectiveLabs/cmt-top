package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func TestAuthenticationProtectsAPIAndWebSocket(t *testing.T) {
	const token = "operator-secret"
	srv := &Server{opts: Options{Token: token}}
	for _, tc := range []struct {
		name, target, authorization string
		want                        int
	}{
		{"API missing token", "/api/state", "", http.StatusUnauthorized},
		{"API wrong token", "/api/state?token=wrong", "", http.StatusUnauthorized},
		{"WebSocket missing token", "/ws", "", http.StatusUnauthorized},
		{"API bearer", "/api/state", "Bearer " + token, http.StatusOK},
		{"WebSocket query token", "/ws?token=" + token, "", http.StatusOK},
		{"invalid bearer is not rescued by query", "/api/state?token=" + token, "Bearer wrong", http.StatusUnauthorized},
		{"health remains public", "/healthz", "", http.StatusOK},
		{"readiness remains public", "/readyz", "", http.StatusOK},
		{"login shell remains public", "/", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.target, nil)
			r.Header.Set("Authorization", tc.authorization)
			w := httptest.NewRecorder()
			srv.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.RawQuery, token) {
					t.Error("authenticated request exposed its query token to downstream handler")
				}
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestAuthenticationPreservesNonSecretQueryParameters(t *testing.T) {
	srv := &Server{opts: Options{Token: "secret"}}
	r := httptest.NewRequest(http.MethodGet, "/api/validators?token=secret&search=North+Star", nil)
	w := httptest.NewRecorder()
	srv.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("token") || r.URL.Query().Get("search") != "North Star" {
			t.Fatalf("unexpected downstream query: %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestCORSPreflightDoesNotRequireBearerToken(t *testing.T) {
	srv := &Server{opts: Options{Token: "secret", CORSOrigin: "https://dashboard.example"}}
	r := httptest.NewRequest(http.MethodOptions, "/api/state", nil)
	r.Header.Set("Origin", "https://dashboard.example")
	w := httptest.NewRecorder()
	srv.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached application handler")
	})).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://dashboard.example" {
		t.Fatalf("allowed origin = %q", got)
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatal("preflight does not allow Authorization header")
	}
}

func requestJSON(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestHTTPSnapshotCarriesUnavailableHealthAndExplicitRemoval(t *testing.T) {
	srv, _ := newWSTestServer(t, "secret")
	handler := srv.Handler()
	initial := requestJSON(t, handler, "/api/state?token=secret")
	if initial["health"].(map[string]any)["mode"] != "unavailable" {
		t.Fatalf("uninitialized upstream was shown healthy: %#v", initial["health"])
	}
	srv.opts.State.Mutate(func(s *state.StateData) {
		s.Height = 201
		s.LastCommittedHeight = 200
		s.Upgrade = &state.Upgrade{Name: "upgrade-v2", Height: 300}
		s.ConsensusError = errors.New("RPC offline")
	})
	before := requestJSON(t, handler, "/api/state?token=secret")
	if before["upgrade"] == nil || before["errors"].(map[string]any)["consensus"] != "RPC offline" {
		t.Fatalf("missing initial upgrade/error: %#v", before)
	}
	if before["height"] != float64(201) || before["committedHeight"] != float64(200) {
		t.Fatal("active and committed heights were not exposed separately")
	}
	srv.opts.State.Mutate(func(s *state.StateData) { s.Upgrade = nil; s.ConsensusError = nil })
	for _, path := range []string{"/api/state?token=secret", "/api/chain?token=secret"} {
		after := requestJSON(t, handler, path)
		if value, exists := after["upgrade"]; !exists || value != nil {
			t.Fatalf("%s cannot explicitly clear an upgrade: %#v", path, after)
		}
		if errs, exists := after["errors"]; exists && len(errs.(map[string]any)) != 0 {
			t.Fatalf("resolved errors remain in snapshot: %#v", errs)
		}
	}
}

func TestBlocksAPIProvidesRetainedSamples(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	handler := srv.Handler()
	empty := requestJSON(t, handler, "/api/blocks")
	if samples, ok := empty["items"].([]any); !ok || len(samples) != 0 {
		t.Fatalf("empty block history must be an array: %#v", empty)
	}
	srv.opts.State.Mutate(func(s *state.StateData) {
		s.Blocks = []state.BlockSample{{Height: 99, Time: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC), BlockTimeMs: 1234}}
	})
	response := requestJSON(t, handler, "/api/blocks")
	items := response["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["height"] != float64(99) || items[0].(map[string]any)["blockTimeMs"] != float64(1234) {
		t.Fatalf("retained sample not exposed with millisecond duration: %#v", items)
	}
}
