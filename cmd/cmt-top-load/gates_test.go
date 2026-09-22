package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDropScrapeRejectsUnavailableMalformedMissingAndResetCounters(t *testing.T) {
	before, err := parseDropMetrics([]byte("cmt_top_ws_dropped_events_total 4\ncmt_top_ingest_dropped_events_total 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "# HELP only metadata", "cmt_top_ws_dropped_events_total NaN", "cmt_top_ws_dropped_events_total +Inf", "cmt_top_ws_dropped_events_total -1", "cmt_top_ws_dropped_events_total bogus", "cmt_top_ws_dropped_events_total", "cmt_top_ws_dropped_events_total 0\ncmt_top_ws_dropped_events_total 0"} {
		if _, err := parseDropMetrics([]byte(body)); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	for name, after := range map[string]map[string]float64{
		"missing": {"cmt_top_ws_dropped_events_total": 4},
		"reset":   {"cmt_top_ws_dropped_events_total": 3, "cmt_top_ingest_dropped_events_total": 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := dropDeltas(before, after); err == nil {
				t.Fatal("invalid final counters accepted")
			}
		})
	}
	after := map[string]float64{"cmt_top_ws_dropped_events_total": 4, "cmt_top_ingest_dropped_events_total": 0, `cmt_top_browser_events_total{event="dropped",reason="queue"}`: 1}
	deltas, err := dropDeltas(before, after)
	if err != nil || deltas[`cmt_top_browser_events_total{event="dropped",reason="queue"}`] != 1 {
		t.Fatalf("new series drop lost: %v %v", deltas, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "cmt_top_ws_dropped_events_total 0")
	}))
	r := &run{metrics: server.URL, client: server.Client()}
	r.finalizeDropMetrics(context.Background(), before)
	if r.s.DropMetricsVerified || r.s.SessionErrors != 1 {
		t.Fatal("503 scrape passed")
	}
	server.Close()
	r.finalizeDropMetrics(context.Background(), before)
	if r.s.DropMetricsVerified || r.s.SessionErrors != 2 {
		t.Fatal("unavailable scrape passed")
	}
}

func TestEveryIntendedInvestigatorMustFinishProfileAndGetValidReport(t *testing.T) {
	good := investigatorOutcome{Intended: true, ProfileReady: true, ValidReport: true}
	for _, blocked := range []investigatorOutcome{
		{Intended: true},
		{Intended: true, ProfileReady: true},
		{Intended: true, ValidReport: true},
	} {
		if investigatorsComplete([]investigatorOutcome{good, blocked, {}}) {
			t.Fatalf("one success hid blocked investigator: %+v", blocked)
		}
	}
	if !investigatorsComplete([]investigatorOutcome{good, {}, good}) {
		t.Fatal("non-investigators were incorrectly required to poll")
	}
}

const validDetail = `{"round":7,"validators":[],"prevotes":{},"precommits":{}}`
const validCompact = `{"height":1001,"found":true,"status":"observed","schemaVersion":1,"serverEpoch":"epoch","revision":"1:2","rounds":[{"round":7,"prevotes":{},"precommits":{}}],"details":[` + validDetail + `],"selectedRound":7,"selectionStatus":"available","comparisonRound":null,"comparisonStatus":"none"}`
const validFull = `{"height":1001,"found":true,"status":"observed","rounds":[` + validDetail + `],"context":{}}`

func TestReportRequiresUsableRepresentationAndConditionalProvenance(t *testing.T) {
	if err := validateReportResponse(200, []byte(validCompact), `"tag"`, "", 1001, true, "epoch"); err != nil {
		t.Fatal(err)
	}
	if err := validateReportResponse(304, nil, `"tag"`, `"tag"`, 1001, true, "epoch"); err != nil {
		t.Fatal(err)
	}
	if err := validateReportResponse(200, []byte(validFull), "", "", 1001, false, ""); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"arbitrary": `{}`, "html": `<html>ok</html>`, "wrong-height": strings.Replace(validCompact, "1001", "1002", 1),
		"wrong-epoch":           strings.Replace(validCompact, `"epoch"`, `"another"`, 1),
		"wrong-schema":          strings.Replace(validCompact, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		"missing-details":       strings.Replace(validCompact, `"details":[`+validDetail+`]`, `"details":null`, 1),
		"wrong-selection":       strings.Replace(validCompact, `"selectedRound":7`, `"selectedRound":8`, 1),
		"missing-summary-phase": strings.Replace(validCompact, `"rounds":[{"round":7,"prevotes":{},"precommits":{}}]`, `"rounds":[{"round":7,"prevotes":{}}]`, 1),
		"missing-votes":         strings.Replace(validCompact, `,"prevotes":{}`, "", 1),
		"full-in-compact":       validFull,
	} {
		t.Run(name, func(t *testing.T) {
			if validateReportResponse(200, []byte(body), `"tag"`, "", 1001, true, "epoch") == nil {
				t.Fatal("unusable compact response credited")
			}
		})
	}
	for _, tc := range []struct {
		code              int
		body, next, prior string
		compact           bool
	}{
		{304, "", `"tag"`, "", true}, {304, "", `"new"`, `"old"`, true}, {304, "unexpected", `"tag"`, `"tag"`, true},
		{304, "", `"tag"`, `"tag"`, false}, {200, validCompact, "", "", true}, {200, validCompact, `"tag"`, "", false},
		{200, `{"height":1001,"found":true,"status":"observed","rounds":[{"round":7}],"context":{}}`, "", "", false}, {503, "", "", "", true},
	} {
		if validateReportResponse(tc.code, []byte(tc.body), tc.next, tc.prior, 1001, tc.compact, "epoch") == nil {
			t.Fatalf("unusable response credited: %+v", tc)
		}
	}
}

func TestAdmissionRetryOnlyRecoversTransientFailures(t *testing.T) {
	for _, code := range []int{0, 429, 503} {
		if !retryAdmission(code, errors.New("transient")) {
			t.Fatalf("should retry %d", code)
		}
	}
	for _, code := range []int{400, 401, 403, 404, 500} {
		if retryAdmission(code, errors.New("handshake")) {
			t.Fatalf("should fail %d", code)
		}
	}
	if retryAdmission(200, nil) {
		t.Fatal("success should not retry")
	}
}
