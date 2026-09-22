package divergence

import (
	"sort"
	"time"
)

// ArchiveVersion is cheap metadata, independent of expensive evidence grouping.
// Catalogue changes must not invalidate settled heights' materialized evidence.
type ArchiveVersion struct {
	Evidence  uint64
	Catalogue uint64
	Header    BlockInvestigation
}

func (t *Tracker) InvestigationVersion(height int64) ArchiveVersion {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.investigationVersionLocked(height)
}
func (t *Tracker) investigationVersionLocked(height int64) ArchiveVersion {
	v := ArchiveVersion{Catalogue: t.catalogueRevision, Header: t.investigationHeaderLocked(height)}
	if b := t.archive[height]; b != nil {
		v.Evidence = b.revision
	}
	return v
}

// InvestigationCapture owns detached raw evidence from exactly one lock epoch.
// Build may take time, but no longer blocks incoming votes with that work.
type InvestigationCapture struct {
	Version      ArchiveVersion
	CapturedAt   time.Time
	LockDuration time.Duration
	block        *archivedHeight
}

func (t *Tracker) CaptureInvestigation(height int64) InvestigationCapture {
	t.mu.RLock()
	started := time.Now()
	c := InvestigationCapture{Version: t.investigationVersionLocked(height), CapturedAt: time.Now().UTC()}
	if b := t.archive[height]; b != nil {
		copy := *b
		copy.rounds = make(map[int64]*archivedRound, len(b.rounds))
		for number, rd := range b.rounds {
			r := *rd
			r.roster = make(map[string]ValidatorPower, len(rd.roster))
			for addr, v := range rd.roster {
				r.roster[addr] = v
			}
			r.votes = make(map[string]map[VoteType]*InvestigationVote, len(rd.votes))
			for addr, phases := range rd.votes {
				pv := make(map[VoteType]*InvestigationVote, len(phases))
				for kind, v := range phases {
					vc := cloneInvestigationVote(v)
					pv[kind] = &vc
				}
				r.votes[addr] = pv
			}
			copy.rounds[number] = &r
		}
		c.block = &copy
	}
	c.LockDuration = time.Since(started)
	t.mu.RUnlock()
	return c
}
func (c InvestigationCapture) Build() BlockInvestigation {
	out := c.Version.Header
	out.Rounds = []InvestigationRound{}
	out.Retention.RetainedHeights = append([]int64{}, out.Retention.RetainedHeights...)
	if c.block == nil {
		return out
	}
	for _, rd := range c.block.rounds {
		round := snapshotArchiveRound(rd, c.block.canonicalHash, c.block.committed)
		out.Rounds = append(out.Rounds, round)
		out.Truncated = out.Truncated || round.Truncated
		out.Coverage.ValidatorRosterComplete = out.Coverage.ValidatorRosterComplete && round.ValidatorRosterComplete
	}
	sort.Slice(out.Rounds, func(i, j int) bool { return out.Rounds[i].Round < out.Rounds[j].Round })
	return out
}
