# Chapter 1.2: Capacity Math -- Deriving Architecture from Requirements

**Estimated reading time:** 30 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Derive throughput requirements from business volume targets (50M/day to TPS)
2. Calculate where Postgres, Redis, and standard architectures hit their limits
3. Explain why each architectural decision in Settla exists by showing the specific math
4. Identify the hot-key problem and why it demands in-memory solutions
5. Map every throughput requirement to its corresponding architectural pattern

---

## The Fundamental Principle

> **Key Insight:** Architecture is not chosen from a menu. It is derived from constraints. Every pattern in Settla exists because the math demands it. If you cannot point to the specific number that forces a design decision, you are over-engineering. If you ignore a number that demands a design decision, you will hit a wall in production.

---

## Start with the Business Number

Every architectural decision flows from a single business requirement:

```
    50 million transfers per day
```

This is the capacity target for settlement infrastructure serving multiple high-volume fintechs (Lemfi, Fincra, Paystack, and others). Let us derive everything from this number.

### From Daily Volume to TPS

```
    50,000,000 transfers / day
    ÷ 86,400 seconds / day
    = 578.7 TPS sustained average

    Round up: ~580 TPS sustained
```

But payment traffic is not evenly distributed. It follows daily and weekly patterns:

```
    TPS Distribution Over 24 Hours
    ================================

    00:00 - 06:00 UTC    Low traffic          ~100 TPS
    06:00 - 09:00 UTC    Ramp up              ~400 TPS
    09:00 - 17:00 UTC    Peak business hours   ~1,500 TPS sustained
    17:00 - 21:00 UTC    Gradual decline       ~800 TPS
    21:00 - 00:00 UTC    Evening low           ~200 TPS

    Peak bursts (payroll day, flash sales):   3,000 - 5,000 TPS
```

> **Key Insight:** The average tells you nothing useful for architecture. You must design for the peak. A system that handles 580 TPS but collapses at 3,000 TPS is a system that fails on payroll day -- the one day every month when your largest tenants submit batch payouts for thousands of employees simultaneously.

---

## The Write Amplification Problem

580 TPS sounds manageable. Many web applications handle more. But settlement is not a web application. Every transfer triggers a chain of operations that **multiplies** the write load.

### Per-Transfer Write Breakdown

```
    Per-Transfer Database Operations:
    ─────────────────────────────────────────────
    1 transfer record              (INSERT)     x1
    6 state transitions            (UPDATE)     x6
    6 transfer_events              (INSERT)     x6
    5 outbox entries               (INSERT)     x5
    4-5 ledger journal entries     (INSERT)     x5
    8-10 ledger entry lines        (INSERT)     x10
    1 quote record                 (INSERT)     x1
    1 provider_transactions record (INSERT)     x1
    ─────────────────────────────────────────────
    Total: ~35 database writes per transfer
```

### The Ledger Write Problem

The ledger is the worst offender. Each transfer produces multiple balanced journal entries (treasury debit, clearing credit, fee entries), each with 2+ entry lines:

```
    50M transfers/day
    x ~5 entry lines per transfer
    = 250M entry_lines/day

    In writes per second:
    250,000,000 / 86,400 = ~2,900 writes/sec AVERAGE

    At peak (5,000 TPS transfers):
    5,000 x 5 = 25,000 ledger writes/sec PEAK
```

This is the number that breaks standard architectures. Not the 580 TPS of transfers -- the 15,000-25,000 writes/sec of ledger entries at peak.

### Deposit Session Write Amplification

Each deposit session (crypto or fiat) introduces its own write amplification chain, separate from the transfer pipeline:

```
    Per Crypto Deposit Session:
    ─────────────────────────────────────────────
    1 deposit_session record         (INSERT)     x1
    5-8 state transitions            (UPDATE)     x8
    5-8 outbox entries               (INSERT)     x8
      - chain monitor detection intent
      - chain monitor confirmation intent
      - ledger credit intent
      - treasury position credit intent
      - settlement intent (if AUTO_CONVERT)
      - webhook delivery intent
    2-3 ledger journal entries       (INSERT)     x3
    4-6 ledger entry lines           (INSERT)     x6
    ─────────────────────────────────────────────
    Total: ~26 database writes per deposit session

    Per Bank Deposit Session:
    ─────────────────────────────────────────────
    1 bank_deposit_session record    (INSERT)     x1
    5-8 state transitions            (UPDATE)     x8
    5-8 outbox entries               (INSERT)     x8
    1 virtual_account allocation     (UPDATE)     x1
    2-3 ledger journal entries       (INSERT)     x3
    4-6 ledger entry lines           (INSERT)     x6
    ─────────────────────────────────────────────
    Total: ~27 database writes per bank deposit session
```

At scale, if 10% of daily volume comes from deposits (5M sessions/day), that adds ~130M additional database writes per day to the transfer and ledger databases.

### Outbox Table Writes

```
    50M transfers/day
    x 5 outbox entries per transfer
    = 250M outbox rows/day

    Plus deposit sessions (~5M/day x 6 entries):   ~30M
    Plus bank deposits (~2M/day x 6 entries):      ~12M
    Plus webhooks, emails, position events:        ~8M
    Total: ~300M outbox rows/day

    Monthly: ~9 BILLION rows
    Quarterly: ~27 BILLION rows
```

---

## Where Postgres Breaks

PostgreSQL is an excellent database. It handles most workloads brilliantly. But it has concrete, measurable limits that the math above exposes.

### Bottleneck 1: Single-Table Write Throughput

A well-tuned Postgres instance on good hardware achieves:

```
    Simple INSERTs (no indexes):       ~10,000-15,000/sec
    INSERTs with 2-3 B-tree indexes:    ~5,000-8,000/sec
    INSERTs with constraints + WAL:     ~3,000-5,000/sec
    Multi-row transactions:             ~1,000-3,000/sec
```

Our ledger needs 15,000-25,000 writes/sec at peak. A single Postgres instance cannot sustain this, even with aggressive tuning (huge shared_buffers, WAL compression, synchronous_commit=off).

### Bottleneck 2: The Connection Problem

```
    6 server replicas (minimum for HA)
    x 100 connections per replica (Go's default pool)
    = 600 connections

    8 worker nodes
    x 50 connections per node
    = 400 connections

    Gateway, monitoring, admin:
    + 100 connections

    Total: ~1,100 concurrent connections

    Postgres max_connections default: 100
    Practical limit before degradation: ~500
    Each connection: ~10MB memory overhead
    1,100 connections x 10MB = 11GB just for connection state
```

This is why PgBouncer sits in front of every Postgres instance in Settla. Three PgBouncer instances (one per database: ledger :6433, transfer :6434, treasury :6435) multiplex hundreds of application connections onto a smaller pool of actual Postgres connections.

### Bottleneck 3: WAL Generation at Scale

```
    Transfers table:       50M rows/day x ~500 bytes = ~25 GB/day
    Transfer events table: 300M rows/day x ~200 bytes = ~60 GB/day
    Outbox table:          300M rows/day x ~300 bytes = ~90 GB/day
    Ledger entry lines:    250M rows/day x ~200 bytes = ~50 GB/day
    ──────────────────────────────────────────────────────────────
    Total data written:     ~225 GB/day
    WAL generated:          ~300-400 GB/day (includes index updates)
    Per hour:               ~12-17 GB/hour
```

### Bottleneck 4: Table Bloat and Vacuum Pressure

At 300M outbox rows/day, with rows being marked as `published = true` after relay processing:

```
    300M UPDATEs/day on outbox table (published flag)
    Each UPDATE creates a dead tuple (MVCC)
    Autovacuum must process 300M dead tuples/day
    Vacuum I/O competes with production writes
    Table bloat grows faster than vacuum can reclaim
```

Without partitioning, the outbox table becomes the single biggest source of operational pain.

---

## The Hot-Key Problem

This is the single most important architectural constraint in Settla. It determines why the treasury manager lives in memory instead of in the database.

### What Is a Hot Key?

A "hot key" is a database row that many concurrent transactions need to read and write simultaneously. In Settla, the hottest keys are **treasury positions** -- there are roughly 50 positions (one per tenant per currency per location), and the busiest positions receive thousands of concurrent operations per second.

### The Naive Database Approach

With `SELECT FOR UPDATE` (pessimistic locking):

```sql
BEGIN;
SELECT available, locked FROM positions
  WHERE tenant_id = 'lemfi-uuid' AND currency = 'GBP' AND location = 'uk'
  FOR UPDATE;  -- Row lock acquired

-- Application checks: available >= amount
-- If yes:
UPDATE positions
  SET available = available - 1000,
      locked = locked + 1000
  WHERE tenant_id = 'lemfi-uuid' AND currency = 'GBP' AND location = 'uk';
COMMIT;  -- Row lock released
```

### The Math of Lock Contention

At peak, Lemfi might be sending 500+ GBP transfers per second. Each `SELECT FOR UPDATE` holds the row lock for the full transaction duration:

```
    Lock hold time (round-trip):
      Network to PgBouncer:    ~0.5ms
      PgBouncer to Postgres:   ~0.5ms
      SELECT execution:        ~0.5ms
      Application logic:       ~0.5ms
      UPDATE execution:        ~0.5ms
      COMMIT + WAL sync:       ~1.0ms
      ─────────────────────────────
      Total lock hold:         ~3.5ms

    Maximum throughput per row:
      1000ms / 3.5ms = ~285 transactions/sec

    But 500+ requests arrive per second.
    Deficit: 215 requests/sec queue up.
```

```
    Lock Contention Cascade
    =======================

    Time 0ms:     Request 1 acquires lock on Lemfi GBP position
    Time 0ms:     Requests 2-500 enter WAIT state
    Time 3.5ms:   Request 1 releases, Request 2 acquires lock
    Time 3.5ms:   Requests 3-500 still WAITING
    Time 7.0ms:   Request 2 releases, Request 3 acquires lock
    ...
    Time 1,750ms: Request 500 finally acquires lock

    Request 500 waited 1.75 SECONDS for a simple balance check.
    P99 latency: catastrophic. SLA violated.
    Cascade: timeouts cause retries, retries increase contention.
```

### Why "Just Shard It" Does Not Work

The obvious suggestion is to split each position across multiple rows (shard the balance). But this creates worse problems:

```
    Split Lemfi's GBP 500,000 position into 4 shards:

    Shard 1: 125,000 GBP available
    Shard 2: 125,000 GBP available
    Shard 3: 125,000 GBP available
    Shard 4: 125,000 GBP available

    Problem 1: A 200,000 GBP transfer exceeds any single shard.
               Need distributed transaction across shards (2PC = slower).

    Problem 2: Total available requires reading ALL shards.
               Concurrent reads while writes are in-flight = inconsistent.

    Problem 3: Liquidity fragmentation.
               130,000 GBP transfer fails because no single shard has enough,
               even though total available is 500,000 GBP.
```

### Settla's Solution: In-Memory Atomic Reservation

Instead of database locks, Settla's treasury manager (defined in `domain/treasury.go`) keeps positions in memory and uses Go's atomic operations:

```go
// From domain/treasury.go -- the interface contract
type TreasuryManager interface {
    // Reserve atomically decrements available balance and increments locked balance.
    // Operates on in-memory state only.
    Reserve(ctx context.Context, tenantID uuid.UUID, currency Currency,
            location string, amount decimal.Decimal, reference uuid.UUID) error

    // Release atomically decrements locked balance and increments available balance.
    // Operates on in-memory state only.
    Release(ctx context.Context, tenantID uuid.UUID, currency Currency,
            location string, amount decimal.Decimal, reference uuid.UUID) error
}
```

The comment in the interface is a critical invariant: **"Operates on in-memory state only."**

```
    Database Lock Path:                In-Memory Path:
    ==================                ================

    API Request                       API Request
         |                                 |
    SELECT FOR UPDATE  (~3.5ms)       atomic.CompareAndSwap  (~100ns)
         |                                 |
    UPDATE positions   (~1ms)         Done. Response returned.
         |                                 |
    COMMIT + WAL       (~1ms)         Background flush (every 100ms)
         |                                 |
    Response           (~5.5ms)       Postgres UPDATE (batched)

    Speedup: ~55,000x for the hot path
    Throughput: limited only by CPU, not by database locks
```

The background flush goroutine writes in-memory state to Postgres every 100ms. This means the database is at most 100ms stale, which is acceptable because:
1. The in-memory state is the source of truth for reservations
2. On process restart, positions are reloaded from Postgres (100ms of in-flight reservations may need reconciliation)
3. The 100ms flush interval is configurable based on durability requirements

> **Key Insight:** The hot-key problem appears in any system with a shared counter under concurrent pressure: inventory counts, rate limiters, balance checks, seat reservations. The solution is always the same pattern: move the hot path out of the database and into memory, then flush asynchronously. The tradeoff is always the same: latency of the flush interval defines the durability window.

---

## The Dual-Write Problem

The dual-write problem is the second fundamental constraint that shapes Settla's architecture.

### The Classic Dual-Write Bug

Consider a naive implementation of the transfer engine:

```
    1. UPDATE transfer SET status = 'ON_RAMPING';    -- DB write succeeds
    2. http.Post("https://provider.com/onramp", ...); -- Network call fails
```

**Scenario A:** Step 1 succeeds, Step 2 fails (network error):

```
    Database says: transfer is ON_RAMPING
    Provider says: never received this request
    Result: stuck transfer, money not moving, customer waiting
```

**Scenario B:** Step 2 succeeds, Step 1 fails (DB connection dropped):

```
    Provider says: I executed the on-ramp, converted GBP to USDT
    Database says: transfer is still FUNDED (step 1 never committed)
    Result: money has moved but the system does not know it
```

Both scenarios require manual intervention to resolve. At 50M transfers/day, even a 0.001% failure rate means 500 stuck transfers per day requiring manual investigation.

### The Outbox Solution

Settla eliminates dual writes entirely. The engine never makes network calls. Instead, it writes outbox entries atomically with state transitions:

```
    SINGLE atomic database transaction:
    ┌─────────────────────────────────────────────────────┐
    │ BEGIN;                                               │
    │   UPDATE transfers SET status = 'ON_RAMPING';       │
    │   INSERT INTO outbox (event_type, payload)          │
    │     VALUES ('provider.onramp.execute', '{...}');     │
    │ COMMIT;                                             │
    │                                                     │
    │ Both succeed or both fail. No partial state.        │
    └─────────────────────────────────────────────────────┘

    THEN (asynchronously, decoupled from the request):
    ┌─────────────────────────────────────────────────────┐
    │ Outbox Relay (polls every 20ms, batch 500):         │
    │   SELECT * FROM outbox WHERE published = false;     │
    │   For each entry:                                    │
    │     Publish to NATS JetStream                       │
    │     UPDATE outbox SET published = true;             │
    └─────────────────────────────────────────────────────┘

    THEN (worker picks up from NATS):
    ┌─────────────────────────────────────────────────────┐
    │ ProviderWorker:                                      │
    │   Check provider_transactions (idempotency)         │
    │   If not executed: call provider API                 │
    │   Record result in provider_transactions            │
    │   Call engine.HandleOnRampResult(IntentResult{...})  │
    └─────────────────────────────────────────────────────┘
```

The intent types from the actual codebase show every side effect the engine can request:

```go
// From domain/outbox.go -- Intent type constants
const (
    IntentTreasuryReserve  = "treasury.reserve"
    IntentTreasuryRelease  = "treasury.release"
    IntentProviderOnRamp   = "provider.onramp.execute"
    IntentProviderOffRamp  = "provider.offramp.execute"
    IntentLedgerPost       = "ledger.post"
    IntentLedgerReverse    = "ledger.reverse"
    IntentBlockchainSend   = "blockchain.send"
    IntentWebhookDeliver   = "webhook.deliver"
)
```

And the corresponding result events that workers publish when done:

```go
// From domain/outbox.go -- Result event types
const (
    EventTreasuryReserved      = "treasury.reserved"
    EventTreasuryFailed        = "treasury.failed"
    EventProviderOnRampDone    = "provider.onramp.completed"
    EventProviderOnRampFailed  = "provider.onramp.failed"
    EventProviderOffRampDone   = "provider.offramp.completed"
    EventProviderOffRampFailed = "provider.offramp.failed"
    EventBlockchainConfirmed   = "blockchain.confirmed"
    EventBlockchainFailed      = "blockchain.failed"
)
```

---

## The Auth Cache Math

At 5,000 TPS peak, every request needs authentication. Let us calculate the cost of each approach.

### Approach 1: Direct Database Lookup

```
    5,000 req/sec x 3ms Postgres query = 15 seconds of DB time per second
    = impossible (more than 1 second of work per second)
    Need 15 concurrent DB connections just for auth
```

### Approach 2: Redis Cache

```
    5,000 req/sec x 0.5ms Redis round-trip = 2.5 seconds of Redis time per second
    = 2-3 Redis connections fully saturated for auth alone
    = 0.5ms added to EVERY request latency
```

### Approach 3: Settla's Three-Level Cache

```
    Level 1: Local in-process LRU    ~107ns    30-second TTL
    Level 2: Redis                   ~0.5ms    5-minute TTL
    Level 3: Postgres (DB)           ~3ms      source of truth

    At steady state with 95%+ L1 hit rate:
    5,000 req/sec x 107ns = 0.535ms total compute time
    vs 2,500ms with Redis-only
    vs 15,000ms with DB-only

    L1 miss (5%): 250 req/sec hit Redis (manageable)
    L2 miss (1%): 50 req/sec hit Postgres (trivial)
```

> **Key Insight:** Each cache layer absorbs the miss rate of the layer above it. The L1 cache handles 95% of requests at 107 nanoseconds. The remaining 5% hit Redis. The remaining 0.05% hit Postgres. This cascade means the database sees almost no auth traffic regardless of API throughput.

---

## The NATS Partitioning Math

Workers process events from NATS JetStream across 12 streams (11 domain + 1 DLQ). The 11 dedicated workers are: TransferWorker, ProviderWorker, LedgerWorker, TreasuryWorker, BlockchainWorker, WebhookWorker, InboundWebhookWorker, DepositWorker, BankDepositWorker, EmailWorker, and DLQMonitor. Each event requires real work (API calls to providers, database queries, blockchain transactions). A single consumer cannot keep up:

```
    Single consumer processing time: ~5-10ms per event (average)
    Max throughput: 1000ms / 7.5ms = ~133 events/sec
    Need: 580 events/sec sustained, 3,000+ at peak
    Deficit: massive
```

Settla partitions NATS streams by tenant hash across 8 partitions:

```
    Subject pattern: settla.transfer.partition.{N}.{event_type}
    N = hash(tenant_id) % 8

    At 580 events/sec:
    Partition 0: ~73 events/sec  (Consumer A)
    Partition 1: ~73 events/sec  (Consumer B)
    ...
    Partition 7: ~73 events/sec  (Consumer H)

    Each consumer: 73 events/sec << 133 max throughput
    Headroom: 45% spare capacity per consumer

    At peak 3,000 events/sec:
    Each partition: ~375 events/sec
    Need 3 consumers per partition: 375/133 = 2.8
    Total consumers: 8 x 3 = 24 (handled by 8 worker nodes with 3 goroutines each)
```

The key property: all events for a given tenant hash to the same partition, so per-tenant ordering is preserved. Cross-tenant ordering is not guaranteed -- but it is never needed.

The complete list of 12 NATS streams:

```
    Stream                    Subject Pattern                        Consumer
    ──────────────────────────────────────────────────────────────────────────────────
    SETTLA_TRANSFERS          settla.transfer.partition.*.>          TransferWorker
    SETTLA_PROVIDERS          settla.provider.command.partition.*.>  ProviderWorker
    SETTLA_LEDGER             settla.ledger.partition.*.>            LedgerWorker
    SETTLA_TREASURY           settla.treasury.partition.*.>          TreasuryWorker
    SETTLA_BLOCKCHAIN         settla.blockchain.partition.*.>        BlockchainWorker
    SETTLA_WEBHOOKS           settla.webhook.partition.*.>           WebhookWorker
    SETTLA_PROVIDER_WEBHOOKS  settla.provider.inbound.partition.*.>  InboundWebhookWorker
    SETTLA_CRYPTO_DEPOSITS    settla.deposit.partition.*.>           DepositWorker
    SETTLA_BANK_DEPOSITS      settla.bank_deposit.partition.*.>      BankDepositWorker
    SETTLA_EMAILS             settla.email.partition.*.>             EmailWorker
    SETTLA_POSITION_EVENTS    settla.position.event.>               PositionEventWriter
    SETTLA_DLQ                settla.dlq.>                           DLQMonitor
```

The deposit and bank deposit streams carry their own event load. At peak, the crypto deposit stream may process 1,000+ events/sec (chain detections, confirmations, credits), each partitioned by tenant hash for ordering guarantees.

---

## The Outbox Table Growth Problem

```
    300M outbox rows/day
    Monthly: ~9 BILLION rows
    6 months: ~54 BILLION rows (without cleanup)
```

A table with 54 billion rows is unqueryable. Even with indexes, B-tree depth becomes a performance cliff (5-6 levels, each requiring a disk seek). Settla solves this with monthly partitioning:

```
    outbox (parent table, empty -- holds no rows directly)
    +-- outbox_2026_01  (January partition)
    +-- outbox_2026_02  (February partition)
    +-- outbox_2026_03  (March partition, CURRENT)
    +-- outbox_2026_04  (pre-created by PartitionManager)
    +-- outbox_2026_05  (pre-created)
    ...

    Dropping January data:
    DROP TABLE outbox_2026_01;  -- INSTANT, O(1), any row count

    vs. without partitioning:
    DELETE FROM outbox WHERE created_at < '2026-02-01';
    -- Scans billions of rows
    -- Holds locks for minutes to hours
    -- Generates massive WAL
    -- Triggers enormous VACUUM operation
    -- Blocks production writes during cleanup
```

The `PartitionManager` in `core/maintenance/` automates this: it pre-creates partitions 6 months ahead and drops partitions older than the retention window.

---

## The Complete Architecture Derivation Table

Every pattern traced back to its specific math:

| Problem | Threshold | What Breaks Without Solution | Settla Solution |
|---------|-----------|------------------------------|-----------------|
| Transfer volume | 50M/day, 5K TPS peak | Single server overloaded | 6+ server replicas, load balanced |
| Ledger writes | 15K-25K writes/sec peak | Postgres WAL serialization (~5-8K max) | TigerBeetle write path (1M+ TPS) |
| Ledger queries | Rich queries (date range, account, pagination) | TigerBeetle has limited query | Postgres read path (CQRS split) |
| Write batching | 25K individual INSERTs/sec | Per-INSERT overhead | Write-ahead batching: 5-50ms collect, bulk flush |
| Treasury hot keys | ~50 positions, 500+ ops/sec each | `SELECT FOR UPDATE` caps at ~285/sec per key | In-memory atomic CAS (~100ns) |
| Treasury durability | Process crash loses memory | Reservations lost, accounting mismatch | 100ms background flush to Postgres |
| Dual-write bug | Any failure between DB + API call | Stuck transfers, inconsistent state | Transactional outbox (atomic DB write) |
| Database connections | 1,100+ total from all services | Postgres degrades above ~500 | PgBouncer (3 instances, one per DB) |
| Gateway auth | 5K lookups/sec at peak | Redis RTT (0.5ms) = 2.5sec compute/sec | Local LRU cache (30s TTL, ~107ns) |
| Event parallelism | 580 events/sec, per-tenant ordering | Single consumer maxes at ~133/sec | NATS partition by tenant hash (8 partitions) |
| NATS redelivery | At-least-once delivery guarantee | Provider called twice = money sent twice | CHECK-BEFORE-CALL (check provider_transactions first) |
| Outbox table growth | 300M rows/day, 9B/month | Table bloat, vacuum pressure, query degradation | Monthly partitions + DROP TABLE for cleanup |
| Rate limiting | Per-tenant limits at 5K TPS | Redis round-trip per check | Local counters, sync to Redis every 5 seconds |
| gRPC overhead | New TCP connection per request | Handshake latency, connection exhaustion | Connection pool (~50 persistent, round-robin) |

---

## Capacity Reference Card

Keep these numbers handy throughout the course:

```
    SETTLA CAPACITY TARGETS
    =======================

    Transfers:     50M/day  |  580 TPS sustained  |  3,000-5,000 TPS peak
    Ledger writes: 250M/day |  2,900 avg           |  15,000-25,000 peak
    Transfer DB:   50M transfers + 300M events/day
    Outbox:        300M entries/day
    Treasury:      ~50 hot positions under constant concurrent pressure
    Gateway auth:  5,000 lookups/sec at peak
    Local cache:   107ns auth lookup (measured)

    INFRASTRUCTURE
    ==============
    settla-server:   6+ replicas   (ports :8080 HTTP, :9090 gRPC, :6060 pprof)
    settla-node:     8+ instances  (outbox relay + 11 workers)
    gateway:         4+ replicas   (port :3000)
    NATS streams:    12 (11 domain + 1 DLQ)
    NATS partitions: 8 per stream
    Postgres DBs:    3 (ledger :5433, transfer :5434, treasury :5435)
    PgBouncer:       3 (ledger :6433, transfer :6434, treasury :6435)
    Redis:           1 cluster (port :6379)
    TigerBeetle:     1 cluster (port :3001)
```

---

## Common Mistakes

### Mistake 1: Designing for Average, Not Peak

"580 TPS? Postgres handles that easily." Yes, the average is fine. But payroll day brings 5,000 TPS in a 2-hour burst. If you designed for the average, you have a 2-hour outage every month. Capacity planning must be based on the worst plausible burst, not the daily average.

### Mistake 2: Benchmarking in Isolation

A Postgres INSERT benchmark on an empty table with no indexes might show 15,000/sec. In production, the table has 5 indexes, foreign keys, CHECK constraints, WAL replication to a standby, and 500 concurrent connections all competing for the WAL lock. Real throughput is 3-5x lower than isolated benchmark numbers.

### Mistake 3: Ignoring the Multiplication Effect

"50M transfers is our target." But each transfer generates ~5 outbox entries, ~5 ledger entries, ~6 events, and webhook deliveries. The actual write load is 5-10x the transfer count. You must multiply before you can evaluate architecture.

### Mistake 4: Adding Caches Without Understanding Why

Caching is not a generic performance optimization. Each cache in Settla exists because the math demanded it. The auth cache exists because 5,000 Redis round-trips per second consumes too much network time. The treasury lives in memory because row locks on hot keys cap at 285 TPS. If you cannot state the specific bottleneck a cache solves with a number, you do not need that cache -- and you are adding complexity for no benefit.

### Mistake 5: "We'll Use Microservices"

Microservices distribute compute but add distributed transaction complexity. When every transfer needs atomic state + outbox writes, distributing that across services means distributed transactions (2PC/Saga). A modular monolith with purpose-built components (TigerBeetle for ledger writes, in-memory treasury, partitioned Postgres) delivers better throughput with simpler operations.

---

## Exercises

### Exercise 1: Derive Your Own Architecture

You are building a ride-sharing payment system that processes 10 million rides per day. Each ride generates:
- 1 payment authorization
- 1 driver payout
- 1 platform fee deduction
- 2 ledger entries (4 lines each)

Calculate:
1. The sustained TPS for ride payments
2. The peak TPS (assume a 5x peak-to-average ratio during rush hour)
3. The ledger write rate at peak
4. Whether a single Postgres instance can handle the ledger writes
5. Which patterns from the derivation table you would adopt and why

### Exercise 2: Hot-Key Analysis

Your e-commerce platform has 200 merchant accounts. The top 10 merchants account for 80% of volume. The busiest merchant processes 2,000 transactions/second during a flash sale.

1. Calculate the maximum throughput with `SELECT FOR UPDATE` (assume 3ms lock hold time)
2. How many transactions queue up per second?
3. What is the P99 latency for the queued transactions?
4. Design an in-memory solution. What is the staleness tradeoff?

### Exercise 3: Outbox Table Sizing

Given 50M transfers/day with 5 outbox entries each:
1. Calculate the monthly row count
2. Calculate the storage requirement (assume 300 bytes per row including indexes)
3. If the retention window is 90 days, how many rows exist at any point?
4. Estimate the time to `DELETE FROM outbox WHERE created_at < ?` on 27 billion rows vs `DROP TABLE outbox_2026_01` on 9 billion rows
5. What operational problems does the DELETE approach cause that DROP avoids?

---

## What's Next

Now you understand WHY every architectural decision exists -- each one is derived from specific math. In Chapter 1.3, we will look at how Settla's `domain/` package prevents bugs at the type level. You will see how value objects, strict typing, decimal-only monetary math, and compile-time interface checks create a codebase where entire categories of financial bugs are structurally impossible.

---
