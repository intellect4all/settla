# Stage 7.1: Component Benchmarks

This directory contains comprehensive Go benchmarks for all performance-critical components in Settla. These benchmarks prove each component's throughput ceiling exceeds what the system demands at 50M transactions/day.

## Scale Requirements

The system must handle:
- **Ledger writes**: >25,000 entries/sec (peak demand)
- **Treasury reservation**: >5,000 reserves/sec at <1μs each
- **State machine transitions**: >5,000 transitions/sec
- **Router scoring**: >5,000 route evaluations/sec
- **Domain validation**: >50,000 ValidateEntries/sec

## Benchmark Files

### 1. `domain/bench_test.go` - Core Domain Logic
Pure computation benchmarks with no I/O dependencies.

**Benchmarks:**
- `BenchmarkValidateEntries` - 4-line balanced entry validation (<1μs)
- `BenchmarkValidateEntries_TwoLine` - Simple 2-line validation (<500ns)
- `BenchmarkTransferTransitionTo` - State machine transition (<100ns)
- `BenchmarkTransferCanTransitionTo` - Transition validation (<50ns)
- `BenchmarkTransferTransition_FullLifecycle` - Full 6-step lifecycle (<500ns)
- `BenchmarkPositionAvailable` - Available balance calculation (<50ns)
- `BenchmarkPositionCanLock` - Lock validation (<50ns)
- `BenchmarkQuoteIsExpired` - Quote expiration check (<20ns)
- `BenchmarkValidateCurrency` - Currency validation (<10ns)
- `BenchmarkMoneyAdd` / `BenchmarkMoneyMul` - Decimal operations

### 2. `cache/bench_test.go` - Cache Performance
Tests local in-process cache and Redis cache performance.

**Benchmarks:**
- `BenchmarkLocalCacheGet` - Local cache lookup (<200ns)
- `BenchmarkLocalCacheSet` - Local cache write (<500ns)
- `BenchmarkRedisGet` / `BenchmarkRedisSet` - Redis operations (<1ms)
- `BenchmarkRedisSetJSON` / `BenchmarkRedisGetJSON` - JSON serialization
- `BenchmarkIdempotencyCheckSet` - Idempotency key check (<2ms)
- `BenchmarkConcurrentLocalCache` - Concurrent access patterns
- `BenchmarkConcurrentRedisCache` - Redis under load

### 3. `treasury/bench_test.go` - In-Memory Reservation
Tests the atomic CAS-based reservation system.

**Benchmarks:**
- `BenchmarkReserve_Single` - Single reserve call (<1μs)
- `BenchmarkReserve_Concurrent` - 1000 goroutines, same position (>100K/sec)
- `BenchmarkReserve_Concurrent_MultiTenant` - 10 tenants × 100 goroutines
- `BenchmarkRelease` - Release operation (<1μs)
- `BenchmarkCommitReservation` - Commit reserved → locked (<1μs)
- `BenchmarkGetPosition` - Position lookup (<500ns)
- `BenchmarkFlush` - Batch flush to Postgres (<50ms for 1000 positions)
- `BenchmarkReserveConcurrentContention` - Extreme contention test
- `BenchmarkGetLiquidityReport` - Report generation (<10ms)
- `BenchmarkUpdateBalance` - Balance update (<500ns)

**Critical Invariants Tested:**
- No over-reservation (reserved ≤ available)
- No cross-tenant interference
- Linear scaling with tenant count

### 4. `rail/router/bench_test.go` - Route Scoring
Tests the smart router's scoring and selection algorithm.

**Benchmarks:**
- `BenchmarkRoute` - Full route evaluation (<100μs)
- `BenchmarkRoute_MultiChain` - 4 chains × providers (<100μs)
- `BenchmarkRouteConcurrent` - Concurrent routing (>5,000/sec)
- `BenchmarkScoreRoute` - Single route score (<1μs)
- `BenchmarkScoreRouteConcurrent` - Scoring throughput (>100K/sec)
- `BenchmarkGetQuote` - Quote generation (<200μs)
- `BenchmarkGetQuoteConcurrent` - Quote throughput (>5,000/sec)
- `BenchmarkRouteLargeAmount` / `BenchmarkRouteSmallAmount` - Edge cases
- `BenchmarkScoreRouteVariations` - Different fee ratios

### 5. `core/bench_test.go` - Settlement Engine
Tests the full settlement orchestration pipeline.

**Benchmarks:**
- `BenchmarkCreateTransfer` - Transfer creation (<100μs)
- `BenchmarkCreateTransferConcurrent` - Creation throughput (>10,000/sec)
- `BenchmarkFundTransfer` - Fund + reserve (<50μs)
- `BenchmarkInitiateOnRamp` - On-ramp initiation (<100μs)
- `BenchmarkSettleOnChain` - Blockchain settlement (<100μs)
- `BenchmarkProcessTransfer_FullPipeline` - Complete pipeline (<500μs)
- `BenchmarkProcessTransferConcurrent` - Pipeline throughput (>2,000/sec)
- `BenchmarkGetTransfer` - Transfer lookup (<10μs)
- `BenchmarkGetQuote` - Quote retrieval (<100μs)
- `BenchmarkCompleteTransfer` - Transfer completion (<100μs)
- `BenchmarkTransferStateTransition` - State transitions (<1μs)
- `BenchmarkEngineWithIdempotency` - With idempotency check (<150μs)
- `BenchmarkListTransfers` - List with pagination (<10ms for 100)

### 6. `ledger/bench_test.go` - TigerBeetle Write Path
Tests the dual-backend ledger (TigerBeetle writes, Postgres reads).

**Benchmarks:**
- `BenchmarkPostEntries_Single` - Single entry posting (<500μs)
- `BenchmarkPostEntries_Batch` - Batched posting (>30,000/sec)
- `BenchmarkGetBalance` - Balance lookup (<100μs)
- `BenchmarkPostEntries_Concurrent` - Concurrent posting (>25,000/sec)
- `BenchmarkPostEntries_MultiLine` - 4-line entries (<600μs)
- `BenchmarkPostEntries_WithAccountCreation` - With account setup (<1ms)
- `BenchmarkEnsureAccounts` - Account creation (<200μs)
- `BenchmarkPostEntries_HighThroughput` - Sustained throughput (>25,000/sec)
- `BenchmarkGetEntries` - Query Postgres read-side (<10ms)
- `BenchmarkPostEntriesValidation` - Pure validation (>50,000/sec)
- `BenchmarkTBCreateTransfers` - Raw TigerBeetle writes (<100μs)
- `BenchmarkTBLookupAccounts` - Raw TigerBeetle reads (<50μs)

## Running Benchmarks

### Run All Benchmarks
```bash
make bench
```

This runs all benchmarks with 5s duration per benchmark and outputs to `bench-results.txt`.

### Run Specific Package
```bash
go test ./domain -bench=Benchmark -benchmem -benchtime=5s
```

### Run with Parser
```bash
make bench | python3 scripts/parse_benchmarks.py
```

The parser compares results against target thresholds and outputs PASS/FAIL for each benchmark.

### Quick Test (CI-friendly)
```bash
go test ./... -bench=Benchmark -benchtime=100ms -run=^$
```

## Benchmark Design Principles

1. **Use `b.ReportAllocs()`** - All benchmarks report allocations to track memory pressure
2. **Use `b.RunParallel()`** - Concurrent benchmarks use proper Go bench scaling
3. **Use `b.ResetTimer()`** - Setup code is excluded from timing
4. **Use mocks for infra** - Benchmarks that test pure logic use mocks (no real DB/Redis)
5. **Include invariant checks** - Critical benchmarks verify correctness (e.g., no over-reservation)
6. **Fixed seeds** - Any randomness uses fixed seeds for reproducibility

## Interpreting Results

### Good Performance Indicators
- **Domain benchmarks**: <1μs for validation, <100ns for transitions
- **Cache benchmarks**: <200ns local, <1ms Redis
- **Treasury benchmarks**: <1μs reserve, scales linearly with tenants
- **Router benchmarks**: <100μs full route, <1μs scoring
- **Core benchmarks**: <500μs full pipeline, >2,000/sec throughput
- **Ledger benchmarks**: >25,000 entries/sec sustained

### Red Flags
- Domain validation >10μs (indicates excessive allocations)
- Treasury reserve >10μs (indicates lock contention)
- Router scoring >10μs (indicates algorithmic inefficiency)
- Ledger throughput <10,000/sec (indicates batching issues)

## Performance Targets vs Reality

Some benchmarks may fail their targets on development hardware (e.g., M1 MacBook). The targets are set for production hardware (AMD EPYC or similar). Key differences:

- **Development**: M1 MacBook Pro, single-node TigerBeetle, local Redis
- **Production**: AMD EPYC, replicated TigerBeetle cluster, dedicated Redis

If benchmarks fail on development but pass CI (which runs on production-like hardware), that's expected. If they fail everywhere, investigate.

## Future Enhancements

1. **Infra benchmarks** - Add build tag for benchmarks that need real TigerBeetle/Postgres
2. **Memory profiling** - Add `-memprofile` support to track heap growth
3. **CPU profiling** - Add `-cpuprofile` support for hotspot analysis
4. **Chaos benchmarks** - Test performance under failure conditions
5. **Tenant isolation benchmarks** - Verify no cross-tenant leakage under load
