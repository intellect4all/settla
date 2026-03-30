# Chapter 3.1: The Transactional Outbox Pattern

**Reading time:** ~25 minutes
**Prerequisites:** Module 1 (Domain Model), Module 2 (Data Layer basics)
**Code references:** `domain/outbox.go`, `core/engine.go`, `store/transferdb/outbox_store.go`

---

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain the dual-write problem and why it breaks financial systems.
2. Describe why distributed transactions (2PC) fail at scale.
3. Implement the transactional outbox pattern using a single database transaction.
4. Distinguish between outbox **intents** and **events** in Settla's domain model.
5. Trace a complete data flow from Engine through the outbox relay to NATS workers and back.

---

## 3.1.1 The Dual-Write Problem

Consider the simplest possible implementation of a settlement engine. When a
transfer is funded, you need to do two things:

1. Update the transfer status to `FUNDED` in the database.
2. Send a message to the treasury service to reserve funds.

Here is the naive approach:

```
func (e *Engine) FundTransfer(ctx context.Context, transferID uuid.UUID) error {
    // Step 1: Update database
    err := e.db.UpdateStatus(ctx, transferID, "FUNDED")
    if err != nil {
        return err
    }

    // Step 2: Send message to treasury
    err = e.messaging.Publish("treasury.reserve", payload)
    if err != nil {
        // DATABASE SAYS FUNDED, BUT TREASURY NEVER GOT THE MESSAGE
        // What do we do here? Roll back? The DB commit already happened.
        return err
    }

    return nil
}
```

This is the **dual-write problem**: you are writing to two different systems
(database and message broker) without a shared transaction boundary.

### The Timeline of Failure

Here is what happens when the message broker is down for 200 milliseconds:

```
Timeline (dual-write failure)
==============================

T=0ms    Engine receives FundTransfer request
         |
T=5ms    BEGIN transaction
         |  UPDATE transfers SET status = 'FUNDED' WHERE id = $1
         |  COMMIT
         |                                              <-- DB says FUNDED
T=10ms   Publish to NATS: "treasury.reserve"
         |
T=10ms   NATS connection refused (broker restart)      <-- Message lost
         |
T=10ms   Return error to caller
         |
         v
Result:  Transfer is FUNDED in the database
         Treasury has NO reservation
         System is INCONSISTENT
```

The failure window is small -- perhaps only a few milliseconds between the
commit and the publish. But at 580 TPS sustained (50M transactions/day), even
a 0.1% failure rate means **50,000 inconsistent transfers per day**.

> **Key Insight:** The dual-write problem is not about whether failures are
> likely. It is about whether failures are *possible*. In financial systems,
> any possible inconsistency will eventually happen, and when it does, money
> is at risk.

### Four Flavors of Dual-Write Failure

The problem manifests in four ways depending on which operation succeeds:

```
Scenario Matrix
================

DB Write    Message Publish    Result
--------    ---------------    ------
SUCCESS     SUCCESS            OK (happy path)
SUCCESS     FAILURE            DB inconsistent with downstream systems
FAILURE     SUCCESS            Message sent for non-existent state change
FAILURE     FAILURE            Both failed, safe to retry
```

Only two of four scenarios are safe. The dangerous one is row 2: the database
committed but the message was lost. You cannot roll back a committed
transaction, and you have no record of what message you needed to send.

---

## 3.1.2 Why Distributed Transactions Fail at Scale

The textbook solution is a distributed transaction using Two-Phase Commit (2PC):

```
2PC Protocol
=============

Phase 1 (Prepare):
  Coordinator --> Database:  "Can you commit?"
  Coordinator --> NATS:      "Can you commit?"
  Database    --> Coordinator: "Yes, prepared"
  NATS        --> Coordinator: "Yes, prepared"

Phase 2 (Commit):
  Coordinator --> Database:  "Commit!"
  Coordinator --> NATS:      "Commit!"
```

This works in theory. In practice, it fails for three reasons:

**1. Latency multiplication.** Every operation now requires two round-trips
instead of one. At 580 TPS, adding 10ms of coordination overhead means
5.8 seconds of additional latency per second of throughput.

**2. Availability coupling.** If *either* participant is down, the entire
transaction blocks. Your database uptime (99.99%) multiplied by your message
broker uptime (99.99%) gives you 99.98% -- which sounds fine until you realize
that is 105 minutes of downtime per year where *all* transfers stall.

**3. Lock holding.** During the prepare phase, both systems hold locks. The
database holds a row lock on the transfer; NATS holds a message slot. If the
coordinator crashes between Phase 1 and Phase 2, both systems are stuck
holding locks until a timeout expires. At 580 TPS, a 30-second timeout means
17,400 transfers are blocked.

> **Key Insight:** Distributed transactions trade availability for consistency.
> The transactional outbox trades latency for consistency -- you get both
> availability and consistency, but side effects are *eventually* executed
> rather than *immediately* executed.

---

## 3.1.3 The Outbox Pattern Explained

The solution is elegant: instead of writing to two systems, write to one
system (the database) twice -- once for the state change, and once for a
record of what messages need to be sent:

```
Transactional Outbox Pattern
==============================

                    Single Database Transaction
                    +--------------------------+
                    |                          |
  FundTransfer ---> | 1. UPDATE transfers      |
                    |    SET status = 'FUNDED'  |
                    |                          |
                    | 2. INSERT INTO outbox     |
                    |    (treasury.reserve,     |
                    |     {transfer_id, ...})   |
                    |                          |
                    +--------------------------+
                              |
                              | COMMIT (atomic)
                              |
                              v
                    Both rows committed or
                    neither row committed.
                    NEVER inconsistent.
```

A separate process -- the **outbox relay** -- polls the outbox table and
publishes entries to the message broker:

```
Outbox Relay (separate process)
================================

  Every 20ms:
    SELECT * FROM outbox
    WHERE published = false
    ORDER BY created_at
    LIMIT 500

    For each entry:
      Publish to NATS stream
      UPDATE outbox SET published = true
```

The relay is idempotent: if it crashes after publishing but before marking the
row as published, it will re-publish the message on restart. NATS JetStream
deduplication (2-minute window) ensures the message is delivered exactly once.

### Why This Works

The key insight is that the database transaction guarantees atomicity. Either
*both* the status update and the outbox entry are committed, or *neither* is.
There is no failure window between the two writes because they are the same
write.

```
Failure Analysis
=================

Scenario 1: DB commit succeeds
  - Transfer is FUNDED
  - Outbox entry exists
  - Relay will publish it (eventually)
  - System is CONSISTENT

Scenario 2: DB commit fails
  - Transfer is NOT FUNDED
  - Outbox entry does NOT exist
  - Nothing to publish
  - System is CONSISTENT

Scenario 3: Relay publishes, crashes before marking published
  - Transfer is FUNDED
  - Outbox entry exists, still marked unpublished
  - Relay re-publishes on restart
  - NATS deduplicates
  - System is CONSISTENT

There is no Scenario 4. The only two outcomes are "both" or "neither."
```

---

## 3.1.4 Settla's OutboxEntry: The Actual Code

Every outbox entry in Settla is represented by the `OutboxEntry` struct
defined in `domain/outbox.go`:

```go
// OutboxEntry represents a domain event or worker intent stored in the
// transactional outbox. The outbox pattern ensures that state changes and
// their corresponding events/intents are written atomically in the same
// database transaction -- eliminating dual-write bugs.
//
// There are two kinds of outbox entries:
//   - Events (IsIntent=false): notifications about something that happened.
//     Workers may subscribe to these but no action is required.
//   - Intents (IsIntent=true): instructions for a worker to execute a
//     side effect. A dedicated worker picks up each intent, executes it,
//     and publishes a result event.
type OutboxEntry struct {
    ID            uuid.UUID
    AggregateType string     // "transfer", "position"
    AggregateID   uuid.UUID
    TenantID      uuid.UUID
    CorrelationID uuid.UUID  // traces multi-step flows across partition boundaries
    EventType     string     // e.g., "transfer.created", "provider.onramp.execute"
    Payload       []byte     // JSON-encoded intent/event data
    IsIntent      bool       // true = worker should execute this; false = notification only
    Published     bool
    PublishedAt   *time.Time
    RetryCount    int
    MaxRetries    int
    CreatedAt     time.Time
}
```

Notice the field design decisions:

- **`AggregateType` + `AggregateID`**: Every outbox entry is anchored to a
  domain aggregate. For transfers, this is `"transfer"` + the transfer UUID.
  This lets you query "show me all outbox entries for transfer X" during
  debugging.

- **`TenantID`**: Every entry is tenant-scoped. This is critical for the NATS
  partitioning scheme (8 partitions by tenant hash).

- **`CorrelationID`**: Traces multi-step flows across partition boundaries.
  When a transfer spawns multiple intents across different NATS streams, the
  correlation ID links them for distributed tracing. Set via the fluent
  `WithCorrelationID()` method after construction.

- **`IsIntent`**: This boolean distinguishes between two fundamentally
  different kinds of entries (see Section 3.1.5).

- **`Payload`**: JSON-encoded, not protobuf. This keeps the outbox table
  human-readable during debugging and avoids a compile-time dependency on
  proto definitions in the domain layer.

- **`Published` + `PublishedAt`**: The relay marks entries after successful
  NATS publish. Unpublished entries are retried.

- **`RetryCount` + `MaxRetries`**: Dead-letter protection. After 5 failed
  publish attempts, the relay stops retrying and alerts.

### Constructor Functions

Settla provides two constructors that enforce the intent/event distinction:

```go
// NewOutboxEvent creates a notification outbox entry (not an intent).
// Returns an error if the eventType is not a known outbox event/intent constant.
func NewOutboxEvent(aggregateType string, aggregateID, tenantID uuid.UUID,
    eventType string, payload []byte) (OutboxEntry, error) {
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
// Returns an error if the eventType is not a known outbox event/intent constant.
func NewOutboxIntent(aggregateType string, aggregateID, tenantID uuid.UUID,
    eventType string, payload []byte) (OutboxEntry, error) {
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
```

Both constructors now validate the event type at creation time using
`ValidateEventType()`, which checks against a registry of all known outbox
constants. This catches typos and invalid intent types immediately rather
than at relay/worker processing time. For backward compatibility, convenience
wrappers `MustNewOutboxEvent` and `MustNewOutboxIntent` log and return a zero
entry on invalid types instead of panicking.

Note that both use `uuid.NewV7()` -- a time-ordered UUID. This means outbox
entries are naturally ordered by creation time even when sorted by ID, which
helps the relay process entries in FIFO order without an explicit `ORDER BY
created_at` index scan.

---

## 3.1.5 Intents vs. Events: The Critical Distinction

Settla's outbox carries two fundamentally different kinds of messages:

```
Intents vs. Events
====================

INTENT (IsIntent = true)                EVENT (IsIntent = false)
----------------------------------      ----------------------------------
"Please DO this thing"                  "This thing HAPPENED"
Worker MUST execute it                  Workers MAY subscribe to it
Exactly one consumer                    Zero or many consumers
Has a typed payload with parameters     Has a notification payload
Produces a result event                 No result expected
Example: treasury.reserve               Example: transfer.created
Example: provider.onramp.execute        Example: transfer.funded
Example: blockchain.send                Example: onramp.completed
```

### All Intent Types

These are the intent constants from `domain/outbox.go` and
`domain/deposit_outbox.go` / `domain/bank_deposit_outbox.go` that drive
the entire platform:

```go
// Transfer pipeline intents (domain/outbox.go)
const (
    IntentTreasuryReserve       = "treasury.reserve"
    IntentTreasuryRelease       = "treasury.release"
    IntentTreasuryConsume       = "treasury.consume"       // NEW: moves reserved to locked (transfer complete)
    IntentProviderOnRamp        = "provider.onramp.execute"
    IntentProviderOffRamp       = "provider.offramp.execute"
    IntentProviderReverseOnRamp = "provider.reverse_onramp"
    IntentLedgerPost            = "ledger.post"
    IntentLedgerReverse         = "ledger.reverse"
    IntentBlockchainSend        = "blockchain.send"
    IntentWebhookDeliver        = "webhook.deliver"
    IntentPositionCredit        = "position.credit"        // NEW: credit treasury from deposit/top-up
    IntentPositionDebit         = "position.debit"         // NEW: debit treasury for withdrawal
    IntentEmailNotify           = "email.notify"           // NEW: send transactional email
)

// Crypto deposit intents (domain/deposit_outbox.go)
const (
    IntentMonitorAddress = "deposit.monitor.address"       // NEW: start chain monitoring
    IntentCreditDeposit  = "deposit.credit"                // NEW: credit tenant from crypto deposit
    IntentSettleDeposit  = "deposit.settle"                // NEW: convert crypto to fiat
)

// Bank deposit intents (domain/bank_deposit_outbox.go)
const (
    IntentBankDepositCredit     = "bank_deposit.credit"    // NEW: credit from bank deposit
    IntentBankDepositSettle     = "bank_deposit.settle"    // NEW: convert fiat to stablecoin
    IntentBankDepositRefund     = "bank_deposit.refund"    // NEW: refund bank payment
    IntentRecycleVirtualAccount = "bank_deposit.recycle_account" // NEW: recycle virtual account
)
```

Each intent has a dedicated worker that consumes it from a specific NATS
stream. There are now 12 streams (11 domain + DLQ) and 11 workers:

```
Intent Routing
===============

Intent Type                  NATS Stream                Worker
--------------------------   -----------------------    -------------------------
treasury.reserve             SETTLA_TREASURY            TreasuryWorker
treasury.release             SETTLA_TREASURY            TreasuryWorker
treasury.consume             SETTLA_TREASURY            TreasuryWorker
position.credit              SETTLA_TREASURY            TreasuryWorker
position.debit               SETTLA_TREASURY            TreasuryWorker
provider.onramp.execute      SETTLA_PROVIDERS           ProviderWorker
provider.offramp.execute     SETTLA_PROVIDERS           ProviderWorker
provider.reverse_onramp      SETTLA_PROVIDERS           ProviderWorker
ledger.post                  SETTLA_LEDGER              LedgerWorker
ledger.reverse               SETTLA_LEDGER              LedgerWorker
blockchain.send              SETTLA_BLOCKCHAIN          BlockchainWorker
webhook.deliver              SETTLA_WEBHOOKS            WebhookWorker
email.notify                 SETTLA_EMAILS              EmailWorker
deposit.monitor.address      SETTLA_CRYPTO_DEPOSITS     DepositWorker
deposit.credit               SETTLA_CRYPTO_DEPOSITS     DepositWorker
deposit.settle               SETTLA_CRYPTO_DEPOSITS     DepositWorker
bank_deposit.credit          SETTLA_BANK_DEPOSITS       BankDepositWorker
bank_deposit.settle          SETTLA_BANK_DEPOSITS       BankDepositWorker
bank_deposit.refund          SETTLA_BANK_DEPOSITS       BankDepositWorker
bank_deposit.recycle_account SETTLA_BANK_DEPOSITS       BankDepositWorker
(provider inbound webhooks)  SETTLA_PROVIDER_WEBHOOKS   InboundWebhookWorker
(position audit events)      SETTLA_POSITION_EVENTS     PositionEventWriter
(dead letters)               SETTLA_DLQ                 DLQMonitor
```

### All Event Types (Result Events)

After a worker executes an intent, it publishes a result event:

```go
// Transfer result events
const (
    EventTreasuryReserved      = "treasury.reserved"
    EventTreasuryReleased      = "treasury.released"
    EventTreasuryFailed        = "treasury.failed"
    EventTreasuryConsumed      = "treasury.consumed"       // NEW
    EventProviderOnRampDone    = "provider.onramp.completed"
    EventProviderOnRampFailed  = "provider.onramp.failed"
    EventProviderOffRampDone   = "provider.offramp.completed"
    EventProviderOffRampFailed = "provider.offramp.failed"
    EventLedgerPosted          = "ledger.posted"
    EventLedgerReversed        = "ledger.reversed"
    EventBlockchainConfirmed   = "blockchain.confirmed"
    EventBlockchainFailed      = "blockchain.failed"
    EventPositionCredited      = "position.credited"       // NEW
    EventPositionDebited       = "position.debited"        // NEW
)

// Inbound provider webhook events
const (
    EventProviderRawWebhook     = "provider.inbound.raw"
    EventProviderOnRampWebhook  = "provider.inbound.onramp.webhook"
    EventProviderOffRampWebhook = "provider.inbound.offramp.webhook"
)
```

### Lifecycle Events

In addition to intent results, the engine writes lifecycle events at each
state transition. These are pure notifications -- no worker action required:

```go
// Transfer lifecycle events
const (
    EventTransferCreated     = "transfer.created"
    EventTransferFunded      = "transfer.funded"
    EventOnRampInitiated     = "onramp.initiated"
    EventOnRampCompleted     = "onramp.completed"
    EventSettlementStarted   = "settlement.started"
    EventSettlementCompleted = "settlement.completed"
    EventOffRampInitiated    = "offramp.initiated"
    EventOffRampCompleted    = "offramp.completed"
    EventTransferCompleted   = "transfer.completed"
    EventTransferFailed      = "transfer.failed"
    EventRefundInitiated     = "refund.initiated"
    EventRefundCompleted     = "refund.completed"
    EventPositionUpdated     = "position.updated"
    EventLiquidityAlert      = "liquidity.alert"
)

// Deposit session lifecycle events
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

// Bank deposit session lifecycle events
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
```

All event types are validated at construction time by `ValidateEventType()`,
which checks against the `knownEventTypes` map in `domain/outbox.go`. This
map is the single source of truth for all valid outbox constants across
transfers, deposits, bank deposits, and provider webhooks.

---

## 3.1.6 Intent Payload Types

Every intent carries a strongly-typed JSON payload that gives the worker
everything it needs to execute the side effect. The worker never needs to
query the database for additional information.

### TreasuryReservePayload

```go
type TreasuryReservePayload struct {
    TransferID uuid.UUID       `json:"transfer_id"`
    TenantID   uuid.UUID       `json:"tenant_id"`
    Currency   Currency        `json:"currency"`
    Amount     decimal.Decimal `json:"amount"`
    Location   string          `json:"location"`
}
```

### TreasuryReleasePayload

```go
type TreasuryReleasePayload struct {
    TransferID uuid.UUID       `json:"transfer_id"`
    TenantID   uuid.UUID       `json:"tenant_id"`
    Currency   Currency        `json:"currency"`
    Amount     decimal.Decimal `json:"amount"`
    Location   string          `json:"location"`
    Reason     string          `json:"reason"`
}
```

The `Reason` field is critical: the same transfer may trigger multiple
releases for different reasons (e.g., `"onramp_failure"`,
`"settlement_failure"`, `"transfer_complete"`). The treasury worker uses
`transfer_id + reason` as an idempotency key to prevent double-release.

### ProviderOnRampPayload

```go
type ProviderOnRampPayload struct {
    TransferID   uuid.UUID        `json:"transfer_id"`
    TenantID     uuid.UUID        `json:"tenant_id"`
    ProviderID   string           `json:"provider_id"`
    Amount       decimal.Decimal  `json:"amount"`
    FromCurrency Currency         `json:"from_currency"`
    ToCurrency   Currency         `json:"to_currency"`
    Reference    string           `json:"reference"`
    Alternatives []OnRampFallback `json:"alternatives,omitempty"`
    QuotedRate   decimal.Decimal  `json:"quoted_rate"`
}
```

Note `Alternatives` -- the engine pre-computes fallback providers from the
quote's alternative routes. If the primary on-ramp provider fails, the
ProviderWorker can try alternatives without calling back to the engine.

### LedgerPostPayload

```go
type LedgerPostPayload struct {
    TransferID     uuid.UUID         `json:"transfer_id"`
    TenantID       uuid.UUID         `json:"tenant_id"`
    IdempotencyKey string            `json:"idempotency_key"`
    Description    string            `json:"description"`
    ReferenceType  string            `json:"reference_type"`
    Lines          []LedgerLineEntry `json:"lines"`
}

type LedgerLineEntry struct {
    AccountCode string          `json:"account_code"`
    EntryType   string          `json:"entry_type"` // "DEBIT" or "CREDIT"
    Amount      decimal.Decimal `json:"amount"`
    Currency    string          `json:"currency"`
    Description string          `json:"description"`
}
```

### BlockchainSendPayload

```go
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
```

### WebhookDeliverPayload

```go
type WebhookDeliverPayload struct {
    TransferID uuid.UUID `json:"transfer_id,omitempty"`
    SessionID  uuid.UUID `json:"session_id,omitempty"`
    TenantID   uuid.UUID `json:"tenant_id"`
    EventType  string    `json:"event_type"`
    Data       []byte    `json:"data"` // JSON-encoded webhook body
}
```

Note the `SessionID` field -- webhook delivery is no longer limited to
transfers. Deposit sessions and bank deposit sessions can also trigger tenant
webhooks.

### TreasuryConsumePayload (NEW)

```go
type TreasuryConsumePayload struct {
    TransferID uuid.UUID       `json:"transfer_id"`
    TenantID   uuid.UUID       `json:"tenant_id"`
    Currency   Currency        `json:"currency"`
    Amount     decimal.Decimal `json:"amount"`
    Location   string          `json:"location"`
}
```

Emitted when a transfer completes. Unlike `TreasuryRelease` (which returns
reserved funds back to the available pool), `TreasuryConsume` marks the
reserved funds as consumed because money physically left the tenant's
position.

### PositionCreditPayload (NEW)

```go
type PositionCreditPayload struct {
    TenantID  uuid.UUID       `json:"tenant_id"`
    Currency  Currency        `json:"currency"`
    Amount    decimal.Decimal `json:"amount"`
    Location  string          `json:"location"`
    Reference uuid.UUID       `json:"reference"` // source entity ID
    RefType   string          `json:"ref_type"`  // "deposit_session", "bank_deposit",
                                                  // "position_transaction", "transfer",
                                                  // "compensation"
}
```

Used for deposit credits, manual top-ups, stablecoin compensation, and
internal rebalancing (destination side). The `RefType` field enables
audit tracing back to the originating domain entity.

### PositionDebitPayload (NEW)

```go
type PositionDebitPayload struct {
    TenantID    uuid.UUID       `json:"tenant_id"`
    Currency    Currency        `json:"currency"`
    Amount      decimal.Decimal `json:"amount"`
    Location    string          `json:"location"`
    Reference   uuid.UUID       `json:"reference"`
    RefType     string          `json:"ref_type"`
    Destination string          `json:"destination,omitempty"` // bank account ref or crypto address
}
```

Used for manual withdrawals and internal rebalancing (source side).

### EmailNotifyPayload (NEW)

```go
type EmailNotifyPayload struct {
    TenantID   uuid.UUID `json:"tenant_id"`
    SessionID  uuid.UUID `json:"session_id,omitempty"`
    TransferID uuid.UUID `json:"transfer_id,omitempty"`
    EventType  string    `json:"event_type"`
    Subject    string    `json:"subject"`
    Data       []byte    `json:"data"` // JSON-encoded template data
}
```

### IntentResult (Worker Callback)

Workers report outcomes back to the engine via `IntentResult`:

```go
type IntentResult struct {
    Success     bool              `json:"success"`
    ProviderRef string            `json:"provider_ref,omitempty"`
    TxHash      string            `json:"tx_hash,omitempty"`
    Error       string            `json:"error,omitempty"`
    ErrorCode   string            `json:"error_code,omitempty"`
    Metadata    map[string]string `json:"metadata,omitempty"`
}
```

---

## 3.1.7 Complete Data Flow: Engine to Worker and Back

Here is the complete flow for a single state transition, using `FundTransfer`
as the example:

```
Complete Outbox Data Flow
===========================

 1. API Gateway receives POST /v1/transfers/:id/fund
    |
    v
 2. Engine.FundTransfer(ctx, tenantID, transferID)
    |
    |  a. Load transfer, verify status == CREATED
    |  b. Build TreasuryReservePayload (JSON)
    |  c. Create outbox entries:
    |     - NewOutboxIntent("transfer", id, tenant, "treasury.reserve", payload)
    |     - NewOutboxEvent("transfer", id, tenant, "transfer.funded", payload)
    |
    v
 3. TransferStore.TransitionWithOutbox(ctx, transferID, FUNDED, version, entries)
    |
    |  Single database transaction:
    |  +----------------------------------------------------------+
    |  | BEGIN (REPEATABLE READ)                                   |
    |  |                                                          |
    |  | UPDATE transfers SET status = 'FUNDED',                  |
    |  |   version = version + 1                                  |
    |  |   WHERE id = $1 AND version = $2   -- optimistic lock    |
    |  |                                                          |
    |  | INSERT INTO outbox_entries (id, aggregate_type, ...)     |
    |  |   VALUES ($1, 'transfer', ..., 'treasury.reserve', ...)  |
    |  | INSERT INTO outbox_entries (id, aggregate_type, ...)     |
    |  |   VALUES ($2, 'transfer', ..., 'transfer.funded', ...)   |
    |  |                                                          |
    |  | COMMIT                                                   |
    |  +----------------------------------------------------------+
    |
    v
 4. Outbox Relay (polls every 20ms, batch of 500)
    |
    |  SELECT * FROM outbox_entries
    |  WHERE published = false
    |  ORDER BY created_at LIMIT 500
    |
    |  For the treasury.reserve entry:
    |    Publish to NATS stream SETTLA_TREASURY
    |    subject: settla.treasury.reserve
    |
    |  UPDATE outbox_entries SET published = true WHERE id = $1
    |
    v
 5. NATS JetStream (SETTLA_TREASURY stream)
    |
    |  WorkQueue retention, 7-day max age, 5-minute dedup window
    |
    v
 6. TreasuryWorker picks up the message
    |
    |  a. Deserialize TreasuryReservePayload from message body
    |  b. CHECK-BEFORE-CALL: Has this reservation already been made?
    |     - Check provider_transactions table for existing "treasury_reserve"
    |       entry with this transfer_id
    |  c. If not already executed: call Treasury.Reserve(currency, amount, location)
    |  d. Record execution in provider_transactions for idempotency
    |  e. ACK the NATS message
    |
    v
 7. TreasuryWorker calls back to Engine
    |
    |  Engine.InitiateOnRamp(ctx, tenantID, transferID)
    |    -- OR on failure --
    |  Engine.FailTransfer(ctx, tenantID, transferID, reason, code)
    |
    |  This triggers another TransitionWithOutbox with NEW outbox entries,
    |  and the cycle repeats for the next step in the pipeline.
    |
    v
 8. Loop continues until terminal state (COMPLETED or FAILED)
```

### Why 20ms Polling?

The relay polls every 20ms in batches of 500. At 580 TPS, that is roughly
11.6 entries per poll cycle -- well within the batch size. The 20ms interval
adds at most 20ms of latency to side-effect execution, which is acceptable
for a system where the side effects themselves (provider API calls, blockchain
confirmations) take hundreds of milliseconds to seconds.

### Why REPEATABLE READ?

The `TransitionWithOutbox` method uses REPEATABLE READ isolation:

```go
func beginRepeatableRead(ctx context.Context, pool TxBeginner) (pgx.Tx, error) {
    if p, ok := pool.(TxBeginnerWithOptions); ok {
        return p.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
    }
    return pool.Begin(ctx)
}
```

This prevents phantom reads during the concurrent `UPDATE` + `INSERT`
pattern. Without REPEATABLE READ, two concurrent transitions for the same
transfer could both read the same version, both pass the optimistic lock
check, and both commit -- resulting in a corrupted state. REPEATABLE READ
ensures the second transaction sees the first transaction's UPDATE and fails
the version check.

---

## 3.1.8 Outbox Table Growth and Partition Management

At 50M transactions/day, each producing 2-4 outbox entries, the outbox table
grows by 100-200M rows per day. Without management, queries degrade within
weeks.

Settla uses monthly partitions managed by `core/maintenance.PartitionManager`:

```
Partition Strategy
===================

outbox_entries (parent)
  |
  +-- outbox_entries_2026_01  (January)
  +-- outbox_entries_2026_02  (February)
  +-- outbox_entries_2026_03  (March)     <-- current month
  +-- outbox_entries_2026_04  (April)     <-- pre-created
  +-- ...
  +-- outbox_entries_2026_08  (August)    <-- 6 months ahead

Old partitions are dropped with DROP TABLE (instant), never DELETE (slow).
```

---

## Common Mistakes

1. **Writing side effects directly from the engine.** Never call a provider,
   ledger, or treasury API from the engine. Every side effect must go through
   the outbox. The engine's only I/O is the database.

2. **Forgetting the optimistic lock version.** `TransitionWithOutbox` requires
   `expectedVersion`. If you pass the wrong version, the transition silently
   fails (returns `ErrOptimisticLock`). Always use `transfer.Version` from the
   most recent load.

3. **Using READ COMMITTED for outbox transactions.** The default PostgreSQL
   isolation level allows phantom reads that can break the UPDATE + INSERT
   pattern under concurrency. Always use REPEATABLE READ.

4. **Assuming message order.** NATS JetStream provides at-least-once delivery,
   not exactly-once. Workers must be idempotent. The CHECK-BEFORE-CALL pattern
   handles this.

5. **Publishing before committing.** Never publish a message and then commit
   the database transaction. If the commit fails, you have published a message
   for a state change that did not happen. The outbox inverts this: commit
   first, publish later via the relay.

---

## Exercises

1. **Trace a failure:** Draw a timeline showing what happens when the database
   goes down during `TransitionWithOutbox`. What state is the transfer in?
   What outbox entries exist? What does the relay do?

2. **Design a new intent:** Suppose you need to add an AML (Anti-Money
   Laundering) screening step before on-ramp. Design the `IntentAMLScreen`
   constant, its payload type, and where in the engine it would be created.

3. **Calculate outbox growth:** At 580 TPS with an average of 3 outbox entries
   per transfer, how many outbox rows are created per day? Per month? How
   large is each partition if the average row is 500 bytes?

4. **Implement dead-letter handling:** The current relay retries up to
   `MaxRetries` (5) times. Design a dead-letter mechanism that alerts the ops
   team when an entry exceeds max retries. What information should the alert
   contain?

---

## What's Next

In Chapter 3.2, we will build the settlement engine itself -- examining the
`Engine` struct, its zero-side-effect design, and walking through
`CreateTransfer` and `FundTransfer` line by line to see how they produce the
outbox entries we studied in this chapter.
