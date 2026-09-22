package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func TestRoundInvestigationRequiresAuthentication(t *testing.T) {
	srv, _ := newWSTestServer(t, "rounds-secret")
	handler := srv.Handler()
	for _, path := range []string{"/api/blocks/1001/rounds", "/api/blocks/invalid/rounds"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/blocks/1001/rounds", nil)
	r.Header.Set("Authorization", "Bearer rounds-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("authenticated investigation returned %d %s: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}

func TestRoundInvestigationRejectsInvalidOrUnsafeHeights(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	handler := srv.Handler()
	for _, height := range []string{"0", "01", "-1", "1.5", "1e3", "+1", "not-a-height", "%20", "9007199254740992", "184467440737095516160"} {
		t.Run(height, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/blocks/"+height+"/rounds", nil))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("invalid height returned %d %s: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
		})
	}
	for _, height := range []string{"1", "9007199254740991"} {
		payload := requestJSON(t, handler, "/api/blocks/"+height+"/rounds")
		if payload["found"] != false {
			t.Fatalf("unobserved valid height %s did not report unavailable: %#v", height, payload)
		}
	}
}

func TestRoundInvestigationMissingServicesAndUnknownAPIPathsReturnJSON(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	handler := srv.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/blocks/1001/not-an-endpoint", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API path was mistaken for a SPA route: %d %s", w.Code, w.Body.String())
	}
	srv.opts.Tracker = nil
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/blocks/1001/rounds", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("missing archive did not report service unavailable: %d %s", w.Code, w.Body.String())
	}
}

func TestUnavailableRoundInvestigationCarriesObservationContext(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	srv.opts.State.Mutate(func(s *state.StateData) {
		s.Height, s.Round, s.LastCommittedHeight = 1001, 3, 1000
		s.NodeStatus = &state.NodeStatus{Network: "fixture-1"}
		s.Upgrade = &state.Upgrade{Name: "upgrade-at-stalled-height", Height: 1001}
	})
	payload := requestJSON(t, srv.Handler(), "/api/blocks/999/rounds")
	if payload["found"] != false {
		t.Fatalf("unobserved height was presented as observed: %#v", payload)
	}
	context, ok := payload["context"].(map[string]any)
	if !ok || context["chainId"] != "fixture-1" || context["activeHeight"] != float64(1001) || context["activeRound"] != float64(3) || context["committedHeight"] != float64(1000) {
		t.Fatalf("missing navigation/chain context: %#v", payload["context"])
	}
	if context["source"] != "Observed votes from the configured RPC; not a complete network record." {
		t.Fatalf("missing observation limitation: %#v", context["source"])
	}
	if _, err := time.Parse(time.RFC3339Nano, context["generatedAt"].(string)); err != nil {
		t.Fatalf("invalid generatedAt timestamp: %v", err)
	}
	if context["upgrade"].(map[string]any)["height"] != float64(1001) {
		t.Fatalf("upgrade investigation context missing: %#v", context["upgrade"])
	}
}

func TestDeepRoundInvestigationRouteServesSPAShell(t *testing.T) {
	if _, err := fs.ReadFile(SPA(), "index.html"); err != nil {
		t.Skip("SPA routing requires the bundled webui build; covered by tagged CI pass")
	}
	srv, _ := newWSTestServer(t, "protected")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/blocks/1001/rounds", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), `id="app"`) {
		t.Fatalf("deep link did not load the public login-capable SPA shell: %d %s", w.Code, w.Body.String())
	}
}

func readInvestigation(t *testing.T, srv *Server, path string) divergence.BlockInvestigation {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("live investigation response can be cached")
	}
	var report divergence.BlockInvestigation
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestInvestigationAPIRetainsPhaseChangesConflictsAndLateEvidence(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	tracker := srv.opts.Tracker
	tracker.SetValidators([]divergence.ValidatorPower{{Address: "AA", Power: 70, Moniker: "Heavy"}, {Address: "BB", Power: 20, Moniker: "Middle"}, {Address: "CC", Power: 10, Moniker: "Small"}})
	tracker.ObserveRound(120, 0, "AA")
	observe := func(address string, phase divergence.VoteType, hash string) {
		tracker.IngestVote(divergence.VoteEvent{Height: 120, Round: 0, Type: phase, ValidatorAddr: address, BlockIDHash: hash})
	}
	observe("AA", divergence.Prevote, "a")
	observe("BB", divergence.Prevote, "b")
	observe("CC", divergence.Prevote, "")
	observe("AA", divergence.Precommit, "b")
	observe("BB", divergence.Precommit, "a")
	tracker.ObserveRound(120, 1, "BB") // A visible round without any received votes.
	tracker.MarkRoundAdvanced(120, 0)
	observe("AA", divergence.Prevote, "a")   // Duplicate must not add another membership.
	observe("BB", divergence.Precommit, "c") // Explicit same-phase conflict.
	tracker.ResolveCommit(120, "b")
	observe("CC", divergence.Precommit, "") // Delayed evidence in an existing retained round.
	report := readInvestigation(t, srv, "/api/blocks/120/rounds")
	if !report.Found || report.Status != "committed" || !report.Committed || report.CanonicalHash != "b" || len(report.Rounds) != 2 {
		t.Fatalf("retained height lifecycle/round coverage lost: %+v", report)
	}
	first, empty := report.Rounds[0], report.Rounds[1]
	if first.Round != 0 || first.TotalVotingPower != "100" || first.Prevotes.ObservedVotingPower != "100" || first.Precommits.ObservedVotingPower != "100" {
		t.Fatalf("weighted phase totals count conflicts/duplicates twice: %+v", first)
	}
	if empty.Round != 1 || empty.Prevotes.ObservedValidatorCount != 0 || empty.Precommits.NotObservedVotingPower != "100" || len(empty.Validators) != 3 {
		t.Fatalf("round with no visible votes disappeared or invented participation: %+v", empty)
	}
	byAddress := map[string]divergence.InvestigationValidator{}
	for _, validator := range first.Validators {
		byAddress[validator.Address] = validator
	}
	if got := byAddress["AA"]; got.Prevote.Hashes[0].Hash != "a" || got.Precommit.Hashes[0].Hash != "b" || got.Prevote.Conflicting || got.Precommit.Conflicting {
		t.Fatalf("ordinary prevote/precommit cohort switch misclassified: %+v", got)
	}
	if got := byAddress["BB"].Precommit; !got.Conflicting || len(got.Hashes) != 2 {
		t.Fatalf("conflicting same-phase evidence was discarded: %+v", got)
	}
	if got := byAddress["CC"].Precommit; !got.Observed || len(got.Hashes) != 1 || got.Hashes[0].Hash != "" {
		t.Fatalf("late nil vote was confused with missing observation: %+v", got)
	}
	if got := tracker.CurrentReport(); len(got.Live) != 0 {
		t.Fatalf("late archived evidence reopened a committed live round: %+v", got.Live)
	}
}

func TestInvestigationAPIDistinguishesEvictedAndUnobservedHeights(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	srv.opts.Tracker = divergence.New(divergence.Config{HistorySize: 2, ThresholdPct: 5})
	srv.rounds = newRoundsCache(srv.opts.Tracker, srv.opts.Capacity.ObserveReport)
	for _, height := range []int64{10, 11, 12} {
		srv.opts.Tracker.ObserveRound(height, 0, "")
	}
	evicted := readInvestigation(t, srv, "/api/blocks/10/rounds")
	if evicted.Found || evicted.Status != "evicted" || !evicted.Truncated || len(evicted.Rounds) != 0 {
		t.Fatalf("eviction was not explicit: %+v", evicted)
	}
	if evicted.Retention.HeightLimit != 2 || len(evicted.Retention.RetainedHeights) != 2 || evicted.Retention.EarliestHeight != 11 || evicted.Retention.LatestHeight != 12 {
		t.Fatalf("retention navigation does not describe actual retained data: %+v", evicted.Retention)
	}
	future := readInvestigation(t, srv, "/api/blocks/13/rounds")
	if future.Found || future.Status != "not_observed" || future.Truncated {
		t.Fatalf("unobserved height confused with eviction: %+v", future)
	}
	retained := readInvestigation(t, srv, "/api/blocks/11/rounds")
	if !retained.Found || retained.Status != "passed" || retained.Committed || retained.CanonicalHash != "" {
		t.Fatalf("passing a height invented commit evidence: %+v", retained)
	}
}

func TestInvestigationAPIConcurrentCollectionAndRetention(t *testing.T) {
	srv, _ := newWSTestServer(t, "")
	tracker := srv.opts.Tracker
	tracker.SetValidators([]divergence.ValidatorPower{{Address: "AA", Power: 70}, {Address: "BB", Power: 30}})
	tracker.ObserveRound(1001, 0, "AA")
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		for round := int64(0); round < divergence.InvestigationRoundLimit+20; round++ {
			tracker.ObserveRound(1001, round, "AA")
			tracker.IngestVote(divergence.VoteEvent{Height: 1001, Round: round, Type: divergence.Prevote, ValidatorAddr: "AA", BlockIDHash: "a"})
			tracker.IngestVote(divergence.VoteEvent{Height: 1001, Round: round, Type: divergence.Precommit, ValidatorAddr: "BB", BlockIDHash: "b"})
		}
	}()
	go func() {
		defer writers.Done()
		for i := 0; i < 40; i++ {
			tracker.SetValidators([]divergence.ValidatorPower{{Address: "BB", Power: 30}, {Address: "AA", Power: 70}})
			srv.opts.State.Mutate(func(s *state.StateData) { s.Height, s.Round = 1001, int64(i) })
		}
	}()
	for i := 0; i < 40; i++ {
		report := readInvestigation(t, srv, "/api/blocks/1001/rounds")
		if !report.Found || len(report.Rounds) > divergence.InvestigationRoundLimit {
			t.Fatalf("concurrent response lost the height or exceeded retention: %+v", report.Coverage)
		}
		for j, round := range report.Rounds {
			if round.TotalVotingPower != "100" || len(round.Validators) != 2 || j > 0 && report.Rounds[j-1].Round >= round.Round {
				t.Fatalf("concurrent response contains an inconsistent roster or order: %+v", round)
			}
		}
	}
	writers.Wait()
	final := readInvestigation(t, srv, "/api/blocks/1001/rounds")
	if len(final.Rounds) != divergence.InvestigationRoundLimit || final.Rounds[0].Round != 20 || !final.Truncated {
		t.Fatalf("concurrent collection did not enforce round retention: %+v", final.Coverage)
	}
}
