# Chapter 2.4: Write-Ahead Batching

**Estimated reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why individual TigerBeetle round-trips cannot sustain 25K writes/sec
2. Describe the write-ahead batching algorithm (collect window + max size trigger)
3. Walk through the complete `Batcher` implementation line by line
4. Understand how per-entry error channels distribute batch results
5. Trace the `flushBatch()` method that aggregates multiple entries into a single `CreateTransfers` call
6. Configure batching parameters for different throughput/latency tradeoffs

---

## The Problem: Round-Trip Overhead

Even though TigerBeetle can process millions of transfers per second internally, the client-server round-trip adds overhead:

```
    Without batching (individual calls):

    Entry 1: ──[network]──> TB ──[process]──> TB ──[network]──> Done   ~200us
    Entry 2: ──[network]──> TB ──[process]──> TB ──[network]──> Done   ~200us
    Entry 3: ──[network]──> TB ──[process]──> TB ──[network]──> Done   ~200us
    ...
    Entry N: ──[network]──> TB ──[process]──> TB ──[network]──> Done   ~200us

    Total: N * ~200us
    At 25,000 entries/sec: 25,000 * 200us = 5 seconds per second  (IMPOSSIBLE)
```

Each round-trip includes:
- Serialization of the request (~5us)
- Network latency (~50-100us on the same data center)
- TigerBeetle processing (~1-5us per transfer)
- Response deserialization (~5us)

At 200us per round-trip, we get a maximum of 5,000 individual calls per second per connection. We need 25,000/sec.

```
    With batching (bulk calls):

    ┌─────────────────────────────────────────┐
    │ Collect for 10ms window:                 │
    │   Entry 1, Entry 2, ... Entry 250       │
    └────────────────┬────────────────────────┘
                     │
                     v
    250 entries: ──[network]──> TB ──[process 250]──> TB ──[network]──> Done  ~300us
                     │
                     v
    Distribute results to 250 individual callers

    Total: ~300us for 250 entries
    Per entry: ~1.2us  (167x improvement)
    At 25,000 entries/sec: 100 batch calls/sec * ~300us = 30ms/sec  (EASY)
```

> **Key Insight**: TigerBeetle processes batches almost as fast as individual transfers. A batch of 250 transfers takes ~300us total, not 250 * 200us. The per-transfer processing time inside TigerBeetle is ~1-5us -- the overhead is all in the round-trip. Batching amortizes that overhead across hundreds of entries.

---

## The Batcher Architecture

```
    Caller A ──┐
    Caller B ──┤    ┌────────────┐    ┌────────────────┐    ┌──────────┐
    Caller C ──┼───>│  Batcher   │───>│ CreateTransfers │───>│ TigerBeetle
    Caller D ──┤    │            │    │ (single call)   │    │          │
    Caller E ──┘    │ collects   │    └────────┬───────┘    └──────────┘
                    │ 5-50ms     │             │
       ┌────────────┤            │<────────────┘
       │            └────────────┘    results
       v
    Caller A <── err/nil
    Caller B <── err/nil
    Caller C <── err/nil
    Caller D <── err/nil
    Caller E <── err/nil
```

Each caller blocks on its own result channel. When the batch flushes, all callers are unblocked simultaneously with their individual results.

---

## The Complete Batcher Implementation

Here is `ledger/batch.go` with detailed annotations:

```go
// Batcher implements write-ahead batching for TigerBeetle.
//
// At 25K writes/sec, batching in 10ms windows means ~250 entries per batch,
// significantly reducing TB round-trips. Each individual entry still gets
// its own error response via the result channel.
type Batcher struct {
    tb         *tbBackend
    window     time.Duration    // How long to collect entries (default 10ms)
    maxSize    int              // Force flush at this count (default 500)
    logger     *slog.Logger
    metrics    *observability.Metrics

    mu      sync.Mutex          // Protects pending and timer
    pending []batchItem         // Entries waiting to be flushed
    timer   *time.Timer         // Window timer (nil = no active window)

    done chan struct{}           // Signals shutdown
    wg   sync.WaitGroup         // Tracks in-flight flushes
}

type batchItem struct {
    entry  domain.JournalEntry  // The entry to post
    result chan error            // Per-caller result channel (buffered 1)
}
```

The `batchItem` is the key abstraction. Each caller gets their own `chan error` with buffer size 1. The batcher collects items, flushes them all at once, and sends each caller's result on their individual channel.

### Submit: Adding an Entry to the Batch

```go
func (b *Batcher) Submit(ctx context.Context, entry domain.JournalEntry) error {
    resultCh := make(chan error, 1)

    b.mu.Lock()

    // Check if stopped.
    select {
    case <-b.done:
        b.mu.Unlock()
        return fmt.Errorf("settla-ledger: batcher stopped")
    default:
    }

    b.pending = append(b.pending, batchItem{
        entry:  entry,
        result: resultCh,
    })

    shouldFlush := len(b.pending) >= b.maxSize

    if shouldFlush {
        // Max size reached -- flush immediately.
        if b.timer != nil {
            b.timer.Stop()
            b.timer = nil
        }
        pending := b.pending
        b.pending = nil
        b.mu.Unlock()

        b.wg.Add(1)
        go func() {
            defer b.wg.Done()
            b.flushBatch(pending)
        }()
    } else if b.timer == nil {
        // First item in a new batch -- start the timer.
        b.timer = time.AfterFunc(b.window, func() {
            b.mu.Lock()
            pending := b.pending
            b.pending = nil
            b.timer = nil
            b.mu.Unlock()

            if len(pending) > 0 {
                b.wg.Add(1)
                go func() {
                    defer b.wg.Done()
                    b.flushBatch(pending)
                }()
            }
        })
        b.mu.Unlock()
    } else {
        b.mu.Unlock()
    }

    // Wait for result.
    select {
    case err := <-resultCh:
        return err
    case <-ctx.Done():
        return fmt.Errorf("settla-ledger: batch submit cancelled: %w", ctx.Err())
    }
}
```

Let's trace through the three code paths:

**Path 1: Batch full (shouldFlush = true)**
When the pending count reaches `maxSize`, flush immediately. Stop the timer if one is running, take the pending slice, nil it out, and flush in a new goroutine. The caller's goroutine blocks on `resultCh` until the flush completes.

**Path 2: First item in a new batch (timer == nil)**
The first entry to arrive after a flush starts a new collection window. `time.AfterFunc(b.window, ...)` creates a timer that fires after the window duration (default 10ms). When it fires, it takes whatever has accumulated and flushes it.

**Path 3: Middle of a batch (timer != nil, not full)**
Simply append and unlock. The timer from Path 2 is already ticking. The caller blocks on `resultCh`.

```
    Timeline of a batch lifecycle:

    t=0ms    Entry A arrives, starts timer (10ms window)
    t=2ms    Entry B arrives, appends to pending
    t=5ms    Entry C arrives, appends to pending
    t=8ms    Entry D arrives, appends to pending
    t=10ms   Timer fires! Flush [A, B, C, D] to TigerBeetle
    t=10.3ms Results distributed, all four callers unblocked
    t=12ms   Entry E arrives, starts NEW timer
    ...
```

### Context Cancellation

```go
    select {
    case err := <-resultCh:
        return err
    case <-ctx.Done():
        return fmt.Errorf("settla-ledger: batch submit cancelled: %w", ctx.Err())
    }
```

If the caller's context is cancelled while waiting for the batch to flush (e.g., HTTP request timeout), the caller gets an error immediately. The entry may still be flushed to TigerBeetle as part of the batch -- this is fine because TigerBeetle handles idempotency via the deterministic transfer IDs.

---

## flushBatch: The Core Flush Logic

This is where all entries in a batch are aggregated into a single TigerBeetle `CreateTransfers` call:

```go
func (b *Batcher) flushBatch(items []batchItem) {
    // Phase 1: Build TB transfers for all entries
    type entryTransfers struct {
        startIdx int    // Where this entry's transfers start in allTransfers
        count    int    // How many TB transfers this entry produced
    }

    var allTransfers []TBTransfer
    entryMap := make([]entryTransfers, len(items))

    for i, item := range items {
        var debits, credits []domain.EntryLine
        for _, line := range item.entry.Lines {
            switch line.EntryType {
            case domain.EntryTypeDebit:
                debits = append(debits, line)
            case domain.EntryTypeCredit:
                credits = append(credits, line)
            }
        }

        transfers, err := b.tb.buildTransfers(item.entry, debits, credits)
        if err != nil {
            items[i].result <- fmt.Errorf("settla-ledger: building transfers: %w", err)
            continue  // This entry fails, but others continue
        }

        entryMap[i] = entryTransfers{
            startIdx: len(allTransfers),
            count:    len(transfers),
        }
        allTransfers = append(allTransfers, transfers...)
    }

    if len(allTransfers) == 0 {
        return  // All entries failed at build stage
    }

    // Phase 2: Send all transfers to TigerBeetle in ONE call
    results, err := b.tb.client.CreateTransfers(allTransfers)

    if err != nil {
        // TB call failed entirely -- all entries fail
        for _, item := range items {
            select {
            case item.result <- fmt.Errorf("settla-ledger: batch TB write failed: %w", err):
            default:
            }
        }
        return
    }

    // Phase 3: Map TB results back to individual entries
    failedIdx := make(map[uint32]uint32)
    for _, r := range results {
        if r.Result != TBResultOK && r.Result != TBResultExists {
            failedIdx[r.Index] = r.Result
        }
    }

    for i, item := range items {
        et := entryMap[i]
        if et.count == 0 {
            continue  // Already got an error from build stage
        }

        var entryErr error
        for j := et.startIdx; j < et.startIdx+et.count; j++ {
            if code, failed := failedIdx[uint32(j)]; failed {
                entryErr = fmt.Errorf("settla-ledger: TB transfer at index %d failed: result code %d",
                    j, code)
                break
            }
        }

        select {
        case item.result <- entryErr:
        default:
        }
    }
}
```

Let's visualize Phase 1 with a concrete example:

```
    Batch: [Entry A (2 lines), Entry B (3 lines), Entry C (2 lines)]

    Entry A: DR account1, CR account2
      -> buildTransfers -> [Transfer 0]
      -> entryMap[0] = {startIdx: 0, count: 1}

    Entry B: DR account3, DR account4, CR account5
      -> buildTransfers -> [Transfer 1 (linked), Transfer 2]
      -> entryMap[1] = {startIdx: 1, count: 2}

    Entry C: DR account6, CR account7
      -> buildTransfers -> [Transfer 3]
      -> entryMap[2] = {startIdx: 3, count: 1}

    allTransfers = [T0, T1, T2, T3]  (4 TB transfers total)
    Single CreateTransfers call to TigerBeetle!
```

Phase 3 maps results back:

```
    TB returns: results = [{Index: 2, Result: 5}]
    (Transfer at index 2 failed with code 5)

    failedIdx = {2: 5}

    Entry A: transfers [0..0], none in failedIdx -> result = nil (success)
    Entry B: transfers [1..2], index 2 IS in failedIdx -> result = error
    Entry C: transfers [3..3], none in failedIdx -> result = nil (success)
```

Entry B's caller gets an error. Entries A and C succeed. The batch is split correctly.

> **Key Insight**: Cross-entry atomicity is NOT guaranteed. Each entry's transfers are linked internally (within the entry), but different entries in the same batch can independently succeed or fail. This is correct: the entries came from different callers with different idempotency keys. They should not be coupled.

---

## Graceful Shutdown

```go
func (b *Batcher) Stop() {
    close(b.done)

    b.mu.Lock()
    if b.timer != nil {
        b.timer.Stop()
    }
    pending := b.pending
    b.pending = nil
    b.mu.Unlock()

    if len(pending) > 0 {
        b.flushBatch(pending)  // Synchronous final flush
    }

    b.wg.Wait()  // Wait for any in-flight async flushes
}
```

On shutdown:
1. Signal `done` (prevents new entries from being accepted)
2. Stop the timer (no more timer-triggered flushes)
3. Take any remaining pending entries and flush them synchronously
4. Wait for any goroutines from previous flushes to complete

This guarantees no data loss: every entry that was accepted via `Submit()` before `Stop()` was called will be flushed to TigerBeetle.

---

## Configurable Parameters

The default configuration balances throughput and latency:

```go
func defaultConfig() config {
    return config{
        batchWindow:  10 * time.Millisecond,  // Collect for 10ms
        batchMaxSize: 500,                     // Or until 500 entries
    }
}
```

| Parameter | Default | Tradeoff |
|-----------|---------|----------|
| `batchWindow` | 10ms | Shorter = lower latency, more TB calls. Longer = higher throughput, more latency |
| `batchMaxSize` | 500 | Lower = more frequent flushes. Higher = fewer TB calls but higher memory usage |

At sustained 25K entries/sec with a 10ms window:
- Entries per window: 25,000 * 0.01 = **250 entries per batch**
- Batches per second: 25,000 / 250 = **100 TB calls/sec** (vs 25,000 without batching)

That is a **250x reduction** in TigerBeetle round-trips.

### Tuning for Different Scenarios

**Low latency (API queries that need fast response):**
```go
WithBatchWindow(2 * time.Millisecond)
WithBatchMaxSize(50)
```

**Maximum throughput (background settlement batch):**
```go
WithBatchWindow(50 * time.Millisecond)
WithBatchMaxSize(2000)
```

**No batching (testing, debugging):**
```go
WithNoBatching()  // Sets batchWindow = 0, disabling the batcher entirely
```

---

## Testing the Batcher

The tests verify two critical behaviors: batching reduces TB calls, and the timer triggers flushes.

### Test: Batching Reduces Calls

```go
func TestBatcher_BatchesMultipleEntries(t *testing.T) {
    tb := newMockTBClient()
    pub := &mockPublisher{}
    svc := NewService(tb, nil, pub, slogDiscard(), nil,
        WithBatchWindow(50*time.Millisecond),
        WithBatchMaxSize(10),
    )
    svc.Start()
    defer svc.Stop()

    const count = 10
    var wg sync.WaitGroup
    var errCount atomic.Int32

    wg.Add(count)
    for i := 0; i < count; i++ {
        go func(idx int) {
            defer wg.Done()
            // ... create and post entry ...
        }(i)
    }
    wg.Wait()

    // With max batch size 10, all 10 should go in fewer CreateTransfers calls
    // than 10 individual calls (ideally 1 batched call).
    tb.mu.Lock()
    calls := tb.createTransfersCalls
    tb.mu.Unlock()
    if calls >= count {
        t.Errorf("batcher should reduce TB calls: got %d calls for %d entries", calls, count)
    }
}
```

### Test: Window Timer Flush

```go
func TestBatcher_WindowFlush(t *testing.T) {
    tb := newMockTBClient()
    pub := &mockPublisher{}
    svc := NewService(tb, nil, pub, slogDiscard(), nil,
        WithBatchWindow(20*time.Millisecond),
        WithBatchMaxSize(1000),  // High max so only timer triggers flush
    )
    svc.Start()
    defer svc.Stop()

    entry := balancedEntry("timer-test")
    _, err := svc.PostEntries(context.Background(), entry)
    if err != nil {
        t.Fatalf("PostEntries failed: %v", err)
    }

    // Entry should have been flushed after the 20ms window.
    tb.mu.Lock()
    transfers := len(tb.transfers)
    tb.mu.Unlock()
    if transfers != 1 {
        t.Errorf("expected 1 transfer after window flush, got %d", transfers)
    }
}
```

This test sets `maxSize` to 1000 (far above 1 entry) so the only flush trigger is the 20ms timer. It confirms that the timer mechanism works independently of the size trigger.

---

## Backpressure and Error Handling

### What If a Single Entry's Build Fails?

```go
transfers, err := b.tb.buildTransfers(item.entry, debits, credits)
if err != nil {
    items[i].result <- fmt.Errorf("settla-ledger: building transfers: %w", err)
    continue  // This entry fails, but others continue
}
```

The failing entry gets its error immediately. The remaining entries in the batch continue to be processed. One bad entry does not poison the entire batch.

### What If TigerBeetle Is Down?

```go
results, err := b.tb.client.CreateTransfers(allTransfers)
if err != nil {
    // TB call failed entirely -- all entries fail
    for _, item := range items {
        select {
        case item.result <- fmt.Errorf("settla-ledger: batch TB write failed: %w", err):
        default:
        }
    }
    return
}
```

All entries in the batch fail. Each caller receives the TigerBeetle connection error on their result channel. The `PostEntries()` method has retry logic (3 attempts with exponential backoff) that will retry the individual entry.

### The `select { default: }` Pattern

```go
select {
case item.result <- entryErr:
default:
}
```

This non-blocking send prevents a goroutine leak. If a caller has already returned (context cancelled), the channel might have no receiver. Without `default`, the send would block forever. With `default`, the result is silently dropped -- acceptable because the caller has already received a context cancellation error.

---

## Relationship to the Bulkhead

The batcher works in conjunction with a bulkhead that limits concurrent TigerBeetle operations:

```go
if s.bulkhead != nil {
    if err := resilience.Retry(ctx, tbRetryConfig, shouldRetryTB, func(ctx context.Context) error {
        return s.bulkhead.Execute(ctx, tbWrite)
    }); err != nil {
        return nil, fmt.Errorf("settla-ledger: TB write (bulkhead): %w", err)
    }
}
```

The bulkhead (default 100 concurrent operations) prevents a thundering herd from overwhelming TigerBeetle's client connection. With the batcher, each "operation" is a batch of ~250 entries, so 100 concurrent operations means up to 25,000 entries in flight -- exactly the peak requirement.

```
    Without bulkhead:
    25,000 goroutines ──> TigerBeetle client ──> connection overwhelmed

    With batcher + bulkhead:
    25,000 goroutines ──> Batcher (250/batch) ──> 100 concurrent batches ──> TigerBeetle
                          ~100 batches/sec          max 100 in-flight
```

---

## Common Mistakes

**Mistake 1: Setting the batch window too high for latency-sensitive paths.**
A 50ms batch window means every ledger write takes at least 50ms. For API responses that include balance updates, this is too slow. The default 10ms provides ~250 entries/batch at peak, which is sufficient.

**Mistake 2: Assuming cross-entry atomicity.**
Entries in the same batch are NOT atomic with each other. Entry A can succeed while Entry B fails. This is correct behavior -- they are independent operations that happen to share a batch for efficiency.

**Mistake 3: Forgetting to call Start() and Stop().**
The batcher relies on `time.AfterFunc` for timer-based flushes. Without `Start()`, the timer mechanism still works (it does not need a background goroutine), but `Stop()` is critical for flushing remaining entries on shutdown.

**Mistake 4: Using the batcher for low-volume testing.**
Use `WithNoBatching()` in tests. The timer-based flush adds 10ms latency that makes unit tests unnecessarily slow and flaky.

**Mistake 5: Not monitoring batch sizes.**
The metric `LedgerTBBatchSize` tracks how many entries are in each flush. If this consistently shows 1, batching is not helping. If it consistently hits `maxSize`, the window is too long and entries are queueing.

---

## Exercises

1. **Latency Math**: With a 10ms batch window and 5,000 entries/sec sustained, what is the average batch size? What is the maximum additional latency a caller experiences compared to unbatched writes?

2. **Race Condition Analysis**: The `Submit()` method acquires `b.mu.Lock()`, appends to `b.pending`, then releases the lock. The timer callback also acquires the lock to take `b.pending`. Explain why there is no race condition, even though `Submit()` and the timer callback run on different goroutines.

3. **Failure Modes**: Entry A has 2 TB transfers, Entry B has 1 TB transfer, and Entry C has 3 TB transfers. TigerBeetle returns a failure for the transfer at index 3 (Entry C's first transfer). Which entries succeed and which fail? Trace through the `failedIdx` logic.

4. **Tuning Exercise**: A new deployment has a TigerBeetle round-trip latency of 500us (high-latency network). At 10K entries/sec, what batch window maximizes throughput while keeping p99 latency under 50ms?

5. **Shutdown Ordering**: Why does `Stop()` flush pending entries synchronously (on the calling goroutine) rather than starting a new goroutine? What could go wrong with an async final flush?

---

## What's Next

Batching solves the write throughput problem. But what happens when a transfer fails and we need to undo ledger entries? You cannot DELETE from a ledger -- entries are immutable. The next chapter covers reversal mechanics: how to "undo" a posting by creating a mirror entry with swapped debits and credits.

**Next: [Chapter 2.5: Ledger Reversals](./chapter-2.5-ledger-reversals.md)**
