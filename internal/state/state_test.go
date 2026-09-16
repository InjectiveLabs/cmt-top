package state

import (
	"math/big"
	"testing"
	"time"
)

func TestHealthUsesSuccessfulDataNotMutationAge(t *testing.T) {
	now := time.Now()
	h := Health{StaleAfterMs: 1000}
	if got := h.ModeAt(now); got != "unavailable" {
		t.Fatal(got)
	}
	h.LastSuccessAt = now
	h.LastHTTPAt = now
	if got := h.ModeAt(now); got != "polling" {
		t.Fatal(got)
	}
	h.WSConnected = true
	h.LastEventAt = now
	if got := h.ModeAt(now); got != "streaming" {
		t.Fatal(got)
	}
	if got := h.ModeAt(now.Add(2 * time.Second)); got != "stale" {
		t.Fatal(got)
	}
	s := New()
	s.Mutate(func(d *StateData) { d.Health = h; d.Health.LastSuccessAt = now.Add(-time.Hour) })
	s.Mutate(func(d *StateData) { d.StatusError = errTest{} })
	if got := s.Snapshot().Health.Mode; got != "stale" {
		t.Fatalf("failed mutation refreshed health: %s", got)
	}
}

type errTest struct{}

func (errTest) Error() string { return "test" }

func TestSnapshotDoesNotExposeMutablePointers(t *testing.T) {
	s := New()
	s.Mutate(func(d *StateData) {
		d.LastRound = &RoundView{TotalVP: big.NewInt(10), Validators: []ValidatorWithVote{{Validator: Validator{VotingPower: big.NewInt(10)}, ChainValidator: &ChainValidator{Moniker: "original"}}}}
		d.RPCComparison.Endpoints = []EndpointResult{{Endpoint: "original"}}
	})
	copy := s.Snapshot()
	copy.LastRound.TotalVP.SetInt64(999)
	copy.LastRound.Validators[0].Validator.VotingPower.SetInt64(999)
	copy.LastRound.Validators[0].ChainValidator.Moniker = "changed"
	copy.RPCComparison.Endpoints[0].Endpoint = "changed"
	actual := s.Snapshot()
	if actual.LastRound.TotalVP.Int64() != 10 || actual.LastRound.Validators[0].Validator.VotingPower.Int64() != 10 || actual.LastRound.Validators[0].ChainValidator.Moniker != "original" || actual.RPCComparison.Endpoints[0].Endpoint != "original" {
		t.Fatal("snapshot leaked mutable state")
	}
}
