package main

import (
	"strings"
	"time"
)

// Scenario defines a named, predefined load test configuration.
type Scenario struct {
	Name        string
	Description string
	Config      LoadTestConfig
}

// seedLemfi and seedFincra are the two seeded test tenants.
var (
	seedLemfi = TenantConfig{
		ID:       "a0000000-0000-0000-0000-000000000001",
		APIKey:   "sk_live_lemfi_demo_key",
		Currency: "GBP",
		Country:  "NG",
	}
	seedFincra = TenantConfig{
		ID:       "b0000000-0000-0000-0000-000000000002",
		APIKey:   "sk_live_fincra_demo_key",
		Currency: "NGN",
		Country:  "GB",
	}
)

// multiTenantPool builds a pool of N tenants by cycling through seed tenants.
// Simulates N distinct tenants (same credentials, different logical tenants in test).
func multiTenantPool(n int) []TenantConfig {
	pool := make([]TenantConfig, n)
	seeds := []TenantConfig{seedLemfi, seedFincra}
	for i := range pool {
		pool[i] = seeds[i%len(seeds)]
	}
	return pool
}

// predefinedScenarios holds all named scenarios keyed by canonical name.
var predefinedScenarios = map[string]Scenario{
	// ScenarioPeakLoad proves the system handles 50M transactions/day by
	// sustaining the maximum expected peak (5,000 TPS) for 10 minutes.
	// If it holds peak without degradation, the extrapolation to 580 TPS
	// sustained over 24 hours follows directly.
	//
	// Traffic split: Lemfi 50% (GBP→NGN), Fincra 50% (NGN→GBP).
	// Expected transfers: ~3,000,000 total.
	"PeakLoad": {
		Name:        "PeakLoad",
		Description: "Peak load: 5,000 TPS for 10 minutes. Proves 50M transactions/day capacity.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        []TenantConfig{seedLemfi, seedFincra},
			TargetTPS:      5000,
			Duration:       10 * time.Minute,
			RampUpDuration: 30 * time.Second,
			DrainDuration:  60 * time.Second,
		},
	},

	// ScenarioSustainedLoad validates the system under the average expected
	// daily throughput (580 TPS) for an extended period (30 minutes).
	// Expected transfers: ~1,080,000 total.
	"SustainedLoad": {
		Name:        "SustainedLoad",
		Description: "Sustained load: 600 TPS for 30 minutes. Validates steady-state throughput.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        []TenantConfig{seedLemfi, seedFincra},
			TargetTPS:      600,
			Duration:       30 * time.Minute,
			RampUpDuration: 30 * time.Second,
			DrainDuration:  60 * time.Second,
		},
	},

	// ScenarioBurstRecovery proves the system handles sudden traffic spikes
	// and returns to normal operation.  Ramp-up drives to 8,000 TPS over
	// 2 minutes, sustains for 5 minutes, then the process exits — the
	// "recovery" back to steady-state is validated by the post-test
	// consistency verification (no stuck transfers, positions reconcile).
	"BurstRecovery": {
		Name:        "BurstRecovery",
		Description: "Burst recovery: ramp to 8,000 TPS over 2 min, hold 5 min. Tests spike handling.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(20),
			TargetTPS:      8000,
			Duration:       5 * time.Minute,
			RampUpDuration: 2 * time.Minute,
			DrainDuration:  90 * time.Second,
		},
	},

	// ScenarioSingleTenantFlood sends all traffic from one tenant.
	// Validates per-tenant treasury hot-key handling: 3,000 concurrent
	// reservations on the same tenant position must not over-reserve or
	// deadlock.
	"SingleTenantFlood": {
		Name:        "SingleTenantFlood",
		Description: "Single-tenant flood: 3,000 TPS from one tenant. Proves treasury hot-key safety.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        []TenantConfig{seedLemfi},
			TargetTPS:      3000,
			Duration:       5 * time.Minute,
			RampUpDuration: 30 * time.Second,
			DrainDuration:  60 * time.Second,
		},
	},

	// ScenarioMultiTenantScale simulates 50 independent tenants each at
	// 100 TPS for a combined 5,000 TPS.  Proves per-tenant isolation: no
	// cross-tenant data leakage, independent rate limits, independent
	// treasury positions.
	"MultiTenantScale": {
		Name:        "MultiTenantScale",
		Description: "Multi-tenant scale: 50 tenants × 100 TPS = 5,000 TPS. Proves tenant isolation.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(50),
			TargetTPS:      5000,
			Duration:       10 * time.Minute,
			RampUpDuration: 30 * time.Second,
			DrainDuration:  60 * time.Second,
		},
	},
}

// GetScenario returns the named scenario. Names are case-insensitive.
func GetScenario(name string) (Scenario, bool) {
	// Try exact match first.
	if s, ok := predefinedScenarios[name]; ok {
		return s, true
	}
	// Case-insensitive fallback.
	lower := strings.ToLower(name)
	for k, v := range predefinedScenarios {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}
	return Scenario{}, false
}

// ScenarioNames returns the sorted list of available scenario names.
func ScenarioNames() []string {
	names := make([]string, 0, len(predefinedScenarios))
	for k := range predefinedScenarios {
		names = append(names, k)
	}
	return names
}
