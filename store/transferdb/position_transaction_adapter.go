package transferdb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// PositionTransactionStoreAdapter implements the position transaction store
// interface using raw SQL queries against the Transfer DB.
type PositionTransactionStoreAdapter struct {
	pool *pgxpool.Pool
}

// NewPositionTransactionStoreAdapter creates a new adapter.
func NewPositionTransactionStoreAdapter(pool *pgxpool.Pool) *PositionTransactionStoreAdapter {
	return &PositionTransactionStoreAdapter{pool: pool}
}

// CreateWithOutbox atomically creates a position transaction and inserts outbox entries.
func (s *PositionTransactionStoreAdapter) CreateWithOutbox(ctx context.Context, tx *domain.PositionTransaction, entries []domain.OutboxEntry) error {
	dbTx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settla-position-tx-store: begin tx: %w", err)
	}
	defer dbTx.Rollback(ctx) //nolint:errcheck

	_, err = dbTx.Exec(ctx, `
		INSERT INTO position_transactions (
			id, tenant_id, type, currency, location, amount, status,
			method, destination, reference, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		tx.ID, tx.TenantID, string(tx.Type), string(tx.Currency), tx.Location,
		tx.Amount.String(), string(tx.Status), tx.Method, tx.Destination,
		tx.Reference, tx.Version, tx.CreatedAt, tx.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("settla-position-tx-store: insert transaction: %w", err)
	}

	for _, entry := range entries {
		_, err = dbTx.Exec(ctx, `
			INSERT INTO outbox (
				id, aggregate_type, aggregate_id, tenant_id, correlation_id,
				event_type, payload, is_intent, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			entry.ID, entry.AggregateType, entry.AggregateID, entry.TenantID,
			entry.CorrelationID, entry.EventType, entry.Payload, entry.IsIntent,
			entry.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("settla-position-tx-store: insert outbox entry: %w", err)
		}
	}

	return dbTx.Commit(ctx)
}

// UpdateStatus updates a position transaction's status and optional failure reason.
func (s *PositionTransactionStoreAdapter) UpdateStatus(ctx context.Context, id, tenantID uuid.UUID, status domain.PositionTxStatus, failureReason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE position_transactions
		SET status = $3, failure_reason = $4, version = version + 1, updated_at = now()
		WHERE id = $1 AND tenant_id = $2`,
		id, tenantID, string(status), failureReason,
	)
	return err
}

// Get returns a position transaction by ID and tenant ID.
func (s *PositionTransactionStoreAdapter) Get(ctx context.Context, id, tenantID uuid.UUID) (*domain.PositionTransaction, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, type, currency, location, amount, status,
		       method, destination, reference, failure_reason, version,
		       created_at, updated_at
		FROM position_transactions
		WHERE id = $1 AND tenant_id = $2`, id, tenantID)

	return scanPositionTransaction(row)
}

// ListByTenant returns paginated position transactions for a tenant.
func (s *PositionTransactionStoreAdapter) ListByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int32) ([]domain.PositionTransaction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, type, currency, location, amount, status,
		       method, destination, reference, failure_reason, version,
		       created_at, updated_at
		FROM position_transactions
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-position-tx-store: list by tenant: %w", err)
	}
	defer rows.Close()

	var txns []domain.PositionTransaction
	for rows.Next() {
		tx, err := scanPositionTransactionFromRows(rows)
		if err != nil {
			return nil, err
		}
		txns = append(txns, *tx)
	}
	return txns, rows.Err()
}

// ListByTenantCursor returns cursor-paginated position transactions for a tenant.
func (s *PositionTransactionStoreAdapter) ListByTenantCursor(ctx context.Context, tenantID uuid.UUID, pageSize int32, cursor time.Time) ([]domain.PositionTransaction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, type, currency, location, amount, status,
		       method, destination, reference, failure_reason, version,
		       created_at, updated_at
		FROM position_transactions
		WHERE tenant_id = $1 AND created_at < $2
		ORDER BY created_at DESC
		LIMIT $3`, tenantID, cursor, pageSize)
	if err != nil {
		return nil, fmt.Errorf("settla-position-tx-store: list by tenant cursor: %w", err)
	}
	defer rows.Close()

	var txns []domain.PositionTransaction
	for rows.Next() {
		tx, err := scanPositionTransactionFromRows(rows)
		if err != nil {
			return nil, err
		}
		txns = append(txns, *tx)
	}
	return txns, rows.Err()
}

// scanRow is a generic row scanner interface.
type scanRow interface {
	Scan(dest ...any) error
}

func scanPositionTransaction(row scanRow) (*domain.PositionTransaction, error) {
	var tx domain.PositionTransaction
	var txType, currency, status, amount string
	var createdAt, updatedAt time.Time

	err := row.Scan(
		&tx.ID, &tx.TenantID, &txType, &currency, &tx.Location, &amount, &status,
		&tx.Method, &tx.Destination, &tx.Reference, &tx.FailureReason, &tx.Version,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	tx.Type = domain.PositionTxType(txType)
	tx.Currency = domain.Currency(currency)
	tx.Status = domain.PositionTxStatus(status)
	tx.Amount, _ = decimal.NewFromString(amount)
	tx.CreatedAt = createdAt
	tx.UpdatedAt = updatedAt
	return &tx, nil
}

type scanRowsRow interface {
	Scan(dest ...any) error
}

func scanPositionTransactionFromRows(row scanRowsRow) (*domain.PositionTransaction, error) {
	return scanPositionTransaction(row)
}
