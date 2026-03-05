package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TreasuryManager tracks liquidity positions and manages fund reservations.
//
// Reserve and Release operate on in-memory atomic counters with nanosecond
// latency. They never hit the database directly. This is critical for
// performance because treasury positions are hot keys (~50 positions under
// constant concurrent pressure) — using SELECT FOR UPDATE would bottleneck
// at thousands of concurrent requests on the same row.
//
// Instead, a background goroutine flushes the in-memory state to Postgres
// every 100ms. This gives us atomic, lock-free reservations at nanosecond
// speed while maintaining durable state with at most 100ms staleness.
//
// GetPositions and GetPosition read from in-memory state for consistency
// with Reserve/Release. GetLiquidityReport may combine in-memory positions
// with computed aggregates.
type TreasuryManager interface {
	// Reserve atomically decrements available balance and increments locked balance
	// for the given tenant, currency, and location. Operates on in-memory state only.
	// Returns ErrInsufficientFunds if available balance is less than amount.
	// The reference UUID is used for idempotency and audit trail.
	Reserve(ctx context.Context, tenantID uuid.UUID, currency Currency, location string, amount decimal.Decimal, reference uuid.UUID) error

	// Release atomically decrements locked balance and increments available balance.
	// Operates on in-memory state only. Used when a transfer fails or is cancelled.
	// The reference UUID must match the original reservation.
	Release(ctx context.Context, tenantID uuid.UUID, currency Currency, location string, amount decimal.Decimal, reference uuid.UUID) error

	// GetPositions returns all treasury positions for a tenant.
	GetPositions(ctx context.Context, tenantID uuid.UUID) ([]Position, error)

	// GetPosition returns a specific position for a tenant, currency, and location.
	GetPosition(ctx context.Context, tenantID uuid.UUID, currency Currency, location string) (*Position, error)

	// GetLiquidityReport generates a summary of all positions for a tenant.
	GetLiquidityReport(ctx context.Context, tenantID uuid.UUID) (*LiquidityReport, error)
}

// LiquidityReport provides a summary of a tenant's treasury positions
// including alerts for positions that are below minimum thresholds.
type LiquidityReport struct {
	TenantID       uuid.UUID
	Positions      []Position
	TotalAvailable map[Currency]decimal.Decimal
	AlertPositions []Position // Positions below their MinBalance threshold
	GeneratedAt    time.Time
}
