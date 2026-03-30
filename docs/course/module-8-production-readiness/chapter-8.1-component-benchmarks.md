# Chapter 8.1: Component Benchmarks

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Write Go benchmarks that measure latency, throughput, and memory allocations
2. Understand how Settla's 76 benchmark targets map to the 50M transactions/day requirement
3. Read benchmark output (ns/op, B/op, allocs/op) and identify performance regressions
4. Build mock stores that eliminate I/O from benchmarks to isolate pure computation
5. Use `b.RunParallel` to measure concurrent throughput under contention

---

## Why Component Benchmarks Matter

Settla's capacity requirement is 50M transactions/day, which breaks down to:

```
50,000,000 txns / 86,400 seconds = ~580 TPS sustained
Peak: 3,000-5,000 TPS (10x burst multiplier)
Ledger: 15,000-25,000 writes/sec at peak
Treasury: ~50 hot positions under constant concurrent pressure
```

Before you can prove the system handles this load end-to-end (Chapter 8.2), you need
to prove each component handles its share. Component benchmarks are the foundation
of the capacity proof pyramid:

```
                    +-------------------+
                    |   Load Test       |  Chapter 8.2
                    |   (end-to-end)    |
                    +---------+---------+
                              |
                    +---------+---------+
                    | Integration Tests |  Chapter 8.6
                    |  (cross-module)   |
                    +---------+---------+
                              |
              +---------------+---------------+
              |     Component Benchmarks      |  <-- YOU ARE HERE
              |   (isolated, deterministic)   |
              +-------------------------------+
```

If a single component cannot meet its target in isolation (with zero I/O), it
certainly cannot meet it under real-world conditions with network latency, disk
I/O, and contention from other modules.

---

## Go Benchmarking Methodology

### The Testing.B Contract

Go benchmarks use `testing.B` which provides three critical capabilities:

```
b.N           -- Go increases this automatically until timing is stable
b.ReportAllocs() -- Tracks heap allocations per operation
b.ResetTimer()   -- Excludes setup time from measurement
b.RunParallel()  -- Measures throughput under GOMAXPROCS goroutines
```

### The Benchmark Structure Pattern

Every Settla benchmark follows the same four-step pattern:

```
1. Setup:      Create engine/router/treasury with mock stores
2. Warm:       Pre-populate any required data (transfers, positions)
3. Reset:      Call b.ResetTimer() to exclude setup
4. Measure:    Loop b.N times over the hot path
```

---

## Building Mock Stores for Benchmarks

Benchmarks must isolate the component under test. Settla achieves this with
in-memory stores that implement the same interfaces as production stores but
eliminate all I/O. Here is the actual benchmark transfer store from
`core/bench_test.go`:

```go
// benchTransferStore is a thread-safe in-memory transfer store for benchmarks.
type benchTransferStore struct {
    mu         sync.RWMutex
    transfers  map[uuid.UUID]*domain.Transfer
    idempotent map[string]*domain.Transfer
}

func newBenchTransferStore() *benchTransferStore {
    return &benchTransferStore{
        transfers:  make(map[uuid.UUID]*domain.Transfer),
        idempotent: make(map[string]*domain.Transfer),
    }
}

func (s *benchTransferStore) CreateTransfer(_ context.Context, t *domain.Transfer) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if t.ID == uuid.Nil {
        t.ID = uuid.New()
    }
    if t.CreatedAt.IsZero() {
        t.CreatedAt = time.Now().UTC()
    }
    t.UpdatedAt = t.CreatedAt
    t.Version = 1
    s.transfers[t.ID] = t
    if t.IdempotencyKey != "" {
        s.idempotent[fmt.Sprintf("%s:%s", t.TenantID, t.IdempotencyKey)] = t
    }
    return nil
}

func (s *benchTransferStore) TransitionWithOutbox(
    _ context.Context,
    transferID uuid.UUID,
    newStatus domain.TransferStatus,
    expectedVersion int64,
    _ []domain.OutboxEntry,
) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    t, ok := s.transfers[transferID]
    if !ok {
        return domain.ErrTransferNotFound(transferID.String())
    }
    if t.Version != expectedVersion {
        return domain.ErrOptimisticLock("transfer", transferID.String())
    }
    t.Status = newStatus
    t.Version++
    return nil
}
```

**Key Insight:** The mock store uses `sync.RWMutex` rather than no synchronization.
This is intentional -- benchmarks must reflect real contention patterns. If the
engine holds locks during operations, the benchmark must measure lock contention
too. A benchmark with no synchronization would produce falsely optimistic numbers
that do not predict production behavior.

### Engine Setup for Benchmarks

The engine constructor wires only the minimal dependencies:

```go
func setupBenchmarkEngine(b *testing.B) *Engine {
    b.Helper()

    tenant := activeTenant()
    transfers := newBenchTransferStore()
    tenants := &mockTenantStore{
        getFn: func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
            if tenantID == tenant.ID {
                return tenant, nil
            }
            return nil, domain.ErrTenantNotFound(tenantID.String())
        },
    }
    router := &mockRouter{}

    logger := slog.New(slog.NewTextHandler(os.Stderr,
        &slog.HandlerOptions{Level: slog.LevelError}))
    engine := NewEngine(transfers, tenants, router, logger, nil)

    return engine
}
```

Notice: the logger level is set to `slog.LevelError` to prevent log output from
contaminating benchmark timings. In production the logger is `slog.LevelInfo`,
but benchmark output should measure computation, not I/O.

---

## The 76 Benchmark Targets

Settla defines 76 benchmark targets across four modules. Here are the key
benchmarks organized by component:

### Engine Benchmarks (core/bench_test.go)

| Benchmark | Target | What It Measures |
|-----------|--------|------------------|
| `BenchmarkCreateTransfer` | <100us | Tenant lookup + validation + quote + persistence |
| `BenchmarkCreateTransferConcurrent` | >10,000/sec | Creation throughput under GOMAXPROCS contention |
| `BenchmarkFundTransfer` | <50us | State transition: CREATED -> FUNDED |
| `BenchmarkInitiateOnRamp` | <100us | State transition: FUNDED -> ON_RAMPING |
| `BenchmarkProcessTransfer_FullPipeline` | <500us | Complete synchronous pipeline (all state transitions) |
| `BenchmarkProcessTransferConcurrent` | >2,000/sec | Full pipeline throughput under load |
| `BenchmarkGetTransfer` | <10us | Single transfer retrieval |
| `BenchmarkGetQuote` | <100us | Quote generation (routing + fee calculation) |
| `BenchmarkTransferStateTransition` | <1us | Pure state machine transition (no I/O) |
| `BenchmarkEngineWithIdempotency` | <150us | Engine with idempotency cache lookup |
| `BenchmarkListTransfers` | <10ms | List 100 transfers with pagination |

Here is the actual full pipeline benchmark:

```go
// BenchmarkProcessTransfer_FullPipeline measures complete synchronous pipeline.
// Create -> Fund -> OnRamp -> HandleOnRampResult -> HandleSettlementResult ->
//   HandleOffRampResult
//
// Target: <500us per full pipeline (excludes real provider delays)
func BenchmarkProcessTransfer_FullPipeline(b *testing.B) {
    engine := setupBenchmarkEngine(b)
    ctx := context.Background()
    tenant := activeTenant()

    transferIDs := make([]uuid.UUID, 100)
    for i := 0; i < 100; i++ {
        req := validRequest()
        req.IdempotencyKey = fmt.Sprintf("idem-pipeline-%d", i)
        transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
        if err != nil {
            b.Fatalf("CreateTransfer: %v", err)
        }
        transferIDs[i] = transfer.ID
    }

    b.ReportAllocs()
    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        transferID := transferIDs[i%100]
        _ = engine.ProcessTransfer(ctx, tenant.ID, transferID)
    }
}
```

**Why 100 pre-created transfers?** The benchmark cycles through them with `i%100`
to avoid measuring the same transfer repeatedly (which would benefit from CPU
cache warming and produce unrealistically fast numbers).

### State Machine Transition Benchmark

The lowest-level benchmark isolates the pure domain logic:

```go
// Target: <1us per transition
func BenchmarkTransferStateTransition(b *testing.B) {
    b.ReportAllocs()
    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        transfer := &domain.Transfer{
            ID:             uuid.New(),
            TenantID:       uuid.New(),
            Status:         domain.TransferStatusCreated,
            Version:        1,
            SourceCurrency: domain.CurrencyGBP,
            SourceAmount:   decimal.NewFromInt(1000),
            DestCurrency:   domain.CurrencyNGN,
            DestAmount:     decimal.NewFromInt(2000000),
            CreatedAt:      time.Now().UTC(),
            UpdatedAt:      time.Now().UTC(),
        }

        _, _ = transfer.TransitionTo(domain.TransferStatusFunded)
        _, _ = transfer.TransitionTo(domain.TransferStatusOnRamping)
        _, _ = transfer.TransitionTo(domain.TransferStatusSettling)
        _, _ = transfer.TransitionTo(domain.TransferStatusOffRamping)
        _, _ = transfer.TransitionTo(domain.TransferStatusCompleted)
    }
}
```

This traverses the entire state machine (5 transitions) in under 1 microsecond.
At 580 TPS sustained, that means state transitions consume less than 0.06% of
a single CPU core.

### Router Benchmarks (rail/router/bench_test.go)

| Benchmark | Target | What It Measures |
|-----------|--------|------------------|
| `BenchmarkRoute` | <100us | Full route: build candidates, score, sort |
| `BenchmarkRoute_MultiChain` | <100us | Routing with multiple blockchain options |
| `BenchmarkRouteConcurrent` | >5,000/sec | Route evaluation throughput |
| `BenchmarkScoreRoute` | <1us | Scoring function in isolation |
| `BenchmarkScoreRouteConcurrent` | >100,000/sec | Scoring throughput |
| `BenchmarkGetQuote` | <200us | Quote generation through CoreRouterAdapter |
| `BenchmarkGetQuoteConcurrent` | >5,000/sec | Quote generation throughput |
| `BenchmarkRouteLargeAmount` | same as Route | Edge case: 1M GBP transfer |
| `BenchmarkRouteSmallAmount` | same as Route | Edge case: 0.01 GBP transfer |
| `BenchmarkScoreRouteVariations` | <1us each | Scoring with varied fee/amount ratios |

The scoring benchmark isolates the weighted scoring formula
(cost 40%, speed 30%, liquidity 20%, reliability 10%):

```go
// Target: <1us per route score
func BenchmarkScoreRoute(b *testing.B) {
    router, _ := setupBenchmarkRouter(b)

    onRamp := mock.NewOnRampProvider("onramp-gbp",
        []domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
        decimal.NewFromFloat(1.25), decimal.NewFromFloat(1.0), 300)

    offRamp := mock.NewOffRampProvider("offramp-ngn",
        []domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
        decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 600)

    route := Route{
        OnRamp: onRamp, OffRamp: offRamp,
        Chain: "tron", Stable: domain.CurrencyUSDT,
        OnQuote: &domain.ProviderQuote{
            Rate: decimal.NewFromFloat(1.25),
            Fee: decimal.NewFromFloat(1.0), EstimatedSeconds: 300,
        },
        OffQuote: &domain.ProviderQuote{
            Rate: decimal.NewFromFloat(830.0),
            Fee: decimal.NewFromFloat(0.5), EstimatedSeconds: 600,
        },
        GasFee: decimal.NewFromFloat(0.5),
    }
    amount := decimal.NewFromInt(1000)

    b.ReportAllocs()
    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        _, _ = router.scoreRoute(context.Background(), route, amount)
    }
}
```

### Treasury Benchmarks (treasury/bench_test.go)

| Benchmark | Target | What It Measures |
|-----------|--------|------------------|
| `BenchmarkReserve_Single` | sub-microsecond | Single-threaded reservation |
| `BenchmarkReserve_Concurrent` | sub-microsecond CAS | Concurrent atomic reservation |
| `BenchmarkReserve_Concurrent_MultiTenant` | sub-microsecond | Multi-tenant concurrent reservations |
| `BenchmarkRelease` | sub-microsecond | Release a reservation |
| `BenchmarkCommitReservation` | sub-microsecond | Commit a pending reservation |
| `BenchmarkGetPosition` | sub-microsecond | Read a single position |
| `BenchmarkFlush` | <1ms | Flush dirty positions to DB |
| `BenchmarkFlush_DirtySet` | <1ms | Flush with dirty position tracking |
| `BenchmarkReserveConcurrentContention` | sub-microsecond CAS | Maximum contention on single position |
| `BenchmarkGetLiquidityReport` | <1ms | Aggregate liquidity across positions |
| `BenchmarkUpdateBalance` | sub-microsecond | Direct balance update |
| `BenchmarkReserve_100KTenants` | sub-microsecond | Reservation with 100K tenant positions loaded |
| `BenchmarkGetPositions_100KTenants` | <1ms | Position lookup at 100K tenant scale |

The treasury benchmark is the most demanding because it measures the hot-key
contention pattern -- thousands of goroutines competing to reserve from the
same position:

```go
func BenchmarkReserve(b *testing.B) {
    tenantID := uuid.New()
    store := &mockStore{
        positions: []domain.Position{
            {
                ID:       uuid.New(),
                TenantID: tenantID,
                Currency: domain.CurrencyUSD,
                Location: "bank:chase",
                Balance:  decimal.NewFromInt(1_000_000_000), // 1 billion
                Locked:   decimal.Zero,
                MinBalance: decimal.Zero,
                UpdatedAt: time.Now().UTC(),
            },
        },
    }
    logger := slog.New(slog.NewTextHandler(io.Discard, nil))
    m := NewManager(store, nil, logger, nil)
    ctx := context.Background()
    _ = m.LoadPositions(ctx)

    amount := decimal.NewFromInt(1)

    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            _ = m.Reserve(ctx, tenantID, domain.CurrencyUSD,
                "bank:chase", amount, uuid.New())
        }
    })
}
```

**Key Insight:** The balance is set to 1 billion to ensure the benchmark measures
CAS loop performance, not insufficient-funds rejection. With `b.RunParallel`,
Go launches GOMAXPROCS goroutines that all compete on the same position --
this is exactly the hot-key contention pattern in production where 3,000 TPS
from a single tenant all hit the same treasury position.

---

## Reading Benchmark Output

Running benchmarks produces output like:

```
$ go test -bench=Benchmark -benchmem -benchtime=5s -run='^$' -count=1 ./core/...

BenchmarkCreateTransfer-12              84912   67234 ns/op   8432 B/op   142 allocs/op
BenchmarkCreateTransferConcurrent-12   204521   28910 ns/op   8448 B/op   142 allocs/op
BenchmarkGetTransfer-12              12847293     467 ns/op     48 B/op     1 allocs/op
BenchmarkTransferStateTransition-12   2847103     421 ns/op   1856 B/op    32 allocs/op
```

How to read each column:

```
BenchmarkCreateTransfer-12     -- benchmark name, GOMAXPROCS=12
84912                          -- b.N (number of iterations)
67234 ns/op                    -- 67.2 microseconds per operation
8432 B/op                      -- 8.4 KB heap allocated per operation
142 allocs/op                  -- 142 heap allocation calls per operation
```

### Interpreting the Numbers

```
+-------------------+------------------+---------------------------+
| Metric            | What It Means    | When to Worry             |
+-------------------+------------------+---------------------------+
| ns/op             | Latency          | Exceeds target threshold  |
| B/op              | Memory pressure  | Causes GC pressure        |
| allocs/op         | Allocation count | High count = GC overhead  |
+-------------------+------------------+---------------------------+
```

**The allocs/op number is often more important than ns/op.** Each allocation
contributes to GC pressure. At 5,000 TPS with 142 allocs/op, the runtime
performs 710,000 allocations per second -- well within Go's GC capacity. But
if a refactor increased this to 1,000 allocs/op, you would see 5M allocs/sec,
which creates measurable GC pause pressure.

### Position Event Writer (treasury/event_writer.go)

The treasury event writer is a background goroutine that batch-inserts position
events (reserve, release, commit, balance update) to the `position_events` table
for audit purposes. It targets ~20,000 events/sec at peak:

```go
// eventWriteInterval is how often the event writer drains the pendingEvents
// channel and batch-inserts events to the position_events table. 10ms at peak
// load (~20,000 events/sec) yields ~200 events per batch -- one DB round-trip.
const eventWriteInterval = 10 * time.Millisecond

// eventWriteBatchSize is the maximum number of events to batch-insert per cycle.
const eventWriteBatchSize = 1000
```

The design uses a buffered channel as a write-ahead queue. The writer goroutine
wakes every 10ms, drains up to 1,000 events from the channel, and batch-inserts
them. At 20,000 events/sec, each batch contains ~200 events -- one DB round-trip
per batch keeps the write path efficient.

If the store does not implement `EventStore`, the writer drains and discards
events silently. This means the event writer is a best-effort audit log -- it
never blocks the hot path (Reserve/Release/Commit operations).

---

## Running Benchmarks

### Single Package

```bash
go test -bench=Benchmark -benchmem -benchtime=5s -run='^$' ./core/...
```

Flags explained:
- `-bench=Benchmark`: Run all functions matching `Benchmark*`
- `-benchmem`: Report memory allocations
- `-benchtime=5s`: Run each benchmark for at least 5 seconds
- `-run='^$'`: Skip all unit tests (regex matches nothing)

### All Packages with Threshold Comparison

```bash
make bench
```

This runs all benchmarks across every package, writes results to
`bench-results.txt`, and then invokes `scripts/parse_benchmarks.py` to compare
against the 76 defined targets:

```
=== Threshold Comparison ===
PASS  BenchmarkCreateTransfer           67.2us  < 100us
PASS  BenchmarkGetTransfer               0.5us  <  10us
PASS  BenchmarkTransferStateTransition   0.4us  <   1us
PASS  BenchmarkScoreRoute                0.3us  <   1us
FAIL  BenchmarkListTransfers            12.3ms  > 10ms    <-- REGRESSION
```

---

## Common Mistakes

### Mistake 1: Benchmarking with Real I/O

```go
// BAD: This measures Postgres latency, not engine latency
func BenchmarkCreateTransfer(b *testing.B) {
    db := connectToPostgres()  // 5ms per query
    engine := NewEngine(db, ...)
    // ...
}
```

Component benchmarks must use mock stores. End-to-end performance is measured
in load tests (Chapter 8.2).

### Mistake 2: Not Calling b.ResetTimer()

```go
// BAD: Setup time is included in the measurement
func BenchmarkGetQuote(b *testing.B) {
    engine := setupBenchmarkEngine(b)  // 500ms of setup
    // Missing b.ResetTimer()
    for i := 0; i < b.N; i++ { ... }
}
```

### Mistake 3: Reusing the Same Input

```go
// BAD: CPU cache effects make this faster than reality
func BenchmarkProcess(b *testing.B) {
    transfer := createOneTransfer()
    for i := 0; i < b.N; i++ {
        engine.Process(ctx, transfer.ID)  // Same ID every time
    }
}
```

The correct approach is to pre-create N transfers and cycle through them:
`transferIDs[i%100]`.

### Mistake 4: Not Using b.RunParallel for Throughput Targets

```go
// BAD: Sequential benchmark for a throughput target
// Target: >10,000 transfers/sec total
func BenchmarkCreateConcurrent(b *testing.B) {
    for i := 0; i < b.N; i++ { ... }  // Single goroutine
}

// GOOD: b.RunParallel spawns GOMAXPROCS goroutines
func BenchmarkCreateConcurrent(b *testing.B) {
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() { ... }
    })
}
```

---

## Exercises

1. **Add a benchmark for idempotent quote retrieval.** Write
   `BenchmarkGetQuoteIdempotent` that creates a quote, then benchmarks
   retrieving the same quote 1000 times. Target: <50us per retrieval.

2. **Identify the allocation hotspot.** Run `go test -bench=BenchmarkCreateTransfer
   -benchmem -memprofile mem.prof ./core/...` and analyze with
   `go tool pprof mem.prof`. Which allocation dominates? Can it be pooled?

3. **Write a sub-benchmark suite.** Using `b.Run()`, create a benchmark that
   tests `BenchmarkScoreRoute` with 5 different fee structures (LowFee through
   VeryHighFee). Verify that fee magnitude does not affect scoring latency.

---

## What's Next

With component benchmarks proving each module meets its isolated targets,
Chapter 8.2 builds the custom Go load testing harness that proves the *system*
handles 5,000 TPS end-to-end through the gateway, gRPC, engine, treasury,
ledger, and provider layers combined.
