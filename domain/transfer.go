package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TransferStatus represents a point in the transfer state machine.
type TransferStatus string

const (
	// TransferStatusCreated is the initial state after a transfer is submitted.
	TransferStatusCreated TransferStatus = "CREATED"
	// TransferStatusFunded indicates treasury funds have been reserved.
	TransferStatusFunded TransferStatus = "FUNDED"
	// TransferStatusOnRamping indicates fiat→stablecoin conversion is in progress.
	TransferStatusOnRamping TransferStatus = "ON_RAMPING"
	// TransferStatusSettling indicates on-chain settlement is in progress.
	TransferStatusSettling TransferStatus = "SETTLING"
	// TransferStatusOffRamping indicates stablecoin→fiat conversion is in progress.
	TransferStatusOffRamping TransferStatus = "OFF_RAMPING"
	// TransferStatusCompleted is the terminal success state.
	TransferStatusCompleted TransferStatus = "COMPLETED"
	// TransferStatusFailed indicates the transfer encountered an error.
	TransferStatusFailed TransferStatus = "FAILED"
	// TransferStatusRefunding indicates a refund is in progress.
	TransferStatusRefunding TransferStatus = "REFUNDING"
	// TransferStatusRefunded is the terminal state after a successful refund.
	TransferStatusRefunded TransferStatus = "REFUNDED"
)

// ValidTransitions defines the allowed state machine transitions as data.
// The transfer lifecycle: CREATED → FUNDED → ON_RAMPING → SETTLING →
// OFF_RAMPING → COMPLETED. Failures trigger refunds.
var ValidTransitions = map[TransferStatus][]TransferStatus{
	TransferStatusCreated:    {TransferStatusFunded, TransferStatusFailed},
	TransferStatusFunded:     {TransferStatusOnRamping, TransferStatusRefunding},
	TransferStatusOnRamping:  {TransferStatusSettling, TransferStatusRefunding, TransferStatusFailed},
	TransferStatusSettling:   {TransferStatusOffRamping, TransferStatusFailed},
	TransferStatusOffRamping: {TransferStatusCompleted, TransferStatusFailed},
	TransferStatusFailed:     {TransferStatusRefunding},
	TransferStatusRefunding:  {TransferStatusRefunded},
}

// Sender identifies the party initiating the transfer.
type Sender struct {
	ID      uuid.UUID
	Name    string
	Email   string
	Country string
}

// Recipient identifies the party receiving funds.
type Recipient struct {
	Name          string
	AccountNumber string
	SortCode      string
	BankName      string
	Country       string
	IBAN          string
}

// FeeBreakdown itemizes the fees applied to a transfer or quote.
type FeeBreakdown struct {
	OnRampFee   decimal.Decimal
	NetworkFee  decimal.Decimal
	OffRampFee  decimal.Decimal
	TotalFeeUSD decimal.Decimal
}

// BlockchainTx records a single blockchain transaction associated with a transfer.
type BlockchainTx struct {
	Chain       string // e.g. "tron", "ethereum"
	Type        string // "on_ramp", "off_ramp", "settlement"
	TxHash      string
	ExplorerURL string
	Status      string // "pending", "confirmed", "failed"
}

// Transfer is the core domain aggregate representing a settlement request.
// All transfers are tenant-scoped and use optimistic locking via Version.
type Transfer struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ExternalRef    string
	IdempotencyKey string
	Status         TransferStatus
	Version        int64

	SourceCurrency Currency
	SourceAmount   decimal.Decimal
	DestCurrency   Currency
	DestAmount     decimal.Decimal

	StableCoin   Currency
	StableAmount decimal.Decimal
	Chain        string

	FXRate decimal.Decimal
	Fees   FeeBreakdown

	OnRampProviderID  string
	OffRampProviderID string

	Sender    Sender
	Recipient Recipient

	QuoteID *uuid.UUID

	BlockchainTxs []BlockchainTx // on-chain transactions for this transfer

	CreatedAt     time.Time
	UpdatedAt     time.Time
	FundedAt      *time.Time
	CompletedAt   *time.Time
	FailedAt      *time.Time
	FailureReason string
	FailureCode   string
}

// CanTransitionTo returns true if the state machine allows a transition
// from the current status to the target status.
func (t *Transfer) CanTransitionTo(target TransferStatus) bool {
	allowed, ok := ValidTransitions[t.Status]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == target {
			return true
		}
	}
	return false
}

// TransitionTo validates and applies a state transition.
// Returns a TransferEvent capturing the transition, or ErrInvalidTransition
// if the transition is not allowed. Mutates Status and increments Version.
func (t *Transfer) TransitionTo(target TransferStatus) (*TransferEvent, error) {
	if !t.CanTransitionTo(target) {
		return nil, ErrInvalidTransition(string(t.Status), string(target))
	}
	from := t.Status
	t.Status = target
	t.Version++
	t.UpdatedAt = time.Now().UTC()

	event := &TransferEvent{
		ID:         uuid.New(),
		TransferID: t.ID,
		TenantID:   t.TenantID,
		FromStatus: from,
		ToStatus:   target,
		OccurredAt: t.UpdatedAt,
	}
	return event, nil
}

// TransferEvent records a state change on a transfer for audit and event sourcing.
type TransferEvent struct {
	ID          uuid.UUID
	TransferID  uuid.UUID
	TenantID    uuid.UUID
	FromStatus  TransferStatus
	ToStatus    TransferStatus
	OccurredAt  time.Time
	Metadata    map[string]string
	ProviderRef string
}

// IdempotencyKey is a validated idempotency key string.
type IdempotencyKey string

// NewIdempotencyKey validates and returns an IdempotencyKey.
// Returns an error if the key is empty or exceeds 256 characters.
func NewIdempotencyKey(key string) (IdempotencyKey, error) {
	if key == "" {
		return "", fmt.Errorf("settla-domain: idempotency key must not be empty")
	}
	if len(key) > 256 {
		return "", fmt.Errorf("settla-domain: idempotency key exceeds 256 characters")
	}
	return IdempotencyKey(key), nil
}

// String returns the underlying string value.
func (k IdempotencyKey) String() string { return string(k) }

// TransferStore persists transfer aggregates.
type TransferStore interface {
	// Create persists a new transfer.
	Create(ctx context.Context, transfer *Transfer) error
	// Get retrieves a transfer by tenant and ID.
	Get(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*Transfer, error)
	// GetByIdempotencyKey retrieves a transfer by tenant and idempotency key.
	GetByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*Transfer, error)
	// UpdateStatus persists a status change.
	UpdateStatus(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, status TransferStatus) error
	// List retrieves transfers for a tenant with pagination.
	List(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]Transfer, error)
}
