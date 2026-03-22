package treasury

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
	"github.com/shopspring/decimal"
)

// PositionState holds the in-memory state for a single treasury position.
// Balance and locked are stored as atomic int64 micro-units so that Reserve
// can run a lock-free CAS loop without holding any mutex.
type PositionState struct {
	// Immutable metadata (set once during load, never mutated).
	ID            uuid.UUID
	TenantID      uuid.UUID
	Currency      domain.Currency
	Location      string
	MinBalance    decimal.Decimal
	TargetBalance decimal.Decimal

	// mu protects multi-field operations that must be observed atomically:
	// - snapshot() takes RLock to read balance+locked+reserved consistently
	// - CommitReservation takes Lock to modify reserved and locked together
	// Single-field CAS operations (Reserve, Release) do not need this lock.
	mu sync.RWMutex

	// Atomic micro-unit counters — the hot path.
	// balance: total funds (updated by ledger sync / admin top-up)
	// locked:  committed for in-flight transfers (incremented by CommitReservation)
	// reserved: tentatively held (incremented by Reserve, decremented by Release/Commit)
	balanceMicro  atomic.Int64
	lockedMicro   atomic.Int64
	reservedMicro atomic.Int64

	// dirty is set when the position has been modified since the last flush.
	dirty atomic.Bool
}

// Available returns balance - locked - reserved in decimal.
func (ps *PositionState) Available() decimal.Decimal {
	b := ps.balanceMicro.Load()
	l := ps.lockedMicro.Load()
	r := ps.reservedMicro.Load()
	return fromMicro(b - l - r)
}

// snapshot returns the current position as a domain.Position for reads.
// Takes RLock to ensure balance, locked, and reserved are read consistently
// (no intermediate state from CommitReservation modifying both reserved and locked).
func (ps *PositionState) snapshot() domain.Position {
	ps.mu.RLock()
	b := ps.balanceMicro.Load()
	l := ps.lockedMicro.Load()
	r := ps.reservedMicro.Load()
	ps.mu.RUnlock()

	return domain.Position{
		ID:            ps.ID,
		TenantID:      ps.TenantID,
		Currency:      ps.Currency,
		Location:      ps.Location,
		Balance:       fromMicro(b),
		Locked:        fromMicro(l + r),
		MinBalance:    ps.MinBalance,
		TargetBalance: ps.TargetBalance,
		UpdatedAt:     time.Now().UTC(),
	}
}
