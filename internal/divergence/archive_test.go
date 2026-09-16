package divergence

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func archiveVote(height, round int64, kind VoteType, address, hash string) VoteEvent {
	return VoteEvent{Height: height, Round: round, Type: kind, ValidatorAddr: address, BlockIDHash: hash}
}

func archiveValidator(t *testing.T, round InvestigationRound, address string) InvestigationValidator {
	t.Helper()
	for _, v := range round.Validators {
		if v.Address == address {
			return v
		}
	}
	t.Fatalf("validator %s missing from round %d", address, round.Round)
	return InvestigationValidator{}
}

func TestInvestigationRetainsQuietRoundsAndBothVotePhases(t *testing.T) {
	tracker := New(Config{HistorySize: 8, ThresholdPct: 100, IncludePrevotes: false})
	tracker.SetValidators([]ValidatorPower{{Address: "A", Moniker: "Alpha", Power: 70}, {Address: "B", Power: 30}})
	tracker.ObserveRound(10, 0, "A")
	tracker.IngestVote(archiveVote(10, 0, Prevote, "A", ""))
	tracker.IngestVote(archiveVote(10, 0, Prevote, "B", "block-a"))
	tracker.IngestVote(archiveVote(10, 0, Precommit, "A", "block-b"))
	tracker.ObserveRound(10, 1, "B")                                   // quiet round must still be inspectable
	tracker.ObserveVote(archiveVote(10, 0, Precommit, "B", "block-a")) // late older-round vote
	result := tracker.BlockInvestigation(10)
	if !result.Found || result.Status != "live" || len(result.Rounds) != 2 || result.Coverage.MissingRounds != 0 {
		t.Fatalf("round history incomplete: %+v", result)
	}
	round := result.Rounds[0]
	if round.Proposer != "A" || round.TotalVotingPower != "100" || len(round.Prevotes.Groups) != 2 || len(round.Precommits.Groups) != 2 {
		t.Fatalf("phase hashes or powers missing: %+v", round)
	}
	if round.Prevotes.Groups[0].Hash != "" || round.Prevotes.Groups[0].VotingPower != "70" {
		t.Fatal("nil prevote did not remain a weighted observed group")
	}
	if round.Precommits.ObservedVotingPower != "100" || len(round.Precommits.NotObservedValidators) != 0 {
		t.Fatal("late precommit did not complete retained historical phase")
	}
	quiet := result.Rounds[1]
	if len(quiet.Prevotes.Groups) != 0 || quiet.Prevotes.NotObservedVotingPower != "100" || len(quiet.Prevotes.NotObservedValidators) != 2 {
		t.Fatal("a quiet round was interpreted as nil-voting")
	}
	if len(tracker.CurrentReport().History) != 0 {
		t.Fatal("inspection unexpectedly changed incident history")
	}
}

func TestInvestigationConflictingDuplicatesPreserveUniqueParticipation(t *testing.T) {
	tracker := New(DefaultConfig())
	tracker.SetValidators([]ValidatorPower{{Address: "A", Power: 70}, {Address: "B", Power: 30}})
	v := archiveVote(10, 0, Prevote, "A", "one")
	v.Timestamp = time.Unix(100, 0).UTC()
	tracker.ObserveVote(v)
	first := tracker.BlockInvestigation(10)
	tracker.ObserveVote(v)
	tracker.IngestVote(v)
	duplicate := tracker.BlockInvestigation(10)
	if !reflect.DeepEqual(first, duplicate) {
		t.Fatal("identical duplicate changed evidence timestamps or content")
	}
	tracker.IngestVote(archiveVote(10, 0, Prevote, "A", "two"))
	// A different hash in the following phase is an ordinary phase change.
	tracker.IngestVote(archiveVote(10, 0, Precommit, "A", "three"))
	result := tracker.BlockInvestigation(10).Rounds[0]
	a := archiveValidator(t, result, "A")
	if !a.Prevote.Conflicting || len(a.Prevote.Hashes) != 2 || a.Precommit.Conflicting {
		t.Fatal("same-phase conflict and phase-to-phase switch were conflated")
	}
	if result.Prevotes.ObservedVotingPower != "70" || result.Prevotes.NotObservedVotingPower != "30" || len(result.Prevotes.ConflictingValidators) != 1 {
		t.Fatal("conflicting hashes double-counted observed participation")
	}
	if result.Prevotes.Groups[0].VotingPower != "70" || result.Prevotes.Groups[1].VotingPower != "70" {
		t.Fatal("alternative hash memberships were lost")
	}
	for i := 3; i < 9; i++ {
		tracker.ObserveVote(archiveVote(10, 0, Prevote, "A", fmt.Sprintf("hash-%d", i)))
	}
	bounded := tracker.BlockInvestigation(10)
	a = archiveValidator(t, bounded.Rounds[0], "A")
	if !bounded.Truncated || !a.Prevote.Truncated || len(a.Prevote.Hashes) != InvestigationHashLimit {
		t.Fatal("alternative hash retention is unbounded or undisclosed")
	}
}

func TestInvestigationRosterSnapshotAndIndependentCopies(t *testing.T) {
	tracker := New(DefaultConfig())
	tracker.SetValidators([]ValidatorPower{{Address: "A", Moniker: "Original", Power: 70}, {Address: "B", Power: 30}})
	v := archiveVote(10, 0, Prevote, "A", "one")
	v.Timestamp = time.Unix(100, 0).UTC()
	tracker.IngestVote(v)
	tracker.SetValidators([]ValidatorPower{{Address: "A", Moniker: "Changed", Power: 1}, {Address: "C", Power: 99}})
	tracker.ObserveRound(10, 1, "C")
	before := tracker.BlockInvestigation(10)
	if archiveValidator(t, before.Rounds[0], "A").VotingPower != "70" || archiveValidator(t, before.Rounds[1], "A").VotingPower != "1" {
		t.Fatal("a validator refresh rewrote earlier round metadata")
	}
	copy := tracker.BlockInvestigation(10)
	copy.Rounds[0].Validators[0].Moniker = "corrupted"
	copy.Rounds[0].Validators[0].Prevote.Hashes[0].Hash = "corrupted"
	*copy.Rounds[0].Validators[0].Prevote.Hashes[0].VoteTimestamp = time.Time{}
	copy.Rounds[0].Prevotes.Groups[0].Validators[0] = "corrupted"
	copy.Rounds[1].Prevotes.NotObservedValidators[0] = "corrupted"
	copy.Retention.RetainedHeights[0] = 999
	*copy.FirstSeenAt = time.Time{}
	*copy.Coverage.FirstObservedRound = 999
	if !reflect.DeepEqual(before, tracker.BlockInvestigation(10)) {
		t.Fatal("consumer mutation changed retained evidence")
	}
}

func TestInvestigationCommitLateVoteAndCoverage(t *testing.T) {
	tracker := New(DefaultConfig())
	tracker.SetValidators([]ValidatorPower{{Address: "A", Power: 70}, {Address: "B", Power: 30}})
	tracker.ObserveRound(10, 2, "A")
	tracker.IngestVote(archiveVote(10, 2, Prevote, "A", "canonical"))
	tracker.ResolveCommit(10, "canonical")
	tracker.IngestVote(archiveVote(10, 2, Precommit, "B", "canonical"))
	tracker.ObserveVote(archiveVote(10, 0, Prevote, "A", "missing-round"))
	tracker.ObserveRound(10, 3, "B")
	result := tracker.BlockInvestigation(10)
	if result.Status != "committed" || !result.Committed || result.CanonicalHash != "canonical" || len(result.Rounds) != 1 || result.Coverage.MissingRounds != 2 {
		t.Fatalf("commit or coverage wrong: %+v", result)
	}
	if result.Rounds[0].Precommits.ObservedVotingPower != "30" || !result.Rounds[0].Precommits.Groups[0].IsCanonical {
		t.Fatal("late retained vote was not preserved with canonical classification")
	}
	tracker.ObserveRound(11, 0, "A")
	tracker.ResolveCommit(12, "newer")
	if result := tracker.BlockInvestigation(11); result.Status != "passed" || result.Committed || result.CanonicalHash != "" {
		t.Fatal("missed commit was presented as an observed canonical commit")
	}
	tracker.ObserveVote(archiveVote(9, 0, Prevote, "A", "late-unobserved"))
	if tracker.BlockInvestigation(9).Found {
		t.Fatal("late committed vote created unobserved history")
	}
}

func TestInvestigationBoundsAndEvictedContextsCannotResurrect(t *testing.T) {
	tracker := New(Config{HistorySize: 2})
	tracker.SetValidators([]ValidatorPower{{Address: "A", Power: 100}})
	for round := int64(0); round < InvestigationRoundLimit+2; round++ {
		tracker.IngestVote(archiveVote(10, round, Prevote, "A", "a"))
		tracker.IngestVote(archiveVote(10, round, Precommit, "A", "a"))
	}
	result := tracker.BlockInvestigation(10)
	if len(result.Rounds) != InvestigationRoundLimit || result.Rounds[0].Round != 2 || !result.Truncated || result.Coverage.RoundsEvicted != 2 || result.Coverage.MissingRounds != 0 {
		t.Fatalf("stalled-height archive unbounded or missing eviction disclosure: %+v", result.Coverage)
	}
	if len(tracker.CurrentReport().Live) != 2*InvestigationRoundLimit {
		t.Fatal("live detector not bounded with investigation archive")
	}
	tracker.IngestVote(archiveVote(10, 0, Prevote, "B", "late"))
	if len(tracker.BlockInvestigation(10).Rounds) != InvestigationRoundLimit || len(tracker.CurrentReport().Live) != 2*InvestigationRoundLimit {
		t.Fatal("evicted round was resurrected")
	}
	tracker.ObserveRound(11, 0, "A")
	tracker.ObserveRound(12, 0, "A")
	tracker.IngestVote(archiveVote(10, 200, Prevote, "A", "late"))
	tracker.ResolveCommit(10, "late-commit")
	if result := tracker.BlockInvestigation(10); result.Status != "evicted" || result.Found || !result.Truncated {
		t.Fatal("evicted height resurrected")
	}
	if got := tracker.BlockInvestigation(12).Retention.RetainedHeights; !reflect.DeepEqual(got, []int64{11, 12}) {
		t.Fatalf("wrong retained heights: %v", got)
	}
	if len(tracker.CurrentReport().Live) != 0 {
		t.Fatal("detector retained an evicted height")
	}
	for i := 0; i < InvestigationUnknownValidatorLimit+10; i++ {
		tracker.IngestVote(archiveVote(12, 0, Prevote, fmt.Sprintf("unknown-%d", i), "hash"))
	}
	bounded := tracker.BlockInvestigation(12)
	if len(bounded.Rounds[0].Validators) != InvestigationUnknownValidatorLimit+1 || !bounded.Truncated || bounded.Coverage.ValidatorRosterComplete {
		t.Fatal("unknown validator cap missing")
	}
}

func TestInvestigationMalformedInputMissingRosterAndExactPower(t *testing.T) {
	tracker := New(DefaultConfig())
	for _, event := range []VoteEvent{
		archiveVote(0, 0, Prevote, "A", "a"), archiveVote(1, -1, Prevote, "A", "a"),
		archiveVote(1, math.MaxInt64, Prevote, "A", "a"), archiveVote(1, 0, VoteType(5), "A", "a"), archiveVote(1, 0, Prevote, "", "a"),
		archiveVote(1, 0, Prevote, strings.Repeat("a", 129), "a"), archiveVote(1, 0, Prevote, "A", strings.Repeat("a", 129)),
	} {
		tracker.IngestVote(event)
	}
	if len(tracker.archive) != 0 {
		t.Fatal("malformed votes created history")
	}
	tracker.ObserveRound(1, -1, "A")
	tracker.ObserveRound(1, math.MaxInt64, "A")
	tracker.ObserveRound(1, 0, strings.Repeat("a", 129))
	if len(tracker.archive) != 0 {
		t.Fatal("malformed round created history")
	}
	tracker.ObserveVote(archiveVote(2, 0, Prevote, "A", "a"))
	unknown := tracker.BlockInvestigation(2)
	if unknown.Coverage.ValidatorRosterComplete || unknown.Rounds[0].TotalVotingPower != "0" || unknown.Rounds[0].Validators[0].KnownToRoster {
		t.Fatal("missing roster coverage was hidden")
	}
	tracker.SetValidators([]ValidatorPower{{Address: "A", Power: 9007199254740993}, {Address: "B", Power: 1}})
	known := tracker.BlockInvestigation(2)
	if !known.Coverage.ValidatorRosterComplete || known.Rounds[0].TotalVotingPower != "9007199254740994" || known.Rounds[0].Prevotes.ObservedVotingPower != "9007199254740993" {
		t.Fatal("roster backfill lost exact voting power")
	}
	body, err := json.Marshal(tracker.BlockInvestigation(999))
	if err != nil {
		t.Fatal(err)
	}
	var jsonResult map[string]any
	if err := json.Unmarshal(body, &jsonResult); err != nil {
		t.Fatal(err)
	}
	if _, exists := jsonResult["firstSeenAt"]; exists {
		t.Fatal("missing observation serialized a fabricated timestamp")
	}
}
