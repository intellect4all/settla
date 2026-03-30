# Chapter 3.3: The Full Transfer Lifecycle

**Reading time:** ~35 minutes
**Prerequisites:** Chapters 3.1 (Transactional Outbox), 3.2 (Building the Engine)
**Code references:** `core/engine.go`, `domain/transfer.go`, `domain/outbox.go`

---

## Learning Objectives

By the end of this chapter you will be able to:

1. Name every state in the transfer state machine and the valid transitions
   between them.
2. Walk through the happy-path lifecycle from CREATED to COMPLETED, identifying
   every engine method, outbox entry, and worker callback.
3. Explain how `HandleOnRampResult`, `HandleSettlementResult`, and
   `HandleOffRampResult` branch on success vs. failure.
4. Describe the worker callback pattern using `IntentResult`.
5. Draw a complete sequence diagram of a successful GBP-to-NGN transfer.

---

## 3.3.1 The State Machine

Every transfer in Settla follows a strict state machine defined in
`domain/transfer.go`:

```go
var ValidTransitions = map[TransferStatus][]TransferStatus{
    TransferStatusCreated:    {TransferStatusFunded, TransferStatusFailed},
    TransferStatusFunded:     {TransferStatusOnRamping, TransferStatusRefunding},
    TransferStatusOnRamping:  {TransferStatusSettling, TransferStatusRefunding, TransferStatusFailed},
    TransferStatusSettling:   {TransferStatusOffRamping, TransferStatusFailed},
    TransferStatusOffRamping: {TransferStatusCompleted, TransferStatusFailed},
    TransferStatusFailed:     {TransferStatusRefunding},
    TransferStatusRefunding:  {TransferStatusRefunded},
}
```

Visualized as a diagram:

```
Transfer State Machine
=======================

  CREATED -----> FUNDED -----> ON_RAMPING -----> SETTLING -----> OFF_RAMPING -----> COMPLETED
     |              |              |                 |                |
     |              |              |                 |                |
     v              v              v                 v                v
   FAILED       REFUNDING      REFUNDING          FAILED           FAILED
                   |            FAILED                               |
                   v                                                 |
                REFUNDED                                             |
                                                                     v
                                                                  REFUNDING
                                                                     |
                                                                     v
                                                                  REFUNDED
```

### The Status Constants

```go
const (
    TransferStatusCreated    TransferStatus = "CREATED"
    TransferStatusFunded     TransferStatus = "FUNDED"
    TransferStatusOnRamping  TransferStatus = "ON_RAMPING"
    TransferStatusSettling   TransferStatus = "SETTLING"
    TransferStatusOffRamping TransferStatus = "OFF_RAMPING"
    TransferStatusCompleted  TransferStatus = "COMPLETED"
    TransferStatusFailed     TransferStatus = "FAILED"
    TransferStatusRefunding  TransferStatus = "REFUNDING"
    TransferStatusRefunded   TransferStatus = "REFUNDED"
)
```

### The CanTransitionTo Guard

```go
func (t *Transfer) CanTransitionTo(target TransferStatus) bool {
    allowed, ok := ValidTransitions[t.Status]
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
```

This is a data-driven guard -- the valid transitions are defined as data
(the map), not as code (if/else chains). Adding a new state requires only
updating the map, not modifying conditional logic scattered across methods.

---

## 3.3.2 Complete Happy-Path Sequence

Here is the full journey of a GBP-to-NGN transfer through Settla. Each step
shows the engine method, the outbox entries produced, and the worker that
executes them.

```
Happy Path: GBP -> USDT (on-chain) -> NGN
============================================

Step  Engine Method          From -> To       Outbox Entries
----  ---------------------  ---------------  ---------------------------------
 1    CreateTransfer         (new) -> CREATED  EVENT: transfer.created
 2    FundTransfer           CREATED -> FUNDED INTENT: treasury.reserve
                                               EVENT: transfer.funded
 3    InitiateOnRamp         FUNDED -> ON_RAMPING
                                               INTENT: provider.onramp.execute
 4    HandleOnRampResult     ON_RAMPING -> SETTLING
      (success)                                INTENT: ledger.post
                                               INTENT: blockchain.send
                                               EVENT: onramp.completed
 5    HandleSettlementResult SETTLING -> OFF_RAMPING
      (success)                                INTENT: provider.offramp.execute
                                               EVENT: settlement.completed
 6    HandleOffRampResult    OFF_RAMPING -> COMPLETED
      (success) -> CompleteTransfer            INTENT: treasury.release
                                               INTENT: ledger.post
                                               INTENT: webhook.deliver
                                               EVENT: transfer.completed
```

Total: 6 engine calls, 7 intents, 4 events = 11 outbox entries per transfer.

### Full Sequence Diagram

```
API         Engine       TransferDB     OutboxRelay    NATS       Workers
 |            |              |              |            |            |
 |--create--->|              |              |            |            |
 |            |--CreateTransferWithOutbox-->|             |            |
 |            |   (INSERT transfer +       |             |            |
 |            |    INSERT outbox event)    |             |            |
 |<--transfer-|              |              |            |            |
 |            |              |              |            |            |
 |---fund---->|              |              |            |            |
 |            |--TransitionWithOutbox----->|             |            |
 |            |   (UPDATE status=FUNDED + |             |            |
 |            |    INSERT treasury.reserve|             |            |
 |            |    INSERT transfer.funded)|             |            |
 |<---ok------|              |              |            |            |
 |            |              |              |            |            |
 |            |              |    poll 20ms |            |            |
 |            |              |<-------------|            |            |
 |            |              |--entries---->|            |            |
 |            |              |              |--publish-->|            |
 |            |              |              |  treasury  |            |
 |            |              |              |  .reserve  |            |
 |            |              |              |            |--deliver-->|
 |            |              |              |            | Treasury   |
 |            |              |              |            | Worker     |
 |            |              |              |            |            |
 |            |              |              |            |  Reserve   |
 |            |              |              |            |  funds     |
 |            |              |              |            |  in-memory |
 |            |              |              |            |            |
 |            |<-----------InitiateOnRamp-----------------|----------|
 |            |--TransitionWithOutbox----->|             |            |
 |            |  (UPDATE status=ON_RAMPING |             |            |
 |            |   INSERT provider.onramp)  |             |            |
 |            |              |              |            |            |
 |            |              |    poll 20ms |            |            |
 |            |              |<-------------|            |            |
 |            |              |--entries---->|            |            |
 |            |              |              |--publish-->|            |
 |            |              |              |  provider  |            |
 |            |              |              |  .onramp   |            |
 |            |              |              |            |--deliver-->|
 |            |              |              |            | Provider   |
 |            |              |              |            | Worker     |
 |            |              |              |            |            |
 |            |              |              |            | Call       |
 |            |              |              |            | on-ramp    |
 |            |              |              |            | provider   |
 |            |              |              |            | API        |
 |            |              |              |            |            |
 |            |<------HandleOnRampResult(success)--------|-----------|
 |            |--TransitionWithOutbox----->|             |            |
 |            |  (UPDATE status=SETTLING   |             |            |
 |            |   INSERT ledger.post       |             |            |
 |            |   INSERT blockchain.send)  |             |            |
 |            |              |              |            |            |
 |            |   ... (relay publishes, workers execute) |            |
 |            |              |              |            |            |
 |            |<------HandleSettlementResult(success)----|-----------|
 |            |--TransitionWithOutbox----->|             |            |
 |            |  (UPDATE status=OFF_RAMPING|             |            |
 |            |   INSERT provider.offramp) |             |            |
 |            |              |              |            |            |
 |            |   ... (relay publishes, provider worker executes)    |
 |            |              |              |            |            |
 |            |<------HandleOffRampResult(success)-------|-----------|
 |            |--CompleteTransfer          |             |            |
 |            |--TransitionWithOutbox----->|             |            |
 |            |  (UPDATE status=COMPLETED  |             |            |
 |            |   INSERT treasury.release  |             |            |
 |            |   INSERT ledger.post       |             |            |
 |            |   INSERT webhook.deliver)  |             |            |
 |            |              |              |            |            |
 |            |              |   relay publishes all 3 intents       |
 |            |              |              |            |            |
 |            |              |              |            | Treasury   |
 |            |              |              |            | releases   |
 |            |              |              |            | reservation|
 |            |              |              |            |            |
 |            |              |              |            | Ledger     |
 |            |              |              |            | posts      |
 |            |              |              |            | final      |
 |            |              |              |            | entries    |
 |            |              |              |            |            |
 |            |              |              |            | Webhook    |
 |            |              |              |            | notifies   |
 |            |              |              |            | tenant     |
```

---

## 3.3.3 Step-by-Step: InitiateOnRamp

After treasury reservation succeeds, the worker calls `InitiateOnRamp`:

```go
func (e *Engine) InitiateOnRamp(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID) error {

    transfer, err := e.loadTransferForStep(ctx, tenantID, transferID,
        domain.TransferStatusFunded)
    if err != nil {
        return fmt.Errorf("settla-core: on-ramp transfer %s: %w", transferID, err)
    }
```

Guard: transfer must be FUNDED. If the treasury worker calls this but the
transfer has already moved past FUNDED (e.g., due to a concurrent retry),
the guard rejects it.

```go
    // Load quote to get fallback alternatives
    var alternatives []domain.OnRampFallback
    if transfer.QuoteID != nil {
        quote, qErr := e.transferStore.GetQuote(ctx, transfer.TenantID, *transfer.QuoteID)
        if qErr == nil && quote != nil {
            for _, alt := range quote.Route.AlternativeRoutes {
                alternatives = append(alternatives, domain.OnRampFallback{
                    ProviderID:      alt.OnRampProvider,
                    OffRampProvider: alt.OffRampProvider,
                    Chain:           alt.Chain,
                    StableCoin:      alt.StableCoin,
                    Fee:             alt.Fee,
                    Rate:            alt.Rate,
                    StableAmount:    alt.StableAmount,
                })
            }
        }
    }
```

The engine pre-computes fallback alternatives from the quote and includes
them in the intent payload. This means the ProviderWorker can try alternative
on-ramp providers without calling back to the engine -- reducing latency
on failure paths.

```go
    onRampPayload, err := json.Marshal(domain.ProviderOnRampPayload{
        TransferID:   transfer.ID,
        TenantID:     transfer.TenantID,
        ProviderID:   transfer.OnRampProviderID,
        Amount:       transfer.SourceAmount,
        FromCurrency: transfer.SourceCurrency,
        ToCurrency:   transfer.StableCoin,
        Reference:    transfer.ID.String(),
        Alternatives: alternatives,
        QuotedRate:   transfer.FXRate,
    })
```

The `QuotedRate` field lets the provider enforce slippage limits. If the live
rate deviates more than the configured tolerance from the quoted rate, the
provider can reject the on-ramp rather than executing at a bad rate.

```go
    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentProviderOnRamp, onRampPayload),
    }

    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusOnRamping, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "on-ramp transfer", transferID)
    }
```

Note: only one outbox entry here (an intent, no event). Not every transition
produces lifecycle events -- `InitiateOnRamp` does not because the
`HandleOnRampResult` method emits the relevant event (`onramp.completed` or
`provider.onramp.failed`).

---

## 3.3.4 Step-by-Step: HandleOnRampResult

This is the first method that branches on success vs. failure:

```go
func (e *Engine) HandleOnRampResult(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID, result domain.IntentResult) error {

    transfer, err := e.loadTransferForStep(ctx, tenantID, transferID,
        domain.TransferStatusOnRamping)
    if err != nil {
        return fmt.Errorf("settla-core: handle on-ramp result %s: %w", transferID, err)
    }
```

Guard: must be ON_RAMPING. The `IntentResult` carries the outcome:

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

### Success Path: ON_RAMPING -> SETTLING

On success, the engine does three things atomically:

1. **Builds ledger posting entries** -- debits the crypto asset account and
   on-ramp fee expense, credits the tenant's clearing account:

```go
    onRampLines := []domain.LedgerLineEntry{
        {
            AccountCode: fmt.Sprintf("assets:crypto:%s:%s",
                strings.ToLower(string(transfer.StableCoin)),
                strings.ToLower(transfer.Chain)),
            EntryType:   string(domain.EntryTypeDebit),
            Amount:      netAmount,
            Currency:    string(transfer.SourceCurrency),
            Description: "Debit crypto asset",
        },
        {
            AccountCode: "expenses:provider:onramp",
            EntryType:   string(domain.EntryTypeDebit),
            Amount:      onRampFee,
            Currency:    string(transfer.SourceCurrency),
            Description: "Debit on-ramp fee",
        },
        {
            AccountCode: domain.TenantAccountCode(slug,
                fmt.Sprintf("assets:bank:%s:clearing",
                    strings.ToLower(string(transfer.SourceCurrency)))),
            EntryType:   string(domain.EntryTypeCredit),
            Amount:      transfer.SourceAmount,
            Currency:    string(transfer.SourceCurrency),
            Description: "Credit clearing account",
        },
    }
```

2. **Validates balance** -- debits must equal credits before the entry is
   queued:

```go
    if err := validateLedgerLines(onRampLines); err != nil {
        return fmt.Errorf("settla-core: handle on-ramp result %s: ledger entries imbalanced: %w",
            transferID, err)
    }
```

3. **Creates three outbox entries** and transitions atomically:

```go
    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentLedgerPost, ledgerPayload),
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentBlockchainSend, blockchainPayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventOnRampCompleted, transferEventPayload(transfer.ID, transfer.TenantID)),
    }

    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusSettling, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "handle on-ramp result", transferID)
    }
```

> **Key Insight:** The ledger post and blockchain send intents are created
> simultaneously. The LedgerWorker and BlockchainWorker can execute in
> parallel -- there is no dependency between them. This parallelism reduces
> end-to-end latency by executing accounting and on-chain settlement
> concurrently.

### Failure Path: ON_RAMPING -> REFUNDING

```go
    } else {
        location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))
        releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
            TransferID: transfer.ID,
            TenantID:   transfer.TenantID,
            Currency:   transfer.SourceCurrency,
            Amount:     transfer.SourceAmount,
            Location:   location,
            Reason:     "onramp_failure",
        })

        entries := []domain.OutboxEntry{
            domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
                domain.IntentTreasuryRelease, releasePayload),
            domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
                domain.EventProviderOnRampFailed,
                transferEventPayload(transfer.ID, transfer.TenantID)),
        }

        if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
            domain.TransferStatusRefunding, transfer.Version, entries); err != nil {
            return wrapTransitionError(err, "handle on-ramp result", transferID)
        }
    }
```

On failure: release the treasury reservation (the money was never converted)
and transition to REFUNDING.

---

## 3.3.5 Step-by-Step: HandleSettlementResult

After the blockchain send confirms (or fails):

```go
func (e *Engine) HandleSettlementResult(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID, result domain.IntentResult) error {

    transfer, err := e.loadTransferForStep(ctx, tenantID, transferID,
        domain.TransferStatusSettling)
```

### Success Path: SETTLING -> OFF_RAMPING

The engine loads off-ramp fallback alternatives (only those sharing the same
chain + stablecoin, since the on-ramp already delivered on that chain):

```go
    var offRampAlts []domain.OffRampFallback
    if transfer.QuoteID != nil {
        quote, qErr := e.transferStore.GetQuote(ctx, transfer.TenantID, *transfer.QuoteID)
        if qErr == nil && quote != nil {
            for _, alt := range quote.Route.AlternativeRoutes {
                if alt.Chain == transfer.Chain && alt.StableCoin == transfer.StableCoin {
                    offRampAlts = append(offRampAlts, domain.OffRampFallback{
                        ProviderID: alt.OffRampProvider,
                        Fee:        alt.Fee,
                        Rate:       alt.Rate,
                    })
                }
            }
        }
    }
```

Then builds the off-ramp intent and transitions:

```go
    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentProviderOffRamp, offRampPayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventSettlementCompleted,
            transferEventPayload(transfer.ID, transfer.TenantID)),
    }

    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusOffRamping, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "handle settlement result", transferID)
    }
```

### Failure Path: SETTLING -> FAILED

Settlement failure is more severe than on-ramp failure because money has
already been converted and potentially sent on-chain. The engine creates
compensation intents for both treasury release AND ledger reversal:

```go
    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentTreasuryRelease, releasePayload),
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentLedgerReverse, reversePayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventBlockchainFailed,
            transferEventPayload(transfer.ID, transfer.TenantID)),
    }

    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusFailed, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "handle settlement result", transferID)
    }
```

---

## 3.3.6 Step-by-Step: HandleOffRampResult and CompleteTransfer

The off-ramp result handler has the simplest success path -- it delegates to
`CompleteTransfer`:

```go
func (e *Engine) HandleOffRampResult(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID, result domain.IntentResult) error {

    if result.Success {
        return e.CompleteTransfer(ctx, tenantID, transferID)
    }
    // ... failure handling ...
}
```

### CompleteTransfer: The Terminal Success State

```go
func (e *Engine) CompleteTransfer(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID) error {

    transfer, err := e.loadTransfer(ctx, tenantID, transferID)
    if err != nil { /* ... */ }

    if transfer.Status != domain.TransferStatusOffRamping {
        return fmt.Errorf("settla-core: complete transfer %s: %w",
            transferID, domain.ErrInvalidTransition(
                string(transfer.Status), string(domain.TransferStatusCompleted)))
    }
```

CompleteTransfer produces the most outbox entries of any single transition --
four entries:

```go
    entries := []domain.OutboxEntry{
        // 1. Release treasury reservation (money has been delivered)
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentTreasuryRelease, releasePayload),

        // 2. Post final ledger entries (pending -> payable + revenue)
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentLedgerPost, ledgerPayload),

        // 3. Notify tenant via webhook
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentWebhookDeliver, webhookPayload),

        // 4. Lifecycle event
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventTransferCompleted,
            transferEventPayload(transfer.ID, transfer.TenantID)),
    }
```

The completion ledger entries move money from pending to payable:

```go
    completionLines := []domain.LedgerLineEntry{
        {
            AccountCode: domain.TenantAccountCode(slug, "liabilities:customer:pending"),
            EntryType:   string(domain.EntryTypeDebit),
            Amount:      transfer.SourceAmount,
            Currency:    string(transfer.SourceCurrency),
            Description: "Debit customer pending",
        },
        {
            AccountCode: domain.TenantAccountCode(slug, "liabilities:payable:recipient"),
            EntryType:   string(domain.EntryTypeCredit),
            Amount:      netAmount,
            Currency:    string(transfer.SourceCurrency),
            Description: "Credit recipient payable (net of fees)",
        },
        {
            AccountCode: domain.TenantAccountCode(slug, "revenue:fees:settlement"),
            EntryType:   string(domain.EntryTypeCredit),
            Amount:      totalFees,
            Currency:    string(transfer.SourceCurrency),
            Description: "Credit settlement fee revenue",
        },
    }
```

> **Key Insight:** Completion is not free. Even on the happy path, three
> workers must execute: TreasuryWorker (release reservation), LedgerWorker
> (post final entries), and WebhookWorker (notify tenant). All three execute
> in parallel since they are independent intents in independent NATS streams.

---

## 3.3.7 The ProcessTransfer Shortcut (Testing Only)

For integration tests and demos, the engine provides a method that runs the
entire pipeline synchronously with simulated success at each step:

```go
func (e *Engine) ProcessTransfer(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID) error {

    if err := e.FundTransfer(ctx, tenantID, transferID); err != nil {
        return err
    }
    if err := e.InitiateOnRamp(ctx, tenantID, transferID); err != nil {
        return err
    }
    if err := e.HandleOnRampResult(ctx, tenantID, transferID,
        domain.IntentResult{Success: true}); err != nil {
        return err
    }
    if err := e.HandleSettlementResult(ctx, tenantID, transferID,
        domain.IntentResult{Success: true, TxHash: "0xdemo"}); err != nil {
        return err
    }
    return e.HandleOffRampResult(ctx, tenantID, transferID,
        domain.IntentResult{Success: true})
}
```

This method exists because in production, the lifecycle is driven by workers
processing outbox entries asynchronously. For tests, you need a way to drive
the entire pipeline without standing up NATS, the relay, and all eleven
workers.

---

## 3.3.8 Summary: Outbox Entries by Transition

```
State Transition Summary
=========================

Transition                 Intents Created              Events Created
------------------------   --------------------------   ----------------------
CREATED (new)              (none)                       transfer.created
CREATED -> FUNDED          treasury.reserve             transfer.funded
FUNDED -> ON_RAMPING       provider.onramp.execute      (none)
ON_RAMPING -> SETTLING     ledger.post                  onramp.completed
                           blockchain.send
ON_RAMPING -> REFUNDING    treasury.release             provider.onramp.failed
SETTLING -> OFF_RAMPING    provider.offramp.execute     settlement.completed
SETTLING -> FAILED         treasury.release             blockchain.failed
                           ledger.reverse
OFF_RAMPING -> COMPLETED   treasury.release             transfer.completed
                           ledger.post
                           webhook.deliver
OFF_RAMPING -> FAILED      treasury.release             provider.offramp.failed
                           ledger.reverse
                           webhook.deliver
```

---

## Common Mistakes

1. **Calling ProcessTransfer in production.** This method bypasses the
   entire async pipeline. It is for tests and demos only. In production,
   workers drive the lifecycle by calling Handle*Result methods.

2. **Assuming linear execution.** The ledger post and blockchain send in
   step 4 execute in parallel. Do not write code that assumes one completes
   before the other.

3. **Forgetting that HandleSettlementResult waits for BOTH ledger and
   blockchain.** In the current implementation, the engine transitions to
   SETTLING and waits for the blockchain confirmation. The ledger post
   result is not explicitly awaited at this step -- it happens concurrently.
   The BlockchainWorker calls back with HandleSettlementResult.

4. **Not handling the off-ramp failure case.** Off-ramp failure is the
   most complex failure because the fiat was already converted to stablecoin
   and settled on-chain. The engine must reverse both the ledger entries AND
   release the treasury reservation. Missing either leaves money in limbo.

---

## Exercises

1. **Count the outbox entries:** For a happy-path transfer from CREATED to
   COMPLETED, count the total number of outbox entries (intents + events).
   Verify your count against the summary table above.

2. **Draw the failure path:** A transfer fails at the SETTLING stage
   (blockchain send times out). Draw the complete sequence of engine methods,
   outbox entries, and worker actions from the failure through compensation.

3. **Add a new state:** Suppose you need to add an AML screening step
   between FUNDED and ON_RAMPING. Update the `ValidTransitions` map, write
   the `InitiateAMLScreen` engine method, and define the `HandleAMLResult`
   method with success and failure paths.

4. **Parallel vs. sequential:** Identify all places in the lifecycle where
   multiple outbox intents can execute in parallel. What would happen if you
   made the ledger post and blockchain send sequential instead?

---

## What's Next

In Chapter 3.4, we dive deep into failure paths -- `FailTransfer`,
`InitiateRefund`, `HandleRefundResult`, idempotency enforcement, and the
compensation intents created at each failure point.
