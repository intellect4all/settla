package factory

import (
	"testing"
)

func TestLoadProviderConfig_Defaults(t *testing.T) {
	cfg := LoadProviderConfig("unknown-provider")

	if !cfg.Enabled {
		t.Error("expected Enabled=true by default")
	}
	if cfg.CBFailures != 15 {
		t.Errorf("expected CBFailures=15, got %d", cfg.CBFailures)
	}
	if cfg.CBResetMs != 10000 {
		t.Errorf("expected CBResetMs=10000, got %d", cfg.CBResetMs)
	}
	if cfg.RateLimitPerSec != 100 {
		t.Errorf("expected RateLimitPerSec=100, got %d", cfg.RateLimitPerSec)
	}
	if cfg.RateLimitBurst != 200 {
		t.Errorf("expected RateLimitBurst=200, got %d", cfg.RateLimitBurst)
	}
}

func TestLoadProviderConfig_FromEnv(t *testing.T) {
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_ENABLED", "true")
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_CB_FAILURES", "10")
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_CB_RESET_MS", "5000")
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_RATE_LIMIT", "50")
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_RATE_BURST", "100")
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_API_KEY", "sk_live_xxx")
	t.Setenv("SETTLA_PROVIDER_FLUTTERWAVE_TX_LIMIT", "50000")

	cfg := LoadProviderConfig("flutterwave")

	if !cfg.Enabled {
		t.Error("expected Enabled=true")
	}
	if cfg.CBFailures != 10 {
		t.Errorf("expected CBFailures=10, got %d", cfg.CBFailures)
	}
	if cfg.CBResetMs != 5000 {
		t.Errorf("expected CBResetMs=5000, got %d", cfg.CBResetMs)
	}
	if cfg.RateLimitPerSec != 50 {
		t.Errorf("expected RateLimitPerSec=50, got %d", cfg.RateLimitPerSec)
	}
	if cfg.RateLimitBurst != 100 {
		t.Errorf("expected RateLimitBurst=100, got %d", cfg.RateLimitBurst)
	}
	if cfg.Extra["API_KEY"] != "sk_live_xxx" {
		t.Errorf("expected Extra[API_KEY]=sk_live_xxx, got %q", cfg.Extra["API_KEY"])
	}
	if cfg.Extra["TX_LIMIT"] != "50000" {
		t.Errorf("expected Extra[TX_LIMIT]=50000, got %q", cfg.Extra["TX_LIMIT"])
	}
}

func TestLoadProviderConfig_DisabledProvider(t *testing.T) {
	t.Setenv("SETTLA_PROVIDER_YELLOW_CARD_ENABLED", "false")

	cfg := LoadProviderConfig("yellow-card")

	if cfg.Enabled {
		t.Error("expected Enabled=false")
	}
}

func TestLoadProviderConfig_HyphenToUnderscore(t *testing.T) {
	t.Setenv("SETTLA_PROVIDER_MOCK_ONRAMP_GBP_DELAY_MS", "250")

	cfg := LoadProviderConfig("mock-onramp-gbp")

	if cfg.Extra["DELAY_MS"] != "250" {
		t.Errorf("expected Extra[DELAY_MS]=250, got %q", cfg.Extra["DELAY_MS"])
	}
}

func TestEnvKey(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"flutterwave", "FLUTTERWAVE"},
		{"mock-onramp-gbp", "MOCK_ONRAMP_GBP"},
		{"yellow-card", "YELLOW_CARD"},
		{"settla-onramp", "SETTLA_ONRAMP"},
	}
	for _, tc := range tests {
		got := envKey(tc.input)
		if got != tc.want {
			t.Errorf("envKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
