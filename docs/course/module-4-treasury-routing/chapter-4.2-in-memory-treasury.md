# Chapter 4.2: In-Memory Treasury Manager

**Reading time:** ~25 minutes
**Prerequisites:** Chapter 4.1 (Hot-Key Problem), understanding of atomic operations
**Code references:** `treasury/position_state.go`, `treasury/manager.go`, `treasury/store.go`, `treasury/manager_test.go`

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the `PositionState` struct and its atomic int64 counters
2. Convert between decimal amounts and micro-unit int64 representation
3. Trace the `Reserve()` CAS loop line-by-line and explain each branch
4. Describe `Release()` and `CommitReservation()` and their ordering guarantees
5. Articulate why CAS is superior to a mutex for this workload

---

## The PositionState Struct

Every treasury position lives in memory as a `PositionState`. This struct
lives in its own file, `treasury/position_state.go`, separate from the
manager logic:

```go
type PositionState struct {
    // Immutable metadata (set once during load, never mutated).
    ID            uuid.UUID
    TenantID      uuid.UUID
    Currency      domain.Currency
    Location      string
    MinBalance    decimal.Decimal
    TargetBalance decimal.Decimal

    // mu protects multi-field operations that must be observed atomically:
    // - snapshot() takes RLock to read balance+locked+reserved consistently
    // - CommitReservation takes Lock to modify reserved and locked together
    // Single-field CAS operations (Reserve, Release) do not need this lock.
    mu sync.RWMutex

    // Atomic micro-unit counters — the hot path.
    // balance: total funds (updated by ledger sync / admin top-up)
    // locked:  committed for in-flight transfers (incremented by CommitReservation)
    // reserved: tentatively held (incremented by Reserve, decremented by Release/Commit)
    balanceMicro  atomic.Int64
    lockedMicro   atomic.Int64
    reservedMicro atomic.Int64

    // dirty is set when the position has been modified since the last flush.
    dirty atomic.Bool
}
```

The struct is split into three zones:

```
+──────────────────────────────────────────────────────+
|  COLD ZONE (set once at load, never mutated)         |
|                                                      |
|  ID, TenantID, Currency, Location                    |
|  MinBalance, TargetBalance                           |
+──────────────────────────────────────────────────────+
|  SNAPSHOT ZONE (RWMutex for multi-field reads)       |
|                                                      |
|  mu sync.RWMutex  -- snapshot() and CommitReservation|
+──────────────────────────────────────────────────────+
|  HOT ZONE (mutated on every reservation)             |
|                                                      |
|  balanceMicro   atomic.Int64  -- total funds         |
|  lockedMicro    atomic.Int64  -- committed transfers |
|  reservedMicro  atomic.Int64  -- tentative holds     |
|  dirty          atomic.Bool   -- needs flush?        |
+──────────────────────────────────────────────────────+
```

The cold zone uses normal Go types because it is never written after
initialization. The hot zone uses `atomic.Int64` because multiple goroutines
read and write these concurrently without any mutex on the hot path
(Reserve/Release use CAS loops). The `mu` RWMutex exists solely for
multi-field snapshot operations and CommitReservation where two counters
must change together.

---

## The Three Counters

The relationship between the three atomic counters is the core invariant:

```
Available = balance - locked - reserved
```

This is implemented in the `Available()` method:

```go
func (ps *PositionState) Available() decimal.Decimal {
    b := ps.balanceMicro.Load()
    l := ps.lockedMicro.Load()
    r := ps.reservedMicro.Load()
    return fromMicro(b - l - r)
}
```

Each counter represents a different lifecycle stage:

```
                        +-----------+
     UpdateBalance ---> | balance   |  Total funds in this position
                        +-----------+
                              |
                              |  Reserve()
                              v
                        +-----------+
                        | reserved  |  Tentatively held for a transfer
                        +-----------+
                              |
                   +----------+----------+
                   |                     |
            CommitReservation()      Release()
                   |                     |
                   v                     v
             +-----------+         (funds return
             | locked    |          to available)
             +-----------+
                   |
             Settlement / ledger sync
                   |
                   v
             (balance decremented)
```

The lifecycle of a transfer's treasury reservation:

1. **Reserve**: `reservedMicro += amount` (tentative hold)
2. **Success path**: `CommitReservation` moves reserved to locked
3. **Failure path**: `Release` returns reserved to available
4. **Settlement**: External process adjusts `balance` via `UpdateBalance`

---

## Micro-Unit Representation

Atomic operations in Go work on `int64`, not `decimal.Decimal`. Settla
converts all amounts to micro-units -- integer multiples of 10^-6:

```go
const microScale int64 = 1_000_000
```

The conversion functions:

```go
func toMicro(d decimal.Decimal) int64 {
    scaled := d.Mul(decimal.NewFromInt(microScale))
    if scaled.GreaterThan(decimal.NewFromInt(maxMicroValue)) ||
       scaled.LessThan(decimal.NewFromInt(-maxMicroValue)) {
        panic(fmt.Sprintf(
            "settla-treasury: amount %s overflows int64 micro-units (max ~$9.2T)",
            d.String()))
    }
    return scaled.IntPart()
}

func fromMicro(v int64) decimal.Decimal {
    return decimal.NewFromInt(v).Div(decimal.NewFromInt(microScale))
}
```

Why 10^6?

```
USDT on Tron:  6 decimal places  --> 10^6 gives exact representation
Fiat (USD):    2 decimal places  --> 10^6 gives 4 extra digits of precision
int64 max:     9,223,372,036,854,775,807
Max position:  9,223,372,036,854,775,807 / 1,000,000 = ~$9.2 trillion

Previous scale (10^8) capped at ~$92 billion -- too low for institutional flows.
```

The `validateMicroRange` function is the safe version for user-facing input:

```go
func validateMicroRange(amount decimal.Decimal) error {
    scaled := amount.Mul(decimal.NewFromInt(microScale))
    if scaled.GreaterThan(decimal.NewFromInt(maxMicroValue)) ||
       scaled.LessThan(decimal.NewFromInt(-maxMicroValue)) {
        return fmt.Errorf(
            "settla-treasury: amount %s exceeds maximum position size (~$9.2T)",
            amount.String())
    }
    return nil
}
```

The `toMicro` function panics on overflow because it is only called after
validation. `validateMicroRange` returns an error for user input paths.

Conversion examples (from `TestMicroConversion`):

```
Input          Micro-units          Round-trip
──────         ───────────          ──────────
"1"            1,000,000            "1"
"0.5"          500,000              "0.5"
"0.000001"     1                    "0.000001"
"1234.567890"  1,234,567,890        "1234.56789"  (truncated to 6dp)
"0"            0                    "0"
```

---

## The Reserve() CAS Loop -- Line by Line

Here is the actual `Reserve` function with annotations:

```go
func (m *Manager) Reserve(ctx context.Context, tenantID uuid.UUID,
    currency domain.Currency, location string, amount decimal.Decimal,
    reference uuid.UUID) error {

    start := time.Now()

    // Guard 1: Amount must be positive.
    if !amount.IsPositive() {
        return domain.ErrReservationFailed("amount must be positive")
    }

    // Guard 2: Amount must fit in int64 micro-units.
    if err := validateMicroRange(amount); err != nil {
        return err
    }

    // Guard 3: Circuit breaker -- reject during persistent DB outage.
    // If the background flush has failed 3+ times consecutively, new
    // reservations are rejected because they cannot be durably persisted.
    if failures := m.consecutiveFlushFailures.Load();
       failures >= maxFlushFailuresBeforeReject {
        return fmt.Errorf(
            "settla-treasury: rejecting reserve -- DB flush failing "+
            "(consecutive failures: %d)", failures)
    }

    // Guard 4: Idempotency -- skip if already reserved with this reference.
    // On failure below, we rollback so NATS redelivery can retry.
    if m.tryAcquireIdempotency(reference, "reserve") {
        return nil  // Already done. Return success without double-reserving.
    }

    // Lookup the position in the in-memory map.
    ps, err := m.getPosition(tenantID, currency, location)
    if err != nil {
        m.rollbackIdempotency(reference, "reserve")
        return err
    }

    amountMicro := toMicro(amount)

    // === THE CAS LOOP ===
    // Balance and locked are re-read on EVERY iteration to avoid stale-snapshot
    // races where two concurrent reserves both see the same available balance.
    for {
        balance := ps.balanceMicro.Load()
        locked := ps.lockedMicro.Load()
        currentReserved := ps.reservedMicro.Load()
        available := balance - locked - currentReserved

        // Check: is there enough available?
        if available < amountMicro {
            m.rollbackIdempotency(reference, "reserve")
            return domain.ErrInsufficientFunds(string(currency), location)
        }

        // Attempt atomic compare-and-swap:
        // "If reservedMicro is still currentReserved, set it to
        //  currentReserved + amountMicro. Return true if successful."
        if ps.reservedMicro.CompareAndSwap(
            currentReserved, currentReserved+amountMicro) {
            break  // CAS succeeded -- we own this reservation.
        }
        // CAS failed -- another goroutine changed reservedMicro between
        // our Load() and CompareAndSwap(). Loop re-reads everything.
    }

    // Mark position as needing flush to DB (uses a separate dirty set with its own mutex).
    m.markDirty(ps)

    // Log the operation for crash recovery (WAL write).
    // If logOp fails (channel full for >1s), roll back the reservation.
    if err := m.logOp(tenantID, currency, location, amount, reference, OpReserve); err != nil {
        ps.reservedMicro.Add(-amountMicro)
        m.rollbackIdempotency(reference, "reserve")
        return err
    }

    // For large amounts, flush synchronously to close the crash window.
    // Uses per-currency thresholds (e.g., $100K USD, £80K GBP, ₦160M NGN).
    // If the flush fails, roll back the reservation.
    if threshold := m.syncThresholdFor(currency);
       !threshold.IsZero() && amount.GreaterThanOrEqual(threshold) {
        if err := m.syncFlushPosition(ps); err != nil {
            ps.reservedMicro.Add(-amountMicro)
            m.rollbackIdempotency(reference, "reserve")
            return fmt.Errorf(
                "settla-treasury: rejecting large reserve (%s) — sync flush failed: %w",
                amount.String(), err)
        }
    }

    // Metrics.
    if m.metrics != nil {
        m.metrics.TreasuryReserveTotal.WithLabelValues(
            tenantID.String(), string(currency)).Inc()
        m.metrics.TreasuryReserveLatency.Observe(
            time.Since(start).Seconds())
    }

    // Alert if remaining balance is below minimum threshold.
    remaining := fromMicro(
        ps.balanceMicro.Load() - ps.lockedMicro.Load() - ps.reservedMicro.Load())
    if remaining.LessThan(ps.MinBalance) && !ps.MinBalance.IsZero() {
        m.publishLiquidityAlert(ctx, ps)
    }

    return nil
}
```

### CAS Loop Trace

Here is what happens when 3 goroutines race to reserve from the same position
(balance=10,000, locked=0, reserved=0):

```
                G1 (reserve 3000)       G2 (reserve 2000)       G3 (reserve 6000)

Load all:       bal=10000,locked=0      bal=10000,locked=0      bal=10000,locked=0
                reserved=0              reserved=0              reserved=0
Calc available: 10000                   10000                   10000
Check >= amt:   10000 >= 3000 YES       10000 >= 2000 YES       10000 >= 6000 YES

CAS(0, 3000):   SUCCESS (reserved=3000)
CAS(0, 2000):                           FAIL (reserved is now 3000, not 0)
CAS(0, 6000):                                                   FAIL

Re-read all:                            bal=10000,locked=0      bal=10000,locked=0
                                        reserved=3000           reserved=3000
Calc available:                         7000                    7000
Check >= amt:                           7000 >= 2000 YES        7000 >= 6000 YES

CAS(3000, 5000):                        SUCCESS (reserved=5000)
CAS(3000, 9000):                                                FAIL

Re-read all:                                                    bal=10000,locked=0
                                                                reserved=5000
Calc available:                                                 5000
Check >= amt:                                                   5000 >= 6000 NO
Return:                                                         ErrInsufficientFunds
Rollback idemp:                                                 (key removed)

Final state: balance=10000, locked=0, reserved=5000, available=5000
```

Critical observation: G3 correctly rejected even though it initially saw
enough balance. The CAS loop guarantees that **available is checked atomically
with the reservation**.

---

## Release()

Release reverses a reservation when a transfer fails:

```go
func (m *Manager) Release(ctx context.Context, tenantID uuid.UUID,
    currency domain.Currency, location string, amount decimal.Decimal,
    reference uuid.UUID) error {

    if !amount.IsPositive() {
        return domain.ErrReservationFailed("amount must be positive")
    }
    if err := validateMicroRange(amount); err != nil {
        return err
    }
    if m.tryAcquireIdempotency(reference, "release") {
        return nil
    }

    ps, err := m.getPosition(tenantID, currency, location)
    if err != nil {
        m.rollbackIdempotency(reference, "release")
        return err
    }

    amountMicro := toMicro(amount)

    // CAS loop: atomically decrement reserved.
    for {
        currentReserved := ps.reservedMicro.Load()
        if currentReserved < amountMicro {
            m.rollbackIdempotency(reference, "release")
            return domain.ErrReservationFailed("release amount exceeds reserved")
        }
        if ps.reservedMicro.CompareAndSwap(
            currentReserved, currentReserved-amountMicro) {
            break
        }
    }

    ps.dirty.Store(true)
    m.logOp(tenantID, currency, location, amount, reference, OpRelease)
    return nil
}
```

The guard `currentReserved < amountMicro` prevents underflow. If you try to
release more than is reserved, the operation fails with a clear error.

---

## CommitReservation()

When a transfer succeeds, the reservation moves from `reserved` to `locked`:

```go
func (m *Manager) CommitReservation(ctx context.Context, tenantID uuid.UUID,
    currency domain.Currency, location string, amount decimal.Decimal,
    reference uuid.UUID) error {

    if !amount.IsPositive() {
        return domain.ErrReservationFailed("amount must be positive")
    }
    if m.tryAcquireIdempotency(reference, "commit") {
        return nil
    }

    ps, err := m.getPosition(tenantID, currency, location)
    if err != nil {
        m.rollbackIdempotency(reference, "commit")
        return err
    }

    amountMicro := toMicro(amount)

    // Step 1: Decrement reserved FIRST.
    for {
        currentReserved := ps.reservedMicro.Load()
        if currentReserved < amountMicro {
            m.rollbackIdempotency(reference, "commit")
            return fmt.Errorf(
                "settla-treasury: insufficient reserved amount: "+
                "have %d, need %d (transfer %s)",
                currentReserved, amountMicro, reference)
        }
        if ps.reservedMicro.CompareAndSwap(
            currentReserved, currentReserved-amountMicro) {
            break
        }
    }

    // Step 2: Increment locked (always succeeds -- simple atomic add).
    ps.lockedMicro.Add(amountMicro)

    ps.dirty.Store(true)
    m.logOp(tenantID, currency, location, amount, reference, OpCommit)
    return nil
}
```

### Why Decrement Reserved Before Incrementing Locked?

The ordering is deliberate. If the process crashes between Step 1 and Step 2:

```
Scenario A: reserved FIRST, then locked (Settla's approach)
    Crash between steps:
    reserved decreased, locked NOT increased
    Available = balance - locked - reserved  (HIGHER than expected)
    Result: briefly permissive (a new reservation might succeed that shouldn't)
    Recovery: WAL replay restores correct state

Scenario B: locked FIRST, then reserved (DANGEROUS)
    Crash between steps:
    locked increased, reserved NOT decreased
    Available = balance - (locked+amount) - reserved  (LOWER than expected)
    Result: funds double-counted (both locked AND reserved)
    Recovery: money "disappears" from available until manual intervention
```

Settla chooses to be **briefly permissive** on crash rather than **permanently
restrictive**. Permissive is self-healing; restrictive requires manual
intervention.

---

## Why CAS Beats a Mutex

An alternative design uses `sync.Mutex`:

```go
// MUTEX APPROACH (not used)
func (m *Manager) ReserveWithMutex(amount int64) error {
    ps.mu.Lock()
    defer ps.mu.Unlock()

    available := ps.balance - ps.locked - ps.reserved
    if available < amount {
        return ErrInsufficientFunds
    }
    ps.reserved += amount
    return nil
}
```

Both are correct. The difference is throughput under contention:

```
Approach     Contention Model         Under 100 goroutines
──────────   ────────────────         ────────────────────
Mutex        Queue (FIFO)             99 goroutines blocked, 1 runs
             O(1) per operation       But: OS scheduler must wake each
                                      goroutine sequentially

CAS loop     Optimistic retry         100 goroutines all execute simultaneously
             O(retries) per op        1 wins the CAS, 99 re-read and retry
                                      immediately (no scheduler involvement)
```

The key advantage: **CAS never blocks the OS thread**. A goroutine that fails
a CAS immediately retries with fresh values. With a mutex, a goroutine that
fails to acquire must be parked by the Go runtime, enqueued, and later woken
-- each transition involves OS scheduler overhead.

For Settla's workload (~50 positions, ~100 concurrent reservations per position),
CAS typically completes in 1-3 iterations. The worst case (all 100 goroutines
fail simultaneously) still completes in microseconds, not milliseconds.

Benchmark from `treasury/manager_test.go`:

```go
func BenchmarkReserve(b *testing.B) {
    // Setup: 1 position with $1 billion balance
    // Parallel benchmark with b.RunParallel
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            _ = m.Reserve(ctx, tenantID, domain.CurrencyUSD,
                "bank:chase", amount, uuid.New())
        }
    })
}
// Result: ~150-300 ns/op across 8+ cores
```

---

## The Idempotency Map

NATS uses at-least-once delivery. The TreasuryWorker might receive the same
reserve command twice. The idempotency map prevents double-reservation.

At 5,000+ TPS with 3 ops per transfer (reserve, commit/release, plus events),
a single global mutex becomes a bottleneck. Settla uses **sharded locking**:
16 independent shards selected by FNV-32a hash, reducing contention by 16x:

```go
type idempotencyShard struct {
    mu    sync.Mutex
    items map[string]time.Time
}

// Manager holds 16 shards instead of one global map.
type Manager struct {
    idempotencyShards [16]idempotencyShard
    // ...
}

func (m *Manager) tryAcquireIdempotency(reference uuid.UUID, op string) bool {
    key := idempotencyKey(reference, op)
    idx := idempotencyShardFor(key)
    shard := &m.idempotencyShards[idx]

    shard.mu.Lock()
    defer shard.mu.Unlock()

    if _, exists := shard.items[key]; exists {
        return true   // Already processed -- caller should return nil
    }
    // If this shard exceeds its share of the size limit, trigger a forced
    // cleanup with a short TTL before adding.
    if len(shard.items) >= maxIdempotencyMapSize/16 {
        forcedCutoff := time.Now().Add(-forcedCleanupMaxAge)
        for k, t := range shard.items {
            if t.Before(forcedCutoff) {
                delete(shard.items, k)
            }
        }
    }
    shard.items[key] = time.Now()
    return false  // First time -- caller should proceed
}
```

The shard selection uses FNV-32a hashing:

```go
func idempotencyShardFor(key string) int {
    h := fnv.New32a()
    _, _ = h.Write([]byte(key))
    return int(h.Sum32() % 16)
}
```

Critical detail: on failure, the idempotency key is **rolled back**:

```go
ps, err := m.getPosition(tenantID, currency, location)
if err != nil {
    m.rollbackIdempotency(reference, "reserve")  // Allow retry!
    return err
}
```

This ensures NATS redelivery can retry a legitimately failed operation (e.g.,
position not found because it was not yet loaded), while preventing duplicate
application of a successful operation.

The idempotency map is cleaned up periodically with a two-tier strategy:

```go
const maxIdempotencyMapSize = 500_000   // ~33 seconds at peak load
const forcedCleanupMaxAge = 2 * time.Minute

func (m *Manager) cleanupIdempotencyMap(maxAge time.Duration) {
    cutoff := time.Now().Add(-maxAge)
    shardLimit := maxIdempotencyMapSize / 16

    for i := range m.idempotencyShards {
        shard := &m.idempotencyShards[i]
        shard.mu.Lock()
        // Pass 1: remove entries older than maxAge (10 minutes)
        for k, t := range shard.items {
            if t.Before(cutoff) {
                delete(shard.items, k)
            }
        }
        // Pass 2: if still over limit, force aggressive cleanup (2 min TTL)
        if len(shard.items) > shardLimit {
            forcedCutoff := time.Now().Add(-forcedCleanupMaxAge)
            for k, t := range shard.items {
                if t.Before(forcedCutoff) {
                    delete(shard.items, k)
                }
            }
        }
        shard.mu.Unlock()
    }
}
```

Entries older than 10 minutes are purged on the normal schedule. If a shard
still exceeds its capacity share (500,000 / 16 = 31,250), a forced cleanup
with a 2-minute TTL kicks in. This covers the NATS dedup window while
preventing unbounded memory growth under sustained peak load.

---

## The Position Map

Positions are stored in a map keyed by a composite struct, with a secondary
index for O(1) tenant lookups:

```go
type positionKey struct {
    TenantID uuid.UUID
    Currency string
    Location string
}

type Manager struct {
    positions   map[positionKey]*PositionState
    tenantIndex map[uuid.UUID][]*PositionState // O(1) tenant lookup, avoids full-scan
    mu          sync.RWMutex                   // protects map reads/writes, NOT individual positions

    // dirtySet tracks positions modified since the last flush. Only these
    // positions are written to Postgres -- avoids scanning all positions.
    dirtySet map[positionKey]*PositionState
    dirtyMu  sync.Mutex  // separate lock so Reserve/Release don't contend with flush
    // ...
}
```

The `mu` RWMutex protects **map access** (adding/removing positions), not
individual position operations. The `dirtyMu` is a separate mutex so that
marking a position dirty (which happens on every Reserve/Release) does not
contend with the position map lock. Position reads and writes use atomic
operations, so concurrent reservations on the same position never touch
either mutex:

```go
func (m *Manager) getPosition(tenantID uuid.UUID,
    currency domain.Currency, location string) (*PositionState, error) {

    key := positionKey{TenantID: tenantID, Currency: string(currency),
        Location: location}
    m.mu.RLock()          // Read lock -- concurrent with other reads
    ps, ok := m.positions[key]
    m.mu.RUnlock()
    if !ok {
        return nil, domain.ErrInsufficientFunds(string(currency), location)
    }
    return ps, nil        // Caller operates on ps via atomics, no lock held
}
```

---

## Key Insight

> The treasury manager separates the concurrency problem into two layers:
> a read-locked map for position lookup (cold, infrequent changes) and
> lock-free atomic counters for balance operations (hot, every request).
> This layered approach means the map mutex is never held during a reservation,
> and the reservation itself never blocks any other goroutine.

---

## Common Mistakes

1. **Using `atomic.Int64` for the map itself.** Go maps are not safe for
   concurrent access even with atomic values inside. The `sync.RWMutex`
   on the map is necessary; the atomics are for the values within each
   position.

2. **Forgetting to re-read balance and locked in the CAS retry.** If
   `balanceMicro` or `lockedMicro` changed between iterations, using stale
   values could allow over-reservation. Settla re-reads all three counters
   on retry.

3. **Using `atomic.Add` instead of CAS for Reserve.** `Add` always succeeds,
   which means it could push `reserved` past `available`. CAS checks the
   current value first, so it rejects when insufficient.

4. **Panicking on overflow in the hot path.** `toMicro` panics because it is
   only called after `validateMicroRange`. Never call `toMicro` on unvalidated
   user input.

5. **Assuming `Available()` is a consistent snapshot.** The three `Load()`
   calls in `Available()` are not atomic as a group. Between loading `balance`
   and `reserved`, another goroutine might change `reserved`. This is
   acceptable because `Available()` is used for the CAS loop's check (which
   retries on conflict) and for display/reporting. For external APIs that need
   a consistent point-in-time view, use the `snapshot()` method instead --
   it takes `RLock` to read all three counters atomically:

   ```go
   func (ps *PositionState) snapshot() domain.Position {
       ps.mu.RLock()
       b := ps.balanceMicro.Load()
       l := ps.lockedMicro.Load()
       r := ps.reservedMicro.Load()
       ps.mu.RUnlock()

       return domain.Position{
           ID:            ps.ID,
           TenantID:      ps.TenantID,
           Currency:      ps.Currency,
           Location:      ps.Location,
           Balance:       fromMicro(b),
           Locked:        fromMicro(l + r),  // External view combines locked + reserved
           MinBalance:    ps.MinBalance,
           TargetBalance: ps.TargetBalance,
           UpdatedAt:     time.Now().UTC(),
       }
   }
   ```

   Note that `snapshot()` combines `locked + reserved` into a single `Locked`
   field for the external view. External consumers do not need to distinguish
   between committed-locked and tentatively-reserved -- both represent funds
   that are not currently available.

---

## Exercises

### Exercise 4.2.1: Trace the CAS Loop

Given a position with balance=50,000, locked=10,000, reserved=5,000:

Three goroutines attempt simultaneously:
- G1: Reserve 20,000
- G2: Reserve 15,000
- G3: Reserve 25,000

Trace the CAS loop for each goroutine. Which succeed? What is the final state?

### Exercise 4.2.2: Micro-Unit Edge Cases

Calculate the micro-unit representation for:
1. $0.01 (minimum fiat precision)
2. 0.000001 USDT (minimum Tron precision)
3. $9,000,000,000,000 (near the ceiling)
4. $9,300,000,000,000 (over the ceiling)

For case 4, what happens if you call `toMicro`? What if you call
`validateMicroRange`?

### Exercise 4.2.3: Design a Mutex-Based Alternative

Rewrite `Reserve()` using a per-position `sync.Mutex` instead of CAS.
Compare:
1. Lines of code
2. Error handling complexity
3. Expected throughput under 100 concurrent goroutines

Which do you prefer and why?

---

## What's Next

The in-memory approach achieves nanosecond latency, but what happens when the
process crashes? Chapter 4.3 explores Settla's crash recovery model: the WAL
pattern, synchronous flush for large amounts, the background flush goroutine,
and startup replay.

---

## Further Reading

- Go documentation on [sync/atomic](https://pkg.go.dev/sync/atomic)
- Preshing on Programming, "An Introduction to Lock-Free Programming"
- `treasury/position_state.go` for the full `PositionState` definition and `snapshot()` method
- `treasury/manager.go` for the sharded idempotency map and CAS-loop Reserve implementation
- `treasury/manager_test.go` `TestConcurrentReserves` for the 10,000-goroutine test
