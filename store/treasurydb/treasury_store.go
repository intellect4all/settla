package treasurydb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/treasury"
)

// TreasuryStoreAdapter implements treasury.Store, treasury.BatchStore,
// treasury.EventStore, and treasury.ReserveOpStore using sqlc-generated queries.
type TreasuryStoreAdapter struct {
	q *Queries
}

// NewTreasuryStoreAdapter creates a TreasuryStoreAdapter backed by sqlc Queries.
// Pass a dbpool.RoutedPool as the DBTX to New() for automatic read replica routing.
func NewTreasuryStoreAdapter(q *Queries) *TreasuryStoreAdapter {
	return &TreasuryStoreAdapter{q: q}
}


func (s *TreasuryStoreAdapter) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	rows, err := s.q.ListAllPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading positions: %w", err)
	}
	return mapPositions(rows), nil
}

func (s *TreasuryStoreAdapter) LoadPositionsPaginated(ctx context.Context, limit, offset int32) ([]domain.Position, error) {
	rows, err := s.q.ListPositionsPaginated(ctx, ListPositionsPaginatedParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading positions page at offset %d: %w", offset, err)
	}
	return mapPositions(rows), nil
}

func (s *TreasuryStoreAdapter) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return s.q.UpdatePositionBalances(ctx, UpdatePositionBalancesParams{
		ID:      id,
		Balance: numericFromDecimal(balance),
		Locked:  numericFromDecimal(locked),
	})
}

func (s *TreasuryStoreAdapter) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	_, err := s.q.CreatePositionHistory(ctx, CreatePositionHistoryParams{
		PositionID:  positionID,
		TenantID:    tenantID,
		Balance:     numericFromDecimal(balance),
		Locked:      numericFromDecimal(locked),
		TriggerType: pgtype.Text{String: triggerType, Valid: true},
		TriggerRef:  pgtype.UUID{},
	})
	return err
}


func (s *TreasuryStoreAdapter) BatchUpdatePositions(ctx context.Context, updates []treasury.PositionUpdate) error {
	for _, u := range updates {
		if err := s.q.BatchUpdatePositions(ctx, BatchUpdatePositionsParams{
			ID:      u.ID,
			Balance: numericFromDecimal(u.Balance),
			Locked:  numericFromDecimal(u.Locked),
		}); err != nil {
			return err
		}
	}
	return nil
}


func (s *TreasuryStoreAdapter) BatchWriteEvents(ctx context.Context, events []domain.PositionEvent) error {
	if len(events) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, len(events))
	positionIDs := make([]uuid.UUID, len(events))
	tenantIDs := make([]uuid.UUID, len(events))
	eventTypes := make([]string, len(events))
	amounts := make([]pgtype.Numeric, len(events))
	balanceAfters := make([]pgtype.Numeric, len(events))
	lockedAfters := make([]pgtype.Numeric, len(events))
	referenceIDs := make([]uuid.UUID, len(events))
	referenceTypes := make([]string, len(events))
	idempotencyKeys := make([]string, len(events))
	recordedAts := make([]time.Time, len(events))

	for i, e := range events {
		ids[i] = e.ID
		positionIDs[i] = e.PositionID
		tenantIDs[i] = e.TenantID
		eventTypes[i] = string(e.EventType)
		amounts[i] = numericFromDecimal(e.Amount)
		balanceAfters[i] = numericFromDecimal(e.BalanceAfter)
		lockedAfters[i] = numericFromDecimal(e.LockedAfter)
		referenceIDs[i] = e.ReferenceID
		referenceTypes[i] = e.ReferenceType
		idempotencyKeys[i] = e.IdempotencyKey
		recordedAts[i] = e.RecordedAt
	}

	return s.q.BatchInsertPositionEvents(ctx, BatchInsertPositionEventsParams{
		Ids:             ids,
		PositionIds:     positionIDs,
		TenantIds:       tenantIDs,
		EventTypes:      eventTypes,
		Amounts:         amounts,
		BalanceAfters:   balanceAfters,
		LockedAfters:    lockedAfters,
		ReferenceIds:    referenceIDs,
		ReferenceTypes:  referenceTypes,
		IdempotencyKeys: idempotencyKeys,
		RecordedAts:     recordedAts,
	})
}

func (s *TreasuryStoreAdapter) GetEventsAfter(ctx context.Context, positionID uuid.UUID, after time.Time) ([]domain.PositionEvent, error) {
	rows, err := s.q.GetEventsAfterTimestamp(ctx, GetEventsAfterTimestampParams{
		PositionID: positionID,
		RecordedAt: after,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading events after %v for position %s: %w", after, positionID, err)
	}
	return mapPositionEvents(rows), nil
}

func (s *TreasuryStoreAdapter) GetPositionEventHistory(ctx context.Context, tenantID, positionID uuid.UUID, from, to time.Time, limit, offset int32) ([]domain.PositionEvent, error) {
	rows, err := s.q.GetPositionEventHistory(ctx, GetPositionEventHistoryParams{
		TenantID:     tenantID,
		PositionID:   positionID,
		RecordedAt:   from,
		RecordedAt_2: to,
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading event history for position %s: %w", positionID, err)
	}
	return mapPositionEvents(rows), nil
}


func (s *TreasuryStoreAdapter) LogReserveOp(ctx context.Context, op treasury.ReserveOp) error {
	return s.q.InsertReserveOp(ctx, InsertReserveOpParams{
		ID:        op.ID,
		TenantID:  op.TenantID,
		Currency:  string(op.Currency),
		Location:  op.Location,
		Amount:    numericFromDecimal(op.Amount),
		Reference: op.Reference,
		OpType:    string(op.OpType),
		CreatedAt: op.CreatedAt,
	})
}

func (s *TreasuryStoreAdapter) LogReserveOps(ctx context.Context, ops []treasury.ReserveOp) error {
	for _, op := range ops {
		if err := s.LogReserveOp(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *TreasuryStoreAdapter) GetUncommittedOps(ctx context.Context) ([]treasury.ReserveOp, error) {
	rows, err := s.q.GetUncommittedReserveOps(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading uncommitted ops: %w", err)
	}
	ops := make([]treasury.ReserveOp, len(rows))
	for i, row := range rows {
		ops[i] = treasury.ReserveOp{
			ID:        row.ID,
			TenantID:  row.TenantID,
			Currency:  domain.Currency(row.Currency),
			Location:  row.Location,
			Amount:    decimalFromNumeric(row.Amount),
			Reference: row.Reference,
			OpType:    treasury.ReserveOpType(row.OpType),
			CreatedAt: row.CreatedAt,
		}
	}
	return ops, nil
}

func (s *TreasuryStoreAdapter) MarkOpCompleted(ctx context.Context, opID uuid.UUID) error {
	return s.q.MarkReserveOpCompleted(ctx, opID)
}

func (s *TreasuryStoreAdapter) CleanupOldOps(ctx context.Context, before time.Time) error {
	return s.q.CleanupCompletedReserveOps(ctx, before)
}


func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	n := pgtype.Numeric{}
	_ = n.Scan(d.String())
	return n
}

func decimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}

func mapPositions(rows []Position) []domain.Position {
	positions := make([]domain.Position, len(rows))
	for i, row := range rows {
		positions[i] = domain.Position{
			ID:            row.ID,
			TenantID:      row.TenantID,
			Currency:      domain.Currency(row.Currency),
			Location:      row.Location,
			Balance:       decimalFromNumeric(row.Balance),
			Locked:        decimalFromNumeric(row.Locked),
			MinBalance:    decimalFromNumeric(row.MinBalance),
			TargetBalance: decimalFromNumeric(row.TargetBalance),
			UpdatedAt:     row.UpdatedAt,
		}
	}
	return positions
}

func mapPositionEvents(rows []PositionEvent) []domain.PositionEvent {
	events := make([]domain.PositionEvent, len(rows))
	for i, row := range rows {
		events[i] = domain.PositionEvent{
			ID:             row.ID,
			PositionID:     row.PositionID,
			TenantID:       row.TenantID,
			EventType:      domain.PositionEventType(row.EventType),
			Amount:         decimalFromNumeric(row.Amount),
			BalanceAfter:   decimalFromNumeric(row.BalanceAfter),
			LockedAfter:    decimalFromNumeric(row.LockedAfter),
			ReferenceID:    row.ReferenceID,
			ReferenceType:  row.ReferenceType,
			IdempotencyKey: row.IdempotencyKey,
			RecordedAt:     row.RecordedAt,
		}
	}
	return events
}
