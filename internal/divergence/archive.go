package divergence

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"
)

const (
	// InvestigationRoundLimit bounds memory even when a height cannot commit.
	InvestigationRoundLimit            = 128
	InvestigationHashLimit             = 4
	InvestigationUnknownValidatorLimit = 64
	investigationIdentityLimit         = 128
)

// BlockInvestigation is a retained, observation-only view of one consensus
// height. It cannot identify a binary version or establish nondeterminism.
type BlockInvestigation struct {
	Height        int64                  `json:"height"`
	Found         bool                   `json:"found"`
	Status        string                 `json:"status"`
	Committed     bool                   `json:"committed"`
	CanonicalHash string                 `json:"canonicalHash"`
	FirstSeenAt   *time.Time             `json:"firstSeenAt,omitempty"`
	LastSeenAt    *time.Time             `json:"lastSeenAt,omitempty"`
	Rounds        []InvestigationRound   `json:"rounds"`
	Truncated     bool                   `json:"truncated"`
	Coverage      InvestigationCoverage  `json:"coverage"`
	Retention     InvestigationRetention `json:"retention"`
}

type InvestigationCoverage struct {
	FirstObservedRound      *int64 `json:"firstObservedRound,omitempty"`
	LastObservedRound       *int64 `json:"lastObservedRound,omitempty"`
	RoundsObserved          int    `json:"roundsObserved"`
	MissingRounds           int64  `json:"missingRounds"`
	RoundsEvicted           int    `json:"roundsEvicted"`
	ValidatorRosterComplete bool   `json:"validatorRosterComplete"`
}

type InvestigationRetention struct {
	HeightLimit             int     `json:"heightLimit"`
	RoundLimit              int     `json:"roundLimit"`
	HashesPerValidatorLimit int     `json:"hashesPerValidatorLimit"`
	UnknownValidatorLimit   int     `json:"unknownValidatorLimit"`
	EarliestHeight          int64   `json:"earliestHeight"`
	LatestHeight            int64   `json:"latestHeight"`
	RetainedHeights         []int64 `json:"retainedHeights"`
}

type InvestigationRound struct {
	Round                   int64                    `json:"round"`
	Proposer                string                   `json:"proposer"`
	FirstSeenAt             time.Time                `json:"firstSeenAt"`
	LastSeenAt              time.Time                `json:"lastSeenAt"`
	ValidatorRosterComplete bool                     `json:"validatorRosterComplete"`
	TotalVotingPower        string                   `json:"totalVotingPower"`
	Validators              []InvestigationValidator `json:"validators"`
	Prevotes                InvestigationPhase       `json:"prevotes"`
	Precommits              InvestigationPhase       `json:"precommits"`
	Truncated               bool                     `json:"truncated"`
}

type InvestigationValidator struct {
	Address       string            `json:"address"`
	Moniker       string            `json:"moniker"`
	VotingPower   string            `json:"votingPower"`
	KnownToRoster bool              `json:"knownToRoster"`
	Prevote       InvestigationVote `json:"prevote"`
	Precommit     InvestigationVote `json:"precommit"`
}

type InvestigationVote struct {
	Observed    bool                `json:"observed"`
	Hashes      []InvestigationHash `json:"hashes"`
	Conflicting bool                `json:"conflicting"`
	Truncated   bool                `json:"truncated"`
}

type InvestigationHash struct {
	Hash          string     `json:"hash"` // empty string is an observed nil vote
	FirstSeenAt   time.Time  `json:"firstSeenAt"`
	LastSeenAt    time.Time  `json:"lastSeenAt"`
	VoteTimestamp *time.Time `json:"voteTimestamp,omitempty"`
}

type InvestigationPhase struct {
	Groups                 []InvestigationGroup `json:"groups"`
	ObservedVotingPower    string               `json:"observedVotingPower"`
	ObservedValidatorCount int                  `json:"observedValidatorCount"`
	NotObservedVotingPower string               `json:"notObservedVotingPower"`
	NotObservedValidators  []string             `json:"notObservedValidators"`
	ConflictingValidators  []string             `json:"conflictingValidators"`
}

type InvestigationGroup struct {
	Hash           string   `json:"hash"`
	VotingPower    string   `json:"votingPower"`
	ValidatorCount int      `json:"validatorCount"`
	Validators     []string `json:"validators"`
	IsCanonical    bool     `json:"isCanonical"`
}

type archivedHeight struct {
	height                        int64
	rounds                        map[int64]*archivedRound
	firstSeenAt, lastSeenAt       time.Time
	committed                     bool
	canonicalHash                 string
	firstRound, lastRound         int64
	roundsObserved, roundsEvicted int
	evictedThrough                int64
}

type archivedRound struct {
	round                   int64
	proposer                string
	firstSeenAt, lastSeenAt time.Time
	roster                  map[string]ValidatorPower
	votes                   map[string]map[VoteType]*InvestigationVote
	truncated               bool
}

// ObserveRound retains rounds even when no votes were visible. Call it only for
// accepted consensus observations; repeated notifications do not age the data.
func (t *Tracker) ObserveRound(height, round int64, proposer string) {
	if height <= 0 || round < 0 || round > math.MaxInt32 || len(proposer) > investigationIdentityLimit {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now().UTC()
	block := t.archiveHeightLocked(height, now)
	if block == nil {
		return
	}
	rd := t.archiveRoundLocked(block, round, now)
	if rd == nil {
		return
	}
	if proposer != "" && rd.proposer != proposer {
		rd.proposer = proposer
		rd.lastSeenAt, block.lastSeenAt = now, now
	}
}

// ObserveVote records evidence without changing the live divergence detector.
// A late vote can extend an existing retained round after commit. Identical
// deliveries are idempotent, including server-observed timestamps.
func (t *Tracker) ObserveVote(v VoteEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observeVoteLocked(v)
}

func (t *Tracker) observeVoteLocked(v VoteEvent) {
	if v.Height <= 0 || v.Round < 0 || v.Round > math.MaxInt32 || v.ValidatorAddr == "" || len(v.ValidatorAddr) > investigationIdentityLimit || len(v.BlockIDHash) > investigationIdentityLimit || (v.Type != Prevote && v.Type != Precommit) {
		return
	}
	now := time.Now().UTC()
	block := t.archiveHeightLocked(v.Height, now)
	if block == nil {
		return
	}
	rd := t.archiveRoundLocked(block, v.Round, now)
	if rd == nil {
		return
	}
	phases, exists := rd.votes[v.ValidatorAddr]
	if !exists {
		if _, known := rd.roster[v.ValidatorAddr]; !known {
			unknown := 0
			for address := range rd.votes {
				if _, known := rd.roster[address]; !known {
					unknown++
				}
			}
			if unknown >= InvestigationUnknownValidatorLimit {
				rd.truncated = true
				return
			}
		}
		phases = make(map[VoteType]*InvestigationVote)
		rd.votes[v.ValidatorAddr] = phases
	}
	vote := phases[v.Type]
	if vote == nil {
		vote = &InvestigationVote{Observed: true, Hashes: []InvestigationHash{}}
		phases[v.Type] = vote
	}
	for _, observed := range vote.Hashes {
		if observed.Hash == v.BlockIDHash {
			return
		}
	}
	if len(vote.Hashes) >= InvestigationHashLimit {
		vote.Truncated = true
		vote.Conflicting = true
		rd.truncated = true
		return
	}
	observation := InvestigationHash{Hash: v.BlockIDHash, FirstSeenAt: now, LastSeenAt: now}
	if !v.Timestamp.IsZero() {
		signedAt := v.Timestamp
		observation.VoteTimestamp = &signedAt
	}
	vote.Hashes = append(vote.Hashes, observation)
	vote.Conflicting = len(vote.Hashes) > 1
	rd.lastSeenAt, block.lastSeenAt = now, now
}

func (t *Tracker) archiveHeightLocked(height int64, now time.Time) *archivedHeight {
	if height <= 0 || height <= t.archiveEvictedThrough {
		return nil
	}
	if block := t.archive[height]; block != nil {
		return block
	}
	// A late vote cannot create history after the height has been committed.
	// ResolveCommit creates its own entry before advancing committedHeight.
	if height <= t.committedHeight {
		return nil
	}
	return t.createArchiveHeightLocked(height, now)
}

func (t *Tracker) createArchiveHeightLocked(height int64, now time.Time) *archivedHeight {
	if height <= 0 || height <= t.archiveEvictedThrough {
		return nil
	}
	if block := t.archive[height]; block != nil {
		return block
	}
	block := &archivedHeight{height: height, rounds: make(map[int64]*archivedRound), firstSeenAt: now, lastSeenAt: now, evictedThrough: -1}
	t.archive[height] = block
	if height > t.archiveLatestHeight {
		t.archiveLatestHeight = height
	}
	if len(t.archive) > t.cfg.HistorySize {
		oldest := height
		for h := range t.archive {
			if h < oldest {
				oldest = h
			}
		}
		delete(t.archive, oldest)
		if oldest > t.archiveEvictedThrough {
			t.archiveEvictedThrough = oldest
		}
		t.pruneDetectorRoundsLocked()
	}
	return t.archive[height]
}

func (t *Tracker) archiveRoundLocked(block *archivedHeight, round int64, now time.Time) *archivedRound {
	if round < 0 || round <= block.evictedThrough {
		return nil
	}
	if rd := block.rounds[round]; rd != nil {
		return rd
	}
	if block.committed || block.height <= t.committedHeight {
		return nil
	}
	roster := make(map[string]ValidatorPower, len(t.defaultValidators))
	for address, v := range t.defaultValidators {
		roster[address] = v
	}
	rd := &archivedRound{round: round, firstSeenAt: now, lastSeenAt: now, roster: roster, votes: make(map[string]map[VoteType]*InvestigationVote)}
	block.rounds[round] = rd
	if block.roundsObserved == 0 || round < block.firstRound {
		block.firstRound = round
	}
	if block.roundsObserved == 0 || round > block.lastRound {
		block.lastRound = round
	}
	block.roundsObserved++
	block.lastSeenAt = now
	if len(block.rounds) > InvestigationRoundLimit {
		oldest := round
		for r := range block.rounds {
			if r < oldest {
				oldest = r
			}
		}
		delete(block.rounds, oldest)
		block.evictedThrough = oldest
		block.roundsEvicted++
		t.pruneDetectorRoundsLocked()
	}
	return block.rounds[round]
}

func (t *Tracker) archiveCommitLocked(height int64, hash string) {
	if len(hash) > investigationIdentityLimit {
		return
	}
	now := time.Now().UTC()
	block := t.createArchiveHeightLocked(height, now)
	if block == nil {
		return
	}
	if !block.committed || block.canonicalHash == "" && hash != "" {
		block.committed, block.canonicalHash, block.lastSeenAt = true, hash, now
	}
}

// BlockInvestigation returns an independent snapshot; callers may freely mutate
// its maps/slices. The live detector's thresholds do not filter this archive.
func (t *Tracker) BlockInvestigation(height int64) BlockInvestigation {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := BlockInvestigation{Height: height, Status: "not_observed", Rounds: []InvestigationRound{},
		Retention: InvestigationRetention{HeightLimit: t.cfg.HistorySize, RoundLimit: InvestigationRoundLimit,
			HashesPerValidatorLimit: InvestigationHashLimit, UnknownValidatorLimit: InvestigationUnknownValidatorLimit, RetainedHeights: []int64{}}}
	for h := range t.archive {
		result.Retention.RetainedHeights = append(result.Retention.RetainedHeights, h)
	}
	sort.Slice(result.Retention.RetainedHeights, func(i, j int) bool { return result.Retention.RetainedHeights[i] < result.Retention.RetainedHeights[j] })
	if len(result.Retention.RetainedHeights) > 0 {
		result.Retention.EarliestHeight = result.Retention.RetainedHeights[0]
		result.Retention.LatestHeight = result.Retention.RetainedHeights[len(result.Retention.RetainedHeights)-1]
	}
	block := t.archive[height]
	if block == nil {
		if height > 0 && height <= t.archiveEvictedThrough {
			result.Status = "evicted"
			result.Truncated = true
		}
		return result
	}
	result.Found, result.Status, result.Committed, result.CanonicalHash = true, "live", block.committed, block.canonicalHash
	if block.committed {
		result.Status = "committed"
	} else if height < t.archiveLatestHeight || height <= t.committedHeight {
		result.Status = "passed"
	}
	first, last := block.firstSeenAt, block.lastSeenAt
	result.FirstSeenAt, result.LastSeenAt = &first, &last
	result.Coverage = InvestigationCoverage{RoundsObserved: block.roundsObserved, RoundsEvicted: block.roundsEvicted, ValidatorRosterComplete: len(block.rounds) > 0}
	if block.roundsObserved > 0 {
		firstRound, lastRound := block.firstRound, block.lastRound
		result.Coverage.FirstObservedRound, result.Coverage.LastObservedRound = &firstRound, &lastRound
		// Subtract before adding to avoid overflow for malformed maximum rounds.
		result.Coverage.MissingRounds = block.lastRound - int64(block.roundsObserved) + 1
	}
	result.Truncated = block.roundsEvicted > 0
	for _, rd := range block.rounds {
		round := snapshotArchiveRound(rd, block.canonicalHash, block.committed)
		result.Rounds = append(result.Rounds, round)
		result.Truncated = result.Truncated || round.Truncated
		result.Coverage.ValidatorRosterComplete = result.Coverage.ValidatorRosterComplete && round.ValidatorRosterComplete
	}
	sort.Slice(result.Rounds, func(i, j int) bool { return result.Rounds[i].Round < result.Rounds[j].Round })
	return result
}

func snapshotArchiveRound(rd *archivedRound, canonical string, committed bool) InvestigationRound {
	result := InvestigationRound{Round: rd.round, Proposer: rd.proposer, FirstSeenAt: rd.firstSeenAt,
		LastSeenAt: rd.lastSeenAt, ValidatorRosterComplete: len(rd.roster) > 0, Validators: []InvestigationValidator{}, Truncated: rd.truncated}
	addresses := make(map[string]struct{}, len(rd.roster)+len(rd.votes))
	for address := range rd.roster {
		addresses[address] = struct{}{}
	}
	for address := range rd.votes {
		addresses[address] = struct{}{}
	}
	total := new(big.Int)
	for address := range addresses {
		v, known := rd.roster[address]
		if !known {
			result.ValidatorRosterComplete = false
		}
		row := InvestigationValidator{Address: address, Moniker: v.Moniker, VotingPower: strconv.FormatInt(v.Power, 10), KnownToRoster: known,
			Prevote: cloneInvestigationVote(rd.votes[address][Prevote]), Precommit: cloneInvestigationVote(rd.votes[address][Precommit])}
		if known && v.Power > 0 {
			total.Add(total, big.NewInt(v.Power))
		}
		result.Validators = append(result.Validators, row)
	}
	sort.Slice(result.Validators, func(i, j int) bool { return result.Validators[i].Address < result.Validators[j].Address })
	result.TotalVotingPower = total.String()
	result.Prevotes = snapshotPhase(result.Validators, Prevote, canonical, committed)
	result.Precommits = snapshotPhase(result.Validators, Precommit, canonical, committed)
	return result
}

func cloneInvestigationVote(v *InvestigationVote) InvestigationVote {
	result := InvestigationVote{Hashes: []InvestigationHash{}}
	if v == nil {
		return result
	}
	result.Observed, result.Conflicting, result.Truncated = v.Observed, v.Conflicting, v.Truncated
	for _, hash := range v.Hashes {
		copy := hash
		if hash.VoteTimestamp != nil {
			ts := *hash.VoteTimestamp
			copy.VoteTimestamp = &ts
		}
		result.Hashes = append(result.Hashes, copy)
	}
	return result
}

func snapshotPhase(validators []InvestigationValidator, kind VoteType, canonical string, committed bool) InvestigationPhase {
	result := InvestigationPhase{Groups: []InvestigationGroup{}, NotObservedValidators: []string{}, ConflictingValidators: []string{}}
	groups := make(map[string]*InvestigationGroup)
	powers := make(map[string]*big.Int)
	observed, absent := new(big.Int), new(big.Int)
	for _, v := range validators {
		vote := v.Prevote
		if kind == Precommit {
			vote = v.Precommit
		}
		power, ok := new(big.Int).SetString(v.VotingPower, 10)
		if !ok || power.Sign() < 0 {
			power = new(big.Int)
		}
		if !vote.Observed {
			absent.Add(absent, power)
			result.NotObservedValidators = append(result.NotObservedValidators, v.Address)
			continue
		}
		observed.Add(observed, power)
		result.ObservedValidatorCount++
		if vote.Conflicting {
			result.ConflictingValidators = append(result.ConflictingValidators, v.Address)
		}
		for _, h := range vote.Hashes {
			g := groups[h.Hash]
			if g == nil {
				g = &InvestigationGroup{Hash: h.Hash, Validators: []string{}, IsCanonical: committed && h.Hash != "" && canonical == h.Hash}
				groups[h.Hash], powers[h.Hash] = g, new(big.Int)
			}
			g.Validators = append(g.Validators, v.Address)
			g.ValidatorCount++
			powers[h.Hash].Add(powers[h.Hash], power)
		}
	}
	result.ObservedVotingPower, result.NotObservedVotingPower = observed.String(), absent.String()
	for hash, g := range groups {
		g.VotingPower = powers[hash].String()
		result.Groups = append(result.Groups, *g)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		iPower, jPower := powers[result.Groups[i].Hash], powers[result.Groups[j].Hash]
		if cmp := iPower.Cmp(jPower); cmp != 0 {
			return cmp > 0
		}
		return result.Groups[i].Hash < result.Groups[j].Hash
	})
	return result
}

// Keep the threshold-based live detector inside the same resource bounds as
// the investigation archive, including at a height stalled for many rounds.
func (t *Tracker) pruneDetectorRoundsLocked() {
	for key := range t.rounds {
		block := t.archive[key.Height]
		if block == nil || block.rounds[key.Round] == nil {
			delete(t.rounds, key)
		}
	}
}
