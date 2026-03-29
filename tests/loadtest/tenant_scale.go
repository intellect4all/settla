package main

import (
	"fmt"
	"math/rand"
)

// TenantScaleConfig configures bulk tenant generation for scale tests.
type TenantScaleConfig struct {
	Count        int    // Number of tenants to generate
	IDPrefix     string // UUID prefix pattern (default: "t" + zero-padded index)
	KeyPrefix    string // API key prefix (default: "sk_live_scale_")
	CurrencyMix []CurrencyWeight
}

// CurrencyWeight defines a currency and its probability in the tenant mix.
type CurrencyWeight struct {
	Currency string
	Country  string
	Weight   float64 // Relative weight (will be normalized)
}

// DefaultCurrencyMix returns the standard multi-currency distribution:
// NGN 70%, USD 20%, GBP 10%.
func DefaultCurrencyMix() []CurrencyWeight {
	return []CurrencyWeight{
		{Currency: "NGN", Country: "GB", Weight: 0.70},
		{Currency: "GBP", Country: "NG", Weight: 0.10},
		{Currency: "USD", Country: "NG", Weight: 0.20},
	}
}

// GenerateScaleTenants creates n tenant configs with deterministic IDs and API keys.
// IDs are formatted as UUIDs: tNNNNNNN-0000-0000-0000-000000000000
// API keys are: sk_live_scale_NNNNNNN
// Currency assignment follows the provided mix weights.
func GenerateScaleTenants(n int, mix []CurrencyWeight) []TenantConfig {
	if len(mix) == 0 {
		mix = DefaultCurrencyMix()
	}

	// Normalize weights
	totalWeight := 0.0
	for _, m := range mix {
		totalWeight += m.Weight
	}

	// Build cumulative distribution for currency assignment
	cumul := make([]float64, len(mix))
	cumSum := 0.0
	for i, m := range mix {
		cumSum += m.Weight / totalWeight
		cumul[i] = cumSum
	}
	cumul[len(cumul)-1] = 1.0

	tenants := make([]TenantConfig, n)
	// Use deterministic seed for reproducibility
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < n; i++ {
		// Deterministic UUID based on index
		id := fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1)

		// API key: sk_live_scale_NNNNNNN
		apiKey := fmt.Sprintf("sk_live_scale_%07d", i+1)

		// Assign currency based on mix
		r := rng.Float64()
		currIdx := 0
		for j, c := range cumul {
			if r <= c {
				currIdx = j
				break
			}
		}

		tenants[i] = TenantConfig{
			ID:       id,
			APIKey:   apiKey,
			Currency: mix[currIdx].Currency,
			Country:  mix[currIdx].Country,
		}
	}

	return tenants
}

// GenerateScaleTenantSQL generates SQL INSERT statements for bulk tenant provisioning.
// Uses multi-row INSERT for efficiency (batches of 1000).
// Returns: tenant SQL, api_key SQL, treasury SQL.
func GenerateScaleTenantSQL(tenants []TenantConfig) (tenantSQL, apiKeySQL, treasurySQL string) {
	const batchSize = 1000

	tenantSQL = "-- Auto-generated scale test tenants\nBEGIN;\n"
	apiKeySQL = "-- Auto-generated scale test API keys\nBEGIN;\n"
	treasurySQL = "-- Auto-generated scale test treasury positions\nBEGIN;\n"

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(tenants) {
			batchEnd = len(tenants)
		}
		batch := tenants[batchStart:batchEnd]

		// Tenants INSERT
		tenantSQL += "INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model, daily_limit_usd, per_transfer_limit, kyb_status, kyb_verified_at, created_at, updated_at) VALUES\n"
		for i, t := range batch {
			slug := fmt.Sprintf("scale-tenant-%s", t.ID[:8])
			name := fmt.Sprintf("Scale Tenant %s", t.ID[:8])
			feeJSON := `{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00", "crypto_collection_bps": 50, "crypto_collection_max_fee_usd": "100.00", "bank_collection_bps": 30, "bank_collection_min_fee_usd": "1.00", "bank_collection_max_fee_usd": "200.00"}`
			comma := ","
			if i == len(batch)-1 {
				comma = ""
			}
			tenantSQL += fmt.Sprintf("('%s', '%s', '%s', 'ACTIVE', '%s', 'PREFUNDED', 9999999999, 1000000, 'VERIFIED', NOW(), NOW(), NOW())%s\n",
				t.ID, name, slug, feeJSON, comma)
		}
		tenantSQL += "ON CONFLICT (id) DO NOTHING;\n\n"

		// API keys INSERT
		apiKeySQL += "INSERT INTO api_keys (id, tenant_id, key_hash, key_prefix, environment, name, is_active, created_at) VALUES\n"
		for i, t := range batch {
			keyHash := hashLoadtestAPIKey(t.APIKey)
			keyID := fmt.Sprintf("%08x-0000-4000-a000-%012x", batchStart+i+1, batchStart+i+1)
			comma := ","
			if i == len(batch)-1 {
				comma = ""
			}
			apiKeySQL += fmt.Sprintf("('%s', '%s', '%s', '%s', 'LIVE', 'scale-test-key', true, NOW())%s\n",
				keyID, t.ID, keyHash, t.APIKey[:14], comma)
		}
		apiKeySQL += "ON CONFLICT (id) DO NOTHING;\n\n"

		// Treasury positions INSERT (one per tenant per currency)
		treasurySQL += "INSERT INTO positions (tenant_id, currency, location, available, reserved, total, min_balance, target_balance, updated_at) VALUES\n"
		rowCount := 0
		for i, t := range batch {
			_ = i
			currencies := []struct{ currency, location string }{
				{t.Currency, fmt.Sprintf("bank:%s", toLower(t.Currency))},
				{"USDT", "chain:tron"},
			}
			for j, c := range currencies {
				comma := ","
				if i == len(batch)-1 && j == len(currencies)-1 {
					comma = ""
				}
				treasurySQL += fmt.Sprintf("('%s', '%s', '%s', 999999999, 0, 999999999, 100000, 500000, NOW())%s\n",
					t.ID, c.currency, c.location, comma)
				rowCount++
			}
		}
		treasurySQL += "ON CONFLICT DO NOTHING;\n\n"
	}

	tenantSQL += "COMMIT;\n"
	apiKeySQL += "COMMIT;\n"
	treasurySQL += "COMMIT;\n"

	return tenantSQL, apiKeySQL, treasurySQL
}

// toLower is a simple ASCII lowercase helper to avoid importing strings.
func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// HotspotTenantPool creates a tenant pool where hotPct% of traffic goes to
// the first tenant. Used for Scenario F (single tenant hot-spot).
func HotspotTenantPool(tenants []TenantConfig, hotPct float64) (*HotspotSelector, error) {
	if len(tenants) < 2 {
		return nil, fmt.Errorf("hotspot pool requires at least 2 tenants, got %d", len(tenants))
	}
	return &HotspotSelector{
		tenants: tenants,
		hotPct:  hotPct,
	}, nil
}

// HotspotSelector distributes traffic with a configurable hotspot.
type HotspotSelector struct {
	tenants []TenantConfig
	hotPct  float64
}

// Select returns a tenant, with hotPct probability of returning the first (hot) tenant.
func (h *HotspotSelector) Select() TenantConfig {
	if rand.Float64() < h.hotPct/100.0 {
		return h.tenants[0]
	}
	// Distribute remaining traffic uniformly across non-hot tenants
	idx := 1 + rand.Intn(len(h.tenants)-1)
	return h.tenants[idx]
}
