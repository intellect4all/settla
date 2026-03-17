package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// BankDepositQuerier queries the bank deposit subsystem for reconciliation metrics.
type BankDepositQuerier interface {
	// CountStuckBankDepositSessions returns sessions in PENDING_PAYMENT status
	// that have not been updated since the given cutoff (stuck waiting for payment).
	CountStuckBankDepositSessions(ctx context.Context, olderThan time.Time) (int, error)
	// CountStuckBankDepositCrediting returns sessions in CREDITING status
	// that have not advanced since the given cutoff (stuck crediting).
	CountStuckBankDepositCrediting(ctx context.Context, olderThan time.Time) (int, error)
	// CountOrphanedVirtualAccounts returns the number of virtual accounts marked as
	// unavailable whose linked session is in a terminal state (the account should
	// have been recycled).
	CountOrphanedVirtualAccounts(ctx context.Context) (int, error)
}

// BankDepositCheck verifies the health of the bank deposit subsystem by checking
// for stuck sessions, stuck crediting operations, and orphaned virtual accounts.
type BankDepositCheck struct {
	store                      BankDepositQuerier
	logger                     *slog.Logger
	stuckSessionsThresholdMin  time.Duration
	stuckCreditingThresholdMin time.Duration
}

// NewBankDepositCheck creates a BankDepositCheck with sensible defaults.
// Pass zero values to use defaults: stuckSessionsMinutes=30min, stuckCreditingMinutes=5min.
func NewBankDepositCheck(store BankDepositQuerier, logger *slog.Logger, stuckSessionsMinutes, stuckCreditingMinutes int) *BankDepositCheck {
	stuckSessions := time.Duration(stuckSessionsMinutes) * time.Minute
	if stuckSessions == 0 {
		stuckSessions = 30 * time.Minute
	}
	stuckCrediting := time.Duration(stuckCreditingMinutes) * time.Minute
	if stuckCrediting == 0 {
		stuckCrediting = 5 * time.Minute
	}
	return &BankDepositCheck{
		store:                      store,
		logger:                     logger,
		stuckSessionsThresholdMin:  stuckSessions,
		stuckCreditingThresholdMin: stuckCrediting,
	}
}

// Name returns the check identifier.
func (c *BankDepositCheck) Name() string {
	return "bank_deposit"
}

// Run executes all bank deposit health checks and aggregates results.
func (c *BankDepositCheck) Run(ctx context.Context) (*CheckResult, error) {
	var issues int
	var details []string

	// 1. Stuck sessions — PENDING_PAYMENT sessions older than expected TTL
	stuckCutoff := time.Now().UTC().Add(-c.stuckSessionsThresholdMin)
	stuck, err := c.store.CountStuckBankDepositSessions(ctx, stuckCutoff)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting stuck bank deposit sessions: %w", err)
	}
	if stuck > 0 {
		issues += stuck
		details = append(details, fmt.Sprintf(
			"%d bank deposit sessions stuck in PENDING_PAYMENT > %s", stuck, c.stuckSessionsThresholdMin,
		))
		c.logger.Warn("settla-reconciliation: stuck bank deposit sessions",
			slog.Int("count", stuck),
			slog.Duration("threshold", c.stuckSessionsThresholdMin),
		)
	}

	// 2. Stuck crediting — CREDITING sessions not advancing within threshold
	creditingCutoff := time.Now().UTC().Add(-c.stuckCreditingThresholdMin)
	stuckCrediting, err := c.store.CountStuckBankDepositCrediting(ctx, creditingCutoff)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting stuck bank deposit crediting: %w", err)
	}
	if stuckCrediting > 0 {
		issues += stuckCrediting
		details = append(details, fmt.Sprintf(
			"%d bank deposit sessions stuck in CREDITING > %s", stuckCrediting, c.stuckCreditingThresholdMin,
		))
		c.logger.Warn("settla-reconciliation: stuck bank deposit crediting",
			slog.Int("count", stuckCrediting),
			slog.Duration("threshold", c.stuckCreditingThresholdMin),
		)
	}

	// 3. Orphaned virtual accounts — marked unavailable but session is terminal
	orphaned, err := c.store.CountOrphanedVirtualAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting orphaned virtual accounts: %w", err)
	}
	if orphaned > 0 {
		issues += orphaned
		details = append(details, fmt.Sprintf(
			"%d orphaned virtual accounts (unavailable but session is terminal)", orphaned,
		))
		c.logger.Warn("settla-reconciliation: orphaned virtual accounts",
			slog.Int("count", orphaned),
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
