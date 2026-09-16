package metrics

import (
	"testing"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/prometheus/client_golang/prometheus"
)

func TestDivergenceSeriesDoNotGrowWithHeight(t *testing.T) {
	registry := prometheus.NewRegistry()
	old := prometheus.DefaultRegisterer
	prometheus.DefaultRegisterer = registry
	defer func() { prometheus.DefaultRegisterer = old }()
	c := Register()
	for h := int64(1); h <= 500; h++ {
		report := divergence.RoundReport{Height: h, Type: divergence.Precommit, IsDivergent: true, Groups: []divergence.Group{{BlockIDHash: "", VotingPowerPct: 60}, {BlockIDHash: "aa", VotingPowerPct: 30}, {BlockIDHash: "bb", VotingPowerPct: 10}}}
		c.handle(events.Event{Kind: events.KindDivergenceDetected, Payload: report})
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "cmt_top_divergence_voting_power_pct" {
			if len(family.Metric) != 1 || family.Metric[0].GetGauge().GetValue() != 10 {
				t.Fatalf("unbounded or incorrect minority metric: %+v", family)
			}
			if len(family.Metric[0].Label) != 1 || family.Metric[0].Label[0].GetName() != "kind" {
				t.Fatalf("unbounded labels: %+v", family)
			}
		}
		if family.GetName() == "cmt_top_divergent_rounds_total" && family.Metric[0].GetCounter().GetValue() != 500 {
			t.Fatalf("incorrect incident count: %+v", family)
		}
	}
}
