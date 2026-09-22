package web

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/gorilla/websocket"
)

// The two subscription profiles coexist: normal dashboards reconcile divergence
// inside the state baseline; clients that explicitly omit state get a separate
// periodic report. Suppressing a redundant frame must not consume a sequence.
func TestDivergenceRepairProfilesRemainCompleteAndContiguous(t *testing.T) {
	srv, ts := newWSTestServer(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() { srv.hub.run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()
	waitWS(t, func() bool { _, ok := srv.opts.Bus.Stats()["wshub"]; return ok })
	normal := dialTestWS(t, ts)
	normalFirst := readTestEnvelope(t, normal)
	focused := dialTestWS(t, ts)
	focusedFirst := readTestEnvelope(t, focused)
	if normalFirst.Type != "state.snapshot" || focusedFirst.Type != "state.snapshot" {
		t.Fatal("missing first baseline")
	}
	sendTestCommand(t, focused, "unsubscribe", "state", "blocks", "votes")
	sendTestCommand(t, focused, "ping")
	focusedSeq := focusedFirst.Seq
	for {
		env := readTestEnvelope(t, focused)
		if env.Seq != focusedSeq+1 {
			t.Fatal("subscription barrier introduced gap")
		}
		focusedSeq = env.Seq
		if env.Type == "pong" {
			break
		}
	}
	type observed struct {
		profile string
		counts  map[string]int
		err     error
	}
	results := make(chan observed, 2)
	read := func(profile string, c *websocket.Conn, seq uint64) {
		counts := map[string]int{}
		_ = c.SetReadDeadline(time.Now().Add(2200 * time.Millisecond))
		for {
			var env wsEnvelope
			if err := c.ReadJSON(&env); err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					results <- observed{profile, counts, nil}
				} else {
					results <- observed{profile, counts, err}
				}
				return
			}
			if env.Seq != seq+1 {
				results <- observed{profile, counts, fmt.Errorf("sequence %d after %d", env.Seq, seq)}
				return
			}
			seq = env.Seq
			counts[env.Type]++
			if env.Type == "state.snapshot" {
				p, ok := env.Payload.(map[string]any)
				if !ok || p["divergence"] == nil {
					results <- observed{profile, counts, fmt.Errorf("state baseline omitted divergence")}
					return
				}
			}
		}
	}
	go read("normal", normal, normalFirst.Seq)
	go read("focused", focused, focusedSeq)
	rep := divergence.RoundReport{Height: 10, Round: 0, Type: divergence.Precommit, IsDivergent: true}
	srv.opts.Bus.Publish(events.Event{Kind: events.KindDivergenceDetected, Payload: rep})
	rep.Resolved = true
	srv.opts.Bus.Publish(events.Event{Kind: events.KindDivergenceResolved, Payload: rep})
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("%s reader: %v", r.profile, r.err)
		}
		if r.counts["divergence.detected"] != 1 || r.counts["divergence.resolved"] != 1 {
			t.Fatalf("%s lost immediate alert: %v", r.profile, r.counts)
		}
		if r.profile == "normal" {
			if r.counts["state.snapshot"] < 1 || r.counts["state.snapshot"] > 3 || r.counts["divergence.snapshot"] != 0 {
				t.Fatalf("redundant/missing dashboard repair: %v", r.counts)
			}
		} else if r.counts["divergence.snapshot"] < 1 || r.counts["divergence.snapshot"] > 3 || r.counts["state.snapshot"] != 0 {
			t.Fatalf("missing/unbounded focused repair: %v", r.counts)
		}
	}
}
