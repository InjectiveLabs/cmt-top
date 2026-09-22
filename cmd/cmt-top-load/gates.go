package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Outcomes are checked per session: one prolific investigator must not hide a
// different session that never crossed its subscription barrier or got a report.
type investigatorOutcome struct {
	Intended, ProfileReady, ValidReport bool
}

func investigatorsComplete(outcomes []investigatorOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.Intended && (!outcome.ProfileReady || !outcome.ValidReport) {
			return false
		}
	}
	return true
}

func parseDropMetrics(body []byte) (map[string]float64, error) {
	out := map[string]float64{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "cmt_top_") || !(strings.Contains(line, "dropped") || strings.Contains(line, `event="dropped"`)) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed drop counter: %q", line)
		}
		n, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return nil, fmt.Errorf("invalid drop counter %s: %q", fields[0], fields[1])
		}
		if _, duplicate := out[fields[0]]; duplicate {
			return nil, fmt.Errorf("duplicate drop counter %s", fields[0])
		}
		out[fields[0]] = n
	}
	if len(out) == 0 {
		return nil, errors.New("metrics endpoint did not expose drop counters")
	}
	return out, nil
}

func dropDeltas(before, after map[string]float64) (map[string]float64, error) {
	for key, value := range before {
		last, exists := after[key]
		if !exists {
			return nil, fmt.Errorf("final scrape missing baseline drop counter %s", key)
		}
		if last < value {
			return nil, fmt.Errorf("drop counter reset during run: %s", key)
		}
	}
	out := make(map[string]float64, len(after))
	for key, value := range after {
		// Newly created label series have an implicit baseline of zero, so their
		// first observed drop cannot disappear from the acceptance result.
		out[key] = value - before[key]
	}
	return out, nil
}

type reportRound struct {
	Round      *int64          `json:"round"`
	Validators json.RawMessage `json:"validators"`
	Prevotes   json.RawMessage `json:"prevotes"`
	Precommits json.RawMessage `json:"precommits"`
}

func isJSONArray(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '['
}
func isJSONObject(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '{'
}
func validateRoundDetail(round reportRound) error {
	if round.Round == nil || *round.Round < 0 || !isJSONArray(round.Validators) || !isJSONObject(round.Prevotes) || !isJSONObject(round.Precommits) {
		return errors.New("report round lacks its validator and vote details")
	}
	return nil
}

// validateReport checks the response representation, not just HTTP success. The
// independent source oracle subsequently checks the complete captured evidence.
func validateReport(body []byte, height int64, compact bool, epoch string) error {
	var report struct {
		Height           *int64          `json:"height"`
		Found            *bool           `json:"found"`
		Status           string          `json:"status"`
		SchemaVersion    int             `json:"schemaVersion"`
		ServerEpoch      string          `json:"serverEpoch"`
		Revision         string          `json:"revision"`
		Rounds           json.RawMessage `json:"rounds"`
		Details          json.RawMessage `json:"details"`
		Context          json.RawMessage `json:"context"`
		SelectedRound    *int64          `json:"selectedRound"`
		SelectionStatus  string          `json:"selectionStatus"`
		ComparisonRound  *int64          `json:"comparisonRound"`
		ComparisonStatus string          `json:"comparisonStatus"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		return fmt.Errorf("invalid report JSON: %w", err)
	}
	if report.Height == nil || *report.Height != height || report.Found == nil || report.Status == "" || !isJSONArray(report.Rounds) {
		return errors.New("report height, availability or rounds shape is invalid")
	}
	var rounds []reportRound
	if err := json.Unmarshal(report.Rounds, &rounds); err != nil {
		return fmt.Errorf("invalid report rounds: %w", err)
	}
	if !compact {
		if len(report.Details) != 0 || !isJSONObject(report.Context) {
			return errors.New("legacy report lacks full representation/context")
		}
		for _, round := range rounds {
			if err := validateRoundDetail(round); err != nil {
				return err
			}
		}
		return nil
	}
	if report.SchemaVersion != 1 || epoch == "" || report.ServerEpoch != epoch || report.Revision == "" || !isJSONArray(report.Details) || len(report.Context) != 0 {
		return errors.New("compact report schema, epoch, revision or details shape is invalid")
	}
	var details []reportRound
	if err := json.Unmarshal(report.Details, &details); err != nil {
		return fmt.Errorf("invalid compact details: %w", err)
	}
	// This runner requests latest with no comparison, so exactly its selected
	// round must carry details, and summaries must not contain validators.
	if report.ComparisonRound != nil || report.ComparisonStatus != "none" || len(details) > 1 {
		return errors.New("compact report returned an unrequested comparison")
	}
	for _, round := range rounds {
		if round.Round == nil || *round.Round < 0 || len(round.Validators) != 0 || !isJSONObject(round.Prevotes) || !isJSONObject(round.Precommits) {
			return errors.New("compact report has invalid round summaries")
		}
	}
	if len(rounds) > 0 {
		latest := rounds[len(rounds)-1].Round
		if report.SelectedRound == nil || *report.SelectedRound != *latest || report.SelectionStatus != "available" || len(details) != 1 {
			return errors.New("compact latest selection does not match summaries")
		}
		if err := validateRoundDetail(details[0]); err != nil {
			return err
		}
		if *details[0].Round != *report.SelectedRound {
			return errors.New("compact detail differs from selected round")
		}
	} else if report.SelectedRound != nil || len(details) != 0 || (report.SelectionStatus != "not_observed" && report.SelectionStatus != "evicted") {
		return errors.New("empty compact report has invalid selection")
	}
	return nil
}

func validateReportResponse(code int, body []byte, etag, priorETag string, height int64, compact bool, epoch string) error {
	if code == 304 {
		if !compact || priorETag == "" || etag != priorETag || len(body) != 0 {
			return errors.New("304 does not reference a previously validated report")
		}
		return nil
	}
	if code != 200 {
		return fmt.Errorf("report HTTP %d", code)
	}
	if compact && etag == "" {
		return errors.New("compact report has no ETag")
	}
	return validateReport(body, height, compact, epoch)
}
