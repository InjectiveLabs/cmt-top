package config

import "testing"

func TestCapacitySettingsPrecedenceAndValidation(t *testing.T) {
	isolatedConfigEnvironment(t)
	path := writeTestConfig(t, "[ui.web]\nmax_clients=100\napi_rate_limit=300\ntrusted_proxies=[\"10.0.0.0/24\"]\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Web.MaxClients != 100 || cfg.UI.Web.APIRateLimit != 300 || len(cfg.UI.Web.TrustedProxies) != 1 {
		t.Fatalf("TOML settings: %+v", cfg.UI.Web)
	}
	t.Setenv("CMTOP_WEB_MAX_CLIENTS", "256")
	t.Setenv("CMTOP_WEB_API_RATE_LIMIT", "600")
	t.Setenv("CMTOP_WEB_TRUSTED_PROXIES", "192.0.2.0/24, 2001:db8::/32")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Web.MaxClients != 256 || cfg.UI.Web.APIRateLimit != 600 || len(cfg.UI.Web.TrustedProxies) != 2 {
		t.Fatalf("environment settings: %+v", cfg.UI.Web)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CMTOP_WEB_MAX_CLIENTS", "CMTOP_WEB_API_RATE_LIMIT"} {
		for _, value := range []string{"0", "-1", "oops", "9999999999999999999999999"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				t.Setenv(name, value)
				if _, err := Load(path); err == nil {
					t.Fatal("invalid value accepted")
				}
			})
		}
	}
	for _, text := range []string{"max_clients=0", "api_rate_limit=-1", "trusted_proxies=[\"not-a-cidr\"]"} {
		t.Run(text, func(t *testing.T) {
			isolatedConfigEnvironment(t)
			cfg, err := Load(writeTestConfig(t, "[ui.web]\n"+text+"\n"))
			if err == nil {
				err = cfg.Validate()
			}
			if err == nil {
				t.Fatal("invalid TOML settings accepted")
			}
		})
	}
}
