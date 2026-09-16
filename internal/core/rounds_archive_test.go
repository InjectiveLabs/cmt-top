package core

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/InjectiveLabs/cmt-top/internal/chain/cometws"
	"github.com/InjectiveLabs/cmt-top/internal/config"
	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func TestRoundArchiveReceivesLateAndConflictingVotesWithoutLivePatches(t *testing.T) {
	st := state.New()
	st.Mutate(func(s *state.StateData) {
		s.LastRound = &state.RoundView{Validators: []state.ValidatorWithVote{testValidator("A", 70), testValidator("B", 30)}, TotalVP: big.NewInt(100)}
	})
	bus := events.NewBus(20)
	ch, cancel := bus.Subscribe("archive-test", events.KindVoteReceived)
	defer cancel()
	o := &Orchestrator{state: st, bus: bus, div: divergence.New(divergence.DefaultConfig())}
	o.div.SetValidators([]divergence.ValidatorPower{{Address: "A", Power: 70}, {Address: "B", Power: 30}})
	round := cometws.EventDataNewRound{Height: 10, Round: 0, Step: "RoundStepNewRound"}
	round.Proposer.Address = "A"
	o.handleNewRound(round)
	vote := cometws.VoteData{Height: 10, Round: 0, Type: cometws.VoteTypePrevote, ValidatorAddress: "A", BlockID: cometws.BlockID{Hash: "one"}}
	o.handleVote(vote)
	<-ch
	vote.BlockID.Hash = "two"
	o.handleVote(vote)
	round.Round = 1
	round.Proposer.Address = "B"
	o.handleNewRound(round)
	vote.Type, vote.ValidatorAddress, vote.BlockID.Hash = cometws.VoteTypePrecommit, "B", "one"
	o.handleVote(vote)
	select {
	case event := <-ch:
		t.Fatalf("historical/conflicting evidence escaped as a live vote patch: %+v", event)
	default:
	}
	live := st.Snapshot()
	if live.Round != 1 || live.LastRound.Validators[1].RoundVote.Precommit.Kind != state.VoteAbsent {
		t.Fatal("historical evidence repainted the current round")
	}
	archive := o.div.BlockInvestigation(10)
	if len(archive.Rounds) != 2 || archive.Rounds[0].Proposer != "A" || archive.Rounds[1].Proposer != "B" {
		t.Fatal("accepted NewRound observations did not retain quiet rounds/proposers")
	}
	if !archive.Rounds[0].Validators[0].Prevote.Conflicting || archive.Rounds[0].Precommits.ObservedVotingPower != "30" {
		t.Fatal("core rejected evidence before the historical archive could capture it")
	}
	block := cometws.EventDataNewBlock{}
	block.Block.Header.Height = 10
	block.BlockID.Hash = "one"
	o.handleNewBlock(block)
	vote.ValidatorAddress = "A"
	o.handleVote(vote)
	committed := o.div.BlockInvestigation(10)
	if !committed.Committed || committed.CanonicalHash != "one" || committed.Rounds[0].Precommits.ObservedVotingPower != "100" {
		t.Fatal("late postcommit vote or canonical metadata missing from retained round")
	}
}

func TestHTTPFallbackArchivesRoundWithoutInventingVotes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"round_state": map[string]any{"height/round/step": "20/4/6"}}})
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.Chain.RPCs = []config.RPC{{URL: server.URL, Primary: true}}
	o, err := New(cfg, events.NewBus(10), state.New())
	if err != nil {
		t.Fatal(err)
	}
	o.div.SetValidators([]divergence.ValidatorPower{{Address: "A", Power: 100}})
	if err := o.refreshConsensusViaHTTP(context.Background()); err != nil {
		t.Fatal(err)
	}
	archive := o.div.BlockInvestigation(20)
	if len(archive.Rounds) != 1 || archive.Rounds[0].Round != 4 || archive.Coverage.MissingRounds != 4 || archive.Rounds[0].Proposer != "" {
		t.Fatalf("HTTP-observed round not archived accurately: %+v", archive)
	}
	if len(archive.Rounds[0].Prevotes.Groups) != 0 || archive.Rounds[0].Precommits.NotObservedVotingPower != "100" {
		t.Fatal("HTTP fallback invented vote evidence")
	}
}
