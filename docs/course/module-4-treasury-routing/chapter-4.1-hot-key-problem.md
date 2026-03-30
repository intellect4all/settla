# Chapter 4.1: The Hot-Key Problem

**Reading time:** ~20 minutes
**Prerequisites:** Module 3 (Ledger), basic understanding of database locking
**Code references:** `treasury/manager.go`, `treasury/position_state.go`, `treasury/manager_test.go`

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why treasury positions become hot keys under concurrent load
2. Trace a lock contention timeline for `SELECT FOR UPDATE` at 5,000 TPS
3. Articulate why database sharding does not solve the treasury hot-key problem
4. Benchmark the naive database approach and identify the throughput ceiling
5. Describe the architectural insight that leads to an in-memory solution

---

## The Problem: 50 Hot Rows Under 5,000 TPS

Settla processes ~580 TPS sustained, with peaks of 3,000-5,000 TPS. Every transfer
must reserve treasury funds before execution. The treasury has approximately 50
active positions -- one per (tenant, currency, location) tuple.

Here is the math that breaks everything:

```
50 treasury positions
5,000 peak TPS
~100 concurrent transfers per position at any given moment

Each reservation needs to:
  1. Read the current balance
  2. Verify sufficient funds
  3. Decrement available balance
  4. Write back

All four steps must be atomic.
```

This is the classic **hot-key problem**: many concurrent writers contending for
the same small set of rows.

---

## The Naive Approach: SELECT FOR UPDATE

The straightforward database solution uses a pessimistic lock:

```sql
BEGIN;

-- Step 1: Lock the row. Every other transaction blocks here.
SELECT balance, locked, reserved
FROM treasury_positions
WHERE tenant_id = $1 AND currency = $2 AND location = $3
FOR UPDATE;

-- Step 2: Application checks available = balance - locked - reserved
-- Step 3: If sufficient, update
UPDATE treasury_positions
SET reserved = reserved + $4,
    updated_at = NOW()
WHERE tenant_id = $1 AND currency = $2 AND location = $3;

COMMIT;
```

This works correctly at low concurrency. Let us trace what happens at scale.

---

## Lock Contention Timeline

Imagine 100 goroutines trying to reserve from the same position simultaneously.
Each database round-trip takes ~1ms over a local network:

```
Time(ms)  Goroutine-1    Goroutine-2    Goroutine-3    ...    Goroutine-100
--------  -----------    -----------    -----------           -------------
  0       BEGIN          BEGIN          BEGIN                 BEGIN
  1       SELECT..FU     SELECT..FU     SELECT..FU            SELECT..FU
          (acquires      (BLOCKED)      (BLOCKED)             (BLOCKED)
           row lock)
  2       check avail
  3       UPDATE
  4       COMMIT
          (releases      (acquires
           row lock)      row lock)
  5                      check avail
  6                      UPDATE
  7                      COMMIT
                         (releases      (acquires
                          row lock)      row lock)
  8                                     check avail
  ...
 400                                                          (finally acquires
                                                               row lock)
 403                                                          COMMIT
```

The **serialization point** is the row lock. With 100 goroutines and ~4ms per
transaction:

```
Total time for 100 reservations = 100 x 4ms = 400ms

Throughput per position = 1000ms / 4ms = 250 reservations/sec
```

At 5,000 TPS spread across 50 positions, each position needs 100 reservations
per second. With a 250/sec ceiling, this seems tight but feasible -- until you
factor in real-world conditions.

---

## Why It Gets Worse in Practice

### Connection Pool Exhaustion

Each blocked `SELECT FOR UPDATE` holds a database connection for the entire wait
time. With 100 goroutines waiting on one position:

```
Connections consumed per hot position:  ~100
Total positions:                         50
Peak concurrent connections:             up to 5,000

PgBouncer pool size:                     ~100 per server
Servers:                                 6 replicas
Total pool:                              ~600 connections
```

The connection pool is exhausted long before the lock queue drains.

### Lock Queue Overhead

PostgreSQL's lock manager adds overhead per waiting transaction. At high
contention, the lock queue itself becomes a bottleneck:

```
Lock queue depth    Overhead per acquisition
-----------------   -----------------------
1-10                ~0.1ms
10-50               ~0.5ms
50-100              ~2ms
100+                exponential degradation
```

### Deadlock Risk

Multiple transfers reserve from different positions. Transfer A reserves
Position 1 then Position 2; Transfer B reserves Position 2 then Position 1.
Classic deadlock scenario:

```
Transfer A:  LOCK(pos_1) -----> wants LOCK(pos_2) [BLOCKED]
Transfer B:  LOCK(pos_2) -----> wants LOCK(pos_1) [BLOCKED]

                    DEADLOCK DETECTED
                    One transaction aborted
                    Retry required
                    Throughput halved
```

---

## Why Sharding Does Not Help

The instinctive response to a hot-key problem is to shard: split each position
into N sub-positions and spread the load.

```
Position "lemfi:USD:bank:chase" (balance: $1,000,000)
    |
    +-- Shard 0: $200,000
    +-- Shard 1: $200,000
    +-- Shard 2: $200,000
    +-- Shard 3: $200,000
    +-- Shard 4: $200,000
```

This fails for treasury because of **liquidity fragmentation**:

### Problem 1: Insufficient Shard Balance

A $150,000 reservation requires one shard with enough balance. If each shard
holds $200,000, a few reservations can drain a shard while others have plenty:

```
After some traffic:
    Shard 0: $  5,000    (nearly empty)
    Shard 1: $180,000
    Shard 2: $ 50,000    (not enough for $150K)
    Shard 3: $200,000    (enough, but may be locked)
    Shard 4: $165,000    (enough)
```

Now a $150K reservation might hit Shard 0, fail, retry Shard 1 (enough!), but
Shard 1 is locked by another transaction. The routing logic becomes complex and
still serializes at the individual shard level.

### Problem 2: Cross-Shard Operations

A $500,000 reservation exceeds any single shard. Now you need a distributed
transaction across multiple shards:

```
BEGIN;
SELECT ... FROM treasury_shard_0 FOR UPDATE;  -- lock
SELECT ... FROM treasury_shard_1 FOR UPDATE;  -- lock
SELECT ... FROM treasury_shard_2 FOR UPDATE;  -- lock
-- Check combined balance
UPDATE treasury_shard_0 SET ...;
UPDATE treasury_shard_1 SET ...;
UPDATE treasury_shard_2 SET ...;
COMMIT;
```

This is worse than the original problem -- more locks held for longer, with
distributed coordination overhead.

### Problem 3: Balance Rebalancing

Shards become imbalanced over time. You need a background rebalancer that moves
funds between shards, which itself requires locks. The complexity compounds
without fundamentally solving the serialization problem.

---

## Benchmarking the Naive Approach

To quantify the problem, consider what a database-backed reserve looks like
in Go:

```go
// NAIVE APPROACH - DO NOT USE IN PRODUCTION
func (s *NaiveStore) Reserve(ctx context.Context, tenantID uuid.UUID,
    currency string, location string, amount decimal.Decimal) error {

    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback()

    var balance, locked, reserved decimal.Decimal
    err = tx.QueryRowContext(ctx,
        `SELECT balance, locked, reserved FROM treasury_positions
         WHERE tenant_id = $1 AND currency = $2 AND location = $3
         FOR UPDATE`,
        tenantID, currency, location,
    ).Scan(&balance, &locked, &reserved)
    if err != nil {
        return err
    }

    available := balance.Sub(locked).Sub(reserved)
    if available.LessThan(amount) {
        return ErrInsufficientFunds
    }

    _, err = tx.ExecContext(ctx,
        `UPDATE treasury_positions SET reserved = reserved + $1
         WHERE tenant_id = $2 AND currency = $3 AND location = $4`,
        amount, tenantID, currency, location,
    )
    if err != nil {
        return err
    }

    return tx.Commit()
}
```

Expected benchmark results under concurrency (local Postgres):

```
BenchmarkNaiveReserve/serial-8          2000      750 us/op
BenchmarkNaiveReserve/parallel-8         500     3200 us/op    (4.3x slower)
BenchmarkNaiveReserve/parallel-100       100    12000 us/op    (16x slower)
```

Compare with what Settla actually achieves with the in-memory approach
(from `treasury/manager_test.go`):

```
BenchmarkReserve-8    ~150-300 ns/op    (5,000x faster than naive parallel)
```

The in-memory CAS loop runs in **nanoseconds**, not milliseconds.

---

## The Insight: The Database Is the Wrong Place

The key realization is that treasury reservations are **transient state**.
A reservation lives for seconds to minutes -- from when a transfer begins
until it completes or fails. This transient state has different requirements
than durable state:

```
                    Durable State            Transient State
                    (balances)               (reservations)
                    ─────────────            ────────────────
Lifetime:           Permanent                Seconds to minutes
Consistency:        Must survive crashes     Acceptable to re-derive
Read pattern:       Infrequent               Every transfer
Write pattern:      Batch (settlement)       Every transfer (hot path)
Concurrency:        Low                      Extreme (5,000/sec/position)
Latency budget:     100ms acceptable         Must be < 1ms
```

The insight: **move the hot-path state to memory, flush to the database
asynchronously**. The database stores the durable truth (committed balances);
memory stores the transient truth (current reservations).

```
                                    HOT PATH (nanoseconds)
                                    ─────────────────────────
API Request ──> Reserve() ──> Atomic CAS on in-memory counter ──> Return
                                          |
                                          | (dirty flag set)
                                          v
                                    COLD PATH (100ms interval)
                                    ─────────────────────────
                                    Background flush goroutine
                                          |
                                          v
                                    Postgres UPDATE
```

This is the architecture that the next chapter explores in detail.

---

## Key Insight

> The hot-key problem is not a database tuning problem -- it is a fundamental
> mismatch between the access pattern (thousands of concurrent writes to the
> same row) and the tool (row-level locking in a relational database). The
> solution is to move the contended state out of the database entirely and
> into memory where atomic CPU instructions replace row locks.

---

## Common Mistakes

1. **Trying to fix it with connection pool tuning.** Increasing pool size from
   100 to 1,000 just moves the bottleneck from the pool to the lock queue.
   The row is still serialized.

2. **Using SKIP LOCKED.** This is designed for work queues, not balance
   management. Skipping a locked position means the reservation is silently
   dropped or erroneously routed to a different position.

3. **Optimistic locking with version columns.** At high contention, the retry
   rate approaches 100%. With 100 goroutines, 99 fail on each round, leading
   to a retry storm.

4. **Redis as a middle ground.** Redis is single-threaded and introduces a
   network hop (~0.5ms). It is better than Postgres for this pattern but still
   1,000x slower than in-process atomics. For ~50 positions, there is no
   reason to leave the process.

5. **Ignoring crash recovery.** Moving state to memory is the easy part.
   Surviving a crash without losing reservations is the hard part. Chapter 4.3
   covers this in depth.

---

## Exercises

### Exercise 4.1.1: Calculate Your Contention

Given a system with:
- 80 treasury positions
- 2,000 sustained TPS, 8,000 peak TPS
- 3ms average database round-trip (including lock wait)
- PgBouncer pool of 200 connections per server, 4 servers

Calculate:
1. Peak reservations per position per second
2. Maximum theoretical throughput per position with `SELECT FOR UPDATE`
3. Total connections needed at peak vs. available pool
4. The contention multiplier (actual latency / no-contention latency)

### Exercise 4.1.2: Sharding Analysis

Design a sharding scheme for the treasury with 8 shards per position. Walk
through these scenarios and identify where the design breaks:
1. A reservation for 30% of total position balance
2. Two concurrent reservations that each need 60% of a single shard
3. A position rebalance operation while reservations are in flight

### Exercise 4.1.3: Benchmark Comparison

If you have a local PostgreSQL instance, implement the naive `SELECT FOR UPDATE`
approach and benchmark it at:
- 1 goroutine
- 10 goroutines
- 100 goroutines
- 1,000 goroutines

Plot the results and identify the inflection point where lock contention
dominates.

---

## What's Next

Chapter 4.2 dives into Settla's actual solution: the in-memory treasury manager.
We walk through the `PositionState` struct with its atomic int64 counters, the
micro-unit representation, and the lock-free CAS loop that achieves nanosecond
reservation latency.

---

## Further Reading

- PostgreSQL documentation on [Explicit Locking](https://www.postgresql.org/docs/current/explicit-locking.html)
- Martin Kleppmann, *Designing Data-Intensive Applications*, Chapter 7: Transactions
- Pat Helland, "Life beyond Distributed Transactions: an Apostate's Opinion" (2007)
- `treasury/manager_test.go` -- `BenchmarkReserve` for the actual in-memory numbers
