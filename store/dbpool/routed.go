// Package dbpool provides a connection-routing pool that transparently directs
// read queries to a replica and writes to the primary.
//
// # Motivation
//
// At scale, read-heavy queries (List*, Count*, analytics, reconciliation) compete
// with writes for the same connection pool and I/O bandwidth. A read replica
// offloads the primary, giving writes 2-3x more headroom.
//
// # Design
//
// RoutedPool implements the SQLC-generated DBTX interface so it can be passed
// directly to any store's New(db DBTX) constructor. No store code changes needed.
//
//   - Query / QueryRow → replica (reads)
//   - Exec / CopyFrom  → primary (writes)
//   - Transactions      → always use primary (via separate pool.Begin path)
//
// # Read-your-writes safety
//
// After a write, the replica may lag by a few milliseconds. For paths that need
// to read back a row immediately after writing it (e.g., CreateTransfer then
// GetTransfer), use dbpool.ForcePrimary(ctx) to pin that request to the primary:
//
//	ctx = dbpool.ForcePrimary(ctx)
//	store.CreateTransfer(ctx, ...)
//	transfer, _ := store.GetTransfer(ctx, ...) // hits primary, not replica
//
// When no replica is configured, RoutedPool is a transparent pass-through to the
// primary — zero overhead, zero behavior change.
package dbpool

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ctxKey struct{}

// ForcePrimary returns a context that forces all queries (including reads)
// to use the primary pool. Use this for read-your-writes consistency.
func ForcePrimary(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, true)
}

func forcedPrimary(ctx context.Context) bool {
	_, ok := ctx.Value(ctxKey{}).(bool)
	return ok
}

// RoutedPool routes read queries to a replica and write queries to the primary.
// It implements the DBTX interface used by all SQLC-generated Queries structs.
//
// When replica is nil, all operations use the primary (no-op wrapper).
type RoutedPool struct {
	primary *pgxpool.Pool
	replica *pgxpool.Pool // nil = all traffic to primary
}

// New creates a RoutedPool. Pass nil for replica to use primary for everything.
func New(primary, replica *pgxpool.Pool) *RoutedPool {
	return &RoutedPool{primary: primary, replica: replica}
}

// Primary returns the primary pool directly. Use this when you need pool-level
// operations like Begin() for transactions.
func (rp *RoutedPool) Primary() *pgxpool.Pool {
	return rp.primary
}

// reader returns the pool to use for read operations.
func (rp *RoutedPool) reader(ctx context.Context) *pgxpool.Pool {
	if rp.replica != nil && !forcedPrimary(ctx) {
		return rp.replica
	}
	return rp.primary
}


// Query routes to the replica for read operations.
func (rp *RoutedPool) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return rp.reader(ctx).Query(ctx, sql, args...)
}

// QueryRow routes to the replica for read operations.
func (rp *RoutedPool) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return rp.reader(ctx).QueryRow(ctx, sql, args...)
}

// Exec always uses the primary (writes).
func (rp *RoutedPool) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return rp.primary.Exec(ctx, sql, args...)
}

// CopyFrom always uses the primary (bulk writes). Required by transfer DBTX.
func (rp *RoutedPool) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return rp.primary.CopyFrom(ctx, tableName, columnNames, rowSrc)
}

// Close closes both pools.
func (rp *RoutedPool) Close() {
	rp.primary.Close()
	if rp.replica != nil {
		rp.replica.Close()
	}
}
