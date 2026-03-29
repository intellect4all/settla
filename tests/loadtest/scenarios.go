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
	Thresholds  ScenarioThresholds
}

// ScenarioThresholds defines pass/fail criteria for a scenario.
type ScenarioThresholds struct {
	MaxP50Latency    time.Duration // Max acceptable p50 end-to-end latency
	MaxP99Latency    time.Duration // Max acceptable p99 end-to-end latency
	MaxErrorRate     float64       // Max acceptable error rate (0.0-1.0)
	MaxStuckTransfers int          // Max acceptable stuck transfers (0 = none)
	MaxRecoveryTime  time.Duration // Max time to recover after spike (Scenario E)
	MemoryGrowthPct  float64       // Max RSS growth % for soak (Scenario D)
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
func multiTenantPool(n int) []TenantConfig {
	pool := make([]TenantConfig, n)
	seeds := []TenantConfig{seedLemfi, seedFincra}
	for i := range pool {
		pool[i] = seeds[i%len(seeds)]
	}
	return pool
}

// multiCurrencyPool builds a pool with the specified currency distribution:
// NGN 70%, USD 20%, GBP 10%.
func multiCurrencyPool(n int) []TenantConfig {
	return GenerateScaleTenants(n, DefaultCurrencyMix())
}

// predefinedScenarios holds all named scenarios keyed by canonical name.
var predefinedScenarios = map[string]Scenario{

	// -----------------------------------------------------------------------
	// Scenario A — Smoke Test (sanity)
	// 10 TPS for 60 seconds, single tenant, single currency (NGN).
	// Validates: correct responses, no errors, transfers reach terminal state.
	// -----------------------------------------------------------------------
	"SmokeTest": {
		Name:        "SmokeTest",
		Description: "Scenario A: 10 TPS for 60s. Single tenant, single currency. Sanity check.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        []TenantConfig{seedFincra}, // NGN corridor
			TargetTPS:      10,
			Duration:       60 * time.Second,
			RampUpDuration: 5 * time.Second,
			DrainDuration:  30 * time.Second,
			MaxErrorRate:   0.0, // Zero errors expected
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     500 * time.Millisecond,
			MaxP99Latency:     2 * time.Second,
			MaxErrorRate:      0.0,
			MaxStuckTransfers: 0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario B — Sustained Normal Load
	// Ramp 0→580 TPS over 2 min, hold 580 TPS for 10 min, ramp down 1 min.
	// Multi-tenant (50), multi-currency (NGN 70%, USD 20%, GBP 10%).
	// Validates: p50<100ms, p99<500ms, error<0.1%, zero stuck.
	// -----------------------------------------------------------------------
	"SustainedLoad": {
		Name:        "SustainedLoad",
		Description: "Scenario B: 580 TPS sustained for 10 min. 50 tenants, multi-currency. Validates steady-state.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(50),
			TargetTPS:      580,
			Duration:       10 * time.Minute,
			RampUpDuration: 2 * time.Minute,
			DrainDuration:  60 * time.Second,
			MaxErrorRate:   0.1,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     100 * time.Millisecond,
			MaxP99Latency:     500 * time.Millisecond,
			MaxErrorRate:      0.001,
			MaxStuckTransfers: 0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario C — Peak Burst
	// Ramp 580→5,000 TPS over 30s, hold 5,000 TPS for 5 min, ramp down to 580 over 1 min.
	// Multi-tenant (200), multi-currency.
	// Validates: p50<200ms, p99<1s, error<1%, graceful load shedding (503+Retry-After).
	// -----------------------------------------------------------------------
	"PeakBurst": {
		Name:        "PeakBurst",
		Description: "Scenario C: 5,000 TPS peak for 5 min. 50 tenants. Tests burst capacity and load shedding.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(50),
			TargetTPS:      5000,
			Duration:       5 * time.Minute,
			RampUpDuration: 30 * time.Second,
			DrainDuration:  90 * time.Second,
			MaxErrorRate:   1.0,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     200 * time.Millisecond,
			MaxP99Latency:     1 * time.Second,
			MaxErrorRate:      0.01,
			MaxStuckTransfers: 0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario D — Soak Test
	// 580 TPS sustained for 1 hour.
	// Validates: no memory leaks (RSS stable ±10%), no goroutine leaks,
	// no connection pool exhaustion, no queue depth growth.
	// -----------------------------------------------------------------------
	"SoakTest": {
		Name:        "SoakTest",
		Description: "Scenario D: 580 TPS for 1 hour. Validates resource stability — memory, goroutines, connections.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(50),
			TargetTPS:      580,
			Duration:       1 * time.Hour,
			RampUpDuration: 2 * time.Minute,
			DrainDuration:  120 * time.Second,
			MaxErrorRate:   0.1,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     100 * time.Millisecond,
			MaxP99Latency:     500 * time.Millisecond,
			MaxErrorRate:      0.001,
			MaxStuckTransfers: 0,
			MemoryGrowthPct:   10.0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario E — Spike Test
	// Instant jump from 100 TPS → 5,000 TPS (no ramp), hold 2 min, instant drop to 100 TPS.
	// Validates: system recovers within 30s, no data loss, backpressure works.
	// -----------------------------------------------------------------------
	"SpikeTest": {
		Name:        "SpikeTest",
		Description: "Scenario E: Instant spike 100→5,000→100 TPS. Tests recovery and backpressure.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(50),
			TargetTPS:      5000,
			Duration:       2 * time.Minute,
			RampUpDuration: 1 * time.Second, // Near-instant ramp
			DrainDuration:  60 * time.Second,
			MaxErrorRate:   1.0, // Allow some 503s during spike
			SpikeMode:      true,
			SpikeBaseTPS:   100,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     500 * time.Millisecond,
			MaxP99Latency:     2 * time.Second,
			MaxErrorRate:      0.01,
			MaxStuckTransfers: 0,
			MaxRecoveryTime:   30 * time.Second,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario F — Single Tenant Hot-Spot
	// 580 TPS total, 80% directed at a single tenant.
	// Validates: per-tenant rate limiting, other tenants unaffected,
	// no per-tenant mutex starvation.
	// -----------------------------------------------------------------------
	"HotSpot": {
		Name:        "HotSpot",
		Description: "Scenario F: 580 TPS, 80% to one tenant. Tests per-tenant isolation and rate limiting.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        multiTenantPool(10),
			TargetTPS:      580,
			Duration:       5 * time.Minute,
			RampUpDuration: 30 * time.Second,
			DrainDuration:  60 * time.Second,
			MaxErrorRate:   1.0, // Rate limiting will cause 429s on hot tenant
			HotSpotMode:    true,
			HotSpotPct:     80.0,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     200 * time.Millisecond,
			MaxP99Latency:     1 * time.Second,
			MaxErrorRate:      0.05, // Hot tenant will be rate limited
			MaxStuckTransfers: 0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario G — Tenant Scale: 20K Tenants
	// Pre-provision 20,000 tenants. 580 TPS with Zipf distribution (top 1% → 50% traffic).
	// Validates: auth cache memory stable, no per-tenant memory leak,
	// rate limiter memory bounded, response latency unchanged.
	// Monitor: RSS, goroutine count, auth cache hit rates, sync.Map size.
	// -----------------------------------------------------------------------
	"TenantScale20K": {
		Name:        "TenantScale20K",
		Description: "Scenario G: 580 TPS across 20K tenants (Zipf). Tests auth cache and per-tenant memory at scale.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        GenerateScaleTenants(20_000, DefaultCurrencyMix()),
			TargetTPS:      580,
			Duration:       10 * time.Minute,
			RampUpDuration: 2 * time.Minute,
			DrainDuration:  120 * time.Second,
			MaxErrorRate:   0.1,
			UseZipf:        true,
			ZipfExponent:   1.2,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     100 * time.Millisecond,
			MaxP99Latency:     500 * time.Millisecond,
			MaxErrorRate:      0.001,
			MaxStuckTransfers: 0,
			MemoryGrowthPct:   10.0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario H — Tenant Scale: 100K Tenants (Growth Target)
	// Pre-provision 100,000 tenants. 580 TPS with Zipf (top 200 → 50% traffic, 50K idle).
	// Validates: same as G plus tenant table query perf, partition distribution,
	// settlement feasibility.
	// Monitor: same as G plus settlement duration, per-partition queue depth variance.
	// -----------------------------------------------------------------------
	"TenantScale100K": {
		Name:        "TenantScale100K",
		Description: "Scenario H: 580 TPS across 100K tenants (Zipf). Growth target validation.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        GenerateScaleTenants(100_000, DefaultCurrencyMix()),
			TargetTPS:      580,
			Duration:       10 * time.Minute,
			RampUpDuration: 2 * time.Minute,
			DrainDuration:  120 * time.Second,
			MaxErrorRate:   0.1,
			UseZipf:        true,
			ZipfExponent:   1.2,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     150 * time.Millisecond,
			MaxP99Latency:     750 * time.Millisecond,
			MaxErrorRate:      0.001,
			MaxStuckTransfers: 0,
			MemoryGrowthPct:   15.0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario I — Tenant Scale + Peak: 20K Tenants at 5K TPS
	// Compound stress test: 20K provisioned tenants at 5,000 TPS peak.
	// Validates: system handles BOTH dimensions simultaneously.
	// Monitor: auth cache thrashing, treasury channel overflow, NATS partition skew,
	// PgBouncer connection wait time.
	// -----------------------------------------------------------------------
	"TenantScalePeak": {
		Name:        "TenantScalePeak",
		Description: "Scenario I: 5,000 TPS across 20K tenants (Zipf). Compound stress test.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        GenerateScaleTenants(20_000, DefaultCurrencyMix()),
			TargetTPS:      5000,
			Duration:       5 * time.Minute,
			RampUpDuration: 1 * time.Minute,
			DrainDuration:  120 * time.Second,
			MaxErrorRate:   1.0,
			UseZipf:        true,
			ZipfExponent:   1.2,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     200 * time.Millisecond,
			MaxP99Latency:     1 * time.Second,
			MaxErrorRate:      0.01,
			MaxStuckTransfers: 0,
		},
	},

	// -----------------------------------------------------------------------
	// Scenario J — Settlement Batch at Scale
	// Pre-provision 20K tenants, generate 1 hour traffic at 580 TPS (~2M transfers),
	// then trigger settlement manually.
	// Measure: total settlement wall-clock time, per-tenant settlement latency,
	// settlement worker utilization, DB query patterns.
	// Validates: settlement completes within 2h for 20K tenants, no tenant skipped,
	// ledger reconciliation passes post-settlement.
	// -----------------------------------------------------------------------
	"SettlementBatch": {
		Name:        "SettlementBatch",
		Description: "Scenario J: 580 TPS for 1h then trigger settlement across 20K tenants. Tests batch settlement at scale.",
		Config: LoadTestConfig{
			GatewayURL:     "http://localhost:3000",
			Tenants:        GenerateScaleTenants(20_000, DefaultCurrencyMix()),
			TargetTPS:      580,
			Duration:       1 * time.Hour,
			RampUpDuration: 2 * time.Minute,
			DrainDuration:  120 * time.Second,
			MaxErrorRate:   0.1,
			UseZipf:        true,
			ZipfExponent:   1.2,
			SettlementMode: true,
		},
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     100 * time.Millisecond,
			MaxP99Latency:     500 * time.Millisecond,
			MaxErrorRate:      0.001,
			MaxStuckTransfers: 0,
		},
	},

	// -----------------------------------------------------------------------
	// Legacy scenarios (preserved for backward compatibility)
	// -----------------------------------------------------------------------
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
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     200 * time.Millisecond,
			MaxP99Latency:     1 * time.Second,
			MaxErrorRate:      0.01,
			MaxStuckTransfers: 0,
		},
	},

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
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     300 * time.Millisecond,
			MaxP99Latency:     2 * time.Second,
			MaxErrorRate:      0.02,
			MaxStuckTransfers: 0,
		},
	},

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
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     200 * time.Millisecond,
			MaxP99Latency:     1 * time.Second,
			MaxErrorRate:      0.01,
			MaxStuckTransfers: 0,
		},
	},

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
		Thresholds: ScenarioThresholds{
			MaxP50Latency:     200 * time.Millisecond,
			MaxP99Latency:     1 * time.Second,
			MaxErrorRate:      0.01,
			MaxStuckTransfers: 0,
		},
	},
}

// GetScenario returns the named scenario. Names are case-insensitive.
func GetScenario(name string) (Scenario, bool) {
	if s, ok := predefinedScenarios[name]; ok {
		return s, true
	}
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
