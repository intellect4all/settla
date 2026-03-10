package domain

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// CompensationStrategy identifies the compensation approach for a failed transfer.
type CompensationStrategy string

const (
	// CompensationSimpleRefund means nothing completed beyond funding — release
	// treasury reservation and reverse any ledger entries.
	CompensationSimpleRefund CompensationStrategy = "SIMPLE_REFUND"

	// CompensationReverseOnRamp means the on-ramp completed (stablecoins acquired)
	// but the off-ramp failed — sell stablecoins back to source currency.
	// The tenant bears any FX loss from rate movement.
	CompensationReverseOnRamp CompensationStrategy = "REVERSE_ONRAMP"

	// CompensationCreditStablecoin means the on-ramp completed but off-ramp failed
	// — credit the tenant's stablecoin position instead of converting back.
	CompensationCreditStablecoin CompensationStrategy = "CREDIT_STABLECOIN"

	// CompensationManualReview means the state is ambiguous and needs human
	// investigation before any automated compensation can proceed.
	CompensationManualReview CompensationStrategy = "MANUAL_REVIEW"
)

// CompensationPlan describes what the system should do to compensate a failed
// transfer. It includes the strategy, refund amounts, FX loss, and the ordered
// list of steps to execute.
type CompensationPlan struct {
	Strategy       CompensationStrategy
	TransferID     uuid.UUID
	TenantID       uuid.UUID
	RefundAmount   decimal.Decimal // amount to refund (in RefundCurrency)
	RefundCurrency Currency
	FXLoss         decimal.Decimal // FX loss from reversal (>= 0)
	Steps          []CompensationStep
	TransferStatus TransferStatus // current status when plan was created
}

// CompensationStep is a single action in a compensation plan. Each step maps
// to an outbox intent that a worker will execute.
type CompensationStep struct {
	Type    string // e.g. "treasury.release", "ledger.reverse", "provider.reverse_onramp", "position.credit"
	Payload []byte // JSON-encoded intent payload
}
