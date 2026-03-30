package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/recovery"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/treasurydb"
	"github.com/intellect4all/settla/treasury"
)

// stubTreasuryStore returns empty data when treasury DB is unavailable.
type stubTreasuryStore struct{}

func (s *stubTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	return nil, nil
}

func (s *stubTreasuryStore) LoadPositionsPaginated(ctx context.Context, limit, offset int32) ([]domain.Position, error) {
	return nil, nil
}

func (s *stubTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return nil
}

func (s *stubTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	return nil
}

func (s *stubTreasuryStore) LogReserveOp(ctx context.Context, op treasury.ReserveOp) error {
	return nil
}

func (s *stubTreasuryStore) LogReserveOps(ctx context.Context, ops []treasury.ReserveOp) error {
	return nil
}

func (s *stubTreasuryStore) GetUncommittedOps(ctx context.Context) ([]treasury.ReserveOp, error) {
	return nil, nil
}

func (s *stubTreasuryStore) MarkOpCompleted(ctx context.Context, opID uuid.UUID) error {
	return nil
}

func (s *stubTreasuryStore) CleanupOldOps(ctx context.Context, before time.Time) error {
	return nil
}

// ── Postgres Treasury Store ─────────────────────────────────────────────────

type postgresTreasuryStore struct {
	q    *treasurydb.Queries
	pool *pgxpool.Pool
}

func newPostgresTreasuryStore(q *treasurydb.Queries, pool ...*pgxpool.Pool) *postgresTreasuryStore {
	s := &postgresTreasuryStore{q: q}
	if len(pool) > 0 {
		s.pool = pool[0]
	}
	return s
}

func (s *postgresTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	rows, err := s.q.ListAllPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading positions: %w", err)
	}
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
	return positions, nil
}

func (s *postgresTreasuryStore) LoadPositionsPaginated(ctx context.Context, limit, offset int32) ([]domain.Position, error) {
	rows, err := s.q.ListPositionsPaginated(ctx, treasurydb.ListPositionsPaginatedParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading positions page at offset %d: %w", offset, err)
	}
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
	return positions, nil
}

func (s *postgresTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return s.q.UpdatePositionBalances(ctx, treasurydb.UpdatePositionBalancesParams{
		ID:      id,
		Balance: numericFromDecimal(balance),
		Locked:  numericFromDecimal(locked),
	})
}

func (s *postgresTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	_, err := s.q.CreatePositionHistory(ctx, treasurydb.CreatePositionHistoryParams{
		PositionID:  positionID,
		TenantID:    tenantID,
		Balance:     numericFromDecimal(balance),
		Locked:      numericFromDecimal(locked),
		TriggerType: pgtype.Text{String: triggerType, Valid: true},
		TriggerRef:  pgtype.UUID{},
	})
	return err
}

func (s *postgresTreasuryStore) LogReserveOp(ctx context.Context, op treasury.ReserveOp) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO reserve_ops (id, tenant_id, currency, location, amount, reference, op_type, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT DO NOTHING`,
		op.ID, op.TenantID, string(op.Currency), op.Location, op.Amount.String(), op.Reference, string(op.OpType), op.CreatedAt,
	)
	return err
}

func (s *postgresTreasuryStore) LogReserveOps(ctx context.Context, ops []treasury.ReserveOp) error {
	if s.pool == nil {
		return nil
	}
	for _, op := range ops {
		if err := s.LogReserveOp(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *postgresTreasuryStore) GetUncommittedOps(ctx context.Context) ([]treasury.ReserveOp, error) {
	if s.pool == nil {
		return nil, nil
	}
	// Self-healing query: returns only reserve ops that don't have a matching
	// commit or release op for the same reference. This is correct even if
	// completion marking was missed (e.g. crash between flush and mark).
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, r.tenant_id, r.currency, r.location, r.amount, r.reference, r.op_type, r.created_at
		 FROM reserve_ops r
		 WHERE r.op_type = 'reserve'
		   AND NOT EXISTS (
		       SELECT 1 FROM reserve_ops c
		       WHERE c.reference = r.reference
		         AND c.op_type IN ('commit', 'release', 'consume')
		   )
		 ORDER BY r.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading uncommitted ops: %w", err)
	}
	defer rows.Close()

	var ops []treasury.ReserveOp
	for rows.Next() {
		var op treasury.ReserveOp
		var currency, opType, amount string
		if err := rows.Scan(&op.ID, &op.TenantID, &currency, &op.Location, &amount, &op.Reference, &opType, &op.CreatedAt); err != nil {
			return nil, err
		}
		op.Currency = domain.Currency(currency)
		op.OpType = treasury.ReserveOpType(opType)
		op.Amount, _ = decimal.NewFromString(amount)
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

func (s *postgresTreasuryStore) MarkOpCompleted(ctx context.Context, opID uuid.UUID) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE reserve_ops SET completed = true WHERE id = $1`, opID)
	return err
}

func (s *postgresTreasuryStore) CleanupOldOps(ctx context.Context, before time.Time) error {
	if s.pool == nil {
		return nil
	}
	// Delete reserve ops that have a matching commit/release (fully resolved),
	// and any ops older than the cutoff. This keeps the table bounded.
	_, err := s.pool.Exec(ctx, `
		DELETE FROM reserve_ops
		WHERE created_at < $1
		  AND (
		      op_type IN ('commit', 'release', 'consume')
		      OR EXISTS (
		          SELECT 1 FROM reserve_ops c
		          WHERE c.reference = reserve_ops.reference
		            AND c.op_type IN ('commit', 'release', 'consume')
		      )
		  )`, before)
	return err
}

func (s *postgresTreasuryStore) BatchWriteEvents(ctx context.Context, events []domain.PositionEvent) error {
	if s.pool == nil || len(events) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, len(events))
	positionIDs := make([]uuid.UUID, len(events))
	tenantIDs := make([]uuid.UUID, len(events))
	eventTypes := make([]string, len(events))
	amounts := make([]string, len(events))
	balanceAfters := make([]string, len(events))
	lockedAfters := make([]string, len(events))
	referenceIDs := make([]uuid.UUID, len(events))
	referenceTypes := make([]string, len(events))
	idempotencyKeys := make([]string, len(events))
	recordedAts := make([]time.Time, len(events))

	for i, e := range events {
		ids[i] = e.ID
		positionIDs[i] = e.PositionID
		tenantIDs[i] = e.TenantID
		eventTypes[i] = string(e.EventType)
		amounts[i] = e.Amount.String()
		balanceAfters[i] = e.BalanceAfter.String()
		lockedAfters[i] = e.LockedAfter.String()
		referenceIDs[i] = e.ReferenceID
		referenceTypes[i] = e.ReferenceType
		idempotencyKeys[i] = e.IdempotencyKey
		recordedAts[i] = e.RecordedAt
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO position_events (
			id, position_id, tenant_id, event_type, amount,
			balance_after, locked_after, reference_id, reference_type,
			idempotency_key, recorded_at
		)
		SELECT
			unnest($1::uuid[]),
			unnest($2::uuid[]),
			unnest($3::uuid[]),
			unnest($4::text[]),
			unnest($5::numeric[]),
			unnest($6::numeric[]),
			unnest($7::numeric[]),
			unnest($8::uuid[]),
			unnest($9::text[]),
			unnest($10::text[]),
			unnest($11::timestamptz[])
		ON CONFLICT (idempotency_key, recorded_at) DO NOTHING`,
		ids, positionIDs, tenantIDs, eventTypes, amounts,
		balanceAfters, lockedAfters, referenceIDs, referenceTypes,
		idempotencyKeys, recordedAts,
	)
	return err
}

func (s *postgresTreasuryStore) GetEventsAfter(ctx context.Context, positionID uuid.UUID, after time.Time) ([]domain.PositionEvent, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, position_id, tenant_id, event_type, amount,
		       balance_after, locked_after, reference_id, reference_type,
		       idempotency_key, recorded_at
		FROM position_events
		WHERE position_id = $1 AND recorded_at > $2
		ORDER BY recorded_at ASC`, positionID, after)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading events after %v for position %s: %w", after, positionID, err)
	}
	defer rows.Close()

	var events []domain.PositionEvent
	for rows.Next() {
		var e domain.PositionEvent
		var eventType, amount, balanceAfter, lockedAfter string
		if err := rows.Scan(
			&e.ID, &e.PositionID, &e.TenantID, &eventType, &amount,
			&balanceAfter, &lockedAfter, &e.ReferenceID, &e.ReferenceType,
			&e.IdempotencyKey, &e.RecordedAt,
		); err != nil {
			return nil, err
		}
		e.EventType = domain.PositionEventType(eventType)
		e.Amount, _ = decimal.NewFromString(amount)
		e.BalanceAfter, _ = decimal.NewFromString(balanceAfter)
		e.LockedAfter, _ = decimal.NewFromString(lockedAfter)
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *postgresTreasuryStore) GetPositionEventHistory(ctx context.Context, tenantID, positionID uuid.UUID, from, to time.Time, limit, offset int32) ([]domain.PositionEvent, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, position_id, tenant_id, event_type, amount,
		       balance_after, locked_after, reference_id, reference_type,
		       idempotency_key, recorded_at
		FROM position_events
		WHERE tenant_id = $1 AND position_id = $2
		  AND recorded_at >= $3 AND recorded_at <= $4
		ORDER BY recorded_at DESC
		LIMIT $5 OFFSET $6`,
		tenantID, positionID, from, to, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading event history for position %s: %w", positionID, err)
	}
	defer rows.Close()

	var events []domain.PositionEvent
	for rows.Next() {
		var e domain.PositionEvent
		var eventType, amount, balanceAfter, lockedAfter string
		if err := rows.Scan(
			&e.ID, &e.PositionID, &e.TenantID, &eventType, &amount,
			&balanceAfter, &lockedAfter, &e.ReferenceID, &e.ReferenceType,
			&e.IdempotencyKey, &e.RecordedAt,
		); err != nil {
			return nil, err
		}
		e.EventType = domain.PositionEventType(eventType)
		e.Amount, _ = decimal.NewFromString(amount)
		e.BalanceAfter, _ = decimal.NewFromString(balanceAfter)
		e.LockedAfter, _ = decimal.NewFromString(lockedAfter)
		events = append(events, e)
	}
	return events, rows.Err()
}

// ── Stub ProviderStatusChecker ───────────────────────────────────────────────

// stubProviderStatusChecker is used until real provider status checks are wired.
// The recovery detector is nil-safe with respect to ProviderStatusChecker responses —
// an "unknown" status causes the detector to skip provider-driven recovery and
// rely solely on time-threshold-based escalation.
type stubProviderStatusChecker struct{}

var _ recovery.ProviderStatusChecker = (*stubProviderStatusChecker)(nil)

func (s *stubProviderStatusChecker) CheckOnRampStatus(_ context.Context, _ string, _ uuid.UUID) (*recovery.ProviderStatus, error) {
	return &recovery.ProviderStatus{Status: "unknown"}, nil
}

func (s *stubProviderStatusChecker) CheckOffRampStatus(_ context.Context, _ string, _ uuid.UUID) (*recovery.ProviderStatus, error) {
	return &recovery.ProviderStatus{Status: "unknown"}, nil
}

func (s *stubProviderStatusChecker) CheckBlockchainStatus(_ context.Context, _ string, _ string) (*recovery.ChainStatus, error) {
	return &recovery.ChainStatus{Confirmed: false}, nil
}

// ── Conversion helpers ──────────────────────────────────────────────────────

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
