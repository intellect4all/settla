# Chapter 8.2: Load Testing

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why Settla uses a custom Go load test harness instead of k6 or Locust
2. Trace the four-phase load test lifecycle: ramp-up, sustained peak, drain, verify
3. Read and interpret the live metrics dashboard output
4. Run all five named load test scenarios and understand what each proves
5. Understand the post-test verification pipeline (outbox drain, ledger balance, stuck transfers)

---

## Why a Custom Go Harness

Most teams reach for k6, Locust, or Gatling. Settla chose a custom Go harness
for three reasons specific to settlement infrastructure:

```
+-------------------+---------------------+------------------------------+
| Requirement       | k6 / Locust         | Custom Go Harness            |
+-------------------+---------------------+------------------------------+
| Decimal math      | Float-only          | shopspring/decimal            |
| Async polling     | Complex scripting   | Native goroutines             |
| DB verification   | External script     | pgx in same binary            |
| Rate control      | Token bucket        | golang.org/x/time/rate        |
| Memory tracking   | External process    | runtime.ReadMemStats in-proc  |
+-------------------+---------------------+------------------------------+
```

The critical gap is **decimal precision**. k6 uses JavaScript floats internally.
A settlement system that verifies "debits equal credits" to 8 decimal places
cannot tolerate IEEE 754 rounding. The custom harness uses `shopspring/decimal`
end-to-end -- the same library the production code uses.

---

## Architecture of the Load Test Harness

The harness lives in `tests/loadtest/` with this structure:

```
tests/loadtest/
  main.go           -- Entry point, flag parsing, phase orchestration
  scenarios.go      -- Named scenario definitions (PeakLoad, BurstRecovery, etc.)
  metrics.go        -- Real-time metrics collection (latency histograms, counters)
  soak.go           -- Soak test wrapper (health monitoring, profile capture)
  verifier.go       -- Post-test DB consistency verification
  report.go         -- Report generation
  results.go        -- Structured JSON result output + aggregate reporting
  seed_tenants.go   -- Bulk tenant provisioning for scale tests (batches of 1000)
  tenant_scale.go   -- Scale tenant generation with currency mix distribution
  zipf.go           -- Zipf distribution for realistic tenant traffic patterns
```

### Core Types

```go
// LoadTestConfig configures a load test run.
type LoadTestConfig struct {
    GatewayURL     string
    Tenants        []TenantConfig
    TargetTPS      int
    Duration       time.Duration
    RampUpDuration time.Duration
    DrainDuration  time.Duration
    TransferDBURL  string  // Optional: for post-test outbox/stuck checks
    LedgerDBURL    string  // Optional: for debit=credit balance check
    MaxErrorRate   float64 // Maximum acceptable error rate (default 1.0%)
}

// LoadTestRunner orchestrates the load test.
type LoadTestRunner struct {
    config     LoadTestConfig
    metrics    *LoadTestMetrics
    logger     *slog.Logger
    client     *http.Client
    stopCh     chan struct{}
    wg         sync.WaitGroup
    inflight   atomic.Int64
    transferCh chan TransferResult
    startTime  time.Time
}
```

---

## The Four Phases

Every load test runs through four sequential phases:

```
Phase 1: Ramp-Up         Phase 2: Sustained Peak    Phase 3: Drain      Phase 4: Verify
  0 --> target TPS        Hold at target TPS         Wait for in-flight   Check DB consistency
                                                     to complete

  TPS
  ^
  |                  +--------------------------+
  |                 /                            \
  |                /                              \
  |               /                                +--------+
  |              /                                           |
  +-----------+/                                             +--------> time
  |  ramp-up  |        sustained peak             |  drain  | verify
  |   30s     |       (configurable)              |  60s    |
```

### Phase 1: Ramp-Up

The ramp-up phase gradually increases load from 0 to the target TPS over
30 seconds (configurable). This serves two purposes: warming caches and
connection pools, and verifying the system can handle progressive load
without cliff failures.

```go
func (r *LoadTestRunner) phaseRampUp(ctx context.Context) error {
    r.logger.Info("phase 1: ramp-up", "duration", r.config.RampUpDuration)

    rampCtx, rampCancel := context.WithTimeout(ctx, r.config.RampUpDuration)
    defer rampCancel()

    // Start with 1 TPS; workers block on limiter until rate increases.
    limiter := rate.NewLimiter(1, 1)

    // Cap ramp workers so we don't spawn thousands at high TPS targets.
    numWorkers := r.config.TargetTPS / 20
    if numWorkers < 5 {
        numWorkers = 5
    }
    if numWorkers > 100 {
        numWorkers = 100
    }

    var rampWg sync.WaitGroup
    for i := 0; i < numWorkers; i++ {
        rampWg.Add(1)
        go func() {
            defer rampWg.Done()
            for {
                select {
                case <-rampCtx.Done():
                    return
                default:
                }
                if err := limiter.Wait(rampCtx); err != nil {
                    return
                }
                r.executeTransferFlow(rampCtx)
            }
        }()
    }

    // Gradually increase the rate once per second.
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()
    start := time.Now()
    for {
        select {
        case <-rampCtx.Done():
            rampWg.Wait()
            r.logger.Info("ramp-up complete")
            return nil
        case <-ticker.C:
            elapsed := time.Since(start)
            progress := float64(elapsed) / float64(r.config.RampUpDuration)
            currentTPS := int(float64(r.config.TargetTPS) * progress)
            if currentTPS < 1 {
                currentTPS = 1
            }
            limiter.SetLimit(rate.Limit(currentTPS))
            limiter.SetBurst(currentTPS)
        }
    }
}
```

**Key Insight:** The worker count is capped at 100 regardless of the target TPS.
The rate limiter does the throttling, not the goroutine count. At 5,000 TPS with
100 workers, each worker handles ~50 requests/second. This avoids the overhead of
spawning 5,000 goroutines (one per TPS), which would waste memory on goroutine
stacks.

### Phase 2: Sustained Peak

The sustained phase maintains the target TPS for the configured duration.
Workers are governed by a `rate.Limiter` set to the exact target:

```go
func (r *LoadTestRunner) phaseSustainedPeak(ctx context.Context) error {
    // Create rate limiter for target TPS
    limiter := rate.NewLimiter(rate.Limit(r.config.TargetTPS), r.config.TargetTPS)

    ctx, cancel := context.WithTimeout(ctx, r.config.Duration)
    defer cancel()

    // Launch worker pool: 1 worker per 10 TPS
    for i := 0; i < r.config.TargetTPS/10; i++ {
        r.wg.Add(1)
        go r.transferWorker(ctx, limiter)
    }

    <-ctx.Done()
    close(r.stopCh)
    r.wg.Wait()
    return nil
}
```

### The Transfer Flow

Each transfer follows the complete API lifecycle: create quote, create transfer,
then poll asynchronously until terminal state:

```go
func (r *LoadTestRunner) executeTransferFlow(ctx context.Context) {
    r.inflight.Add(1)

    tenant := r.config.Tenants[rand.Intn(len(r.config.Tenants))]

    // Step 1: Create quote
    quoteStart := time.Now()
    quote, err := r.createQuote(ctx, tenant)
    if err != nil {
        r.inflight.Add(-1)
        r.metrics.RecordError(categorizeError(err))
        return
    }
    r.metrics.RecordQuoteLatency(time.Since(quoteStart))

    // Step 2: Create transfer
    createStart := time.Now()
    transfer, err := r.createTransfer(ctx, tenant, quote)
    if err != nil {
        r.inflight.Add(-1)
        r.metrics.RecordError(categorizeError(err))
        return
    }
    r.metrics.RecordCreateLatency(time.Since(createStart))
    r.metrics.TransfersCreated.Add(1)

    // Step 3: Poll asynchronously (does not block the worker)
    start := time.Now()
    go func() {
        defer r.inflight.Add(-1)

        pollCtx, pollCancel := context.WithTimeout(
            context.Background(), 90*time.Second)
        defer pollCancel()

        status, err := r.pollTransfer(pollCtx, tenant, transfer.ID)
        // ... record metrics and status ...
    }()
}
```

**Key Insight:** Polling happens in a separate goroutine with its own context.
The worker goroutine is free to create the next transfer immediately. This
decouples creation throughput from polling latency -- critical because polling
involves 1-second intervals waiting for async processing to complete.

### Phase 3: Drain

After the sustained phase completes, the harness waits for all in-flight
transfers to reach a terminal state:

```go
func (r *LoadTestRunner) phaseDrain(ctx context.Context) error {
    ctx, cancel := context.WithTimeout(ctx, r.config.DrainDuration)
    defer cancel()

    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            inflight := r.inflight.Load()
            if inflight > 0 {
                return fmt.Errorf(
                    "drain timeout with %d transfers still in-flight", inflight)
            }
            return nil
        case <-ticker.C:
            inflight := r.inflight.Load()
            r.logger.Info("draining", "inflight", inflight)
            if inflight == 0 {
                return nil
            }
        }
    }
}
```

### Phase 4: Verification

The verification phase runs five consistency checks against the actual databases:

```go
func (v *Verifier) VerifyConsistency(ctx context.Context) (*VerificationReport, error) {
    report := &VerificationReport{}

    // Check 1: All transfers in terminal state (API-level)
    v.checkTransferStates(results, report)

    // Check 2: Error rate within threshold
    v.checkErrorRate(results, report)

    // Check 3: Outbox fully drained (zero unpublished entries)
    v.checkOutboxDrained(ctx, report)

    // Check 4: No stuck transfers in DB
    v.checkDBStuckTransfers(ctx, report)

    // Check 5: Treasury positions + orphaned reservations
    v.checkTreasuryPositions(ctx, results, report)

    // Check 6: Ledger debit = credit balance
    v.checkLedgerBalance(ctx, report)

    report.AllPassed = report.TransfersPass &&
        report.ErrorRatePass && report.OutboxPass &&
        report.DBStuckPass && report.TreasuryPass &&
        report.LedgerPass && report.ReservationsPass

    return report, nil
}
```

The outbox drain check verifies the most critical invariant -- that every
outbox entry written by the engine was published to NATS:

```go
func (v *Verifier) checkOutboxDrained(ctx context.Context, report *VerificationReport) {
    conn, err := pgx.Connect(ctx, v.config.TransferDBURL)
    // ...

    var unpublished int64
    err = conn.QueryRow(ctx,
        `SELECT COUNT(*) FROM outbox WHERE published = false`).Scan(&unpublished)

    if unpublished == 0 {
        report.OutboxPass = true
        report.OutboxMessage = "outbox fully drained (0 unpublished entries)"
    } else {
        report.OutboxPass = false
        report.OutboxMessage = fmt.Sprintf(
            "%d unpublished outbox entries remain", unpublished)
    }
}
```

The ledger balance check verifies the double-entry bookkeeping invariant:

```go
func (v *Verifier) checkLedgerBalanceViaDB(ctx context.Context, report *VerificationReport) {
    conn, err := pgx.Connect(ctx, v.config.LedgerDBURL)
    // ...

    err = conn.QueryRow(ctx, `
        SELECT
            COALESCE(SUM(CASE WHEN entry_type = 'DEBIT'  THEN amount ELSE 0 END), 0)::text,
            COALESCE(SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE 0 END), 0)::text
        FROM entry_lines
        WHERE created_at > now() - INTERVAL '2 hours'
    `).Scan(&debitStr, &creditStr)

    imbalance := totalDebits.Sub(totalCredits).Abs()
    if imbalance.IsZero() {
        report.LedgerPass = true
        report.LedgerMessage = fmt.Sprintf(
            "debits = credits = %s (balanced)", totalDebits.StringFixed(2))
    }
}
```

---

## The Named Scenarios

Settla defines multiple predefined scenarios in `tests/loadtest/scenarios.go`,
each accessed via a dedicated Makefile target:

### 1. PeakLoad -- Proving 50M/day Capacity

```bash
make loadtest
# Equivalent to:
go run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=10 -drain=300s
```

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| TPS | 5,000 | Peak expected throughput |
| Duration | 10 minutes | Long enough to stress connection pools |
| Tenants | 2 (Lemfi + Fincra) | Realistic corridor split |
| Expected transfers | ~3,000,000 | Extrapolates to 50M/day at 580 TPS sustained |

### 2. QuickLoad -- CI-Friendly Validation

```bash
make loadtest-quick
# go run ./tests/loadtest/ -tps=1000 -duration=2m -tenants=10 -drain=60s
```

A fast, CI-feasible smoke test at 1,000 TPS for 2 minutes. Runs the same
four-phase lifecycle and verification pipeline, just at reduced throughput.

### 3. SustainedLoad -- Steady-State Validation

```bash
make loadtest-sustained
# go run ./tests/loadtest/ -tps=600 -duration=30m -tenants=10 -drain=300s
```

Validates the average daily throughput (580 TPS) for 30 minutes. Expected
transfers: ~1,080,000.

### 4. BurstRecovery -- Spike Handling

```bash
make loadtest-burst
# go run ./tests/loadtest/ -tps=8000 -duration=5m -tenants=20 -rampup=2m -drain=300s
```

Ramps from 600 TPS to 8,000 TPS over 2 minutes, sustains for 5 minutes,
then ramps back down to 600 TPS. Tests whether the system recovers cleanly
after the spike ends (verified by the post-test consistency checks -- no
stuck transfers, outbox drained). This is the burst recovery proof: can
the system absorb a 13x traffic spike and return to steady state?

### 5. SingleTenantFlood -- Treasury Hot-Key Safety

```bash
make loadtest-flood
# go run ./tests/loadtest/ -tps=3000 -duration=5m -tenants=1 -drain=300s
```

All 3,000 TPS from a single tenant. This is the treasury hot-key stress test:
3,000 concurrent reservations on the same tenant position must not over-reserve
or deadlock. The in-memory CAS loop must handle this without contention collapse.

### 6. MultiTenantScale -- Tenant Isolation at Scale

```bash
make loadtest-multi
# go run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=50 -drain=300s
```

50 tenants each at 100 TPS for combined 5,000 TPS. Proves per-tenant isolation:
no cross-tenant data leakage, independent rate limits, independent treasury
positions.

### Soak Tests -- Long-Running Stability

```bash
make soak          # 2-hour soak test at 1,000 TPS
make soak-short    # 15-minute soak test at 1,000 TPS (CI-feasible)
```

Soak tests extend load testing with continuous health monitoring to detect
resource leaks over time (see the Soak Testing section below).

### Full Benchmark Report

```bash
make report
```

Runs the complete benchmark suite: component benchmarks (`make bench`),
a quick load test (`make loadtest-quick`), and a short soak test
(`make soak-short`). Results are collected in `tests/loadtest/results/`
as structured JSON and aggregated into a single report file.

---

## Metrics Collection

The harness collects real-time metrics using lock-free atomics and a bounded
histogram:

```go
type LoadTestMetrics struct {
    // Throughput counters
    RequestsTotal      atomic.Int64
    TransfersCreated   atomic.Int64
    TransfersCompleted atomic.Int64
    TransfersFailed    atomic.Int64
    PeakInflight       atomic.Int64
    CurrentTPS         atomic.Int64
    PeakTPS            atomic.Int64

    // Latency histograms (capped at 100K samples)
    quoteLatency    *LatencyHistogram
    createLatency   *LatencyHistogram
    pollLatency     *LatencyHistogram
    endToEndLatency *LatencyHistogram

    // Error tracking by category
    errors   map[string]*atomic.Int64
    errorsMu sync.RWMutex
}
```

The histogram caps at 100,000 samples to avoid sorting 3M entries on every
metrics print:

```go
const maxHistogramCap = 100_000

func (h *LatencyHistogram) Record(duration time.Duration) {
    micros := duration.Microseconds()
    h.mu.Lock()
    if len(h.buckets) < maxHistogramCap {
        h.buckets = append(h.buckets, micros)
    }
    h.mu.Unlock()
}
```

### Live Dashboard Output

Every 5 seconds the harness prints a dashboard:

```
[02:30] TPS: 4987/5000 | Created: 748050 | Completed: 745210 | Failed: 12
Quote  p50: 3ms p95: 8ms p99: 15ms
Create p50: 5ms p95: 12ms p99: 25ms
E2E    p50: 2s  p95: 4s  p99: 8s
Errors: (none)
Memory: 245MB | Goroutines: 1847
```

### Error Categorization

Errors are classified by their root cause for easier diagnosis:

```go
func categorizeError(err error) string {
    msg := err.Error()
    switch {
    case strings.Contains(msg, "timeout"):
        return "timeout"
    case strings.Contains(msg, "http 429"):
        return "rate_limited_429"
    case strings.Contains(msg, "http 5"):
        return "server_error_5xx"
    case strings.Contains(msg, "connection refused"):
        return "connection_error"
    default:
        return "unknown"
    }
}
```

---

## Zipf Distribution for Realistic Traffic

In production, traffic is not uniformly distributed across tenants. A few
large fintechs (Lemfi, Paystack) generate the majority of volume, while
hundreds of smaller integrators contribute the long tail. Settla models this
with a Zipf distribution (`tests/loadtest/zipf.go`):

```go
// ZipfDistribution generates tenant traffic weights following Zipf's law.
// Top 1% of tenants generate ~50% of total traffic, modeling realistic
// fintech platform usage where a few large customers dominate volume.
type ZipfDistribution struct {
    weights []float64 // Normalized weights per tenant index (sum = 1.0)
    cumul   []float64 // Cumulative distribution for sampling
    n       int
    s       float64   // Zipf exponent
}
```

The exponent `s` controls skew: `s=1.0` is standard Zipf, `s=1.2` gives
heavier top-tenant concentration (~50% from top 1%). The distribution is
validated at startup:

```go
stats := zipf.Stats()
// stats.Top1PctTraffic  ≈ 0.50  (top 1% of tenants = 50% of traffic)
// stats.Top5PctTraffic  ≈ 0.72
// stats.Top10PctTraffic ≈ 0.82
```

This matters for treasury contention testing. Uniform distribution spreads
load evenly across all positions, hiding the hot-key problem. Zipf
distribution concentrates traffic on a few positions, which is the real
production pattern.

## Scale Tenant Provisioning

The multi-tenant scale test requires provisioning tenants in bulk.
`tests/loadtest/seed_tenants.go` handles this:

```go
// SeedRunner provisions tenants and treasury positions in bulk for scale tests.
// Uses batch INSERT (batches of 1000 rows) and COPY where available for speed.
// Target performance: 50 tenants < 5s, 20K tenants < 2 min, 100K tenants < 10 min.
type SeedRunner struct {
    transferDBURL string
    treasuryDBURL string
    tenantCount   int
    logger        *slog.Logger
    cleanupOnly   bool
}
```

`tests/loadtest/tenant_scale.go` generates deterministic tenant configurations
with a configurable currency mix (default: NGN 70%, USD 20%, GBP 10%):

```go
// GenerateScaleTenants creates n tenant configs with deterministic IDs and API keys.
// IDs are formatted as UUIDs: tNNNNNNN-0000-0000-0000-000000000000
// API keys are: sk_live_scale_NNNNNNN
func GenerateScaleTenants(n int, mix []CurrencyWeight) []TenantConfig
```

The scale infrastructure supports up to 20,000 tenants. Each tenant gets two
treasury positions (one for their primary currency, one for USDT on Tron).

## Structured Results

Every load test scenario produces structured JSON output via the
`ResultCollector` (`tests/loadtest/results.go`):

```go
type ScenarioResult struct {
    Scenario     string              `json:"scenario"`
    Throughput   ThroughputResult    `json:"throughput"`
    Latency      LatencyResult       `json:"latency"`
    Errors       ErrorResult         `json:"errors"`
    Verification VerificationResult  `json:"verification"`
    Thresholds   ThresholdResults    `json:"thresholds"`
    ZipfStats    *ZipfStats          `json:"zipf_stats,omitempty"`
    Passed       bool                `json:"passed"`
}
```

Results are written to `tests/loadtest/results/` as individual JSON files
per scenario. The `WriteAggregateReport` function combines all results into
a single report with a pass/fail summary. This is what `make report` produces.

---

## Soak Testing

Soak tests extend load testing with continuous health monitoring to detect
resource leaks. The soak runner wraps the standard load test with periodic
health checks:

```go
type SoakConfig struct {
    LoadTestConfig                     // Embeds base config
    CheckInterval      time.Duration   // Default: 60s
    BaselineWindow     time.Duration   // Default: 5min
    MaxMemoryGrowth    int64           // Default: 50MB
    MaxGoroutineGrowth int64           // Default: 1000
    MaxP99Degradation  float64         // Default: 2.0x
    MaxErrorRate       float64         // Default: 1%
    PprofURL           string          // Default: http://localhost:6060
}
```

Fail conditions trigger automatic test termination:

```go
func (s *SoakRunner) checkFailConditions(snapshot SoakSnapshot) string {
    // 1. Memory growth exceeds 50MB
    if memGrowth > s.config.MaxMemoryGrowth { return "..." }

    // 2. Goroutine growth exceeds 1000
    if goroutineGrowth > s.config.MaxGoroutineGrowth { return "..." }

    // 3. PgBouncer waiting clients sustained >60s
    if snapshot.PgBouncerWaitingClients > 10 && sustained > 60s { return "..." }

    // 4. p99 latency degradation > 2x baseline
    if degradation > s.config.MaxP99Degradation { return "..." }

    // 5. Error rate sustained >1% for >60s
    if snapshot.ErrorRate > s.config.MaxErrorRate && sustained > 60s { return "..." }

    return "" // All checks passed
}
```

Run soak tests with:

```bash
make soak        # 2-hour soak test at 1,000 TPS
make soak-short  # 15-minute soak test (CI-feasible)
```

---

## Common Mistakes

### Mistake 1: Using Float for Monetary Amounts in Load Tests

The custom harness uses `decimal.Decimal` for all monetary amounts:

```go
func randomAmount(currency string) decimal.Decimal {
    switch currency {
    case "NGN":
        amount := 10000 + rand.Intn(990000)
        return decimal.NewFromInt(int64(amount))
    default:
        amount := 100 + rand.Intn(9900)
        return decimal.NewFromInt(int64(amount))
    }
}
```

Never use `float64` for amounts -- verification would fail with false imbalances.

### Mistake 2: Not Draining Before Verification

Skipping the drain phase means transfers are still in-flight when verification
runs. The verifier would incorrectly report stuck transfers.

### Mistake 3: Tight Polling Without Backoff

The harness polls at 1-second intervals, not as fast as possible:

```go
for attempt := 0; attempt < maxAttempts; attempt++ {
    // ...check status...
    time.Sleep(pollInterval)  // 1 second
}
```

Aggressive polling would flood the gateway with GET requests, consuming
capacity that should be available for new transfer creation.

---

## Exercises

1. **Run the quick load test locally.** Execute `make loadtest-quick` against
   your local Docker Compose environment. Examine the verification report --
   did all checks pass? If not, which invariant failed?

2. **Add a latency SLO check.** Modify the verifier to fail if p99 end-to-end
   latency exceeds 10 seconds. This catches the scenario where transfers
   complete but take too long.

3. **Create a new scenario.** Define a `CorridorImbalance` scenario with 80%
   GBP-to-NGN and 20% NGN-to-GBP traffic. Does the system handle the
   imbalanced corridor split?

---

## What's Next

Load tests prove the system handles expected throughput. But what happens when
infrastructure fails? Chapter 8.3 introduces chaos engineering -- systematically
injecting failures to prove the system recovers without data loss.
