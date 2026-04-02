package bankdeposit

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
var ErrOptimisticLock = errors.New("settla-bank-deposit: optimistic lock conflict")

// BankDepositStore is the bank deposit engine's port for persisting deposit session aggregates.
// All mutations are atomic: state change + outbox entries in a single transaction.
type BankDepositStore interface {
	// CreateSessionWithOutbox atomically creates a bank deposit session, registers the
	// virtual account in the account index, and inserts outbox entries.
	CreateSessionWithOutbox(ctx context.Context, session *domain.BankDepositSession, entries []domain.OutboxEntry) error

	// GetSession retrieves a bank deposit session by tenant and ID.
	GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.BankDepositSession, error)

	// GetSessionByIdempotencyKey retrieves a session by tenant and idempotency key.
	GetSessionByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key domain.IdempotencyKey) (*domain.BankDepositSession, error)

	// GetSessionByAccountNumber retrieves the most recent active session for a virtual account.
	GetSessionByAccountNumber(ctx context.Context, accountNumber string) (*domain.BankDepositSession, error)

	// ListSessions retrieves bank deposit sessions for a tenant with pagination.
	ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.BankDepositSession, error)

	// ListSessionsCursor retrieves bank deposit sessions using cursor-based pagination (created_at < cursor, DESC).
	ListSessionsCursor(ctx context.Context, tenantID uuid.UUID, pageSize int, cursor time.Time) ([]domain.BankDepositSession, error)

	// TransitionWithOutbox atomically updates session status and inserts outbox entries
	// in a single database transaction. Uses optimistic locking via version check.
	TransitionWithOutbox(ctx context.Context, session *domain.BankDepositSession, entries []domain.OutboxEntry) error

	// DispenseVirtualAccount obtains a virtual account from the pre-provisioned pool.
	// Uses SKIP LOCKED to avoid contention under concurrent session creation.
	DispenseVirtualAccount(ctx context.Context, tenantID uuid.UUID, currency string) (*domain.VirtualAccountPool, error)

	// RecycleVirtualAccount marks a virtual account as available for reuse.
	RecycleVirtualAccount(ctx context.Context, accountNumber string) error

	// CreateBankDepositTx records a bank credit transaction linked to a session.
	CreateBankDepositTx(ctx context.Context, tx *domain.BankDepositTransaction) error

	// GetBankDepositTxByRef retrieves a bank deposit transaction by bank reference (dedup key).
	GetBankDepositTxByRef(ctx context.Context, bankReference string) (*domain.BankDepositTransaction, error)

	// ListSessionTxs retrieves all transactions for a session.
	ListSessionTxs(ctx context.Context, sessionID uuid.UUID) ([]domain.BankDepositTransaction, error)

	// AccumulateReceived adds an amount to the session's received_amount.
	AccumulateReceived(ctx context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error

	// RecordBankDepositTx atomically creates a bank deposit transaction and
	// accumulates the received amount on the session in a single database transaction.
	RecordBankDepositTx(ctx context.Context, tx *domain.BankDepositTransaction, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error

	// GetExpiredPendingSessions returns sessions in PENDING_PAYMENT with expires_at < now().
	GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.BankDepositSession, error)

	// ListVirtualAccountsByTenant returns all virtual accounts for a tenant.
	ListVirtualAccountsByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.VirtualAccountPool, error)

	// ListVirtualAccountsPaginated returns a paginated, filterable list of virtual accounts.
	ListVirtualAccountsPaginated(ctx context.Context, params VirtualAccountListParams) ([]domain.VirtualAccountPool, int64, error)

	// ListVirtualAccountsCursor returns virtual accounts using cursor-based pagination (created_at > cursor, ASC).
	ListVirtualAccountsCursor(ctx context.Context, params VirtualAccountCursorParams) ([]domain.VirtualAccountPool, error)

	// CountAvailableVirtualAccountsByCurrency returns available account counts grouped by currency.
	CountAvailableVirtualAccountsByCurrency(ctx context.Context, tenantID uuid.UUID) (map[string]int64, error)

	// GetVirtualAccountIndexByNumber retrieves the account index entry by account number.
	GetVirtualAccountIndexByNumber(ctx context.Context, accountNumber string) (*VirtualAccountIndex, error)
}

// TenantStore is the bank deposit engine's port for reading tenant configuration.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}

// VirtualAccountListParams holds the filters for paginated virtual account listing.
type VirtualAccountListParams struct {
	TenantID    uuid.UUID
	Currency    string // empty string = no filter
	AccountType string // empty string = no filter
	Limit       int32
	Offset      int32
}

// VirtualAccountCursorParams holds the filters for cursor-based virtual account listing.
type VirtualAccountCursorParams struct {
	TenantID    uuid.UUID
	Currency    string    // empty string = no filter
	AccountType string    // empty string = no filter
	PageSize    int32
	Cursor      time.Time // created_at > cursor
}

// VirtualAccountIndex is a lookup type that maps a virtual account number to its
// owning tenant and optional session. Used for routing incoming bank credits.
type VirtualAccountIndex struct {
	AccountNumber string
	TenantID      uuid.UUID
	SessionID     *uuid.UUID
	AccountType   domain.VirtualAccountType
}
