// Package metrics exposes Prometheus collectors that subscribe to the bus.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/obs"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

// Collectors holds the prometheus metrics cmt-top exposes.
type Collectors struct {
	Height          prometheus.Gauge
	Round           prometheus.Gauge
	BlockTime       prometheus.Gauge
	DivergentRounds prometheus.Counter
	DivergenceVPpct *prometheus.GaugeVec
	WSConnected     prometheus.Gauge
	VoteEventsTotal *prometheus.CounterVec
	BusDropped      *prometheus.GaugeVec
	DataAge         prometheus.Gauge
	tracker         *divergence.Tracker
}

// SetTracker configures live gauge reconciliation before Run is started.
func (c *Collectors) SetTracker(tracker *divergence.Tracker) { c.tracker = tracker }

var wsDropped atomic.Uint64
var ingestDropped atomic.Uint64

func RecordWSDrop(n uint64)     { wsDropped.Add(n) }
func RecordIngestDrop(n uint64) { ingestDropped.Add(n) }

// Register builds collectors and registers them on the default registry.
func Register() *Collectors {
	c := &Collectors{
		Height: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "cmt_top_height", Help: "Current block height observed.",
		}),
		Round: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "cmt_top_round", Help: "Current consensus round.",
		}),
		BlockTime: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "cmt_top_block_time_seconds", Help: "Average block time over last sample window.",
		}),
		DivergentRounds: promauto.NewCounter(prometheus.CounterOpts{
			Name: "cmt_top_divergent_rounds_total", Help: "Total divergent rounds detected.",
		}),
		DivergenceVPpct: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cmt_top_divergence_voting_power_pct",
			Help: "Power percentage in the largest other non-nil BlockID group of the latest live split; zero on resolution.",
		}, []string{"kind"}),
		WSConnected: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "cmt_top_ws_connected", Help: "1 if the CometBFT WebSocket is currently connected.",
		}),
		VoteEventsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "cmt_top_vote_events_total", Help: "Total vote events received from CometBFT.",
		}, []string{"type"}),
		BusDropped: promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "cmt_top_bus_dropped_events_total", Help: "Dropped events for each active internal subscriber."}, []string{"subscriber"}),
		DataAge:    promauto.NewGauge(prometheus.GaugeOpts{Name: "cmt_top_data_age_seconds", Help: "Seconds since the latest successful upstream observation; -1 before first data."}),
	}
	promauto.NewCounterFunc(prometheus.CounterOpts{Name: "cmt_top_ws_dropped_events_total", Help: "Outbound websocket queue drops."}, func() float64 { return float64(wsDropped.Load()) })
	promauto.NewCounterFunc(prometheus.CounterOpts{Name: "cmt_top_ingest_dropped_events_total", Help: "Dropped upstream events before orchestration."}, func() float64 { return float64(ingestDropped.Load()) })
	c.DataAge.Set(-1)
	return c
}

// Run subscribes to the bus and updates collectors.
func (c *Collectors) Run(ctx context.Context, bus *events.Bus, states ...*state.State) {
	defer obs.Recover("metrics")
	ch, cancel := bus.Subscribe("metrics",
		events.KindNewBlock, events.KindRoundChanged, events.KindBlockTimeUpdated,
		events.KindDivergenceDetected, events.KindDivergenceResolved,
		events.KindVoteReceived, events.KindConnectionLost, events.KindConnectionRestored,
	)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			c.handle(ev)
		case <-ticker.C:
			for name, count := range bus.Stats() {
				c.BusDropped.WithLabelValues(name).Set(float64(count))
			}
			if c.tracker != nil {
				for _, kind := range []divergence.VoteType{divergence.Prevote, divergence.Precommit} {
					c.DivergenceVPpct.WithLabelValues(kind.String()).Set(0)
				}
				for _, report := range c.tracker.CurrentReport().Live {
					if report.IsDivergent {
						c.setDivergencePower(report)
					}
				}
			}
			if len(states) > 0 && states[0] != nil {
				s := states[0].Snapshot()
				c.Height.Set(float64(s.LastCommittedHeight))
				c.Round.Set(float64(s.Round))
				if !s.Health.LastSuccessAt.IsZero() {
					c.DataAge.Set(time.Since(s.Health.LastSuccessAt).Seconds())
				}
				if s.Health.WSConnected {
					c.WSConnected.Set(1)
				} else {
					c.WSConnected.Set(0)
				}
			}
		}
	}
}

func (c *Collectors) handle(ev events.Event) {
	switch ev.Kind {
	case events.KindNewBlock:
		p := ev.Payload.(events.NewBlock)
		c.Height.Set(float64(p.Height))
	case events.KindRoundChanged:
		p := ev.Payload.(events.RoundChanged)
		c.Round.Set(float64(p.Round))
	case events.KindBlockTimeUpdated:
		p := ev.Payload.(events.BlockTimeUpdated)
		c.BlockTime.Set(p.BlockTime.Seconds())
	case events.KindDivergenceDetected:
		p, ok := ev.Payload.(divergence.RoundReport)
		if !ok || !p.IsDivergent {
			return
		}
		c.DivergentRounds.Inc()
		c.setDivergencePower(p)
	case events.KindDivergenceResolved:
		if p, ok := ev.Payload.(divergence.RoundReport); ok {
			c.DivergenceVPpct.WithLabelValues(p.Type.String()).Set(0)
		}
	case events.KindVoteReceived:
		p := ev.Payload.(events.VoteReceived)
		c.VoteEventsTotal.WithLabelValues(p.Type.String()).Inc()
	case events.KindConnectionLost:
		c.WSConnected.Set(0)
	case events.KindConnectionRestored:
		c.WSConnected.Set(1)
	}
}

func (c *Collectors) setDivergencePower(report divergence.RoundReport) {
	var maxOther float64
	leader := false
	for _, group := range report.Groups {
		if group.BlockIDHash == "" {
			continue
		}
		if !leader {
			leader = true
			continue
		}
		if group.VotingPowerPct > maxOther {
			maxOther = group.VotingPowerPct
		}
	}
	c.DivergenceVPpct.WithLabelValues(report.Type.String()).Set(maxOther)
}

func itoa64(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// Listen serves the metrics endpoint on its own listener.
func Listen(ctx context.Context, addr string, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("metrics listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
