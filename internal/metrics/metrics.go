// Package metrics exposes Prometheus collectors that subscribe to the bus.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ri-go/cmt-top/internal/divergence"
	"github.com/Ri-go/cmt-top/internal/events"
	"github.com/Ri-go/cmt-top/internal/obs"
)

// Collectors holds the prometheus metrics cmt-top exposes.
type Collectors struct {
	Height           prometheus.Gauge
	Round            prometheus.Gauge
	BlockTime        prometheus.Gauge
	DivergentRounds  prometheus.Counter
	DivergenceVPpct  *prometheus.GaugeVec
	WSConnected      prometheus.Gauge
	VoteEventsTotal  *prometheus.CounterVec
}

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
			Help: "Voting power percentage in the largest non-canonical group of the latest divergent round.",
		}, []string{"height", "round", "kind"}),
		WSConnected: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "cmt_top_ws_connected", Help: "1 if the CometBFT WebSocket is currently connected.",
		}),
		VoteEventsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "cmt_top_vote_events_total", Help: "Total vote events received from CometBFT.",
		}, []string{"type"}),
	}
	return c
}

// Run subscribes to the bus and updates collectors.
func (c *Collectors) Run(ctx context.Context, bus *events.Bus) {
	defer obs.Recover("metrics")
	ch, cancel := bus.Subscribe("metrics",
		events.KindNewBlock, events.KindRoundChanged, events.KindBlockTimeUpdated,
		events.KindDivergenceDetected, events.KindDivergenceResolved,
		events.KindVoteReceived, events.KindConnectionLost, events.KindConnectionRestored,
	)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			c.handle(ev)
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
		// Largest non-canonical group's pct
		var maxNon float64
		for i := 1; i < len(p.Groups); i++ {
			if p.Groups[i].VotingPowerPct > maxNon {
				maxNon = p.Groups[i].VotingPowerPct
			}
		}
		c.DivergenceVPpct.WithLabelValues(
			itoa64(p.Height), itoa64(p.Round), p.Type.String(),
		).Set(maxNon)
	case events.KindVoteReceived:
		p := ev.Payload.(events.VoteReceived)
		c.VoteEventsTotal.WithLabelValues(p.Type.String()).Inc()
	case events.KindConnectionLost:
		c.WSConnected.Set(0)
	case events.KindConnectionRestored:
		c.WSConnected.Set(1)
	}
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
