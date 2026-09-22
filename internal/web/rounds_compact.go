package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
)

type roundGroupSummary struct {
	Hash           string `json:"hash"`
	VotingPower    string `json:"votingPower"`
	ValidatorCount int    `json:"validatorCount"`
	IsCanonical    bool   `json:"isCanonical"`
}
type phaseSummary struct {
	Groups                    []roundGroupSummary `json:"groups"`
	ObservedVotingPower       string              `json:"observedVotingPower"`
	ObservedValidatorCount    int                 `json:"observedValidatorCount"`
	NotObservedVotingPower    string              `json:"notObservedVotingPower"`
	NotObservedValidatorCount int                 `json:"notObservedValidatorCount"`
	ConflictingValidatorCount int                 `json:"conflictingValidatorCount"`
}
type roundSummary struct {
	Round                   int64        `json:"round"`
	Proposer                string       `json:"proposer"`
	FirstSeenAt             time.Time    `json:"firstSeenAt"`
	LastSeenAt              time.Time    `json:"lastSeenAt"`
	ValidatorRosterComplete bool         `json:"validatorRosterComplete"`
	TotalVotingPower        string       `json:"totalVotingPower"`
	Truncated               bool         `json:"truncated"`
	Prevotes                phaseSummary `json:"prevotes"`
	Precommits              phaseSummary `json:"precommits"`
}

func summarizePhase(p divergence.InvestigationPhase) phaseSummary {
	s := phaseSummary{Groups: []roundGroupSummary{}, ObservedVotingPower: p.ObservedVotingPower, ObservedValidatorCount: p.ObservedValidatorCount, NotObservedVotingPower: p.NotObservedVotingPower, NotObservedValidatorCount: len(p.NotObservedValidators), ConflictingValidatorCount: len(p.ConflictingValidators)}
	for _, g := range p.Groups {
		s.Groups = append(s.Groups, roundGroupSummary{g.Hash, g.VotingPower, g.ValidatorCount, g.IsCanonical})
	}
	return s
}
func summarizeRounds(rounds []divergence.InvestigationRound) []roundSummary {
	out := make([]roundSummary, 0, len(rounds))
	for _, r := range rounds {
		out = append(out, roundSummary{r.Round, r.Proposer, r.FirstSeenAt, r.LastSeenAt, r.ValidatorRosterComplete, r.TotalVotingPower, r.Truncated, summarizePhase(r.Prevotes), summarizePhase(r.Precommits)})
	}
	return out
}

type roundSelection struct {
	latest     bool
	round      int64
	comparison *int64
}

func parseRoundSelection(q url.Values) (roundSelection, error) {
	result := roundSelection{latest: true}
	parse := func(raw string) (int64, error) {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > 1<<31-1 || strconv.FormatInt(n, 10) != raw {
			return 0, fmt.Errorf("round must be a nonnegative decimal integer no greater than 2147483647")
		}
		return n, nil
	}
	for _, key := range []string{"view", "round", "compare", "capture"} {
		if len(q[key]) > 1 {
			return result, fmt.Errorf("duplicate %s parameter", key)
		}
	}
	if q.Has("round") && q.Get("round") == "" {
		return result, fmt.Errorf("round must be latest or a nonnegative decimal integer")
	}
	if raw := q.Get("round"); raw != "" && raw != "latest" {
		n, err := parse(raw)
		if err != nil {
			return result, err
		}
		result.latest, result.round = false, n
	}
	if raw, present := q["compare"]; present {
		n, err := parse(raw[0])
		if err != nil {
			return result, err
		}
		result.comparison = &n
	}
	return result, nil
}

type compactInvestigation struct {
	divergence.BlockInvestigation
	SchemaVersion    int                             `json:"schemaVersion"`
	ServerEpoch      string                          `json:"serverEpoch"`
	Revision         string                          `json:"revision"`
	Rounds           []roundSummary                  `json:"rounds"`
	Details          []divergence.InvestigationRound `json:"details"`
	SelectedRound    *int64                          `json:"selectedRound"`
	ComparisonRound  *int64                          `json:"comparisonRound"`
	SelectionStatus  string                          `json:"selectionStatus"`
	ComparisonStatus string                          `json:"comparisonStatus"`
}

func compactReport(e *roundsEntry, v divergence.ArchiveVersion, epoch string, sel roundSelection) compactInvestigation {
	r := withCatalogue(e, v)
	out := compactInvestigation{BlockInvestigation: r, SchemaVersion: 1, ServerEpoch: epoch, Revision: fmt.Sprintf("%d:%d", e.evidence, v.Catalogue), Rounds: e.summaries, Details: []divergence.InvestigationRound{}, SelectionStatus: "not_observed", ComparisonStatus: "none"}
	out.BlockInvestigation.Rounds = nil // only the compact rounds field is serialized
	pick := func(n int64) (*int64, string) {
		for _, rd := range r.Rounds {
			if rd.Round == n {
				for _, d := range out.Details {
					if d.Round == n {
						return &n, "available"
					}
				}
				out.Details = append(out.Details, rd)
				return &n, "available"
			}
		}
		if r.Status == "evicted" || r.Coverage.RoundsEvicted > 0 && len(r.Rounds) > 0 && n < r.Rounds[0].Round {
			return nil, "evicted"
		}
		return nil, "not_observed"
	}
	if sel.latest {
		if len(r.Rounds) > 0 {
			out.SelectedRound, out.SelectionStatus = pick(r.Rounds[len(r.Rounds)-1].Round)
		} else if r.Status == "evicted" {
			out.SelectionStatus = "evicted"
		}
	} else {
		out.SelectedRound, out.SelectionStatus = pick(sel.round)
	}
	if sel.comparison != nil {
		out.ComparisonRound, out.ComparisonStatus = pick(*sel.comparison)
	}
	return out
}
func (c *roundsCache) compact(ctx context.Context, e *roundsEntry, v divergence.ArchiveVersion, epoch string, sel roundSelection) (roundsEncoded, error) {
	compare := "none"
	if sel.comparison != nil {
		compare = strconv.FormatInt(*sel.comparison, 10)
	}
	key := fmt.Sprintf("%s:%d:%d:%t:%d:%s", epoch, e.evidence, v.Catalogue, sel.latest, sel.round, compare)
	return c.encoded(ctx, e, key, func() ([]byte, error) { return json.Marshal(compactReport(e, v, epoch, sel)) })
}
func (c *roundsCache) fullView(ctx context.Context, e *roundsEntry, v divergence.ArchiveVersion) (roundsEncoded, error) {
	return c.encoded(ctx, e, fmt.Sprintf("full:%d:%d", e.evidence, v.Catalogue), func() ([]byte, error) { return json.Marshal(withCatalogue(e, v)) })
}
func (c *roundsCache) encoded(ctx context.Context, e *roundsEntry, key string, build func() ([]byte, error)) (roundsEncoded, error) {
	for {
		c.mu.Lock()
		if hit, ok := e.variants[key]; ok {
			c.mu.Unlock()
			return hit, nil
		}
		if done := e.variantFlights[key]; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return roundsEncoded{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		e.variantFlights[key] = done
		c.mu.Unlock()
		var result roundsEncoded
		var err error
		select {
		case c.builders <- struct{}{}:
			result.body, err = build()
			<-c.builders
			sum := sha256.Sum256(result.body)
			result.etag = `"` + hex.EncodeToString(sum[:]) + `"`
		case <-ctx.Done():
			err = ctx.Err()
		}
		c.mu.Lock()
		if err == nil && c.entries[e.report.Height] == e {
			// Keep variant cardinality finite even when callers probe arbitrary pairs.
			if len(e.variants) >= roundsCacheVariants {
				for k, b := range e.variants {
					e.size -= len(b.body)
					c.bytes -= len(b.body)
					delete(e.variants, k)
				}
			}
			e.variants[key] = result
			e.size += len(result.body)
			c.bytes += len(result.body)
			c.trimLocked()
			c.record("cache_bytes", float64(c.bytes))
		}
		delete(e.variantFlights, key)
		close(done)
		c.mu.Unlock()
		return result, err
	}
}
func matchesETag(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}
