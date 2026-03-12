package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ProviderTxQuerier counts pending provider transactions older than a threshold.
type ProviderTxQuerier interface {
	CountPendingProviderTxOlderThan(ctx context.Context, olderThan time.Time) (int, error)
}

// ProviderTxCheck counts provider transactions stuck in "pending" status beyond
// a configurable threshold. This is a lightweight check that does not call
// external providers -- it only queries the local database.
type ProviderTxCheck struct {
	store         ProviderTxQuerier
	logger        *slog.Logger
	maxPendingAge time.Duration
}

// NewProviderTxCheck creates a ProviderTxCheck. Pass 0 for maxPendingAge to
// use the default of 1 hour.
func NewProviderTxCheck(store ProviderTxQuerier, logger *slog.Logger, maxPendingAge time.Duration) *ProviderTxCheck {
	if maxPendingAge == 0 {
		maxPendingAge = 1 * time.Hour
	}
	return &ProviderTxCheck{
		store:         store,
		logger:        logger,
		maxPendingAge: maxPendingAge,
	}
}

// Name returns the check identifier.
func (c *ProviderTxCheck) Name() string {
	return "provider_tx_reconciliation"
}

// Run counts pending provider transactions older than the configured threshold.
// Returns "warn" if any are found (since providers may be slow but not necessarily broken).
func (c *ProviderTxCheck) Run(ctx context.Context) (*CheckResult, error) {
	cutoff := time.Now().UTC().Add(-c.maxPendingAge)

	count, err := c.store.CountPendingProviderTxOlderThan(ctx, cutoff)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting pending provider txs: %w", err)
	}

	status := "pass"
	details := ""

	if count > 0 {
		status = "warn"
		details = fmt.Sprintf("%d pending provider transactions older than %s", count, c.maxPendingAge)
		c.logger.Warn("settla-reconciliation: stale pending provider transactions",
			slog.Int("count", count),
			slog.Duration("max_pending_age", c.maxPendingAge),
		)
	}

	return &CheckResult{
		Name:       c.Name(),
		Status:     status,
		Details:    details,
		Mismatches: count,
		CheckedAt:  time.Now().UTC(),
	}, nil
}
