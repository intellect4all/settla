# Chapter 2.2: Why TigerBeetle

**Estimated reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Calculate the exact throughput requirements that break Postgres for ledger writes
2. Explain where Postgres bottlenecks occur (WAL, row-level locks, fsync, vacuum)
3. Describe TigerBeetle's architecture and why it achieves 1M+ TPS
4. Understand the AmountScale (10^8) fixed-point representation
5. Trace how Settla's `TBClient` interface abstracts the real TigerBeetle client
6. Interpret benchmark results and connect them to production capacity targets

---

## The Scale Problem

Let's do the math that drove the TigerBeetle decision.

Settla's target: **50 million transactions per day**.

Each transfer generates ledger entries at multiple stages:

```
Transfer lifecycle ledger writes:
  1. Treasury reservation      → 2 entry lines (DR asset, CR pending)
  2. On-ramp completion        → 3-4 entry lines (DR crypto, CR fiat, DR fees)
  3. Blockchain confirmation   → 2 entry lines
  4. Off-ramp completion       → 3-4 entry lines
  5. Settlement finalization   → 2 entry lines
                                 ─────────────
                        Total: ~12-14 entry lines per transfer
```

At 50M transfers/day:

```
50,000,000 transfers × ~5 journal entries × ~2.5 lines each
= 200-250 million entry_lines per day
= ~2,900 entry_lines per second (sustained)
= ~15,000-25,000 entry_lines per second (peak, 5x burst)
```

Can Postgres handle 25,000 inserts per second into the ledger? Let's find out.

---

## Where Postgres Breaks

### Problem 1: Write-Ahead Log (WAL) Serialization

Every Postgres write goes through the WAL. The WAL is an append-only sequential file that records changes before they hit the data pages. This is great for durability, but it creates a serialization bottleneck:

```
    Concurrent writers
    ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐
    │ W1   │ │ W2   │ │ W3   │ │ W4   │
    └──┬───┘ └──┬───┘ └──┬───┘ └──┬───┘
       │        │        │        │
       v        v        v        v
    ┌──────────────────────────────────┐
    │   WAL (serial append)            │  <-- bottleneck
    │   [W1][W2][W3][W4]...           │
    └──────────────────────────────────┘
       │
       v
    ┌──────────────────────────────────┐
    │   fsync() to disk                │  <-- ~1-5ms per sync
    └──────────────────────────────────┘
```

With `synchronous_commit = on` (required for a financial ledger), each transaction waits for WAL fsync. On NVMe SSDs, fsync takes 0.5-2ms. This caps durable throughput at approximately 2,000-5,000 transactions per second on a single Postgres instance.

### Problem 2: Row-Level Locks on balance_snapshots

If we tried to maintain real-time balances in Postgres, every debit or credit to the same account would need:

```sql
SELECT balance FROM balance_snapshots WHERE account_id = $1 FOR UPDATE;
-- compute new balance
UPDATE balance_snapshots SET balance = $2 WHERE account_id = $1;
```

The `FOR UPDATE` lock serializes all concurrent writes to the same account. For a system clearing account that participates in every transaction, this creates a single-row hotspot:

```
    Thread 1: SELECT ... FOR UPDATE (acquires lock)
    Thread 2: SELECT ... FOR UPDATE (BLOCKED - waiting)
    Thread 3: SELECT ... FOR UPDATE (BLOCKED - waiting)
    Thread 4: SELECT ... FOR UPDATE (BLOCKED - waiting)
              ...
    Thread 1: COMMIT (releases lock)
    Thread 2: acquires lock...
```

At 25K TPS, with each lock held for ~1ms, the queue time alone makes this impossible. This is the exact same problem that treasury reservations face (solved with in-memory atomics in Module 5).

### Problem 3: MVCC Bloat and Vacuum

Postgres uses Multi-Version Concurrency Control (MVCC). Every UPDATE creates a new row version; the old version becomes "dead." For a hot `balance_snapshots` row updated 25,000 times per second:

```
25,000 dead tuples/sec × 60 sec = 1.5M dead tuples/min
```

The autovacuum process must clean these up. Under heavy write load, vacuum cannot keep pace, causing table bloat, index bloat, and degraded query performance.

### Problem 4: B-tree Index Maintenance

Every INSERT into `entry_lines` updates the indexes:

```sql
CREATE INDEX idx_entry_lines_journal ON entry_lines(journal_entry_id);
CREATE INDEX idx_entry_lines_account ON entry_lines(account_id, created_at DESC);
```

At 25K inserts/sec, each insert requires 2 B-tree index updates. B-tree page splits and rebalancing add I/O amplification. The total write amplification (WAL + heap + indexes) reaches 5-10x the raw data size.

### The Verdict

| Metric | Postgres Capacity | Settla Requirement | Gap |
|--------|------------------|--------------------|-----|
| Durable writes/sec | ~2,000-5,000 | 25,000 | 5-12x |
| Hot-key updates/sec | ~500-1,000 | 25,000 | 25-50x |
| Write amplification | 5-10x | 1x ideal | N/A |
| Vacuum pressure | Grows with TPS | Zero tolerance | N/A |

Postgres is 5-50x short of the requirement. We need a purpose-built financial database.

---

## Enter TigerBeetle

TigerBeetle is a purpose-built financial accounting database designed from first principles for exactly this workload. It achieves **1,000,000+ transactions per second** on a single node.

### Architecture Overview

```
    ┌─────────────────────────────────────────────────┐
    │                TigerBeetle Node                   │
    │                                                   │
    │  ┌──────────────────────────────────────────┐    │
    │  │  Deterministic State Machine              │    │
    │  │  (single-threaded, no locks needed)       │    │
    │  │                                            │    │
    │  │  ┌─────────┐  ┌──────────┐  ┌─────────┐  │    │
    │  │  │ Account │  │ Transfer │  │ Balance │  │    │
    │  │  │ Table   │  │ Table    │  │ Index   │  │    │
    │  │  └─────────┘  └──────────┘  └─────────┘  │    │
    │  └──────────────────────────────────────────┘    │
    │                      │                            │
    │  ┌──────────────────────────────────────────┐    │
    │  │  io_uring I/O Engine                      │    │
    │  │  (zero-copy, kernel-bypassing I/O)        │    │
    │  └──────────────────────────────────────────┘    │
    │                      │                            │
    │  ┌──────────────────────────────────────────┐    │
    │  │  Storage Engine                            │    │
    │  │  (LSM-like, no WAL overhead)              │    │
    │  └──────────────────────────────────────────┘    │
    │                                                   │
    │  ┌──────────────────────────────────────────┐    │
    │  │  Consensus (Viewstamped Replication)       │    │
    │  │  (3-node or 6-node clusters)              │    │
    │  └──────────────────────────────────────────┘    │
    └─────────────────────────────────────────────────┘
```

### Why TigerBeetle Is Fast

**1. Deterministic Simulation Testing**

TigerBeetle's entire state machine is deterministic. Given the same inputs in the same order, it produces the same outputs. This enables exhaustive testing via simulation: the test harness can inject arbitrary failures (disk corruption, network partitions, power loss) and verify correctness. No other database tests this way.

**2. io_uring for I/O**

Traditional databases use POSIX `read()`/`write()` syscalls, each requiring a context switch between user space and kernel space. TigerBeetle uses Linux's `io_uring` interface, which batches I/O submissions and completions through shared ring buffers:

```
    Traditional I/O:
    User Space    Kernel Space
    ┌────────┐    ┌────────┐
    │ write()├───>│ syscall│    ← context switch (1-5 us)
    │        │<───┤ return │    ← context switch (1-5 us)
    └────────┘    └────────┘
    Per operation: 2 context switches

    io_uring:
    User Space              Kernel Space
    ┌────────────────┐      ┌────────────────┐
    │ Submit 1000    │      │                │
    │ operations to  ├─────>│ Process batch  │   ← 1 context switch
    │ submission ring│      │ (1000 ops)     │
    │                │<─────┤ Complete batch  │   ← 1 context switch
    │ Read from      │      │                │
    │ completion ring│      │                │
    └────────────────┘      └────────────────┘
    Per 1000 operations: 2 context switches total
```

**3. Single-Threaded State Machine**

Counter-intuitively, TigerBeetle processes ALL state mutations on a single thread. This eliminates:
- Lock contention (no locks needed)
- Cache coherence traffic between CPU cores
- Lock-free data structure complexity

The I/O is asynchronous and parallel (via io_uring), but the state machine processing is serial. Since the state machine is in-memory and computation-bound operations complete in nanoseconds, the bottleneck is I/O, not CPU.

**4. Fixed-Size Records**

TigerBeetle accounts and transfers are fixed-size structs (128 bytes each). No variable-length fields, no heap allocation, no serialization overhead. This enables:
- O(1) lookup by ID (direct offset calculation)
- Zero-copy reads from memory-mapped storage
- Predictable memory layout for CPU cache optimization

**5. Built-In Double-Entry Enforcement**

TigerBeetle's transfer is inherently a double-entry posting: every transfer debits one account and credits another. The engine enforces balance at the hardware level -- there is no way to create an imbalanced entry.

---

## AmountScale: Fixed-Point Representation

TigerBeetle stores amounts as unsigned 128-bit integers. Settla maps between `shopspring/decimal` and TigerBeetle's integer representation using a fixed scale factor:

```go
// AmountScale is the fixed-point scale for converting decimal amounts to
// TigerBeetle uint64 amounts. Matches Postgres NUMERIC(28,8).
const AmountScale int64 = 100_000_000 // 10^8
```

This means:

```
Decimal       -> TigerBeetle uint64
1.00          -> 100,000,000
0.00000001    -> 1              (minimum representable amount)
1,000,000.00  -> 100,000,000,000,000
```

The conversion functions in `ledger/tigerbeetle.go`:

```go
// DecimalToTBAmount converts a shopspring/decimal to a TigerBeetle uint64
// by multiplying by AmountScale. Truncates any fractional digits beyond 8 dp
// (the ledger's precision limit). Truncation -- not rounding -- ensures we never
// overstate amounts, which is standard practice in settlement systems.
// Returns an error if the value is negative.
func DecimalToTBAmount(d decimal.Decimal) (uint64, error) {
    truncated := d.Truncate(8)
    scaled := truncated.Mul(amountScaleDec)
    if scaled.IsNegative() {
        return 0, fmt.Errorf("settla-ledger: negative amount %s", d)
    }
    return uint64(scaled.IntPart()), nil
}

// TBAmountToDecimal converts a TigerBeetle uint64 back to decimal.
func TBAmountToDecimal(amount uint64) decimal.Decimal {
    return decimal.NewFromInt(int64(amount)).Div(amountScaleDec)
}
```

> **Key Insight**: Truncation, not rounding. When converting 1.123456789 to 8 decimal places, the result is 1.12345678, NOT 1.12345679. This is deliberate. In settlement systems, you must never overstate an amount. Truncation always understates, which is the safe direction. The sub-cent difference is either absorbed as a fee or settled in the next batch.

The round-trip is verified in tests:

```go
func TestTBBackend_AmountConversion(t *testing.T) {
    tests := []struct {
        name      string
        input     string
        wantErr   bool
        wantValue string
    }{
        {"integer", "1000", false, ""},
        {"8 decimals", "1000.12345678", false, ""},
        {"zero", "0", false, ""},
        {"negative", "-100", true, ""},
        {"truncates beyond 8 dp", "1.123456789", false, "1.12345678"},
    }
    // ...
}
```

---

## The TBClient Interface

Settla does not import TigerBeetle types throughout the codebase. Instead, it defines a `TBClient` interface in `ledger/tigerbeetle.go`:

```go
// TBClient abstracts the TigerBeetle client so the backend can be tested
// without a running TigerBeetle cluster. The production adapter wraps
// tigerbeetle-go/pkg/client.
type TBClient interface {
    CreateAccounts(accounts []TBAccount) ([]TBCreateResult, error)
    CreateTransfers(transfers []TBTransfer) ([]TBCreateResult, error)
    LookupAccounts(ids []ID128) ([]TBAccount, error)
    Close()
}
```

This interface is implemented by two types:

**1. `realTBClient`** (in `ledger/tigerbeetle_adapter.go`) -- wraps the real TigerBeetle Go client:

```go
// realTBClient adapts the tigerbeetle-go Client to our TBClient interface.
type realTBClient struct {
    client tb.Client
}

func NewRealTBClient(clusterID uint64, addresses []string) (TBClient, error) {
    client, err := tb.NewClient(tbtypes.ToUint128(clusterID), addresses)
    if err != nil {
        return nil, fmt.Errorf("settla-ledger: creating TigerBeetle client: %w", err)
    }
    return &realTBClient{client: client}, nil
}
```

**2. `mockTBClient`** (in `ledger/ledger_test.go`) -- an in-memory mock for testing:

```go
type mockTBClient struct {
    mu        sync.Mutex
    accounts  map[ID128]TBAccount
    transfers map[ID128]TBTransfer
}
```

The mock faithfully simulates TigerBeetle's behavior: it tracks account balances, returns `TBResultExists` for duplicate creates, and supports concurrent access with a mutex.

```
    Production:                     Testing:
    ┌──────────────┐                ┌──────────────┐
    │ ledger.Service│                │ ledger.Service│
    │              │                │              │
    │ TBClient ────┼──┐             │ TBClient ────┼──┐
    └──────────────┘  │             └──────────────┘  │
                      v                                v
              ┌──────────────┐                ┌──────────────┐
              │ realTBClient │                │ mockTBClient │
              │ (TB cluster) │                │ (in-memory)  │
              └──────────────┘                └──────────────┘
```

---

## Account IDs: Deterministic Hashing

TigerBeetle identifies accounts by 128-bit IDs. Settla generates these deterministically from account codes using SHA-256:

```go
// AccountIDFromCode generates a deterministic 128-bit TigerBeetle account ID
// from a ledger account code using SHA-256(code)[:16].
func AccountIDFromCode(code string) ID128 {
    h := sha256.Sum256([]byte(code))
    var id ID128
    copy(id[:], h[:16])
    return id
}
```

Why deterministic? Because the same account code must always map to the same TigerBeetle account. If we used random UUIDs, we would need a mapping table between codes and TB IDs -- adding another database lookup to the hot path.

The determinism is verified:

```go
func TestTBBackend_DeterministicIDs(t *testing.T) {
    code := "tenant:lemfi:assets:bank:gbp:clearing"
    id1 := AccountIDFromCode(code)
    id2 := AccountIDFromCode(code)
    if id1 != id2 {
        t.Error("AccountIDFromCode should be deterministic")
    }

    id3 := AccountIDFromCode("tenant:fincra:assets:bank:gbp:clearing")
    if id1 == id3 {
        t.Error("different codes should produce different IDs")
    }
}
```

---

## Benchmarks: Proving the Capacity

The benchmark targets from `ledger/bench_test.go` map directly to production requirements:

| Benchmark | Target | What It Proves |
|-----------|--------|----------------|
| `BenchmarkPostEntries_Single` | <500us/op | Single-entry latency acceptable |
| `BenchmarkPostEntries_HotKey` | >15,000 entries/s | Hot-key contention survivable |
| `BenchmarkPostEntries_Batch` | >30,000 entries/s | Batching delivers throughput |
| `BenchmarkPostEntries_HighThroughput` | >25,000 entries/s | Sustained peak capacity met |
| `BenchmarkGetBalance` | <100us/op | Balance lookups fast enough for real-time |
| `BenchmarkPostEntriesValidation` | <1us/op | Validation is negligible overhead |

The high-throughput benchmark simulates production conditions with 256 concurrent workers:

```go
// BenchmarkPostEntries_HighThroughput stress tests the batcher.
// Simulates sustained high-throughput posting.
//
// Target: Sustained >25,000 entries/sec
func BenchmarkPostEntries_HighThroughput(b *testing.B) {
    svc, _ := setupBenchmarkServiceWithBatching(b)
    defer svc.Stop()

    ctx := context.Background()
    entry := balancedEntry("throughput-test")
    _, _ = svc.PostEntries(ctx, entry) // Pre-create accounts

    b.ReportAllocs()
    b.ResetTimer()

    var wg sync.WaitGroup
    numWorkers := 256
    opsPerWorker := b.N / numWorkers

    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            for j := 0; j < opsPerWorker; j++ {
                e := balancedEntry(fmt.Sprintf("worker-%d", workerID))
                e.IdempotencyKey = fmt.Sprintf("idem-throughput-%d-%d", workerID, j)
                _, _ = svc.PostEntries(ctx, e)
            }
        }(i)
    }
    wg.Wait()
}
```

The hot-key benchmark specifically tests the worst case -- all goroutines writing to the SAME account pair:

```go
// BenchmarkPostEntries_HotKey measures throughput under hot-key contention.
// A single debit+credit account pair is shared across 256 goroutines to simulate
// the worst-case hot-key scenario (e.g., a system clearing account).
//
// Target: >15,000 entries/sec under contention, no data races
func BenchmarkPostEntries_HotKey(b *testing.B) {
    // ...
}
```

> **Key Insight**: The hot-key benchmark exists because in production, the system USDT wallet (`assets:crypto:usdt:tron`) participates in nearly every transaction. If this single account becomes a bottleneck, the entire system grinds to a halt. TigerBeetle's single-threaded state machine handles this naturally -- there are no locks to contend on.

---

## Postgres Still Has a Role

TigerBeetle is optimized for two operations: create accounts and create transfers. It does NOT support:
- Range queries ("show me all entries for account X between dates Y and Z")
- Full-text search
- JOINs
- Aggregations
- Rich reporting

These are essential for dashboards, audit trails, and reconciliation. So Settla keeps Postgres as the **read model** in a CQRS architecture (covered in depth in Chapter 2.3).

```
    ┌─────────────────────────────────────────────┐
    │            Write Path (Hot)                   │
    │                                               │
    │  PostEntries() ──> TigerBeetle               │
    │                    (1M+ TPS, O(1) balance)   │
    │                                               │
    ├─────────────────────────────────────────────┤
    │            Read Path (Query)                  │
    │                                               │
    │  GetEntries()  ──> Postgres                  │
    │  GetBalance()  ──> TigerBeetle               │
    │  Dashboard     ──> Postgres                  │
    │  Audit trail   ──> Postgres                  │
    │                                               │
    ├─────────────────────────────────────────────┤
    │            Sync Path (Background)             │
    │                                               │
    │  SyncConsumer: TB ──> Postgres                │
    │  (100ms interval, batch 1000)                │
    │                                               │
    └─────────────────────────────────────────────┘
```

---

## Common Mistakes

**Mistake 1: Trying to use Postgres as the write authority.**
Postgres cannot sustain 25K durable writes/sec for balanced double-entry postings. The WAL serialization, row-level lock contention on balance snapshots, and MVCC vacuum pressure make it structurally unsuitable for this workload.

**Mistake 2: Using float64 for the TB amount conversion.**
`DecimalToTBAmount` uses `decimal.Truncate(8).Mul(amountScaleDec)` -- pure decimal math. If you used `float64(d) * 1e8`, rounding errors would silently corrupt balances.

**Mistake 3: Generating random TigerBeetle account IDs.**
Account IDs must be deterministic from account codes. `AccountIDFromCode` uses SHA-256. Random IDs would require a mapping table, adding latency to every write.

**Mistake 4: Expecting TigerBeetle to replace Postgres entirely.**
TigerBeetle does accounts and transfers. Period. Queries, reporting, audit trails, and dashboard data all come from Postgres. The two systems are complementary.

**Mistake 5: Ignoring the truncation behavior.**
`DecimalToTBAmount` truncates to 8 decimal places, not rounds. `1.999999999` becomes `1.99999999`. This is the correct behavior for a settlement system (never overstate), but you must account for the sub-cent loss in reconciliation.

---

## Exercises

1. **Capacity Calculation**: If Settla scales to 100M transactions/day, how many entry lines per second at peak (5x burst)? Would a single TigerBeetle node handle this? What about with the write-ahead batcher?

2. **Amount Conversion**: What is the TigerBeetle uint64 representation of GBP 1,234.56789012? Show the truncation step. What value do you get after round-tripping back through `TBAmountToDecimal`?

3. **Mock Client**: The `mockTBClient` uses a `sync.Mutex` for thread safety. Why is this necessary even though TigerBeetle itself is single-threaded? (Hint: think about who calls the mock.)

4. **Interface Design**: Why does `TBClient` return `[]TBCreateResult` instead of just `error`? What information does the result array carry that a simple error cannot?

5. **Postgres Limits**: A team proposes using Postgres with `synchronous_commit = off` to hit the 25K writes/sec target. List three reasons why this is unacceptable for a settlement ledger.

---

## What's Next

You now understand why TigerBeetle handles writes and Postgres handles reads. But how do these two systems stay in sync? How does a query to Postgres return data that was written to TigerBeetle? The next chapter dives into the CQRS architecture that makes this work.

**Next: [Chapter 2.3: CQRS Ledger Architecture](./chapter-2.3-cqrs-ledger.md)**
