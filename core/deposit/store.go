package deposit

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ErrOptimisticLock is returned when a store operation fails due to a concurrent
// modification (version mismatch). Callers should treat this as retryable.
var ErrOptimisticLock = errors.New("settla-deposit: optimistic lock conflict")

// DepositStore is the deposit engine's port for persisting deposit session aggregates.
// All mutations are atomic: state change + outbox entries in a single transaction.
type DepositStore interface {
	// CreateSessionWithOutbox atomically creates a deposit session, registers the
	// address in the address index, and inserts outbox entries.
	CreateSessionWithOutbox(ctx context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error

	// GetSession retrieves a deposit session by tenant and ID.
	GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.DepositSession, error)

	// GetSessionByAddress retrieves the most recent deposit session for an address.
	GetSessionByAddress(ctx context.Context, address string) (*domain.DepositSession, error)

	// GetSessionByIdempotencyKey retrieves a session by tenant and idempotency key.
	GetSessionByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.DepositSession, error)

	// ListSessions retrieves deposit sessions for a tenant with pagination.
	ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.DepositSession, error)

	// TransitionWithOutbox atomically updates session status and inserts outbox entries
	// in a single database transaction. Uses optimistic locking via version check.
	TransitionWithOutbox(ctx context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error

	// DispenseAddress obtains a deposit address from the pre-generated pool.
	// Uses SKIP LOCKED to avoid contention under concurrent session creation.
	DispenseAddress(ctx context.Context, tenantID uuid.UUID, chain string, sessionID uuid.UUID) (*domain.CryptoAddressPool, error)

	// CreateDepositTx records an on-chain transaction linked to a session.
	CreateDepositTx(ctx context.Context, tx *domain.DepositTransaction) error

	// GetSessionByTxHash retrieves a deposit session by looking up the transaction
	// hash and then loading the associated session.
	GetSessionByTxHash(ctx context.Context, tenantID uuid.UUID, chain, txHash string) (*domain.DepositSession, error)

	// GetDepositTxByHash retrieves a deposit transaction by chain and tx hash.
	GetDepositTxByHash(ctx context.Context, chain, txHash string) (*domain.DepositTransaction, error)

	// ListSessionTxs retrieves all transactions for a session.
	ListSessionTxs(ctx context.Context, sessionID uuid.UUID) ([]domain.DepositTransaction, error)

	// AccumulateReceived adds an amount to the session's received_amount.
	AccumulateReceived(ctx context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error

	// GetExpiredPendingSessions returns sessions in PENDING_PAYMENT with expires_at < now().
	GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.DepositSession, error)

	// GetSessionByIDOnly retrieves a deposit session by ID without tenant filtering.
	// Used for public-facing endpoints that need limited session info.
	GetSessionByIDOnly(ctx context.Context, sessionID uuid.UUID) (*domain.DepositSession, error)
}

// TenantStore is the deposit engine's port for reading tenant configuration.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}
