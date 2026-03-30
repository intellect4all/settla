# Chapter 5.5: The Worker Fleet -- Eleven Workers and Beyond

**Reading time: 45 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Describe the role and stream assignment of each of the eleven core workers
2. Trace the event-driven saga from transfer creation through completion
3. Explain the SettlementEngine interface and how workers report results back to the engine
4. Understand error classification: which errors trigger NAK (retry) vs. ACK (skip)
5. Describe the DLQ monitor's role in observing and replaying dead-lettered messages
6. Identify the architectural difference between intent-driven, event-driven, and timer-driven workers
7. Explain how deposit, bank deposit, email, and rebalance workers extend the original seven

---

## Architecture Overview

```
                                    Outbox Relay
                                        |
    +-------+-------+-------+-------+---+---+-------+-------+-------+-------+-------+
    |       |       |       |       |       |       |       |       |       |       |
+---v---+ +v-----+ +v-----+ +v---+ +v-----+ +v-----+ +v------+ +v------+ +v------+ +v----+
|Trans- | |Prov- | |Ledger| |Trea| |Block-| |Web-  | |Inbound| |Deposit| |Bank   | |Email|
|fer    | |ider  | |Worker| |sury| |chain | |hook  | |Webhook| |Worker | |Deposit| |Wrkr |
|Worker | |Worker| |      | |Wrkr| |Worker| |Worker| |Worker | |       | |Worker | |     |
|(saga  | |(CB+  | |(post | |(res| |(send | |(HTTP | |(async | |(crypto| |(fiat  | |(noti|
| orch.)| |claim)| | rev.)| |rel)| | poll)| |deliv)| | prov.)| | deps) | | deps) | |fic.)|
+---+---+ +--+---+ +--+---+ +-+-+ +--+---+ +--+---+ +--+----+ +--+----+ +--+----+ +-+--+
    |        |        |       |       |        |        |         |         |        |
    v        v        v       v       v        v        v         v         v        v
SETTLA_  SETTLA_  SETTLA_  SETTLA_ SETTLA_  SETTLA_  SETTLA_   SETTLA_   SETTLA_  SETTLA_
TRANS-   PROV-    LEDGER   TREA-   BLOCK-   WEB-     PROVIDER_ CRYPTO_   BANK_    EMAILS
FERS     IDERS             SURY    CHAIN    HOOKS    WEBHOOKS  DEPOSITS  DEPOSITS

                     +---------------+
                     |  Position     |     (timer-driven, no NATS stream)
                     |  Rebalance    |
                     |  Worker       |
                     +-------+-------+
                             |
                        30s ticker
```

The original seven workers (TransferWorker through InboundWebhookWorker) handle the core transfer lifecycle. The four additional workers extend the platform to crypto deposits, bank deposits, transactional email, and automatic liquidity rebalancing. A DLQ monitor and supporting components round out the fleet.

---

## 1. TransferWorker -- The Saga Orchestrator

**Stream:** `SETTLA_TRANSFERS`
**Subject filter:** `settla.transfer.partition.{N}.>`
**Intent types consumed:** None (consumes events, not intents)
**Engine methods called:** `FundTransfer`, `InitiateOnRamp`, `HandleOnRampResult`, `HandleSettlementResult`, `HandleOffRampResult`, `CompleteTransfer`, `FailTransfer`

The TransferWorker is unique: it does not execute side effects. Instead, it routes domain events to the settlement engine, which advances the transfer state machine and writes the next outbox entries. It is the saga orchestrator.

```go
type SettlementEngine interface {
    FundTransfer(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID) error
    InitiateOnRamp(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID) error
    HandleOnRampResult(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID, result domain.IntentResult) error
    HandleSettlementResult(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID, result domain.IntentResult) error
    HandleOffRampResult(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID, result domain.IntentResult) error
    CompleteTransfer(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID) error
    FailTransfer(ctx context.Context, tenantID uuid.UUID,
        transferID uuid.UUID, reason, code string) error
}
```

The event routing switch:

```go
func (w *TransferWorker) handleEvent(ctx context.Context,
                                      event domain.Event) error {
    transferID, err := w.extractTransferID(event)
    if err != nil {
        return nil  // ACK -- can't process without ID
    }

    tenantID := event.TenantID

    switch event.Type {
    case domain.EventTransferCreated:
        return w.callEngine(ctx, "FundTransfer", transferID,
            func(ctx context.Context, id uuid.UUID) error {
                return w.engine.FundTransfer(ctx, tenantID, id)
            })

    case domain.EventTransferFunded:
        return w.callEngine(ctx, "InitiateOnRamp", transferID,
            func(ctx context.Context, id uuid.UUID) error {
                return w.engine.InitiateOnRamp(ctx, tenantID, id)
            })

    case domain.EventTreasuryReserved:
        return w.callEngine(ctx, "InitiateOnRamp", transferID,
            func(ctx context.Context, id uuid.UUID) error {
                return w.engine.InitiateOnRamp(ctx, tenantID, id)
            })

    case domain.EventTreasuryFailed:
        errMsg := w.extractErrorFromEvent(event)
        return w.callEngine(ctx, "FailTransfer", transferID,
            func(ctx context.Context, id uuid.UUID) error {
                return w.engine.FailTransfer(ctx, tenantID, id,
                    errMsg, "TREASURY_FAILED")
            })

    // Provider results
    case domain.EventProviderOnRampDone:
        return w.callEngineResult(ctx, "HandleOnRampResult",
            transferID, func(ctx context.Context, id uuid.UUID) error {
                return w.engine.HandleOnRampResult(ctx, tenantID, id,
                    domain.IntentResult{Success: true})
            })

    case domain.EventProviderOnRampFailed:
        errMsg := w.extractErrorFromEvent(event)
        return w.callEngineResult(ctx, "HandleOnRampResult",
            transferID, func(ctx context.Context, id uuid.UUID) error {
                return w.engine.HandleOnRampResult(ctx, tenantID, id,
                    domain.IntentResult{
                        Success:   false,
                        Error:     errMsg,
                        ErrorCode: "ONRAMP_FAILED",
                    })
            })

    // Blockchain results
    case domain.EventBlockchainConfirmed:
        txHash := w.extractTxHashFromEvent(event)
        return w.callEngineResult(ctx, "HandleSettlementResult",
            transferID, func(ctx context.Context, id uuid.UUID) error {
                return w.engine.HandleSettlementResult(ctx, tenantID, id,
                    domain.IntentResult{Success: true, TxHash: txHash})
            })

    // ... off-ramp results, legacy events, acknowledgments ...

    default:
        return nil  // ACK -- skip unhandled events
    }
}
```

**Error classification via callEngine:**

```go
func (w *TransferWorker) callEngine(ctx context.Context, step string,
    transferID uuid.UUID,
    fn func(context.Context, uuid.UUID) error) error {

    err := fn(ctx, transferID)
    if err == nil {
        return nil
    }

    var domErr *domain.DomainError
    if errors.As(err, &domErr) &&
        domErr.Code() == domain.CodeInvalidTransition {
        // Transfer already advanced past this step -- ACK
        return nil
    }
    if errors.Is(err, core.ErrOptimisticLock) {
        // Concurrent modification -- NAK for retry
    }
    return err  // NAK
}
```

The `CodeInvalidTransition` check is critical: if a transfer already moved past FUNDED to ON_RAMP_INITIATED, a redelivered `EventTransferFunded` would try to call `InitiateOnRamp` again. The engine rejects it with `CodeInvalidTransition`, and the worker correctly ACKs (skips) instead of retrying.

---

## 2. ProviderWorker -- On-Ramp and Off-Ramp Execution

**Stream:** `SETTLA_PROVIDERS`
**Subject filter:** `settla.provider.command.partition.{N}.>`
**Intent types consumed:** `provider.onramp.execute`, `provider.offramp.execute`
**Pattern:** CLAIM-CALL-UPDATE with circuit breakers and fallback routing

The ProviderWorker is the most complex worker. It:
- Claims the transaction slot atomically
- Executes the provider call through a circuit breaker
- Retries with exponential backoff (3 attempts, 200ms initial, 2x multiplier)
- Falls back to alternative providers on failure
- Reports results to the engine

```go
func NewProviderWorker(partition int, ...) *ProviderWorker {
    // Create per-provider circuit breakers
    onRampCBs := make(map[string]*resilience.CircuitBreaker)
    for id := range onRampProviders {
        onRampCBs[id] = resilience.NewCircuitBreaker(
            "provider-onramp-"+id,
            resilience.WithFailureThreshold(15),
            resilience.WithResetTimeout(10*time.Second),
            resilience.WithHalfOpenMax(2),
        )
    }
    // ... same for offRampCBs ...
}
```

**Fallback routing loop (iterative, not recursive):**

```go
func (w *ProviderWorker) handleOnRamp(ctx context.Context,
                                       event domain.Event) error {
    // ...
    for {
        // Phase 1: CLAIM
        claimID, err := w.transferStore.ClaimProviderTransaction(...)
        if claimID == nil { return nil }  // already claimed

        // Phase 2: CALL (with CB + retry)
        tx, execErr := w.executeOnRamp(...)

        if execErr != nil {
            // Try fallback
            if len(payload.Alternatives) > 0 {
                alt := payload.Alternatives[0]
                w.transferStore.DeleteProviderTransaction(...)
                w.transferStore.UpdateTransferRoute(...)
                payload.ProviderID = alt.ProviderID
                payload.Alternatives = payload.Alternatives[1:]
                continue  // <-- iterate, don't recurse
            }
            // No alternatives: report failure
            w.engine.HandleOnRampResult(ctx, ...,
                domain.IntentResult{Success: false, ...})
            return nil
        }

        // Phase 3: UPDATE + report
        w.transferStore.UpdateProviderTransaction(...)
        if tx.Status == "pending" { return nil }  // await webhook
        w.engine.HandleOnRampResult(ctx, ...,
            domain.IntentResult{Success: true, ...})
        return nil
    }
}
```

---

## 3. LedgerWorker -- Double-Entry Bookkeeping

**Stream:** `SETTLA_LEDGER`
**Subject filter:** `settla.ledger.partition.{N}.>`
**Intent types consumed:** `ledger.post`, `ledger.reverse`
**Pattern:** Delegate idempotency to TigerBeetle

The simplest worker -- it converts outbox payloads into journal entries:

```go
func (w *LedgerWorker) handlePost(ctx context.Context,
                                   event domain.Event) error {
    payload, err := unmarshalEventData[domain.LedgerPostPayload](event)
    if err != nil { return nil }  // ACK -- malformed

    entry := domain.JournalEntry{
        ID:             uuid.Must(uuid.NewV7()),
        TenantID:       &payload.TenantID,
        IdempotencyKey: payload.IdempotencyKey,
        PostedAt:       time.Now().UTC(),
        EffectiveDate:  time.Now().UTC(),
        Description:    payload.Description,
        ReferenceType:  payload.ReferenceType,
        ReferenceID:    &payload.TransferID,
        Lines:          make([]domain.EntryLine, len(payload.Lines)),
    }

    for i, line := range payload.Lines {
        entry.Lines[i] = domain.EntryLine{
            ID: uuid.Must(uuid.NewV7()),
            Posting: domain.Posting{
                AccountCode: line.AccountCode,
                EntryType:   domain.EntryType(line.EntryType),
                Amount:      line.Amount,
                Currency:    domain.Currency(line.Currency),
                Description: line.Description,
            },
        }
    }

    _, err = w.ledger.PostEntries(ctx, entry)
    if err != nil {
        return fmt.Errorf("posting entry for transfer %s: %w",
            payload.TransferID, err)  // NAK for retry
    }
    return nil
}
```

**Why no claim step?** TigerBeetle handles idempotency internally via `IdempotencyKey`. If the same key is posted twice, TigerBeetle returns success without creating a duplicate entry. This is simpler and more reliable than maintaining a separate claim table.

---

## 4. TreasuryWorker -- Reserve, Release, and Consume

**Stream:** `SETTLA_TREASURY`
**Subject filter:** `""` (no filter -- consumes all treasury events)
**Intent types consumed:** `treasury.reserve`, `treasury.release`, `treasury.consume`, `position.credit`, `position.debit`
**Pattern:** Reference-based idempotency (transfer ID)

The TreasuryWorker has grown beyond the original reserve/release pair. It now handles five intent types, covering the full treasury lifecycle including reservation consumption on transfer completion and direct position credits/debits for deposits and rebalancing:

```go
func (w *TreasuryWorker) handleEvent(ctx context.Context, event domain.Event) error {
    switch event.Type {
    case domain.IntentTreasuryReserve:
        return w.handleReserve(ctx, event)
    case domain.IntentTreasuryRelease:
        return w.handleRelease(ctx, event)
    case domain.IntentTreasuryConsume:
        return w.handleConsume(ctx, event)
    case domain.IntentPositionCredit:
        return w.handlePositionCredit(ctx, event)
    case domain.IntentPositionDebit:
        return w.handlePositionDebit(ctx, event)
    default:
        return nil
    }
}
```

The reserve and release handlers follow the pattern described earlier. The consume handler converts a reservation into a permanent balance change on transfer completion:

```go
func (w *TreasuryWorker) handleConsume(ctx context.Context,
                                        event domain.Event) error {
    payload, err := unmarshalEventData[domain.TreasuryConsumePayload](event)
    if err != nil { return nil }  // ACK — malformed payload

    err = w.treasury.ConsumeReservation(
        ctx,
        payload.TenantID,
        payload.Currency,
        payload.Location,
        payload.Amount,
        payload.TransferID,
    )
    if err != nil {
        return w.publishResult(ctx, payload.TransferID,
            payload.TenantID, domain.EventTreasuryFailed, err.Error())
    }

    return w.publishResult(ctx, payload.TransferID,
        payload.TenantID, domain.EventTreasuryConsumed, "")
}
```

The position credit/debit handlers serve deposit flows and rebalancing. They call `CreditBalance`/`DebitBalance` directly (no reservation step) and publish corresponding result events:

```go
func (w *TreasuryWorker) handlePositionCredit(ctx context.Context,
                                               event domain.Event) error {
    payload, err := unmarshalEventData[domain.PositionCreditPayload](event)
    if err != nil { return nil }

    err = w.treasury.CreditBalance(
        ctx,
        payload.TenantID,
        payload.Currency,
        payload.Location,
        payload.Amount,
        payload.Reference,
        payload.RefType,
    )
    if err != nil {
        return w.publishResult(ctx, payload.Reference,
            payload.TenantID, domain.EventTreasuryFailed, err.Error())
    }

    return w.publishResult(ctx, payload.Reference,
        payload.TenantID, domain.EventPositionCredited, "")
}
```

Note that the TreasuryWorker **publishes result events** back to NATS (which land on the SETTLA_TRANSFERS stream) rather than calling the engine directly. This is because treasury operations are decoupled from the transfer saga -- the TransferWorker picks up `EventTreasuryReserved` and calls `Engine.InitiateOnRamp`.

The release handler has an important subtlety for idempotency:

```go
func (w *TreasuryWorker) handleRelease(ctx context.Context,
                                        event domain.Event) error {
    payload, err := unmarshalEventData[domain.TreasuryReleasePayload](event)
    // ...

    // Build deterministic per-scenario dedup key
    releaseRef := payload.TransferID
    if payload.Reason != "" {
        releaseRef = uuid.NewSHA1(payload.TransferID,
            []byte(payload.Reason))
    }

    err = w.treasury.Release(ctx, payload.TenantID,
        payload.Currency, payload.Location, payload.Amount, releaseRef)
    // ...
}
```

The `uuid.NewSHA1` call creates a deterministic UUID from the transfer ID + reason string. This ensures that a release for "settlement_failure" and a release for "transfer_complete" get different dedup keys, even though they reference the same transfer.

---

## 5. BlockchainWorker -- On-Chain Transactions

**Stream:** `SETTLA_BLOCKCHAIN`
**Subject filter:** `settla.blockchain.partition.{N}.>`
**Intent types consumed:** `blockchain.send`
**Pattern:** CLAIM-CALL-UPDATE with pending transaction polling

The BlockchainWorker is unique in handling "pending" states. Blockchain transactions can take minutes to confirm, so the worker cannot wait synchronously:

```go
func (w *BlockchainWorker) handleSend(ctx context.Context,
                                       event domain.Event) error {
    // ... CLAIM phase ...
    // ... CALL phase (executeBlockchain with CB) ...

    chainStatus := strings.ToLower(chainTx.Status)
    if chainStatus == "pending" {
        w.trackPending(payload, chainTx.Hash)
        return nil  // ACK -- tracked in pendingTxs map
    }

    if chainStatus == "confirmed" {
        result := domain.IntentResult{Success: true, TxHash: chainTx.Hash}
        return w.engine.HandleSettlementResult(ctx, ...)
    }

    // Failed
    result := domain.IntentResult{Success: false, ...}
    return w.engine.HandleSettlementResult(ctx, ...)
}
```

**Pending transaction polling:** A background goroutine checks tracked transactions every 30 seconds:

```go
func (w *BlockchainWorker) startPendingPoller(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.pollPendingTransactions(ctx)
        }
    }
}
```

The poller checks up to 100 pending transactions per cycle, and escalates transactions stuck for more than 1 hour to manual review:

```go
func (w *BlockchainWorker) pollPendingTransactions(ctx context.Context) {
    checked := 0
    w.pendingTxs.Range(func(key, value any) bool {
        entry := value.(pendingEntry)

        if time.Since(entry.addedAt) > pendingTxEscalationTimeout {
            // 1 hour timeout -- escalate to manual review
            if _, deleted := w.pendingTxs.LoadAndDelete(key); deleted {
                w.escalatePendingTx(ctx, transferID, entry)
            }
            return true
        }

        if checked >= maxPendingChecksPerPoll {
            return false  // cap RPC checks per cycle
        }
        checked++

        w.checkPendingTx(ctx, entry.payload, existing)
        return true
    })
}
```

---

## 6. WebhookWorker -- Outbound Delivery

**Stream:** `SETTLA_WEBHOOKS`
**Subject filter:** `settla.webhook.partition.{N}.>`
**Intent types consumed:** `webhook.deliver`
**Pattern:** No dedup (duplicate delivery is acceptable)

```go
func (w *WebhookWorker) handleDeliver(ctx context.Context,
                                       event domain.Event) error {
    payload, err := unmarshalEventData[domain.WebhookDeliverPayload](event)
    if err != nil { return nil }  // ACK -- malformed

    tenant, err := w.tenantStore.GetTenant(ctx, payload.TenantID)
    if err != nil { return err }  // NAK -- tenant lookup failed

    if tenant.WebhookURL == "" {
        return nil  // ACK -- no webhook configured
    }

    // Build payload
    webhookBody := WebhookPayload{
        ID:        uuid.Must(uuid.NewV7()).String(),
        EventType: payload.EventType,
        TenantID:  payload.TenantID.String(),
        Data:      payload.Data,
        CreatedAt: time.Now().UTC(),
    }

    body, err := json.Marshal(webhookBody)
    if err != nil { return nil }  // ACK -- marshalling won't fix on retry

    // Sign with HMAC-SHA256
    signature := signWebhook(body, tenant.WebhookSecret)

    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Settla-Signature", signature)
    req.Header.Set("X-Settla-Event", payload.EventType)
    req.Header.Set("X-Settla-Delivery", webhookBody.ID)

    // Execute through circuit breaker
    var resp *http.Response
    cbErr := w.cb.Execute(ctx, func(ctx context.Context) error {
        var doErr error
        resp, doErr = w.httpClient.Do(req)
        return doErr
    })

    if cbErr != nil { return cbErr }  // NAK (includes ErrCircuitOpen)

    // Classify response
    if resp.StatusCode >= 200 && resp.StatusCode < 300 {
        return nil  // ACK -- success
    }
    if resp.StatusCode >= 500 || resp.StatusCode == 408 ||
        resp.StatusCode == 429 {
        return fmt.Errorf("retryable HTTP %d", resp.StatusCode)  // NAK
    }
    return nil  // ACK -- 4xx is permanent failure, don't retry
}
```

---

## 7. InboundWebhookWorker -- Async Provider Callbacks

**Stream:** `SETTLA_PROVIDER_WEBHOOKS`
**Subject filter:** `settla.provider.inbound.partition.{N}.>`
**Event types consumed:** `provider.inbound.onramp.webhook`, `provider.inbound.offramp.webhook`
**Pattern:** Status-based idempotency check

When a provider returns "pending" during on-ramp execution, the final result arrives later as a webhook. The inbound HTTP handler normalizes the raw webhook into a `ProviderWebhookPayload` and publishes it to NATS. The InboundWebhookWorker processes it:

```go
func (w *InboundWebhookWorker) handleOnRampWebhook(ctx context.Context,
                                                     event domain.Event) error {
    payload, err := unmarshalEventData[domain.ProviderWebhookPayload](event)
    if err != nil { return nil }

    // Lookup existing provider transaction
    existing, err := w.transferStore.GetProviderTransaction(
        ctx, payload.TenantID, payload.TransferID, "onramp")
    if err != nil { return err }  // NAK
    if existing == nil { return nil }  // ACK -- unknown transfer

    // Idempotency check
    if existing.Status == "completed" || existing.Status == "confirmed" {
        return nil  // ACK -- already processed
    }

    // Update provider transaction
    existing.Status = payload.Status
    if payload.TxHash != "" { existing.TxHash = payload.TxHash }
    w.transferStore.UpdateProviderTransaction(
        ctx, payload.TransferID, "onramp", existing)

    // Report to engine
    switch payload.Status {
    case "completed":
        result := domain.IntentResult{
            Success:     true,
            ProviderRef: payload.ProviderRef,
            TxHash:      payload.TxHash,
        }
        return w.engine.HandleOnRampResult(ctx,
            payload.TenantID, payload.TransferID, result)
    case "failed":
        result := domain.IntentResult{
            Success:   false,
            Error:     payload.Error,
            ErrorCode: payload.ErrorCode,
        }
        return w.engine.HandleOnRampResult(ctx,
            payload.TenantID, payload.TransferID, result)
    }
    return nil
}
```

---

## 8. DepositWorker -- Crypto Deposit Sessions

**Stream:** `SETTLA_CRYPTO_DEPOSITS`
**Subject filter:** `settla.deposit.partition.{N}.>`
**Event types consumed:** `deposit.tx.detected`, `deposit.tx.confirmed`, `deposit.credit`, `deposit.settle`
**Pattern:** Engine-routed event handling (similar to TransferWorker)

The DepositWorker drives the crypto deposit session lifecycle. When the chain monitor detects an incoming stablecoin transfer to a tenant's deposit address, it publishes a detection event. The DepositWorker routes these events to the deposit engine, which advances the session state machine: `PENDING` -> `AWAITING_PAYMENT` -> `CONFIRMING` -> `CONFIRMED` -> `COMPLETED`.

```go
type DepositEngine interface {
    HandleTransactionDetected(ctx context.Context, tenantID, sessionID uuid.UUID,
        tx domain.IncomingTransaction) error
    HandleTransactionConfirmed(ctx context.Context, tenantID, sessionID uuid.UUID,
        txHash string, confirmations int32) error
    HandleCreditResult(ctx context.Context, tenantID, sessionID uuid.UUID,
        result domain.IntentResult) error
    HandleSettlementResult(ctx context.Context, tenantID, sessionID uuid.UUID,
        result domain.IntentResult) error
}
```

The event routing switch follows the same ACK/NAK discipline as all other workers -- malformed payloads are ACKed (retrying will not fix them), engine errors are NAKed (transient):

```go
func (w *DepositWorker) handleEvent(ctx context.Context, event domain.Event) error {
    switch event.Type {
    case domain.EventDepositTxDetected:
        return w.handleTxDetected(ctx, event)
    case domain.EventDepositTxConfirmed:
        return w.handleTxConfirmed(ctx, event)
    case domain.IntentCreditDeposit:
        return w.handleCreditResult(ctx, event)
    case domain.IntentSettleDeposit:
        return w.handleSettlementResult(ctx, event)
    default:
        return nil // ACK
    }
}
```

The credit handler integrates with the treasury to update the tenant's position. It derives the treasury location from chain and token (e.g., `crypto:tron:usdt`) and credits the net amount (gross minus platform fees):

```go
func (w *DepositWorker) handleCreditResult(ctx context.Context,
                                            event domain.Event) error {
    payload, err := unmarshalEventData[domain.CreditDepositPayload](event)
    if err != nil { return nil }  // ACK — malformed payload

    // Derive treasury position location from chain + token.
    location := fmt.Sprintf("crypto:%s:%s",
        strings.ToLower(string(payload.Chain)),
        strings.ToLower(payload.Token))
    currency := domain.Currency(strings.ToUpper(payload.Token))

    // Credit the tenant's treasury position with the net amount.
    err = w.treasury.CreditBalance(
        ctx,
        payload.TenantID,
        currency,
        location,
        payload.NetAmount,
        payload.SessionID, // idempotency reference
        "deposit_session",
    )

    var result domain.IntentResult
    if err != nil {
        result = domain.IntentResult{
            Success: false,
            Error:   fmt.Sprintf("treasury credit failed: %v", err),
        }
    } else {
        result = domain.IntentResult{Success: true}
    }

    return w.engine.HandleCreditResult(ctx,
        payload.TenantID, payload.SessionID, result)
}
```

The deposit engine supports two post-confirmation strategies: **auto-convert** (immediately convert the stablecoin to fiat and credit the tenant's fiat position) and **stablecoin-hold** (credit the stablecoin position directly, tenant converts later). The strategy is configured per-tenant and determines which outbox entries the engine writes after confirmation.

---

## 9. BankDepositWorker -- Fiat Bank Deposits

**Stream:** `SETTLA_BANK_DEPOSITS`
**Subject filter:** `settla.bank_deposit.partition.{N}.>`
**Event types consumed:** `bank_deposit.payment.received`, `bank_deposit.credit`, `bank_deposit.settle`, `bank_deposit.recycle_account`, `bank_deposit.refund`
**Pattern:** Dual-consumer (partitioned events + non-partitioned inbound credits)

The BankDepositWorker handles fiat deposits through virtual bank accounts. It runs two NATS consumers concurrently: a partitioned consumer for session lifecycle events, and a non-partitioned consumer for raw inbound bank credit webhooks that need routing to the correct session.

```go
func (w *BankDepositWorker) Start(ctx context.Context) error {
    // Start inbound bank credit consumer (non-partitioned)
    go func() {
        if err := w.inboundSub.SubscribeStream(ctx,
            "settla.inbound.bank.>", w.handleInboundBankCredit); err != nil {
            w.logger.Error("settla-bank-deposit-worker: inbound consumer failed",
                "error", err)
        }
    }()

    // Start partitioned consumer
    filter := messaging.StreamPartitionFilter(
        messaging.SubjectPrefixBankDeposit, w.partition)
    return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}
```

The event routing covers the full session lifecycle plus account recycling and refunds:

```go
func (w *BankDepositWorker) handleEvent(ctx context.Context,
                                         event domain.Event) error {
    switch event.Type {
    case domain.EventBankDepositPaymentReceived:
        return w.handlePaymentReceived(ctx, event)
    case domain.IntentBankDepositCredit:
        return w.handleCreditResult(ctx, event)
    case domain.IntentBankDepositSettle:
        return w.handleSettlementResult(ctx, event)
    case domain.IntentRecycleVirtualAccount:
        return w.handleRecycleAccount(ctx, event)
    case domain.IntentBankDepositRefund:
        return w.handleRefund(ctx, event)
    default:
        return nil // ACK
    }
}
```

The inbound credit handler is particularly interesting -- it routes raw bank credit webhooks by looking up the virtual account index. For **permanent** accounts (long-lived accounts assigned to a tenant), it auto-creates a new session via the engine. For **temporary** accounts (one-time accounts assigned to a specific session), it looks up the existing pending session:

```go
func (w *BankDepositWorker) handleInboundBankCredit(ctx context.Context,
                                                     event domain.Event) error {
    // ... unmarshal payload with accountNumber, amount, currency, etc. ...

    // Look up account index
    index, err := w.inboundStore.GetVirtualAccountIndexByNumber(
        ctx, payload.AccountNumber)
    if err != nil { return nil }  // ACK — unknown account

    var session *domain.BankDepositSession

    if index.AccountType == domain.VirtualAccountTypePermanent {
        // PERMANENT account: auto-create session via engine
        session, err = w.engine.CreateSessionForPermanentAccount(
            ctx, index.TenantID, payload.AccountNumber,
            payload.PartnerID, credit)
    } else {
        // TEMPORARY account: find existing PENDING_PAYMENT session
        session, err = w.inboundStore.GetSessionByAccountNumber(
            ctx, payload.AccountNumber)
    }

    // Route credit to the resolved session
    return w.engine.HandleBankCreditReceived(
        ctx, session.TenantID, session.ID, credit)
}
```

The credit handler follows the same pattern as the DepositWorker -- it derives the treasury location from currency (e.g., `bank:ngn`) and credits the tenant's position with the net amount after deducting fees.

---

## 10. EmailWorker -- Transactional Notifications

**Stream:** `SETTLA_EMAILS`
**Subject filter:** `settla.email.partition.{N}.>`
**Intent types consumed:** `email.notify`
**Pattern:** Tenant-config-gated delivery

The EmailWorker sends transactional email notifications for deposit confirmations, transfer completions, failure alerts, and other lifecycle events. It follows a three-step gate before sending: check if email is enabled for the tenant, check if the specific event type warrants a notification, and check if notification recipients are configured.

```go
func (w *EmailWorker) handleNotify(ctx context.Context,
                                    event domain.Event) error {
    payload, err := unmarshalEventData[domain.EmailNotifyPayload](event)
    if err != nil { return nil }  // ACK — malformed payload

    // Load tenant for notification config
    tenant, err := w.tenantStore.GetTenant(ctx, payload.TenantID)
    if err != nil {
        return fmt.Errorf("settla-email-worker: loading tenant %s: %w",
            payload.TenantID, err)
    }

    // Gate 1: email notifications enabled?
    if !tenant.NotificationConfig.EmailEnabled {
        return nil // ACK
    }

    // Gate 2: this event type configured for notification?
    if !w.shouldNotify(tenant, payload.EventType) {
        return nil // ACK
    }

    // Gate 3: recipients configured?
    recipients := tenant.NotificationConfig.NotificationEmails
    if len(recipients) == 0 {
        return nil // ACK
    }

    // Build template data and send
    if err := w.sender.SendEmail(ctx, SendEmailRequest{
        To:        recipients,
        Subject:   payload.Subject,
        EventType: payload.EventType,
        Data:      templateData,
        TenantID:  payload.TenantID,
    }); err != nil {
        return fmt.Errorf("settla-email-worker: sending email: %w", err)
    }

    return nil // ACK
}
```

The `shouldNotify` method classifies events into three categories, each independently toggleable per tenant:

```go
func (w *EmailWorker) shouldNotify(tenant *domain.Tenant,
                                    eventType string) bool {
    cfg := tenant.NotificationConfig

    switch eventType {
    // Success events
    case domain.EventDepositSessionCredited, domain.EventDepositSessionSettled,
        domain.EventBankDepositSessionCredited, domain.EventTransferCompleted:
        return cfg.NotifyOnSuccess

    // Failure events
    case domain.EventDepositSessionFailed, domain.EventDepositSessionExpired,
        domain.EventTransferFailed, domain.EventBankDepositSessionFailed:
        return cfg.NotifyOnFailure

    // Detection events
    case domain.EventDepositTxDetected, domain.EventDepositTxConfirmed,
        domain.EventBankDepositPaymentReceived, domain.EventBankDepositUnderpaid:
        return cfg.NotifyOnDetection

    default:
        return false
    }
}
```

The `EmailSender` interface supports multiple backends. In production, this is typically Resend or AWS SES. For development, the `LogEmailSender` logs email details without sending:

```go
type EmailSender interface {
    SendEmail(ctx context.Context, req SendEmailRequest) error
}

// LogEmailSender is a development backend that logs instead of sending.
type LogEmailSender struct {
    logger *slog.Logger
}

func (s *LogEmailSender) SendEmail(_ context.Context, req SendEmailRequest) error {
    s.logger.Info("settla-email: would send email",
        "to", req.To,
        "subject", req.Subject,
        "event_type", req.EventType,
        "tenant_id", req.TenantID,
    )
    return nil
}
```

---

## 11. PositionRebalanceWorker -- Automatic Liquidity Management

**Not driven by NATS stream -- runs on a 30-second timer.**

The PositionRebalanceWorker is architecturally different from the other ten workers. It does not consume from a NATS stream. Instead, it runs a periodic scan (every 30 seconds) of all tenant positions, looking for positions that have fallen below their `MinBalance` threshold. When it finds one, it attempts an internal rebalance by finding a surplus position in the same tenant and triggering an internal transfer. If no internal source is available, it publishes a low-balance webhook alert to the tenant.

```go
type PositionRebalanceWorker struct {
    treasury     RebalanceTreasuryReader
    tenantLister RebalanceTenantLister
    engine       RebalancePositionEngine
    publisher    RebalanceAlertPublisher
    interval     time.Duration
    cooldown     time.Duration
    logger       *slog.Logger

    // recentRebalances tracks position IDs that were recently rebalanced
    // to prevent over-triggering. Keyed by position ID, value is last rebalance time.
    recentMu          sync.Mutex
    recentRebalances  map[uuid.UUID]time.Time
}
```

The main loop is a simple ticker:

```go
func (w *PositionRebalanceWorker) Run(ctx context.Context) error {
    ticker := time.NewTicker(w.interval)  // 30 seconds
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            w.runCycle(ctx)
        }
    }
}
```

Each cycle iterates all active tenants, loads their positions, and checks each position against its minimum balance threshold:

```go
func (w *PositionRebalanceWorker) checkTenant(ctx context.Context,
                                               tenantID uuid.UUID) {
    positions, err := w.treasury.GetPositions(ctx, tenantID)
    if err != nil { return }

    for i := range positions {
        pos := &positions[i]

        // Skip positions without a min_balance threshold.
        if pos.MinBalance.IsZero() { continue }

        // Skip positions that are above the minimum.
        if pos.IsAboveMinimum() { continue }

        // Skip if recently rebalanced (cooldown).
        if w.isOnCooldown(pos.ID) { continue }

        deficit := pos.TargetBalance.Sub(pos.Balance)
        if !deficit.IsPositive() {
            deficit = pos.MinBalance.Sub(pos.Balance)
        }
        if !deficit.IsPositive() { continue }

        // Look for a surplus position in the same tenant.
        source := w.findSurplusPosition(positions, pos, deficit)
        if source != nil {
            w.triggerRebalance(ctx, tenantID, source, pos, deficit)
        } else {
            w.publishAlert(ctx, tenantID, pos)
        }
    }
}
```

**Surplus detection:** The worker looks for positions in the same tenant with the same currency where the available balance exceeds the position's own target balance. This ensures the source position is not drained below its own threshold:

```go
func (w *PositionRebalanceWorker) findSurplusPosition(
    positions []domain.Position,
    deficit *domain.Position,
    amount decimal.Decimal,
) *domain.Position {
    for i := range positions {
        candidate := &positions[i]
        if candidate.ID == deficit.ID { continue }
        if w.isOnCooldown(candidate.ID) { continue }
        if candidate.Currency != deficit.Currency { continue }

        threshold := candidate.TargetBalance
        if threshold.IsZero() { threshold = candidate.MinBalance }

        surplus := candidate.Available().Sub(threshold)
        if surplus.GreaterThanOrEqual(amount) {
            return candidate
        }
    }
    return nil
}
```

**Rebalancing execution:** When a surplus source is found, the worker triggers a withdrawal from the source and a top-up to the destination via the position engine. Both positions are then placed on a 5-minute cooldown to prevent over-triggering:

```go
func (w *PositionRebalanceWorker) triggerRebalance(
    ctx context.Context,
    tenantID uuid.UUID,
    source, dest *domain.Position,
    amount decimal.Decimal,
) {
    // Debit from source position.
    _, err := w.engine.RequestWithdrawal(ctx, tenantID,
        domain.WithdrawalRequest{
            Currency:    source.Currency,
            Location:    source.Location,
            Amount:      amount,
            Method:      "internal",
            Destination: string(dest.Currency) + ":" + dest.Location,
        })
    if err != nil { return }

    // Credit to destination position.
    _, err = w.engine.RequestTopUp(ctx, tenantID,
        domain.TopUpRequest{
            Currency: dest.Currency,
            Location: dest.Location,
            Amount:   amount,
            Method:   "internal",
        })

    // Mark both positions as recently rebalanced.
    w.setCooldown(source.ID)
    w.setCooldown(dest.ID)
}
```

**Alert publishing:** If no internal source is available, the worker publishes a `liquidity.alert` event containing the deficit details. This event is delivered to the tenant via the WebhookWorker, so the tenant can take manual action (e.g., top up from an external source):

```go
func (w *PositionRebalanceWorker) publishAlert(ctx context.Context,
                                                tenantID uuid.UUID,
                                                pos *domain.Position) {
    event := domain.Event{
        ID:       uuid.Must(uuid.NewV7()),
        TenantID: tenantID,
        Type:     domain.EventLiquidityAlert,
        Data: map[string]any{
            "position_id": pos.ID,
            "currency":    pos.Currency,
            "location":    pos.Location,
            "balance":     pos.Balance.String(),
            "available":   pos.Available().String(),
            "min_balance": pos.MinBalance.String(),
            "deficit":     pos.MinBalance.Sub(pos.Balance).String(),
        },
    }
    w.publisher.Publish(ctx, event)
    w.setCooldown(pos.ID)  // Cooldown to avoid alert spam
}
```

The cooldown map is cleaned up each cycle. Entries older than 2x the cooldown period are deleted to prevent unbounded memory growth:

```go
func (w *PositionRebalanceWorker) cleanupCooldowns() {
    w.recentMu.Lock()
    defer w.recentMu.Unlock()
    for id, t := range w.recentRebalances {
        if time.Since(t) > w.cooldown*2 {
            delete(w.recentRebalances, id)
        }
    }
}
```

---

## The DLQ Monitor -- Observing Failures

The DLQ monitor is not a worker in the traditional sense -- it does not execute side effects. It consumes from `SETTLA_DLQ`, logs messages at ERROR level, records Prometheus metrics, and stores recent entries in a bounded ring buffer:

```go
type DLQMonitor struct {
    client  *messaging.Client
    logger  *slog.Logger
    metrics *DLQMetrics
    entries []DLQEntry       // ring buffer, max 10,000
    head    int
    count   int
    // aggregate counters
    totalReceived  int64
    bySourceStream map[string]int64
    byEventType    map[string]int64
}
```

The DLQ monitor also supports **replaying** dead-lettered messages back to their original stream:

```go
func (m *DLQMonitor) Replay(ctx context.Context, entryID string) error {
    entry := m.GetEntry(entryID)
    if entry == nil { return fmt.Errorf("entry %s not found", entryID) }

    originalSubject, err := m.resolveReplaySubject(entry)
    if err != nil { return err }

    // New dedup ID to bypass NATS duplicate detection
    replayMsgID := fmt.Sprintf("replay-%s-%d",
        entryID, time.Now().UnixNano())
    return m.client.PublishToStream(ctx, originalSubject,
        replayMsgID, entry.RawData)
}
```

---

## Supporting Components

Beyond the eleven workers and the DLQ monitor, two supporting components run alongside the worker fleet:

### VirtualAccountProvisioner

The VirtualAccountProvisioner runs on a 60-second timer and ensures the virtual bank account pool stays above a low watermark (default: 10 available accounts per tenant per currency). It iterates all active tenants in batches, checks available account counts, and provisions new accounts through the banking partner registry when the pool is depleted:

```go
func (p *VirtualAccountProvisioner) poll(ctx context.Context) {
    err := p.tenantForEach(ctx, 500, func(ids []uuid.UUID) error {
        for _, tenantID := range ids {
            availableByCurrency, err := p.store.CountAvailableVirtualAccountsByCurrency(
                ctx, tenantID)
            if err != nil { continue }

            for currency, avail := range availableByCurrency {
                if avail >= int64(p.lowWatermark) { continue }

                needed := int64(p.lowWatermark) - avail
                // Provision via banking partner...
            }
        }
        return nil
    })
}
```

### PositionEventWriter

The PositionEventWriter is a background goroutine inside the Treasury Manager (not a standalone worker). It batch-writes position events to the `position_events` table every 10ms, with a maximum batch size of 1,000 events. This provides an audit trail for all position mutations (reserves, releases, credits, debits) without adding per-operation DB latency:

```go
const eventWriteInterval = 10 * time.Millisecond
const eventWriteBatchSize = 1000

func (m *Manager) eventWriteLoop() {
    defer close(m.eventWriterDone)
    ticker := time.NewTicker(eventWriteInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            m.drainAndWriteEvents(eventStore, hasEventStore)
        case <-m.stopCh:
            m.drainAndWriteEvents(eventStore, hasEventStore)  // final drain
            return
        }
    }
}
```

Failed batch writes are re-queued for retry on the next cycle. If the channel is full, older events are silently dropped -- the event log is best-effort audit, not a correctness requirement (TigerBeetle is the source of truth for balances).

---

## The Complete Transfer Saga Flow

```
API                        Engine + Outbox         Workers
 |                              |                      |
 | POST /v1/transfers           |                      |
 |----------------------------->|                      |
 |                              |                      |
 |  Engine.CreateTransfer()     |                      |
 |  writes: status=CREATED      |                      |
 |  outbox: transfer.created    |                      |
 |                              |                      |
 |                          Relay publishes             |
 |                              |                      |
 |                              +--> TransferWorker     |
 |                              |    FundTransfer()     |
 |                              |    writes: FUNDED     |
 |                              |    outbox: treasury.reserve
 |                              |                      |
 |                              +--> TreasuryWorker     |
 |                              |    Reserve()          |
 |                              |    publishes:         |
 |                              |      treasury.reserved|
 |                              |                      |
 |                              +--> TransferWorker     |
 |                              |    InitiateOnRamp()   |
 |                              |    writes: ON_RAMP_INIT
 |                              |    outbox:            |
 |                              |      provider.onramp  |
 |                              |      ledger.post      |
 |                              |                      |
 |                              +--> ProviderWorker     |
 |                              |    Execute(onramp)    |
 |                              |    HandleOnRampResult |
 |                              |                      |
 |                              +--> LedgerWorker       |
 |                              |    PostEntries()      |
 |                              |                      |
 |                  ... continues through settlement,   |
 |                      blockchain, off-ramp, webhook...|
 |                              |                      |
 |                              +--> TreasuryWorker     |
 |                              |    ConsumeReservation()|
 |                              |                      |
 |                              +--> WebhookWorker      |
 |                              |    HTTP POST to tenant|
 |                              |                      |
 |                              +--> EmailWorker        |
 |                              |    Send notification  |
 |                              |                      |
 |  transfer.completed          |                      |
```

---

## Key Insight

> Workers never talk to each other directly. Every interaction goes through the engine (via outbox entries) or through NATS (via result events). This is the modular monolith in action: workers are independent components that can be extracted to separate services by swapping the engine calls to gRPC calls. The TransferWorker is the only component that knows the full saga flow -- all other workers are stateless functions that process a single intent and report the result. The four newer workers (Deposit, BankDeposit, Email, PositionRebalance) follow the same pattern, each owning a single bounded context and communicating exclusively through the outbox and NATS.

---

## Common Mistakes

1. **Calling the engine directly from a non-TransferWorker.** ProviderWorker calls `engine.HandleOnRampResult` directly because it needs the engine to compute the next state. But TreasuryWorker publishes result events instead. The inconsistency exists because provider workers need the engine to decide the next step (with fallback logic), while treasury results are always the same: "reserved" or "failed."

2. **Treating all errors as NAK-worthy.** Malformed payloads should be ACKed (retrying an unmarshal error is futile). Database errors should generally be NAKed (transient). Circuit breaker open should be NAKed (will recover). The workers carefully classify each error.

3. **Not handling optimistic lock errors.** When two workers race to update the same transfer, one gets `ErrOptimisticLock`. The workers return this error to trigger a NAK, allowing NATS backoff to give the winning worker time to complete.

4. **Forgetting the pending state.** Both ProviderWorker and BlockchainWorker must handle "pending" responses. A provider that returns "pending" means the transaction is in progress -- the worker must ACK the NATS message and wait for an async webhook. Retrying would create a second pending transaction.

5. **Confusing stream-driven and timer-driven workers.** The PositionRebalanceWorker does not consume from NATS -- it runs on a 30-second timer. Treating it like a stream consumer (e.g., trying to partition it or configure consumer options) is a category error.

6. **Ignoring tenant notification config in EmailWorker.** The EmailWorker has three gates before sending: email enabled, event type configured, recipients present. Skipping any gate risks sending unwanted notifications or failing on nil recipients.

---

## Exercises

1. **Saga Trace:** Trace a complete transfer lifecycle from `transfer.created` to `transfer.completed`, listing every outbox entry written, every NATS message published, and every worker invocation. How many NATS messages are involved in the happy path? Now trace a crypto deposit session from creation through completion -- how does the DepositWorker's saga compare?

2. **Error Propagation:** The ProviderWorker catches `ErrCircuitOpen` and returns it (NAK). But for `ONRAMP_EXECUTION_FAILED`, it reports to the engine and returns nil (ACK). Explain why these two errors are treated differently.

3. **DLQ Replay Safety:** When replaying a dead-lettered message, the DLQ monitor uses a new message ID (`replay-{entryID}-{timestamp}`). Why can't it reuse the original message ID? What would happen if it did?

4. **Worker Scaling:** If you need to handle 10,000 TPS sustained, how would you scale each of the eleven worker types? Consider: partition count changes, pool size, NATS cluster sizing, database connection pool limits, and which workers (like PositionRebalanceWorker) cannot be partitioned.

5. **Dual-Consumer Pattern:** The BankDepositWorker runs two NATS consumers -- a partitioned one and a non-partitioned one. Why can't the inbound bank credit consumer be partitioned? What ordering guarantees does this sacrifice?

6. **Rebalance Safety:** The PositionRebalanceWorker debits a source position and then credits a destination position in two separate calls. What happens if the process crashes between the debit and credit? How does the outbox pattern provide eventual consistency here?

---

## What's Next

In Chapter 5.6, we will examine the circuit breaker pattern in detail -- how the three states (Closed, Open, Half-Open) protect workers from cascading failures, and how per-provider circuit breakers enable fallback routing.
