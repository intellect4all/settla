# Chapter 7.1: Automated Reconciliation

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why automated reconciliation is critical in high-volume financial systems
2. Describe all 6 consistency checks Settla runs and what each one detects
3. Trace how the Reconciler orchestrates checks, builds reports, and gates features
4. Implement a new reconciliation check by conforming to the `Check` interface
5. Design tolerance-based comparison strategies for monetary values

---

## Why Reconciliation Matters

At 50M transactions/day, Settla processes approximately $2.5 billion in daily volume across multiple databases, caches, and external providers. The system has three independent sources of truth for financial data:

- **TigerBeetle** (ledger write authority)
- **PostgreSQL** (ledger read model, transfer state, treasury snapshots)
- **In-memory treasury** (real-time reservations, flushed every 100ms)

These systems can drift apart due to:

- Network partitions between TigerBeetle and Postgres sync
- Outbox relay delays or failures
- Provider webhook callbacks that never arrive
- Partial writes during process crashes
- Treasury flush goroutine lag under extreme load

Manual reconciliation at this volume is impossible. A human reviewing 50M transfers would take approximately 95 years at 1 second per review. Settla's reconciliation engine automates this entirely.

```
                 RECONCILIATION ARCHITECTURE

  +------------------+     +------------------+     +------------------+
  |    TigerBeetle   |     |   PostgreSQL     |     |  In-Memory       |
  |  (ledger writes) |     | (read model +    |     |  Treasury        |
  |                  |     |  transfer state) |     |  (reservations)  |
  +--------+---------+     +--------+---------+     +--------+---------+
           |                        |                        |
           +----------+    +--------+             +----------+
                      |    |                      |
                      v    v                      v
              +-------+----+------+     +---------+---------+
              | Treasury-Ledger   |     | Transfer State    |
              | Balance Check     |     | Consistency Check |
              +-------------------+     +-------------------+
                      |                          |
        +-------------+      +-------------------+      +-----------+
        |                    |                          |           |
        v                    v                          v           v
  +----------+    +----------+--------+    +-----------+   +-------+------+
  | Outbox   |    | Provider Tx       |    | Daily     |   | Settlement   |
  | Health   |    | Reconciliation    |    | Volume    |   | Fee Recon    |
  | Check    |    | Check             |    | Sanity    |   | Check        |
  +----------+    +-------------------+    +-----------+   +--------------+
        |                    |                   |                |
        +----------+---------+-------------------+----------------+
                   |
                   v
         +---------+----------+
         | ReconciliationReport|
         | (stored to DB)     |
         +--------------------+
```

---

## The Check Interface

Every reconciliation check implements a single interface:

```go
// Check is a single reconciliation check that verifies one aspect of system consistency.
type Check interface {
    // Name returns a human-readable identifier for this check.
    Name() string
    // Run executes the check and returns its result.
    Run(ctx context.Context) (*CheckResult, error)
}
```

And returns a standardized result:

```go
// CheckResult captures the outcome of a single reconciliation check.
// (Defined in domain/reconciliation.go as ReconciliationCheckResult)
type CheckResult struct {
    Name       string    `json:"name"`
    Status     string    `json:"status"`      // "pass", "fail", or "warn"
    Details    string    `json:"details"`
    Mismatches int       `json:"mismatches"`
    CheckedAt  time.Time `json:"checked_at"`
}
```

Three status levels exist:

| Status | Meaning | Example |
|--------|---------|---------|
| `pass` | All assertions hold | Treasury and ledger balances match within tolerance |
| `warn` | Soft anomaly, not necessarily broken | 8 provider transactions pending >1 hour |
| `fail` | Hard discrepancy, requires investigation | Unpublished outbox entries older than 5 minutes |

> **Key Insight:** The `warn` status exists because some checks cannot distinguish between "slow" and "broken." A provider transaction pending for 2 hours might be a slow SWIFT transfer, or it might be a lost webhook. The `warn` status lets operators triage without drowning in false positives.

---

## The 6 Reconciliation Checks

### Check 1: Treasury-Ledger Balance

**File:** `core/reconciliation/check_treasury_ledger.go`

This is the most critical check. It compares the in-memory treasury position for every tenant/currency/location against the corresponding ledger account balance.

```go
type TreasuryLedgerCheck struct {
    treasury     domain.TreasuryManager
    ledger       LedgerQuerier
    tenants      TenantLister
    slugResolver TenantSlugResolver
    logger       *slog.Logger
    tolerance    decimal.Decimal
}
```

The check iterates over all active tenants in batches (to bound memory usage at 100K+ tenants), retrieves their treasury positions, constructs the corresponding ledger account code, and compares:

```go
// TenantLister iterates over active tenants in batches for reconciliation.
type TenantLister interface {
    ForEachActiveTenant(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error
}

func (c *TreasuryLedgerCheck) Run(ctx context.Context) (*CheckResult, error) {
    var mismatches int
    var details []string

    err := c.tenants.ForEachActiveTenant(ctx, 500, func(tenantIDs []uuid.UUID) error {
        for _, tenantID := range tenantIDs {
            slug, err := c.slugResolver.GetTenantSlug(ctx, tenantID)
            // ... (falls back to UUID if slug resolution fails)

            positions, err := c.treasury.GetPositions(ctx, tenantID)
            // ...

            for _, pos := range positions {
                accountCode := buildAccountCode(slug, pos)
                ledgerBalance, err := c.ledger.GetAccountBalance(ctx, accountCode)
                // ...

                treasuryBalance := pos.Balance
                diff := treasuryBalance.Sub(ledgerBalance).Abs()
                if diff.GreaterThan(c.tolerance) {
                    mismatches++
                    details = append(details, fmt.Sprintf(
                        "tenant=%s account=%s: treasury=%s ledger=%s diff=%s",
                        tenantID, accountCode,
                        treasuryBalance.StringFixed(2),
                        ledgerBalance.StringFixed(2),
                        diff.StringFixed(2),
                    ))
                }
            }
        }
        return nil
    })
    // ...
}
```

Account codes are constructed from the tenant slug and position metadata:

```go
// Format: tenant:{slug}:assets:bank:{currency}:{location}
func buildAccountCode(slug string, pos domain.Position) string {
    return fmt.Sprintf("tenant:%s:assets:bank:%s:%s",
        slug,
        strings.ToLower(string(pos.Currency)),
        pos.Location,
    )
}
```

The tolerance parameter (default: 0.01 USD equivalent) accounts for legitimate timing differences. The treasury flush goroutine runs every 100ms, so at any given instant the in-memory position may be slightly ahead of the persisted ledger balance. A tolerance of 0.01 absorbs this jitter without masking real discrepancies.

> **Common Mistake:** Setting tolerance to zero. The 100ms treasury flush interval means there is always a brief window where treasury and ledger differ by the amount of in-flight reservations. Zero tolerance produces constant false positives.

---

### Check 2: Transfer State Consistency

**File:** `core/reconciliation/check_transfer_state.go`

This check identifies transfers stuck in non-terminal states beyond configurable time thresholds:

```go
var DefaultTransferThresholds = map[domain.TransferStatus]time.Duration{
    domain.TransferStatusFunded:     30 * time.Minute,
    domain.TransferStatusOnRamping:  2 * time.Hour,
    domain.TransferStatusSettling:   1 * time.Hour,
    domain.TransferStatusOffRamping: 2 * time.Hour,
}
```

For each non-terminal status, the check counts transfers older than the threshold:

```go
func (c *TransferStateCheck) Run(ctx context.Context) (*CheckResult, error) {
    var totalStuck int
    var details []string

    for status, threshold := range c.thresholds {
        cutoff := time.Now().UTC().Add(-threshold)
        count, err := c.store.CountTransfersInStatus(ctx, status, cutoff)
        // ...

        if count > 0 {
            totalStuck += count
            details = append(details, fmt.Sprintf(
                "%s: %d transfers stuck > %s",
                status, count, threshold,
            ))
        }
    }
    // ...
}
```

Note the distinction from the recovery detector (Chapter 7.3): reconciliation only *counts* stuck transfers. Recovery *acts* on them. This separation follows the principle that observation and action are different responsibilities.

---

### Check 3: Outbox Health

**File:** `core/reconciliation/check_outbox.go`

The transactional outbox is the backbone of Settla's event-driven architecture. This check verifies two aspects of outbox health:

1. **Stale entries:** Outbox entries unpublished after the max age (default 5 minutes) indicate the relay is down or overloaded
2. **Default partition leaks:** Rows in the default partition mean future partitions are missing

```go
func (c *OutboxCheck) Run(ctx context.Context) (*CheckResult, error) {
    cutoff := time.Now().UTC().Add(-c.maxAge)

    unpublished, err := c.store.CountUnpublishedOlderThan(ctx, cutoff)
    // ...

    defaultRows, err := c.store.CountDefaultPartitionRows(ctx)
    // ...

    var issues int
    var details []string

    if unpublished > 0 {
        issues += unpublished
        details = append(details, fmt.Sprintf(
            "%d unpublished entries older than %s", unpublished, c.maxAge,
        ))
    }

    if defaultRows > 0 {
        issues += defaultRows
        details = append(details, fmt.Sprintf(
            "%d rows in default partition (expected 0)", defaultRows,
        ))
    }
    // ...
}
```

The default partition check is subtle but important. PostgreSQL range-partitioned tables use a default partition as a catch-all for rows that do not match any partition range. At 50M outbox entries/day, if the partition manager fails to create tomorrow's partition, all new rows silently land in the default partition. Queries that scan by date range will miss them entirely, causing the relay to skip outbox entries and stall the entire pipeline.

> **Key Insight:** The default partition is a canary. If it ever contains rows, it means the partition manager (Chapter 7.5) is failing or running behind. This check catches partition management failures before they cascade into data loss.

---

### Check 4: Provider Transaction Reconciliation

**File:** `core/reconciliation/check_provider_tx.go`

This lightweight check counts provider transactions stuck in "pending" status beyond a configurable threshold (default 1 hour):

```go
func (c *ProviderTxCheck) Run(ctx context.Context) (*CheckResult, error) {
    cutoff := time.Now().UTC().Add(-c.maxPendingAge)

    count, err := c.store.CountPendingProviderTxOlderThan(ctx, cutoff)
    // ...

    status := "pass"
    if count > 0 {
        status = "warn"   // <-- "warn", not "fail"
        details = fmt.Sprintf("%d pending provider transactions older than %s",
            count, c.maxPendingAge)
    }
    // ...
}
```

Notice this check returns `"warn"` rather than `"fail"`. This is deliberate: provider transactions may legitimately take hours (e.g., SWIFT transfers, manual bank approvals). The check flags the anomaly without asserting it is broken.

The check is also read-only and local. It does not call external providers, which keeps the reconciliation cycle fast and free of external dependencies.

---

### Check 5: Daily Volume Sanity

**File:** `core/reconciliation/check_daily_volume.go`

This check compares today's transaction volume against the 7-day rolling average, using two thresholds:

```go
type DailyVolumeCheck struct {
    store        VolumeQuerier
    logger       *slog.Logger
    warnPercent  float64 // default: 200
    failPercent  float64 // default: 500
}
```

The calculation:

```go
func (c *DailyVolumeCheck) Run(ctx context.Context) (*CheckResult, error) {
    today := time.Now().UTC().Truncate(24 * time.Hour)
    todayCount, err := c.store.GetDailyTransferCount(ctx, today)
    // ...

    endDate := today.Add(-24 * time.Hour)
    startDate := today.Add(-7 * 24 * time.Hour)
    avg, err := c.store.GetAverageDailyTransferCount(ctx, startDate, endDate)
    // ...

    if avg == 0 {
        return &CheckResult{
            Name:    c.Name(),
            Status:  "pass",
            Details: fmt.Sprintf("today=%d, no historical average available", todayCount),
            // ...
        }, nil
    }

    pct := (float64(todayCount) / avg) * 100

    if pct >= c.failPercent {     // 500%
        status = "fail"
    } else if pct >= c.warnPercent { // 200%
        status = "warn"
    }
    // ...
}
```

| Volume vs Average | Status | Meaning |
|-------------------|--------|---------|
| < 200% | `pass` | Normal variation |
| 200% - 499% | `warn` | Volume spike, investigate |
| >= 500% | `fail` | Extreme spike, possible attack or duplicate processing |

The check handles the cold-start case (no historical data) by returning `pass` with a note. This prevents false alarms on newly deployed systems.

---

### Check 6: Settlement Fee Reconciliation

**File:** `core/reconciliation/check_settlement_fees.go`

This check catches fee calculation drift between settlement records and individual transfers:

```go
func (c *SettlementFeeCheck) Run(ctx context.Context) (*CheckResult, error) {
    settlement, err := c.store.GetLatestNetSettlement(ctx)
    // ...

    if settlement == nil {
        return &CheckResult{
            Name:   c.Name(),
            Status: "pass",
            Details: "no net settlements found; skipping fee reconciliation",
            // ...
        }, nil
    }

    transferFees, err := c.store.SumCompletedTransferFeesUSD(
        ctx,
        settlement.TenantID,
        settlement.PeriodStart,
        settlement.PeriodEnd,
    )
    // ...

    diff := transferFees.Sub(settlement.TotalFeesUSD).Abs()

    if diff.GreaterThan(c.tolerance) {
        // ... report failure with full details
    }
    // ...
}
```

**Why this check exists:** The settlement calculator sums fees from transfer rows at calculation time and writes the total to `net_settlements.total_fees_usd`. If a transfer fee is later amended, a rounding inconsistency exists, or a transfer is retroactively marked as completed after the settlement window, the two totals diverge silently. This check detects that drift.

---

## The Reconciler Orchestrator

**File:** `core/reconciliation/reconciler.go`

The `Reconciler` ties all checks together:

```go
type Reconciler struct {
    checks      []Check
    store       ReportStore
    logger      *slog.Logger
    metrics     *ReconcilerMetrics
    flagChecker FeatureFlagChecker
}
```

### Feature Gating

Checks whose names start with `"enhanced_"` are gated behind a feature flag:

```go
for _, check := range r.checks {
    if r.flagChecker != nil && strings.HasPrefix(check.Name(), "enhanced_") {
        if !r.flagChecker.IsEnabled("enhanced_reconciliation") {
            r.logger.Debug("settla-reconciliation: skipping gated check",
                slog.String("check", check.Name()),
            )
            continue
        }
    }
    // ... run check
}
```

This allows new, experimental checks to be deployed in production without running them until explicitly enabled. An operator flips the `enhanced_reconciliation` flag to activate them.

### Report Building

The report aggregates all check results with an overall pass/fail:

```go
report := &Report{
    ID:          uuid.New(),
    RunAt:       time.Now().UTC(),
    OverallPass: true,
}

for _, check := range r.checks {
    result, err := check.Run(ctx)
    if err != nil {
        result = &CheckResult{
            Name:    check.Name(),
            Status:  "fail",
            Details: fmt.Sprintf("check error: %v", err),
            // ...
        }
    }

    if result.Status != "pass" {
        report.OverallPass = false
    }

    report.Results = append(report.Results, *result)
}
```

**Important:** `OverallPass` is `false` if *any* check returns a non-`"pass"` status, including `"warn"`. This means a single stale provider transaction causes the entire report to be marked as not passing. This is intentional: operators should be aware of all anomalies, even soft ones.

### Metrics

Every reconciliation run emits Prometheus metrics:

```go
type ReconcilerMetrics struct {
    ReconciliationRuns     prometheus.Counter
    ReconciliationFailed   prometheus.Counter
    DiscrepanciesFound     *prometheus.CounterVec // labels: check_name
    ReconciliationDuration prometheus.Histogram
}
```

These drive Grafana dashboards and PagerDuty alerts. The `DiscrepanciesFound` counter with per-check labels allows operators to see which checks are failing most frequently.

---

## Wiring It All Together

In production, the reconciler is configured in `cmd/settla-server/main.go` with all 6 checks:

```
Reconciler
  |-- TreasuryLedgerCheck   (treasury vs ledger, tolerance: 0.01)
  |-- TransferStateCheck    (stuck transfers, default thresholds)
  |-- OutboxCheck           (stale entries, max age: 5m)
  |-- ProviderTxCheck       (pending txs, max age: 1h)
  |-- DailyVolumeCheck      (7-day average, warn: 200%, fail: 500%)
  +-- SettlementFeeCheck    (fee drift, tolerance: 0.01 USD)
```

The reconciler is typically scheduled to run every 5-10 minutes via a ticker in the server's background goroutines.

---

## Common Mistakes

1. **Setting monetary tolerance to zero.** The 100ms treasury flush creates an inherent lag. Zero tolerance means constant false positives.

2. **Running reconciliation checks that call external systems.** The provider transaction check deliberately does not call providers. Reconciliation should be fast and free of external dependencies. If a check needs external data, it should read from the local database (which is populated by workers).

3. **Treating `warn` as ignorable.** Settla's `OverallPass` is false on `warn` for a reason. Sustained warnings often precede outages.

4. **Forgetting the cold-start case.** The daily volume check handles zero historical data gracefully. New checks should do the same.

5. **Not testing the tolerance boundary.** The test suite explicitly verifies that a difference of 0.005 within a 0.01 tolerance passes, and a difference of 5.00 fails. Every monetary check needs boundary tests.

---

## Exercises

1. **Design a new check:** Write a reconciliation check that compares the number of completed transfers in the Transfer DB against the number of journal entries in the Ledger DB for the same period. What tolerance would you use? What could cause legitimate differences?

2. **Analyze the timing window:** The treasury flush runs every 100ms. If the reconciler runs at the exact moment a flush completes but before the ledger sync consumer processes the TigerBeetle event, what is the maximum possible drift? How does the 0.01 tolerance handle this?

3. **Feature flag exercise:** You want to add a check that verifies NATS stream lag is below 1,000 messages. How would you name it to take advantage of the existing feature gating? What interface would the check's store need to implement?

4. **Alert design:** Given the `settla_reconciliation_discrepancies_total` counter with `check_name` labels, write a PromQL expression that alerts when the treasury-ledger check finds more than 0 discrepancies in a 15-minute window.

---

## What's Next

Chapter 7.2 covers Settla's compensation system: what happens when reconciliation (or a worker) discovers that a transfer has partially failed. While reconciliation detects problems, compensation resolves them.
