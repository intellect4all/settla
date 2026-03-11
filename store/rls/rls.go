// Package rls provides helpers for PostgreSQL Row-Level Security tenant isolation.
//
// The helpers start a transaction, set the session variable app.current_tenant_id
// via SET LOCAL (scoped to the transaction), and call the user-provided function.
// SET LOCAL is safe with PgBouncer transaction mode because it resets on COMMIT/ROLLBACK.
package rls

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTenantTx runs fn inside a read-write transaction with the RLS tenant context set.
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rls: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := setTenantLocal(ctx, tx, tenantID); err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// WithTenantReadTx runs fn inside a read-only transaction with the RLS tenant context set.
func WithTenantReadTx(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("rls: begin read tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := setTenantLocal(ctx, tx, tenantID); err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SetTenantLocal sets app.current_tenant_id for the current transaction.
// Exported for use in existing transaction flows (e.g. outbox operations)
// where the caller already manages the transaction lifecycle.
func SetTenantLocal(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	return setTenantLocal(ctx, tx, tenantID)
}

func setTenantLocal(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	_, err := tx.Exec(ctx, "SET LOCAL app.current_tenant_id = $1", tenantID.String())
	if err != nil {
		return fmt.Errorf("rls: set tenant context: %w", err)
	}
	return nil
}
