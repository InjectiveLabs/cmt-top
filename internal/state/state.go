package state

import (
	"sync"
	"time"
)

// State is the thread-safe wrapper around StateData.
//
// Invariant: only one goroutine (the orchestrator) calls Mutate. Many goroutines
// may call Snapshot. Snapshot returns a deep copy that is safe to read without
// further synchronization.
type State struct {
	mu   sync.RWMutex
	data StateData
}

func New() *State {
	return &State{
		data: StateData{
			StartTime:  time.Now(),
			LastUpdate: time.Now(),
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
	return cloneStateData(s.data)
}

func cloneStateData(d StateData) StateData {
	out := d // shallow copy of scalars, error interfaces and pointers
	if d.LastRound != nil {
		rv := *d.LastRound
		rv.Validators = append([]ValidatorWithVote(nil), d.LastRound.Validators...)
		out.LastRound = &rv
	}
	if d.AllRounds != nil {
		ar := *d.AllRounds
		ar.Validators = append([]Validator(nil), d.AllRounds.Validators...)
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
	return out
}
