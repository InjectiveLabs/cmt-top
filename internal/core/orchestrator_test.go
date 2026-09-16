package core

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/chain/cometws"
	"github.com/InjectiveLabs/cmt-top/internal/config"
	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func TestRejectedVotesAreNotForwardedAndCommitAdvancesContext(t *testing.T) {
	st := state.New()
	st.Mutate(func(s *state.StateData) {
		s.LastRound = &state.RoundView{Validators: []state.ValidatorWithVote{{Validator: state.Validator{Address: "A", VotingPower: big.NewInt(100)}}}}
	})
	bus := events.NewBus(20)
	ch, cancel := bus.Subscribe("test")
	defer cancel()
	o := &Orchestrator{state: st, bus: bus, div: divergence.New(divergence.DefaultConfig())}
	o.div.SetValidators([]divergence.ValidatorPower{{Address: "A", Power: 100}})
	vote := cometws.VoteData{Height: 11, Round: 3, Type: cometws.VoteTypePrevote, ValidatorAddress: "A", BlockID: cometws.BlockID{Hash: "aa"}}
	o.handleVote(vote)
	<-ch
	vote.BlockID.Hash = "bb"
	o.handleVote(vote)
	select {
	case ev := <-ch:
		t.Fatalf("duplicate vote forwarded: %+v", ev)
	default:
	}
	if got := st.Snapshot().LastRound.Validators[0].RoundVote.Prevote.BlockIDHash; got != "aa" {
		t.Fatalf("first vote replaced by %q", got)
	}
	block := cometws.EventDataNewBlock{}
	block.Block.Header.Height = 11
	block.Block.Header.Time = time.Now()
	block.Block.Header.ProposerAddress = "A"
	block.BlockID.Hash = "aa"
	o.handleNewBlock(block)
	<-ch
	snap := st.Snapshot()
	if snap.LastCommittedHeight != 11 || snap.Height != 12 || snap.Round != 0 || snap.LastRound.Round != 0 || snap.LastRound.Validators[0].RoundVote.IsProposer {
		t.Fatalf("incorrect commit transition: %+v", snap)
	}
	o.handleVote(vote)
	select {
	case ev := <-ch:
		t.Fatalf("closed-height vote forwarded: %+v", ev)
	default:
	}
}

func TestStatusSeedsCommittedHeightAndHistoryWithoutInventingActiveHeight(t *testing.T) {
	server := (&comparisonFixture{chain: "test", height: 10, hash: "AA"}).server(t)
	cfg := config.Defaults()
	cfg.Chain.RPCs = []config.RPC{{URL: server.URL, Primary: true}}
	st := state.New()
	o, err := New(cfg, events.NewBus(10), st)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.refreshStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := st.Snapshot()
	if snap.LastCommittedHeight != 10 || snap.Height != 0 || len(snap.Blocks) != 1 || snap.Health.Mode != "polling" {
		t.Fatalf("bad status-only initialization: %+v", snap)
	}
}

func TestDelayedFallbackCannotClearRecoveredStream(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"round_state": map[string]any{"height/round/step": "11/0/4"}}})
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Chain.RPCs = []config.RPC{{URL: server.URL, Primary: true}}
	st := state.New()
	o, err := New(cfg, events.NewBus(10), st)
	if err != nil {
		t.Fatal(err)
	}
	st.Mutate(func(s *state.StateData) {
		s.LastRound = &state.RoundView{Validators: []state.ValidatorWithVote{{Validator: state.Validator{Address: "A", VotingPower: big.NewInt(100)}}}}
	})
	done := make(chan error, 1)
	go func() { done <- o.refreshConsensusViaHTTP(context.Background()) }()
	<-started
	st.Mutate(func(s *state.StateData) { s.Health.WSConnected = true })
	o.handleWSEvent(cometws.Event{Data: cometws.EventDataVote{Vote: cometws.VoteData{Height: 12, Type: cometws.VoteTypePrevote, ValidatorAddress: "A", BlockID: cometws.BlockID{Hash: "aa"}}}})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snap := st.Snapshot()
	if snap.Height != 12 || snap.LastRound.Validators[0].RoundVote.Prevote.Kind != state.VoteForBlock {
		t.Fatalf("fallback replaced recovered stream: %+v", snap)
	}
}

func TestUpgradeRemovalPublishesUpdate(t *testing.T) {
	var calls atomic.Int32
	lcd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"plan":{"name":"upgrade","height":"100"}}`))
		} else {
			_, _ = w.Write([]byte(`{"plan":null}`))
		}
	}))
	defer lcd.Close()
	cfg := config.Defaults()
	cfg.Chain.LCD = lcd.URL
	bus := events.NewBus(10)
	ch, cancel := bus.Subscribe("test", events.KindUpgradePlanUpdated)
	defer cancel()
	st := state.New()
	o, err := New(cfg, bus, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.refreshUpgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-ch
	if err := o.refreshUpgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	update := (<-ch).Payload.(events.UpgradePlanUpdated)
	if st.Snapshot().Upgrade != nil || update.Name != "" || update.Height != 0 {
		t.Fatalf("upgrade removal not emitted: %+v", update)
	}
}
