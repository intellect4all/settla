package main

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/core/maintenance"
	"github.com/intellect4all/settla/domain"
)

// stubPublisher drops all events when NATS is unavailable (development mode).
type stubPublisher struct{}

func (s *stubPublisher) Publish(ctx context.Context, event domain.Event) error {
	return nil
}

// stubTreasuryStore returns empty data when treasury DB is unavailable.
type stubTreasuryStore struct{}

func (s *stubTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	return nil, nil
}

func (s *stubTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return nil
}

func (s *stubTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	return nil
}

// ── pgxPoolDBExecutor: bridges *pgxpool.Pool → maintenance.DBExecutor ────────

// pgxPoolDBExecutor adapts pgxpool.Pool to the maintenance.DBExecutor interface
// so the PartitionManager can run DDL commands against the same pool used by
// other stores — without adding a pgx dependency to the maintenance package.
type pgxPoolDBExecutor struct {
	pool *pgxpool.Pool
}

func newPgxPoolDBExecutor(pool *pgxpool.Pool) maintenance.DBExecutor {
	return &pgxPoolDBExecutor{pool: pool}
}

type pgxCommandTag struct{ rowsAffected int64 }

func (t pgxCommandTag) RowsAffected() int64 { return t.rowsAffected }

type pgxRows struct {
	rows interface {
		Next() bool
		Scan(dest ...any) error
		Close()
		Err() error
	}
}

func (r *pgxRows) Next() bool                  { return r.rows.Next() }
func (r *pgxRows) Scan(dest ...interface{}) error { return r.rows.Scan(dest...) }
func (r *pgxRows) Close()                       { r.rows.Close() }
func (r *pgxRows) Err() error                   { return r.rows.Err() }

func (e *pgxPoolDBExecutor) Exec(ctx context.Context, sql string, args ...interface{}) (maintenance.CommandTag, error) {
	tag, err := e.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgxCommandTag{rowsAffected: tag.RowsAffected()}, nil
}

func (e *pgxPoolDBExecutor) Query(ctx context.Context, sql string, args ...interface{}) (maintenance.Rows, error) {
	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRows{rows: rows}, nil
}
