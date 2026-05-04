// Package config loads configuration from TOML, environment variables, and CLI
// flags, in that order of increasing precedence.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the in-memory representation of the TOML schema plus runtime
// resolved fields.
type Config struct {
	Chain      Chain      `toml:"chain"`
	Refresh    Refresh    `toml:"refresh"`
	Divergence Divergence `toml:"divergence"`
	UI         UI         `toml:"ui"`
	Obs        Obs        `toml:"obs"`
}

type Chain struct {
	Name          string   `toml:"name"`
	Bech32Prefix  string   `toml:"bech32_prefix"`
	RPCs          []RPC    `toml:"rpc"`
	LCD           string   `toml:"lcd"`            // Cosmos REST API for moniker / upgrade enrichment
	MonitoredRPCs []string `toml:"monitored_rpcs"` // optional cross-RPC AppHash comparison (HTTP RPC URLs)
	ExplorerURL   string   `toml:"explorer_url"`   // template: {address}
	MintscanPath  string   `toml:"mintscan_path"`  // e.g. "injective"
}

type RPC struct {
	URL     string `toml:"url"`
	Primary bool   `toml:"primary"`
}

type Refresh struct {
	Validators  Duration `toml:"validators"`
	UpgradePlan Duration `toml:"upgrade_plan"`
	Status      Duration `toml:"status"`
	BlockTime   Duration `toml:"block_time"`
	Consensus   Duration `toml:"consensus"` // HTTP fallback poll cadence
}

type Divergence struct {
	ThresholdPct      float64 `toml:"threshold_pct"`
	HistorySize       int     `toml:"history_size"`
	Debounce          Duration `toml:"debounce"`
	IncludePrevotes   bool    `toml:"include_prevotes"`
	SimulateDivergence bool   `toml:"simulate_divergence"`
}

type UI struct {
	Mode string `toml:"mode"` // tui | web | both
	TUI  TUI    `toml:"tui"`
	Web  Web    `toml:"web"`
}

type TUI struct {
	Timezone      string `toml:"timezone"`
	DisableEmojis bool   `toml:"disable_emojis"`
}

type Web struct {
	Listen    string `toml:"listen"`
	Token     string `toml:"token"`
	CORSOrigin string `toml:"cors_origin"`
	Disabled  bool   `toml:"disabled"`
}

type Obs struct {
	MetricsListen string `toml:"metrics_listen"`
	LogLevel      string `toml:"log_level"`
	LogFormat     string `toml:"log_format"`
}

// Duration wraps time.Duration with TOML string parsing.
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	td, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(td)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// Defaults returns Injective-targeted defaults.
func Defaults() Config {
	return Config{
		Chain: Chain{
			Name:         "injective-1",
			Bech32Prefix: "inj",
			RPCs: []RPC{
				{URL: "https://tm.injective.network", Primary: true},
				{URL: "https://injective-rpc.publicnode.com:443"},
			},
			LCD:          "https://lcd.injective.network",
			ExplorerURL:  "https://explorer.injective.network/validator/{address}",
			MintscanPath: "injective",
		},
		Refresh: Refresh{
			Validators:  Duration(30 * time.Second),
			UpgradePlan: Duration(5 * time.Minute),
			Status:      Duration(5 * time.Second),
			BlockTime:   Duration(30 * time.Second),
			Consensus:   Duration(time.Second),
		},
		Divergence: Divergence{
			ThresholdPct:    5.0,
			HistorySize:     32,
			Debounce:        Duration(250 * time.Millisecond),
			IncludePrevotes: false,
		},
		UI: UI{
			Mode: "tui",
			TUI:  TUI{Timezone: "UTC"},
			Web:  Web{Listen: "127.0.0.1:8080"},
		},
		Obs: Obs{
			MetricsListen: "127.0.0.1:9091",
			LogLevel:      "info",
			LogFormat:     "text",
		},
	}
}

// Load merges defaults < TOML file < env (CMTOP_*) < flags. The flags arg is
// applied last by the caller (after this returns).
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path == "" {
		path = DefaultPath()
	}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				return cfg, fmt.Errorf("decode %s: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("stat %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	return cfg, nil
}

// DefaultPath returns the platform-appropriate config path.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cmt-top", "config.toml")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "cmt-top", "config.toml")
	}
	return ""
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("CMTOP_RPC"); v != "" {
		cfg.Chain.RPCs = []RPC{{URL: v, Primary: true}}
	}
	if v := os.Getenv("CMTOP_MODE"); v != "" {
		cfg.UI.Mode = v
	}
	if v := os.Getenv("CMTOP_WEB_LISTEN"); v != "" {
		cfg.UI.Web.Listen = v
	}
	if v := os.Getenv("CMTOP_WEB_TOKEN"); v != "" {
		cfg.UI.Web.Token = v
	}
	if v := os.Getenv("CMTOP_LOG_LEVEL"); v != "" {
		cfg.Obs.LogLevel = v
	}
	if v := os.Getenv("CMTOP_BECH32_PREFIX"); v != "" {
		cfg.Chain.Bech32Prefix = v
	}
	if v := os.Getenv("CMTOP_LCD"); v != "" {
		cfg.Chain.LCD = v
	}
	if v := os.Getenv("CMTOP_MONITORED_RPCS"); v != "" {
		cfg.Chain.MonitoredRPCs = splitCSV(v)
	}
	if v := os.Getenv("CMTOP_METRICS_LISTEN"); v != "" {
		cfg.Obs.MetricsListen = v
	}
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// PrimaryRPC returns the primary RPC URL or the first one.
func (c Config) PrimaryRPC() string {
	for _, r := range c.Chain.RPCs {
		if r.Primary {
			return r.URL
		}
	}
	if len(c.Chain.RPCs) > 0 {
		return c.Chain.RPCs[0].URL
	}
	return ""
}

// Validate returns an error if the config is internally inconsistent.
func (c Config) Validate() error {
	if c.PrimaryRPC() == "" {
		return errors.New("at least one [[chain.rpc]] entry is required")
	}
	switch strings.ToLower(c.UI.Mode) {
	case "tui", "web", "both", "headless":
	default:
		return fmt.Errorf("ui.mode must be one of tui|web|both|headless (got %q)", c.UI.Mode)
	}
	if !strings.HasPrefix(c.UI.Web.Listen, "127.") &&
		!strings.HasPrefix(c.UI.Web.Listen, "localhost") &&
		c.UI.Web.Token == "" {
		return errors.New("ui.web.listen is non-loopback but ui.web.token is empty; refusing to expose dashboard without auth")
	}
	if c.Divergence.ThresholdPct < 0 || c.Divergence.ThresholdPct > 100 {
		return errors.New("divergence.threshold_pct out of range")
	}
	return nil
}

// PrintTOML writes a human-friendly TOML rendering of cfg.
func PrintTOML(cfg Config, out *os.File) error {
	enc := toml.NewEncoder(out)
	return enc.Encode(cfg)
}
