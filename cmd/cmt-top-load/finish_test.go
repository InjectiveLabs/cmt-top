package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/testutil/replay"
)

func TestFinishSelectsAvailableSourceEvidenceAtFreezeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name            string
		activeHeight    int64
		availableHeight int64
		wantQueries     []int64
		wantReport      int64
	}{
		{"current evidence takes precedence", 1002, 1002, []int64{1002}, 1002},
		{"between commit and next round", 1002, 1001, []int64{1002, 1001}, 1001},
		{"committed evidence also missing", 1002, 0, []int64{1002, 1001}, 0},
		{"missing evidence outside commit boundary", 1003, 1001, []int64{1003}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var oracleQueries []int64
			var reportQueries []string
			var pauseChanges []bool
			paused := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/_fixture/stats":
					_ = json.NewEncoder(w).Encode(replay.Stats{Height: tc.activeHeight, CommittedHeight: 1001, Paused: paused})
				case "/_fixture/control":
					var control struct {
						Paused bool `json:"paused"`
					}
					if err := json.NewDecoder(req.Body).Decode(&control); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					paused = control.Paused
					pauseChanges = append(pauseChanges, paused)
					_, _ = fmt.Fprint(w, `{}`)
				case "/_fixture/oracle":
					height, _ := strconv.ParseInt(req.URL.Query().Get("height"), 10, 64)
					oracleQueries = append(oracleQueries, height)
					var expected *replay.ExpectedHeight
					if height == tc.availableHeight {
						expected = &replay.ExpectedHeight{Height: height, Committed: height == 1001, Rounds: map[int64]*replay.ExpectedRound{}}
					}
					_ = json.NewEncoder(w).Encode(expected)
				default:
					reportQueries = append(reportQueries, req.URL.RequestURI())
					_, _ = fmt.Fprintf(w, `{"height":%d,"found":true,"committed":%t,"rounds":[]}`, tc.availableHeight, tc.availableHeight == 1001)
				}
			}))
			defer server.Close()
			r := admissionTestRun(server)
			r.fixture = server.URL
			r.client.Timeout = time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			r.finish(ctx)
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(oracleQueries, tc.wantQueries) {
				t.Fatalf("oracle queries=%v want %v", oracleQueries, tc.wantQueries)
			}
			if paused || !reflect.DeepEqual(pauseChanges, []bool{true, false}) {
				t.Fatalf("fixture pause was not restored: paused=%v changes=%v", paused, pauseChanges)
			}
			if tc.wantReport == 0 {
				if r.s.OracleVerified || r.s.SessionErrors != 1 || len(reportQueries) != 0 || !strings.Contains(r.s.FirstErrors[0], "source oracle evidence unavailable") {
					t.Fatalf("missing evidence did not fail closed: summary=%+v reports=%v", r.s, reportQueries)
				}
				return
			}
			wantReport := fmt.Sprintf("/api/blocks/%d/rounds?view=full&capture=1", tc.wantReport)
			if !r.s.OracleVerified || r.s.SessionErrors != 0 || r.s.SemanticDigest == "" || !reflect.DeepEqual(reportQueries, []string{wantReport}) {
				t.Fatalf("oracle verification failed: summary=%+v reports=%v", r.s, reportQueries)
			}
		})
	}
}
