package main

import (
	"math/rand"
	"testing"
)

func TestLoadTargetsMustBeLiteralLoopback(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8080", "http://[::1]:8080"} {
		if err := localURL(u); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"", "https://cmt-top.injective.dev", "http://10.0.0.1", "http://localhost:8080", "http://127.0.0.1.evil.test", "http://user:pass@127.0.0.1", "http://127.0.0.1/api", "http://127.0.0.1?target=live"} {
		if localURL(u) == nil {
			t.Fatalf("unsafe target accepted: %s", u)
		}
	}
}

func TestLongSoakLatencyStorageIsBounded(t *testing.T) {
	r := &run{latencies: make(map[string][]float64), random: rand.New(rand.NewSource(1)), s: summary{LatencyObservations: make(map[string]int64)}}
	for i := 0; i < 1_000_000; i++ {
		r.recordLatencyLocked("report", float64(i))
	}
	if len(r.latencies["report"]) != 32768 || r.s.LatencyObservations["report"] != 1_000_000 {
		t.Fatal("soak latency storage lost count or exceeded bound")
	}
	var early, late bool
	for _, sample := range r.latencies["report"] {
		early = early || sample < 10_000
		late = late || sample > 990_000
	}
	if !early || !late {
		t.Fatal("reservoir discarded one end of the soak")
	}
}
