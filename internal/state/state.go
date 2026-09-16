package state

import (
	"math/big"
	"sync"
	"time"
)

// State is the thread-safe wrapper around StateData.
//
// Orchestrator inputs call Mutate under the shared write lock. Snapshot returns
// a deep copy that is safe to retain without further synchronization.
type State struct {
	mu   sync.RWMutex
	data StateData
}

func New() *State {
	return &State{
		data: StateData{
			StartTime:     time.Now(),
			LastUpdate:    time.Now(),
			Health:        Health{Mode: "unavailable", StaleAfterMs: 30_000},
			RPCComparison: RPCComparison{Status: "not_configured", Endpoints: []EndpointResult{}},
		},
	}
}

// Mutate runs fn under the write lock. The pointer is the live state; fn may
// freely modify it. After fn returns, LastUpdate is bumped.
func (s *State) Mutate(fn func(*StateData)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
	s.data.LastUpdate = time.Now()
}

// Snapshot returns a deep copy of the current state. Safe to retain and read
// from any goroutine.
func (s *State) Snapshot() StateData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := cloneStateData(s.data)
	out.Health.Mode = out.Health.ModeAt(time.Now())
	return out
}

func (h Health) ModeAt(now time.Time) string {
	staleAfter := time.Duration(h.StaleAfterMs) * time.Millisecond
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	if h.LastSuccessAt.IsZero() {
		return "unavailable"
	}
	if now.Sub(h.LastSuccessAt) > staleAfter {
		return "stale"
	}
	if h.WSConnected && !h.LastEventAt.IsZero() && now.Sub(h.LastEventAt) <= staleAfter {
		return "streaming"
	}
	if !h.LastHTTPAt.IsZero() && now.Sub(h.LastHTTPAt) <= staleAfter {
		return "polling"
	}
	return "stale"
}

func cloneStateData(d StateData) StateData {
	out := d // shallow copy of scalars, error interfaces and pointers
	if d.LastRound != nil {
		rv := *d.LastRound
		rv.Validators = append([]ValidatorWithVote(nil), d.LastRound.Validators...)
		for i := range rv.Validators {
			rv.Validators[i].Validator = cloneValidator(rv.Validators[i].Validator)
			if cv := rv.Validators[i].ChainValidator; cv != nil {
				copy := *cv
				rv.Validators[i].ChainValidator = &copy
			}
		}
		if rv.TotalVP != nil {
			rv.TotalVP = new(big.Int).Set(rv.TotalVP)
		}
		out.LastRound = &rv
	}
	if d.AllRounds != nil {
		ar := *d.AllRounds
		ar.Validators = append([]Validator(nil), d.AllRounds.Validators...)
		for i := range ar.Validators {
			ar.Validators[i] = cloneValidator(ar.Validators[i])
		}
		ar.RoundsVotes = make([][]RoundVote, len(d.AllRounds.RoundsVotes))
		for i, r := range d.AllRounds.RoundsVotes {
			ar.RoundsVotes[i] = append([]RoundVote(nil), r...)
		}
		out.AllRounds = &ar
	}
	if d.ChainValidators != nil {
		out.ChainValidators = append([]ChainValidator(nil), d.ChainValidators...)
	}
	if d.NodeStatus != nil {
		ns := *d.NodeStatus
		out.NodeStatus = &ns
	}
	if d.Upgrade != nil {
		u := *d.Upgrade
		out.Upgrade = &u
	}
	out.Blocks = append([]BlockSample{}, d.Blocks...)
	out.RPCComparison.Endpoints = append([]EndpointResult{}, d.RPCComparison.Endpoints...)
	return out
}

func cloneValidator(v Validator) Validator {
	if v.VotingPower != nil {
		v.VotingPower = new(big.Int).Set(v.VotingPower)
	}
	return v
}
