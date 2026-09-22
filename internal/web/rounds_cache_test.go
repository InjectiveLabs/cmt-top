package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
)

func seedRoundCache(tr *divergence.Tracker, height int64, rounds int) {
	tr.SetValidators([]divergence.ValidatorPower{{Address: "A", Power: 70}, {Address: "B", Power: 30}})
	for r := 0; r < rounds; r++ {
		tr.ObserveRound(height, int64(r), "A")
		for _, kind := range []divergence.VoteType{divergence.Prevote, divergence.Precommit} {
			tr.ObserveVote(divergence.VoteEvent{Height: height, Round: int64(r), Type: kind, ValidatorAddr: "A", BlockIDHash: "a"})
		}
	}
}
func reportRequest(s *Server, path, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}
func decodeCompact(t *testing.T, rr *httptest.ResponseRecorder) compactInvestigation {
	t.Helper()
	if rr.Code != 200 {
		t.Fatalf("compact status %d: %s", rr.Code, rr.Body.String())
	}
	var out compactInvestigation
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestRoundCacheSharesBuildAndInvalidatesEvidenceOnlyWhenNeeded(t *testing.T) {
	tr := divergence.New(divergence.DefaultConfig())
	seedRoundCache(tr, 10, 32)
	var builds atomic.Int64
	c := newRoundsCache(tr, func(event string, _ float64) {
		if event == "build" {
			builds.Add(1)
		}
	})
	var wg sync.WaitGroup
	errs := make(chan error, 150)
	for i := 0; i < 150; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, _, err := c.get(context.Background(), 10, true)
			if err == nil && len(e.report.Rounds) != 32 {
				err = fmt.Errorf("wrong report rounds")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if builds.Load() != 1 {
		t.Fatalf("150 readers caused %d builds", builds.Load())
	}
	e, v, _ := c.get(context.Background(), 10, true)
	tr.ObserveRound(11, 0, "")
	e2, v2, _ := c.get(context.Background(), 10, true)
	if e2 != e || builds.Load() != 1 || v2.Catalogue == v.Catalogue || withCatalogue(e2, v2).Status != "passed" {
		t.Fatal("catalogue-only change rebuilt evidence or missed status")
	}
	tr.ObserveVote(divergence.VoteEvent{Height: 10, Round: 31, Type: divergence.Precommit, ValidatorAddr: "B", BlockIDHash: "b"})
	cached, _, _ := c.get(context.Background(), 10, true)
	if cached != e {
		t.Fatal("vote burst was not coalesced")
	}
	// A legacy full read and explicit capture must never depend on stale coalescing.
	fresh, _, _ := c.get(context.Background(), 10, false)
	if fresh.evidence == e.evidence {
		t.Fatal("fresh read returned coalesced evidence")
	}
}
func TestCompactAPISelectionConditionalReadsAndCapture(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	seedRoundCache(srv.opts.Tracker, 10, 3)
	path := "/api/blocks/10/rounds?view=compact&round=latest&compare=0"
	rr := reportRequest(srv, path, "")
	out := decodeCompact(t, rr)
	if out.SchemaVersion != 1 || out.ServerEpoch != srv.hub.epoch || len(out.Rounds) != 3 || len(out.Details) != 2 || out.SelectedRound == nil || *out.SelectedRound != 2 || out.ComparisonRound == nil || *out.ComparisonRound != 0 {
		t.Fatalf("compact selection mismatch: %+v", out)
	}
	if strings.Contains(rr.Body.String(), `"context"`) || strings.Contains(rr.Body.String(), `"generatedAt"`) {
		t.Fatal("volatile context invalidates compact representation")
	}
	etag := rr.Header().Get("ETag")
	notModified := reportRequest(srv, path, etag)
	if etag == "" || notModified.Code != 304 || notModified.Body.Len() != 0 {
		t.Fatal("conditional request did not avoid body")
	}
	pinned := decodeCompact(t, reportRequest(srv, "/api/blocks/10/rounds?view=compact&round=1&compare=1", ""))
	if len(pinned.Details) != 1 {
		t.Fatal("same selected/comparison round duplicated detail")
	}
	missing := decodeCompact(t, reportRequest(srv, "/api/blocks/10/rounds?view=compact&round=99", ""))
	if missing.SelectedRound != nil || missing.SelectionStatus != "not_observed" {
		t.Fatal("missing selection silently replaced")
	}
	srv.opts.Tracker.ObserveRound(11, 0, "")
	updated := reportRequest(srv, path, etag)
	if updated.Code != 200 || updated.Header().Get("ETag") == etag || decodeCompact(t, updated).Status != "passed" {
		t.Fatal("catalogue-only status change did not invalidate ETag")
	}
	srv.opts.Tracker.ObserveVote(divergence.VoteEvent{Height: 10, Round: 2, Type: divergence.Precommit, ValidatorAddr: "B", BlockIDHash: "late"})
	capture := reportRequest(srv, "/api/blocks/10/rounds?view=full&capture=1", "")
	var full struct {
		divergence.BlockInvestigation
		ServerEpoch string    `json:"serverEpoch"`
		Revision    string    `json:"revision"`
		CapturedAt  time.Time `json:"capturedAt"`
	}
	if capture.Code != 200 || json.Unmarshal(capture.Body.Bytes(), &full) != nil || full.Revision == "" || full.CapturedAt.IsZero() || full.ServerEpoch != srv.hub.epoch || !full.Rounds[2].Validators[1].Precommit.Observed {
		t.Fatalf("capture was not fresh/complete: %s", capture.Body.String())
	}
}
func TestCompactAPIBoundsAndEviction(t *testing.T) {
	tr := divergence.New(divergence.Config{HistorySize: 2})
	srv := New(Options{Tracker: tr})
	// Use normal state/bus options from the established fixture.
	base, _ := newWSTestServer(t, "")
	srv.opts.State = base.opts.State
	srv.opts.Bus = base.opts.Bus
	seedRoundCache(tr, 10, 1)
	path := "/api/blocks/10/rounds?view=compact"
	first := reportRequest(srv, path, "")
	_ = decodeCompact(t, first)
	tr.ObserveRound(11, 0, "")
	tr.ObserveRound(12, 0, "")
	gone := decodeCompact(t, reportRequest(srv, path, first.Header().Get("ETag")))
	if gone.Found || gone.Status != "evicted" || len(gone.Details) != 0 || gone.SelectionStatus != "evicted" {
		t.Fatal("cached retained evidence survived eviction")
	}
	for _, q := range []string{"view=compact&round=", "view=compact&round=-1", "view=compact&round=wat", "view=compact&compare=", "view=compact&round=01", "view=compact&round=1&round=2", "view=bad", "view=compact&capture=1"} {
		rr := reportRequest(srv, "/api/blocks/12/rounds?"+q, "")
		if rr.Code != 400 {
			t.Fatalf("invalid %s status=%d", q, rr.Code)
		}
	}
	for i := 0; i < cap(srv.rounds.captures); i++ {
		srv.rounds.captures <- struct{}{}
	}
	busy := reportRequest(srv, "/api/blocks/12/rounds?view=full&capture=1", "")
	if busy.Code != 503 || busy.Header().Get("Retry-After") == "" {
		t.Fatal("capture work was not bounded")
	}
	// A saturated capture lane must leave interactive reads available.
	if rr := reportRequest(srv, "/api/blocks/12/rounds?view=compact", ""); rr.Code != 200 {
		t.Fatal("capture saturation blocked interactive read")
	}
	for len(srv.rounds.captures) > 0 {
		<-srv.rounds.captures
	}
}
func TestRoundCacheVariantAndUnknownHeightBounds(t *testing.T) {
	tr := divergence.New(divergence.DefaultConfig())
	seedRoundCache(tr, 10, 32)
	cache := newRoundsCache(tr, nil)
	e, v, err := cache.get(context.Background(), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		_, err = cache.compact(context.Background(), e, v, "epoch", roundSelection{round: int64(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(e.variants) > roundsCacheVariants || cache.bytes > roundsCacheBytes {
		t.Fatal("variant budget exceeded")
	}
	for i := int64(100); i < 200; i++ {
		_, _, err = cache.get(context.Background(), i, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 1 {
		t.Fatal("unknown-height probes grew cache")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = cache.get(ctx, 10, true); err == nil {
		t.Fatal("cancelled reader was not released")
	}
}
