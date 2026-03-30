# Chapter 5.4: CHECK-BEFORE-CALL -- Preventing Double Execution

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why at-least-once delivery makes double-execution possible and why this is dangerous in financial systems
2. Describe the CHECK-BEFORE-CALL pattern and its three-phase structure (CLAIM-CALL-UPDATE)
3. Trace the `ClaimProviderTransaction` function's `INSERT ON CONFLICT DO NOTHING` semantics
4. Distinguish between normal delivery, redelivery, and concurrent delivery scenarios
5. Implement idempotency for different worker types (provider vs. ledger vs. treasury)
6. Understand the relationship between NATS dedup, outbox dedup, and worker-level dedup

---

## The Double-Execution Problem

NATS JetStream provides **at-least-once** delivery: a message is guaranteed to be delivered at least once, but may be delivered more than once. Redelivery happens when:

1. **Worker crashes** after starting to process a message but before acking
2. **AckWait timeout** (30s) expires because the worker is slow
3. **NATS restarts** and replays unacked messages
4. **Network partition** causes NATS to consider a message unacked

In a payment system, double-execution means double-charging. If the ProviderWorker calls `provider.Execute(onRampRequest)` twice for the same transfer, the tenant's customer is charged twice. This is a compliance violation and a financial loss.

```
DANGEROUS: At-Least-Once Without Idempotency

Worker Instance A                    Provider API
    |                                    |
    |-- Execute(transfer_123) ---------->|
    |                   (timeout)        |-- charges customer
    |   (NATS redelivers)                |
    |                                    |
Worker Instance B                        |
    |-- Execute(transfer_123) ---------->|
    |                                    |-- charges customer AGAIN
    |<-- success -----------------------|
    |-- ack                              |
```

---

## The Three Layers of Deduplication

Settla has three independent deduplication layers:

```
Layer 1: Outbox Relay --> NATS
  - NATS Msg-Id = outbox entry UUID
  - NATS dedup window: 5 minutes
  - Prevents: relay re-publishing the same outbox entry

Layer 2: NATS --> Worker (BackOff + MaxDeliver)
  - NATS tracks delivery count per message
  - After 6 deliveries, message goes to DLQ
  - Prevents: infinite retry loops

Layer 3: Worker --> External System (CHECK-BEFORE-CALL)
  - Worker checks provider_transactions table before calling
  - INSERT ON CONFLICT DO NOTHING for atomic claim
  - Prevents: double-execution of side effects
```

Layer 3 is the last line of defense and the most critical. Even if layers 1 and 2 allow a message to reach the worker twice, layer 3 ensures the external call is made at most once.

---

## The CLAIM-CALL-UPDATE Pattern

The ProviderWorker implements CHECK-BEFORE-CALL using a three-phase pattern:

```
Phase 1: CLAIM
  - INSERT INTO provider_transactions ... ON CONFLICT DO NOTHING
  - If insert succeeds: we own execution (proceed to phase 2)
  - If conflict:        another worker already claimed (skip, ACK)
  - If terminal status: work is already done (skip, ACK)

Phase 2: CALL
  - Execute the external call (provider API, blockchain RPC)
  - This is the only phase with side effects

Phase 3: UPDATE
  - Update provider_transactions with result (completed/failed)
  - Report result to engine
```

---

## The ClaimProviderTransaction Interface

From `node/worker/provider_worker.go`:

```go
type ProviderTransferStore interface {
    // GetProviderTransaction looks up an existing provider transaction.
    // Returns nil, nil if not found.
    GetProviderTransaction(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID, txType string) (*domain.ProviderTx, error)

    // CreateProviderTransaction records a new provider transaction.
    CreateProviderTransaction(ctx context.Context, transferID uuid.UUID,
        txType string, tx *domain.ProviderTx) error

    // UpdateProviderTransaction updates an existing provider transaction.
    UpdateProviderTransaction(ctx context.Context, transferID uuid.UUID,
        txType string, tx *domain.ProviderTx) error

    // ClaimProviderTransaction atomically claims a provider transaction
    // slot using INSERT ON CONFLICT DO NOTHING semantics.
    // Returns non-nil UUID if claimed (this worker owns execution),
    // or nil if already claimed by another worker.
    // Returns nil, nil (not an error) if the transaction already has
    // a terminal status (completed/confirmed/pending).
    ClaimProviderTransaction(ctx context.Context,
        params ClaimProviderTransactionParams) (*uuid.UUID, error)

    // UpdateTransferRoute updates provider IDs during fallback routing.
    UpdateTransferRoute(ctx context.Context, transferID uuid.UUID,
        onRampProvider, offRampProvider, chain string,
        stableCoin domain.Currency) error

    // DeleteProviderTransaction removes a record, allowing fallback
    // re-claim.
    DeleteProviderTransaction(ctx context.Context, transferID uuid.UUID,
        txType string) error
}
```

The `ClaimProviderTransactionParams` struct:

```go
type ClaimProviderTransactionParams struct {
    TenantID   uuid.UUID
    TransferID uuid.UUID
    TxType     string    // "onramp", "offramp", "blockchain"
    Provider   string    // provider ID or chain name
}
```

---

## In-Memory Implementation (for Testing)

The test implementation reveals the claim logic clearly:

```go
func (s *InMemoryProviderTransferStore) ClaimProviderTransaction(
    _ context.Context,
    params ClaimProviderTransactionParams,
) (*uuid.UUID, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    k := s.key(params.TransferID, params.TxType)
    if existing, ok := s.txs[k]; ok {
        switch existing.Status {
        case "completed", "confirmed", "pending":
            return nil, nil  // Already done -- caller should skip
        }
        // "failed" or "claiming" (stuck) -- allow re-claim
        delete(s.txs, k)
    }

    id := uuid.New()
    s.txs[k] = &domain.ProviderTx{ID: id.String(), Status: "claiming"}
    return &id, nil
}
```

**Key behaviors:**
- Returns `(*uuid.UUID, nil)` on successful claim -- the worker owns execution
- Returns `(nil, nil)` if already in terminal state -- the worker should ACK and skip
- Returns `(nil, error)` on database failure -- the worker should NAK for retry
- Re-claims are allowed for "failed" status (supports fallback routing)

In production, the database implementation uses `INSERT ... ON CONFLICT DO NOTHING` with a unique constraint on `(transfer_id, tx_type)`:

```sql
INSERT INTO provider_transactions (id, transfer_id, tx_type, provider_id,
                                    tenant_id, status, created_at)
VALUES ($1, $2, $3, $4, $5, 'claiming', NOW())
ON CONFLICT (transfer_id, tx_type) DO NOTHING
RETURNING id;
```

If the `RETURNING id` clause returns no rows, the insert was a no-op (conflict), meaning another worker already claimed it.

---

## The ProviderWorker's handleOnRamp

Here is how the CLAIM-CALL-UPDATE pattern is implemented for on-ramp execution:

```go
func (w *ProviderWorker) handleOnRamp(ctx context.Context,
                                       event domain.Event) error {
    payload, err := unmarshalEventData[domain.ProviderOnRampPayload](event)
    if err != nil {
        return nil  // ACK -- malformed payload, retrying won't help
    }

    budget := resilience.NewTimeoutBudget(ctx, 30*time.Second)

    for {
        // === PHASE 1: CLAIM ===
        claimCtx, claimCancel := budget.Allocate(5 * time.Second)
        claimID, err := w.transferStore.ClaimProviderTransaction(
            claimCtx, ClaimProviderTransactionParams{
                TenantID:   payload.TenantID,
                TransferID: payload.TransferID,
                TxType:     "onramp",
                Provider:   payload.ProviderID,
            })
        claimCancel()

        if err != nil {
            return fmt.Errorf("claim onramp for %s: %w",
                payload.TransferID, err)  // NAK
        }
        if claimID == nil {
            // Another worker already claimed or completed
            return nil  // ACK
        }

        // === PHASE 2: CALL ===
        callCtx, callCancel := budget.Allocate(20 * time.Second)
        tx, execErr := w.executeOnRamp(callCtx, payload.ProviderID,
            provider, domain.OnRampRequest{
                Amount:       payload.Amount,
                FromCurrency: payload.FromCurrency,
                ToCurrency:   payload.ToCurrency,
                Reference:    payload.Reference,
                QuotedRate:   payload.QuotedRate,
            })
        callCancel()

        if execErr != nil {
            // Try fallback if available (see circuit breaker chapter)
            if len(payload.Alternatives) > 0 {
                // ... fallback logic, continues loop ...
                continue
            }

            // === PHASE 3: UPDATE (failure) ===
            failedTx := &domain.ProviderTx{Status: "failed"}
            _ = w.transferStore.UpdateProviderTransaction(
                ctx, payload.TransferID, "onramp", failedTx)

            result := domain.IntentResult{
                Success:   false,
                Error:     execErr.Error(),
                ErrorCode: "ONRAMP_EXECUTION_FAILED",
            }
            return w.engine.HandleOnRampResult(ctx, payload.TenantID,
                payload.TransferID, result)
        }

        // === PHASE 3: UPDATE (success) ===
        w.transferStore.UpdateProviderTransaction(
            ctx, payload.TransferID, "onramp", tx)

        if tx.Status == "pending" {
            return nil  // ACK -- awaiting async webhook
        }

        result := domain.IntentResult{
            Success:     true,
            ProviderRef: tx.ExternalID,
            TxHash:      tx.TxHash,
        }
        return w.engine.HandleOnRampResult(ctx, payload.TenantID,
            payload.TransferID, result)
    }
}
```

---

## Sequence Diagram: Normal Delivery

```
NATS           ProviderWorker          DB (provider_tx)    Provider API
 |                  |                       |                   |
 |-- deliver msg -->|                       |                   |
 |                  |                       |                   |
 |                  |-- CLAIM ------------->|                   |
 |                  |   INSERT ... ON       |                   |
 |                  |   CONFLICT DO NOTHING |                   |
 |                  |<-- claimID (success) -|                   |
 |                  |                       |                   |
 |                  |-- CALL ------------------------------>|
 |                  |                       |                   |
 |                  |<-- result (success) ------------------|
 |                  |                       |                   |
 |                  |-- UPDATE ------------>|                   |
 |                  |   status = completed  |                   |
 |                  |                       |                   |
 |                  |-- engine.HandleResult |                   |
 |                  |                       |                   |
 |<-- ack ----------|                       |                   |
```

---

## Sequence Diagram: Redelivery After Crash

```
NATS           Worker A (crashed)      DB (provider_tx)    Provider API
 |                  |                       |                   |
 |-- deliver msg -->|                       |                   |
 |                  |-- CLAIM ------------->|                   |
 |                  |<-- claimID -----------|                   |
 |                  |-- CALL ------------------------------>|
 |                  |   X (CRASH)           |   charges customer|
 |                  |                       |                   |

  ... 30 seconds (AckWait) ...

NATS           Worker B (redelivery)   DB (provider_tx)    Provider API
 |                  |                       |                   |
 |-- redeliver ---->|                       |                   |
 |                  |                       |                   |
 |                  |-- CLAIM ------------->|                   |
 |                  |   INSERT ... ON       |                   |
 |                  |   CONFLICT DO NOTHING |                   |
 |                  |<-- nil (conflict!) ---|                   |
 |                  |                       |                   |
 |                  | "already claimed"     |                   |
 |<-- ack ----------|                       |  NO double call!  |
```

Worker B never calls the provider API because the claim failed (the row already exists from Worker A's claim).

---

## Sequence Diagram: Concurrent Delivery (Two Workers)

```
NATS           Worker A               DB (provider_tx)      Worker B
 |                  |                       |                   |
 |-- deliver ------>|                       |                   |
 |-- deliver (dup)->|----(concurrent)-------|----------------->|
 |                  |                       |                   |
 |                  |-- CLAIM (wins) ------>|<-- CLAIM (loses)-|
 |                  |<-- claimID -----------|-- nil (conflict)->|
 |                  |                       |                   |
 |                  |-- CALL (executes) --->|   |<-- ack (skip)|
 |                  |<-- result             |                   |
 |                  |-- UPDATE ------------>|                   |
 |<-- ack ----------|                       |                   |
```

The database's unique constraint on `(transfer_id, tx_type)` ensures exactly one worker wins the claim, regardless of timing.

---

## Idempotency Strategies by Worker Type

Different workers use different idempotency mechanisms:

### ProviderWorker / BlockchainWorker: CLAIM-CALL-UPDATE

As described above. The `provider_transactions` table is the idempotency guard.

### LedgerWorker: TigerBeetle Idempotency

The ledger worker does NOT use CHECK-BEFORE-CALL. Instead, it relies on TigerBeetle's built-in idempotency:

```go
func (w *LedgerWorker) handlePost(ctx context.Context,
                                   event domain.Event) error {
    payload, err := unmarshalEventData[domain.LedgerPostPayload](event)
    if err != nil {
        return nil  // ACK -- malformed
    }

    entry := domain.JournalEntry{
        ID:             uuid.Must(uuid.NewV7()),
        IdempotencyKey: payload.IdempotencyKey,  // <-- the guard
        // ...
    }

    _, err = w.ledger.PostEntries(ctx, entry)
    if err != nil {
        return fmt.Errorf("posting entry for transfer %s: %w",
            payload.TransferID, err)  // NAK for retry
    }

    return nil
}
```

TigerBeetle enforces idempotency at the engine level: if a transfer with the same `IdempotencyKey` already exists, the duplicate is silently accepted. No separate claim step needed.

### TreasuryWorker: Reference-Based Idempotency

The treasury manager uses the transfer ID as a reservation reference:

```go
func (w *TreasuryWorker) handleReserve(ctx context.Context,
                                        event domain.Event) error {
    payload, err := unmarshalEventData[domain.TreasuryReservePayload](event)
    // ...

    err = w.treasury.Reserve(
        ctx,
        payload.TenantID,
        payload.Currency,
        payload.Location,
        payload.Amount,
        payload.TransferID,  // <-- idempotency reference
    )
    // ...
}
```

The in-memory treasury manager tracks which transfer IDs have already reserved. Duplicate reserves for the same transfer ID are no-ops.

### WebhookWorker: Accept Duplicates

The webhook worker does NOT deduplicate. Sending a webhook notification twice is harmless -- the tenant's system should be idempotent on their end (using the `X-Settla-Delivery` header as a dedup key). The cost of tracking every webhook delivery in a database table is not justified by the minimal harm of a duplicate notification.

```go
func (w *WebhookWorker) handleDeliver(ctx context.Context,
                                       event domain.Event) error {
    // No claim step -- just deliver
    // ...
    req.Header.Set("X-Settla-Delivery", webhookBody.ID)  // dedup key
    // ...
}
```

### InboundWebhookWorker: Status-Based Idempotency

The inbound webhook worker checks the existing provider transaction status:

```go
func (w *InboundWebhookWorker) handleOnRampWebhook(ctx context.Context,
                                                     event domain.Event) error {
    payload, err := unmarshalEventData[domain.ProviderWebhookPayload](event)
    // ...

    existing, err := w.transferStore.GetProviderTransaction(
        ctx, payload.TenantID, payload.TransferID, "onramp")
    // ...

    // Idempotency: if already completed or confirmed, skip
    if existing.Status == "completed" || existing.Status == "confirmed" {
        return nil  // ACK -- duplicate webhook
    }

    // Update status and report to engine
    // ...
}
```

---

## Idempotency Strategy Summary

```
+----------------------+------------------------+---------------------------+
| Worker               | Strategy               | Guard Mechanism           |
+----------------------+------------------------+---------------------------+
| ProviderWorker       | CLAIM-CALL-UPDATE      | provider_transactions     |
|                      |                        | INSERT ON CONFLICT        |
+----------------------+------------------------+---------------------------+
| BlockchainWorker     | CLAIM-CALL-UPDATE      | provider_transactions     |
|                      |                        | INSERT ON CONFLICT        |
+----------------------+------------------------+---------------------------+
| LedgerWorker         | Delegated to TB        | TigerBeetle               |
|                      |                        | IdempotencyKey            |
+----------------------+------------------------+---------------------------+
| TreasuryWorker       | Reference-based        | In-memory reservation     |
|                      |                        | map (transfer ID)         |
+----------------------+------------------------+---------------------------+
| WebhookWorker        | Accept duplicates      | X-Settla-Delivery header  |
|                      |                        | (tenant-side dedup)       |
+----------------------+------------------------+---------------------------+
| InboundWebhookWorker | Status check           | GetProviderTransaction    |
|                      |                        | status == completed       |
+----------------------+------------------------+---------------------------+
| TransferWorker       | Engine state machine   | Invalid transition error  |
|                      |                        | --> ACK (skip)            |
+----------------------+------------------------+---------------------------+
```

---

## The Fallback Complication

When a provider call fails and alternatives are available, the worker needs to re-claim the slot for a different provider. This is why `DeleteProviderTransaction` exists:

```go
// Try fallback alternative if available
if len(payload.Alternatives) > 0 {
    alt := payload.Alternatives[0]
    remaining := payload.Alternatives[1:]

    // Delete the failed claim to allow re-claim
    w.transferStore.DeleteProviderTransaction(
        ctx, payload.TransferID, "onramp")

    // Update the transfer record with the new provider
    w.transferStore.UpdateTransferRoute(
        ctx, payload.TransferID,
        alt.ProviderID, alt.OffRampProvider,
        alt.Chain, alt.StableCoin)

    // Continue loop with fallback provider
    payload.ProviderID = alt.ProviderID
    payload.Alternatives = remaining
    continue  // <-- re-enters the claim phase
}
```

The `for {}` loop in `handleOnRamp` iterates through alternatives without recursion (avoiding unbounded stack growth):

```
Attempt 1: Claim(ProviderA) --> Call(ProviderA) --> FAIL
            Delete claim, update route
Attempt 2: Claim(ProviderB) --> Call(ProviderB) --> FAIL
            Delete claim, update route
Attempt 3: Claim(ProviderC) --> Call(ProviderC) --> SUCCESS
            Update, report to engine
```

---

## Key Insight

> CHECK-BEFORE-CALL is the last line of defense against double-execution. NATS deduplication prevents the relay from publishing twice. Consumer BackOff limits retries. But neither prevents a worker from calling an external system twice if the same message is delivered to two worker instances. The `INSERT ON CONFLICT DO NOTHING` claim mechanism is the only thing that guarantees at-most-once execution of the external call. Without it, at-least-once delivery becomes at-least-once execution -- a catastrophic failure mode for a payment system.

---

## CHECK-BEFORE-CALL in Other Workers

The CLAIM-CALL-UPDATE pattern originated in the ProviderWorker, but the same principle -- check whether the work has already been done before doing it -- is now applied across several newer workers. Each adapts the pattern to fit its domain.

### DepositWorker

The DepositWorker processes chain monitor events for crypto deposit sessions. Before acting on an incoming event (e.g., a confirmation or completion notification from the chain poller), it checks the deposit session's current status. If the session is already `CONFIRMED` or `COMPLETED`, the worker ACKs immediately and skips processing. This prevents a redelivered chain event from crediting a deposit twice.

The idempotency key for deposit events is `(session_id, event_type)`. This allows a single session to process distinct events (e.g., `detected` then `confirmed`) while rejecting duplicate deliveries of the same event. The check is a simple status read rather than an `INSERT ON CONFLICT` claim, because the session state machine itself is the guard -- once it advances past a given state, it cannot be moved backward.

### BankDepositWorker

The BankDepositWorker handles fiat credit notifications from banking partners. When a credit notification arrives, the worker checks whether the bank deposit session already has a matching credit recorded. If a credit with the same reference already exists, the notification is a duplicate and the worker ACKs without re-processing.

The deduplication key is `(session_id, credit_reference)`, where `credit_reference` is the bank's unique transaction identifier. This mirrors the CLAIM-CALL-UPDATE pattern conceptually: the "claim" is recording the credit reference, and a duplicate reference means another worker (or a redelivery) already handled it. This is critical because double-crediting a bank deposit would create a ledger imbalance.

### VirtualAccountProvisioner

The VirtualAccountProvisioner creates virtual bank accounts for tenants. Before provisioning a new account, it checks whether a virtual account already exists for the given `(tenant_id, chain)` combination. If one exists, provisioning is skipped entirely.

The database-level guard uses `INSERT ON CONFLICT DO NOTHING` semantics, identical to `ClaimProviderTransaction`. This means two concurrent provisioning requests for the same tenant and chain will result in exactly one virtual account being created. The losing request sees zero rows returned and exits cleanly. This pattern is especially important during tenant onboarding, where multiple deposit session events may trigger provisioning simultaneously.

### InboundWebhookWorker (Audit Log Extension)

The InboundWebhookWorker's status-based idempotency was covered earlier in this chapter. In addition to the status check, raw webhook payloads are now logged to the `provider_webhook_logs` table for audit purposes. This insert uses `ON CONFLICT DO NOTHING` deduplication on `(provider, external_id)`, so redelivered or duplicated provider webhooks produce exactly one audit log entry. The audit log is write-only and never consulted for control flow -- it exists purely for compliance and debugging.

---

## Common Mistakes

1. **Checking before calling without atomicity.** A SELECT-then-INSERT has a race condition: two workers can SELECT "not found" simultaneously, then both INSERT and both proceed to CALL. The claim must be atomic -- `INSERT ON CONFLICT DO NOTHING` or equivalent.

2. **Treating "pending" as re-claimable.** A "pending" status means the provider accepted the request but hasn't completed it yet. Re-claiming and re-calling would create a second pending transaction. The in-memory store correctly returns `nil, nil` for "pending" status.

3. **Forgetting to delete the claim before fallback.** If the worker tries to claim with ProviderB while ProviderA's claim still exists, the claim will fail (conflict on `transfer_id + tx_type`). The delete step is essential for fallback routing.

4. **Relying solely on NATS dedup for idempotency.** NATS dedup prevents duplicate publish from the relay, but does not prevent redelivery from the consumer. A message that was delivered and not acked will be redelivered even though it was only published once.

5. **ACKing malformed payloads without logging.** The workers correctly return `nil` (ACK) for unmarshal errors, but they log the error. An ACK without a log would silently drop the message, making debugging impossible.

---

## Exercises

1. **Race Condition Analysis:** Two ProviderWorker instances receive the same on-ramp intent simultaneously. Trace the execution through ClaimProviderTransaction for both workers. At what point does the race resolve? What determines which worker wins?

2. **Recovery Scenario:** A ProviderWorker claims a transaction, calls the provider API, the provider charges the customer, but the worker crashes before updating the provider_transactions table. When the message is redelivered, the new worker finds the claim in "claiming" status. What should happen? What does happen? Is this correct?

3. **Idempotency Key Design:** The LedgerWorker uses `payload.IdempotencyKey` for TigerBeetle dedup. What should this key contain to ensure that two ledger posts for the same transfer but different phases (e.g., on-ramp credit vs. off-ramp debit) are treated as distinct operations?

4. **Timeout Budget:** The ProviderWorker uses `resilience.NewTimeoutBudget(ctx, 30*time.Second)` and allocates 5s for claim, 20s for call, and 5s for reporting. What happens if the claim takes 4.9s, the call takes 19.9s, and reporting is attempted? Calculate the remaining budget.

---

## What's Next

In Chapter 5.5, we will examine all eleven workers in detail -- their streams, event handlers, and how they interact with the settlement engine through the `Handle*Result` callback pattern.
