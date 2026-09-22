package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func admissionTestRun(server *httptest.Server) *run {
	return &run{target: server.URL, client: server.Client(), latencies: map[string][]float64{}, random: rand.New(rand.NewSource(1)), s: summary{HTTPStatuses: map[string]int{}, LatencyObservations: map[string]int64{}}}
}

func TestAdmissionHonorsRetryAfterAndRecordsRecoveredFailure(t *testing.T) {
	var probes atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/session" {
			if probes.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{}`)
			return
		}
		conn, err := upgrader.Upgrade(w, req, nil)
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	r := admissionTestRun(server)
	start := time.Now()
	conn, _, err := r.admit(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if time.Since(start) < time.Second || probes.Load() != 2 || r.s.AdmissionTransientErrors != 1 || r.s.AdmissionRetries != 1 {
		t.Fatalf("retry contract failed: elapsed=%s probes=%d transient=%d retries=%d", time.Since(start), probes.Load(), r.s.AdmissionTransientErrors, r.s.AdmissionRetries)
	}
}

func TestAdmissionStopsOnUnauthorizedAndRespectsDeadline(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var probes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				probes.Add(1)
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(code)
			}))
			defer server.Close()
			r := admissionTestRun(server)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			conn, _, err := r.admit(ctx, 0)
			if err == nil || conn != nil || probes.Load() != 1 {
				t.Fatalf("admission did not stop: %v probes=%d", err, probes.Load())
			}
			if code == http.StatusUnauthorized && (r.s.AdmissionRetries != 0 || r.s.AdmissionTransientErrors != 0) {
				t.Fatal("401 was retried")
			}
		})
	}
	fixed := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	if parseRetryAfter(fixed.Add(3*time.Second).Format(http.TimeFormat), fixed) != 3*time.Second || parseRetryAfter("nonsense", fixed) != 0 {
		t.Fatal("Retry-After parsing failed")
	}
}
