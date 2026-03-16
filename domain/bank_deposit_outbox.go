package domain

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Bank deposit intent constants — workers consume these from NATS and execute the side effect.
const (
	// IntentBankDepositCredit instructs the ledger+treasury worker to credit the tenant.
	IntentBankDepositCredit = "bank_deposit.credit"
	// IntentBankDepositSettle instructs the settlement worker to convert fiat to stablecoin.
	IntentBankDepositSettle = "bank_deposit.settle"
	// IntentBankDepositRefund instructs the banking partner to refund the payment.
	IntentBankDepositRefund = "bank_deposit.refund"
	// IntentRecycleVirtualAccount instructs the pool manager to recycle a virtual account.
	IntentRecycleVirtualAccount = "bank_deposit.recycle_account"
)

// Bank deposit event constants — published after state transitions.
const (
	EventBankDepositSessionCreated   = "bank_deposit.session.created"
	EventBankDepositPaymentReceived  = "bank_deposit.payment.received"
	EventBankDepositSessionCrediting = "bank_deposit.session.crediting"
	EventBankDepositSessionCredited  = "bank_deposit.session.credited"
	EventBankDepositSessionSettling  = "bank_deposit.session.settling"
	EventBankDepositSessionSettled   = "bank_deposit.session.settled"
	EventBankDepositSessionHeld      = "bank_deposit.session.held"
	EventBankDepositSessionExpired   = "bank_deposit.session.expired"
	EventBankDepositSessionFailed    = "bank_deposit.session.failed"
	EventBankDepositSessionCancelled = "bank_deposit.session.cancelled"
	EventBankDepositUnderpaid        = "bank_deposit.underpaid"
	EventBankDepositOverpaid         = "bank_deposit.overpaid"
	EventBankDepositLatePayment      = "bank_deposit.late_payment"
)

// CreditBankDepositPayload is the payload for IntentBankDepositCredit.
type CreditBankDepositPayload struct {
	SessionID      uuid.UUID       `json:"session_id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	Currency       Currency        `json:"currency"`
	GrossAmount    decimal.Decimal `json:"gross_amount"`
	FeeAmount      decimal.Decimal `json:"fee_amount"`
	NetAmount      decimal.Decimal `json:"net_amount"`
	BankReference  string          `json:"bank_reference"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// SettleBankDepositPayload is the payload for IntentBankDepositSettle.
type SettleBankDepositPayload struct {
	SessionID  uuid.UUID       `json:"session_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Currency   Currency        `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	TargetFiat Currency        `json:"target_fiat"`
}

// RefundBankDepositPayload is the payload for IntentBankDepositRefund.
type RefundBankDepositPayload struct {
	SessionID     uuid.UUID       `json:"session_id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	AccountNumber string          `json:"account_number"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      Currency        `json:"currency"`
	Reason        string          `json:"reason"`
}

// RecycleVirtualAccountPayload is the payload for IntentRecycleVirtualAccount.
type RecycleVirtualAccountPayload struct {
	AccountID        uuid.UUID `json:"account_id"`
	AccountNumber    string    `json:"account_number"`
	BankingPartnerID string    `json:"banking_partner_id"`
}

// BankDepositSessionEventPayload is a generic payload for bank deposit session lifecycle events.
type BankDepositSessionEventPayload struct {
	SessionID uuid.UUID                `json:"session_id"`
	TenantID  uuid.UUID                `json:"tenant_id"`
	Status    BankDepositSessionStatus `json:"status"`
	Currency  string                   `json:"currency,omitempty"`
	Amount    decimal.Decimal          `json:"amount,omitempty"`
	Reason    string                   `json:"reason,omitempty"`
}
