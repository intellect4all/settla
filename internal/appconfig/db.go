package appconfig

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPgxPool creates a pgxpool.Pool with PgBouncer-compatible settings.
// Uses QueryExecModeSimpleProtocol (no prepared statements) because PgBouncer
// in transaction mode reassigns backend connections between transactions —
// prepared statements created on one connection would fail on another.
func NewPgxPool(ctx context.Context, connString string, maxConns, minConns int) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConns = int32(maxConns)
	config.MinConns = int32(minConns)
	config.MaxConnIdleTime = 2 * time.Minute
	config.MaxConnLifetime = 30 * time.Minute
	return pgxpool.NewWithConfig(ctx, config)
}
