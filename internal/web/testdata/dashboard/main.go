// Dashboard is a deterministic, local-only visual QA fixture. It never contacts
// a chain. Run from the repository root with:
// go run -tags webui ./internal/web/testdata/dashboard -scenario split
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
	"github.com/InjectiveLabs/cmt-top/internal/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18082", "local listen address")
	scenario := flag.String("scenario", "healthy", "healthy, split, upgrade, or unavailable")
	token := flag.String("token", "", "optional dashboard bearer token")
	flag.Parse()
	if *scenario != "healthy" && *scenario != "split" && *scenario != "upgrade" && *scenario != "unavailable" {
		log.Fatal("unknown fixture scenario")
	}
	if !strings.HasPrefix(*listen, "127.0.0.1:") {
		log.Fatal("fixture must bind to 127.0.0.1")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, bus := state.New(), events.NewBus(128)
	tracker := divergence.New(divergence.DefaultConfig())
	if *scenario == "unavailable" {
		st.Mutate(func(s *state.StateData) {
			s.ConsensusError = errors.New("fixture RPC is unavailable")
			s.Health.LastError = "No upstream data received"
		})
	} else {
		if *scenario == "upgrade" {
			seedUpgrade(st, tracker)
		} else {
			seed(st, tracker, *scenario)
		}
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-tick.C:
					st.Mutate(func(s *state.StateData) { s.Health.LastSuccessAt = now; s.Health.LastEventAt = now })
				}
			}
		}()
	}
	srv := web.New(web.Options{Listen: *listen, Token: *token, State: st, Bus: bus, Tracker: tracker,
		Version: "visual-fixture", ExplorerURL: "https://explorer.example/validator/{address}",
		Ready: func() bool { return *scenario != "unavailable" },
	})
	log.Printf("synthetic %s dashboard on http://%s", *scenario, *listen)
	log.Printf("round investigation: http://%s/blocks/1001/rounds", *listen)
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

// seedUpgrade models observations at an uncommitted upgrade height. The phase
// cohorts intentionally differ; that alone says nothing about binary versions.
func seedUpgrade(st *state.State, tracker *divergence.Tracker) {
	const height int64 = 1001
	now := time.Now().UTC()
	powers := []int64{35, 25, 18, 12, 7, 3}
	names := []string{"North Star — independent validator infrastructure", "Kraken One", "東京 Validator", "Atlas Network", "Kraken Two", "Quiet Node"}
	rows := make([]state.ValidatorWithVote, len(powers))
	roster := make([]divergence.ValidatorPower, len(powers))
	for i, power := range powers {
		address := fmt.Sprintf("%040X", i+1)
		rows[i] = state.ValidatorWithVote{
			Validator:      state.Validator{Address: address, Index: i, VotingPower: big.NewInt(power), VotingPowerPercent: float64(power)},
			RoundVote:      state.RoundVote{Address: address, IsProposer: i == 3},
			ChainValidator: &state.ChainValidator{Moniker: names[i], OperatorAddress: fmt.Sprintf("fixturevaloper1%s%d", strings.Repeat("q", 32), i+1), Active: true, CommissionRate: "0.050000000000000000"},
		}
		roster[i] = divergence.ValidatorPower{Address: address, Moniker: names[i], Power: power}
	}
	tracker.SetValidators(roster)
	// Empty string means observed nil; '-' means no observation in that phase.
	type phasePair struct{ prevote, precommit string }
	rounds := [][]phasePair{
		{{"a", "a"}, {"a", "b"}, {"b", "b"}, {"b", ""}, {"", "-"}, {"-", "-"}},
		{{"b", "a"}, {"a", "b"}, {"a", ""}, {"", "b"}, {"b", "b"}, {"-", "-"}},
		{{"a", "a"}, {"b", "a"}, {"a", "b"}, {"", ""}, {"-", "-"}, {"a", "-"}},
		{{"b", "b"}, {"b", "-"}, {"", "-"}, {"a", ""}, {"-", "-"}, {"-", "-"}},
	}
	observe := func(round int64, i int, kind divergence.VoteType, hash string) {
		tracker.IngestVote(divergence.VoteEvent{Height: height, Round: round, Type: kind, ValidatorAddr: rows[i].Validator.Address,
			BlockIDHash: strings.Repeat(hash, 64), Timestamp: now.Add(-time.Duration(3-round) * 20 * time.Second)})
	}
	for round, pairs := range rounds {
		tracker.ObserveRound(height, int64(round), rows[round%len(rows)].Validator.Address)
		for i, pair := range pairs {
			for _, phase := range []struct {
				kind divergence.VoteType
				hash string
			}{{divergence.Prevote, pair.prevote}, {divergence.Precommit, pair.precommit}} {
				if phase.hash == "-" {
					continue
				}
				observe(int64(round), i, phase.kind, phase.hash)
				if round == len(rounds)-1 {
					kind := state.VoteForBlock
					if phase.hash == "" {
						kind = state.VoteNil
					}
					vote := state.Vote{Kind: kind, BlockIDHash: strings.Repeat(phase.hash, 64), Timestamp: now}
					if phase.kind == divergence.Prevote {
						rows[i].RoundVote.Prevote = vote
					} else {
						rows[i].RoundVote.Precommit = vote
					}
				}
			}
		}
		if round < len(rounds)-1 {
			tracker.MarkRoundAdvanced(height, int64(round))
		}
	}
	// A repeated delivery is idempotent. A different same-phase hash remains
	// explicit evidence, while a delayed round-zero vote fills only its archive.
	observe(0, 0, divergence.Prevote, "a")
	observe(1, 2, divergence.Prevote, "b")
	observe(0, 5, divergence.Prevote, "b")
	st.Mutate(func(s *state.StateData) {
		s.Height, s.Round, s.Step, s.LastCommittedHeight = height, 3, 6, height-1
		s.StartTime = now.Add(-20 * time.Second)
		s.NodeStatus = &state.NodeStatus{Network: "fixture-upgrade-1", CometVersion: "0.38.21", Moniker: "Synthetic RPC", OurValidator: rows[0].Validator.Address, LatestHeight: height - 1}
		s.ActiveRPC = "http://fixture-rpc.invalid:26657"
		s.Health = state.Health{Mode: "streaming", WSConnected: true, WSEndpoint: s.ActiveRPC, HTTPEndpoint: s.ActiveRPC, LastSuccessAt: now, LastEventAt: now, StaleAfterMs: 30000}
		s.LastRound = &state.RoundView{Round: 3, Validators: rows, TotalVP: big.NewInt(100)}
		s.BlockTime = 1250 * time.Millisecond
		s.Upgrade = &state.Upgrade{Name: "fixture-upgrade-at-1001", Height: height}
		for blockHeight := height - 25; blockHeight < height; blockHeight++ {
			s.Blocks = append(s.Blocks, state.BlockSample{Height: blockHeight, Time: now.Add(-2*time.Minute + time.Duration(blockHeight-height+1)*1250*time.Millisecond), BlockTimeMs: 1250, NumTxs: int(blockHeight % 17), BlockIDHash: strings.Repeat("c", 64), AppHash: strings.Repeat("d", 64)})
		}
	})
}

func seed(st *state.State, tracker *divergence.Tracker, scenario string) {
	now := time.Now().UTC()
	powers := []int64{70, 10, 8, 5, 4, 3}
	names := []string{"North Star — independent validator infrastructure", "Kraken One", "東京 Validator", "Atlas Network", "Kraken Two", "Quiet Node"}
	rows := make([]state.ValidatorWithVote, len(powers))
	validatorPowers := make([]divergence.ValidatorPower, len(powers))
	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for i, power := range powers {
		address := fmt.Sprintf("%040X", i+1)
		rows[i] = state.ValidatorWithVote{
			Validator:      state.Validator{Address: address, Index: i, VotingPower: big.NewInt(power), VotingPowerPercent: float64(power)},
			RoundVote:      state.RoundVote{Address: address, IsProposer: i == 0},
			ChainValidator: &state.ChainValidator{Moniker: names[i], OperatorAddress: fmt.Sprintf("fixturevaloper1%s%d", strings.Repeat("q", 32), i+1), Active: true, CommissionRate: "0.050000000000000000"},
		}
		validatorPowers[i] = divergence.ValidatorPower{Address: address, Power: power, Moniker: names[i]}
		if i == len(powers)-1 {
			continue
		}
		hash := hashA
		if scenario == "split" && (i == 1 || i == 2) {
			hash = hashB
		}
		kind := state.VoteForBlock
		if i == 3 || i == 4 {
			hash = ""
			kind = state.VoteNil
		}
		rows[i].RoundVote.Prevote = state.Vote{Kind: kind, BlockIDHash: hash, Timestamp: now}
		rows[i].RoundVote.Precommit = rows[i].RoundVote.Prevote
	}
	tracker.SetValidators(validatorPowers)
	for _, height := range []int64{999, 1001} {
		for _, row := range rows {
			if row.RoundVote.Precommit.Kind == state.VoteAbsent {
				continue
			}
			tracker.IngestVote(divergence.VoteEvent{Height: height, Round: 2, Type: divergence.Precommit, ValidatorAddr: row.Validator.Address, BlockIDHash: row.RoundVote.Precommit.BlockIDHash, Timestamp: now})
		}
		if height == 999 {
			tracker.ResolveCommit(height, hashA)
		}
	}
	st.Mutate(func(s *state.StateData) {
		s.Height, s.Round, s.Step, s.LastCommittedHeight = 1001, 2, 6, 1000
		s.StartTime = now.Add(-3 * time.Second)
		s.NodeStatus = &state.NodeStatus{Network: "fixture-1", CometVersion: "0.38.21", Moniker: "Synthetic RPC", OurValidator: rows[0].Validator.Address, LatestHeight: 1000}
		s.ActiveRPC = "http://fixture-rpc.invalid:26657"
		s.Health = state.Health{Mode: "streaming", WSConnected: true, WSEndpoint: s.ActiveRPC, HTTPEndpoint: s.ActiveRPC, LastSuccessAt: now, LastEventAt: now, StaleAfterMs: 30000}
		s.LastRound = &state.RoundView{Round: 2, Validators: rows, TotalVP: big.NewInt(100)}
		s.BlockTime = 1250 * time.Millisecond
		s.Upgrade = &state.Upgrade{Name: "fixture-upgrade", Height: 1500}
		for height := int64(977); height <= 1000; height++ {
			s.Blocks = append(s.Blocks, state.BlockSample{Height: height, Time: now.Add(time.Duration(height-1000) * 1250 * time.Millisecond), BlockTimeMs: 1200 + (height%5)*25, NumTxs: int(height % 17), BlockIDHash: hashA, AppHash: hashA})
		}
		if scenario == "split" {
			s.RPCComparison = state.RPCComparison{Status: "mismatch", Height: 1000, ChainID: "fixture-1", CheckedAt: now,
				Endpoints: []state.EndpointResult{
					{Endpoint: s.ActiveRPC, Status: "reference", ChainID: "fixture-1", Height: 1000, LatestHeight: 1000, AppHash: hashA},
					{Endpoint: "http://fixture-observer.invalid:26657", Status: "mismatch", ChainID: "fixture-1", Height: 1000, LatestHeight: 1000, AppHash: hashB},
					{Endpoint: "http://fixture-lagging.invalid:26657", Status: "lagging", ChainID: "fixture-1", LatestHeight: 998},
				}}
		}
	})
}
