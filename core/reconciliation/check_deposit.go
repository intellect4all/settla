package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DepositQuerier queries the deposit subsystem for reconciliation metrics.
type DepositQuerier interface {
	// CountStuckDepositSessions returns sessions in a non-terminal status
	// that have not been updated since the given cutoff.
	CountStuckDepositSessions(ctx context.Context, olderThan time.Time) (int, error)
	// CountStaleBlockCheckpoints returns chains whose block checkpoint has
	// not advanced since the given cutoff — indicates a stalled monitor.
	CountStaleBlockCheckpoints(ctx context.Context, olderThan time.Time) (int, error)
	// CountAvailablePoolAddressesAll returns the total number of available
	// (undispensed) pool addresses across all tenants and chains.
	CountAvailablePoolAddressesAll(ctx context.Context) (int, error)
	// CountDepositTxAmountMismatches returns the number of sessions where
	// the sum of confirmed transaction amounts differs from received_amount.
	CountDepositTxAmountMismatches(ctx context.Context) (int, error)
}

// DepositCheck verifies the health of the crypto deposit subsystem by checking
// for stuck sessions, stale chain monitors, depleted address pools, and
// transaction-amount mismatches.
type DepositCheck struct {
	store              DepositQuerier
	logger             *slog.Logger
	stuckThreshold     time.Duration
	checkpointMaxAge   time.Duration
	poolAlertThreshold int
}

// NewDepositCheck creates a DepositCheck with sensible defaults.
// Pass zero values to use defaults: stuckThreshold=10min, checkpointMaxAge=5min,
// poolAlertThreshold=50.
func NewDepositCheck(store DepositQuerier, logger *slog.Logger, stuckThreshold, checkpointMaxAge time.Duration, poolAlertThreshold int) *DepositCheck {
	if stuckThreshold == 0 {
		stuckThreshold = 10 * time.Minute
	}
	if checkpointMaxAge == 0 {
		checkpointMaxAge = 5 * time.Minute
	}
	if poolAlertThreshold == 0 {
		poolAlertThreshold = 50
	}
	return &DepositCheck{
		store:              store,
		logger:             logger,
		stuckThreshold:     stuckThreshold,
		checkpointMaxAge:   checkpointMaxAge,
		poolAlertThreshold: poolAlertThreshold,
	}
}

// Name returns the check identifier.
func (c *DepositCheck) Name() string {
	return "deposit_health"
}

// Run executes all deposit health checks and aggregates results.
func (c *DepositCheck) Run(ctx context.Context) (*CheckResult, error) {
	var issues int
	var details []string

	// 1. Stuck sessions — sessions in non-terminal status with no update
	stuckCutoff := time.Now().UTC().Add(-c.stuckThreshold)
	stuck, err := c.store.CountStuckDepositSessions(ctx, stuckCutoff)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting stuck deposit sessions: %w", err)
	}
	if stuck > 0 {
		issues += stuck
		details = append(details, fmt.Sprintf(
			"%d deposit sessions stuck > %s", stuck, c.stuckThreshold,
		))
		c.logger.Warn("settla-reconciliation: stuck deposit sessions",
			slog.Int("count", stuck),
			slog.Duration("threshold", c.stuckThreshold),
		)
	}

	// 2. Stale block checkpoints — chain monitor may be down
	checkpointCutoff := time.Now().UTC().Add(-c.checkpointMaxAge)
	staleCheckpoints, err := c.store.CountStaleBlockCheckpoints(ctx, checkpointCutoff)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting stale block checkpoints: %w", err)
	}
	if staleCheckpoints > 0 {
		issues += staleCheckpoints
		details = append(details, fmt.Sprintf(
			"%d chains with stale block checkpoints (> %s)", staleCheckpoints, c.checkpointMaxAge,
		))
		c.logger.Warn("settla-reconciliation: stale block checkpoints",
			slog.Int("count", staleCheckpoints),
			slog.Duration("max_age", c.checkpointMaxAge),
		)
	}

	// 3. Address pool depletion — running low on pre-derived addresses
	available, err := c.store.CountAvailablePoolAddressesAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting available pool addresses: %w", err)
	}
	if available < c.poolAlertThreshold {
		issues++
		details = append(details, fmt.Sprintf(
			"address pool low: %d available (threshold %d)", available, c.poolAlertThreshold,
		))
		c.logger.Warn("settla-reconciliation: address pool depleting",
			slog.Int("available", available),
			slog.Int("threshold", c.poolAlertThreshold),
		)
	}

	// 4. Transaction-amount mismatches — received_amount != sum(confirmed tx amounts)
	mismatches, err := c.store.CountDepositTxAmountMismatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting deposit tx-amount mismatches: %w", err)
	}
	if mismatches > 0 {
		issues += mismatches
		details = append(details, fmt.Sprintf(
			"%d sessions with tx-amount mismatches", mismatches,
		))
		c.logger.Warn("settla-reconciliation: deposit tx-amount mismatches",
			slog.Int("count", mismatches),
		)
	}

	status := "pass"
	if issues > 0 {
		status = "fail"
	}

	return &CheckResult{
		Name:       c.Name(),
		Status:     status,
		Details:    strings.Join(details, "; "),
		Mismatches: issues,
		CheckedAt:  time.Now().UTC(),
	}, nil
}
