package position

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// Store persists position transaction aggregates.
type Store interface {
	// CreateWithOutbox atomically creates a position transaction and inserts
	// outbox entries in the same database transaction.
	CreateWithOutbox(ctx context.Context, tx *domain.PositionTransaction, entries []domain.OutboxEntry) error

	// UpdateStatus updates a transaction's status and optional failure reason.
	UpdateStatus(ctx context.Context, id, tenantID uuid.UUID, status domain.PositionTxStatus, failureReason string) error

	// Get retrieves a position transaction by ID and tenant.
	Get(ctx context.Context, id, tenantID uuid.UUID) (*domain.PositionTransaction, error)

	// ListByTenant returns paginated position transactions for a tenant.
	ListByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int32) ([]domain.PositionTransaction, error)

	// ListByTenantCursor returns cursor-paginated position transactions for a tenant.
	ListByTenantCursor(ctx context.Context, tenantID uuid.UUID, pageSize int32, cursor time.Time) ([]domain.PositionTransaction, error)
}
