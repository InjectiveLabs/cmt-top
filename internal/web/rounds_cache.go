package web

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
)

const roundsCacheBytes = 64 << 20
const roundsCacheEntries = 32
const roundsCacheVariants = 8
const roundsCoalesce = 250 * time.Millisecond

var errRoundsBusy = errors.New("round report capacity is busy")

type roundsEncoded struct {
	body []byte
	etag string
}
type roundsEntry struct {
	report         divergence.BlockInvestigation
	evidence       uint64
	capturedAt     time.Time
	builtAt        time.Time
	lastUsed       time.Time // cache mutex
	full           []byte
	summaries      []roundSummary
	variants       map[string]roundsEncoded // cache mutex
	variantFlights map[string]chan struct{} // cache mutex
	size           int                      // conservative materialized+encoded byte charge; cache mutex
}
type roundsCache struct {
	tracker  *divergence.Tracker
	observe  func(string, float64)
	mu       sync.Mutex
	entries  map[int64]*roundsEntry
	flights  map[int64]chan struct{}
	bytes    int
	builders chan struct{}
	readers  chan struct{}
	captures chan struct{}
	legacy   chan struct{}
}

func newRoundsCache(tracker *divergence.Tracker, observe func(string, float64)) *roundsCache {
	return &roundsCache{tracker: tracker, observe: observe, entries: map[int64]*roundsEntry{}, flights: map[int64]chan struct{}{}, builders: make(chan struct{}, 4), readers: make(chan struct{}, 256), captures: make(chan struct{}, 2), legacy: make(chan struct{}, 8)}
}
func (c *roundsCache) record(event string, value float64) {
	if c.observe != nil {
		c.observe(event, value)
	}
}
func (c *roundsCache) admit(lane chan struct{}) bool {
	select {
	case lane <- struct{}{}:
		return true
	default:
		c.record("rejected", 1)
		return false
	}
}
func (c *roundsCache) build(ctx context.Context, height int64) (*roundsEntry, divergence.ArchiveVersion, error) {
	select {
	case c.builders <- struct{}{}:
	case <-ctx.Done():
		return nil, divergence.ArchiveVersion{}, ctx.Err()
	}
	defer func() { <-c.builders }()
	started := time.Now()
	capture := c.tracker.CaptureInvestigation(height)
	c.record("archive_lock_seconds", capture.LockDuration.Seconds())
	report := capture.Build()
	full, err := json.Marshal(report)
	if err != nil {
		return nil, capture.Version, err
	}
	e := &roundsEntry{report: report, evidence: capture.Version.Evidence, capturedAt: capture.CapturedAt, builtAt: time.Now(), lastUsed: time.Now(), full: full, summaries: summarizeRounds(report.Rounds), variants: map[string]roundsEncoded{}, variantFlights: map[string]chan struct{}{}, size: 3 * len(full)}
	c.record("build", 1)
	c.record("build_seconds", time.Since(started).Seconds())
	return e, capture.Version, nil
}
func canCoalesce(e *roundsEntry, v divergence.ArchiveVersion) bool {
	// Votes can be briefly stale with their original revision. A retention/commit
	// boundary must never show a now-evicted round or mixed canonical evidence.
	return v.Header.Found && e.report.Committed == v.Header.Committed && e.report.CanonicalHash == v.Header.CanonicalHash && e.report.Coverage.RoundsEvicted == v.Header.Coverage.RoundsEvicted && time.Since(e.builtAt) < roundsCoalesce
}
func (c *roundsCache) get(ctx context.Context, height int64, allowCoalesce bool) (*roundsEntry, divergence.ArchiveVersion, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, divergence.ArchiveVersion{}, err
		}
		v := c.tracker.InvestigationVersion(height)
		if !v.Header.Found {
			// Unknown-height probes must not populate an unbounded cache.
			c.mu.Lock()
			if old := c.entries[height]; old != nil {
				c.removeLocked(height)
			}
			c.mu.Unlock()
			full, _ := json.Marshal(v.Header)
			return &roundsEntry{report: v.Header, evidence: v.Evidence, full: full, summaries: []roundSummary{}, variants: map[string]roundsEncoded{}, variantFlights: map[string]chan struct{}{}}, v, nil
		}
		c.mu.Lock()
		if e := c.entries[height]; e != nil && (e.evidence == v.Evidence || allowCoalesce && canCoalesce(e, v)) {
			e.lastUsed = time.Now()
			c.mu.Unlock()
			c.record("hit", 1)
			if e.evidence != v.Evidence {
				c.record("coalesced", 1)
			}
			return e, v, nil
		}
		if done := c.flights[height]; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, v, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.flights[height] = done
		c.mu.Unlock()
		c.record("miss", 1)
		e, captured, err := c.build(ctx, height)
		// Copy current retention outside the cache lock; cache eviction never calls
		// into the tracker. Publication always retains the captured evidence stamp.
		latest := c.tracker.InvestigationVersion(height)
		c.mu.Lock()
		if err == nil && latest.Header.Found && e.report.Found && e.size <= roundsCacheBytes {
			if c.entries[height] != nil {
				c.removeLocked(height)
			}
			c.entries[height] = e
			c.bytes += e.size
			c.trimLocked()
			c.record("cache_bytes", float64(c.bytes))
		}
		delete(c.flights, height)
		close(done)
		c.mu.Unlock()
		if err != nil {
			return nil, captured, err
		}
		if !latest.Header.Found || !canCoalesce(e, latest) && e.evidence != latest.Evidence {
			continue
		}
		return e, latest, nil
	}
}
func (c *roundsCache) removeLocked(height int64) {
	if e := c.entries[height]; e != nil {
		delete(c.entries, height)
		c.bytes -= e.size
		c.record("evicted", 1)
		c.record("cache_bytes", float64(c.bytes))
	}
}
func (c *roundsCache) trimLocked() {
	for c.bytes > roundsCacheBytes || len(c.entries) > roundsCacheEntries {
		var oldest int64
		var at time.Time
		for height, e := range c.entries {
			if at.IsZero() || e.lastUsed.Before(at) {
				oldest, at = height, e.lastUsed
			}
		}
		c.removeLocked(oldest)
	}
}

// withCatalogue updates only globally-derived fields, leaving evidence fields
// and their captured revision together. Callers treat the shared report as immutable.
func withCatalogue(e *roundsEntry, v divergence.ArchiveVersion) divergence.BlockInvestigation {
	r := e.report
	r.Retention = v.Header.Retention
	r.Status = v.Header.Status
	return r
}
