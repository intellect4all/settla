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
// performance because treasury positions are hot keys under constant concurrent
// pressure — using SELECT FOR UPDATE would bottleneck at thousands of
// concurrent requests on the same row.
//
// Designed for 100K+ tenants (~500K+ positions). Key scalability features:
//   - Per-tenant index for O(1) tenant position lookups (no full-scan)
//   - Dirty set tracking for O(dirty) flush instead of O(all positions)
//   - Optional batch flush (BatchStore) for single-roundtrip DB writes
//   - CAS-based Reserve/Release scales linearly with position count
//
// A background goroutine flushes the in-memory state to Postgres every 100ms.
// This gives us atomic, lock-free reservations at nanosecond speed while
// maintaining durable state with at most 100ms staleness.
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

	// CreditBalance atomically increases the balance for a position.
	// Used when money enters the tenant's position: deposit confirmations (crypto
	// and bank), payment link redemptions, stablecoin compensation, manual top-ups,
	// and internal rebalancing.
	//
	// This is a high-frequency operation at scale (hundreds/sec) — it uses the same
	// CAS-first, batch-WAL pattern as Reserve/Release for nanosecond latency.
	// The reference UUID ensures idempotency. refType identifies the source for audit.
	CreditBalance(ctx context.Context, tenantID uuid.UUID, currency Currency, location string, amount decimal.Decimal, reference uuid.UUID, refType string) error

	// DebitBalance atomically decreases the balance for a position.
	// Used for manual withdrawals and internal rebalancing (source side).
	// Rejects if available balance (balance - locked - reserved) is less than amount.
	// The reference UUID ensures idempotency. refType identifies the source for audit.
	DebitBalance(ctx context.Context, tenantID uuid.UUID, currency Currency, location string, amount decimal.Decimal, reference uuid.UUID, refType string) error

	// ConsumeReservation atomically decrements both reserved and balance amounts.
	// Called when a transfer completes — the reserved funds are consumed because
	// money actually left the tenant's position (sent to recipient).
	//
	// Different from Release (which returns reserved funds to available without
	// changing balance) and from CommitReservation (which moves reserved → locked
	// without touching balance).
	//
	// Uses mu.Lock() because two fields (reservedMicro and balanceMicro) must
	// change atomically — same pattern as CommitReservation.
	ConsumeReservation(ctx context.Context, tenantID uuid.UUID, currency Currency, location string, amount decimal.Decimal, reference uuid.UUID) error
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
