package cometrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type rpcRequest struct {
	ID     json.RawMessage            `json:"id"`
	Method string                     `json:"method"`
	Params map[string]json.RawMessage `json:"params"`
}

func respond(w http.ResponseWriter, request rpcRequest, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func TestHangingEndpointTimesOutAndFailsOver(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer hung.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		respond(w, request, map[string]any{"node_info": map[string]any{"network": "test-chain"}})
	}))
	defer good.Close()
	pool, err := newPool([]Endpoint{{URL: hung.URL, Primary: true}, {URL: good.URL}}, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, endpoint, err := pool.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != good.URL || status.NodeInfo.Network != "test-chain" {
		t.Fatalf("failed to use healthy fallback: %s %+v", endpoint, status)
	}
}

func TestValidatorPagesArePinnedAndComplete(t *testing.T) {
	for _, scenario := range []string{"valid", "changed_height", "empty", "changed_total", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			var heights []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request rpcRequest
				_ = json.NewDecoder(r.Body).Decode(&request)
				var page string
				_ = json.Unmarshal(request.Params["page"], &page)
				height := string(request.Params["height"])
				mu.Lock()
				heights = append(heights, height)
				mu.Unlock()
				blockHeight, total := int64(10), 2
				address := "01"
				if page == "2" {
					address = "02"
				}
				if page == "2" && scenario == "changed_height" {
					blockHeight = 11
				}
				if page == "2" && scenario == "changed_total" {
					total = 3
				}
				if scenario == "duplicate" {
					address = "01"
				}
				validators := []any{map[string]any{"address": address, "voting_power": "1", "proposer_priority": "0"}}
				if page == "2" && scenario == "empty" {
					validators = nil
				}
				respond(w, request, map[string]any{"block_height": strconv.FormatInt(blockHeight, 10), "validators": validators, "count": fmt.Sprint(len(validators)), "total": fmt.Sprint(total)})
			}))
			defer server.Close()
			pool, err := NewPool([]Endpoint{{URL: server.URL}})
			if err != nil {
				t.Fatal(err)
			}
			vs, height, _, err := pool.AllValidators(context.Background(), nil)
			if scenario == "valid" {
				if err != nil || len(vs) != 2 || height != 10 {
					t.Fatalf("unexpected result: %v %d %v", len(vs), height, err)
				}
			} else if err == nil {
				t.Fatalf("expected rejection of %s", scenario)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(heights) < 2 || strings.Trim(heights[1], "\"") != "10" {
				t.Fatalf("second page not pinned: %v", heights)
			}
		})
	}
}
