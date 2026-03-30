# Chapter 2.3: CQRS Ledger Architecture

**Estimated reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the CQRS pattern and why it fits a high-throughput ledger
2. Trace a write through `PostEntries()` from validation to TigerBeetle
3. Trace a read through `GetEntries()` and `GetBalance()` to understand which backend serves which query
4. Understand how the `tbBackend.buildTransfers()` algorithm decomposes multi-line entries into TigerBeetle transfers
5. Walk through the `SyncConsumer` that populates the Postgres read model
6. Identify the consistency guarantees and understand the ~100ms lag window

---

## What Is CQRS?

CQRS -- Command Query Responsibility Segregation -- separates the system into two distinct paths:

- **Command path** (writes): optimized for throughput and durability
- **Query path** (reads): optimized for flexibility and rich queries

In traditional architectures, a single database handles both reads and writes. This forces a compromise: the schema must be queryable (normalized, indexed) AND writable (fast inserts, no lock contention). At Settla's scale, no single technology can do both well.

```
    ┌─────────────────────────────────────────────────────────────┐
    │                      CQRS Architecture                       │
    │                                                               │
    │   COMMAND SIDE                          QUERY SIDE            │
    │   (Write Path)                          (Read Path)           │
    │                                                               │
    │   ┌──────────────┐                     ┌──────────────┐      │
    │   │ PostEntries()│                     │ GetEntries() │      │
    │   │ ReverseEntry│                     │ GetBalance() │      │
    │   └──────┬───────┘                     └──────┬───────┘      │
    │          │                                     │              │
    │          v                                     v              │
    │   ┌──────────────┐                     ┌──────────────┐      │
    │   │  TigerBeetle │                     │   Postgres   │      │
    │   │  (Write Auth)│                     │  (Read Model)│      │
    │   │  1M+ TPS     │                     │  Rich queries│      │
    │   └──────┬───────┘                     └──────────────┘      │
    │          │                                     ^              │
    │          │         ┌──────────────┐            │              │
    │          +────────>│ SyncConsumer │────────────+              │
    │                    │ (100ms batch)│                           │
    │                    └──────────────┘                           │
    │                                                               │
    │   GetBalance() reads from TigerBeetle directly (O(1))        │
    │   GetEntries() reads from Postgres (rich SQL queries)        │
    │                                                               │
    └─────────────────────────────────────────────────────────────┘
```

> **Key Insight**: `GetBalance()` does NOT read from Postgres. It reads directly from TigerBeetle for authoritative, O(1) balance lookups. Only `GetEntries()` -- which needs date range filtering, pagination, and JOINs -- reads from Postgres. This is a pragmatic split: use each technology for what it does best.

---

## The Service Facade

The `ledger.Service` struct in `ledger/ledger.go` is the composite that ties both backends together. Callers interact with the `domain.Ledger` interface and remain unaware of the dual-backend split:

```go
// Compile-time check: Service implements domain.Ledger.
var _ domain.Ledger = (*Service)(nil)

// Service is the composite dual-backend ledger.
//
// Write path (PostEntries, ReverseEntry): delegates to TigerBeetle via tbBackend.
// Read path (GetBalance): delegates to TigerBeetle for authoritative O(1) lookups.
// Query path (GetEntries): delegates to Postgres via pgBackend for rich queries.
type Service struct {
    tb        *tbBackend
    pg        *pgBackend
    sync      *SyncConsumer
    batcher   *Batcher
    bulkhead  *resilience.Bulkhead
    publisher domain.EventPublisher
    logger    *slog.Logger
    metrics   *observability.Metrics
}
```

The `var _ domain.Ledger = (*Service)(nil)` line is a compile-time interface check. If `Service` fails to implement any method of `domain.Ledger`, the code will not compile. This pattern appears throughout Settla.

---

## The Write Path: PostEntries() Deep Dive

Let's trace a journal entry from API call to durable storage. Here is the complete `PostEntries()` method with annotations:

```go
func (s *Service) PostEntries(ctx context.Context, entry domain.JournalEntry) (*domain.JournalEntry, error) {
    // STEP 1: Validate (pure function, no I/O)
    if err := domain.ValidateEntries(entry.Lines); err != nil {
        return nil, fmt.Errorf("settla-ledger: validating entries: %w", err)
    }

    // STEP 2: Assign IDs and timestamps
    if entry.ID == uuid.Nil {
        entry.ID = uuid.New()
    }
    if entry.PostedAt.IsZero() {
        entry.PostedAt = time.Now().UTC()
    }
    if entry.EffectiveDate.IsZero() {
        entry.EffectiveDate = entry.PostedAt
    }
    for i := range entry.Lines {
        if entry.Lines[i].ID == uuid.Nil {
            entry.Lines[i].ID = uuid.New()
        }
    }

    // STEP 3: Stub mode (no TigerBeetle)
    if s.tb == nil {
        if s.metrics != nil {
            s.metrics.LedgerPostingsTotal.WithLabelValues(entry.ReferenceType).Inc()
        }
        return &entry, nil
    }

    // STEP 4: Ensure all referenced accounts exist in TigerBeetle
    codes := make([]string, len(entry.Lines))
    for i, line := range entry.Lines {
        codes[i] = line.AccountCode
    }
    if err := s.tb.EnsureAccounts(ctx, codes); err != nil {
        return nil, fmt.Errorf("settla-ledger: ensuring accounts: %w", err)
    }

    // STEP 5: Post to TigerBeetle with retry and bulkhead
    tbStart := time.Now()
    tbWrite := func(ctx context.Context) error {
        if s.batcher != nil {
            return s.batcher.Submit(ctx, entry)
        }
        _, err := s.tb.PostEntries(ctx, entry)
        return err
    }
    // ... retry logic with 3 attempts, 50ms-500ms backoff ...

    // STEP 6: Queue for async Postgres sync (non-blocking)
    if s.sync != nil {
        s.sync.Enqueue(entry)
    }

    // STEP 7: Publish domain event
    if s.publisher != nil {
        _ = s.publisher.Publish(ctx, domain.Event{
            Type: "ledger.entry.posted",
            Data: entry.ID,
        })
    }

    return &entry, nil
}
```

The flow as a diagram:

```
    PostEntries(entry)
         │
         v
    ┌────────────────────┐
    │ ValidateEntries()  │──── Fail? Return error immediately
    │ (pure function)    │     TB never sees invalid entries
    └────────┬───────────┘
             │
             v
    ┌────────────────────┐
    │ Assign IDs & times │     UUID for entry, UUID for each line
    └────────┬───────────┘     PostedAt = now().UTC()
             │
             v
    ┌────────────────────┐
    │ EnsureAccounts()   │──── Create TB accounts if they don't exist
    │ (idempotent)       │     SHA-256(code)[:16] = deterministic ID
    └────────┬───────────┘
             │
             v
    ┌────────────────────┐
    │ Batcher.Submit()   │──── Collects entries for 5-50ms
    │ or tb.PostEntries()│     Flushes as single CreateTransfers call
    │ (retry x3)         │     Bulkhead limits concurrent writes
    └────────┬───────────┘
             │
             v
    ┌────────────────────┐
    │ SyncConsumer.      │──── Non-blocking channel send
    │ Enqueue(entry)     │     If queue full, entry dropped (recovered
    └────────┬───────────┘     later via TB->PG reconciliation)
             │
             v
    ┌────────────────────┐
    │ Publish domain     │──── "ledger.entry.posted" event
    │ event              │     Fire-and-forget (error swallowed)
    └────────────────────┘
```

### Step 4: EnsureAccounts -- Idempotent Account Creation

```go
func (tb *tbBackend) EnsureAccounts(ctx context.Context, codes []string) error {
    if len(codes) == 0 {
        return nil
    }
    accounts := make([]TBAccount, len(codes))
    for i, code := range codes {
        accounts[i] = TBAccount{
            ID:     AccountIDFromCode(code),
            Ledger: tb.ledger,
            Code:   1,
        }
    }
    results, err := tb.client.CreateAccounts(accounts)
    if err != nil {
        return fmt.Errorf("settla-ledger: creating TB accounts: %w", err)
    }
    for _, r := range results {
        if r.Result != TBResultOK && r.Result != TBResultExists {
            return fmt.Errorf("settla-ledger: creating TB account at index %d: result code %d",
                r.Index, r.Result)
        }
    }
    return nil
}
```

This is called on every `PostEntries()`, but it is idempotent: `TBResultExists` (the account already exists) is treated as success. The first time an account code is seen, TigerBeetle creates it. Every subsequent time, it returns "exists" and moves on.

### Step 5: Building TigerBeetle Transfers

TigerBeetle's transfer model is a single debit-credit pair. But Settla's journal entries can have multiple debits and credits (e.g., 2 debits + 1 credit for a settlement with fees). The `buildTransfers()` method decomposes multi-line entries:

```go
// buildTransfers decomposes a balanced journal entry into TigerBeetle transfers.
//
// Algorithm: greedily match debits with credits. Each match produces one TB
// transfer. When a debit is larger than the current credit (or vice-versa),
// the remainder carries to the next line. All transfers except the last are
// linked for atomic execution.
func (tb *tbBackend) buildTransfers(entry domain.JournalEntry, debits, credits []domain.EntryLine) ([]TBTransfer, error) {
    var transfers []TBTransfer

    di, ci := 0, 0
    var dRem, cRem decimal.Decimal
    if len(debits) > 0 {
        dRem = debits[0].Amount
    }
    if len(credits) > 0 {
        cRem = credits[0].Amount
    }

    idx := 0
    for di < len(debits) && ci < len(credits) {
        amount := dRem
        if cRem.LessThan(dRem) {
            amount = cRem
        }
        // ... create transfer with min(dRem, cRem) ...

        dRem = dRem.Sub(amount)
        cRem = cRem.Sub(amount)

        if dRem.IsZero() { di++; /* load next debit */ }
        if cRem.IsZero() { ci++; /* load next credit */ }
        idx++
    }

    // Link all but last transfer for atomicity.
    for i := 0; i < len(transfers)-1; i++ {
        transfers[i].Flags |= TBFlagLinked
    }

    return transfers, nil
}
```

Let's trace this with the multi-line test entry (2 debits + 1 credit):

```
Input:
  DR  assets:crypto:usdt:tron          USDT 950.00
  DR  expenses:provider:onramp         USDT  50.00
  CR  tenant:lemfi:assets:bank:gbp     USDT 1000.00

Iteration 1:
  dRem = 950, cRem = 1000
  amount = min(950, 1000) = 950
  Transfer 1: crypto:usdt:tron --950--> bank:gbp
  dRem = 0 (advance to next debit), cRem = 50

Iteration 2:
  dRem = 50, cRem = 50
  amount = min(50, 50) = 50
  Transfer 2: expenses:onramp --50--> bank:gbp
  dRem = 0, cRem = 0 (done)

Result:
  Transfer 1: LINKED  (atomic with Transfer 2)
  Transfer 2: UNLINKED (last in chain)
```

The `TBFlagLinked` flag tells TigerBeetle to execute these transfers atomically -- either both succeed or both fail. This preserves the journal entry's all-or-nothing semantics.

This is verified by the test:

```go
func TestTBBackend_BuildTransfers_MultiLine(t *testing.T) {
    // ...
    if transfers[0].Flags&TBFlagLinked == 0 {
        t.Error("first transfer should be linked")
    }
    if transfers[1].Flags&TBFlagLinked != 0 {
        t.Error("last transfer should not be linked")
    }
    if transfers[0].Amount != 95000000000 { // 950 * 10^8
        t.Errorf("expected first transfer amount 95000000000, got %d", transfers[0].Amount)
    }
    if transfers[1].Amount != 5000000000 { // 50 * 10^8
        t.Errorf("expected second transfer amount 5000000000, got %d", transfers[1].Amount)
    }
}
```

---

## The Read Path: GetBalance() and GetEntries()

### GetBalance(): TigerBeetle Direct

Balance queries go straight to TigerBeetle. No Postgres involved.

```go
// GetBalance returns the authoritative balance for an account code.
// Reads directly from TigerBeetle (O(1) lookup, microsecond latency).
func (s *Service) GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
    if s.tb == nil {
        return decimal.Zero, nil
    }
    balance, err := s.tb.GetBalance(ctx, accountCode)
    if err != nil {
        return decimal.Zero, fmt.Errorf("settla-ledger: getting balance: %w", err)
    }
    return balance, nil
}
```

TigerBeetle computes balance as `credits_posted - debits_posted`:

```go
func (tb *tbBackend) GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
    id := AccountIDFromCode(accountCode)
    accounts, err := tb.client.LookupAccounts([]ID128{id})
    if err != nil {
        return decimal.Zero, fmt.Errorf("settla-ledger: looking up TB account %s: %w", accountCode, err)
    }
    if len(accounts) == 0 {
        return decimal.Zero, fmt.Errorf("settla-ledger: account %s: %w",
            accountCode, domain.ErrAccountNotFound(accountCode))
    }
    acc := accounts[0]
    balance := TBAmountToDecimal(acc.CreditsPosted).Sub(TBAmountToDecimal(acc.DebitsPosted))
    return balance, nil
}
```

This is O(1): TigerBeetle looks up the account by its 128-bit ID (direct offset, no index scan) and returns the pre-computed debit/credit totals. The subtraction happens in Settla's code.

### GetEntries(): Postgres SQL

Entry history queries go to Postgres, which has the rich SQL capabilities needed for filtering, sorting, and pagination:

```go
// GetEntries returns entry lines for an account within a time range.
// Reads from Postgres (rich query capability for dashboards and audit).
// Note: there may be slight lag (~100ms) vs TigerBeetle.
func (s *Service) GetEntries(ctx context.Context, accountCode string, from, to time.Time,
    limit, offset int) ([]domain.EntryLine, error) {
    if s.pg == nil {
        return nil, nil
    }
    entries, err := s.pg.GetEntries(ctx, accountCode, from, to, limit, offset)
    if err != nil {
        return nil, fmt.Errorf("settla-ledger: querying entries: %w", err)
    }
    return entries, nil
}
```

The Postgres backend resolves the account code to an ID, then queries the `entry_lines` table with date range filtering and pagination:

```go
func (pg *pgBackend) GetEntries(ctx context.Context, accountCode string, from, to time.Time,
    limit, offset int) ([]domain.EntryLine, error) {
    account, err := pg.q.GetAccountByCode(ctx, accountCode)
    if err != nil {
        return nil, fmt.Errorf("settla-ledger: resolving account %s: %w", accountCode, err)
    }

    rows, err := pg.q.ListEntryLinesByAccountInDateRange(ctx,
        ledgerdb.ListEntryLinesByAccountInDateRangeParams{
            AccountID:   account.ID,
            CreatedAt:   from,
            CreatedAt_2: to,
            Limit:       int32(limit),
            Offset:      int32(offset),
        })
    // ... convert rows to domain types ...
}
```

The underlying SQL (from `db/queries/ledger/ledger.sql`):

```sql
-- name: ListEntryLinesByAccountInDateRange :many
SELECT * FROM entry_lines
WHERE account_id = $1
  AND created_at >= $2
  AND created_at < $3
ORDER BY created_at DESC
LIMIT $4 OFFSET $5;
```

Because `entry_lines` is partitioned by `created_at`, Postgres can prune irrelevant partitions. A query for "entries in the last 7 days" only scans the current week's partition, not the entire table history.

---

## The Sync Path: TB to PG

The `SyncConsumer` bridges the two backends. After every successful `PostEntries()` to TigerBeetle, the journal entry is enqueued for asynchronous sync to Postgres:

```go
// SyncConsumer runs a background goroutine that syncs journal entries
// from TigerBeetle to Postgres. Entries are queued via Enqueue() after
// being posted to TB, and flushed in batches to PG at a configurable interval.
type SyncConsumer struct {
    pg        pgSyncer
    batchSize int
    interval  time.Duration
    logger    *slog.Logger
    queue     chan domain.JournalEntry
    done      chan struct{}
    wg        sync.WaitGroup
    droppedTotal atomic.Int64
}
```

The sync loop collects entries and flushes them on two triggers:

```go
func (sc *SyncConsumer) run() {
    defer sc.wg.Done()
    ticker := time.NewTicker(sc.interval)
    defer ticker.Stop()

    batch := make([]domain.JournalEntry, 0, sc.batchSize)

    for {
        select {
        case <-sc.done:
            sc.drainQueue(&batch)
            if len(batch) > 0 {
                sc.flush(batch)
            }
            return

        case entry := <-sc.queue:
            batch = append(batch, entry)
            if len(batch) >= sc.batchSize {
                sc.flush(batch)      // Trigger 1: batch full
                batch = batch[:0]
            }

        case <-ticker.C:
            if len(batch) > 0 {
                sc.flush(batch)      // Trigger 2: timer expired
                batch = batch[:0]
            }
        }
    }
}
```

**Trigger 1**: Batch size reached (default 1,000 entries). Under high load, entries are flushed as soon as 1,000 accumulate.

**Trigger 2**: Timer interval (default 100ms). Under low load, entries are flushed every 100ms regardless of batch size. This bounds the maximum lag between TigerBeetle and Postgres.

### Non-Blocking Enqueue with Backpressure

```go
func (sc *SyncConsumer) Enqueue(entry domain.JournalEntry) {
    select {
    case sc.queue <- entry:
    default:
        sc.droppedTotal.Add(1)
        sc.logger.Warn("sync queue full, dropping entry",
            "entry_id", entry.ID,
            "queue_size", len(sc.queue),
            "dropped_total", sc.droppedTotal.Load())
    }
}
```

Enqueue is non-blocking. If the channel buffer (default 50,000) is full, the entry is dropped with a warning. This is a deliberate design choice:

1. The write to TigerBeetle has already succeeded -- the data is durable
2. Blocking the hot write path to wait for PG sync would kill throughput
3. Dropped entries can be recovered via TB-to-PG reconciliation (the reconciliation module runs periodic checks)

The dropped count is exposed as a Prometheus metric:

```go
var syncQueueDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
    Namespace: "settla",
    Subsystem: "ledger",
    Name:      "sync_queue_dropped_total",
    Help:      "Total entries dropped because the TB->PG sync queue was full.",
})
```

### Flush: Writing to Postgres

```go
func (sc *SyncConsumer) flush(batch []domain.JournalEntry) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    synced := 0
    for _, entry := range batch {
        if err := sc.pg.SyncJournalEntry(ctx, entry); err != nil {
            sc.logger.Error("failed to sync entry to PG",
                "entry_id", entry.ID,
                "error", err)
            continue  // Don't fail the entire batch for one error
        }
        synced++
    }
}
```

Each entry is synced independently. If one fails, the rest continue. Failed entries are logged and can be recovered by the reconciliation module.

The Postgres write creates a `journal_entries` row and its associated `entry_lines`:

```go
func (pg *pgBackend) SyncJournalEntry(ctx context.Context, entry domain.JournalEntry) error {
    // Write journal entry header
    _, err := pg.q.CreateJournalEntry(ctx, ledgerdb.CreateJournalEntryParams{
        TenantID:       uuidToPgtype(entry.TenantID),
        IdempotencyKey: textToPgtype(entry.IdempotencyKey),
        EffectiveDate:  dateToPgtype(entry.EffectiveDate),
        Description:    entry.Description,
        ReferenceType:  textToPgtype(entry.ReferenceType),
        ReferenceID:    uuidToPgtype(entry.ReferenceID),
        ReversalOf:     uuidToPgtype(entry.ReversalOf),
        Metadata:       metadata,
    })
    // ...

    // Write each entry line
    for _, line := range entry.Lines {
        _, err := pg.q.CreateEntryLine(ctx, ledgerdb.CreateEntryLineParams{
            JournalEntryID: entry.ID,
            AccountID:      accountID,
            EntryType:      string(line.EntryType),
            Amount:         decimalToNumeric(line.Amount),
            Currency:       string(line.Currency),
            Description:    textToPgtype(line.Description),
        })
        // ...
    }
    return nil
}
```

---

## Consistency Model

The CQRS split creates an eventual consistency window:

```
    Timeline:
    ─────────────────────────────────────────────>
    t0          t1              t2
    │           │               │
    │ PostEntries() succeeds    │ SyncConsumer flushes to PG
    │ (TB write durable)       │ (PG now has data)
    │           │               │
    │<──── TB consistent ──────>│<── PG consistent ──>
    │           │               │
    │     ~0-100ms lag          │
    │           │               │
    │ GetBalance() = correct    │
    │ GetEntries() = stale      │ GetEntries() = correct
```

- **GetBalance()** is always authoritative: it reads from TigerBeetle, which has the write immediately
- **GetEntries()** may lag by up to ~100ms (the sync interval): a dashboard query right after a write may not show the latest entry
- **Balance snapshots** in Postgres are updated by the sync consumer and may also lag

This is acceptable because:
1. Balance-critical operations (treasury reservation, settlement) use `GetBalance()` from TigerBeetle
2. Dashboard queries and audit trails can tolerate 100ms staleness
3. The reconciliation module detects and repairs any divergence

---

## Configuration and Options

The `Service` is configured via functional options:

```go
func defaultConfig() config {
    return config{
        tbLedgerID:    1,
        batchWindow:   10 * time.Millisecond,
        batchMaxSize:  500,
        syncBatchSize: 1000,
        syncInterval:  100 * time.Millisecond,
        syncQueueSize: 50000,
        bulkheadMax:   100,
    }
}
```

| Option | Default | Purpose |
|--------|---------|---------|
| `tbLedgerID` | 1 | TigerBeetle ledger partition ID |
| `batchWindow` | 10ms | Write-ahead batching collection window |
| `batchMaxSize` | 500 | Max entries before forced flush |
| `syncBatchSize` | 1,000 | Max entries per PG sync flush |
| `syncInterval` | 100ms | Timer-based sync flush interval |
| `syncQueueSize` | 50,000 | Channel buffer for TB-to-PG sync queue |
| `bulkheadMax` | 100 | Max concurrent TigerBeetle writes |

The bulkhead limits concurrent writes to TigerBeetle to 100 in-flight operations. This prevents a flood of requests from overwhelming the TB client connection, which would cause timeouts and cascading failures.

---

## Deposit and Position Credit Entries

The CQRS pipeline handles more than settlement transfers. Crypto deposits and bank deposits also generate ledger entries that flow through the same `PostEntries()` path:

```
    Deposit Flow (crypto):

    Chain Monitor detects USDT payment
        |
        v
    Deposit Engine transitions session to CREDITING
        |
        +---> OutboxEntry: IntentCreditDeposit (deposit.credit)
        |
        v
    DepositWorker constructs journal entry:
        DR  tenant:{slug}:assets:crypto:usdt:balance    USDT  net_amount
        DR  tenant:{slug}:revenue:fees:deposit           USDT  fee_amount
        CR  assets:crypto:usdt:tron                      USDT  gross_amount
        |
        +---> ledger.Service.PostEntries()
        |         -> ValidateEntries() (balance check)
        |         -> Batcher.Submit() (write to TigerBeetle)
        |         -> SyncConsumer.Enqueue() (replicate to Postgres)
        |
        +---> treasury.Manager.Credit() (position balance increase)
        |         -> PositionEvent{Type: CREDIT} emitted
```

Bank deposits follow the same pattern, with the `BankDepositWorker` constructing entries when fiat funds are confirmed in a virtual account. The key point: **all ledger writes share the same CQRS infrastructure**, regardless of whether they originate from a transfer, deposit, or position transaction. This means the batching, sync, and consistency guarantees described above apply uniformly.

Position transactions (top-ups, withdrawals, internal rebalances) also generate ledger entries. The `PositionTransaction` entity in `domain/position_transaction.go` follows its own state machine (PENDING -> PROCESSING -> COMPLETED/FAILED), and each completed transaction produces both a journal entry and a `PositionEvent` in the treasury audit log.

---

## Common Mistakes

**Mistake 1: Querying Postgres for balance-critical decisions.**
`GetBalance()` reads from TigerBeetle. The balance snapshots in Postgres are stale by up to 100ms. Using stale balances for treasury reservations or settlement calculations would cause over-spending or under-reserving.

**Mistake 2: Treating sync failures as critical errors.**
The sync consumer logs failures and continues. The data is already durable in TigerBeetle. The reconciliation module repairs PG gaps. Making sync failures block the write path would sacrifice throughput for unnecessary consistency on the read model.

**Mistake 3: Assuming GetEntries() returns all data immediately after PostEntries().**
There is a ~100ms lag. Tests that call `PostEntries()` then immediately `GetEntries()` must account for this by either waiting or using TigerBeetle-backed assertions.

**Mistake 4: Forgetting that buildTransfers() uses greedy matching.**
The algorithm greedily matches debits to credits in order. The order of entry lines matters for which TB transfers get created. While the total amounts are always correct (guaranteed by `ValidateEntries()`), the individual TB transfer decomposition depends on line order.

**Mistake 5: Not starting the Service.**
`Service.Start()` begins the sync consumer and batcher goroutines. Forgetting to call it means entries are never synced to Postgres and batching does not work.

---

## Exercises

1. **Trace Exercise**: Given a journal entry with 3 debits (100, 200, 300) and 2 credits (250, 350), trace through `buildTransfers()` step by step. How many TigerBeetle transfers are created? What are their amounts? Which are linked?

2. **Consistency Window**: A dashboard user clicks "refresh" immediately after submitting a transfer. The balance shown is 100ms stale. Describe two approaches to handle this UX issue without changing the CQRS architecture.

3. **Backpressure Design**: The sync queue has a capacity of 50,000. At 25,000 entries/sec with a 100ms sync interval, how many entries accumulate per interval? Is the queue large enough? What happens if Postgres is slow for 2 seconds?

4. **Code Reading**: In `ledger/postgres.go`, the `SyncJournalEntry` method resolves account codes to IDs using `pg.q.GetAccountByCode()`. What happens if the account does not exist in the `accounts` table? Read the code and explain the fallback behavior.

5. **Options Design**: Write a test that creates a `ledger.Service` with a 5ms batch window, 100 max batch size, and 50ms sync interval. Then post 200 entries concurrently and verify they all arrive in the mock TigerBeetle client.

---

## What's Next

You now understand the CQRS architecture and how writes flow through TigerBeetle while reads come from Postgres. The sync consumer bridges the gap, but what about the write path itself? At 25,000 entries/sec, individual `CreateTransfers` calls to TigerBeetle create too much round-trip overhead. The next chapter covers the write-ahead batcher that collects entries and flushes them in bulk.

**Next: [Chapter 2.4: Write-Ahead Batching](./chapter-2.4-write-ahead-batching.md)**
