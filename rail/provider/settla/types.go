package settla

import (
	"time"

	"github.com/shopspring/decimal"
)

// FiatStatus represents the lifecycle state of a simulated fiat transaction.
type FiatStatus string

const (
	// Collection statuses
	FiatStatusPending    FiatStatus = "PENDING"
	FiatStatusProcessing FiatStatus = "PROCESSING"
	FiatStatusCollected  FiatStatus = "COLLECTED"

	// Payout statuses
	FiatStatusPayoutInitiated  FiatStatus = "PAYOUT_INITIATED"
	FiatStatusPayoutProcessing FiatStatus = "PAYOUT_PROCESSING"
	FiatStatusCompleted        FiatStatus = "COMPLETED"

	// Terminal failure status
	FiatStatusFailed FiatStatus = "FAILED"
)

// FiatTxType distinguishes collection (inbound fiat) from payout (outbound fiat).
type FiatTxType string

const (
	FiatTxCollection FiatTxType = "COLLECTION"
	FiatTxPayout     FiatTxType = "PAYOUT"
)

// StatusChange records a single state transition in a fiat transaction's lifecycle.
type StatusChange struct {
	Status    FiatStatus
	Timestamp time.Time
	Reason    string
}

// FiatTransaction is the internal record of a simulated fiat rail transaction.
type FiatTransaction struct {
	ID          string
	Type        FiatTxType
	Amount      decimal.Decimal
	Currency    string
	Reference   string // collection ref or payout recipient reference
	BankRef     string // simulated bank/PSP reference
	Status      FiatStatus
	History     []StatusChange
	CreatedAt   time.Time
	CompletedAt *time.Time
}
