package ledgerdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
)

// LedgerReconciliationAdapter implements reconciliation.LedgerQuerier against
// the Ledger DB using SQLC-generated queries.
type LedgerReconciliationAdapter struct {
	q *Queries
}

// NewLedgerReconciliationAdapter creates a new LedgerReconciliationAdapter.
func NewLedgerReconciliationAdapter(q *Queries) *LedgerReconciliationAdapter {
	return &LedgerReconciliationAdapter{q: q}
}

// GetAccountBalance returns the balance for the given account code from the
// balance_snapshots table. Returns decimal.Zero if no snapshot exists yet.
func (a *LedgerReconciliationAdapter) GetAccountBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
	bal, err := a.q.GetAccountBalanceByCode(ctx, accountCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return decimal.Zero, nil
		}
		return decimal.Zero, fmt.Errorf(
			"settla-ledger-reconciliation: getting balance for account %s: %w",
			accountCode, err,
		)
	}
	return ledgerDecimalFromNumeric(bal), nil
}

// ledgerDecimalFromNumeric converts a pgtype.Numeric to a shopspring/decimal.Decimal.
func ledgerDecimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}
