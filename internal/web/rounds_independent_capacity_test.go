package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

func independentRoundsServer(t *testing.T) (*divergence.Tracker, http.Handler) {
	t.Helper()
	tracker := divergence.New(divergence.Config{HistorySize: 2, ThresholdPct: 5, IncludePrevotes: true})
	tracker.SetValidators([]divergence.ValidatorPower{{Address: "A", Power: 70, Moniker: "Alpha"}, {Address: "B", Power: 30, Moniker: "Beta"}})
	srv := New(Options{State: state.New(), Bus: events.NewBus(32), Tracker: tracker})
	return tracker, srv.Handler()
}
func independentReport(t *testing.T, h http.Handler, query, etag string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", query, nil)
	req.RemoteAddr = "127.0.0.1:2000"
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var report map[string]any
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatalf("invalid JSON: %v %s", err, rec.Body.String())
		}
	}
	return rec, report
}
func independentVote(tracker *divergence.Tracker, height, round int64, address, hash string) {
	tracker.ObserveVote(divergence.VoteEvent{Height: height, Round: round, Type: divergence.Prevote, ValidatorAddr: address, BlockIDHash: hash, Timestamp: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)})
}

func TestCapacityCompactRevalidationAndFreshCapture(t *testing.T) {
	tracker, h := independentRoundsServer(t)
	tracker.ObserveRound(1001, 0, "A")
	independentVote(tracker, 1001, 0, "A", "aa")
	path := "/api/blocks/1001/rounds?view=compact&round=latest"
	first, body := independentReport(t, h, path, "")
	if first.Code != 200 || first.Header().Get("ETag") == "" {
		t.Fatal("missing compact representation/etag")
	}
	for _, key := range []string{"context", "generatedAt", "capturedAt"} {
		if _, ok := body[key]; ok {
			t.Fatalf("volatile field %s in compact response", key)
		}
	}
	if len(body["details"].([]any)) != 1 || body["selectedRound"] != float64(0) {
		t.Fatal("wrong selected detail")
	}
	summary := body["rounds"].([]any)[0].(map[string]any)
	if _, ok := summary["validators"]; ok {
		t.Fatal("full validator rows leaked into round summary")
	}
	for _, group := range summary["prevotes"].(map[string]any)["groups"].([]any) {
		if _, ok := group.(map[string]any)["validators"]; ok {
			t.Fatal("member arrays leaked into summary")
		}
	}
	unchanged, _ := independentReport(t, h, path, first.Header().Get("ETag"))
	if unchanged.Code != 304 || unchanged.Body.Len() != 0 {
		t.Fatal("unchanged report did not revalidate")
	}
	independentVote(tracker, 1001, 0, "A", "aa")
	duplicate, _ := independentReport(t, h, path, first.Header().Get("ETag"))
	if duplicate.Code != 304 {
		t.Fatal("identical observation invalidated cached evidence")
	}
	// A late conflicting vote must appear immediately in a capture, even during
	// the compact coalescing window. The cached response keeps its old revision.
	independentVote(tracker, 1001, 0, "A", "bb")
	capture, full := independentReport(t, h, "/api/blocks/1001/rounds?view=full&capture=1", "")
	if capture.Code != 200 || full["capturedAt"] == nil || full["revision"] == body["revision"] {
		t.Fatal("capture did not identify fresh evidence")
	}
	round := full["rounds"].([]any)[0].(map[string]any)
	if round["prevotes"].(map[string]any)["conflictingValidators"].([]any)[0] != "A" {
		t.Fatal("capture lost conflicting observation")
	}
	time.Sleep(275 * time.Millisecond)
	changed, updated := independentReport(t, h, path, first.Header().Get("ETag"))
	if changed.Code != 200 || changed.Header().Get("ETag") == first.Header().Get("ETag") || updated["revision"] == body["revision"] {
		t.Fatal("late evidence did not invalidate after coalescing interval")
	}
	tracker.ResolveCommit(1001, "aa")
	commit, committed := independentReport(t, h, path, changed.Header().Get("ETag"))
	if commit.Code != 200 || committed["committed"] != true || committed["canonicalHash"] != "aa" {
		t.Fatal("commit boundary was coalesced/stale")
	}
}

func TestCapacityCompactSelectionAndEvictionAreExact(t *testing.T) {
	tracker, h := independentRoundsServer(t)
	for i := int64(0); i < 130; i++ {
		tracker.ObserveRound(1001, i, "A")
		independentVote(tracker, 1001, i, "A", fmt.Sprint(i))
	}
	for _, tc := range []struct {
		q        string
		selected any
		status   string
		details  int
	}{{"round=latest&compare=128", float64(129), "available", 2}, {"round=129&compare=129", float64(129), "available", 1}, {"round=0", nil, "evicted", 0}, {"round=200", nil, "not_observed", 0}} {
		rec, body := independentReport(t, h, "/api/blocks/1001/rounds?view=compact&"+tc.q, "")
		if rec.Code != 200 || body["selectedRound"] != tc.selected || body["selectionStatus"] != tc.status || len(body["details"].([]any)) != tc.details {
			t.Fatalf("selection %s returned %s", tc.q, rec.Body.String())
		}
		if len(body["rounds"].([]any)) != 128 {
			t.Fatal("retained summaries not bounded to128")
		}
	}
	path := "/api/blocks/1001/rounds?view=compact&round=latest"
	old, _ := independentReport(t, h, path, "")
	tracker.ObserveRound(1002, 0, "B")
	tracker.ObserveRound(1003, 0, "A")
	rec, body := independentReport(t, h, path, old.Header().Get("ETag"))
	if rec.Code != 200 || body["found"] != false || body["status"] != "evicted" || len(body["details"].([]any)) != 0 || len(body["rounds"].([]any)) != 0 {
		t.Fatalf("cached height survived eviction: %s", rec.Body.String())
	}
	for _, q := range []string{"round=-1", "round=1.5", "round=+1", "round=01", "compare=", "compare=latest", "round=1&round=2"} {
		rec, _ := independentReport(t, h, "/api/blocks/1001/rounds?view=compact&"+q, "")
		if rec.Code != 400 {
			t.Fatalf("invalid selection accepted: %s status%d", q, rec.Code)
		}
	}
}

func TestCapacityEpochMakesETagProcessSpecific(t *testing.T) {
	tracker, h := independentRoundsServer(t)
	tracker.ObserveRound(1001, 0, "A")
	one, _ := independentReport(t, h, "/api/blocks/1001/rounds?view=compact", "")
	other := New(Options{State: state.New(), Bus: events.NewBus(32), Tracker: tracker})
	two, body := independentReport(t, other.Handler(), "/api/blocks/1001/rounds?view=compact", one.Header().Get("ETag"))
	if two.Code != 200 || two.Header().Get("ETag") == one.Header().Get("ETag") || !strings.Contains(two.Header().Get("Cache-Control"), "private") || body["serverEpoch"] == "" {
		t.Fatal("process boundary reused private representation")
	}
}
