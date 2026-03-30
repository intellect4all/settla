# Chapter 7.4: Net Settlement

**Reading time: 28 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why net settlement reduces cost and risk compared to gross settlement
2. Trace the settlement calculator's aggregation pipeline from transfers to net positions
3. Describe the daily scheduler's lifecycle and T+3 due date model
4. Design overdue escalation policies with tiered actions
5. Generate settlement instructions from corridor positions

---

## Why Net Settlement

Consider a typical day for Fincra (one of Settla's tenants):

```
10,000 GBP -> NGN transfers  (total: GBP 28,470,000 out, NGN 52,000,000,000 in)
 3,000 NGN -> GBP transfers  (total: NGN 15,600,000,000 out, GBP 8,541,000 in)
```

**Gross settlement** would require 13,000 individual settlement transactions. Each one incurs bank fees, compliance checks, and operational overhead.

**Net settlement** reduces this to just 2 instructions:

```
GBP: Fincra sent 28,470,000 - received 8,541,000 = NET 19,929,000 (Fincra owes Settla)
NGN: Fincra received 52,000,000,000 - sent 15,600,000,000 = NET 36,400,000,000 (Settla owes Fincra)
```

```
          GROSS vs NET SETTLEMENT

  GROSS: 13,000 individual settlements
  +--------+     +--------+
  | Fincra | --> | Settla |  x 10,000 (GBP->NGN)
  +--------+ <-- +--------+  x  3,000 (NGN->GBP)
  Bank fees: 13,000 * $2.50 = $32,500/day

  NET: 2 settlement instructions
  +--------+  GBP 19.9M  +--------+
  | Fincra | -----------> | Settla |
  +--------+ <----------- +--------+
               NGN 36.4B
  Bank fees: 2 * $2.50 = $5.00/day

  SAVINGS: $32,495/day = $11.9M/year (per tenant)
```

Settla supports two settlement models, configured per tenant:

```go
const (
    SettlementModelPrefunded     SettlementModel = "PREFUNDED"
    SettlementModelNetSettlement SettlementModel = "NET_SETTLEMENT"
)
```

Prefunded tenants pre-deposit funds into a treasury position. Net settlement tenants settle on a periodic basis (daily, with T+3 payment terms).

---

## The Calculator

**File:** `core/settlement/calculator.go`

### Data Model

```go
// TransferSummary is a lightweight projection of a completed transfer.
type TransferSummary struct {
    SourceCurrency string
    SourceAmount   decimal.Decimal
    DestCurrency   string
    DestAmount     decimal.Decimal
    Fees           decimal.Decimal // total fees in USD
}

// NetSettlement represents a computed net settlement for a tenant over a period.
type NetSettlement struct {
    ID            uuid.UUID
    TenantID      uuid.UUID
    TenantName    string
    PeriodStart   time.Time
    PeriodEnd     time.Time
    Corridors     []CorridorPosition      // per-corridor aggregation
    NetByCurrency []CurrencyNet           // net position per currency
    TotalFeesUSD  decimal.Decimal
    Instructions  []SettlementInstruction // human-readable directives
    Status        string                  // "pending", "settled", "overdue"
    DueDate       *time.Time              // T+3 from period end
    CreatedAt     time.Time
}
```

### The Calculation Pipeline

```go
func (c *Calculator) CalculateNetSettlement(
    ctx context.Context,
    tenantID uuid.UUID,
    periodStart, periodEnd time.Time,
) (*NetSettlement, error) {
```

**Step 0: Validate period range**

```go
    if !periodStart.Before(periodEnd) {
        return nil, fmt.Errorf("settla-settlement: invalid period range: ...")
    }
```

**Step 1: Load tenant and validate settlement model**

```go
    tenant, err := c.tenantStore.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-settlement: loading tenant %s: %w", tenantID, err)
    }

    if tenant.SettlementModel != domain.SettlementModelNetSettlement {
        return nil, fmt.Errorf("settla-settlement: tenant %s uses %s model, not NET_SETTLEMENT",
            tenantID, tenant.SettlementModel)
    }
```

This guard prevents accidentally calculating net settlements for prefunded tenants, which would produce nonsensical results.

**Step 2: Query pre-aggregated corridor summaries (DB-side GROUP BY)**

```go
    aggregates, err := c.transferStore.AggregateCompletedTransfersByPeriod(
        ctx, tenantID, periodStart, periodEnd)
```

Instead of materializing individual transfer rows and grouping them in Go, the calculator uses a `TransferStore` method that performs the aggregation server-side via SQL `GROUP BY source_currency, dest_currency`. This is critical at scale: a tenant with 10,000 transfers per day returns only 2-5 aggregate rows instead of 10,000 individual rows.

```go
// CorridorAggregate is a pre-aggregated summary computed server-side via GROUP BY.
type CorridorAggregate struct {
    SourceCurrency string
    DestCurrency   string
    TotalSource    decimal.Decimal
    TotalDest      decimal.Decimal
    TransferCount  int64
    TotalFeesUSD   decimal.Decimal
}
```

Only COMPLETED transfers are included. Transfers still in progress, failed, or refunded are excluded.

**Step 3: Build corridors from aggregates**

```go
func (c *Calculator) corridorsFromAggregates(aggregates []CorridorAggregate) []CorridorPosition {
    result := make([]CorridorPosition, 0, len(aggregates))
    for _, a := range aggregates {
        result = append(result, CorridorPosition{
            SourceCurrency: a.SourceCurrency,
            DestCurrency:   a.DestCurrency,
            TotalSource:    a.TotalSource,
            TotalDest:      a.TotalDest,
            TransferCount:  int(a.TransferCount),
        })
    }
    return result
}
```

This produces a view like:

| Corridor | Transfer Count | Total Source | Total Dest |
|----------|---------------|-------------|------------|
| GBP -> NGN | 10,000 | GBP 28,470,000 | NGN 52,000,000,000 |
| NGN -> GBP | 3,000 | NGN 15,600,000,000 | GBP 8,541,000 |

**Step 4: Compute net per currency**

```go
func (c *Calculator) netByCurrencyFromAggregates(aggregates []CorridorAggregate) []CurrencyNet {
    nets := make(map[string]*CurrencyNet)

    for _, a := range aggregates {
        // Source currency: outflow
        srcNet, ok := nets[a.SourceCurrency]
        if !ok {
            srcNet = &CurrencyNet{Currency: a.SourceCurrency, Inflows: decimal.Zero, Outflows: decimal.Zero}
            nets[a.SourceCurrency] = srcNet
        }
        srcNet.Outflows = srcNet.Outflows.Add(a.TotalSource)

        // Dest currency: inflow
        destNet, ok := nets[a.DestCurrency]
        if !ok {
            destNet = &CurrencyNet{Currency: a.DestCurrency, Inflows: decimal.Zero, Outflows: decimal.Zero}
            nets[a.DestCurrency] = destNet
        }
        destNet.Inflows = destNet.Inflows.Add(a.TotalDest)
    }

    result := make([]CurrencyNet, 0, len(nets))
    for _, net := range nets {
        net.Net = net.Inflows.Sub(net.Outflows)
        result = append(result, *net)
    }
    return result
}
```

The net position per currency tells us who owes whom:

```
  CURRENCY NET CALCULATION

  GBP:
    Inflows  (tenant receives):  8,541,000  (from NGN->GBP transfers)
    Outflows (tenant sends):    28,470,000  (from GBP->NGN transfers)
    Net = 8,541,000 - 28,470,000 = -19,929,000
    (Negative = tenant owes Settla GBP 19,929,000)

  NGN:
    Inflows  (tenant receives): 52,000,000,000  (from GBP->NGN transfers)
    Outflows (tenant sends):    15,600,000,000  (from NGN->GBP transfers)
    Net = 52,000,000,000 - 15,600,000,000 = +36,400,000,000
    (Positive = Settla owes tenant NGN 36,400,000,000)
```

**Step 5: Sum total fees**

```go
    totalFees := decimal.Zero
    for _, a := range aggregates {
        totalFees = totalFees.Add(a.TotalFeesUSD)
    }
```

Fees are always in USD, regardless of the transfer currencies. This provides a common denominator for multi-corridor tenants. The `TotalFeesUSD` field is computed server-side in the same `GROUP BY` query, so no individual transfer rows need to be fetched.

**Step 6: Generate settlement instructions**

```go
func (c *Calculator) generateInstructions(
    tenantName string,
    netByCurrency []CurrencyNet,
    totalFees decimal.Decimal,
) []SettlementInstruction {
    var instructions []SettlementInstruction

    for _, net := range netByCurrency {
        if net.Net.IsZero() {
            continue // perfectly netted, no action needed
        }

        var inst SettlementInstruction
        inst.Currency = net.Currency

        if net.Net.IsPositive() {
            inst.Direction = "settla_owes_tenant"
            inst.Amount = net.Net
            inst.Description = fmt.Sprintf("Settla owes %s %s %s",
                tenantName, formatAmount(net.Net), net.Currency)
        } else {
            inst.Direction = "tenant_owes_settla"
            inst.Amount = net.Net.Abs()
            inst.Description = fmt.Sprintf("%s owes Settla %s %s",
                tenantName, formatAmount(net.Net.Abs()), net.Currency)
        }

        instructions = append(instructions, inst)
    }

    // Fee instruction (always tenant owes Settla)
    if totalFees.IsPositive() {
        roundedFees := totalFees.Round(2)
        instructions = append(instructions, SettlementInstruction{
            Direction:   "tenant_owes_settla",
            Currency:    "USD",
            Amount:      roundedFees,
            Description: fmt.Sprintf("Fees: %s USD", formatAmount(roundedFees)),
        })
    }

    return instructions
}
```

The resulting instructions for our Fincra example:

```
1. Fincra owes Settla 19.93M GBP
2. Settla owes Fincra 36.40B NGN
3. Fees: 142.50K USD (tenant owes Settla)
```

**Step 7: Persist and return**

```go
    dueDate := periodEnd.AddDate(0, 0, DefaultSettlementDays) // T+3 settlement
    // Snapshot the tenant's fee schedule at calculation time for audit trail reconstruction.
    feeSnapshot := tenant.FeeSchedule
    settlement := &NetSettlement{
        ID:                  uuid.New(),
        TenantID:            tenantID,
        TenantName:          tenant.Name,
        PeriodStart:         periodStart,
        PeriodEnd:           periodEnd,
        Corridors:           corridors,
        NetByCurrency:       netByCurrency,
        TotalFeesUSD:        totalFees,
        Instructions:        instructions,
        FeeScheduleSnapshot: &feeSnapshot,
        Status:              "pending",
        DueDate:             &dueDate,
        CreatedAt:           time.Now().UTC(),
    }

    if err := c.store.CreateNetSettlement(ctx, settlement); err != nil {
        // If a settlement for this tenant+period already exists (unique constraint),
        // treat it as idempotent success.
        if strings.Contains(err.Error(), "uk_net_settlements_tenant_period") ||
            strings.Contains(err.Error(), "duplicate key") {
            return settlement, nil
        }
        return nil, fmt.Errorf("settla-settlement: persisting net settlement: %w", err)
    }

    return settlement, nil
```

Two important additions since the initial implementation:

1. **Settlement idempotency** (migration 000026): A unique index `uk_net_settlements_tenant_period` on `(tenant_id, period_start, period_end)` prevents duplicate settlements for the same tenant and period. If the scheduler runs twice for the same day (e.g., crash recovery), the second calculation is silently discarded. The calculator detects the duplicate key violation and returns success.

2. **Fee schedule snapshot**: The tenant's fee schedule at calculation time is stored with the settlement record. If the fee schedule is later renegotiated, the original settlement's fees can still be verified against the rates in effect when it was calculated.

> **Key Insight:** The T+3 due date means the tenant has 3 days after the settlement period ends to fulfill their obligations. This is standard in financial markets (equities use T+2, FX historically used T+2, some markets still use T+3). The due date drives the overdue escalation system.

---

## The Scheduler

**File:** `core/settlement/scheduler.go`

### Daily Cycle

The scheduler runs daily, calculating the previous day's settlements for all NET_SETTLEMENT tenants:

```go
func (s *Scheduler) tick(ctx context.Context) error {
    // Yesterday's window: 00:00 UTC to 00:00 UTC
    now := time.Now().UTC()
    periodEnd := now.Truncate(24 * time.Hour)        // today 00:00 UTC
    periodStart := periodEnd.Add(-24 * time.Hour)     // yesterday 00:00 UTC

    // Calculate settlements for all tenants
    if err := s.calculateForAllTenants(ctx, periodStart, periodEnd); err != nil {
        return err
    }

    // Check for overdue settlements
    actions, err := s.checkOverdue(ctx, now)
    // ...

    settlementLastSuccess.SetToCurrentTime()
    return nil
}
```

The settlement period is always midnight-to-midnight UTC. This means the scheduler at 00:30 UTC on March 15 calculates settlements for March 14 00:00 to March 15 00:00.

### Tenant Processing with Cursor Pagination and Worker Pool

At scale (20K-100K tenants), loading all tenant IDs into memory and processing them serially is too slow. The scheduler uses cursor-based pagination to stream tenant IDs and a bounded worker pool for concurrent processing:

```go
const (
    defaultBatchSize int32 = 500       // tenant IDs fetched per page
    defaultWorkers         = 32        // concurrent settlement goroutines
    perTenantTimeout       = 60 * time.Second
)

func (s *Scheduler) calculateForAllTenants(ctx context.Context, periodStart, periodEnd time.Time) error {
    totalTenants, _ := s.tenantStore.CountActiveTenantsBySettlementModel(
        ctx, domain.SettlementModelNetSettlement)

    // Worker pool: feed tenant IDs through a channel
    tenantCh := make(chan uuid.UUID, defaultBatchSize)
    var failCount atomic.Int64

    var wg sync.WaitGroup
    for range defaultWorkers {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for tenantID := range tenantCh {
                tenantCtx, cancel := context.WithTimeout(ctx, perTenantTimeout)
                _, err := s.calculator.CalculateNetSettlement(tenantCtx, tenantID, periodStart, periodEnd)
                cancel()
                if err != nil {
                    failCount.Add(1)
                }
            }
        }()
    }

    // Producer: cursor-paginate through tenant IDs
    var afterID uuid.UUID // uuid.Nil for first page
    for {
        ids, err := s.tenantStore.ListActiveTenantIDsBySettlementModel(
            ctx, domain.SettlementModelNetSettlement, defaultBatchSize, afterID)
        if err != nil {
            close(tenantCh)
            wg.Wait()
            return err
        }

        for _, id := range ids {
            tenantCh <- id
        }

        if int32(len(ids)) < defaultBatchSize {
            break // last page
        }
        afterID = ids[len(ids)-1] // cursor = last ID from this batch
    }

    close(tenantCh)
    wg.Wait()
    // ... log results
    return nil
}
```

Three important patterns here:

1. **Cursor-based pagination:** Instead of loading all tenant IDs at once (which would consume O(N) memory), the producer fetches batches of 500 IDs using `ListActiveTenantIDsBySettlementModel` with a cursor (`afterID`). This keeps memory bounded regardless of tenant count.

2. **Bounded worker pool:** 32 concurrent goroutines process settlements in parallel. Each worker has a 60-second per-tenant timeout to prevent a single slow tenant from blocking the pool.

3. **Continue on per-tenant failure:** If Fincra's settlement fails but Lemfi's succeeds, the scheduler does not skip Lemfi. Individual failures are counted and logged but do not abort the batch. The settlement's unique constraint (migration 000026) ensures idempotent retries on the next cycle.

### Settlement Cycle States

```
                SETTLEMENT LIFECYCLE

    +----------+     +----------+     +----------+
    |          |     |          |     |          |
    | PENDING  +---->+ SETTLED  |     | OVERDUE  |
    |          |     |          |     |          |
    +----+-----+     +----------+     +-----+----+
         |                                  |
         |         (due date passes         |
         +--------> without payment)  ------+
                                            |
                                    +-------+--------+
                                    |                |
                                    v                v
                              3d: reminder     7d: suspend
                              5d: warning
```

---

## Overdue Escalation

**File:** `core/settlement/scheduler.go`

Overdue settlements trigger escalating actions based on how far past the due date they are:

```go
var OverdueThresholds = struct {
    Reminder time.Duration // 3 days: send reminder
    Warning  time.Duration // 5 days: send warning
    Suspend  time.Duration // 7 days: suspend tenant
}{
    Reminder: 3 * 24 * time.Hour,
    Warning:  5 * 24 * time.Hour,
    Suspend:  7 * 24 * time.Hour,
}
```

The `checkOverdue` method examines all pending settlements:

```go
func (s *Scheduler) checkOverdue(ctx context.Context, now time.Time) ([]OverdueAction, error) {
    pending, err := s.calculator.store.ListPendingSettlements(ctx)
    // ...

    var actions []OverdueAction
    for _, settlement := range pending {
        if settlement.DueDate == nil {
            continue
        }

        overdue := now.Sub(*settlement.DueDate)
        if overdue <= 0 {
            continue // not yet overdue
        }

        daysPastDue := int(overdue.Hours() / 24)

        var action string
        switch {
        case overdue >= OverdueThresholds.Suspend:
            action = "suspend"
            // Also update settlement status to "overdue"
            s.calculator.store.UpdateSettlementStatus(ctx, settlement.ID, "overdue")
        case overdue >= OverdueThresholds.Warning:
            action = "warning"
        case overdue >= OverdueThresholds.Reminder:
            action = "reminder"
        default:
            continue // less than 3 days, no action
        }

        actions = append(actions, OverdueAction{
            SettlementID: settlement.ID.String(),
            TenantID:     settlement.TenantID.String(),
            TenantName:   settlement.TenantName,
            Action:       action,
            DaysPastDue:  daysPastDue,
        })
    }

    return actions, nil
}
```

The escalation timeline for a settlement due March 18:

| Date | Days Past Due | Action |
|------|--------------|--------|
| March 18 | 0 | No action |
| March 19-20 | 1-2 | No action |
| March 21 | 3 | Reminder sent |
| March 22 | 4 | Reminder (repeated) |
| March 23 | 5 | Warning sent |
| March 24 | 6 | Warning (repeated) |
| March 25+ | 7+ | Tenant suspended, status set to "overdue" |

> **Key Insight:** Suspension at day 7 is a business decision, not a technical one. A suspended tenant's API keys still work, but new transfer creation is blocked. Existing in-flight transfers continue to completion. This protects Settla from increasing credit exposure to a non-paying tenant while avoiding disruption to end users with active transfers.

### Prometheus Success Metric

```go
var settlementLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
    Name: "settla_settlement_last_success_timestamp",
    Help: "Unix timestamp of the last successful settlement scheduler tick.",
})
```

After each successful tick, the scheduler records the current timestamp. A Prometheus alert fires if this timestamp is more than 25 hours old, indicating the scheduler missed a cycle.

---

## Amount Formatting

A small but important detail: settlement instructions format large amounts with abbreviations for readability:

```go
func formatAmount(amount decimal.Decimal) string {
    billion := decimal.NewFromInt(1_000_000_000)
    million := decimal.NewFromInt(1_000_000)
    thousand := decimal.NewFromInt(1_000)

    if amount.GreaterThanOrEqual(billion) {
        return amount.Div(billion).StringFixed(2) + "B"
    }
    if amount.GreaterThanOrEqual(million) {
        return amount.Div(million).StringFixed(2) + "M"
    }
    if amount.GreaterThanOrEqual(thousand) {
        return amount.Div(thousand).StringFixed(0) + "K"
    }
    return amount.StringFixed(2)
}
```

This turns `NGN 36,400,000,000` into `NGN 36.40B` in settlement instruction descriptions, making them digestible for operations teams reviewing dozens of settlements daily.

---

## Serialization for Storage

The settlement record contains complex nested structures (corridors, nets, instructions) that are stored as JSONB in PostgreSQL:

```go
func MarshalCorridors(corridors []CorridorPosition) ([]byte, error) {
    return json.Marshal(corridors)
}

func MarshalNetByCurrency(nets []CurrencyNet) ([]byte, error) {
    return json.Marshal(nets)
}

func MarshalInstructions(instructions []SettlementInstruction) ([]byte, error) {
    return json.Marshal(instructions)
}
```

JSONB storage allows PostgreSQL to index into the settlement data for queries like "find all settlements where Settla owes the tenant more than $1M in GBP."

---

## Common Mistakes

1. **Including non-COMPLETED transfers in the settlement.** Only COMPLETED transfers should be netted. Including FAILED or IN_PROGRESS transfers would produce incorrect net positions. The query `ListCompletedTransfersByPeriod` enforces this at the data layer.

2. **Forgetting the fee instruction.** Fees are a separate settlement instruction, always with direction `tenant_owes_settla`. Omitting fees from the instructions would mean Settla does not collect revenue on netted transfers.

3. **Using local time instead of UTC for period boundaries.** The settlement period is midnight-to-midnight UTC. Using local time would create overlapping or gapped periods depending on the server's timezone, leading to transfers being counted in two settlements or none.

4. **Not handling the zero-net case.** If a currency's inflows exactly equal its outflows, the net is zero and no instruction is generated. This is correct behavior: a perfectly balanced corridor needs no settlement.

5. **Skipping inactive tenants without logging.** The scheduler explicitly logs skipped inactive tenants. Silent skipping would make it impossible to debug why a tenant's settlement was not calculated.

---

## Exercises

1. **Multi-corridor netting:** A tenant has these corridors: GBP->NGN, NGN->GBP, GBP->KES, KES->GBP. Draw the corridor positions and compute the net per currency. How many settlement instructions are generated?

2. **T+3 edge case:** If the settlement scheduler is down for 48 hours and misses two cycles, what happens when it comes back up? Does it calculate settlements for the missed periods? (Hint: look at how `periodStart` and `periodEnd` are calculated.)

3. **Fee reconciliation link:** Chapter 7.1 describes the SettlementFeeCheck that compares `net_settlements.total_fees_usd` against the sum of per-transfer fees. If a transfer is retroactively marked COMPLETED after the settlement was calculated, the fee check will fail. How would you handle this?

4. **Suspension recovery:** A tenant is suspended for non-payment (7 days overdue). They then pay. Design the workflow to unsuspend them: what status transitions are needed in the settlement and tenant records?

---

## What's Next

Chapter 7.5 covers database maintenance at scale: how partition management, vacuum scheduling, and capacity monitoring keep Settla's databases performing at 50M rows/day without degradation.
