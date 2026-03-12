package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// SettlementRecord is the minimal view of a net settlement needed for fee reconciliation.
type SettlementRecord struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	PeriodStart  time.Time
	PeriodEnd    time.Time
	TotalFeesUSD decimal.Decimal
}

// SettlementFeeStore is the narrow interface required by SettlementFeeCheck.
// It intentionally avoids importing transferdb or settlement packages directly,
// keeping the check decoupled from any single implementation.
type SettlementFeeStore interface {
	// GetLatestNetSettlement returns the most recently created net settlement
	// across all tenants, or (nil, nil) if none exists.
	GetLatestNetSettlement(ctx context.Context) (*SettlementRecord, error)

	// SumCompletedTransferFeesUSD returns the sum of FeeBreakdown.TotalFeeUSD
	// for all completed transfers for the given tenant in [start, end).
	// The sum is expressed in USD using shopspring/decimal precision.
	SumCompletedTransferFeesUSD(ctx context.Context, tenantID uuid.UUID, start, end time.Time) (decimal.Decimal, error)
}

// SettlementFeeCheck verifies that the total fees recorded in net_settlements
// match the sum of per-transfer fees from completed transfers for the same period.
//
// Rationale: the settlement calculator sums fees from transfer rows at calculation
// time and writes the result to net_settlements.total_fees_usd. If a transfer
// row is later amended, retried, or a fee rounding inconsistency exists, the two
// totals diverge silently. This check detects that drift.
//
// The check queries the most recent completed settlement period and compares
// the settlement-recorded fee total against a fresh summation over transfers.
// A discrepancy greater than the configured tolerance (default 0.01 USD) is
// reported as a failure.
type SettlementFeeCheck struct {
	store     SettlementFeeStore
	logger    *slog.Logger
	tolerance decimal.Decimal
}

// NewSettlementFeeCheck creates a SettlementFeeCheck.
// Pass decimal.Zero for tolerance to use the default of 0.01 USD.
func NewSettlementFeeCheck(store SettlementFeeStore, logger *slog.Logger, tolerance decimal.Decimal) *SettlementFeeCheck {
	if tolerance.IsZero() || tolerance.IsNegative() {
		tolerance = decimal.NewFromFloat(0.01)
	}
	return &SettlementFeeCheck{
		store:     store,
		logger:    logger,
		tolerance: tolerance,
	}
}

// Name returns the check identifier.
func (c *SettlementFeeCheck) Name() string {
	return "settlement_fee_reconciliation"
}

// Run fetches the most recent net settlement and compares its recorded fee total
// against the sum of per-transfer fees for the same period and tenant.
func (c *SettlementFeeCheck) Run(ctx context.Context) (*CheckResult, error) {
	settlement, err := c.store.GetLatestNetSettlement(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: fetching latest net settlement: %w", err)
	}

	// If no settlement has been run yet, there is nothing to compare.
	if settlement == nil {
		return &CheckResult{
			Name:      c.Name(),
			Status:    "pass",
			Details:   "no net settlements found; skipping fee reconciliation",
			CheckedAt: time.Now().UTC(),
		}, nil
	}

	transferFees, err := c.store.SumCompletedTransferFeesUSD(
		ctx,
		settlement.TenantID,
		settlement.PeriodStart,
		settlement.PeriodEnd,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"settla-reconciliation: summing transfer fees for settlement %s (tenant=%s period=[%s,%s)): %w",
			settlement.ID, settlement.TenantID,
			settlement.PeriodStart.Format(time.RFC3339),
			settlement.PeriodEnd.Format(time.RFC3339),
			err,
		)
	}

	diff := transferFees.Sub(settlement.TotalFeesUSD).Abs()

	c.logger.Info("settla-reconciliation: settlement fee check",
		slog.String("settlement_id", settlement.ID.String()),
		slog.String("tenant_id", settlement.TenantID.String()),
		slog.String("period_start", settlement.PeriodStart.Format(time.RFC3339)),
		slog.String("period_end", settlement.PeriodEnd.Format(time.RFC3339)),
		slog.String("settlement_fees_usd", settlement.TotalFeesUSD.StringFixed(6)),
		slog.String("transfer_fees_usd", transferFees.StringFixed(6)),
		slog.String("diff_usd", diff.StringFixed(6)),
		slog.String("tolerance_usd", c.tolerance.StringFixed(6)),
	)

	if diff.GreaterThan(c.tolerance) {
		details := fmt.Sprintf(
			"settlement_id=%s tenant=%s period=[%s,%s): settlement_fees=%s transfer_fees=%s diff=%s tolerance=%s",
			settlement.ID,
			settlement.TenantID,
			settlement.PeriodStart.Format(time.RFC3339),
			settlement.PeriodEnd.Format(time.RFC3339),
			settlement.TotalFeesUSD.StringFixed(6),
			transferFees.StringFixed(6),
			diff.StringFixed(6),
			c.tolerance.StringFixed(6),
		)
		c.logger.Warn("settla-reconciliation: settlement fee discrepancy detected",
			slog.String("settlement_id", settlement.ID.String()),
			slog.String("diff_usd", diff.StringFixed(6)),
		)
		return &CheckResult{
			Name:       c.Name(),
			Status:     "fail",
			Details:    details,
			Mismatches: 1,
			CheckedAt:  time.Now().UTC(),
		}, nil
	}

	return &CheckResult{
		Name: c.Name(),
		Status: "pass",
		Details: fmt.Sprintf(
			"settlement_id=%s fees match: settlement=%s transfer_sum=%s diff=%s",
			settlement.ID,
			settlement.TotalFeesUSD.StringFixed(6),
			transferFees.StringFixed(6),
			diff.StringFixed(6),
		),
		CheckedAt: time.Now().UTC(),
	}, nil
}
