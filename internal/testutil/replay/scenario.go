// Package replay provides a synthetic CometBFT/LCD source. It never dials a
// network endpoint; all input is generated from a fixed seed.
package replay

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const Identity = "cmt-top-local-replay-v1"
const InitialHeight int64 = 1000

type Validator struct {
	Address   string `json:"address"`
	PublicKey string `json:"publicKey"`
	Moniker   string `json:"moniker"`
	Power     int64  `json:"power"`
}

type Options struct {
	Profile string
	Seed    int64
	Rounds  int
	// Stalled retains one uncommitted height after producing Rounds rounds.
	Stalled       bool
	BlockInterval time.Duration
	EventInterval time.Duration
	HistoryBlocks int
}

func (o Options) Validate() error {
	if o.Profile != "mainnet45" && o.Profile != "testnet5" {
		return fmt.Errorf("profile must be mainnet45 or testnet5")
	}
	if o.Rounds < 1 || o.Rounds > 256 {
		return fmt.Errorf("rounds must be in [1,256]")
	}
	if o.BlockInterval <= 0 || o.EventInterval < 0 {
		return fmt.Errorf("block interval must be positive and event interval nonnegative")
	}
	if o.HistoryBlocks < 0 || o.HistoryBlocks > 120 {
		return fmt.Errorf("history blocks must be in [0,120]")
	}
	return nil
}

func Validators(profile string, seed int64) []Validator {
	n := 45
	if profile == "testnet5" {
		n = 5
	}
	vs := make([]Validator, n)
	for i := range vs {
		key := sha256.Sum256([]byte(fmt.Sprintf("cmt-top-fixture:%d:%d", seed, i)))
		addr := sha256.Sum256(key[:])
		vs[i] = Validator{Address: strings.ToUpper(hex.EncodeToString(addr[:20])), PublicKey: base64.StdEncoding.EncodeToString(key[:]), Moniker: fmt.Sprintf("Synthetic validator %02d", i), Power: int64(10 + i%7)}
	}
	return vs
}

func Hash(height, round int64, group string) string {
	v := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", height, round, group)))
	return hex.EncodeToString(v[:])
}

func Timestamp(height, round int64, index int) time.Time {
	return time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC).Add(time.Duration(height-InitialHeight)*time.Second + time.Duration(round)*time.Millisecond + time.Duration(index)*time.Microsecond)
}

type Event struct {
	Kind      string
	Height    int64
	Round     int64
	Validator int
	Phase     int
	Hash      string
	Value     any
}

func roundEvent(height, round int64, vs []Validator) Event {
	i := int((height + round) % int64(len(vs)))
	return Event{Kind: "NewRound", Height: height, Round: round, Validator: i, Value: map[string]any{"height": fmt.Sprint(height), "round": fmt.Sprint(round), "step": "RoundStepPropose", "proposer": map[string]any{"address": vs[i].Address, "index": fmt.Sprint(i)}}}
}

func voteEvent(height, round int64, i, phase int, hash string, vs []Validator) Event {
	return Event{Kind: "Vote", Height: height, Round: round, Validator: i, Phase: phase, Hash: hash, Value: map[string]any{"Vote": map[string]any{"type": phase, "height": fmt.Sprint(height), "round": fmt.Sprint(round), "block_id": map[string]any{"hash": hash}, "timestamp": Timestamp(height, round, i), "validator_address": vs[i].Address, "validator_index": fmt.Sprint(i)}}}
}

// Round produces differing phase cohorts, nil votes, an exact duplicate, a
// conflicting observation and one delayed observation from the previous round.
// The final validator is initially absent and arrives after the other votes.
func Round(height, round int64, vs []Validator) []Event {
	out := []Event{roundEvent(height, round, vs)}
	for phase := 1; phase <= 2; phase++ {
		for i := 0; i < len(vs)-1; i++ {
			group := "a"
			if (i+phase)%3 == 0 {
				group = "b"
			}
			hash := Hash(height, round, group)
			if i == len(vs)-2 {
				hash = ""
			}
			out = append(out, voteEvent(height, round, i, phase, hash, vs))
		}
	}
	out = append(out, out[1]) // same observation must not invent extra evidence
	out = append(out, voteEvent(height, round, 0, 1, Hash(height, round, "conflict"), vs))
	if round > 0 {
		out = append(out, voteEvent(height, round-1, len(vs)-1, 2, Hash(height, round-1, "late"), vs))
	}
	return out
}

func Block(height int64, vs []Validator) Event {
	hash := Hash(height, 0, "a")
	value := map[string]any{"block_id": map[string]any{"hash": hash}, "block": map[string]any{"header": map[string]any{"chain_id": "capacity-replay-1", "height": fmt.Sprint(height), "time": Timestamp(height, 0, 0), "proposer_address": vs[int(height%int64(len(vs)))].Address, "app_hash": Hash(height, 0, "app")}, "data": map[string]any{"txs": []string{}}}}
	return Event{Kind: "NewBlock", Height: height, Hash: hash, Value: value}
}
