package divergence

import (
	"testing"
)

func mkValidators(n int, power int64) []ValidatorPower {
	out := make([]ValidatorPower, n)
	for i := 0; i < n; i++ {
		out[i] = ValidatorPower{
			Address: addr(i),
			Power:   power,
			Moniker: "v" + itoa(i),
		}
	}
	return out
}

func addr(i int) string {
	const hexD = "0123456789ABCDEF"
	b := make([]byte, 40)
	for j := 0; j < 40; j++ {
		b[j] = hexD[(i+j)%16]
	}
	b[0] = hexD[i/16%16]
	b[1] = hexD[i%16]
	return string(b)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func TestSingleGroupConsensus(t *testing.T) {
	tr := New(DefaultConfig())
	vs := mkValidators(10, 100)
	tr.SetValidators(vs)
	for _, v := range vs {
		tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: "deadbeef"})
	}
	rep, _ := tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: vs[0].Address, BlockIDHash: "deadbeef"})
	if rep.IsDivergent {
		t.Fatal("expected no divergence for unanimous round")
	}
	if len(rep.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(rep.Groups))
	}
	if rep.Groups[0].VotingPowerPct < 99.9 {
		t.Fatalf("expected ~100%% in single group, got %.2f", rep.Groups[0].VotingPowerPct)
	}
}

func TestTwoGroupSplit(t *testing.T) {
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	vs := mkValidators(10, 100)
	tr.SetValidators(vs)
	// First 7 vote for hash A; last 3 vote for hash B.
	for i, v := range vs {
		hash := "aaaa"
		if i >= 7 {
			hash = "bbbb"
		}
		tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: hash})
	}
	rep := tr.CurrentReport()
	if len(rep.Live) != 1 {
		t.Fatalf("expected 1 live round, got %d", len(rep.Live))
	}
	r := rep.Live[0]
	if !r.IsDivergent {
		t.Fatal("expected divergence with 30%% minority on different BlockID")
	}
	if len(r.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(r.Groups))
	}
	if r.Groups[0].BlockIDHash != "aaaa" {
		t.Fatalf("expected leader=aaaa, got %s", r.Groups[0].BlockIDHash)
	}
	if r.Groups[1].VotingPowerPct < 29 || r.Groups[1].VotingPowerPct > 31 {
		t.Fatalf("expected minority near 30%%, got %.2f", r.Groups[1].VotingPowerPct)
	}
}

func TestDuplicateVoteIgnored(t *testing.T) {
	tr := New(DefaultConfig())
	vs := mkValidators(3, 100)
	tr.SetValidators(vs)
	tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: vs[0].Address, BlockIDHash: "aa"})
	tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: vs[0].Address, BlockIDHash: "bb"})
	rep := tr.CurrentReport()
	r := rep.Live[0]
	if len(r.Groups) != 1 {
		t.Fatalf("expected duplicate to be ignored, got %d groups", len(r.Groups))
	}
	if r.Groups[0].BlockIDHash != "aa" {
		t.Fatalf("expected first vote retained, got %s", r.Groups[0].BlockIDHash)
	}
}

func TestNilVoteNotDivergence(t *testing.T) {
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	vs := mkValidators(10, 100)
	tr.SetValidators(vs)
	// 7 vote A, 3 vote nil. The nil group must NOT count as divergence.
	for i, v := range vs {
		hash := "aaaa"
		if i >= 7 {
			hash = ""
		}
		tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: hash})
	}
	r := tr.CurrentReport().Live[0]
	if r.IsDivergent {
		t.Fatal("nil-vote group must not count as divergence")
	}
}

func TestThreeGroupSplit(t *testing.T) {
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	vs := mkValidators(10, 100)
	tr.SetValidators(vs)
	hashes := []string{"aaaa", "aaaa", "aaaa", "aaaa", "aaaa", "aaaa", "bbbb", "bbbb", "cccc", "cccc"}
	for i, v := range vs {
		tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: hashes[i]})
	}
	r := tr.CurrentReport().Live[0]
	if !r.IsDivergent {
		t.Fatal("expected divergence with two 20%% minorities")
	}
	if len(r.Groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(r.Groups))
	}
}

func TestResolveCommitMarksCanonical(t *testing.T) {
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	vs := mkValidators(10, 100)
	tr.SetValidators(vs)
	for i, v := range vs {
		hash := "aaaa"
		if i >= 7 {
			hash = "bbbb"
		}
		tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: hash})
	}
	resolved := tr.ResolveCommit(1, "aaaa")
	if len(resolved) == 0 {
		t.Fatal("expected at least one resolved report")
	}
	var prec RoundReport
	for _, r := range resolved {
		if r.Type == Precommit {
			prec = r
		}
	}
	if !prec.Resolved {
		t.Fatal("expected resolved=true")
	}
	canonicalFound := false
	for _, g := range prec.Groups {
		if g.BlockIDHash == "aaaa" && !g.IsCanonical {
			t.Fatal("aaaa should be canonical")
		}
		if g.BlockIDHash == "aaaa" && g.IsCanonical {
			canonicalFound = true
		}
		if g.BlockIDHash == "bbbb" && g.IsCanonical {
			t.Fatal("bbbb must not be canonical")
		}
	}
	if !canonicalFound {
		t.Fatal("expected canonical group to be present")
	}
}

func TestEvictionBeyondHistorySize(t *testing.T) {
	tr := New(Config{ThresholdPct: 5, HistorySize: 2})
	vs := mkValidators(3, 100)
	tr.SetValidators(vs)
	for h := int64(1); h <= 5; h++ {
		for _, v := range vs {
			tr.IngestVote(VoteEvent{Height: h, Round: 0, Type: Precommit, ValidatorAddr: v.Address, BlockIDHash: "aa"})
		}
		tr.ResolveCommit(h, "aa")
	}
	// rounds map should only contain heights >= 5 - 2 = 3
	tr.mu.RLock()
	for k := range tr.rounds {
		if k.Height < 3 {
			tr.mu.RUnlock()
			t.Fatalf("unexpected retained round at height %d", k.Height)
		}
	}
	tr.mu.RUnlock()
}

func TestLateVoteAfterCommitDoesNotCreateLiveRound(t *testing.T) {
	// A vote arriving for height N after ResolveCommit(N) must not create a
	// fresh unresolved round — that would leave a stale entry in Live forever.
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	vs := mkValidators(3, 100)
	tr.SetValidators(vs)
	tr.ResolveCommit(10, "aa")
	tr.IngestVote(VoteEvent{Height: 10, Round: 0, Type: Precommit, ValidatorAddr: vs[0].Address, BlockIDHash: "aa"})
	if got := len(tr.CurrentReport().Live); got != 0 {
		t.Fatalf("late vote after commit must not appear live, got %d live rounds", got)
	}
}

func TestResolveCommitClosesOlderUnresolvedRounds(t *testing.T) {
	// If we miss a NewBlock event for height N, rounds at N stay unresolved.
	// The next ResolveCommit (for N+k) must mop them up so they don't linger.
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	vs := mkValidators(3, 100)
	tr.SetValidators(vs)
	for _, v := range vs {
		tr.IngestVote(VoteEvent{Height: 5, Round: 0, Type: Prevote, ValidatorAddr: v.Address, BlockIDHash: "aa"})
		tr.IngestVote(VoteEvent{Height: 6, Round: 0, Type: Prevote, ValidatorAddr: v.Address, BlockIDHash: "bb"})
	}
	if got := len(tr.CurrentReport().Live); got != 2 {
		t.Fatalf("setup: expected 2 live rounds, got %d", got)
	}
	// Skip ResolveCommit(5) entirely — simulate a missed NewBlock.
	tr.ResolveCommit(6, "bb")
	live := tr.CurrentReport().Live
	if len(live) != 0 {
		t.Fatalf("ResolveCommit(6) must close height 5 too, but %d round(s) still live", len(live))
	}
}

func TestDifferentValidatorPowers(t *testing.T) {
	tr := New(Config{ThresholdPct: 5, HistorySize: 16})
	// One whale (90%) and 9 small (1.11% each)
	vs := []ValidatorPower{
		{Address: addr(0), Power: 900, Moniker: "whale"},
	}
	for i := 1; i < 10; i++ {
		vs = append(vs, ValidatorPower{Address: addr(i), Power: 11, Moniker: "v" + itoa(i)})
	}
	tr.SetValidators(vs)
	// Whale votes A, all small validators vote B.
	tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: vs[0].Address, BlockIDHash: "aa"})
	for i := 1; i < 10; i++ {
		tr.IngestVote(VoteEvent{Height: 1, Round: 0, Type: Precommit, ValidatorAddr: vs[i].Address, BlockIDHash: "bb"})
	}
	r := tr.CurrentReport().Live[0]
	if r.Groups[0].BlockIDHash != "aa" {
		t.Fatalf("whale's group should lead, got %s", r.Groups[0].BlockIDHash)
	}
	if !r.IsDivergent {
		t.Fatal("expected divergence: 9.91%% minority on different BlockID")
	}
}
