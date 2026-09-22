package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Oracle is intentionally independent of the application tracker. It stores
// the set of observed hashes per validator and phase directly from source events.
type Oracle struct {
	mu         sync.Mutex
	validators []Validator
	heights    map[int64]*ExpectedHeight
}
type ExpectedHeight struct {
	Height        int64                    `json:"height"`
	Committed     bool                     `json:"committed"`
	CanonicalHash string                   `json:"canonicalHash"`
	Roster        map[string]Validator     `json:"roster"`
	Rounds        map[int64]*ExpectedRound `json:"rounds"`
}
type ExpectedRound struct {
	Round    int64                       `json:"round"`
	Proposer string                      `json:"proposer"`
	Votes    map[string]map[int][]string `json:"votes"`
}

func NewOracle(vs []Validator) *Oracle {
	return &Oracle{validators: vs, heights: make(map[int64]*ExpectedHeight)}
}
func (o *Oracle) Observe(ev Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.heights[ev.Height]
	if h == nil {
		h = &ExpectedHeight{Height: ev.Height, Rounds: make(map[int64]*ExpectedRound)}
		h.Roster = make(map[string]Validator, len(o.validators))
		for _, v := range o.validators {
			h.Roster[v.Address] = v
		}
		o.heights[ev.Height] = h
	}
	if ev.Kind == "NewBlock" {
		h.Committed = true
		h.CanonicalHash = ev.Hash
	} else {
		r := h.Rounds[ev.Round]
		if r == nil {
			r = &ExpectedRound{Round: ev.Round, Votes: make(map[string]map[int][]string)}
			h.Rounds[ev.Round] = r
		}
		if ev.Kind == "NewRound" {
			r.Proposer = o.validators[ev.Validator].Address
		}
		if ev.Kind == "Vote" {
			addr := o.validators[ev.Validator].Address
			if r.Votes[addr] == nil {
				r.Votes[addr] = make(map[int][]string)
			}
			found := false
			for _, hash := range r.Votes[addr][ev.Phase] {
				if hash == ev.Hash {
					found = true
				}
			}
			if !found {
				r.Votes[addr][ev.Phase] = append(r.Votes[addr][ev.Phase], ev.Hash)
				sort.Strings(r.Votes[addr][ev.Phase])
			}
		}
	}
	// Bound fixture memory during indefinite streaming runs.
	if len(o.heights) > 256 {
		var oldest int64 = 1<<63 - 1
		for height := range o.heights {
			if height < oldest {
				oldest = height
			}
		}
		delete(o.heights, oldest)
	}
}

func (o *Oracle) Height(height int64) *ExpectedHeight {
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.heights[height]
	if h == nil {
		return nil
	}
	b, _ := json.Marshal(h)
	var copy ExpectedHeight
	_ = json.Unmarshal(b, &copy)
	return &copy
}

// CheckReport compares source evidence with returned full detail. Observation
// wall-clock timestamps are excluded: each run observes events at different
// times even though source vote timestamps and evidence are deterministic.
// The caller freezes replay and waits for ingestion before taking both views.
func CheckReport(body []byte, expected *ExpectedHeight) error {
	if expected == nil {
		return fmt.Errorf("oracle height unavailable")
	}
	var report struct {
		Height        int64             `json:"height"`
		Found         bool              `json:"found"`
		Committed     bool              `json:"committed"`
		CanonicalHash string            `json:"canonicalHash"`
		Rounds        []json.RawMessage `json:"rounds"`
		Details       []json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return err
	}
	if report.Height != expected.Height || !report.Found || report.Committed != expected.Committed || report.CanonicalHash != expected.CanonicalHash {
		return fmt.Errorf("height/commit mismatch: got %d found=%t committed=%t canonical=%s", report.Height, report.Found, report.Committed, report.CanonicalHash)
	}
	rounds := report.Rounds
	if report.Details != nil {
		rounds = report.Details
	} else {
		want := len(expected.Rounds)
		if want > 128 {
			want = 128
		}
		if len(rounds) != want {
			return fmt.Errorf("retained round count %d want %d", len(rounds), want)
		}
	}
	if len(rounds) == 0 && len(expected.Rounds) > 0 {
		return fmt.Errorf("missing all round detail")
	}
	for _, raw := range rounds {
		var rd struct {
			Round            int64  `json:"round"`
			Proposer         string `json:"proposer"`
			TotalVotingPower string `json:"totalVotingPower"`
			Validators       []struct {
				Address       string `json:"address"`
				VotingPower   string `json:"votingPower"`
				Moniker       string `json:"moniker"`
				KnownToRoster bool   `json:"knownToRoster"`
				Prevote       struct {
					Observed    bool `json:"observed"`
					Conflicting bool `json:"conflicting"`
					Hashes      []struct {
						Hash string `json:"hash"`
					} `json:"hashes"`
				} `json:"prevote"`
				Precommit struct {
					Observed    bool `json:"observed"`
					Conflicting bool `json:"conflicting"`
					Hashes      []struct {
						Hash string `json:"hash"`
					} `json:"hashes"`
				} `json:"precommit"`
			} `json:"validators"`
		}
		if err := json.Unmarshal(raw, &rd); err != nil {
			return err
		}
		want := expected.Rounds[rd.Round]
		if want == nil {
			return fmt.Errorf("unexpected round %d", rd.Round)
		}
		if rd.Proposer != want.Proposer {
			return fmt.Errorf("round %d proposer mismatch", rd.Round)
		}
		if len(expected.Roster) > 0 {
			var total int64
			for _, v := range expected.Roster {
				total += v.Power
			}
			if len(rd.Validators) != len(expected.Roster) || rd.TotalVotingPower != fmt.Sprint(total) {
				return fmt.Errorf("round %d roster size or total voting power mismatch", rd.Round)
			}
		}
		seen := make(map[string]bool)
		for _, v := range rd.Validators {
			seen[v.Address] = true
			if roster, ok := expected.Roster[v.Address]; ok {
				if !v.KnownToRoster || v.VotingPower != fmt.Sprint(roster.Power) || v.Moniker != roster.Moniker {
					return fmt.Errorf("round %d validator %s roster metadata mismatch", rd.Round, v.Address)
				}
			}
			for phase, hashes := range map[int][]struct {
				Hash string `json:"hash"`
			}{1: v.Prevote.Hashes, 2: v.Precommit.Hashes} {
				got := make([]string, 0, len(hashes))
				for _, hash := range hashes {
					got = append(got, hash.Hash)
				}
				sort.Strings(got)
				w := want.Votes[v.Address][phase]
				observed, conflicting := v.Prevote.Observed, v.Prevote.Conflicting
				if phase == 2 {
					observed, conflicting = v.Precommit.Observed, v.Precommit.Conflicting
				}
				if observed != (len(w) > 0) || conflicting != (len(w) > 1) {
					return fmt.Errorf("round %d validator %s phase %d observation flags mismatch", rd.Round, v.Address, phase)
				}
				if len(got) != len(w) {
					return fmt.Errorf("round %d validator %s phase %d hash count %d want %d", rd.Round, v.Address, phase, len(got), len(w))
				}
				for i := range got {
					if got[i] != w[i] {
						return fmt.Errorf("round %d validator %s phase %d hash mismatch", rd.Round, v.Address, phase)
					}
				}
			}
		}
		for addr := range want.Votes {
			if !seen[addr] {
				return fmt.Errorf("round %d missing validator %s", rd.Round, addr)
			}
		}
	}
	return nil
}

// SemanticDigest strips nondeterministic observation/request metadata while
// retaining source voteTimestamp, validator evidence and retention semantics.
func SemanticDigest(body []byte) (string, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	var normalize func(any)
	normalize = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				switch k {
				case "firstSeenAt", "lastSeenAt", "generatedAt", "capturedAt", "serverEpoch", "schemaVersion", "revision", "context":
					delete(x, k)
				default:
					normalize(value)
				}
			}
		case []any:
			for _, value := range x {
				normalize(value)
			}
		}
	}
	normalize(v)
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
