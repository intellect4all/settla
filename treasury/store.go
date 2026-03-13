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

	// UpdatePosition persists the current balance and locked amounts.
	UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error

	// RecordHistory appends a snapshot to position_history for audit.
	RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error
}

// ReserveOpType identifies the kind of reservation operation.
type ReserveOpType string

const (
	OpReserve ReserveOpType = "reserve"
	OpRelease ReserveOpType = "release"
	OpCommit  ReserveOpType = "commit"
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
