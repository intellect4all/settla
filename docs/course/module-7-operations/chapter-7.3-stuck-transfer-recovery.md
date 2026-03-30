# Chapter 7.3: Stuck Transfer Detection and Recovery

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why transfers get stuck at scale and quantify the expected stuck rate
2. Describe the three-tier threshold system (warn, recover, escalate) per transfer status
3. Trace the detection-recovery-escalation lifecycle from code
4. Explain how idempotent recovery actions prevent double-processing
5. Design per-status recovery strategies based on external provider queries

---

## Why Transfers Get Stuck

At 50M transactions/day, even a minuscule failure rate creates a significant volume of stuck transfers:

```
50,000,000 transfers/day * 0.01% stuck rate = 5,000 stuck transfers/day
50,000,000 transfers/day * 0.10% stuck rate = 50,000 stuck transfers/day
```

A 0.01% stuck rate is optimistic. Transfers get stuck for several reasons:

| Cause | Frequency | Status When Stuck |
|-------|-----------|-------------------|
| Provider webhook never arrives | Common | ON_RAMPING, OFF_RAMPING |
| Outbox relay crash after DB write | Rare | FUNDED |
| NATS consumer rebalancing | Rare | Any non-terminal |
| Process killed between state write and outbox publish | Very rare | Any non-terminal |
| Blockchain network congestion delays tx > timeout | Seasonal | SETTLING |
| Provider silently drops callback on error | Common | ON_RAMPING, OFF_RAMPING |

The recovery system exists because **at-least-once delivery is not the same as exactly-once completion**. NATS JetStream guarantees at-least-once delivery, but if the consumer process crashes after receiving the message and before processing it, the transfer is left in a non-terminal state with no active handler.

---

## The Three-Tier Threshold System

**File:** `core/recovery/detector.go`

Each non-terminal transfer status has three time thresholds:

```go
type Thresholds struct {
    Warn     time.Duration
    Recover  time.Duration
    Escalate time.Duration
}

var DefaultThresholds = map[domain.TransferStatus]Thresholds{
    domain.TransferStatusFunded:     {Warn: 5m,  Recover: 10m, Escalate: 30m},
    domain.TransferStatusOnRamping:  {Warn: 10m, Recover: 15m, Escalate: 60m},
    domain.TransferStatusSettling:   {Warn: 5m,  Recover: 10m, Escalate: 30m},
    domain.TransferStatusOffRamping: {Warn: 10m, Recover: 15m, Escalate: 60m},
    domain.TransferStatusRefunding:  {Warn: 10m, Recover: 15m, Escalate: 60m},
}
```

The thresholds form a tiered response:

```
  TIME SINCE LAST UPDATE
  |
  |  0 ---------> Warn ---------> Recover ---------> Escalate
  |               (log)          (auto-fix)           (human)
  |
  |  FUNDED:     5 min            10 min              30 min
  |  ON_RAMPING: 10 min           15 min              60 min
  |  SETTLING:   5 min            10 min              30 min
  |  OFF_RAMPING:10 min           15 min              60 min
  |  REFUNDING:  10 min           15 min              60 min


         LIFECYCLE OF A STUCK TRANSFER

  Time: 0        5m        10m        15m        30m       60m
        |---------|---------|----------|----------|---------|
  FUNDED:
        normal    WARN      RECOVER    ...        ESCALATE
                  (log)     (re-inject            (manual
                             on-ramp               review)
                             intent)

  ON_RAMPING:
        normal    ...       WARN       RECOVER    ...       ESCALATE
                            (log)      (query     ...       (manual
                                        provider,            review)
                                        reconcile)
```

Why are FUNDED and SETTLING thresholds shorter? Because these stages involve only internal systems (outbox relay and blockchain, respectively). Internal failures should resolve faster than external provider timeouts. ON_RAMPING and OFF_RAMPING involve external bank APIs that legitimately take longer.

---

## The Detector

**File:** `core/recovery/detector.go`

### Architecture

```go
type Detector struct {
    store              TransferQueryStore     // queries stuck transfers
    reviewStore        ReviewStore            // creates manual review records
    engine             RecoveryEngine         // re-injects outbox intents
    providers          ProviderStatusChecker  // queries external providers
    logger             *slog.Logger
    metrics            *DetectorMetrics
    interval           time.Duration          // default 60 seconds
    thresholds         map[domain.TransferStatus]Thresholds
    recoveryInProgress sync.Map               // prevents duplicate recovery across overlapping cycles
}
```

The detector runs as a background goroutine, ticking every 60 seconds:

```go
func (d *Detector) Run(ctx context.Context) error {
    ticker := time.NewTicker(d.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            // Graceful shutdown: run one final drain cycle
            drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer cancel()
            d.runCycle(drainCtx)
            return ctx.Err()
        case <-ticker.C:
            func() {
                defer func() {
                    if r := recover(); r != nil {
                        d.logger.Error("settla-recovery: panic in detection cycle, will retry next tick",
                            "panic", fmt.Sprintf("%v", r),
                        )
                    }
                }()
                if err := d.runCycle(ctx); err != nil {
                    d.logger.Error("settla-recovery: cycle failed", "error", err)
                }
            }()
        }
    }
}
```

Note the graceful shutdown pattern: when the context is cancelled, the detector runs one final cycle with a 30-second timeout. This ensures any transfers that became stuck during the shutdown window are still processed.

### The Recovery Cycle

Each cycle iterates over all configured statuses, queries for stuck transfers past the recovery threshold, and processes each one:

```go
func (d *Detector) runCycle(ctx context.Context) error {
    now := time.Now().UTC()
    var totalRecovered, totalEscalated, totalSkipped int

    for status, thresholds := range d.thresholds {
        recoverCutoff := now.Add(-thresholds.Recover)

        transfers, err := d.store.ListStuckTransfers(ctx, status, recoverCutoff)
        // ...

        if d.metrics != nil {
            d.metrics.StuckTransfersFound.WithLabelValues(string(status)).
                Set(float64(len(transfers)))
        }

        // Per-tenant fairness: limit recovery attempts per tenant per cycle
        // to prevent one tenant's backlog from starving others.
        const maxPerTenantPerCycle = 50
        tenantCounts := make(map[uuid.UUID]int)

        for _, transfer := range transfers {
            if tenantCounts[transfer.TenantID] >= maxPerTenantPerCycle {
                totalSkipped++
                continue
            }
            tenantCounts[transfer.TenantID]++
            stuckDuration := now.Sub(transfer.UpdatedAt)

            // Try recovery first, escalate only if recovery fails or is skipped.
            recovered, err := d.recoverTransfer(ctx, transfer)
            if recovered {
                totalRecovered++
            } else {
                // Recovery was skipped or not applicable -- escalate if past threshold
                if stuckDuration >= thresholds.Escalate {
                    d.escalate(ctx, transfer, transfer.UpdatedAt)
                    totalEscalated++
                }
                totalSkipped++
            }
        }
    }

    return nil
}
```

Two important patterns in the recovery cycle:

1. **Per-tenant fairness:** The `maxPerTenantPerCycle = 50` cap prevents a single tenant with a massive backlog (e.g., due to a provider outage affecting only that tenant) from monopolizing the detector's cycle time and starving other tenants' stuck transfers.

2. **Recovery-first escalation:** Unlike an approach where escalation and recovery run in parallel, the detector tries recovery first and only escalates if recovery was skipped or not applicable. This avoids creating unnecessary manual review records when automated recovery can resolve the issue.

---

## Per-Status Recovery Strategies

The `recoverTransfer` method dispatches to a status-specific handler. It uses a `sync.Map` to prevent duplicate recovery when detector cycles overlap (e.g., a slow cycle has not finished when the next tick fires):

```go
func (d *Detector) recoverTransfer(ctx context.Context, transfer *domain.Transfer) (bool, error) {
    // Prevent duplicate recovery across overlapping cycles
    if _, alreadyRunning := d.recoveryInProgress.LoadOrStore(transfer.ID, true); alreadyRunning {
        return false, nil
    }
    defer d.recoveryInProgress.Delete(transfer.ID)

    switch transfer.Status {
    case domain.TransferStatusFunded:
        return d.recoverFunded(ctx, transfer)
    case domain.TransferStatusOnRamping:
        return d.recoverOnRamping(ctx, transfer)
    case domain.TransferStatusSettling:
        return d.recoverSettling(ctx, transfer)
    case domain.TransferStatusOffRamping:
        return d.recoverOffRamping(ctx, transfer)
    case domain.TransferStatusRefunding:
        return d.recoverRefunding(ctx, transfer)
    default:
        return false, nil
    }
}
```

### Recovering FUNDED Transfers

**Root cause:** The outbox relay crashed after the engine wrote the FUNDED state change but before it published the outbox entry to NATS.

**Recovery action:** Re-inject the on-ramp intent.

```go
func (d *Detector) recoverFunded(ctx context.Context, transfer *domain.Transfer) (bool, error) {
    err := d.engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID)
    if err == nil {
        return true, nil
    }

    // Optimistic lock means another goroutine already advanced the transfer.
    if errors.Is(err, core.ErrOptimisticLock) {
        return true, nil // Already recovered
    }

    // Invalid transition means the transfer is no longer in FUNDED state.
    var domErr *domain.DomainError
    if errors.As(err, &domErr) && domErr.Code() == domain.CodeInvalidTransition {
        return true, nil // Already past FUNDED
    }

    if strings.Contains(err.Error(), "concurrent modification") {
        return true, nil // Concurrent recovery
    }

    return false, fmt.Errorf("settla-recovery: initiating on-ramp for stuck FUNDED transfer %s: %w",
        transfer.ID, err)
}
```

> **Key Insight:** The three error-handling clauses (`ErrOptimisticLock`, `CodeInvalidTransition`, "concurrent modification") are the idempotency guard. Multiple detector instances (running on different server replicas) may try to recover the same transfer simultaneously. The engine's optimistic locking ensures only one succeeds, and the others get one of these errors, which the detector correctly treats as "already recovered."

### Recovering ON_RAMPING Transfers

**Root cause:** The on-ramp provider processed the request but the webhook callback never arrived (network glitch, provider bug, etc.).

**Recovery action:** Query the provider directly for the current transaction status.

```go
func (d *Detector) recoverOnRamping(ctx context.Context, transfer *domain.Transfer) (bool, error) {
    status, err := d.providers.CheckOnRampStatus(ctx, transfer.OnRampProviderID, transfer.ID)
    if err != nil {
        return false, err
    }

    switch status.Status {
    case "completed":
        // Provider says on-ramp completed -- feed the result to the engine
        err := d.engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID,
            domain.IntentResult{
                Success:     true,
                ProviderRef: status.Reference,
            })
        return err == nil, err

    case "failed":
        // Provider says on-ramp failed -- feed the failure to the engine
        err := d.engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID,
            domain.IntentResult{
                Success:   false,
                Error:     status.Error,
                ErrorCode: "provider_onramp_failed",
            })
        return err == nil, err

    case "pending", "unknown":
        // Still in progress at the provider -- skip, try again next cycle
        return false, nil
    }
    return false, nil
}
```

This is the detector's most common recovery path. Provider webhooks are unreliable: firewalls drop them, DNS resolution fails, the provider has a bug in their callback system. By polling the provider directly, the detector resolves the vast majority of stuck ON_RAMPING transfers.

### Recovering SETTLING Transfers

**Root cause:** The blockchain transaction was sent but the confirmation event was not processed (e.g., the blockchain worker crashed before acknowledging the NATS message).

**Recovery action:** Check the blockchain directly for transaction confirmation.

```go
func (d *Detector) recoverSettling(ctx context.Context, transfer *domain.Transfer) (bool, error) {
    // Find the settlement tx hash from the transfer's blockchain transactions
    var txHash string
    for _, tx := range transfer.BlockchainTxs {
        if tx.Type == "settlement" {
            txHash = tx.TxHash
            break
        }
    }

    if txHash == "" {
        // No tx hash means the blockchain send hasn't happened yet
        return false, nil
    }

    status, err := d.providers.CheckBlockchainStatus(ctx, transfer.Chain, txHash)
    if err != nil {
        return false, err
    }

    if status.Confirmed {
        err := d.engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID,
            domain.IntentResult{
                Success: true,
                TxHash:  status.TxHash,
            })
        return err == nil, err
    }

    if status.Error != "" {
        err := d.engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID,
            domain.IntentResult{
                Success:   false,
                Error:     status.Error,
                ErrorCode: "blockchain_settlement_failed",
            })
        return err == nil, err
    }

    // Still pending on-chain
    return false, nil
}
```

### Recovering OFF_RAMPING Transfers

Follows the same pattern as ON_RAMPING recovery, but queries the off-ramp provider:

```go
func (d *Detector) recoverOffRamping(ctx context.Context, transfer *domain.Transfer) (bool, error) {
    status, err := d.providers.CheckOffRampStatus(ctx, transfer.OffRampProviderID, transfer.ID)
    // ... same switch pattern as recoverOnRamping
}
```

### Recovering REFUNDING Transfers

**Root cause:** The refund was initiated but the treasury release or ledger reversal worker crashed before completing.

**Recovery action:** Re-inject the refund completion signal.

```go
func (d *Detector) recoverRefunding(ctx context.Context, transfer *domain.Transfer) (bool, error) {
    err := d.engine.HandleRefundResult(ctx, transfer.TenantID, transfer.ID,
        domain.IntentResult{Success: true})

    if err == nil {
        return true, nil
    }

    // Same idempotency guards as recoverFunded
    if errors.Is(err, core.ErrOptimisticLock) {
        return true, nil
    }
    // ...
}
```

---

## Escalation

**File:** `core/recovery/escalation.go`

When a transfer exceeds the escalation threshold, the detector creates a manual review record:

```go
func (d *Detector) escalate(ctx context.Context, transfer *domain.Transfer, stuckSince time.Time) error {
    // Idempotent: check if already escalated
    hasReview, err := d.reviewStore.HasActiveReview(ctx, transfer.ID)
    if err != nil {
        return err
    }
    if hasReview {
        return nil // already escalated
    }

    err = d.reviewStore.CreateManualReview(ctx, transfer.ID, transfer.TenantID,
        string(transfer.Status), stuckSince)
    if err != nil {
        return err
    }

    if d.metrics != nil {
        d.metrics.EscalationsCreated.Inc()
    }

    return nil
}
```

Escalation is idempotent: calling `escalate` multiple times for the same transfer only creates one review record. This is critical because the detector runs every 60 seconds, and a transfer past the escalation threshold will be processed in every cycle until it is resolved.

---

## Concurrency Safety

The detector is safe to run on multiple server replicas simultaneously. Three mechanisms prevent double-processing:

```
  CONCURRENCY SAFETY MODEL

  Replica A (detector)          Replica B (detector)
        |                              |
        v                              v
  ListStuckTransfers()          ListStuckTransfers()
        |                              |
   [same transfers returned]    [same transfers returned]
        |                              |
        v                              v
  engine.InitiateOnRamp()      engine.InitiateOnRamp()
        |                              |
        v                              v
  BEGIN TX                      BEGIN TX
  UPDATE transfers              UPDATE transfers
  SET status='ON_RAMPING'       SET status='ON_RAMPING'
  WHERE id=? AND version=3      WHERE id=? AND version=3
  --> 1 row affected            --> 0 rows affected
  COMMIT                        --> ErrOptimisticLock
        |                              |
        v                              v
  SUCCESS (recovered)           "already recovered" (skip)
```

1. **Optimistic locking:** The engine uses version checks on state transitions. Only one replica can advance a transfer.
2. **In-process dedup:** The `recoveryInProgress` sync.Map prevents a slow cycle from overlapping with the next tick and attempting recovery on the same transfer concurrently within a single process.
3. **NATS dedup window:** If two replicas publish the same outbox intent, NATS JetStream's 2-minute dedup window drops the duplicate.
4. **CHECK-BEFORE-CALL:** Workers check the `provider_transactions` table before calling external systems, preventing double-execution even if the same intent arrives twice.

---

## Metrics

```go
type DetectorMetrics struct {
    StuckTransfersFound   *prometheus.GaugeVec  // labels: status
    RecoveryAttempts      *prometheus.CounterVec // labels: status, result
    EscalationsCreated    prometheus.Counter
    RecoveryCycleDuration prometheus.Histogram
}
```

These drive critical alerts:

| Metric | Alert Condition | Meaning |
|--------|----------------|---------|
| `settla_recovery_stuck_transfers{status="FUNDED"}` | > 100 for 5 min | Outbox relay likely down |
| `settla_recovery_stuck_transfers{status="ON_RAMPING"}` | > 500 for 15 min | Provider may be down |
| `settla_recovery_escalations_total` | rate > 10/min | Systemic failure, many transfers unrecoverable |
| `settla_recovery_cycle_duration_seconds` | > 30s | Detector overloaded, may need sharding |

---

## The Scale Math

```
50M transfers/day
0.01% stuck rate (optimistic) = 5,000 stuck transfers/day
0.10% stuck rate (bad day)    = 50,000 stuck transfers/day

Detector interval:    60 seconds
Transfers per cycle:  5,000 / (86,400 / 60) = ~3.5 transfers per cycle (optimistic)
                     50,000 / (86,400 / 60) = ~35 transfers per cycle (bad day)

Recovery query cost:  O(count of non-terminal transfers * number of statuses)
                     = 5 statuses * 1 query each = 5 queries per cycle
                     + N provider status checks per cycle

At 35 stuck transfers per cycle with provider queries:
  ~35 HTTP calls to external providers per minute
  Well within rate limits for most financial providers
```

The detector is lightweight enough to run on every server replica without coordination. The optimistic locking pattern means duplicate work is detected and discarded with negligible overhead.

---

## Common Mistakes

1. **Setting the detection interval too low.** Running every 5 seconds means 12x more provider API calls per minute. Most providers rate-limit status check APIs. 60 seconds is the sweet spot between responsiveness and API budget.

2. **Not handling ErrOptimisticLock.** If the recovery action returns an error that is NOT an optimistic lock or invalid transition, the detector correctly reports it as a failed recovery attempt. But if you add a new recovery path and forget to handle these errors, the detector will log spurious errors for every concurrent recovery attempt.

3. **Escalating without attempting recovery.** The current code escalates AND recovers. Removing the recovery attempt after escalation means the transfer sits in manual review even if automated recovery would have worked.

4. **Querying providers on every cycle.** The detector only queries providers for transfers past the RECOVER threshold, not the WARN threshold. This is deliberate: the WARN threshold exists only for logging. Querying providers at the WARN threshold would multiply API calls by 3x for no benefit.

5. **Forgetting the drain cycle on shutdown.** Without the final drain cycle, transfers that become stuck in the last 60 seconds before a deploy are left unprocessed until a replica starts up again. The 30-second drain timeout ensures these are handled.

6. **Not recovering from panics in the detection cycle.** The detector wraps each tick in a `recover()` deferred function. Without this, a panic in any recovery handler (e.g., a nil pointer from unexpected provider response data) would crash the entire detector goroutine, leaving all stuck transfers unprocessed until the process restarts. The panic-safe design logs the error and retries on the next tick.

7. **Ignoring per-tenant fairness.** Without the `maxPerTenantPerCycle` cap, a provider outage affecting one tenant could generate thousands of stuck transfers that monopolize the detector's cycle. The cap of 50 per tenant ensures other tenants' stuck transfers are still processed promptly.

---

## Exercises

1. **Threshold design:** A new provider guarantees webhook delivery within 45 minutes. What ON_RAMPING thresholds would you set for this provider? How would you make thresholds per-provider rather than per-status?

2. **Scale analysis:** If the stuck rate increases to 1% during a provider outage (500,000 stuck/day), how many provider status checks does the detector make per minute? Would this exceed typical provider rate limits of 100 requests/minute?

3. **Idempotency proof:** Two detector replicas both call `engine.InitiateOnRamp` for the same transfer within 100ms. Trace through the optimistic locking code to show that exactly one succeeds and neither causes a side effect that the other would duplicate.

4. **New status recovery:** Settla adds a new transfer status `CONFIRMING` between SETTLING and OFF_RAMPING. Write the `recoverConfirming` method. What provider interface method would you need? What threshold values would you choose?

---

## What's Next

Chapter 7.4 covers net settlement: how Settla reduces thousands of individual transfer settlements into a handful of net positions per day, dramatically reducing operational cost and counterparty risk.
