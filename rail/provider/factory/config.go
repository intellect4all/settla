package factory

import (
	"os"
	"strconv"
	"strings"
)

// ProviderConfig holds per-provider operational configuration loaded from env.
type ProviderConfig struct {
	// Enabled controls whether this provider is activated during bootstrap.
	Enabled bool

	// Circuit breaker settings.
	CBFailures int // failure threshold before opening (default: 15)
	CBResetMs  int // milliseconds before half-open retry (default: 10000)
	CBHalfOpen int // max concurrent half-open requests (default: 2)

	// Rate limiting.
	RateLimitPerSec int // requests per second (default: 100)
	RateLimitBurst  int // burst capacity (default: 200)

	// Extra holds provider-specific key-value pairs (e.g., api_key, rpc_url).
	Extra map[string]string
}

// DefaultProviderConfig returns the default operational config.
func DefaultProviderConfig() ProviderConfig {
	return ProviderConfig{
		Enabled:         true,
		CBFailures:      15,
		CBResetMs:       10000,
		CBHalfOpen:      2,
		RateLimitPerSec: 100,
		RateLimitBurst:  200,
		Extra:           make(map[string]string),
	}
}

// LoadProviderConfig reads per-provider config from environment variables.
//
// Convention: SETTLA_PROVIDER_{UPPER_ID}_{KEY}
// Example for provider "flutterwave":
//
//	SETTLA_PROVIDER_FLUTTERWAVE_ENABLED=true
//	SETTLA_PROVIDER_FLUTTERWAVE_CB_FAILURES=10
//	SETTLA_PROVIDER_FLUTTERWAVE_CB_RESET_MS=5000
//	SETTLA_PROVIDER_FLUTTERWAVE_CB_HALF_OPEN=3
//	SETTLA_PROVIDER_FLUTTERWAVE_RATE_LIMIT=50
//	SETTLA_PROVIDER_FLUTTERWAVE_RATE_BURST=100
//	SETTLA_PROVIDER_FLUTTERWAVE_API_KEY=sk_live_xxx
//
// Any env var matching the prefix but not a known key is stored in Extra.
func LoadProviderConfig(providerID string) ProviderConfig {
	cfg := DefaultProviderConfig()
	prefix := "SETTLA_PROVIDER_" + envKey(providerID) + "_"

	knownKeys := map[string]bool{
		"ENABLED":       true,
		"CB_FAILURES":   true,
		"CB_RESET_MS":   true,
		"CB_HALF_OPEN":  true,
		"RATE_LIMIT":    true,
		"RATE_BURST":    true,
	}

	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, prefix) {
			continue
		}
		kv := strings.SplitN(env, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimPrefix(kv[0], prefix)
		val := kv[1]

		switch key {
		case "ENABLED":
			cfg.Enabled = val == "true" || val == "1" || val == "yes"
		case "CB_FAILURES":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.CBFailures = n
			}
		case "CB_RESET_MS":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.CBResetMs = n
			}
		case "CB_HALF_OPEN":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.CBHalfOpen = n
			}
		case "RATE_LIMIT":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.RateLimitPerSec = n
			}
		case "RATE_BURST":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.RateLimitBurst = n
			}
		default:
			if !knownKeys[key] {
				cfg.Extra[key] = val
			}
		}
	}

	return cfg
}

// envKey converts a provider ID to an uppercase env-safe key.
// "mock-onramp-gbp" → "MOCK_ONRAMP_GBP"
func envKey(id string) string {
	return strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
}
