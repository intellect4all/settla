package transferdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/compensation"
)

// CompensationStoreAdapter implements compensation.CompensationStore using
// transferdb SQLC queries.
type CompensationStoreAdapter struct {
	q *Queries
}

// NewCompensationStoreAdapter creates a compensation store adapter backed by the transfer DB.
func NewCompensationStoreAdapter(q *Queries) *CompensationStoreAdapter {
	return &CompensationStoreAdapter{q: q}
}

// CreateCompensationRecord inserts a new compensation record with status "in_progress"
// and returns the generated record ID.
func (a *CompensationStoreAdapter) CreateCompensationRecord(ctx context.Context, params compensation.CreateCompensationParams) (uuid.UUID, error) {
	var refundAmount pgtype.Numeric
	if err := refundAmount.Scan(params.RefundAmount.String()); err != nil {
		return uuid.Nil, fmt.Errorf("transferdb: scanning refund amount: %w", err)
	}

	row, err := a.q.CreateCompensationRecord(ctx, CreateCompensationRecordParams{
		TransferID:     params.TransferID,
		TenantID:       params.TenantID,
		Strategy:       CompensationStrategyEnum(params.Strategy),
		RefundAmount:   refundAmount,
		RefundCurrency: pgtype.Text{String: params.RefundCurrency, Valid: true},
	})
	if err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// UpdateCompensationRecord updates step tracking, FX loss, and status on an
// existing compensation record.
func (a *CompensationStoreAdapter) UpdateCompensationRecord(ctx context.Context, id uuid.UUID, stepsCompleted, stepsFailed []byte, fxLoss decimal.Decimal, status string) error {
	var fxLossNum pgtype.Numeric
	if err := fxLossNum.Scan(fxLoss.String()); err != nil {
		return fmt.Errorf("transferdb: scanning fx loss: %w", err)
	}

	return a.q.UpdateCompensationRecord(ctx, UpdateCompensationRecordParams{
		ID:             id,
		StepsCompleted: stepsCompleted,
		StepsFailed:    stepsFailed,
		FxLoss:         fxLossNum,
		Status:         status,
	})
}
