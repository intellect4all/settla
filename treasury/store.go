package treasury

import (
	"context"

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
