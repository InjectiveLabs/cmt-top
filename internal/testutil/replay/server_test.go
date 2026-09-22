package replay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/config"
	"github.com/InjectiveLabs/cmt-top/internal/core"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
	"github.com/InjectiveLabs/cmt-top/internal/testutil/replay"
)

var fixtureHTTPClient = &http.Client{Timeout: 5 * time.Second}

func TestSeededSourceAndTimestampNormalization(t *testing.T) {
	a, b := replay.Validators("mainnet45", 42), replay.Validators("mainnet45", 42)
	if len(a) != 45 || !reflect.DeepEqual(a, b) || reflect.DeepEqual(a, replay.Validators("mainnet45", 43)) {
		t.Fatal("validator seed is not deterministic/distinct")
	}
	if len(replay.Validators("testnet5", 42)) != 5 {
		t.Fatal("testnet profile")
	}
	x, _ := replay.SemanticDigest([]byte(`{"height":1,"firstSeenAt":"a","revision":"1","context":{"generatedAt":"a"},"voteTimestamp":"fixed","hash":"aa"}`))
	y, _ := replay.SemanticDigest([]byte(`{"height":1,"firstSeenAt":"b","revision":"2","context":{"generatedAt":"b"},"voteTimestamp":"fixed","hash":"aa"}`))
	z, _ := replay.SemanticDigest([]byte(`{"height":1,"firstSeenAt":"b","voteTimestamp":"different","hash":"aa"}`))
	if x != y || y == z {
		t.Fatal("normalization must ignore observation time and retain source vote timestamps")
	}
}

func TestReplayExercisesActualOrchestrator(t *testing.T) {
	for _, tc := range []struct {
		profile string
		rounds  int
	}{{"testnet5", 1}, {"mainnet45", 1}, {"mainnet45", 32}, {"testnet5", 128}, {"testnet5", 130}} {
		t.Run(tc.profile+"/"+time.Duration(tc.rounds).String(), func(t *testing.T) {
			src, err := replay.New(replay.Options{Profile: tc.profile, Seed: 42, Rounds: tc.rounds, Stalled: true, BlockInterval: time.Second, EventInterval: 100 * time.Microsecond, HistoryBlocks: 120})
			if err != nil {
				t.Fatal(err)
			}
			httpSource := httptest.NewServer(src)
			defer httpSource.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer src.Close()
			cfg := config.Defaults()
			cfg.Chain.RPCs = []config.RPC{{URL: httpSource.URL, Primary: true}}
			cfg.Chain.LCD = httpSource.URL
			cfg.Chain.MonitoredRPCs = nil
			cfg.Chain.ExplorerURL = ""
			cfg.Refresh.Status = config.Duration(time.Hour)
			cfg.Refresh.Validators = config.Duration(time.Hour)
			cfg.Refresh.BlockTime = config.Duration(time.Hour)
			cfg.Refresh.Consensus = config.Duration(time.Hour)
			st := state.New()
			bus := events.NewBus(4096)
			orch, err := core.New(cfg, bus, st)
			if err != nil {
				t.Fatal(err)
			}
			go orch.Run(ctx)
			go src.Run(ctx)
			deadline := time.Now().Add(15 * time.Second)
			var report []byte
			var mismatch error
			for time.Now().Before(deadline) {
				if src.Stats().Ready {
					report, _ = json.Marshal(orch.Tracker().BlockInvestigation(replay.InitialHeight + 1))
					mismatch = replay.CheckReport(report, sourceOracle(t, httpSource.URL, replay.InitialHeight+1))
					if mismatch == nil {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !src.Stats().Ready || mismatch != nil {
				t.Fatalf("replay failed: stats=%+v mismatch=%v state=%+v", src.Stats(), mismatch, st.Snapshot())
			}
			snap := st.Snapshot()
			if snap.NodeStatus == nil || snap.NodeStatus.Network != "capacity-replay-1" || snap.LastRound == nil || len(snap.LastRound.Validators) != src.Stats().Validators {
				t.Fatal("actual RPC/LCD roster did not reach state")
			}
			if len(snap.Blocks) != 120 {
				t.Fatalf("history=%d want120", len(snap.Blocks))
			}
			if src.Stats().UnexpectedRequests != 0 || src.Stats().Connections != 1 {
				t.Fatalf("unexpected upstream work: %+v", src.Stats())
			}
			for name, n := range bus.Stats() {
				if n != 0 {
					t.Fatalf("bus %s dropped %d", name, n)
				}
			}
			investigation := orch.Tracker().BlockInvestigation(replay.InitialHeight + 1)
			wantRounds := tc.rounds
			if wantRounds > 128 {
				wantRounds = 128
			}
			if len(investigation.Rounds) != wantRounds {
				t.Fatalf("rounds=%d want%d", len(investigation.Rounds), wantRounds)
			}
			if tc.rounds > 128 && investigation.Coverage.RoundsEvicted == 0 {
				t.Fatal("round eviction absent")
			}
			if old := orch.Tracker().BlockInvestigation(replay.InitialHeight - 119); old.Found {
				t.Fatal("old height retained beyond configured archive")
			}
			for _, v := range snap.LastRound.Validators {
				if v.ChainValidator == nil || !strings.HasPrefix(v.ChainValidator.Moniker, "Synthetic validator") {
					t.Fatal("LCD metadata missing")
				}
			}
		})
	}
}

func sourceOracle(t *testing.T, base string, height int64) *replay.ExpectedHeight {
	t.Helper()
	resp, err := fixtureHTTPClient.Get(fmt.Sprintf("%s/_fixture/oracle?height=%d", base, height))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var expected *replay.ExpectedHeight
	if err = json.NewDecoder(resp.Body).Decode(&expected); err != nil {
		t.Fatal(err)
	}
	return expected
}

func TestReplayUpstreamReconnectRetainsEvidence(t *testing.T) {
	src, err := replay.New(replay.Options{Profile: "testnet5", Seed: 7, Rounds: 2, Stalled: true, BlockInterval: time.Second, EventInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(src)
	defer server.Close()
	defer src.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Defaults()
	cfg.Chain.RPCs = []config.RPC{{URL: server.URL, Primary: true}}
	cfg.Chain.LCD = server.URL
	cfg.Chain.MonitoredRPCs = nil
	st := state.New()
	orch, err := core.New(cfg, events.NewBus(1024), st)
	if err != nil {
		t.Fatal(err)
	}
	go src.Run(ctx)
	go orch.Run(ctx)
	await := func(check func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for !check() {
			if time.Now().After(until) {
				t.Fatalf("reconnect condition timed out: %+v", src.Stats())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	await(func() bool {
		return src.Stats().Ready && orch.Tracker().BlockInvestigation(1001).Coverage.RoundsObserved == 2
	})
	control := func(body string) {
		t.Helper()
		resp, err := fixtureHTTPClient.Post(server.URL+"/_fixture/control", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(resp.Status)
		}
	}
	control(`{"paused":true,"disconnect":true}`)
	await(func() bool { return src.Stats().Connections >= 2 && src.Stats().Requests["ws.subscribe"] >= 8 })
	control(`{"paused":false}`)
	await(func() bool { return st.Snapshot().Health.WSConnected })
	await(func() bool {
		actual, _ := json.Marshal(orch.Tracker().BlockInvestigation(1001))
		return replay.CheckReport(actual, sourceOracle(t, server.URL, 1001)) == nil
	})
	if src.Stats().UnexpectedRequests != 0 {
		t.Fatal("unexpected request during reconnect")
	}
}

func TestOracleRejectsMissingOrAlteredEvidence(t *testing.T) {
	vs := replay.Validators("testnet5", 1)
	oracle := replay.NewOracle(vs)
	for _, ev := range replay.Round(1001, 0, vs) {
		oracle.Observe(ev)
	}
	if replay.CheckReport([]byte(`{"height":1001,"found":true,"rounds":[]}`), oracle.Height(1001)) == nil {
		t.Fatal("missing evidence accepted")
	}
}
