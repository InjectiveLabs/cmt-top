package core

import (
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func testValidator(address string, power int64) state.ValidatorWithVote {
	return state.ValidatorWithVote{
		Validator: state.Validator{Address: address, VotingPower: big.NewInt(power)},
		RoundVote: state.RoundVote{Address: address},
	}
}

func TestAdvanceRoundRejectsStaleEventsAndResetsNewContext(t *testing.T) {
	row := testValidator("AA", 100)
	row.RoundVote.Prevote = state.Vote{Kind: state.VoteForBlock, BlockIDHash: "previous"}
	row.RoundVote.Precommit = state.Vote{Kind: state.VoteNil}
	row.RoundVote.IsProposer = true
	started := time.Now().Add(-time.Minute)
	s := state.StateData{Height: 12, Round: 3, Step: 6, LastCommittedHeight: 11, StartTime: started,
		LastRound: &state.RoundView{Round: 3, Validators: []state.ValidatorWithVote{row}, TotalVP: big.NewInt(100)}}
	for _, event := range [][3]int64{{11, 99, 6}, {10, 0, 6}, {12, 2, 7}, {12, -1, 4}} {
		if advanceRound(&s, event[0], event[1], event[2]) {
			t.Errorf("stale context was accepted: %v", event)
		}
	}
	if s.Height != 12 || s.Round != 3 || s.Step != 6 || !s.StartTime.Equal(started) || !s.LastRound.Validators[0].RoundVote.IsProposer {
		t.Fatal("rejected event changed authoritative state")
	}
	if !advanceRound(&s, 12, 3, 4) || s.Step != 6 {
		t.Fatal("a lower-step vote in the current round regressed the consensus step")
	}
	if !advanceRound(&s, 12, 3, 7) || s.Step != 7 {
		t.Fatal("current context did not advance to a later step")
	}
	if !advanceRound(&s, 13, 0, 4) {
		t.Fatal("higher-height vote failed to open a context without NewRound")
	}
	if s.Height != 13 || s.Round != 0 || s.Step != 4 || s.LastRound.Round != 0 || !s.StartTime.After(started) {
		t.Fatalf("inconsistent new context: %+v", s)
	}
	got := s.LastRound.Validators[0].RoundVote
	if got.Address != "AA" || got.Prevote.Kind != state.VoteAbsent || got.Precommit.Kind != state.VoteAbsent || got.IsProposer {
		t.Fatalf("old votes/proposer leaked into next height: %+v", got)
	}
	s.LastRound.Validators[0].RoundVote.Prevote = state.Vote{Kind: state.VoteForBlock, BlockIDHash: "round-zero"}
	if !advanceRound(&s, 13, 1, 1) || s.LastRound.Validators[0].RoundVote.Prevote.Kind != state.VoteAbsent {
		t.Fatal("advancing a round retained the previous round's vote")
	}
}

func TestValidatorRefreshPreservesVotesByAddressNotPosition(t *testing.T) {
	a, b := testValidator("AA", 70), testValidator("BB", 30)
	a.RoundVote.Prevote = state.Vote{Kind: state.VoteForBlock, BlockIDHash: "block-a"}
	a.RoundVote.IsProposer = true
	b.RoundVote.Precommit = state.Vote{Kind: state.VoteNil}
	s := state.StateData{Height: 20, Round: 2, ValidatorHeight: 19,
		LastRound: &state.RoundView{Round: 2, Validators: []state.ValidatorWithVote{a, b}, TotalVP: big.NewInt(100)}}
	rows := []state.ValidatorWithVote{testValidator("BB", 40), testValidator("AA", 80)}
	if !mergeValidatorRound(&s, rows, big.NewInt(120), 20) {
		t.Fatal("current validator set was rejected")
	}
	if !reflect.DeepEqual(s.LastRound.Validators[0].RoundVote, b.RoundVote) || !reflect.DeepEqual(s.LastRound.Validators[1].RoundVote, a.RoundVote) {
		t.Fatalf("votes moved with table rows instead of identity: %+v", s.LastRound.Validators)
	}
	if s.LastRound.TotalVP.Cmp(big.NewInt(120)) != 0 || s.ValidatorHeight != 20 {
		t.Fatal("voting power or validator height remained stale")
	}
	// Same row count, entirely different identity at the old proposer's position.
	rows = []state.ValidatorWithVote{testValidator("BB", 40), testValidator("CC", 50)}
	mergeValidatorRound(&s, rows, big.NewInt(90), 21)
	replacement := s.LastRound.Validators[1]
	if replacement.RoundVote.Address != "CC" || replacement.RoundVote.IsProposer || replacement.RoundVote.Prevote.Kind != state.VoteAbsent {
		t.Fatalf("new validator inherited old validator's vote/proposer: %+v", replacement)
	}
	if mergeValidatorRound(&s, []state.ValidatorWithVote{testValidator("OLD", 1)}, big.NewInt(1), 20) || s.LastRound.TotalVP.Int64() != 90 {
		t.Fatal("a delayed validator response replaced a newer set")
	}
	// A view from another round cannot contribute observations to this one.
	s.Round = 3
	mergeValidatorRound(&s, []state.ValidatorWithVote{testValidator("BB", 40)}, big.NewInt(40), 21)
	if s.LastRound.Round != 3 || s.LastRound.Validators[0].RoundVote.Precommit.Kind != state.VoteAbsent {
		t.Fatal("validator refresh retained a vote from an unrelated round")
	}
}

func TestBlockHistoryIsOrderedBoundedAndDoesNotInventGapDurations(t *testing.T) {
	s := state.StateData{}
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	for height := int64(1); height <= 125; height++ {
		appendBlock(&s, state.BlockSample{Height: height, Time: start.Add(time.Duration(height) * 1500 * time.Millisecond)})
	}
	if len(s.Blocks) != state.BlockHistoryLimit || s.Blocks[0].Height != 6 || s.Blocks[len(s.Blocks)-1].Height != 125 {
		t.Fatalf("history not bounded to most recent blocks: length=%d first=%d last=%d", len(s.Blocks), s.Blocks[0].Height, s.Blocks[len(s.Blocks)-1].Height)
	}
	if s.Blocks[len(s.Blocks)-1].BlockTimeMs != 1500 {
		t.Fatal("adjacent block interval is not in milliseconds")
	}
	appendBlock(&s, state.BlockSample{Height: 125, Time: start.Add(time.Hour)})
	appendBlock(&s, state.BlockSample{Height: 124, Time: start.Add(time.Hour)})
	if s.Blocks[len(s.Blocks)-1].BlockTimeMs != 1500 {
		t.Fatal("duplicate or older block overwrote committed history")
	}
	appendBlock(&s, state.BlockSample{Height: 128, Time: start.Add(time.Hour)})
	if s.Blocks[len(s.Blocks)-1].BlockTimeMs != 0 {
		t.Fatal("gap between observed blocks was presented as one block's duration")
	}
}
