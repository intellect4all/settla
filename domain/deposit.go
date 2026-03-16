package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// DepositSessionStatus represents a point in the deposit session state machine.
type DepositSessionStatus string

const (
	// DepositSessionStatusPendingPayment is the initial state — waiting for on-chain payment.
	DepositSessionStatusPendingPayment DepositSessionStatus = "PENDING_PAYMENT"
	// DepositSessionStatusDetected indicates an on-chain transaction was found but not yet confirmed.
	DepositSessionStatusDetected DepositSessionStatus = "DETECTED"
	// DepositSessionStatusConfirmed indicates the on-chain transaction has enough confirmations.
	DepositSessionStatusConfirmed DepositSessionStatus = "CONFIRMED"
	// DepositSessionStatusCrediting indicates the ledger credit + treasury update is in progress.
	DepositSessionStatusCrediting DepositSessionStatus = "CREDITING"
	// DepositSessionStatusCredited indicates the tenant's ledger has been credited.
	DepositSessionStatusCredited DepositSessionStatus = "CREDITED"
	// DepositSessionStatusSettling indicates conversion to fiat is in progress (AUTO_CONVERT).
	DepositSessionStatusSettling DepositSessionStatus = "SETTLING"
	// DepositSessionStatusSettled is the terminal success state after fiat conversion.
	DepositSessionStatusSettled DepositSessionStatus = "SETTLED"
	// DepositSessionStatusHeld is the terminal state when crypto is held (HOLD preference).
	DepositSessionStatusHeld DepositSessionStatus = "HELD"
	// DepositSessionStatusExpired indicates the session timed out without payment.
	DepositSessionStatusExpired DepositSessionStatus = "EXPIRED"
	// DepositSessionStatusFailed indicates an unrecoverable error occurred.
	DepositSessionStatusFailed DepositSessionStatus = "FAILED"
	// DepositSessionStatusCancelled indicates the session was cancelled before payment.
	DepositSessionStatusCancelled DepositSessionStatus = "CANCELLED"
)

// ValidDepositTransitions defines the allowed state machine transitions for deposit sessions.
// PENDING_PAYMENT → DETECTED → CONFIRMED → CREDITING → CREDITED → SETTLING → SETTLED
// Alternative paths: CREDITED → HELD (hold preference), any pre-payment state → EXPIRED/CANCELLED.
var ValidDepositTransitions = map[DepositSessionStatus][]DepositSessionStatus{
	DepositSessionStatusPendingPayment: {DepositSessionStatusDetected, DepositSessionStatusExpired, DepositSessionStatusCancelled},
	DepositSessionStatusDetected:       {DepositSessionStatusConfirmed, DepositSessionStatusPendingPayment, DepositSessionStatusFailed},
	DepositSessionStatusConfirmed:      {DepositSessionStatusCrediting, DepositSessionStatusFailed},
	DepositSessionStatusCrediting:      {DepositSessionStatusCredited, DepositSessionStatusFailed},
	DepositSessionStatusCredited:       {DepositSessionStatusSettling, DepositSessionStatusHeld},
	DepositSessionStatusSettling:       {DepositSessionStatusSettled, DepositSessionStatusFailed},
	DepositSessionStatusExpired:        {DepositSessionStatusDetected}, // late payment after expiry
	DepositSessionStatusFailed:         {},                             // terminal
	DepositSessionStatusSettled:        {},                             // terminal
	DepositSessionStatusHeld:           {},                             // terminal
	DepositSessionStatusCancelled:      {DepositSessionStatusDetected}, // late payment after cancel
}

// SettlementPreference determines what happens after a deposit is credited.
type SettlementPreference string

const (
	// SettlementPreferenceAutoConvert converts crypto to fiat automatically.
	SettlementPreferenceAutoConvert SettlementPreference = "AUTO_CONVERT"
	// SettlementPreferenceHold keeps the crypto balance without conversion.
	SettlementPreferenceHold SettlementPreference = "HOLD"
	// SettlementPreferenceThreshold accumulates until a threshold, then converts.
	SettlementPreferenceThreshold SettlementPreference = "THRESHOLD"
)

// DepositSession is the core domain aggregate representing a crypto deposit request.
// A tenant creates a session, receives a deposit address, and the system monitors for payment.
// All sessions are tenant-scoped and use optimistic locking via Version.
type DepositSession struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	IdempotencyKey string
	Status         DepositSessionStatus
	Version        int64

	// Payment details
	Chain          string          // e.g. "tron", "ethereum", "base"
	Token          string          // e.g. "USDT", "USDC"
	DepositAddress string          // derived HD wallet address
	ExpectedAmount decimal.Decimal // amount requested by tenant
	ReceivedAmount decimal.Decimal // actual amount received on-chain
	Currency       Currency        // token currency (e.g. CurrencyUSDT)

	// Fee details
	CollectionFeeBPS int             // fee basis points at session creation time
	FeeAmount        decimal.Decimal // calculated fee (deducted from received)
	NetAmount        decimal.Decimal // received - fee = net credited to tenant

	// Settlement
	SettlementPref    SettlementPreference
	SettlementTransferID *uuid.UUID // linked transfer if AUTO_CONVERT

	// Address derivation
	DerivationIndex int64 // HD wallet derivation index

	// Timing
	ExpiresAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DetectedAt  *time.Time
	ConfirmedAt *time.Time
	CreditedAt  *time.Time
	SettledAt   *time.Time
	ExpiredAt   *time.Time
	FailedAt    *time.Time

	FailureReason string
	FailureCode   string

	Metadata map[string]string
}

// CanTransitionTo returns true if the state machine allows a transition
// from the current status to the target status.
func (s *DepositSession) CanTransitionTo(target DepositSessionStatus) bool {
	allowed, ok := ValidDepositTransitions[s.Status]
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
// Returns ErrInvalidTransition if the transition is not allowed.
// Mutates Status and increments Version.
func (s *DepositSession) TransitionTo(target DepositSessionStatus) error {
	if !s.CanTransitionTo(target) {
		return ErrInvalidTransition(string(s.Status), string(target))
	}
	s.Status = target
	s.Version++
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// IsTerminal returns true if the session is in a terminal state.
func (s *DepositSession) IsTerminal() bool {
	switch s.Status {
	case DepositSessionStatusSettled, DepositSessionStatusHeld,
		DepositSessionStatusFailed, DepositSessionStatusCancelled:
		return true
	default:
		return false
	}
}

// DepositTransaction records a single on-chain transaction associated with a deposit session.
type DepositTransaction struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	TenantID  uuid.UUID

	Chain           string
	TxHash          string
	FromAddress     string
	ToAddress       string
	TokenContract   string
	Amount          decimal.Decimal
	BlockNumber     int64
	BlockHash       string
	Confirmations   int32
	RequiredConfirm int32
	Confirmed       bool

	DetectedAt  time.Time
	ConfirmedAt *time.Time
	CreatedAt   time.Time
}

// IncomingTransaction is a raw on-chain transaction detected by the chain monitor.
// It carries enough data for the engine to match it to a deposit session.
type IncomingTransaction struct {
	Chain         string
	TxHash        string
	FromAddress   string
	ToAddress     string
	TokenContract string
	Amount        decimal.Decimal
	BlockNumber   int64
	BlockHash     string
	Timestamp     time.Time
}

// CryptoAddressPool represents a pre-generated deposit address in the pool.
type CryptoAddressPool struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	Chain           string
	Address         string
	DerivationIndex int64
	Dispensed       bool
	DispensedAt     *time.Time
	SessionID       *uuid.UUID
	CreatedAt       time.Time
}

// BlockCheckpoint tracks the last scanned block per chain for the chain monitor.
type BlockCheckpoint struct {
	ID          uuid.UUID
	Chain       string
	BlockNumber int64
	BlockHash   string
	UpdatedAt   time.Time
}

// Token represents a supported token on a specific chain.
type Token struct {
	ID              uuid.UUID
	Chain           string // e.g. "tron", "ethereum", "base"
	Symbol          string // e.g. "USDT", "USDC"
	ContractAddress string
	Decimals        int32
	IsActive        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TenantCryptoConfig holds per-tenant crypto deposit configuration.
type TenantCryptoConfig struct {
	CryptoEnabled          bool
	DefaultSettlementPref  SettlementPreference
	SupportedChains        []string
	MinConfirmationsTron   int32
	MinConfirmationsEth    int32
	MinConfirmationsBase   int32
	PaymentToleranceBPS    int32
	DefaultSessionTTLSecs  int32
}
