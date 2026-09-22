// Command cmt-top is a top-style live dashboard for CometBFT-based Cosmos
// chains, defaulting to Injective.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/InjectiveLabs/cmt-top/internal/config"
	"github.com/InjectiveLabs/cmt-top/internal/core"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/metrics"
	"github.com/InjectiveLabs/cmt-top/internal/obs"
	"github.com/InjectiveLabs/cmt-top/internal/state"
	"github.com/InjectiveLabs/cmt-top/internal/tui"
	"github.com/InjectiveLabs/cmt-top/internal/web"
)

var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		configPath    string
		printConfig   bool
		rpcURLs       []string
		lcdURL        string
		monitoredRPCs []string
		mode          string
		webListen     string
		webToken      string
		webMaxClients int
		metricsAddr   string
		logLevel      string
		bech32        string
		threshold     float64
		thresholdSet  bool
	)
	cmd := &cobra.Command{
		Use:           "cmt-top [rpc-url]",
		Short:         "Live dashboard for CometBFT-based chains, defaulting to Injective",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			// Positional arg overrides primary RPC.
			if len(args) == 1 && args[0] != "" {
				cfg.Chain.RPCs = []config.RPC{{URL: args[0], Primary: true}}
			}
			if len(rpcURLs) > 0 {
				cfg.Chain.RPCs = make([]config.RPC, 0, len(rpcURLs))
				for i, u := range rpcURLs {
					cfg.Chain.RPCs = append(cfg.Chain.RPCs, config.RPC{URL: u, Primary: i == 0})
				}
			}
			if lcdURL != "" {
				cfg.Chain.LCD = lcdURL
			}
			if len(monitoredRPCs) > 0 {
				cfg.Chain.MonitoredRPCs = monitoredRPCs
			}
			if mode != "" {
				cfg.UI.Mode = mode
			}
			if webListen != "" {
				cfg.UI.Web.Listen = webListen
			}
			if cmd.Flags().Changed("web-max-clients") {
				cfg.UI.Web.MaxClients = webMaxClients
			}
			if webToken != "" {
				cfg.UI.Web.Token = webToken
			}
			if metricsAddr != "" {
				cfg.Obs.MetricsListen = metricsAddr
			}
			if logLevel != "" {
				cfg.Obs.LogLevel = logLevel
			}
			if bech32 != "" {
				cfg.Chain.Bech32Prefix = bech32
			}
			if thresholdSet {
				cfg.Divergence.ThresholdPct = threshold
			}

			if printConfig {
				return config.PrintTOML(cfg, os.Stdout)
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			return run(cfg)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "config file (default $XDG_CONFIG_HOME/cmt-top/config.toml)")
	cmd.Flags().BoolVar(&printConfig, "print-config", false, "print resolved config to stdout and exit")
	cmd.Flags().StringSliceVar(&rpcURLs, "rpc", nil, "CometBFT RPC URL (HTTP+WS); pass --rpc multiple times for failover; first is primary")
	cmd.Flags().StringVar(&lcdURL, "lcd", "", "Cosmos LCD/REST URL for moniker + upgrade-plan enrichment")
	cmd.Flags().StringSliceVar(&monitoredRPCs, "monitored-rpc", nil, "extra RPC URL(s) for cross-endpoint AppHash comparison; pass multiple times")
	cmd.Flags().StringVar(&mode, "mode", "", "tui | web | both | headless")
	cmd.Flags().StringVar(&webListen, "web-listen", "", "web bind addr (default 127.0.0.1:8080)")
	cmd.Flags().IntVar(&webMaxClients, "web-max-clients", 256, "maximum browser WebSocket connections")
	cmd.Flags().StringVar(&webToken, "web-token", "", "bearer token; recommended when binding non-loopback")
	cmd.Flags().StringVar(&metricsAddr, "metrics-listen", "", "prometheus bind addr")
	cmd.Flags().StringVar(&logLevel, "log-level", "", "debug | info | warn | error")
	cmd.Flags().StringVar(&bech32, "bech32-prefix", "", "bech32 prefix (default inj)")
	cmd.Flags().Float64Var(&threshold, "divergence-threshold", 5.0, "divergence threshold percent")
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		thresholdSet = cmd.Flags().Changed("divergence-threshold")
	}
	cmd.Version = version
	return cmd
}

func run(cfg config.Config) error {
	level := parseLevel(cfg.Obs.LogLevel)
	format := obs.FormatText
	if strings.EqualFold(cfg.Obs.LogFormat, "json") {
		format = obs.FormatJSON
	}
	// In TUI/both modes, log to stderr is fine — tview owns stdout.
	ring := obs.Setup(level, format, os.Stderr)
	log := slog.Default().With("v", version)

	bus := events.NewBus(512)
	st := state.New()

	orch, err := core.New(cfg, bus, st)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Required services report their failure to the main goroutine. A port bind
	// failure must terminate the process so supervisors can detect it.
	failures := make(chan error, 3)
	startService := func(name string, serve func() error) {
		go func() {
			if err := serve(); err != nil {
				failures <- fmt.Errorf("%s: %w", name, err)
				cancel()
			}
		}()
	}
	go orch.Run(ctx)
	collectors := metrics.Register()
	collectors.SetTracker(orch.Tracker())
	go collectors.Run(ctx, bus, st)
	if cfg.Obs.MetricsListen != "" {
		startService("metrics", func() error { return metrics.Listen(ctx, cfg.Obs.MetricsListen, log) })
	}
	mode := strings.ToLower(cfg.UI.Mode)
	if (mode == "web" || mode == "both") && !cfg.UI.Web.Disabled {
		srv := web.New(web.Options{Listen: cfg.UI.Web.Listen, Token: cfg.UI.Web.Token, CORSOrigin: cfg.UI.Web.CORSOrigin,
			MaxClients: cfg.UI.Web.MaxClients, APIRateLimit: cfg.UI.Web.APIRateLimit, TrustedProxies: cfg.UI.Web.TrustedProxies, Capacity: collectors.Capacity,
			DisplayName: cfg.Chain.Name, ExplorerURL: cfg.Chain.ExplorerURL, State: st, Bus: bus, Tracker: orch.Tracker(), Logger: log, Ring: ring, Version: version,
			Ready: func() bool { snap := st.Snapshot(); return snap.LastRound != nil && snap.NodeStatus != nil },
		})
		startService("web", func() error { return srv.Run(ctx) })
	}
	if mode == "tui" || mode == "both" {
		ui := tui.New(tui.Options{State: st, Bus: bus, Tracker: orch.Tracker(), Logger: log, Timezone: cfg.UI.TUI.Timezone, DisableEmoji: cfg.UI.TUI.DisableEmojis})
		if err := ui.Run(ctx); err != nil {
			return err
		}
	} else {
		<-ctx.Done()
	}
	select {
	case err := <-failures:
		return err
	default:
	}

	return nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
