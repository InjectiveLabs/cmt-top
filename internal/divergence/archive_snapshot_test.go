package divergence

import (
	"fmt"
	"testing"
)

func TestArchiveRevisionsIncludeLateEvidenceAndCatalogueChanges(t *testing.T) {
	tr := New(Config{HistorySize: 2})
	tr.ObserveRound(10, 0, "A")
	v1 := tr.InvestigationVersion(10)
	tr.ObserveRound(10, 0, "A")
	if got := tr.InvestigationVersion(10); got.Evidence != v1.Evidence || got.Catalogue != v1.Catalogue {
		t.Fatal("identical round changed revisions")
	}
	tr.SetValidators([]ValidatorPower{{Address: "A", Power: 1}})
	v2 := tr.InvestigationVersion(10)
	if v2.Evidence <= v1.Evidence {
		t.Fatal("roster fill failed to invalidate")
	}
	tr.SetValidators([]ValidatorPower{{Address: "A", Power: 1}})
	if tr.InvestigationVersion(10).Evidence != v2.Evidence {
		t.Fatal("unchanged roster invalidated old evidence")
	}
	vote := VoteEvent{Height: 10, Round: 0, Type: Prevote, ValidatorAddr: "A", BlockIDHash: "a"}
	tr.ObserveVote(vote)
	captured := tr.CaptureInvestigation(10)
	before := captured.Build()
	tr.ObserveVote(vote)
	if tr.InvestigationVersion(10).Evidence != captured.Version.Evidence {
		t.Fatal("duplicate vote changed revision")
	}
	tr.ObserveRound(11, 0, "A")
	v3 := tr.InvestigationVersion(10)
	if v3.Evidence != captured.Version.Evidence || v3.Catalogue <= captured.Version.Catalogue || v3.Header.Status != "passed" {
		t.Fatal("global status invalidation rebuilt or missed evidence")
	}
	vote.BlockIDHash = "b"
	tr.ObserveVote(vote)
	if tr.InvestigationVersion(10).Evidence <= v3.Evidence {
		t.Fatal("conflicting vote not revisioned")
	}
	if len(captured.Build().Rounds[0].Validators[0].Prevote.Hashes) != 1 {
		t.Fatal("detached capture changed after later observation")
	}
	before.Rounds[0].Validators[0].Prevote.Hashes[0].Hash = "mutated"
	if tr.BlockInvestigation(10).Rounds[0].Validators[0].Prevote.Hashes[0].Hash == "mutated" {
		t.Fatal("detached caller changed tracker")
	}
	tr.ResolveCommit(10, "a")
	v4 := tr.InvestigationVersion(10)
	vote.Type = Precommit
	tr.ObserveVote(vote)
	if tr.InvestigationVersion(10).Evidence <= v4.Evidence {
		t.Fatal("late committed evidence not revisioned")
	}
	tr.ObserveRound(12, 0, "")
	evicted := tr.InvestigationVersion(10)
	if evicted.Header.Found || evicted.Header.Status != "evicted" || evicted.Catalogue <= v4.Catalogue {
		t.Fatal("height eviction not revisioned")
	}
}
func TestArchiveTruncationRevisionsOnlyChangeOnce(t *testing.T) {
	tr := New(DefaultConfig())
	tr.SetValidators([]ValidatorPower{{Address: "A", Power: 1}})
	vote := VoteEvent{Height: 10, Round: 0, Type: Prevote, ValidatorAddr: "A"}
	for i := 0; i < InvestigationHashLimit; i++ {
		vote.BlockIDHash = fmt.Sprint(i)
		tr.ObserveVote(vote)
	}
	before := tr.InvestigationVersion(10)
	vote.BlockIDHash = "overflow"
	tr.ObserveVote(vote)
	after := tr.InvestigationVersion(10)
	if after.Evidence <= before.Evidence || !tr.BlockInvestigation(10).Rounds[0].Truncated {
		t.Fatal("first rejected-hash truncation not revisioned")
	}
	tr.ObserveVote(vote)
	if tr.InvestigationVersion(10).Evidence != after.Evidence {
		t.Fatal("unchanged repeated truncation incremented revision")
	}
	for i := 0; i < InvestigationUnknownValidatorLimit; i++ {
		vote.ValidatorAddr = fmt.Sprint(i)
		tr.ObserveVote(vote)
	}
	// A separate round isolates its first unknown-voter truncation from hash truncation.
	vote.Round = 1
	for i := 0; i < InvestigationUnknownValidatorLimit; i++ {
		vote.ValidatorAddr = fmt.Sprint(i)
		tr.ObserveVote(vote)
	}
	before = tr.InvestigationVersion(10)
	vote.ValidatorAddr = "extra"
	tr.ObserveVote(vote)
	if tr.InvestigationVersion(10).Evidence <= before.Evidence {
		t.Fatal("unknown-voter limit did not invalidate truncation")
	}
}
func BenchmarkDetachedArchiveBuild(b *testing.B) {
	for _, rounds := range []int{1, 32, 128} {
		b.Run(fmt.Sprint(rounds), func(b *testing.B) {
			tr := New(DefaultConfig())
			vals := make([]ValidatorPower, 45)
			for i := range vals {
				vals[i] = ValidatorPower{Address: fmt.Sprintf("%040X", i+1), Power: 1}
			}
			tr.SetValidators(vals)
			for r := 0; r < rounds; r++ {
				for _, v := range vals {
					for _, kind := range []VoteType{Prevote, Precommit} {
						tr.ObserveVote(VoteEvent{Height: 10, Round: int64(r), ValidatorAddr: v.Address, Type: kind, BlockIDHash: "a"})
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			var locked int64
			for i := 0; i < b.N; i++ {
				c := tr.CaptureInvestigation(10)
				locked += c.LockDuration.Nanoseconds()
				_ = c.Build()
			}
			b.ReportMetric(float64(locked)/float64(b.N), "lock-ns/op")
		})
	}
}
