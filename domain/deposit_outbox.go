package domain

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Deposit intent constants — workers consume these from NATS and execute the side effect.
const (
	// IntentMonitorAddress instructs the chain monitor to watch a deposit address.
	IntentMonitorAddress = "deposit.monitor.address"
	// IntentCreditDeposit instructs the ledger+treasury worker to credit the tenant.
	IntentCreditDeposit = "deposit.credit"
	// IntentSettleDeposit instructs the settlement worker to convert crypto to fiat.
	IntentSettleDeposit = "deposit.settle"
)

// Deposit event constants — published after state transitions.
const (
	EventDepositSessionCreated   = "deposit.session.created"
	EventDepositTxDetected       = "deposit.tx.detected"
	EventDepositTxConfirmed      = "deposit.tx.confirmed"
	EventDepositSessionCrediting = "deposit.session.crediting"
	EventDepositSessionCredited  = "deposit.session.credited"
	EventDepositSessionSettling  = "deposit.session.settling"
	EventDepositSessionSettled   = "deposit.session.settled"
	EventDepositSessionHeld      = "deposit.session.held"
	EventDepositSessionExpired   = "deposit.session.expired"
	EventDepositSessionFailed    = "deposit.session.failed"
	EventDepositSessionCancelled = "deposit.session.cancelled"
	EventDepositLatePayment      = "deposit.late_payment"
)

// MonitorAddressPayload is the payload for IntentMonitorAddress.
type MonitorAddressPayload struct {
	SessionID uuid.UUID `json:"session_id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Chain     CryptoChain `json:"chain"`
	Address   string      `json:"address"`
	Token     string    `json:"token"`
}

// CreditDepositPayload is the payload for IntentCreditDeposit.
type CreditDepositPayload struct {
	SessionID      uuid.UUID       `json:"session_id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	Chain          CryptoChain     `json:"chain"`
	Token          string          `json:"token"`
	GrossAmount    decimal.Decimal `json:"gross_amount"`
	FeeAmount      decimal.Decimal `json:"fee_amount"`
	NetAmount      decimal.Decimal `json:"net_amount"`
	TxHash         string          `json:"tx_hash"`
	IdempotencyKey IdempotencyKey  `json:"idempotency_key"`
}

// SettleDepositPayload is the payload for IntentSettleDeposit.
type SettleDepositPayload struct {
	SessionID  uuid.UUID       `json:"session_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Chain      CryptoChain     `json:"chain"`
	Token      string          `json:"token"`
	Amount     decimal.Decimal `json:"amount"`
	TargetFiat Currency        `json:"target_fiat"`
}

// DepositTxDetectedPayload is the payload for EventDepositTxDetected.
type DepositTxDetectedPayload struct {
	SessionID   uuid.UUID       `json:"session_id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	TxHash      string          `json:"tx_hash"`
	Chain       CryptoChain     `json:"chain"`
	Token       string          `json:"token"`
	Amount      decimal.Decimal `json:"amount"`
	BlockNumber int64           `json:"block_number"`
}

// DepositTxConfirmedPayload is the payload for EventDepositTxConfirmed.
type DepositTxConfirmedPayload struct {
	SessionID     uuid.UUID       `json:"session_id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	TxHash        string          `json:"tx_hash"`
	Chain         CryptoChain     `json:"chain"`
	Token         string          `json:"token"`
	Amount        decimal.Decimal `json:"amount"`
	Confirmations int32           `json:"confirmations"`
}

// DepositSessionEventPayload is a generic payload for deposit session lifecycle events.
type DepositSessionEventPayload struct {
	SessionID uuid.UUID            `json:"session_id"`
	TenantID  uuid.UUID            `json:"tenant_id"`
	Status    DepositSessionStatus `json:"status"`
	Chain     CryptoChain          `json:"chain,omitempty"`
	Token     string               `json:"token,omitempty"`
	Amount    decimal.Decimal      `json:"amount,omitempty"`
	Reason    string               `json:"reason,omitempty"`
}
