package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestCapacityMetricsAreIsolatedAndLabelsBounded(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := NewCapacity(reg)
	c.ObserveWS("active_delta", 1)
	c.ObserveWS("active_delta", -1)
	c.ObserveWS("pending_delta", 1)
	c.ObserveWS("pending_delta", -1)
	for i := 0; i < 100; i++ {
		c.ObserveWS("sent_bytes/untrusted-type", 30)
		c.ObserveHTTP("/api/blocks/12345/rounds", 200, time.Millisecond)
	}
	c.ObserveWS("sent_messages/state.snapshot", 1)
	c.ObserveWS("resync", 1)
	c.ObserveReport("build", 1)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "cmt_top_browser_connections" || f.GetName() == "cmt_top_browser_handshakes_pending" {
			if f.Metric[0].GetGauge().GetValue() != 0 {
				t.Fatalf("unbalanced accounting: %v", f)
			}
		}
		for _, m := range f.Metric {
			for _, l := range m.Label {
				if l.GetValue() == "untrusted-type" || l.GetValue() == "/api/blocks/12345/rounds" {
					t.Fatalf("unbounded label: %v", l)
				}
			}
		}
	}
	// Independent embedding/test servers can use their own registry or no registry.
	_ = NewCapacity(prometheus.NewRegistry())
	_ = NewCapacity(nil)
	var absent *Capacity
	absent.ObserveWS("resync", 1)
	absent.ObserveReport("build", 1)
	absent.ObserveHTTP("/", 200, 0)
}
