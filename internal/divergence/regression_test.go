package divergence

import "testing"

func TestNilMajorityDoesNotCreateBlockSplit(t *testing.T) {
	tr := New(DefaultConfig())
	tr.SetValidators([]ValidatorPower{{Address: "A", Power: 70}, {Address: "B", Power: 30}})
	tr.IngestVote(VoteEvent{Height: 1, Type: Precommit, ValidatorAddr: "A"})
	report, trigger := tr.IngestVote(VoteEvent{Height: 1, Type: Precommit, ValidatorAddr: "B", BlockIDHash: "aa"})
	if report.IsDivergent || trigger {
		t.Fatalf("one non-nil block is not a split: %+v", report)
	}
}

func TestSplitTriggersOnlyOnceAndCommitIsIdempotent(t *testing.T) {
	tr := New(DefaultConfig())
	vs := mkValidators(10, 10)
	tr.SetValidators(vs)
	triggers := 0
	for i, v := range vs {
		hash := "aa"
		if i == 0 {
			hash = "bb"
		}
		_, trigger := tr.IngestVote(VoteEvent{Height: 1, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: hash})
		if trigger {
			triggers++
		}
	}
	if triggers != 1 {
		t.Fatalf("one round created %d incidents", triggers)
	}
	tr.ResolveCommit(1, "aa")
	if got := len(tr.ResolveCommit(1, "aa")); got != 0 {
		t.Fatalf("duplicate commit re-resolved %d reports", got)
	}
	if got := len(tr.CurrentReport().History); got != 1 {
		t.Fatalf("expected one history entry, got %d", got)
	}
}

func TestLateValidatorSeedReweightsReceivedVotes(t *testing.T) {
	tr := New(DefaultConfig())
	tr.IngestVote(VoteEvent{Height: 1, Type: Precommit, ValidatorAddr: "A", BlockIDHash: "aa"})
	tr.SetValidators([]ValidatorPower{{Address: "A", Power: 70}, {Address: "B", Power: 30}})
	r := tr.CurrentReport().Live[0]
	if r.TotalVotedPower != 70 || r.Groups[0].VotingPowerPct != 70 {
		t.Fatalf("seed did not reweight existing observations: %+v", r)
	}
}

func TestAdvancedRoundDoesNotCreateNewIncident(t *testing.T) {
	tr := New(DefaultConfig())
	tr.SetValidators([]ValidatorPower{{Address: "A", Power: 60}, {Address: "B", Power: 40}})
	tr.IngestVote(VoteEvent{Height: 1, Type: Precommit, ValidatorAddr: "A", BlockIDHash: "aa"})
	tr.MarkRoundAdvanced(1, 0)
	_, trigger := tr.IngestVote(VoteEvent{Height: 1, Type: Precommit, ValidatorAddr: "B", BlockIDHash: "bb"})
	if trigger {
		t.Fatal("late observation created a new live incident")
	}
}
