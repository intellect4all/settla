# Chapter 4.3: Crash Recovery

**Reading time:** ~25 minutes
**Prerequisites:** Chapter 4.2 (In-Memory Treasury), understanding of WALs
**Code references:** `treasury/manager.go`, `treasury/flush.go`, `treasury/loader.go`, `treasury/store.go`, `treasury/event_writer.go`, `domain/position_event.go`

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the three layers of Settla's treasury crash recovery model
2. Trace the `logOp()` function and its synchronous WAL write
3. Describe the `syncFlushPosition()` threshold for large amounts
4. Walk through the background flush goroutine's 100ms cycle
5. Trace `LoadPositions()` startup recovery including op replay
6. Explain how idempotency map cleanup prevents unbounded memory growth
7. Describe the Position Event Writer and its role as an immutable audit trail

---

## The Crash Recovery Problem

Moving treasury state to memory creates a new problem: what happens when the
process crashes?

```
Time 0:  Reserve($50,000) succeeds in memory
Time 1:  CAS loop completes, available reduced
Time 2:  *** PROCESS CRASH ***
Time 3:  Process restarts, loads from DB
Time 4:  DB still shows old balance -- $50,000 reservation lost!

Consequence: The same funds can be reserved again.
             Two transfers use the same $50,000.
             Treasury is over-committed.
```

Settla uses a three-layer defense:

```
Layer 1: WAL (Write-Ahead Log)
    Every reserve/release/commit is written to DB BEFORE
    the in-memory CAS result is considered committed.

Layer 2: Sync Flush (for large amounts)
    Reservations >= per-currency threshold (e.g., $100K USD,
    £80K GBP, ₦160M NGN) immediately flush the position
    to DB after the CAS succeeds.

Layer 3: Background Flush (for all amounts)
    Every 100ms, all dirty positions are flushed to DB.
    Maximum data loss window: 100ms of small reservations.
```

```
                +-----------+
                |  Reserve  |
                +-----------+
                      |
         +------------+------------+
         |                         |
    Layer 1: WAL              In-memory CAS
    (synchronous DB write)    (nanoseconds)
         |                         |
         v                         v
    reserve_ops table         reservedMicro updated
         |                         |
         |    +--------------------+
         |    |
         v    v
    Amount >= $100K?
         |
    YES: Layer 2 (sync flush position to DB)
    NO:  Layer 3 (wait for background flush, max 100ms)
```

---

## Layer 1: The WAL -- logOp()

The `logOp` function writes every operation to the database synchronously
before the in-memory state is considered committed. Here is the actual
implementation:

```go
func (m *Manager) logOp(tenantID uuid.UUID, currency domain.Currency,
    location string, amount decimal.Decimal, reference uuid.UUID,
    opType ReserveOpType) error {

    op := ReserveOp{
        ID:        uuid.New(),
        TenantID:  tenantID,
        Currency:  currency,
        Location:  location,
        Amount:    amount,
        Reference: reference,
        OpType:    opType,
        CreatedAt: time.Now().UTC(),
    }

    // WAL: synchronous write to DB if store supports it.
    if opStore, ok := m.store.(ReserveOpStore); ok {
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        if err := opStore.LogReserveOp(ctx, op); err != nil {
            m.logger.Warn(
                "settla-treasury: WAL write failed, falling back to channel-only",
                "op_type", opType,
                "reference", reference,
                "error", err,
            )
        }
    }

    // Queue to async channel for batch processing by flushOnce.
    // Block up to 1 second if the channel is full -- failing the operation
    // is safer than silently dropping the WAL entry.
    select {
    case m.pendingOps <- op:
        return nil
    default:
        timer := time.NewTimer(pendingOpsTimeout)  // 1 second
        defer timer.Stop()
        select {
        case m.pendingOps <- op:
            return nil
        case <-timer.C:
            m.logger.Error(
                "settla-treasury: pending ops channel full for >1s, failing operation",
                "op_type", opType,
                "reference", reference,
            )
            return fmt.Errorf(
                "settla-treasury: pending ops channel full, cannot queue %s for %s",
                opType, reference)
        }
    }
}
```

### The ReserveOp Struct

Each operation is recorded as:

```go
type ReserveOp struct {
    ID        uuid.UUID
    TenantID  uuid.UUID
    Currency  domain.Currency
    Location  string
    Amount    decimal.Decimal
    Reference uuid.UUID        // Transfer ID -- used for matching reserve/commit/release
    OpType    ReserveOpType    // "reserve", "release", or "commit"
    CreatedAt time.Time
}
```

The `Reference` field links related operations. A reserve and its matching
commit share the same `Reference` (the transfer ID). This is how
`GetUncommittedOps` knows which reserves have been resolved.

### The ReserveOpStore Interface

The WAL capability is optional, expressed as a separate interface:

```go
type ReserveOpStore interface {
    LogReserveOp(ctx context.Context, op ReserveOp) error
    LogReserveOps(ctx context.Context, ops []ReserveOp) error
    GetUncommittedOps(ctx context.Context) ([]ReserveOp, error)
    MarkOpCompleted(ctx context.Context, opID uuid.UUID) error
    CleanupOldOps(ctx context.Context, before time.Time) error
}
```

If the store does not implement `ReserveOpStore`, the WAL write is skipped
and the op is only queued to the async channel. This allows testing with
simple mock stores while production uses the full WAL.

### Dual Write Path

The operation is written twice:

```
1. Synchronous: LogReserveOp (WAL -- durable, survives crash)
2. Async:       pendingOps channel (batch processing, best-effort)
```

The async channel (capacity 10,000) is drained by the background flush. If
the channel is full under extreme load, the caller blocks for up to 1 second
waiting for space. If the timeout expires, `logOp` returns an error and the
calling Reserve/Release/Commit rolls back the in-memory state. This is safer
than silently dropping the WAL entry -- the operation fails cleanly and NATS
redelivery will retry it.

### Graceful Degradation

If the WAL write fails (DB timeout, connection error), the operation is not
rolled back -- it falls back to channel-only mode. The full degradation model:

```
WAL write fails         Channel queue fails (full for >1s)
    |                       |
    +-> Log warning         +-> Return error to caller
    +-> Queue to channel    +-> Caller rolls back CAS
    +-> In-memory state     +-> NATS redelivery retries
        still updated
```

For WAL failures, the system prioritizes availability over perfect durability
of small reservations. Large reservations get Layer 2 (sync flush) as
additional protection, and the sync flush failure path is strict: the
reservation is rolled back and an error is returned.

---

## Layer 2: Sync Flush for Large Amounts

For reservations at or above the sync threshold (default $100,000), Settla
immediately flushes the entire position to the database:

```go
// In Reserve(), after CAS succeeds:
if !m.syncThreshold.IsZero() && amount.GreaterThanOrEqual(m.syncThreshold) {
    m.syncFlushPosition(ps)
}
```

The sync flush implementation:

```go
func (m *Manager) syncFlushPosition(ps *PositionState) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    balance := fromMicro(ps.balanceMicro.Load())
    locked := fromMicro(ps.lockedMicro.Load())

    if err := m.store.UpdatePosition(ctx, ps.ID, balance, locked); err != nil {
        m.logger.Error(
            "settla-treasury: sync flush failed for large reservation "+
            "(WAL ensures recovery)",
            "position_id", ps.ID,
            "error", err,
        )
        return
    }
    ps.dirty.Store(false)
}
```

Why a threshold?

```
$100 reservation:   If lost in a crash, impact is minimal.
                    Background flush (100ms) provides acceptable durability.

$500,000 reservation: If lost in a crash, the position is significantly
                      over-committed. Immediate flush is worth the ~1ms
                      latency cost.
```

The threshold is configurable:

```go
func WithSyncThreshold(amount decimal.Decimal) Option {
    return func(m *Manager) {
        m.syncThreshold = amount
    }
}
// Set to 0 to disable sync flush (testing only).
```

Even if the sync flush fails, Layer 1 (WAL) ensures the operation can be
replayed on restart.

---

## Layer 3: Background Flush Goroutine

The flush goroutine runs every 100ms (configurable):

```go
func (m *Manager) Start() {
    go m.flushLoop()
}

func (m *Manager) Stop() {
    close(m.stopCh)
    <-m.doneCh   // Block until final flush completes
}
```

### The Flush Loop

```go
func (m *Manager) flushLoop() {
    defer close(m.doneCh)

    ticker := time.NewTicker(m.flushInterval)
    defer ticker.Stop()

    tickCount := 0
    for {
        select {
        case <-ticker.C:
            m.flushOnce()
            tickCount++
            if tickCount%idempotencyCleanupInterval == 0 {  // every 60 ticks
                m.cleanupIdempotencyMap(idempotencyMaxAge)   // 10 min max age
            }
            if tickCount%reserveOpsCleanupInterval == 0 {   // every 600 ticks
                m.cleanupOldReserveOps()                     // 1 hour max age
            }
        case <-m.stopCh:
            m.flushOnce()  // Final flush before shutdown
            return
        }
    }
}
```

Three things happen on the ticker:

```
Every 100ms:         flushOnce()
Every 6 seconds:     cleanupIdempotencyMap() (60 ticks x 100ms)
Every 60 seconds:    cleanupOldReserveOps()  (600 ticks x 100ms)
```

### flushOnce() in Detail

```go
func (m *Manager) flushOnce() {
    start := time.Now()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Step 1: Drain the async ops channel.
    m.drainPendingOps(ctx)

    // Step 2: Find dirty positions.
    dirty := m.dirtyPositions()
    if len(dirty) == 0 {
        return
    }

    // Step 3: Flush each dirty position to DB.
    flushed := 0
    hadError := false
    for _, ps := range dirty {
        balance := fromMicro(ps.balanceMicro.Load())
        // CRITICAL: Flush only committed locked -- NOT reserved.
        locked := fromMicro(ps.lockedMicro.Load())

        if err := m.store.UpdatePosition(ctx, ps.ID, balance, locked); err != nil {
            hadError = true
            m.logger.Error("settla-treasury: flush position update failed",
                "position_id", ps.ID,
                "tenant_id", ps.TenantID,
                "error", err,
            )
            continue  // Don't clear dirty -- retry next interval
        }

        // Best-effort audit history
        if err := m.store.RecordHistory(ctx, ps.ID, ps.TenantID,
            balance, locked, "flush"); err != nil {
            m.logger.Error("settla-treasury: flush history write failed",
                "position_id", ps.ID,
                "error", err,
            )
        }

        ps.dirty.Store(false)  // Clear dirty flag on success only
        flushed++
    }

    // Track consecutive failures for circuit breaker
    if hadError {
        failures := m.consecutiveFlushFailures.Add(1)
        if failures >= 5 {
            m.logger.Error(
                "settla-treasury: persistent flush failures",
                "consecutive_failures", failures,
            )
        }
    } else {
        m.consecutiveFlushFailures.Store(0)
    }
}
```

### Why Not Flush Reserved?

This is the most subtle part of the crash recovery design. The comment in
the code explains:

```
// Flush only committed locked -- NOT reserved. Reserved amounts are
// reconstructed from reserve_ops on crash recovery. This prevents
// double-counting: if we flushed locked+reserved and then replayed
// reserve ops on restart, the reserved amount would be counted twice.
```

Diagram of what would go wrong if we flushed reserved:

```
Before crash:
    DB:      locked=5000  (flushed value includes reserved)
    Memory:  locked=3000, reserved=2000  (total=5000 "held")

After crash, restart:
    LoadPositions reads from DB: locked=5000
    GetUncommittedOps returns:   reserve op for $2000

    replayOp(reserve $2000):
        reservedMicro += 2000

    Final state:
        locked=5000, reserved=2000
        Available = balance - 5000 - 2000  (WRONG: $2000 counted twice!)
```

By flushing only committed `locked`, the replay correctly reconstructs
the reserved amount:

```
Before crash:
    DB:      locked=3000  (committed only)
    Memory:  locked=3000, reserved=2000

After crash, restart:
    LoadPositions reads from DB: locked=3000
    GetUncommittedOps returns:   reserve op for $2000

    replayOp(reserve $2000):
        reservedMicro += 2000

    Final state:
        locked=3000, reserved=2000
        Available = balance - 3000 - 2000  (CORRECT)
```

---

## Startup Recovery: LoadPositions()

When the process restarts, `LoadPositions` reconstructs in-memory state:

```go
func (m *Manager) LoadPositions(ctx context.Context) error {
    start := time.Now()

    // Step 1: Load all positions from Postgres.
    positions, err := m.store.LoadAllPositions(ctx)
    if err != nil {
        return fmt.Errorf("settla-treasury: loading positions from store: %w", err)
    }

    // Step 2: Clear idempotency map (fresh start).
    m.idempotencyMu.Lock()
    m.idempotencyMap = make(map[string]time.Time)
    m.idempotencyMu.Unlock()

    // Step 3: Populate the in-memory position map.
    for _, pos := range positions {
        m.addPosition(pos)
    }

    // Step 4: Replay uncommitted reserve ops (crash recovery).
    replayed := 0
    if opStore, ok := m.store.(ReserveOpStore); ok {
        ops, err := opStore.GetUncommittedOps(ctx)
        if err != nil {
            m.logger.Error(
                "settla-treasury: failed to load uncommitted ops for replay",
                "error", err,
            )
            // Non-fatal: ops will be retried by NATS workers
        } else {
            for _, op := range ops {
                if err := m.replayOp(ctx, op); err != nil {
                    m.logger.Warn("settla-treasury: failed to replay op",
                        "op_id", op.ID,
                        "op_type", op.OpType,
                        "reference", op.Reference,
                        "error", err,
                    )
                } else {
                    replayed++
                }
            }
        }
    }

    m.logger.Info("settla-treasury: loaded positions into memory",
        "count", len(positions),
        "replayed_ops", replayed,
        "duration", time.Since(start),
    )
    return nil
}
```

### The addPosition Function

Each position is initialized with `reservedMicro = 0` because reserved
amounts are reconstructed from the WAL:

```go
func (m *Manager) addPosition(pos domain.Position) {
    key := positionKey{
        TenantID: pos.TenantID,
        Currency: string(pos.Currency),
        Location: pos.Location,
    }
    ps := &PositionState{
        ID:            pos.ID,
        TenantID:      pos.TenantID,
        Currency:      pos.Currency,
        Location:      pos.Location,
        MinBalance:    pos.MinBalance,
        TargetBalance: pos.TargetBalance,
    }
    ps.balanceMicro.Store(toMicro(pos.Balance))
    ps.lockedMicro.Store(toMicro(pos.Locked))
    ps.reservedMicro.Store(0)  // Reconstructed from WAL replay

    m.mu.Lock()
    m.positions[key] = ps
    m.mu.Unlock()
}
```

### GetUncommittedOps: Self-Healing Query

The `GetUncommittedOps` query returns only reserve ops that do NOT have a
matching commit or release. From the mock implementation (mirrors the
production SQL):

```go
func (s *mockStore) GetUncommittedOps(_ context.Context) ([]ReserveOp, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // Find all references that have been resolved (commit or release).
    resolved := make(map[uuid.UUID]bool)
    for _, op := range s.ops {
        if op.OpType == OpCommit || op.OpType == OpRelease {
            resolved[op.Reference] = true
        }
    }

    // Return only reserve ops without a matching resolution.
    var result []ReserveOp
    for _, op := range s.ops {
        if op.OpType == OpReserve && !resolved[op.Reference] {
            result = append(result, op)
        }
    }
    return result, nil
}
```

This is self-healing: if a reserve was committed before the crash, it will
NOT be replayed. If it was released, it will NOT be replayed. Only genuinely
uncommitted reservations are reconstructed.

### replayOp: Uses the Same Code Path

```go
func (m *Manager) replayOp(ctx context.Context, op ReserveOp) error {
    switch op.OpType {
    case OpReserve:
        return m.Reserve(ctx, op.TenantID, op.Currency,
            op.Location, op.Amount, op.Reference)
    case OpRelease:
        return m.Release(ctx, op.TenantID, op.Currency,
            op.Location, op.Amount, op.Reference)
    case OpCommit:
        return m.CommitReservation(ctx, op.TenantID, op.Currency,
            op.Location, op.Amount, op.Reference)
    default:
        return fmt.Errorf("settla-treasury: unknown op type: %s", op.OpType)
    }
}
```

The replay calls the same `Reserve`/`Release`/`CommitReservation` functions,
which means:
- Idempotency is enforced (if NATS also redelivers, no double-application)
- All validation runs (amount checks, balance checks)
- WAL entries are re-logged (harmless duplicates)

---

## Idempotency Map Cleanup

The idempotency map grows with every operation. Without cleanup, it would
consume unbounded memory:

```go
const idempotencyCleanupInterval = 60   // every 60 flush ticks (6 seconds)
const idempotencyMaxAge = 10 * time.Minute

func (m *Manager) cleanupIdempotencyMap(maxAge time.Duration) {
    cutoff := time.Now().Add(-maxAge)
    m.idempotencyMu.Lock()
    for k, t := range m.idempotencyMap {
        if t.Before(cutoff) {
            delete(m.idempotencyMap, k)
        }
    }
    m.idempotencyMu.Unlock()
}
```

The 10-minute retention covers the NATS redelivery window. After 10 minutes,
if NATS redelivers, the worker will process it as a new operation. This is
safe because:

1. The original reservation was either committed or released by then
2. If it was committed, the transfer is complete -- re-reserving is harmless
   (the position's locked amount reflects reality)
3. If it was released, re-reserving is correct behavior (retrying a failed
   transfer)

Reserve ops in the database are cleaned up on a longer schedule:

```go
const reserveOpsCleanupInterval = 600  // every 600 ticks (~60s at 100ms)
const reserveOpsMaxAge = 1 * time.Hour
```

---

## Circuit Breaker: Persistent DB Outage

If the background flush fails repeatedly, the treasury enters a degraded mode:

```go
const maxFlushFailuresBeforeReject = 3

// In Reserve():
if failures := m.consecutiveFlushFailures.Load();
   failures >= maxFlushFailuresBeforeReject {
    return fmt.Errorf(
        "settla-treasury: rejecting reserve -- DB flush failing "+
        "(consecutive failures: %d)", failures)
}
```

After 3 consecutive flush failures:
- New reservations are rejected (fail fast)
- Existing in-memory state remains authoritative
- The flush goroutine continues retrying every 100ms
- When DB recovers, `consecutiveFlushFailures` resets to 0
- New reservations are accepted again

This prevents accepting reservations that cannot be durably persisted. Without
this circuit breaker, a prolonged DB outage could accumulate minutes of
un-flushed reservations that would all be lost on crash.

---

## Full Recovery Timeline

```
Time    Event                              DB State             Memory State
────    ─────                              ────────             ────────────
T+0     Process starts
T+1     LoadAllPositions()                 balance=100K         balance=100K
                                           locked=20K           locked=20K
T+2     GetUncommittedOps()                                     reserved=0
        returns [reserve $5K ref:A]
T+3     replayOp(reserve $5K ref:A)                             reserved=5K

T+10    Normal operation begins
T+11    Reserve($3K ref:B)                 WAL: reserve B       reserved=8K
T+12    Reserve($2K ref:C)                 WAL: reserve C       reserved=10K
T+100   flushOnce()                        locked=20K (no chg)  dirty cleared

T+200   CommitReservation($5K ref:A)       WAL: commit A        reserved=5K
                                                                 locked=25K
T+300   flushOnce()                        locked=25K           dirty cleared

T+350   *** CRASH ***

T+400   Process restarts
T+401   LoadAllPositions()                 balance=100K         balance=100K
                                           locked=25K           locked=25K
T+402   GetUncommittedOps()                                     reserved=0
        returns [reserve $3K ref:B,
                 reserve $2K ref:C]
        (ref:A excluded -- has matching commit)
T+403   replayOp(reserve $3K ref:B)                             reserved=3K
T+404   replayOp(reserve $2K ref:C)                             reserved=5K

Final:  Available = 100K - 25K - 5K = 70K  (CORRECT)
```

---

## Key Insight

> Crash recovery is not a single mechanism but a layered defense: synchronous
> WAL for every operation, immediate flush for large amounts, and periodic
> background flush for everything else. The critical design decision is to
> flush only committed `locked` to the database -- never `reserved`. This
> allows the WAL replay to correctly reconstruct reserved amounts without
> double-counting.

---

## Common Mistakes

1. **Flushing reserved to the database.** This causes double-counting on
   restart when reserve ops are replayed. Only committed locked should be
   flushed.

2. **Making the WAL write blocking on the hot path.** Settla's `logOp` writes
   to the WAL synchronously but does not block the `Reserve` return on WAL
   failure. If the WAL write fails, the operation still succeeds in memory.

3. **Skipping cleanup of the idempotency map.** Without cleanup, the map
   grows by ~580 entries per second (one per transfer). After an hour, that
   is 2 million entries consuming ~200MB of memory.

4. **Not handling the channel-full case.** The `pendingOps` channel has a
   capacity of 10,000. Under extreme load, it could fill up. The `select`
   with `default` ensures the hot path never blocks, even if ops are dropped
   from the channel (they survive in the WAL).

5. **Testing without crash recovery.** The `TestCrashRecoveryAfterCommit`
   and `TestCrashRecoveryAfterRelease` tests in `manager_test.go` verify
   that committed/released ops are NOT replayed. These edge cases are
   critical for correctness.

---

## Exercises

### Exercise 4.3.1: Crash Scenario Analysis

Given:
- DB state: balance=500K, locked=100K
- WAL contains: reserve $50K (ref:X), reserve $30K (ref:Y), commit $50K (ref:X)

After crash and restart:
1. What does `LoadAllPositions` return?
2. What does `GetUncommittedOps` return?
3. What is the final in-memory state after replay?
4. What is Available()?

### Exercise 4.3.2: Sync Threshold Tuning

Your system handles transfers ranging from $10 to $5,000,000.
- 99% of transfers are under $10,000
- 0.9% are $10,000-$100,000
- 0.1% are $100,000-$5,000,000

The sync flush adds ~2ms latency. What sync threshold would you choose? What
is the worst-case data loss for transfers below your threshold? How many
transfers per second pay the sync flush cost at peak load?

### Exercise 4.3.3: Implement a Recovery Test

Write a test that:
1. Creates a manager with 3 positions
2. Makes 10 reservations across the positions
3. Commits 5, releases 3, leaves 2 uncommitted
4. Calls `flushOnce()` to persist
5. Creates a new manager from the same store (simulating crash)
6. Verifies that Available() is correct for all 3 positions

Hint: Look at `TestCrashRecoveryAfterCommit` in `manager_test.go` for the
pattern.

---

## What's Next

With the treasury manager covered, Chapter 4.4 shifts to the other half of
this module: the smart routing algorithm. We explore how Settla scores
candidate routes across cost, speed, liquidity, and reliability to select
the optimal settlement path for each transfer.

---

## Further Reading

- `treasury/flush.go` for the complete flush loop implementation
- `treasury/loader.go` for the startup recovery sequence
- `treasury/store.go` for the `ReserveOpStore` interface
- `treasury/manager_test.go` `TestCrashRecoveryWithReserveOps` for the
  full crash-replay test
