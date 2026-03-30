# Chapter 3.4: Failure Paths and Compensation

**Reading time:** ~30 minutes
**Prerequisites:** Chapters 3.1-3.3
**Code references:** `core/engine.go`, `domain/transfer.go`, `domain/outbox.go`

---

## Learning Objectives

By the end of this chapter you will be able to:

1. Describe every failure path in the transfer lifecycle and the compensation
   intents each produces.
2. Walk through `FailTransfer`, `InitiateRefund`, and `HandleRefundResult`
   with actual code.
3. Explain how idempotency enforcement via `GetByIdempotencyKey` prevents
   duplicate transfers.
4. Draw a failure compensation diagram showing which resources are released
   at each failure point.
5. Describe what happens when a refund itself fails.

---

## 3.4.1 The Failure Landscape

Not every transfer succeeds. At 50M transactions/day, even a 0.1% failure
rate means 50,000 failures daily. The engine must handle each failure
gracefully, ensuring that reserved funds are released, ledger entries are
reversed, and tenants are notified.

Here is every failure point in the lifecycle and what resources have been
acquired by that point:

```
Failure Compensation Matrix
=============================

Failure Point       Resources Acquired       Compensation Required
-----------------   ---------------------    -------------------------
CREATED -> FAILED   None                     Webhook notification only
FUNDED -> REFUND    Treasury reservation     Release treasury
ON_RAMPING fail     Treasury reservation     Release treasury
SETTLING fail       Treasury reservation     Release treasury
                    Ledger entries           Reverse ledger
                    (blockchain may have     (blockchain reversal is
                     partial execution)       not possible -- manual)
OFF_RAMPING fail    Treasury reservation     Release treasury
                    Ledger entries           Reverse ledger
                    Blockchain settlement    (cannot reverse on-chain)
                    (stablecoin delivered)   Webhook notification
```

> **Key Insight:** The further a transfer progresses before failing, the
> more compensation work is required. A failure at CREATED requires zero
> compensation. A failure at OFF_RAMPING requires treasury release, ledger
> reversal, and webhook notification. This is why the engine creates different
> sets of compensation intents depending on where the failure occurs.

---

## 3.4.2 FailTransfer: The General-Purpose Failure Method

`FailTransfer` is used when any part of the system needs to mark a transfer
as failed with a reason and code. It is the catch-all failure transition.

```go
func (e *Engine) FailTransfer(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID, reason string, code string) error {

    transfer, err := e.loadTransfer(ctx, tenantID, transferID)
    if err != nil {
        return fmt.Errorf("settla-core: fail transfer %s: %w", transferID, err)
    }

    if !transfer.CanTransitionTo(domain.TransferStatusFailed) {
        return fmt.Errorf("settla-core: fail transfer %s: %w",
            transferID, domain.ErrInvalidTransition(
                string(transfer.Status), string(domain.TransferStatusFailed)))
    }
```

Notice: `FailTransfer` uses `loadTransfer` (not `loadTransferForStep`). It
does not expect a specific status -- it accepts any status that the state
machine allows to transition to FAILED. Looking at the `ValidTransitions`
map:

```go
TransferStatusCreated:    {TransferStatusFunded, TransferStatusFailed},
TransferStatusOnRamping:  {TransferStatusSettling, TransferStatusRefunding, TransferStatusFailed},
TransferStatusSettling:   {TransferStatusOffRamping, TransferStatusFailed},
TransferStatusOffRamping: {TransferStatusCompleted, TransferStatusFailed},
```

So CREATED, ON_RAMPING, SETTLING, and OFF_RAMPING can all transition directly
to FAILED. But FUNDED cannot -- it must go through REFUNDING first (because
treasury funds are reserved and must be explicitly released).

### Compensation Intents

```go
    location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

    releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        Currency:   transfer.SourceCurrency,
        Amount:     transfer.SourceAmount,
        Location:   location,
        Reason:     "transfer_failed",
    })
    if err != nil {
        return fmt.Errorf("settla-core: fail transfer %s: marshalling release payload: %w",
            transferID, err)
    }

    webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        EventType:  domain.EventTransferFailed,
        Data:       []byte(fmt.Sprintf(`{"reason":%q,"code":%q}`, reason, code)),
    })
    if err != nil {
        return fmt.Errorf("settla-core: fail transfer %s: marshalling webhook payload: %w",
            transferID, err)
    }

    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentTreasuryRelease, releasePayload),
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentWebhookDeliver, webhookPayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventTransferFailed,
            transferEventPayload(transfer.ID, transfer.TenantID)),
    }
```

`FailTransfer` always creates:
1. **Treasury release** -- even if the treasury was never reserved (the
   treasury worker handles this idempotently).
2. **Webhook notification** -- with the failure reason and code embedded in
   the payload so the tenant knows why their transfer failed.
3. **Lifecycle event** -- `transfer.failed` for audit and analytics.

### Atomic Write with Metrics

```go
    corridor := observability.FormatCorridor(
        string(transfer.SourceCurrency), string(transfer.DestCurrency))
    if e.metrics != nil {
        e.metrics.TransfersTotal.WithLabelValues(
            transfer.TenantID.String(), string(domain.TransferStatusFailed), corridor).Inc()
    }

    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusFailed, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "fail transfer", transferID)
    }
```

Note: the metrics increment happens *before* the atomic write. This is
intentional -- at worst, the metric is slightly ahead of reality if the
write fails. The alternative (incrementing after) risks losing the metric
entirely if the process crashes between the write and the increment.

---

## 3.4.3 Failure Paths in HandleOnRampResult

When the on-ramp provider call fails, `HandleOnRampResult` takes the failure
branch:

```go
    } else {
        // On-ramp failed -- release treasury and transition to refunding
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

At this point, only the treasury reservation exists (no ledger entries, no
blockchain transactions). So the compensation is simple: release the treasury
reservation and transition to REFUNDING.

The `Reason: "onramp_failure"` field is important. The treasury worker uses
`transfer_id + reason` as an idempotency key. If the same transfer fails at
multiple stages (e.g., on-ramp retry fails, then settlement fails), each
generates a release intent with a different reason, preventing idempotency
collisions.

---

## 3.4.4 Failure Paths in HandleSettlementResult

Settlement failure is more complex because by this point, ledger entries have
been posted:

```go
    } else {
        // Settlement failed -- release treasury + reverse ledger
        location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

        releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
            TransferID: transfer.ID,
            TenantID:   transfer.TenantID,
            Currency:   transfer.SourceCurrency,
            Amount:     transfer.SourceAmount,
            Location:   location,
            Reason:     "settlement_failure",
        })

        reversePayload, err := json.Marshal(domain.LedgerPostPayload{
            TransferID:     transfer.ID,
            TenantID:       transfer.TenantID,
            IdempotencyKey: fmt.Sprintf("reverse-settle:%s", transfer.ID),
            Description:    fmt.Sprintf("Reverse settlement for transfer %s: %s",
                transfer.ID, result.Error),
            ReferenceType:  "reversal",
        })

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
    }
```

Two compensation intents:
1. **Treasury release** with reason `"settlement_failure"`
2. **Ledger reversal** with idempotency key `"reverse-settle:{transferID}"`

The ledger reversal's `IdempotencyKey` format ensures that each reversal
type gets its own deduplication namespace. This prevents a settlement reversal
from being confused with a refund reversal.

---

## 3.4.5 Failure Paths in HandleOffRampResult

Off-ramp failure is the most severe because the stablecoin has already been
settled on-chain. The engine creates three compensation intents:

```go
    // Off-ramp failed -- release treasury + reverse ledger + notify tenant
    location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

    releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        Currency:   transfer.SourceCurrency,
        Amount:     transfer.SourceAmount,
        Location:   location,
        Reason:     "offramp_failure",
    })

    reversePayload, err := json.Marshal(domain.LedgerPostPayload{
        TransferID:     transfer.ID,
        TenantID:       transfer.TenantID,
        IdempotencyKey: fmt.Sprintf("reverse-offramp:%s", transfer.ID),
        Description:    fmt.Sprintf("Reverse off-ramp for transfer %s: %s",
            transfer.ID, result.Error),
        ReferenceType:  "reversal",
    })

    webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        EventType:  domain.EventTransferFailed,
    })

    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentTreasuryRelease, releasePayload),
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentLedgerReverse, reversePayload),
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentWebhookDeliver, webhookPayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventProviderOffRampFailed,
            transferEventPayload(transfer.ID, transfer.TenantID)),
    }
```

Four outbox entries: three intents + one event. This is the most expensive
failure in the system.

---

## 3.4.6 InitiateRefund: The Explicit Refund Path

`InitiateRefund` is for cases where the system or an operator decides to
refund a transfer that has not yet failed:

```go
func (e *Engine) InitiateRefund(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID) error {

    transfer, err := e.loadTransfer(ctx, tenantID, transferID)
    if err != nil {
        return fmt.Errorf("settla-core: refund transfer %s: %w", transferID, err)
    }

    if !transfer.CanTransitionTo(domain.TransferStatusRefunding) {
        return fmt.Errorf("settla-core: refund transfer %s: %w",
            transferID, domain.ErrInvalidTransition(
                string(transfer.Status), string(domain.TransferStatusRefunding)))
    }
```

Which states can transition to REFUNDING? From the state machine:

```go
TransferStatusFunded:     {TransferStatusOnRamping, TransferStatusRefunding},
TransferStatusOnRamping:  {TransferStatusSettling, TransferStatusRefunding, TransferStatusFailed},
TransferStatusFailed:     {TransferStatusRefunding},
```

So FUNDED, ON_RAMPING, and FAILED can be refunded. CREATED cannot (nothing
to refund). SETTLING and OFF_RAMPING cannot (blockchain transactions are
in-flight and cannot be easily reversed).

### Refund Compensation Intents

```go
    reversePayload, err := json.Marshal(domain.LedgerPostPayload{
        TransferID:     transfer.ID,
        TenantID:       transfer.TenantID,
        IdempotencyKey: fmt.Sprintf("refund:%s", transfer.ID),
        Description:    fmt.Sprintf("Refund for transfer %s", transfer.ID),
        ReferenceType:  "reversal",
    })

    releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        Currency:   transfer.SourceCurrency,
        Amount:     transfer.SourceAmount,
        Location:   location,
        Reason:     "refund",
    })

    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentLedgerReverse, reversePayload),
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentTreasuryRelease, releasePayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventRefundInitiated,
            transferEventPayload(transfer.ID, transfer.TenantID)),
    }

    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusRefunding, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "refund transfer", transferID)
    }
```

Two compensation intents: ledger reversal and treasury release. The
idempotency key `"refund:{transferID}"` ensures the ledger reversal is
distinct from settlement or off-ramp reversals.

---

## 3.4.7 HandleRefundResult: What Happens When Refunds Fail

```go
func (e *Engine) HandleRefundResult(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID, result domain.IntentResult) error {

    transfer, err := e.loadTransferForStep(ctx, tenantID, transferID,
        domain.TransferStatusRefunding)
    if err != nil {
        return fmt.Errorf("settla-core: handle refund result %s: %w", transferID, err)
    }

    if result.Success {
        webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
            TransferID: transfer.ID,
            TenantID:   transfer.TenantID,
            EventType:  domain.EventTransferFailed,
            Data:       []byte(fmt.Sprintf(
                `{"reason":"refund_completed","transfer_id":"%s"}`, transfer.ID)),
        })

        entries := []domain.OutboxEntry{
            domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
                domain.IntentWebhookDeliver, webhookPayload),
            domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
                domain.EventRefundCompleted,
                transferEventPayload(transfer.ID, transfer.TenantID)),
        }

        if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
            domain.TransferStatusFailed, transfer.Version, entries); err != nil {
            return wrapTransitionError(err, "handle refund result", transferID)
        }
```

On success: the refund completed. The transfer transitions REFUNDING ->
REFUNDED (the terminal state for refunded transfers), and a webhook notifies
the tenant.

> **Key Insight:** A successful refund results in a REFUNDED status -- a
> distinct terminal state from FAILED. This allows tenants and analytics to
> distinguish between transfers that failed without compensation and transfers
> where the refund was successfully processed.

### When the Refund Itself Fails

```go
    } else {
        e.logger.Warn("settla-core: refund failed, awaiting recovery escalation",
            "transfer_id", transfer.ID,
            "tenant_id", transfer.TenantID,
            "error", result.Error,
        )
    }
```

When a refund fails, the engine does **nothing**. It does not transition the
state. It does not create new outbox entries. It logs a warning and waits.

This is deliberate. The `core/recovery` module (the stuck-transfer detector)
runs every 60 seconds and looks for transfers that have been in REFUNDING for
too long. When it finds one, it escalates to manual review. A human operator
then investigates and resolves the issue.

```
Refund Failure Escalation
===========================

1. Worker executes refund intent
2. Refund fails (e.g., provider API error)
3. HandleRefundResult logs warning, does NOT transition
4. Transfer stays in REFUNDING state
5. Recovery detector (60s interval) finds stale REFUNDING transfer
6. Recovery detector escalates to manual review
7. Human operator resolves
```

This design avoids infinite retry loops. If a refund keeps failing, the
system does not keep creating new compensation intents (which would also
fail). It stops and asks for human help.

---

## 3.4.8 Idempotency Enforcement

The engine's first line of defense against duplicate transfers is the
idempotency key check in `CreateTransfer`:

```go
    // e. Check idempotency key
    if req.IdempotencyKey != "" {
        existing, err := e.transferStore.GetTransferByIdempotencyKey(
            ctx, tenantID, req.IdempotencyKey)
        if err == nil && existing != nil {
            return existing, nil
        }
    }
```

The SQL behind this lookup:

```sql
-- name: GetTransferByIdempotencyKey :one
SELECT * FROM transfers
WHERE tenant_id = $1 AND idempotency_key = $2
  AND created_at >= now() - INTERVAL '24 hours'
LIMIT 1;
```

Key properties:

1. **Tenant-scoped**: `tenant_id = $1` ensures Lemfi's idempotency keys
   never collide with Fincra's.

2. **Time-windowed**: The 24-hour window prevents the idempotency table from
   growing unboundedly. After 24 hours, the same key can be reused (which is
   fine -- if a client retries after 24 hours, they probably intend a new
   transfer).

3. **Returns the existing transfer**: When a duplicate is detected, the engine
   returns the existing transfer as if the request succeeded. The client sees
   the same response whether this is the first or fifth retry. This is the
   key property of idempotency -- the client cannot distinguish a fresh
   creation from a duplicate detection.

### Idempotency in Outbox Entries

Outbox entries themselves do not have idempotency keys. Instead, each intent
payload carries an idempotency key that the executing worker uses:

```
Intent Idempotency Keys
=========================

Intent Type              Idempotency Key Format
-------------------      ------------------------------------
ledger.post (on-ramp)    "onramp:{transferID}"
ledger.post (complete)   "complete:{transferID}"
ledger.reverse (settle)  "reverse-settle:{transferID}"
ledger.reverse (offramp) "reverse-offramp:{transferID}"
ledger.reverse (refund)  "refund:{transferID}"
treasury.release         "{transferID}:{reason}"
```

This layered approach ensures idempotency at every level:
- Transfer creation: idempotency key from client
- State transitions: optimistic locking (version check)
- Side effect execution: intent-specific idempotency keys
- NATS delivery: JetStream 2-minute dedup window

---

## 3.4.9 Complete Failure Compensation Diagram

```
Failure Compensation by Stage
===============================

STAGE: CREATED
  Compensation: None (no resources acquired)
  Intents: webhook.deliver (optional)

STAGE: FUNDED (treasury reserved)
  Compensation: Release treasury
  Intents: treasury.release (reason: "refund")
           ledger.reverse (idempotency: "refund:{id}")

STAGE: ON_RAMPING (treasury reserved, provider call in-flight)
  On failure:
  Compensation: Release treasury
  Intents: treasury.release (reason: "onramp_failure")

STAGE: SETTLING (treasury reserved, ledger posted, blockchain in-flight)
  On failure:
  Compensation: Release treasury + Reverse ledger
  Intents: treasury.release (reason: "settlement_failure")
           ledger.reverse (idempotency: "reverse-settle:{id}")

STAGE: OFF_RAMPING (treasury reserved, ledger posted, blockchain settled)
  On failure:
  Compensation: Release treasury + Reverse ledger + Notify tenant
  Intents: treasury.release (reason: "offramp_failure")
           ledger.reverse (idempotency: "reverse-offramp:{id}")
           webhook.deliver (event: transfer.failed)

STAGE: REFUNDING (explicit refund in progress)
  On success: Notify tenant
  Intents: webhook.deliver (event: transfer.failed, reason: refund_completed)
  On failure: No action, recovery detector escalates to manual review
```

---

## 3.4.10 The CHECK-BEFORE-CALL Pattern

Workers that execute intents use CHECK-BEFORE-CALL to prevent double
execution when NATS redelivers a message:

```
CHECK-BEFORE-CALL Pattern
============================

Worker receives intent from NATS:

  1. CHECK: Query provider_transactions table
     SELECT * FROM provider_transactions
     WHERE tenant_id = $1 AND transfer_id = $2 AND tx_type = $3

  2. If row exists with status 'completed':
     - Intent already executed
     - ACK the message
     - Skip execution

  3. If row does not exist:
     - INSERT a "claiming" row (INSERT ON CONFLICT DO NOTHING)
     - If INSERT succeeded: we own this intent
     - If INSERT returned no rows: another worker claimed it

  4. CALL: Execute the side effect (provider API, blockchain tx, etc.)

  5. UPDATE provider_transactions SET status = 'completed'
```

This pattern is implemented in the database with a unique constraint:

```sql
-- name: ClaimProviderTransaction :one
INSERT INTO provider_transactions (
    tenant_id, provider, tx_type,
    transfer_id, status, amount, currency, metadata
) VALUES (
    @tenant_id, @provider, @tx_type,
    @transfer_id, 'claiming', 0, '', '{}'
)
ON CONFLICT (tenant_id, transfer_id, tx_type) DO NOTHING
RETURNING id;
```

The `ON CONFLICT DO NOTHING` ensures that only one worker instance can claim
execution of a given intent. All others silently skip it.

---

## Common Mistakes

1. **Assuming FailTransfer works from any state.** It does not. FUNDED
   cannot transition directly to FAILED -- it must go through REFUNDING.
   COMPLETED and REFUNDED are terminal states that cannot transition anywhere.

2. **Creating compensation intents without unique idempotency keys.** If two
   different failure paths produce ledger reversals with the same idempotency
   key, only one will execute. Use the `"reverse-settle:{id}"` and
   `"reverse-offramp:{id}"` format to distinguish them.

3. **Retrying refunds indefinitely.** The engine deliberately does not retry
   failed refunds. Infinite retries can cascade into worse failures. The
   recovery detector + manual review is the correct escalation path.

4. **Ignoring the 24-hour idempotency window.** If a client retries a transfer
   creation after 24 hours with the same idempotency key, it will create a
   *new* transfer. If this is unacceptable for your use case, extend the
   window in the SQL query.

5. **Not scoping treasury release by reason.** The `Reason` field in
   `TreasuryReleasePayload` is not just for logging -- it is part of the
   idempotency key. Without it, a release for `"onramp_failure"` and a
   release for `"transfer_complete"` would be deduplicated as the same
   operation.

---

## Exercises

1. **Map the failure cascade:** A transfer is in SETTLING when the blockchain
   node goes down. The blockchain send times out after 30 seconds. Trace every
   method call, outbox entry, and worker action from the timeout through final
   resolution. Include the CHECK-BEFORE-CALL steps.

2. **Design a partial refund:** The current `InitiateRefund` refunds the full
   `SourceAmount`. Design a `InitiatePartialRefund(ctx, tenantID, transferID,
   refundAmount decimal.Decimal)` method. What validations are needed? What
   changes in the ledger reversal payload?

3. **Idempotency collision:** Two API requests arrive simultaneously with the
   same idempotency key. The first passes the idempotency check (no existing
   transfer) and starts creating. The second also passes the check (the first
   has not committed yet). What prevents a duplicate? (Hint: look at the
   database constraint on `(tenant_id, idempotency_key)`.)

4. **Recovery detector simulation:** Write pseudocode for a recovery detector
   that finds transfers stuck in REFUNDING for more than 5 minutes. What
   query would you run? What action would you take?

5. **Count compensation intents:** For a transfer that fails at OFF_RAMPING
   after a successful on-ramp, settlement, and partial off-ramp, list every
   outbox entry created across the entire lifecycle (both happy-path entries
   and compensation entries). What is the total count?

---

## What's Next

In Chapter 3.5, we examine the data layer that makes all of this possible --
the SQL queries, SQLC code generation, the adapter pattern, and the
`TransitionWithOutbox` atomic transaction that underpins every state change.
