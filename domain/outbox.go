package domain

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// OutboxEntry represents a domain event or worker intent stored in the transactional outbox.
// The outbox pattern ensures that state changes and their corresponding events/intents
// are written atomically in the same database transaction — eliminating dual-write bugs.
//
// There are two kinds of outbox entries:
//   - Events (IsIntent=false): notifications about something that happened.
//     Workers may subscribe to these but no action is required.
//   - Intents (IsIntent=true): instructions for a worker to execute a side effect.
//     A dedicated worker picks up each intent, executes it, and publishes a result event.
type OutboxEntry struct {
	ID            uuid.UUID
	AggregateType string // "transfer", "position"
	AggregateID   uuid.UUID
	TenantID      uuid.UUID
	CorrelationID uuid.UUID // traces multi-step flows across partition boundaries
	EventType     string    // e.g., "transfer.created", "provider.onramp.execute"
	Payload       []byte    // JSON-encoded intent/event data
	IsIntent      bool      // true = worker should execute this; false = notification only
	Published     bool
	PublishedAt   *time.Time
	RetryCount    int
	MaxRetries    int
	CreatedAt     time.Time
}

// NewOutboxEvent creates a notification outbox entry (not an intent).
// The relay publishes these to NATS for subscribers, but no worker action is required.
// Returns an error if the eventType is not a known outbox event/intent constant.
func NewOutboxEvent(aggregateType string, aggregateID, tenantID uuid.UUID, eventType string, payload []byte) (OutboxEntry, error) {
	if err := ValidateEventType(eventType); err != nil {
		return OutboxEntry{}, err
	}
	return OutboxEntry{
		ID:            uuid.Must(uuid.NewV7()),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		TenantID:      tenantID,
		EventType:     eventType,
		Payload:       payload,
		IsIntent:      false,
		MaxRetries:    5,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// NewOutboxIntent creates an intent outbox entry that a worker should execute.
// The relay publishes these to NATS, where a dedicated worker picks them up,
// executes the side effect, and publishes a result event.
// Returns an error if the eventType is not a known outbox event/intent constant.
func NewOutboxIntent(aggregateType string, aggregateID, tenantID uuid.UUID, eventType string, payload []byte) (OutboxEntry, error) {
	if err := ValidateEventType(eventType); err != nil {
		return OutboxEntry{}, err
	}
	return OutboxEntry{
		ID:            uuid.Must(uuid.NewV7()),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		TenantID:      tenantID,
		EventType:     eventType,
		Payload:       payload,
		IsIntent:      true,
		MaxRetries:    5,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// MustNewOutboxEvent is like NewOutboxEvent but logs and returns a zero entry
// on invalid event types instead of panicking. Callers should check err from
// NewOutboxEvent directly when possible; this variant is kept for backward
// compatibility but no longer panics in production code paths.
func MustNewOutboxEvent(aggregateType string, aggregateID, tenantID uuid.UUID, eventType string, payload []byte) OutboxEntry {
	entry, err := NewOutboxEvent(aggregateType, aggregateID, tenantID, eventType, payload)
	if err != nil {
		slog.Error("settla-domain: invalid outbox event type",
			"event_type", eventType, "error", err)
		return OutboxEntry{} // Zero entry — will be filtered by relay
	}
	return entry
}

// MustNewOutboxIntent is like NewOutboxIntent but logs and returns a zero entry
// on invalid intent types instead of panicking.
func MustNewOutboxIntent(aggregateType string, aggregateID, tenantID uuid.UUID, eventType string, payload []byte) OutboxEntry {
	entry, err := NewOutboxIntent(aggregateType, aggregateID, tenantID, eventType, payload)
	if err != nil {
		slog.Error("settla-domain: invalid outbox intent type",
			"event_type", eventType, "error", err)
		return OutboxEntry{} // Zero entry — will be filtered by relay
	}
	return entry
}

// Intent type constants — workers consume these from NATS and execute the side effect.
const (
	IntentTreasuryReserve       = "treasury.reserve"
	IntentTreasuryRelease       = "treasury.release"
	IntentProviderOnRamp        = "provider.onramp.execute"
	IntentProviderOffRamp       = "provider.offramp.execute"
	IntentProviderReverseOnRamp = "provider.reverse_onramp"
	IntentLedgerPost            = "ledger.post"
	IntentLedgerReverse         = "ledger.reverse"
	IntentBlockchainSend        = "blockchain.send"
	IntentWebhookDeliver        = "webhook.deliver"
	IntentPositionCredit        = "position.credit"
	IntentPositionDebit         = "position.debit"
	IntentTreasuryConsume       = "treasury.consume"
)

// Result event types — workers publish these after executing intents.
const (
	EventTreasuryReserved      = "treasury.reserved"
	EventTreasuryReleased      = "treasury.released"
	EventTreasuryFailed        = "treasury.failed"
	EventProviderOnRampDone    = "provider.onramp.completed"
	EventProviderOnRampFailed  = "provider.onramp.failed"
	EventProviderOffRampDone   = "provider.offramp.completed"
	EventProviderOffRampFailed = "provider.offramp.failed"
	EventLedgerPosted          = "ledger.posted"
	EventLedgerReversed        = "ledger.reversed"
	EventBlockchainConfirmed   = "blockchain.confirmed"
	EventBlockchainFailed      = "blockchain.failed"
	EventPositionCredited      = "position.credited"
	EventPositionDebited       = "position.debited"
	EventTreasuryConsumed      = "treasury.consumed"
)

// knownEventTypes is the set of all valid outbox event/intent types.
// Used by ValidateEventType for runtime validation.
var knownEventTypes = map[string]struct{}{
	// Intents
	IntentTreasuryReserve: {}, IntentTreasuryRelease: {},
	IntentProviderOnRamp: {}, IntentProviderOffRamp: {},
	IntentLedgerPost: {}, IntentLedgerReverse: {},
	IntentBlockchainSend: {}, IntentWebhookDeliver: {}, IntentEmailNotify: {},
	IntentProviderReverseOnRamp: {}, IntentPositionCredit: {},
	IntentPositionDebit: {}, IntentTreasuryConsume: {},
	// Result events
	EventTreasuryReserved: {}, EventTreasuryReleased: {}, EventTreasuryFailed: {},
	EventProviderOnRampDone: {}, EventProviderOnRampFailed: {},
	EventProviderOffRampDone: {}, EventProviderOffRampFailed: {},
	EventLedgerPosted: {}, EventLedgerReversed: {},
	EventBlockchainConfirmed: {}, EventBlockchainFailed: {},
	EventPositionCredited: {}, EventPositionDebited: {}, EventTreasuryConsumed: {},
	// Inbound provider webhooks
	EventProviderOnRampWebhook: {}, EventProviderOffRampWebhook: {},
	// Deposit intents
	IntentMonitorAddress: {}, IntentCreditDeposit: {}, IntentSettleDeposit: {},
	// Deposit events
	EventDepositSessionCreated: {}, EventDepositTxDetected: {}, EventDepositTxConfirmed: {},
	EventDepositSessionCrediting: {}, EventDepositSessionCredited: {},
	EventDepositSessionSettling: {}, EventDepositSessionSettled: {},
	EventDepositSessionHeld: {}, EventDepositSessionExpired: {},
	EventDepositSessionFailed: {}, EventDepositSessionCancelled: {},
	EventDepositLatePayment: {},
	// Bank deposit intents
	IntentBankDepositCredit: {}, IntentBankDepositSettle: {},
	IntentBankDepositRefund: {}, IntentRecycleVirtualAccount: {},
	// Bank deposit events
	EventBankDepositSessionCreated: {}, EventBankDepositPaymentReceived: {},
	EventBankDepositSessionCrediting: {}, EventBankDepositSessionCredited: {},
	EventBankDepositSessionSettling: {}, EventBankDepositSessionSettled: {},
	EventBankDepositSessionHeld: {}, EventBankDepositSessionExpired: {},
	EventBankDepositSessionFailed: {}, EventBankDepositSessionCancelled: {},
	EventBankDepositUnderpaid: {}, EventBankDepositOverpaid: {},
	EventBankDepositLatePayment: {},
	// Lifecycle events (from domain/events.go)
	EventTransferCreated: {}, EventTransferFunded: {},
	EventOnRampInitiated: {}, EventOnRampCompleted: {},
	EventSettlementStarted: {}, EventSettlementCompleted: {},
	EventOffRampInitiated: {}, EventOffRampCompleted: {},
	EventTransferCompleted: {}, EventTransferFailed: {},
	EventRefundInitiated: {}, EventRefundCompleted: {},
	EventPositionUpdated: {}, EventLiquidityAlert: {},
}

// ValidateEventType returns an error if eventType is not a known outbox event/intent constant.
func ValidateEventType(eventType string) error {
	if _, ok := knownEventTypes[eventType]; !ok {
		return fmt.Errorf("settla-domain: unknown outbox event type %q", eventType)
	}
	return nil
}

// TreasuryReservePayload is the payload for IntentTreasuryReserve.
type TreasuryReservePayload struct {
	TransferID uuid.UUID       `json:"transfer_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Currency   Currency        `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	Location   string          `json:"location"`
}

// TreasuryReleasePayload is the payload for IntentTreasuryRelease.
// Reason distinguishes release scenarios for the same transfer (e.g.
// "settlement_failure", "offramp_failure", "transfer_complete") so the
// treasury worker can build a unique idempotency key per release path.
type TreasuryReleasePayload struct {
	TransferID uuid.UUID       `json:"transfer_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Currency   Currency        `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	Location   string          `json:"location"`
	Reason     string          `json:"reason"`
}

// TreasuryConsumePayload is the payload for IntentTreasuryConsume.
// Emitted when a transfer completes — the reserved funds are consumed because
// money physically left the tenant's position.
type TreasuryConsumePayload struct {
	TransferID uuid.UUID       `json:"transfer_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Currency   Currency        `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	Location   string          `json:"location"`
}

// PositionCreditPayload is the payload for IntentPositionCredit.
// Used for deposit credits, manual top-ups, stablecoin compensation, and
// internal rebalancing (destination side).
type PositionCreditPayload struct {
	TenantID  uuid.UUID       `json:"tenant_id"`
	Currency  Currency        `json:"currency"`
	Amount    decimal.Decimal `json:"amount"`
	Location  string          `json:"location"`
	Reference uuid.UUID       `json:"reference"` // source entity ID (session, transaction, transfer)
	RefType   string          `json:"ref_type"`  // "deposit_session", "bank_deposit", "position_transaction", "transfer", "compensation"
}

// PositionDebitPayload is the payload for IntentPositionDebit.
// Used for manual withdrawals and internal rebalancing (source side).
type PositionDebitPayload struct {
	TenantID    uuid.UUID       `json:"tenant_id"`
	Currency    Currency        `json:"currency"`
	Amount      decimal.Decimal `json:"amount"`
	Location    string          `json:"location"`
	Reference   uuid.UUID       `json:"reference"`
	RefType     string          `json:"ref_type"`
	Destination string          `json:"destination,omitempty"` // bank account ref or crypto address
}

// OnRampFallback is a fallback alternative for on-ramp provider execution.
// It carries enough information for the worker to switch providers without
// consulting the engine.
type OnRampFallback struct {
	ProviderID      string          `json:"provider_id"`
	OffRampProvider string          `json:"off_ramp_provider"`
	Chain           CryptoChain     `json:"chain"`
	StableCoin      Currency        `json:"stablecoin"`
	Fee             Money           `json:"fee"`
	Rate            decimal.Decimal `json:"rate"`
	StableAmount    decimal.Decimal `json:"stable_amount"`
}

// ProviderOnRampPayload is the payload for IntentProviderOnRamp.
type ProviderOnRampPayload struct {
	TransferID   uuid.UUID        `json:"transfer_id"`
	TenantID     uuid.UUID        `json:"tenant_id"`
	ProviderID   string           `json:"provider_id"`
	Amount       decimal.Decimal  `json:"amount"`
	FromCurrency Currency         `json:"from_currency"`
	ToCurrency   Currency         `json:"to_currency"`
	Reference    string           `json:"reference"`
	Alternatives []OnRampFallback `json:"alternatives,omitempty"`
	// QuotedRate is the FX rate shown to the user at quote time. Providers use
	// this to enforce slippage limits at execution time.
	QuotedRate decimal.Decimal `json:"quoted_rate"`
}

// OffRampFallback is a fallback alternative for off-ramp provider execution.
// Only alternatives sharing the same chain+stablecoin qualify (since the on-ramp
// and blockchain send already completed for that chain).
type OffRampFallback struct {
	ProviderID string          `json:"provider_id"`
	Fee        Money           `json:"fee"`
	Rate       decimal.Decimal `json:"rate"`
}

// ProviderOffRampPayload is the payload for IntentProviderOffRamp.
type ProviderOffRampPayload struct {
	TransferID   uuid.UUID         `json:"transfer_id"`
	TenantID     uuid.UUID         `json:"tenant_id"`
	ProviderID   string            `json:"provider_id"`
	Amount       decimal.Decimal   `json:"amount"`
	FromCurrency Currency          `json:"from_currency"`
	ToCurrency   Currency          `json:"to_currency"`
	Recipient    Recipient         `json:"recipient"`
	Reference    string            `json:"reference"`
	Alternatives []OffRampFallback `json:"alternatives,omitempty"`
	// QuotedRate is the FX rate shown to the user at quote time. Providers use
	// this to enforce slippage limits at execution time.
	QuotedRate decimal.Decimal `json:"quoted_rate"`
	// SourceTxHash is the blockchain tx hash from the settlement send.
	// Carried through to the off-ramp provider for on-chain verification.
	SourceTxHash string `json:"source_tx_hash,omitempty"`
}

// LedgerPostPayload is the payload for IntentLedgerPost.
type LedgerPostPayload struct {
	TransferID     uuid.UUID         `json:"transfer_id"`
	TenantID       uuid.UUID         `json:"tenant_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	Description    string            `json:"description"`
	ReferenceType  string            `json:"reference_type"`
	Lines          []LedgerLineEntry `json:"lines"`
}

// LedgerLineEntry is a simplified posting line for serialization in outbox payloads.
type LedgerLineEntry struct {
	AccountCode string          `json:"account_code"`
	EntryType   string          `json:"entry_type"` // "DEBIT" or "CREDIT"
	Amount      decimal.Decimal `json:"amount"`
	Currency    string          `json:"currency"`
	Description string          `json:"description"`
}

// BlockchainSendPayload is the payload for IntentBlockchainSend.
type BlockchainSendPayload struct {
	TransferID uuid.UUID       `json:"transfer_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Chain      CryptoChain     `json:"chain"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Token      string          `json:"token"`
	Amount     decimal.Decimal `json:"amount"`
	Memo       string          `json:"memo"`
}

// WebhookDeliverPayload is the payload for IntentWebhookDeliver.
// For transfer webhooks, TransferID is set. For deposit webhooks, SessionID is set.
type WebhookDeliverPayload struct {
	TransferID uuid.UUID `json:"transfer_id,omitempty"`
	SessionID  uuid.UUID `json:"session_id,omitempty"`
	TenantID   uuid.UUID `json:"tenant_id"`
	EventType  string    `json:"event_type"`
	Data       []byte    `json:"data"` // JSON-encoded webhook body
}

// IntentEmailNotify instructs the email worker to send a notification email.
const IntentEmailNotify = "email.notify"

// EmailNotifyPayload is the payload for IntentEmailNotify.
type EmailNotifyPayload struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	SessionID  uuid.UUID `json:"session_id,omitempty"`
	TransferID uuid.UUID `json:"transfer_id,omitempty"`
	EventType  string    `json:"event_type"`
	Subject    string    `json:"subject"`
	Data       []byte    `json:"data"` // JSON-encoded template data
}

// Inbound provider webhook event types.
const (
	// EventProviderRawWebhook is published by the HTTP webhook receiver with the
	// raw provider payload. The InboundWebhookWorker stores it, then normalizes
	// using the provider's registered WebhookNormalizer before processing.
	EventProviderRawWebhook = "provider.inbound.raw"

	// EventProviderOnRampWebhook and EventProviderOffRampWebhook are the normalized
	// event types. Published by ProviderListeners (non-HTTP providers) that do their
	// own normalization, or used internally after Go-side normalization.
	EventProviderOnRampWebhook  = "provider.inbound.onramp.webhook"
	EventProviderOffRampWebhook = "provider.inbound.offramp.webhook"
)

// RawWebhookPayload is the un-normalized webhook payload forwarded by the HTTP
// receiver. Contains the provider slug (from the URL path) and the exact raw
// bytes of the POST body. Normalization happens in Go using the provider's
// registered WebhookNormalizer.
type RawWebhookPayload struct {
	ProviderSlug   string            `json:"provider_slug"`
	RawBody        []byte            `json:"raw_body"`          // exact bytes from provider HTTP POST (base64 in JSON)
	IdempotencyKey string            `json:"idempotency_key"`   // SHA-256 prefix of raw body for dedup
	HTTPHeaders    map[string]string `json:"http_headers,omitempty"`
	SourceIP       string            `json:"source_ip,omitempty"`
}

// ProviderWebhookPayload is the normalized payload from a provider webhook callback.
// Produced by calling WebhookNormalizer.NormalizeWebhook() on a RawWebhookPayload,
// or published directly by ProviderListeners for non-HTTP providers.
type ProviderWebhookPayload struct {
	TransferID  uuid.UUID `json:"transfer_id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	ProviderID  string    `json:"provider_id"`
	ProviderRef string    `json:"provider_ref"`
	Status      string    `json:"status"` // "completed", "failed"
	TxHash      string    `json:"tx_hash,omitempty"`
	Error       string    `json:"error,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	TxType      string    `json:"tx_type"` // "onramp", "offramp"
}

// WithCorrelationID returns a copy of the OutboxEntry with the CorrelationID set.
// This avoids changing the signatures of NewOutboxEvent / NewOutboxIntent which
// have 122 call sites across 11 files.
func (e OutboxEntry) WithCorrelationID(id uuid.UUID) OutboxEntry {
	e.CorrelationID = id
	return e
}

// IntentResult carries the outcome of a worker-executed intent back to the engine.
// Workers call Engine.Handle*Result methods with this after processing outbox intents.
type IntentResult struct {
	Success     bool              `json:"success"`
	ProviderRef string            `json:"provider_ref,omitempty"` // external reference from provider
	TxHash      string            `json:"tx_hash,omitempty"`      // blockchain transaction hash
	Error       string            `json:"error,omitempty"`        // error message on failure
	ErrorCode   string            `json:"error_code,omitempty"`   // machine-readable error code
	Metadata    map[string]string `json:"metadata,omitempty"`     // extra data from the worker
}
