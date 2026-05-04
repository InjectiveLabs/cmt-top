// Package divergence implements the live AppHash divergence detector.
//
// During each consensus round, validators are grouped by the BlockID hash they
// signed. After commit, the canonical group is identified; non-canonical groups
// with significant voting power are surfaced as divergence — a strong signal
// that some validators computed a different AppHash for the previous block.
//
// The tracker is a pure function over (VoteEvent stream, ValidatorSet snapshot,
// canonical block id). No I/O, fully unit-testable.
package divergence

import (
	"sort"
	"sync"
)

// Config controls thresholds and history retention.
type Config struct {
	ThresholdPct    float64 // any non-leader group ≥ this VP% triggers divergence
	HistorySize     int     // last N heights of resolved rounds to retain
	IncludePrevotes bool    // emit prevote-stage divergence (noisy by default)
}

// DefaultConfig returns sensible production defaults.
func DefaultConfig() Config {
	return Config{ThresholdPct: 5.0, HistorySize: 32, IncludePrevotes: false}
}

type roundKey struct {
	Height int64
	Round  int64
	Type   VoteType
}

type roundData struct {
	key              roundKey
	groups           map[string]*groupData // by BlockIDHash
	seen             map[string]struct{}   // validator addrs that have voted
	totalVotingPower int64
	totalVotedPower  int64
	validators       map[string]ValidatorPower
	resolved         bool
	canonicalHash    string
	committed        bool // height has committed (we've seen NewBlock)
	advanced         bool // round has advanced (we've seen round+1 or height+1)
}

type groupData struct {
	hash       string
	votingPower int64
	validators []string
	monikers   []string
}

// Tracker is the divergence detector.
type Tracker struct {
	mu      sync.RWMutex
	cfg     Config
	rounds  map[roundKey]*roundData
	history []RoundReport
	// Seed validator set used until a round-specific one is provided.
	defaultValidators map[string]ValidatorPower
	defaultTotalPower int64
	// committedHeight is the highest height we've seen ResolveCommit for. Votes
	// for height ≤ committedHeight are stragglers and must not create fresh
	// (unresolvable) entries in t.rounds.
	committedHeight int64
}

// New constructs a tracker.
func New(cfg Config) *Tracker {
	if cfg.HistorySize <= 0 {
		cfg.HistorySize = 32
	}
	return &Tracker{
		cfg:    cfg,
		rounds: make(map[roundKey]*roundData),
	}
}

// SetValidators updates the default validator set used to weight subsequent
// votes. Address keys are uppercase hex.
func (t *Tracker) SetValidators(vs []ValidatorPower) {
	m := make(map[string]ValidatorPower, len(vs))
	var total int64
	for _, v := range vs {
		m[v.Address] = v
		total += v.Power
	}
	t.mu.Lock()
	t.defaultValidators = m
	t.defaultTotalPower = total
	// Apply to any existing round that lacked a validator set.
	for _, rd := range t.rounds {
		if rd.totalVotingPower == 0 {
			rd.validators = m
			rd.totalVotingPower = total
		}
	}
	t.mu.Unlock()
}

// IngestVote folds one VoteEvent into the tracker. Returns the current
// RoundReport for the (height, round, type) and a bool indicating whether the
// caller should consider it a fresh divergence trigger.
func (t *Tracker) IngestVote(v VoteEvent) (RoundReport, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Reject stragglers from heights that have already committed. A late vote
	// arriving after ResolveCommit would otherwise create a fresh unresolved
	// round that nothing will close, leaving stale entries in the live view.
	if v.Height <= t.committedHeight {
		return RoundReport{}, false
	}

	key := roundKey{Height: v.Height, Round: v.Round, Type: v.Type}
	rd, ok := t.rounds[key]
	if !ok {
		rd = &roundData{
			key:              key,
			groups:           make(map[string]*groupData),
			seen:             make(map[string]struct{}),
			validators:       t.defaultValidators,
			totalVotingPower: t.defaultTotalPower,
		}
		t.rounds[key] = rd
	}

	if _, dup := rd.seen[v.ValidatorAddr]; dup {
		return t.report(rd), false
	}
	rd.seen[v.ValidatorAddr] = struct{}{}

	g, ok := rd.groups[v.BlockIDHash]
	if !ok {
		g = &groupData{hash: v.BlockIDHash}
		rd.groups[v.BlockIDHash] = g
	}
	power := int64(0)
	moniker := ""
	if vp, ok := rd.validators[v.ValidatorAddr]; ok {
		power = vp.Power
		moniker = vp.Moniker
	}
	g.votingPower += power
	g.validators = append(g.validators, v.ValidatorAddr)
	if moniker != "" && len(g.monikers) < 5 {
		g.monikers = append(g.monikers, moniker)
	}
	rd.totalVotedPower += power

	rep := t.report(rd)

	trigger := false
	if v.Type == Precommit || t.cfg.IncludePrevotes {
		// Don't fire while still gathering — wait for round to settle (advanced
		// or committed) OR the precommit set has crossed 2/3.
		if rd.advanced || rd.committed || (v.Type == Precommit && rd.totalVotedPower*3 > rd.totalVotingPower*2) {
			if rep.IsDivergent {
				trigger = true
			}
		}
	}
	return rep, trigger
}

// MarkRoundAdvanced records that the consensus has moved past this round (we
// saw round+1 or height+1). Late votes that arrive afterward remain valid for
// post-mortem grouping but no longer trigger fresh detection.
func (t *Tracker) MarkRoundAdvanced(height, round int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, vt := range []VoteType{Prevote, Precommit} {
		if rd, ok := t.rounds[roundKey{Height: height, Round: round, Type: vt}]; ok {
			rd.advanced = true
		}
	}
}

// ResolveCommit declares the canonical BlockID for height h and emits resolved
// reports for every round at that height. It also silently closes any rounds
// left unresolved at older heights — these come from missed NewBlock events
// (e.g. brief WS gap) and would otherwise linger in the live view forever.
// Older heights beyond HistorySize are evicted.
func (t *Tracker) ResolveCommit(height int64, canonicalBlockIDHash string) []RoundReport {
	t.mu.Lock()
	defer t.mu.Unlock()

	if height > t.committedHeight {
		t.committedHeight = height
	}

	resolved := []RoundReport{}
	for k, rd := range t.rounds {
		switch {
		case k.Height == height:
			rd.committed = true
			rd.resolved = true
			rd.canonicalHash = canonicalBlockIDHash
			rep := t.report(rd)
			resolved = append(resolved, rep)
			t.history = append(t.history, rep)
		case k.Height < height && !rd.resolved:
			// Catch up: this height committed without us seeing its NewBlock.
			// Mark resolved so it leaves the live view; canonical hash unknown.
			rd.committed = true
			rd.resolved = true
		}
	}
	// Trim history.
	if len(t.history) > t.cfg.HistorySize*4 {
		t.history = t.history[len(t.history)-t.cfg.HistorySize*4:]
	}
	// Evict round data for heights below cutoff.
	cutoff := height - int64(t.cfg.HistorySize)
	for k := range t.rounds {
		if k.Height < cutoff {
			delete(t.rounds, k)
		}
	}
	return resolved
}

// CurrentReport returns Report{ live + history } for consumers.
func (t *Tracker) CurrentReport() Report {
	t.mu.RLock()
	defer t.mu.RUnlock()
	live := make([]RoundReport, 0, len(t.rounds))
	for _, rd := range t.rounds {
		if !rd.resolved {
			live = append(live, t.report(rd))
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].Height != live[j].Height {
			return live[i].Height < live[j].Height
		}
		if live[i].Round != live[j].Round {
			return live[i].Round < live[j].Round
		}
		return live[i].Type < live[j].Type
	})
	hist := append([]RoundReport(nil), t.history...)
	// Keep most-recent N divergent rounds.
	divergent := []RoundReport{}
	for _, r := range hist {
		if r.IsDivergent {
			divergent = append(divergent, r)
		}
	}
	if len(divergent) > t.cfg.HistorySize {
		divergent = divergent[len(divergent)-t.cfg.HistorySize:]
	}
	return Report{Live: live, History: divergent}
}

// report builds a RoundReport from the round data; assumes caller holds the
// appropriate lock.
func (t *Tracker) report(rd *roundData) RoundReport {
	groups := make([]Group, 0, len(rd.groups))
	for _, g := range rd.groups {
		var pct float64
		if rd.totalVotingPower > 0 {
			pct = 100 * float64(g.votingPower) / float64(rd.totalVotingPower)
		}
		groups = append(groups, Group{
			BlockIDHash:    g.hash,
			VotingPower:    g.votingPower,
			VotingPowerPct: pct,
			ValidatorCount: len(g.validators),
			SampleMonikers: append([]string(nil), g.monikers...),
			Validators:     append([]string(nil), g.validators...),
			IsCanonical:    rd.resolved && g.hash == rd.canonicalHash,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].VotingPower > groups[j].VotingPower
	})

	// Divergence: any non-leader group above threshold (excluding the absent /
	// nil group as the leader if everyone is nil — that's a different problem).
	divergent := false
	if len(groups) > 1 {
		// Skip the leader (index 0); check each other group's pct.
		for i := 1; i < len(groups); i++ {
			if groups[i].BlockIDHash == "" {
				// nil-vote group is informational, not a "divergence" signal
				continue
			}
			if groups[i].VotingPowerPct >= t.cfg.ThresholdPct {
				divergent = true
				break
			}
		}
	}

	return RoundReport{
		Height:           rd.key.Height,
		Round:            rd.key.Round,
		Type:             rd.key.Type,
		Groups:           groups,
		TotalVotingPower: rd.totalVotingPower,
		TotalVotedPower:  rd.totalVotedPower,
		Resolved:         rd.resolved,
		CanonicalHash:    rd.canonicalHash,
		IsDivergent:      divergent,
	}
}
