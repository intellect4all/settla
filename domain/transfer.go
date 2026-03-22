package domain

import (
	"context"
	"fmt"
	"strings"
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

// Validate checks that TotalFeeUSD equals the sum of component fees and that
// no individual fee is negative. Returns an error describing any inconsistency.
func (f FeeBreakdown) Validate() error {
	if f.OnRampFee.IsNegative() {
		return fmt.Errorf("settla-domain: FeeBreakdown.OnRampFee must be non-negative, got %s", f.OnRampFee.String())
	}
	if f.NetworkFee.IsNegative() {
		return fmt.Errorf("settla-domain: FeeBreakdown.NetworkFee must be non-negative, got %s", f.NetworkFee.String())
	}
	if f.OffRampFee.IsNegative() {
		return fmt.Errorf("settla-domain: FeeBreakdown.OffRampFee must be non-negative, got %s", f.OffRampFee.String())
	}
	expectedTotal := f.OnRampFee.Add(f.NetworkFee).Add(f.OffRampFee)
	if !f.TotalFeeUSD.Equal(expectedTotal) {
		return fmt.Errorf("settla-domain: FeeBreakdown.TotalFeeUSD (%s) does not equal sum of components (%s + %s + %s = %s)",
			f.TotalFeeUSD.String(), f.OnRampFee.String(), f.NetworkFee.String(), f.OffRampFee.String(), expectedTotal.String())
	}
	return nil
}

// ValidateWithSchedule validates the fee breakdown against the tenant's fee schedule bounds.
func (f FeeBreakdown) ValidateWithSchedule(schedule FeeSchedule) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if !schedule.MinFeeUSD.IsZero() && f.TotalFeeUSD.LessThan(schedule.MinFeeUSD) {
		return fmt.Errorf("settla-domain: FeeBreakdown.TotalFeeUSD (%s) is below schedule minimum (%s)",
			f.TotalFeeUSD.String(), schedule.MinFeeUSD.String())
	}
	if !schedule.MaxFeeUSD.IsZero() && f.TotalFeeUSD.GreaterThan(schedule.MaxFeeUSD) {
		return fmt.Errorf("settla-domain: FeeBreakdown.TotalFeeUSD (%s) exceeds schedule maximum (%s)",
			f.TotalFeeUSD.String(), schedule.MaxFeeUSD.String())
	}
	return nil
}

// BlockchainTx records a single blockchain transaction associated with a transfer.
type BlockchainTx struct {
	Chain       CryptoChain // e.g. ChainTron, ChainEthereum
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
	Chain        CryptoChain

	FXRate              decimal.Decimal
	Fees                FeeBreakdown
	FeeScheduleSnapshot *FeeSchedule `json:"fee_schedule_snapshot,omitempty"`

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

// Validate checks that the sender has the minimum required fields populated.
func (s Sender) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("settla-domain: sender name is required")
	}
	if s.Email != "" && !strings.Contains(s.Email, "@") {
		return fmt.Errorf("settla-domain: sender email %q is not a valid email address", s.Email)
	}
	return nil
}

// CryptoChain represents a supported blockchain network.
type CryptoChain string

const (
	ChainTron     CryptoChain = "tron"
	ChainEthereum CryptoChain = "ethereum"
	ChainSolana   CryptoChain = "solana"
	ChainBase     CryptoChain = "base"
	ChainPolygon  CryptoChain = "polygon"
	ChainArbitrum CryptoChain = "arbitrum"
)

// String returns the chain name.
func (c CryptoChain) String() string { return string(c) }

// IsEVM returns true if the chain is EVM-compatible.
func (c CryptoChain) IsEVM() bool {
	return c == ChainEthereum || c == ChainBase || c == ChainPolygon || c == ChainArbitrum
}

// SupportedChains is the set of blockchain chains supported by Settla.
var SupportedChains = map[CryptoChain]struct{}{
	ChainEthereum: {},
	ChainTron:     {},
	ChainSolana:   {},
	ChainPolygon:  {},
	ChainBase:     {},
	ChainArbitrum: {},
}

// ValidateChain returns an error if chain is not in SupportedChains.
func ValidateChain(chain CryptoChain) error {
	if _, ok := SupportedChains[chain]; !ok {
		return fmt.Errorf("settla-domain: unsupported blockchain chain %q", chain)
	}
	return nil
}

// ValidChains returns all supported chains.
func ValidChains() []CryptoChain {
	chains := make([]CryptoChain, 0, len(SupportedChains))
	for c := range SupportedChains {
		chains = append(chains, c)
	}
	return chains
}

// Validate checks that the recipient has the minimum required fields populated.
// Returns an error describing any missing or invalid field.
func (r Recipient) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("settla-domain: recipient name is required")
	}
	if r.Country == "" {
		return fmt.Errorf("settla-domain: recipient country is required")
	}
	if len(r.BankName) > 128 {
		return fmt.Errorf("settla-domain: recipient bank_name must be at most 128 characters, got %d", len(r.BankName))
	}
	if r.AccountNumber != "" {
		if len(r.AccountNumber) < 4 || len(r.AccountNumber) > 34 {
			return fmt.Errorf("settla-domain: recipient account_number must be 4-34 characters, got %d", len(r.AccountNumber))
		}
		for _, c := range r.AccountNumber {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-') {
				return fmt.Errorf("settla-domain: recipient account_number contains invalid character %q", string(c))
			}
		}
		if r.BankName == "" {
			return fmt.Errorf("settla-domain: recipient bank_name is required when account_number is provided")
		}
	}
	return nil
}

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
