package treasury

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// Store is the persistence interface for treasury positions.
// Only the background flush goroutine writes through this interface —
// Reserve and Release never touch it.
type Store interface {
	// LoadAllPositions returns every position from the database.
	// Called once at startup to populate the in-memory map.
	LoadAllPositions(ctx context.Context) ([]domain.Position, error)

	// LoadPositionsPaginated returns a page of positions ordered by (tenant_id, currency, location).
	// Used by LoadPositions to stream positions in batches without a single multi-million-element allocation.
	LoadPositionsPaginated(ctx context.Context, limit, offset int32) ([]domain.Position, error)

	// UpdatePosition persists the current balance and locked amounts.
	UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error

	// RecordHistory appends a snapshot to position_history for audit.
	RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error
}

// PositionUpdate holds the data for a single position flush.
type PositionUpdate struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Balance  decimal.Decimal
	Locked   decimal.Decimal
}

// BatchStore is an optional extension of Store for high-throughput flushing.
// If the underlying Store also implements this interface, flushOnce will use
// a single batch upsert instead of N individual UpdatePosition calls.
// At 100K+ tenants (~500K positions), this reduces flush time from O(N) DB
// round-trips to a single bulk operation.
type BatchStore interface {
	BatchUpdatePositions(ctx context.Context, updates []PositionUpdate) error
}

// ReserveOpType identifies the kind of reservation operation.
type ReserveOpType string

const (
	OpReserve ReserveOpType = "reserve"
	OpRelease ReserveOpType = "release"
	OpCommit  ReserveOpType = "commit"
	OpConsume ReserveOpType = "consume"
	OpCredit  ReserveOpType = "credit"
	OpDebit   ReserveOpType = "debit"
)

// ReserveOp records a single treasury operation for crash recovery.
// Written to the DB by the flush goroutine; replayed on startup.
type ReserveOp struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Currency  domain.Currency
	Location  string
	Amount    decimal.Decimal
	Reference uuid.UUID
	OpType    ReserveOpType
	CreatedAt time.Time
}

// EventStore is an optional extension of Store for the event-sourced position ledger.
// If the underlying Store also implements this interface, the Manager will batch-write
// position events every 10ms for audit and crash recovery.
type EventStore interface {
	// BatchWriteEvents inserts a batch of position events in a single round-trip.
	BatchWriteEvents(ctx context.Context, events []domain.PositionEvent) error

	// GetEventsAfter returns position events recorded after the given timestamp
	// for a specific position. Used during crash recovery to replay events not
	// yet reflected in the position snapshot.
	GetEventsAfter(ctx context.Context, positionID uuid.UUID, after time.Time) ([]domain.PositionEvent, error)

	// GetPositionEventHistory returns paginated position events for a tenant's position.
	// Used by the portal API for tenant-facing event history.
	GetPositionEventHistory(ctx context.Context, tenantID, positionID uuid.UUID, from, to time.Time, limit, offset int32) ([]domain.PositionEvent, error)
}

// ReserveOpStore is an optional extension of Store for crash recovery.
// If the underlying Store also implements this interface, the Manager will
// log reserve operations and replay them on startup.
type ReserveOpStore interface {
	LogReserveOp(ctx context.Context, op ReserveOp) error
	LogReserveOps(ctx context.Context, ops []ReserveOp) error
	GetUncommittedOps(ctx context.Context) ([]ReserveOp, error)
	MarkOpCompleted(ctx context.Context, opID uuid.UUID) error
	CleanupOldOps(ctx context.Context, before time.Time) error
}
