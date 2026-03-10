package domain

import (
	"fmt"
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
	EventType     string // e.g., "transfer.created", "provider.onramp.execute"
	Payload       []byte // JSON-encoded intent/event data
	IsIntent      bool   // true = worker should execute this; false = notification only
	Published     bool
	PublishedAt   *time.Time
	RetryCount    int
	MaxRetries    int
	CreatedAt     time.Time
}

// NewOutboxEvent creates a notification outbox entry (not an intent).
// The relay publishes these to NATS for subscribers, but no worker action is required.
func NewOutboxEvent(aggregateType string, aggregateID, tenantID uuid.UUID, eventType string, payload []byte) OutboxEntry {
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
	}
}

// NewOutboxIntent creates an intent outbox entry that a worker should execute.
// The relay publishes these to NATS, where a dedicated worker picks them up,
// executes the side effect, and publishes a result event.
func NewOutboxIntent(aggregateType string, aggregateID, tenantID uuid.UUID, eventType string, payload []byte) OutboxEntry {
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
	}
}

// Intent type constants — workers consume these from NATS and execute the side effect.
const (
	IntentTreasuryReserve  = "treasury.reserve"
	IntentTreasuryRelease  = "treasury.release"
	IntentProviderOnRamp   = "provider.onramp.execute"
	IntentProviderOffRamp  = "provider.offramp.execute"
	IntentLedgerPost       = "ledger.post"
	IntentLedgerReverse    = "ledger.reverse"
	IntentBlockchainSend   = "blockchain.send"
	IntentWebhookDeliver   = "webhook.deliver"
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
)

// knownEventTypes is the set of all valid outbox event/intent types.
// Used by ValidateEventType for runtime validation.
var knownEventTypes = map[string]struct{}{
	// Intents
	IntentTreasuryReserve: {}, IntentTreasuryRelease: {},
	IntentProviderOnRamp: {}, IntentProviderOffRamp: {},
	IntentLedgerPost: {}, IntentLedgerReverse: {},
	IntentBlockchainSend: {}, IntentWebhookDeliver: {},
	// Result events
	EventTreasuryReserved: {}, EventTreasuryReleased: {}, EventTreasuryFailed: {},
	EventProviderOnRampDone: {}, EventProviderOnRampFailed: {},
	EventProviderOffRampDone: {}, EventProviderOffRampFailed: {},
	EventLedgerPosted: {}, EventLedgerReversed: {},
	EventBlockchainConfirmed: {}, EventBlockchainFailed: {},
	// Inbound provider webhooks
	EventProviderOnRampWebhook: {}, EventProviderOffRampWebhook: {},
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

// OnRampFallback is a fallback alternative for on-ramp provider execution.
// It carries enough information for the worker to switch providers without
// consulting the engine.
type OnRampFallback struct {
	ProviderID      string          `json:"provider_id"`
	OffRampProvider string          `json:"off_ramp_provider"`
	Chain           string          `json:"chain"`
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
	Chain      string          `json:"chain"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Token      string          `json:"token"`
	Amount     decimal.Decimal `json:"amount"`
	Memo       string          `json:"memo"`
}

// WebhookDeliverPayload is the payload for IntentWebhookDeliver.
type WebhookDeliverPayload struct {
	TransferID uuid.UUID `json:"transfer_id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	EventType  string    `json:"event_type"`
	Data       []byte    `json:"data"` // JSON-encoded webhook body
}

// Inbound provider webhook event types — the webhook HTTP handler normalizes
// raw provider callbacks into these events and publishes them to NATS.
const (
	EventProviderOnRampWebhook  = "provider.inbound.onramp.webhook"
	EventProviderOffRampWebhook = "provider.inbound.offramp.webhook"
)

// ProviderWebhookPayload is the normalized payload from a provider webhook callback.
// The inbound webhook HTTP handler converts provider-specific formats into this
// canonical structure before publishing to NATS for processing by InboundWebhookWorker.
type ProviderWebhookPayload struct {
	TransferID  uuid.UUID `json:"transfer_id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	ProviderID  string    `json:"provider_id"`
	ProviderRef string    `json:"provider_ref"`
	Status      string    `json:"status"`     // "completed", "failed"
	TxHash      string    `json:"tx_hash,omitempty"`
	Error       string    `json:"error,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	TxType      string    `json:"tx_type"`    // "onramp", "offramp"
}

// IntentResult carries the outcome of a worker-executed intent back to the engine.
// Workers call Engine.Handle*Result methods with this after processing outbox intents.
type IntentResult struct {
	Success     bool              `json:"success"`
	ProviderRef string            `json:"provider_ref,omitempty"` // external reference from provider
	TxHash      string            `json:"tx_hash,omitempty"`     // blockchain transaction hash
	Error       string            `json:"error,omitempty"`       // error message on failure
	ErrorCode   string            `json:"error_code,omitempty"`  // machine-readable error code
	Metadata    map[string]string `json:"metadata,omitempty"`    // extra data from the worker
}
