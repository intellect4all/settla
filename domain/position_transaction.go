package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PositionTxType identifies the kind of position transaction.
type PositionTxType string

const (
	PositionTxTopUp             PositionTxType = "TOP_UP"
	PositionTxWithdrawal        PositionTxType = "WITHDRAWAL"
	PositionTxDepositCredit     PositionTxType = "DEPOSIT_CREDIT"
	PositionTxInternalRebalance PositionTxType = "INTERNAL_REBALANCE"
)

// PositionTxStatus represents a point in the position transaction state machine.
type PositionTxStatus string

const (
	PositionTxStatusPending    PositionTxStatus = "PENDING"
	PositionTxStatusProcessing PositionTxStatus = "PROCESSING"
	PositionTxStatusCompleted  PositionTxStatus = "COMPLETED"
	PositionTxStatusFailed     PositionTxStatus = "FAILED"
)

// ValidPositionTxTransitions defines the allowed state transitions.
// PENDING → PROCESSING → COMPLETED
// PENDING → FAILED (validation failure or immediate rejection)
// PROCESSING → FAILED (treasury/provider operation failed)
var ValidPositionTxTransitions = map[PositionTxStatus][]PositionTxStatus{
	PositionTxStatusPending:    {PositionTxStatusProcessing, PositionTxStatusFailed},
	PositionTxStatusProcessing: {PositionTxStatusCompleted, PositionTxStatusFailed},
	PositionTxStatusCompleted:  {}, // terminal
	PositionTxStatusFailed:     {}, // terminal
}

// PositionTransaction represents a tenant-initiated position change request.
// Top-ups, withdrawals, deposit credits, and internal rebalancing all flow
// through this entity. Each transaction follows the PENDING → PROCESSING →
// COMPLETED/FAILED state machine.
type PositionTransaction struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Type          PositionTxType
	Currency      Currency
	Location      string          // treasury position location, e.g. "bank:gbp", "crypto:tron:usdt"
	Amount        decimal.Decimal // positive amount to credit or debit
	Status        PositionTxStatus
	Method        string // "bank_transfer", "crypto", "internal"
	Destination   string // for withdrawals: bank account ref or crypto address
	Reference     string // external reference (e.g. bank transfer ref)
	FailureReason string
	Version       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CanTransitionTo returns true if the state machine allows the transition.
func (tx *PositionTransaction) CanTransitionTo(target PositionTxStatus) bool {
	allowed, ok := ValidPositionTxTransitions[tx.Status]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == target {
			return true
		}
	}
	return false
}

// TransitionTo validates and applies a state transition.
func (tx *PositionTransaction) TransitionTo(target PositionTxStatus) error {
	if !tx.CanTransitionTo(target) {
		return ErrInvalidTransition(string(tx.Status), string(target))
	}
	tx.Status = target
	tx.Version++
	tx.UpdatedAt = time.Now().UTC()
	return nil
}

// TopUpRequest is the input for requesting a position top-up.
type TopUpRequest struct {
	Currency Currency        `json:"currency"`
	Location string          `json:"location"`
	Amount   decimal.Decimal `json:"amount"`
	Method   string          `json:"method"` // "bank_transfer", "crypto", "internal"
}

// WithdrawalRequest is the input for requesting a position withdrawal.
type WithdrawalRequest struct {
	Currency    Currency        `json:"currency"`
	Location    string          `json:"location"`
	Amount      decimal.Decimal `json:"amount"`
	Method      string          `json:"method"`      // "bank_transfer", "crypto"
	Destination string          `json:"destination"` // bank account ref or crypto address
}
