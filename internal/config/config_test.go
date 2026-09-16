package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func isolatedConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CMTOP_RPC", "CMTOP_MODE", "CMTOP_WEB_LISTEN", "CMTOP_WEB_TOKEN", "CMTOP_LOG_LEVEL", "CMTOP_BECH32_PREFIX", "CMTOP_LCD", "CMTOP_MONITORED_RPCS", "CMTOP_METRICS_LISTEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingExplicitConfigFailsWhileDefaultPathIsOptional(t *testing.T) {
	isolatedConfigEnvironment(t)
	if _, err := Load(filepath.Join(t.TempDir(), "typo.toml")); err == nil {
		t.Fatal("explicit missing config silently fell back to defaults")
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("absent optional default config: %v", err)
	}
	if cfg.PrimaryRPC() != Defaults().PrimaryRPC() {
		t.Fatalf("default RPC = %q", cfg.PrimaryRPC())
	}
}

func TestConfigPrecedenceAndCorrectlyScopedMonitoredRPCs(t *testing.T) {
	isolatedConfigEnvironment(t)
	path := writeTestConfig(t, `
[chain]
lcd = "https://lcd.example"
monitored_rpcs = ["https://observer.example"]
[[chain.rpc]]
url = "https://primary.example"
primary = true
[ui]
mode = "both"
[refresh]
status = "7s"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PrimaryRPC() != "https://primary.example" || cfg.UI.Mode != "both" || cfg.Refresh.Status.D() != 7*time.Second {
		t.Fatalf("TOML did not override defaults: %+v", cfg)
	}
	if len(cfg.Chain.MonitoredRPCs) != 1 || cfg.Chain.MonitoredRPCs[0] != "https://observer.example" {
		t.Fatalf("monitored RPCs = %#v", cfg.Chain.MonitoredRPCs)
	}
	if cfg.Refresh.Validators != Defaults().Refresh.Validators {
		t.Fatal("omitted TOML field did not retain default")
	}
	t.Setenv("CMTOP_RPC", "https://environment.example")
	t.Setenv("CMTOP_MODE", "web")
	t.Setenv("CMTOP_MONITORED_RPCS", " https://one.example, ,https://two.example ")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PrimaryRPC() != "https://environment.example" || len(cfg.Chain.RPCs) != 1 || cfg.UI.Mode != "web" {
		t.Fatalf("environment did not override TOML: %+v", cfg)
	}
	if cfg.Chain.LCD != "https://lcd.example" || len(cfg.Chain.MonitoredRPCs) != 2 {
		t.Fatalf("unexpected merged chain config: %+v", cfg.Chain)
	}
}

func TestUnknownAndMisplacedConfigKeysFailClearly(t *testing.T) {
	isolatedConfigEnvironment(t)
	for _, content := range []string{
		"[refresh]\nstauts = \"1s\"\n",
		"[[chain.rpc]]\nurl = \"https://primary.example\"\nmonitored_rpcs = [\"https://observer.example\"]\n",
	} {
		if _, err := Load(writeTestConfig(t, content)); err == nil {
			t.Fatalf("unknown configuration silently ignored: %s", content)
		}
	}
}

func TestValidateRejectsUnsafeRuntimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"no RPC", func(c *Config) { c.Chain.RPCs = nil }},
		{"RPC without scheme", func(c *Config) { c.Chain.RPCs[0].URL = "localhost:26657" }},
		{"unsupported RPC scheme", func(c *Config) { c.Chain.RPCs[0].URL = "ftp://node.example" }},
		{"RPC without hostname", func(c *Config) { c.Chain.RPCs[0].URL = "https:///status" }},
		{"bad monitored RPC", func(c *Config) { c.Chain.MonitoredRPCs = []string{"not-a-url"} }},
		{"bad LCD", func(c *Config) { c.Chain.LCD = "file:///tmp/data" }},
		{"zero validators period", func(c *Config) { c.Refresh.Validators = 0 }},
		{"zero status period", func(c *Config) { c.Refresh.Status = 0 }},
		{"negative upgrade period", func(c *Config) { c.Refresh.UpgradePlan = Duration(-time.Second) }},
		{"zero block period", func(c *Config) { c.Refresh.BlockTime = 0 }},
		{"zero consensus period", func(c *Config) { c.Refresh.Consensus = 0 }},
		{"zero history", func(c *Config) { c.Divergence.HistorySize = 0 }},
		{"negative history", func(c *Config) { c.Divergence.HistorySize = -1 }},
		{"negative threshold", func(c *Config) { c.Divergence.ThresholdPct = -1 }},
		{"threshold above 100", func(c *Config) { c.Divergence.ThresholdPct = 101 }},
		{"NaN threshold", func(c *Config) { c.Divergence.ThresholdPct = math.NaN() }},
		{"infinite threshold", func(c *Config) { c.Divergence.ThresholdPct = math.Inf(1) }},
		{"disabled only requested UI", func(c *Config) { c.UI.Mode = "web"; c.UI.Web.Disabled = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
	for _, mode := range []string{"tui", "web", "both", "headless"} {
		cfg := Defaults()
		cfg.UI.Mode = mode
		if err := cfg.Validate(); err != nil {
			t.Errorf("valid mode %q rejected: %v", mode, err)
		}
	}
	cfg := Defaults()
	cfg.UI.Mode = "both"
	cfg.UI.Web.Disabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TUI with explicitly disabled web must remain usable: %v", err)
	}
}
