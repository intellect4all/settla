# Chapter 7.2: Compensation and Partial Failure Recovery

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why partial failures are inevitable in multi-step financial pipelines
2. Trace the decision tree that selects one of 4 compensation strategies
3. Describe how the Executor orchestrates compensation through the outbox pattern
4. Calculate FX loss from reversing a completed on-ramp conversion
5. Identify ambiguous states that require manual review versus automated resolution

---

## Why Partial Failures Happen

A Settla transfer passes through 5 sequential stages:

```
CREATED --> FUNDED --> ON_RAMPING --> SETTLING --> OFF_RAMPING --> COMPLETED
```

Each stage involves a different external system:

| Stage | External System | Failure Mode |
|-------|----------------|--------------|
| FUNDED | Treasury (in-memory) | Process crash before outbox relay |
| ON_RAMPING | Fiat on-ramp provider (e.g., bank API) | Provider timeout, rejected KYC |
| SETTLING | Blockchain (e.g., Tron USDT transfer) | Network congestion, reverted tx |
| OFF_RAMPING | Fiat off-ramp provider (e.g., mobile money) | Recipient account invalid |

The problem: a transfer can succeed at step 3 (on-ramp converts GBP to USDT) but fail at step 5 (off-ramp cannot deliver NGN to recipient). At this point:

- The tenant's GBP has been converted to USDT (irreversible without another FX trade)
- USDT has been transferred on-chain (confirmed, immutable)
- But the NGN was never delivered to the end recipient

Simply marking the transfer as "failed" is not enough. The system must *undo* what was done, and the undo strategy depends on exactly which steps completed.

```
    PARTIAL FAILURE SCENARIO

    GBP 2,847  ----[on-ramp]----> USDT 3,582.82  ---[blockchain]---> USDT arrives
        |                              |                                  |
        |         (completed)          |          (completed)             |
        v                              v                                  v
    Bank debited                 Stablecoins minted              On-chain confirmed
                                                                          |
                                                                 [off-ramp FAILS]
                                                                          |
                                                                          v
                                                                 NGN never delivered
                                                                          |
                                                                   WHAT NOW?
                                                                          |
                                    +-------------------------------------+
                                    |
                                    v
                          COMPENSATION NEEDED
                    (sell USDT back, refund GBP minus FX loss)
```

---

## The Four Compensation Strategies

**File:** `core/compensation/strategy.go`, `domain/compensation.go`

Settla defines four strategies, each corresponding to a different failure point in the pipeline:

```go
const (
    CompensationSimpleRefund    CompensationStrategy = "SIMPLE_REFUND"
    CompensationReverseOnRamp   CompensationStrategy = "REVERSE_ONRAMP"
    CompensationCreditStablecoin CompensationStrategy = "CREDIT_STABLECOIN"
    CompensationManualReview    CompensationStrategy = "MANUAL_REVIEW"
)
```

### Strategy 1: SIMPLE_REFUND

**When:** Nothing completed beyond funding. The tenant's source currency is still in the treasury reservation; no FX conversion happened.

**What happens:**
1. Release the treasury reservation (return reserved GBP)
2. Reverse ledger entries (undo the debit/credit postings)

**FX loss:** Zero. No conversion occurred.

**Steps generated:**

```go
func buildSimpleRefundSteps(transfer *domain.Transfer, tenantSlug string) []CompensationStep {
    var steps []CompensationStep

    releasePayload, _ := json.Marshal(domain.TreasuryReleasePayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        Currency:   transfer.SourceCurrency,
        Amount:     transfer.SourceAmount,
        Location:   "bank:" + lower(string(transfer.SourceCurrency)),
        Reason:     "compensation_simple_refund",
    })
    steps = append(steps, CompensationStep{
        Type:    domain.IntentTreasuryRelease,
        Payload: releasePayload,
    })

    // Build explicit reversal lines to undo any on-ramp posting
    var reversalLines []domain.LedgerLineEntry
    if transfer.StableAmount.IsPositive() {
        reversalLines = []domain.LedgerLineEntry{
            {
                AccountCode: "assets:crypto:" + lower(string(transfer.StableCoin)) + ":" +
                    lower(string(transfer.Chain)),
                EntryType:   "CREDIT",
                Amount:      transfer.StableAmount,
                Currency:    string(transfer.StableCoin),
                Description: "Compensation: reverse crypto asset",
            },
            {
                AccountCode: "expenses:provider:onramp",
                EntryType:   "CREDIT",
                Amount:      transfer.Fees.OnRampFee,
                Currency:    string(transfer.SourceCurrency),
                Description: "Compensation: reverse on-ramp fee",
            },
            {
                AccountCode: domain.TenantAccountCode(tenantSlug,
                    "assets:bank:" + lower(string(transfer.SourceCurrency)) + ":clearing"),
                EntryType:   "DEBIT",
                Amount:      transfer.SourceAmount,
                Currency:    string(transfer.SourceCurrency),
                Description: "Compensation: debit clearing account",
            },
        }
    }

    reversePayload, _ := json.Marshal(domain.LedgerPostPayload{
        TransferID:     transfer.ID,
        TenantID:       transfer.TenantID,
        IdempotencyKey: "compensation-reverse:" + transfer.ID.String(),
        Description:    "Compensation reversal for transfer " + transfer.ID.String(),
        ReferenceType:  "reversal",
        Lines:          reversalLines,
    })
    steps = append(steps, CompensationStep{
        Type:    domain.IntentLedgerReverse,
        Payload: reversePayload,
    })

    return steps
}
```

### Strategy 2: REVERSE_ONRAMP

**When:** On-ramp completed (fiat converted to stablecoin) but a later step failed. The tenant's default refund preference is "source" currency (the default).

**What happens:**
1. Sell stablecoins back to source currency via the same on-ramp provider
2. Release the treasury reservation
3. Reverse ledger entries

**FX loss:** Yes. The reversal rate will differ from the original rate. The tenant bears this loss.

**Steps generated:**

```go
func buildReverseOnRampSteps(transfer *domain.Transfer, tenantSlug string) []CompensationStep {
    var steps []CompensationStep

    reverseOnRampPayload, _ := json.Marshal(ProviderReverseOnRampPayload{
        TransferID:     transfer.ID,
        TenantID:       transfer.TenantID,
        ProviderID:     transfer.OnRampProviderID,
        StableAmount:   transfer.StableAmount,
        StableCoin:     transfer.StableCoin,
        SourceCurrency: transfer.SourceCurrency,
        OriginalRate:   transfer.FXRate,
    })
    steps = append(steps, CompensationStep{
        Type:    domain.IntentProviderReverseOnRamp,
        Payload: reverseOnRampPayload,
    })

    // ... treasury release step follows, then ledger reversal with explicit
    // lines to reverse the on-ramp posting (same pattern as simple refund)
    return steps
}
```

### Strategy 3: CREDIT_STABLECOIN

**When:** On-ramp completed but a later step failed. The tenant has set `auto_refund_currency: "stablecoin"` in their metadata, indicating they prefer to keep the stablecoins rather than convert back.

**What happens:**
1. Credit the tenant's stablecoin position (they keep the USDT)
2. Release the source currency treasury reservation

**FX loss:** Zero. The tenant keeps the stablecoins at the original conversion rate.

**Steps generated:**

```go
func buildCreditStablecoinSteps(transfer *domain.Transfer) []CompensationStep {
    var steps []CompensationStep

    creditPayload, _ := json.Marshal(PositionCreditPayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        Currency:   transfer.StableCoin,
        Amount:     transfer.StableAmount,
    })
    steps = append(steps, CompensationStep{
        Type:    "position.credit",
        Payload: creditPayload,
    })

    // ... treasury release step follows
    return steps
}
```

### Strategy 4: MANUAL_REVIEW

**When:** The transfer is in an ambiguous state. The system cannot determine with certainty what has completed and what has not.

**What happens:** Nothing automated. A manual review record is created for a human operator.

**FX loss:** Unknown until human investigates.

---

## The Decision Tree

**File:** `core/compensation/strategy.go`

The `DetermineCompensation` function implements the complete decision logic:

```
                    COMPENSATION DECISION TREE

                         Transfer fails
                              |
                              v
                    Is state ambiguous?  ------YES-----> MANUAL_REVIEW
                    (ON_RAMPING with                     (human must investigate)
                     unknown provider
                     status, or SETTLING
                     with no blockchain
                     confirmation)
                              |
                             NO
                              |
                              v
                    Did on-ramp complete? ---NO-------> SIMPLE_REFUND
                    (is "onramp_completed"              (release treasury,
                     in completedSteps?)                 reverse ledger)
                              |
                            YES
                              |
                              v
                    Tenant refund preference?
                              |
                    +---------+---------+
                    |                   |
              "stablecoin"          "source" (default)
                    |                   |
                    v                   v
            CREDIT_STABLECOIN    REVERSE_ONRAMP
            (credit USDT         (sell USDT back,
             position,            refund GBP,
             no FX loss)          tenant bears FX loss)
```

### Resolving Ambiguous States

The key complexity is determining whether a state is ambiguous. This depends on optional `ExternalStatus` data from provider queries:

```go
func isAmbiguousState(transfer *domain.Transfer, completedSteps []string, ext ExternalStatus) bool {
    switch transfer.Status {
    case domain.TransferStatusOnRamping:
        switch ext.OnRampStatus {
        case "completed", "failed":
            return false // status is known
        default:
            return true  // "pending", "unknown", or "" -- still ambiguous
        }
    case domain.TransferStatusSettling:
        if !containsStep(completedSteps, StepOnRampCompleted) {
            return true  // shouldn't be settling without on-ramp done
        }
        if ext.BlockchainConfirmed != nil {
            return false // blockchain status is known
        }
        if ext.BlockchainError != "" {
            return false // blockchain check returned error
        }
    }
    return false
}
```

When an ON_RAMPING transfer is stuck and the recovery detector (Chapter 7.3) queries the provider, the provider's response resolves the ambiguity:

| Provider Status | Resolution |
|----------------|------------|
| `"completed"` | Not ambiguous. On-ramp completed, use REVERSE_ONRAMP or CREDIT_STABLECOIN |
| `"failed"` | Not ambiguous. On-ramp failed, use SIMPLE_REFUND |
| `"pending"` | Still ambiguous. Use MANUAL_REVIEW |
| `"unknown"` or `""` | Still ambiguous. Use MANUAL_REVIEW |

> **Key Insight:** The compensation system never guesses. If it cannot determine with certainty what happened, it escalates to a human. This is the correct default for financial systems: false positives (unnecessary manual reviews) are vastly preferable to false negatives (incorrect automated refunds that create double-spend situations).

---

## FX Loss Calculation

**File:** `core/compensation/fx_loss.go`

When reversing a completed on-ramp, the tenant loses money due to the bid-ask spread:

```go
func CalculateFXLoss(
    originalAmount decimal.Decimal,
    originalRate decimal.Decimal,
    reversalRate decimal.Decimal,
) FXLossResult {
    // stableAmount = originalAmount * originalRate
    stableAmount := originalAmount.Mul(originalRate)

    // reversedAmount = stableAmount / reversalRate
    result.ReversedAmount = stableAmount.Div(reversalRate)

    // loss = originalAmount - reversedAmount
    loss := originalAmount.Sub(result.ReversedAmount)

    // FX loss is always >= 0: tenant doesn't profit from a reversal.
    if loss.IsNegative() {
        loss = decimal.Zero
        result.ReversedAmount = originalAmount
    }

    result.FXLoss = loss
    // ...
}
```

**Worked example:**

```
Original:   GBP 2,847 * 1.2581 (rate) = USDT 3,582.05
Reversal:   USDT 3,582.05 / 1.2656 (new rate) = GBP 2,830.21
FX loss:    GBP 2,847 - GBP 2,830.21 = GBP 16.79 (0.59%)
```

The `FXLossResult` captures the full picture:

```go
type FXLossResult struct {
    OriginalAmount decimal.Decimal // GBP 2,847
    ReversedAmount decimal.Decimal // GBP 2,830.21
    FXLoss         decimal.Decimal // GBP 16.79 (always >= 0)
    LossPercent    decimal.Decimal // 0.59%
    OriginalRate   decimal.Decimal // 1.2581
    ReversalRate   decimal.Decimal // 1.2656
}
```

**Critical design decision:** If the reversal rate is more favorable than the original (the tenant would profit), the loss is capped at zero and the reversed amount is capped at the original. The tenant does not receive windfall gains from a reversal. This protects Settla from a scenario where tenants intentionally trigger failures to profit from favorable rate movements.

---

## The Executor

**File:** `core/compensation/executor.go`

The Executor takes a `CompensationPlan` and orchestrates its execution through the engine's outbox pattern:

```go
type Executor struct {
    store   CompensationStore
    engine  CompensationEngine
    logger  *slog.Logger
    metrics *ExecutorMetrics
}
```

### Execution Flow

```go
func (e *Executor) Execute(ctx context.Context, plan CompensationPlan) error {
    // 1. Create compensation record (audit trail)
    recordID, err := e.store.CreateCompensationRecord(ctx, CreateCompensationParams{
        TransferID:     plan.TransferID,
        TenantID:       plan.TenantID,
        Strategy:       string(plan.Strategy),
        RefundAmount:   plan.RefundAmount,
        RefundCurrency: string(plan.RefundCurrency),
    })

    // 2. Delegate to engine based on strategy
    switch plan.Strategy {
    case StrategySimpleRefund:
        err = e.executeSimpleRefund(ctx, plan)
    case StrategyReverseOnRamp:
        err = e.executeReverseOnRamp(ctx, plan)
    case StrategyCreditStablecoin:
        err = e.executeCreditStablecoin(ctx, plan)
    case StrategyManualReview:
        // No automated steps
    }

    // 3. Track completed/failed steps
    status := "completed"
    if err != nil {
        status = "failed"
    }
    if plan.Strategy == StrategyManualReview {
        status = "pending_review"
    }

    // 4. Update record with final status and FX loss
    e.store.UpdateCompensationRecord(ctx, recordID,
        completedJSON, failedJSON, plan.FXLoss, status)

    return err
}
```

### SimpleRefund: The FailTransfer-First Pattern

A subtle detail in the simple refund strategy: if the transfer is not already in FAILED state, the executor must fail it first:

```go
func (e *Executor) executeSimpleRefund(ctx context.Context, plan CompensationPlan) error {
    if plan.TransferStatus != domain.TransferStatusFailed {
        if err := e.engine.FailTransfer(ctx, plan.TenantID, plan.TransferID,
            "provider confirmed failure during compensation",
            "COMPENSATION_SIMPLE_REFUND",
        ); err != nil {
            return fmt.Errorf("settla-compensation: failing transfer before refund: %w", err)
        }
    }
    return e.engine.InitiateRefund(ctx, plan.TenantID, plan.TransferID)
}
```

This is necessary because the engine's `InitiateRefund` method expects the transfer to be in FAILED state. When the recovery detector finds an ON_RAMPING transfer whose provider reports "failed," the transfer is still in ON_RAMPING state. The executor must transition it to FAILED first.

> **Key Insight:** The executor never executes side effects directly. It delegates to `engine.FailTransfer` and `engine.InitiateRefund`, which write outbox entries atomically. Workers pick up these entries and perform the actual treasury release and ledger reversal. This preserves the outbox pattern invariant: no direct side effects from the engine or its callers.

### CompensationRecord as Audit Trail

Every compensation attempt is recorded:

```go
type CreateCompensationParams struct {
    TransferID     uuid.UUID
    TenantID       uuid.UUID
    Strategy       string
    RefundAmount   decimal.Decimal
    RefundCurrency string
}
```

The record is updated after execution with:
- `stepsCompleted`: JSON array of steps that succeeded (e.g., `["treasury.release", "ledger.reverse"]`)
- `stepsFailed`: JSON array of steps that failed (e.g., `["initiate_refund"]`)
- `fxLoss`: The calculated FX loss (zero for non-REVERSE_ONRAMP strategies)
- `status`: Final status (`"completed"`, `"failed"`, or `"pending_review"`)

This audit trail is critical for regulatory compliance. Financial regulators require documented evidence of every compensation decision, including why a particular strategy was chosen and what the outcome was.

---

## Metrics

The executor emits three Prometheus metrics:

```go
type ExecutorMetrics struct {
    CompensationsStarted   *prometheus.CounterVec // labels: strategy
    CompensationsCompleted *prometheus.CounterVec // labels: strategy, result
    CompensationDuration   prometheus.Histogram
}
```

These allow operators to monitor:
- Which strategies are being invoked most frequently (spikes in MANUAL_REVIEW might indicate a provider outage)
- Success vs failure rates per strategy
- How long compensations take (useful for SLA tracking)

---

## Common Mistakes

1. **Calling external providers directly from the executor.** The executor delegates to the engine, which writes outbox entries. Workers execute the actual calls. Bypassing this pattern creates dual-write bugs.

2. **Assuming REVERSE_ONRAMP always succeeds.** The reversal itself can fail (provider down, insufficient liquidity for the reverse trade). The executor records this as a failed step and returns an error.

3. **Forgetting the FX loss cap at zero.** Without the cap, a favorable reversal rate creates negative loss, which would be reported as a profit. The system intentionally does not pass through reversal profits.

4. **Not handling the FailTransfer-before-refund case.** If a transfer is in ON_RAMPING state (not FAILED), calling `InitiateRefund` directly would violate the state machine. The executor must transition through FAILED first.

5. **Treating MANUAL_REVIEW as an error.** It is a legitimate outcome. The executor sets status to `"pending_review"` and returns nil (no error). Manual review is a feature, not a failure.

---

## Exercises

1. **Trace a REVERSE_ONRAMP compensation:** Given a transfer where GBP 5,000 was converted to USDT 6,290.50 at rate 1.2581, and the current reversal rate is 1.2700, calculate:
   - The reversed GBP amount
   - The FX loss in GBP
   - The loss percentage

2. **Design a new strategy:** A tenant requests a "partial stablecoin credit" strategy where 50% of the stablecoins are credited and 50% are sold back. What steps would this strategy generate? What complications arise from splitting the amount?

3. **Ambiguity analysis:** A transfer is in SETTLING state. The `completedSteps` list contains `["funded", "onramp_completed"]` but the `ExternalStatus` has `BlockchainConfirmed: nil` and `BlockchainError: ""`. Is this ambiguous? Trace through `isAmbiguousState` to find the answer.

4. **Error recovery:** The executor creates a compensation record, then the engine call fails, then the record update also fails. What is the system state? How would an operator recover?

---

## What's Next

Chapter 7.3 covers the stuck transfer detector: the background process that discovers transfers needing compensation in the first place. The detector feeds the compensation system by identifying stuck transfers, querying providers for their actual status, and triggering recovery actions.
