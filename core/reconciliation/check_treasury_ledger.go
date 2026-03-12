package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// TenantLister retrieves all active tenant IDs for reconciliation.
type TenantLister interface {
	ListActiveTenantIDs(ctx context.Context) ([]uuid.UUID, error)
}

// TenantSlugResolver maps tenant UUIDs to slugs for account code construction.
type TenantSlugResolver interface {
	GetTenantSlug(ctx context.Context, tenantID uuid.UUID) (string, error)
}

// LedgerQuerier reads account balances from the ledger.
type LedgerQuerier interface {
	GetAccountBalance(ctx context.Context, accountCode string) (decimal.Decimal, error)
}

// TreasuryLedgerCheck compares treasury position balances against ledger account
// balances for each tenant/currency/location. Any mismatch > tolerance
// is recorded as a failure.
type TreasuryLedgerCheck struct {
	treasury     domain.TreasuryManager
	ledger       LedgerQuerier
	tenants      TenantLister
	slugResolver TenantSlugResolver
	logger       *slog.Logger
	tolerance    decimal.Decimal
}

// NewTreasuryLedgerCheck creates a TreasuryLedgerCheck with the given tolerance.
// The tolerance parameter controls the maximum acceptable difference between
// treasury and ledger balances before a mismatch is recorded.
func NewTreasuryLedgerCheck(
	treasury domain.TreasuryManager,
	ledger LedgerQuerier,
	tenants TenantLister,
	slugResolver TenantSlugResolver,
	logger *slog.Logger,
	tolerance decimal.Decimal,
) *TreasuryLedgerCheck {
	return &TreasuryLedgerCheck{
		treasury:     treasury,
		ledger:       ledger,
		tenants:      tenants,
		slugResolver: slugResolver,
		logger:       logger,
		tolerance:    tolerance,
	}
}

// Name returns the check identifier.
func (c *TreasuryLedgerCheck) Name() string {
	return "treasury_ledger_balance"
}

// Run compares treasury positions with ledger balances for all active tenants.
func (c *TreasuryLedgerCheck) Run(ctx context.Context) (*CheckResult, error) {
	tenantIDs, err := c.tenants.ListActiveTenantIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: listing tenants: %w", err)
	}

	var mismatches int
	var details []string

	for _, tenantID := range tenantIDs {
		slug, err := c.slugResolver.GetTenantSlug(ctx, tenantID)
		if err != nil {
			c.logger.Warn("settla-reconciliation: failed to resolve tenant slug, falling back to UUID",
				slog.String("tenant_id", tenantID.String()),
				slog.String("error", err.Error()),
			)
			slug = tenantID.String()
		}

		positions, err := c.treasury.GetPositions(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("settla-reconciliation: getting positions for tenant %s: %w", tenantID, err)
		}

		for _, pos := range positions {
			accountCode := buildAccountCode(slug, pos)
			ledgerBalance, err := c.ledger.GetAccountBalance(ctx, accountCode)
			if err != nil {
				c.logger.Warn("settla-reconciliation: ledger balance lookup failed",
					slog.String("account_code", accountCode),
					slog.String("error", err.Error()),
				)
				mismatches++
				details = append(details, fmt.Sprintf(
					"tenant=%s account=%s: ledger query error: %v",
					tenantID, accountCode, err,
				))
				continue
			}

			treasuryBalance := pos.Balance
			diff := treasuryBalance.Sub(ledgerBalance).Abs()
			if diff.GreaterThan(c.tolerance) {
				mismatches++
				details = append(details, fmt.Sprintf(
					"tenant=%s account=%s: treasury=%s ledger=%s diff=%s",
					tenantID, accountCode,
					treasuryBalance.StringFixed(2),
					ledgerBalance.StringFixed(2),
					diff.StringFixed(2),
				))
			}
		}
	}

	status := "pass"
	if mismatches > 0 {
		status = "fail"
	}

	return &CheckResult{
		Name:       c.Name(),
		Status:     status,
		Details:    strings.Join(details, "; "),
		Mismatches: mismatches,
		CheckedAt:  time.Now().UTC(),
	}, nil
}

// buildAccountCode derives the ledger account code from a treasury position.
// Format: tenant:{slug}:assets:bank:{currency}:{location}
func buildAccountCode(slug string, pos domain.Position) string {
	return fmt.Sprintf("tenant:%s:assets:bank:%s:%s",
		slug,
		strings.ToLower(string(pos.Currency)),
		pos.Location,
	)
}
