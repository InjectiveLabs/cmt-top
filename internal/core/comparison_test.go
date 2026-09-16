package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/config"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

type comparisonFixture struct {
	chain            string
	height           int64
	hash             string
	badStatus        bool
	wrongBlockHeight bool
	mu               *sync.Mutex
	requested        []int64
}

func (f *comparisonFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.mu = &sync.Mutex{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var result any
		switch req.Method {
		case "status":
			if f.badStatus {
				http.Error(w, "unavailable", 503)
				return
			}
			result = map[string]any{"node_info": map[string]any{"network": f.chain}, "sync_info": map[string]any{"latest_block_height": fmt.Sprint(f.height)}}
		case "block":
			height, _ := strconv.ParseInt(strings.Trim(string(req.Params["height"]), "\""), 10, 64)
			f.mu.Lock()
			f.requested = append(f.requested, height)
			f.mu.Unlock()
			if f.wrongBlockHeight {
				height++
			}
			result = map[string]any{"block_id": map[string]any{"hash": "AA"}, "block": map[string]any{"header": map[string]any{"chain_id": f.chain, "height": fmt.Sprint(height), "time": "2026-09-16T00:00:00Z", "app_hash": f.hash}, "data": map[string]any{"txs": []string{}}}}
		default:
			http.Error(w, "unsupported", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRPCComparisonUsesMatchingHeightAndChain(t *testing.T) {
	for _, test := range []struct {
		name        string
		remote      comparisonFixture
		status, row string
		wantBlock   bool
	}{
		{"match", comparisonFixture{chain: "test", height: 12, hash: "AA"}, "matching", "match", true},
		{"mismatch", comparisonFixture{chain: "test", height: 10, hash: "BB"}, "mismatch", "mismatch", true},
		{"lag", comparisonFixture{chain: "test", height: 9, hash: "BB"}, "incomplete", "lagging", false},
		{"chain", comparisonFixture{chain: "other", height: 10, hash: "BB"}, "incomplete", "chain_mismatch", false},
		{"unavailable", comparisonFixture{chain: "test", height: 10, hash: "AA", badStatus: true}, "incomplete", "error", false},
		{"wrong_height", comparisonFixture{chain: "test", height: 10, hash: "AA", wrongBlockHeight: true}, "incomplete", "error", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reference := (&comparisonFixture{chain: "test", height: 10, hash: "AA"}).server(t)
			remote := test.remote.server(t)
			cfg := config.Defaults()
			cfg.Chain.RPCs = []config.RPC{{URL: reference.URL, Primary: true}}
			cfg.Chain.MonitoredRPCs = []string{remote.URL}
			st := state.New()
			o, err := New(cfg, events.NewBus(10), st)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			o.refreshRPCComparison(ctx)
			report := st.Snapshot().RPCComparison
			if report.Status != test.status || report.Height != 10 || report.ChainID != "test" || report.Endpoints[1].Status != test.row {
				t.Fatalf("unexpected report: %+v", report)
			}
			test.remote.mu.Lock()
			defer test.remote.mu.Unlock()
			if test.wantBlock {
				if len(test.remote.requested) != 1 || test.remote.requested[0] != 10 {
					t.Fatalf("comparison not pinned: %v", test.remote.requested)
				}
			} else if len(test.remote.requested) != 0 {
				t.Fatalf("queried incompatible/unready node: %v", test.remote.requested)
			}
		})
	}
}

func TestUnavailableReferenceCannotReportMatching(t *testing.T) {
	reference := (&comparisonFixture{chain: "test", height: 10, hash: "AA", badStatus: true}).server(t)
	remote := (&comparisonFixture{chain: "test", height: 10, hash: "AA"}).server(t)
	cfg := config.Defaults()
	cfg.Chain.RPCs = []config.RPC{{URL: reference.URL, Primary: true}}
	cfg.Chain.MonitoredRPCs = []string{remote.URL}
	st := state.New()
	o, err := New(cfg, events.NewBus(10), st)
	if err != nil {
		t.Fatal(err)
	}
	o.refreshRPCComparison(context.Background())
	if report := st.Snapshot().RPCComparison; report.Status != "incomplete" || report.Endpoints[1].Status != "error" {
		t.Fatalf("unavailable reference compared: %+v", report)
	}
}
