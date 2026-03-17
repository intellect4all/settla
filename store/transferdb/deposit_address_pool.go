package transferdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// HDWalletDeriver generates deterministic addresses from a derivation index.
// This is implemented by the signing/wallet service and injected at startup.
type HDWalletDeriver interface {
	// DeriveAddress returns the deposit address for a given tenant, chain, and derivation index.
	DeriveAddress(ctx context.Context, tenantID uuid.UUID, chain string, index int64) (string, error)
}

// PoolConfig holds tuning parameters for the address pool.
type PoolConfig struct {
	// TargetSize is the desired number of undispensed addresses per (tenant, chain).
	TargetSize int
	// RefillThreshold is the count below which a refill is triggered.
	RefillThreshold int
	// AlertThreshold is the count below which an alert is emitted.
	AlertThreshold int
}

// DefaultPoolConfig returns sensible defaults for production.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		TargetSize:      1000,
		RefillThreshold: 500,
		AlertThreshold:  50,
	}
}

// AddressPoolManager manages the pre-generated address pool for crypto deposits.
// It checks pool levels and refills them using an HD wallet deriver.
type AddressPoolManager struct {
	q      *Queries
	deriver HDWalletDeriver
	cfg     PoolConfig
	logger  *slog.Logger
}

// NewAddressPoolManager creates a pool manager.
func NewAddressPoolManager(q *Queries, deriver HDWalletDeriver, cfg PoolConfig, logger *slog.Logger) *AddressPoolManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &AddressPoolManager{q: q, deriver: deriver, cfg: cfg, logger: logger}
}

// RefillIfNeeded checks the available address count for (tenant, chain) and
// refills up to TargetSize if below RefillThreshold. Returns the number of
// addresses generated.
func (m *AddressPoolManager) RefillIfNeeded(ctx context.Context, tenantID uuid.UUID, chain string) (int, error) {
	count, err := m.q.CountAvailablePoolAddresses(ctx, CountAvailablePoolAddressesParams{
		TenantID: tenantID,
		Chain:    chain,
	})
	if err != nil {
		return 0, fmt.Errorf("settla-pool: counting available addresses: %w", err)
	}

	if int(count) <= m.cfg.AlertThreshold {
		m.logger.Warn("settla-pool: address pool critically low",
			"tenant_id", tenantID,
			"chain", chain,
			"available", count,
			"alert_threshold", m.cfg.AlertThreshold,
		)
	}

	if int(count) >= m.cfg.RefillThreshold {
		return 0, nil // pool is healthy
	}

	needed := m.cfg.TargetSize - int(count)
	generated := 0

	for range needed {
		// Atomically get next derivation index.
		idx, err := m.q.IncrementDerivationCounter(ctx, IncrementDerivationCounterParams{
			TenantID: tenantID,
			Chain:    chain,
		})
		if err != nil {
			return generated, fmt.Errorf("settla-pool: incrementing derivation counter: %w", err)
		}

		addr, err := m.deriver.DeriveAddress(ctx, tenantID, chain, int64(idx))
		if err != nil {
			return generated, fmt.Errorf("settla-pool: deriving address at index %d: %w", idx, err)
		}

		if _, err := m.q.InsertPoolAddress(ctx, InsertPoolAddressParams{
			TenantID:        tenantID,
			Chain:           chain,
			Address:         addr,
			DerivationIndex: int64(idx),
		}); err != nil {
			return generated, fmt.Errorf("settla-pool: inserting address at index %d: %w", idx, err)
		}

		generated++
	}

	m.logger.Info("settla-pool: refilled address pool",
		"tenant_id", tenantID,
		"chain", chain,
		"generated", generated,
		"new_total", int(count)+generated,
	)

	return generated, nil
}

// CountAvailable returns the number of undispensed addresses for (tenant, chain).
func (m *AddressPoolManager) CountAvailable(ctx context.Context, tenantID uuid.UUID, chain string) (int64, error) {
	return m.q.CountAvailablePoolAddresses(ctx, CountAvailablePoolAddressesParams{
		TenantID: tenantID,
		Chain:    chain,
	})
}
