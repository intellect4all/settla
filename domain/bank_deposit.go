package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BankDepositSessionStatus represents a point in the bank deposit session state machine.
type BankDepositSessionStatus string

const (
	// BankDepositSessionStatusPendingPayment is the initial state — waiting for bank transfer.
	BankDepositSessionStatusPendingPayment BankDepositSessionStatus = "PENDING_PAYMENT"
	// BankDepositSessionStatusPaymentReceived indicates the bank credit has been matched to this session.
	BankDepositSessionStatusPaymentReceived BankDepositSessionStatus = "PAYMENT_RECEIVED"
	// BankDepositSessionStatusCrediting indicates the ledger credit + treasury update is in progress.
	BankDepositSessionStatusCrediting BankDepositSessionStatus = "CREDITING"
	// BankDepositSessionStatusCredited indicates the tenant's ledger has been credited.
	BankDepositSessionStatusCredited BankDepositSessionStatus = "CREDITED"
	// BankDepositSessionStatusSettling indicates conversion to stablecoin is in progress.
	BankDepositSessionStatusSettling BankDepositSessionStatus = "SETTLING"
	// BankDepositSessionStatusSettled is the terminal success state after stablecoin conversion.
	BankDepositSessionStatusSettled BankDepositSessionStatus = "SETTLED"
	// BankDepositSessionStatusHeld is the terminal state when fiat is held (HOLD preference).
	BankDepositSessionStatusHeld BankDepositSessionStatus = "HELD"
	// BankDepositSessionStatusExpired indicates the session timed out without payment.
	BankDepositSessionStatusExpired BankDepositSessionStatus = "EXPIRED"
	// BankDepositSessionStatusFailed indicates an unrecoverable error occurred.
	BankDepositSessionStatusFailed BankDepositSessionStatus = "FAILED"
	// BankDepositSessionStatusCancelled indicates the session was cancelled before payment.
	BankDepositSessionStatusCancelled BankDepositSessionStatus = "CANCELLED"
	// BankDepositSessionStatusUnderpaid indicates the received amount is below the expected amount.
	BankDepositSessionStatusUnderpaid BankDepositSessionStatus = "UNDERPAID"
	// BankDepositSessionStatusOverpaid indicates the received amount exceeds the expected amount.
	BankDepositSessionStatusOverpaid BankDepositSessionStatus = "OVERPAID"
)

// VirtualAccountType distinguishes permanent (reusable) from temporary (single-use) virtual accounts.
type VirtualAccountType string

const (
	// VirtualAccountTypePermanent is a reusable virtual account assigned to a tenant.
	VirtualAccountTypePermanent VirtualAccountType = "PERMANENT"
	// VirtualAccountTypeTemporary is a single-use virtual account for one deposit session.
	VirtualAccountTypeTemporary VirtualAccountType = "TEMPORARY"
)

// PaymentMismatchPolicy determines how the system handles underpayment or overpayment.
type PaymentMismatchPolicy string

const (
	// PaymentMismatchPolicyAccept accepts mismatched payments and credits the received amount.
	PaymentMismatchPolicyAccept PaymentMismatchPolicy = "ACCEPT"
	// PaymentMismatchPolicyReject rejects mismatched payments and initiates a refund.
	PaymentMismatchPolicyReject PaymentMismatchPolicy = "REJECT"
)

// ValidBankDepositTransitions defines the allowed state machine transitions for bank deposit sessions.
// PENDING_PAYMENT → PAYMENT_RECEIVED → CREDITING → CREDITED → SETTLING → SETTLED
// Alternative paths: CREDITED → HELD (hold preference), mismatch → UNDERPAID/OVERPAID,
// late payment after EXPIRED/CANCELLED → PAYMENT_RECEIVED.
var ValidBankDepositTransitions = map[BankDepositSessionStatus][]BankDepositSessionStatus{
	BankDepositSessionStatusPendingPayment:  {BankDepositSessionStatusPaymentReceived, BankDepositSessionStatusExpired, BankDepositSessionStatusCancelled},
	BankDepositSessionStatusPaymentReceived: {BankDepositSessionStatusCrediting, BankDepositSessionStatusUnderpaid, BankDepositSessionStatusOverpaid, BankDepositSessionStatusFailed},
	BankDepositSessionStatusCrediting:       {BankDepositSessionStatusCredited, BankDepositSessionStatusFailed},
	BankDepositSessionStatusCredited:        {BankDepositSessionStatusSettling, BankDepositSessionStatusHeld},
	BankDepositSessionStatusSettling:        {BankDepositSessionStatusSettled, BankDepositSessionStatusFailed},
	BankDepositSessionStatusUnderpaid:       {BankDepositSessionStatusFailed},          // REJECT policy terminal
	BankDepositSessionStatusOverpaid:        {BankDepositSessionStatusFailed},          // REJECT policy terminal
	BankDepositSessionStatusExpired:         {BankDepositSessionStatusPaymentReceived}, // late payment after expiry
	BankDepositSessionStatusCancelled:       {BankDepositSessionStatusPaymentReceived}, // late payment after cancel
	BankDepositSessionStatusFailed:          {},                                        // terminal
	BankDepositSessionStatusSettled:         {},                                        // terminal
	BankDepositSessionStatusHeld:            {},                                        // terminal
}

// BankDepositSession is the core domain aggregate representing a bank deposit request.
// A tenant creates a session, receives a virtual account number, and the system monitors for payment.
// All sessions are tenant-scoped and use optimistic locking via Version.
type BankDepositSession struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	IdempotencyKey IdempotencyKey
	Status         BankDepositSessionStatus
	Version        int64

	// Virtual account details
	BankingPartnerID string             // banking partner that provisioned the account
	AccountNumber    string             // virtual account number / sort code destination
	AccountName      string             // name on the virtual account
	SortCode         string             // UK sort code (or equivalent routing number)
	IBAN             string             // International Bank Account Number
	AccountType      VirtualAccountType // PERMANENT or TEMPORARY

	// Payment details
	Currency       Currency        // fiat currency (e.g. CurrencyGBP, CurrencyEUR)
	ExpectedAmount decimal.Decimal // amount requested by tenant
	MinAmount      decimal.Decimal // minimum acceptable amount (tolerance)
	MaxAmount      decimal.Decimal // maximum acceptable amount (tolerance)
	ReceivedAmount decimal.Decimal // actual amount received via bank transfer

	// Fee details
	FeeAmount        decimal.Decimal // calculated fee (deducted from received)
	NetAmount        decimal.Decimal // received - fee = net credited to tenant
	MismatchPolicy   PaymentMismatchPolicy
	CollectionFeeBPS int // fee basis points at session creation time

	// Settlement
	SettlementPref       SettlementPreference
	SettlementTransferID *uuid.UUID // linked transfer if AUTO_CONVERT

	// Payer details (populated when payment is received)
	PayerName      string
	PayerReference string
	BankReference  string

	// Timing
	ExpiresAt         time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	PaymentReceivedAt *time.Time
	CreditedAt        *time.Time
	SettledAt         *time.Time
	ExpiredAt         *time.Time
	FailedAt          *time.Time

	FailureReason string
	FailureCode   string

	Metadata map[string]string
}

// CanTransitionTo returns true if the state machine allows a transition
// from the current status to the target status.
func (s *BankDepositSession) CanTransitionTo(target BankDepositSessionStatus) bool {
	allowed, ok := ValidBankDepositTransitions[s.Status]
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
func (s *BankDepositSession) TransitionTo(target BankDepositSessionStatus) error {
	if !s.CanTransitionTo(target) {
		return ErrInvalidTransition(string(s.Status), string(target))
	}
	s.Status = target
	s.Version++
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// IsTerminal returns true if the session is in a terminal state.
func (s *BankDepositSession) IsTerminal() bool {
	switch s.Status {
	case BankDepositSessionStatusSettled, BankDepositSessionStatusHeld,
		BankDepositSessionStatusFailed, BankDepositSessionStatusCancelled:
		return true
	default:
		return false
	}
}

// BankDepositTransaction records a single bank credit associated with a deposit session.
type BankDepositTransaction struct {
	ID                 uuid.UUID
	SessionID          uuid.UUID
	TenantID           uuid.UUID
	BankReference      string
	PayerName          string
	PayerAccountNumber string
	Amount             decimal.Decimal
	Currency           Currency
	ReceivedAt         time.Time
	CreatedAt          time.Time
}

// IncomingBankCredit is a raw bank credit notification from a banking partner.
// It carries enough data for the engine to match it to a bank deposit session.
type IncomingBankCredit struct {
	AccountNumber      string
	Amount             decimal.Decimal
	Currency           Currency
	PayerName          string
	PayerAccountNumber string
	PayerReference     string
	BankReference      string
	ReceivedAt         time.Time
}

// VirtualAccountPool represents a pre-provisioned virtual account in the pool.
type VirtualAccountPool struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	BankingPartnerID string
	AccountNumber    string
	AccountName      string
	SortCode         string
	IBAN             string
	Currency         Currency
	AccountType      VirtualAccountType
	Available        bool
	SessionID        *uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TenantBankConfig holds per-tenant bank deposit configuration.
type TenantBankConfig struct {
	BankDepositsEnabled     bool                  `json:"bank_deposits_enabled"`
	DefaultBankingPartner   string                `json:"default_banking_partner"`
	BankSupportedCurrencies []Currency            `json:"bank_supported_currencies"`
	DefaultMismatchPolicy   PaymentMismatchPolicy `json:"default_mismatch_policy"`
	DefaultSessionTTLSecs   int32                 `json:"default_session_ttl_secs"`
}
