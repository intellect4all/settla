package core

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ErrOptimisticLock is returned when a store operation fails due to a concurrent
// modification (version mismatch). Callers should treat this as retryable.
var ErrOptimisticLock = errors.New("settla-core: optimistic lock conflict")

// TransferStore is the core engine's port for persisting transfer aggregates.
// This is richer than domain.TransferStore — it includes event persistence,
// daily volume queries, and optimistic-lock-aware updates with outbox support.
type TransferStore interface {
	CreateTransfer(ctx context.Context, transfer *domain.Transfer) error
	GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*domain.Transfer, error)
	GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error)
	GetTransferByExternalRef(ctx context.Context, tenantID uuid.UUID, externalRef string) (*domain.Transfer, error)
	UpdateTransfer(ctx context.Context, transfer *domain.Transfer) error
	CreateTransferEvent(ctx context.Context, event *domain.TransferEvent) error
	GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error)
	GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
	CreateQuote(ctx context.Context, quote *domain.Quote) error
	GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error)
	ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error)
	// ListTransfersFiltered returns transfers with optional server-side filtering by status and search query.
	ListTransfersFiltered(ctx context.Context, tenantID uuid.UUID, statusFilter, searchQuery string, limit int) ([]domain.Transfer, error)

	// CountPendingTransfers returns the number of non-terminal transfers for a tenant.
	// Used to enforce per-tenant pending transfer limits.
	CountPendingTransfers(ctx context.Context, tenantID uuid.UUID) (int, error)

	// TransitionWithOutbox atomically updates transfer status and inserts outbox entries
	// in a single database transaction. Uses optimistic locking via version check.
	// Returns domain.ErrOptimisticLock if version mismatch.
	TransitionWithOutbox(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error

	// CreateTransferWithOutbox atomically creates a transfer and inserts outbox entries
	// in a single database transaction.
	CreateTransferWithOutbox(ctx context.Context, transfer *domain.Transfer, entries []domain.OutboxEntry) error
}

// TenantStore is the core engine's port for reading tenant configuration.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
}

// DailyVolumeCounter provides atomic daily volume tracking for limit enforcement.
// When set on the Engine, it replaces the in-process sync.Map cache with an
// atomic counter (typically backed by Redis INCRBYFLOAT) that is safe under
// concurrent CreateTransfer calls. If nil, the engine falls back to the
// approximate in-memory cache.
type DailyVolumeCounter interface {
	// GetDailyVolume returns the current daily volume for a tenant. If the key
	// does not exist, returns 0 and a nil error.
	GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
	// IncrDailyVolume atomically increments the daily volume counter by amount
	// and returns the new total.
	IncrDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time, amount decimal.Decimal) (decimal.Decimal, error)
	// SeedDailyVolume sets the daily volume counter if it does not already exist.
	// Used to seed from DB on first access. Returns true if the key was set.
	SeedDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time, amount decimal.Decimal) (bool, error)
}

// Router is needed ONLY for quote generation in CreateTransfer and GetQuote.
// It does NOT execute any provider calls. In the outbox pattern, provider
// execution is handled by workers consuming outbox intents.
type Router interface {
	GetQuote(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error)
	GetRoutingOptions(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.RouteResult, error)
}

// CreateTransferRequest is the input for creating a new settlement transfer.
type CreateTransferRequest struct {
	ExternalRef    string
	IdempotencyKey string
	SourceCurrency domain.Currency
	SourceAmount   decimal.Decimal
	DestCurrency   domain.Currency
	Sender         domain.Sender
	Recipient      domain.Recipient
	QuoteID        *uuid.UUID
}
