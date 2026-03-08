# Settla Capacity Planning

This document captures the capacity math for the Settla settlement platform. All numbers derive from the target of **50 million transactions per day** and the architectural decisions made to support that load.

---

## 1. Scale Requirements

### Daily volume breakdown

```
Target:               50,000,000 transactions/day
Hours in a day:       24
Seconds in a day:     86,400

Sustained TPS:        50,000,000 / 86,400 = 578.7 TPS
```

Traffic is not uniform. Settlement platforms see 60-70% of volume during business hours across overlapping time zones (roughly 12 hours of elevated traffic), with sharp spikes around market opens and batch settlement windows.

```
Peak-to-sustained multiplier:   5x - 8x
Peak TPS (5x):                  578.7 × 5 = 2,893 TPS
Peak TPS (8x):                  578.7 × 8 = 4,630 TPS
Design target peak:             5,000 TPS (rounds up the 8x case)
```

### Per-transaction fan-out

Each transfer creates downstream work:

| Operation | Count per transfer | Sustained rate | Peak rate (5x) |
|---|---|---|---|
| Transfer record write | 1 | 579/s | 2,894/s |
| Transfer event write | 1 | 579/s | 2,894/s |
| Ledger journal entry | 1 | 579/s | 2,894/s |
| Ledger entry lines (debit + credit per leg) | 4-6 | 2,315-3,472/s | 11,574-17,361/s |
| Treasury reservation check | 1 | 579/s | 2,894/s |
| NATS event publish | 1 | 579/s | 2,894/s |
| Webhook delivery attempt | 1 | 579/s | 2,894/s |

---

## 2. Throughput Budget

### Per-component requirements

| Component | Sustained | Peak (5x) | Peak (8x) | Design target |
|---|---|---|---|---|
| Gateway (auth + routing) | 579 req/s | 2,894 req/s | 4,630 req/s | 5,000 req/s |
| Core engine (state machine) | 579 txn/s | 2,894 txn/s | 4,630 txn/s | 5,000 txn/s |
| Ledger writes (entry lines) | 2,315/s | 11,574/s | 18,519/s | 25,000/s |
| Ledger reads (balance queries) | ~1,158/s | ~5,789/s | ~9,259/s | 10,000/s |
| Treasury reservations | 579/s | 2,894/s | 4,630/s | 5,000/s |
| Transfer DB writes | 1,158/s | 5,789/s | 9,259/s | 10,000/s |
| NATS event throughput | 579 msg/s | 2,894 msg/s | 4,630 msg/s | 5,000 msg/s |
| Webhook deliveries | 579/s | 2,894/s | 4,630/s | 5,000/s |

### Latency budget per request (p99 target: 200ms)

```
Gateway auth lookup:           0.1 μs  (local cache hit) or 0.5 ms (Redis)
Gateway → gRPC call:           1-2 ms  (connection pool, no handshake)
Core engine state machine:     0.1-0.5 ms
Treasury reservation:          < 1 μs  (in-memory atomic)
Ledger write (TigerBeetle):    1-5 ms  (batched)
Transfer DB write:             2-5 ms  (PgBouncer)
NATS publish:                  0.5-1 ms
─────────────────────────────────────────
Total (happy path):            5-14 ms
Headroom to p99 target:       186-195 ms
```

---

## 3. Ledger Capacity

### TigerBeetle write path (authority)

```
TigerBeetle rated throughput:        1,000,000+ TPS (single cluster)
Required sustained write rate:       2,315 entry_lines/sec
Required peak write rate:            25,000 entry_lines/sec
Utilization at peak:                 25,000 / 1,000,000 = 2.5%
```

TigerBeetle operates at **2.5% capacity at peak load**. This gives roughly 40x headroom before the ledger write path becomes a concern.

### Write batching math

TigerBeetle performs best with batched submissions. The write-ahead batcher collects entries for 5-50ms before flushing:

```
Batch window:                  10 ms (typical)
Entries per batch at sustained: 579 TPS × 4 lines × 0.01s = 23 lines/batch
Entries per batch at peak:      5,000 TPS × 4 lines × 0.01s = 200 lines/batch
TigerBeetle optimal batch:     up to 8,190 entries per batch
```

Even at peak, batches are well within TigerBeetle's optimal range.

### Postgres read path (CQRS read model)

The TB-to-Postgres sync consumer populates the read model. This is eventually consistent (acceptable for balance queries and reporting).

```
Rows written by sync consumer:
  Journal entries:     579/s sustained, 5,000/s peak
  Entry lines:         2,315/s sustained, 25,000/s peak
  Balance snapshots:   ~579/s sustained (one per affected account)

Read query load:
  Balance lookups:     ~1,158/s sustained (2x per transfer: source + dest)
  Reporting queries:   ~50/s (dashboard, reconciliation)
```

Postgres read-side sizing:

```
Target read throughput:   10,000 queries/sec
Postgres capability:      30,000-50,000 simple queries/sec (single instance, indexed)
Utilization at peak:      10,000 / 40,000 = 25%
```

---

## 4. Treasury Capacity

### In-memory reservation throughput

Treasury reservations use `sync/atomic` operations on an in-memory map. No locks, no database calls.

```
Atomic operation latency:         < 1 μs (measured)
Sustained reservation rate:       579/s
Peak reservation rate:            5,000/s
Time spent on reservations/sec:   5,000 × 1 μs = 5 ms/sec = 0.5% of one CPU core
```

The in-memory design eliminates the hot-key contention problem. With database-backed reservations (`SELECT FOR UPDATE`), each call would take 2-10ms and create lock contention across connections:

```
Database approach (hypothetical):
  5,000 peak × 5 ms avg lock hold = 25,000 ms of lock time/sec
  That requires 25 connections holding locks simultaneously
  Plus queueing delays from lock contention → cascading latency

In-memory approach (actual):
  5,000 peak × 1 μs = 5 ms total/sec
  Zero lock contention, zero database connections consumed
```

### Background flush math

The flush goroutine writes position snapshots to Treasury DB every 100ms.

```
Flush interval:                  100 ms
Flushes per second:              10
Hot positions:                   ~50
Rows per flush (worst case):     50 UPSERTs
Rows per second:                 50 × 10 = 500 writes/sec to Treasury DB

Average position changes per flush:
  579 TPS ÷ 10 flushes/sec = ~58 transfers per flush interval
  Each transfer touches 2 positions (source + dest)
  Unique positions per flush (with overlap): ~30-50
```

At 500 UPSERTs/sec, the Treasury DB is under minimal load. The flush is a bulk UPSERT, so even 50 rows is a single round-trip.

### Position count scaling

```
Current hot positions:          ~50 (covering all active tenant corridors)
Memory per position:            ~256 bytes (ID, amounts, metadata)
Total memory for positions:     50 × 256 = 12.8 KB
At 1,000 positions:             256 KB
At 10,000 positions:            2.5 MB
```

In-memory positions scale trivially. The ceiling is not memory but the flush write amplification: 10,000 positions × 10 flushes/sec = 100,000 writes/sec, which would require batched bulk inserts or reduced flush frequency.

---

## 5. Database Sizing

### Row counts (daily and annual)

| Database | Table | Rows/day | Rows/year | Row size (est.) |
|---|---|---|---|---|
| Transfer DB | transfers | 50M | 18.25B | ~500 bytes |
| Transfer DB | transfer_events | 50M | 18.25B | ~300 bytes |
| Transfer DB | quotes | ~25M | 9.1B | ~400 bytes |
| Ledger DB | journal_entries | 50M | 18.25B | ~400 bytes |
| Ledger DB | entry_lines | 200-250M | 73-91B | ~200 bytes |
| Ledger DB | balance_snapshots | ~10M | 3.65B | ~300 bytes |
| Treasury DB | position_snapshots | ~50 (overwritten) | ~50 | ~256 bytes |

### Storage estimates (1 year, uncompressed)

```
Transfer DB:
  transfers:        18.25B × 500 bytes = 9.125 TB
  transfer_events:  18.25B × 300 bytes = 5.475 TB
  quotes:           9.1B × 400 bytes   = 3.640 TB
  Indexes (~30%):                        5.472 TB
  Total:                                ~23.7 TB

Ledger DB:
  journal_entries:  18.25B × 400 bytes = 7.300 TB
  entry_lines:      82B × 200 bytes    = 16.400 TB
  balance_snapshots: 3.65B × 300 bytes = 1.095 TB
  Indexes (~30%):                        7.438 TB
  Total:                                ~32.2 TB

Treasury DB:
  position_snapshots: ~50 rows          < 1 MB
  Total:                                < 1 MB
```

**Aggregate storage (1 year): ~56 TB raw, ~20 TB with compression (3x typical for structured data).**

### Partition strategy

All large tables use **monthly range partitions** on `created_at`:

```
Partitions pre-created:     6 months ahead
Default partition:          catches any out-of-range rows
Partitions per table:       12/year + 6 ahead + 1 default = 19 active

Monthly partition size (Transfer DB, transfers table):
  50M/day × 30 days = 1.5B rows/month
  1.5B × 500 bytes = 750 GB/month (before indexes)

Monthly partition size (Ledger DB, entry_lines table):
  225M/day × 30 days = 6.75B rows/month
  6.75B × 200 bytes = 1.35 TB/month (before indexes)
```

Monthly partitions keep each partition scannable for maintenance (VACUUM, archival) and allow dropping old partitions without expensive DELETE operations.

---

## 6. Network & Connection Pooling

### PgBouncer configuration

Three PgBouncer instances, one per database:

| Pool | Port | Mode | Max client conns | Max server conns | Reasoning |
|---|---|---|---|---|---|
| Ledger | 6433 | transaction | 600 | 50 | Sync consumer + read queries; short transactions |
| Transfer | 6434 | transaction | 600 | 80 | Highest write diversity; transfers + events + quotes |
| Treasury | 6435 | transaction | 200 | 20 | Flush goroutine only; very low connection demand |

Connection math:

```
settla-server replicas:     6
Connections per replica:    100 (Go sql.DB max open)
Raw connections needed:     6 × 100 = 600
PgBouncer multiplexing:    600 client → 50-80 server connections
Multiplexing ratio:         7.5-12x

Without PgBouncer:          600 Postgres connections
  Postgres recommended max: 200-300
  Result:                   connection exhaustion, query queueing
```

### gRPC connection pool

Gateway maintains a persistent gRPC connection pool to settla-server:

```
Pool size:                      ~50 connections
Gateway replicas:               4
Total gRPC connections:         4 × 50 = 200
Requests per connection/sec:    5,000 / 200 = 25 req/conn/sec
gRPC connection capacity:       ~1,000 concurrent streams per connection
Utilization:                    2.5%
```

The pool eliminates per-request TCP/TLS handshake overhead (~5-10ms each), critical at 5,000 TPS.

### NATS JetStream partition math

Events are partitioned by tenant hash across 8 stream partitions:

```
Total event throughput:         579 msg/sec sustained, 5,000 msg/sec peak
Partitions:                     8
Per-partition sustained:        579 / 8 = 72 msg/sec
Per-partition peak:             5,000 / 8 = 625 msg/sec
NATS single-stream capacity:   ~100,000 msg/sec
Per-partition utilization:      0.6% (sustained), 6.25% (peak)
```

Partitioning is not for NATS throughput (NATS can handle all traffic on one stream). It exists for **per-tenant ordering guarantees** — messages for the same tenant always land on the same partition, processed by the same worker, ensuring serial execution of a tenant's transfer saga without distributed locking.

```
settla-node instances:          8 (one per partition)
Messages per worker:            72/s sustained, 625/s peak
Processing time per message:    5-50 ms (depends on provider latency)
Worker utilization at peak:     625 × 25ms avg = 15,625 ms/sec = 15.6 concurrent
Go goroutines available:        thousands (non-blocking)
```

---

## 7. Cache Sizing

### Local in-process cache (L1)

```
Purpose:                        Auth tenant resolution, hot config
TTL:                            30 seconds
Lookup latency (measured):      107 ns
Entries:                        ~100-500 (active tenants + config)
Memory per entry:               ~1 KB (tenant struct + API key hash)
Total memory:                   500 KB (negligible)

Hit rate estimate:
  Unique tenants:               ~50-200
  Requests per tenant/sec:      579 / 100 = ~6/sec (uniform)
  Cache lifetime:               30 seconds
  Requests served per cache entry: 6 × 30 = 180
  Hit rate:                     179/180 = 99.4%
  Cache misses/sec:             579 / 180 = 3.2/sec (flow through to Redis)
```

At 99.4% hit rate, only ~3 requests/sec reach Redis for auth. Without the local cache:

```
Without L1 cache:
  Redis lookups:                5,000/sec at peak
  Redis latency:                0.5 ms each
  Total auth latency:           5,000 × 0.5 ms = 2,500 ms/sec of Redis time
  Redis utilization for auth:   ~5% of a typical Redis instance

With L1 cache:
  Redis lookups:                ~17/sec at peak (0.6% miss rate)
  Auth contribution to Redis:   negligible
```

### Redis cache (L2)

```
Purpose:                        Auth fallback, rate limiting, idempotency, quote cache
TTL:                            5 minutes (auth), varies (others)
Memory per cached tenant:       ~2 KB
Memory for 500 tenants:         1 MB

Rate limiting state:
  Sliding window counters per tenant: ~10 KB/tenant
  500 tenants:                  5 MB

Idempotency keys:
  Key size:                     ~100 bytes (hash + metadata)
  Retention:                    24 hours
  Keys per day:                 50M
  Memory:                       50M × 100 bytes = 5 GB
  With expiry (24h window):     ~5 GB peak (keys expire continuously)

Quote cache:
  Quotes cached:                ~10,000 active
  Size per quote:               ~500 bytes
  Memory:                       5 MB

Total Redis memory estimate:    ~6 GB (dominated by idempotency keys)
Recommended Redis instance:     8 GB (33% headroom)
```

### TTL reasoning

| Cache | TTL | Reasoning |
|---|---|---|
| L1 auth | 30s | Tenant config changes are rare; 30s staleness is acceptable. Short enough that key revocation takes effect within a minute. |
| L2 auth (Redis) | 5 min | Backstop for L1 misses after restart. Longer TTL since Redis persists across process restarts. |
| Idempotency keys | 24h | Clients may retry for hours. 24h covers any reasonable retry window without unbounded growth. |
| Quotes | 30s | FX rates move; quotes older than 30s are stale. Short TTL forces fresh pricing. |
| Rate limit windows | 60s | Sliding window counters. 60s gives smooth rate enforcement without sharp edges. |

---

## 8. Horizontal Scaling

| Component | Replicas | Reasoning |
|---|---|---|
| settla-server | 6+ | Each handles ~100 TPS comfortably. 6 × 800 = 4,800 TPS capacity. One can die and 5 still handle peak (5 × 800 = 4,000 TPS). |
| settla-node | 8 | One per NATS partition. Partition count is the scaling unit. To scale: increase partitions + workers together. |
| Gateway | 4+ | Stateless Fastify. 4 × 1,500 = 6,000 req/sec capacity. Each has its own local cache so no shared state. |
| Webhook dispatcher | 2+ | Delivery is async and retry-tolerant. 2 for availability; scale based on delivery backlog depth. |
| PostgreSQL (each) | 1 primary + 1-2 read replicas | Writes go to primary only. Read replicas serve dashboard, reporting, and read-heavy CQRS queries. |
| Redis | 1 (single instance) | 6 GB working set fits in one instance. Sentinel or Cluster only needed for HA, not throughput. |
| NATS | 3-node cluster | JetStream requires 3 nodes for quorum. Throughput is not the concern; durability is. |
| TigerBeetle | 1 cluster (3 replicas) | Single cluster handles 1M+ TPS. At 2.5% utilization, scaling is years away. |

### Scaling triggers

| Metric | Threshold | Action |
|---|---|---|
| settla-server CPU | > 70% sustained | Add replicas |
| Gateway p99 latency | > 100 ms | Add replicas or investigate slow auth |
| NATS consumer lag | > 1,000 messages | Add partitions + workers |
| PgBouncer wait time | > 50 ms | Increase server pool or add read replicas |
| Redis memory | > 80% | Increase instance size or tune TTLs |
| Postgres replication lag | > 5 seconds | Scale read replicas or optimize queries |

---

## 9. Bottleneck Analysis

Components ranked by proximity to saturation at peak load (5,000 TPS):

| Rank | Component | Peak utilization | Saturates at | Headroom |
|---|---|---|---|---|
| 1 | Transfer DB (Postgres writes) | ~60% | ~8,000 TPS | 1.6x |
| 2 | Ledger DB (sync consumer writes) | ~50% | ~10,000 TPS | 2x |
| 3 | PgBouncer (Transfer) | ~40% | ~12,000 TPS | 2.4x |
| 4 | settla-server (6 replicas) | ~35% | ~14,000 TPS | 2.8x |
| 5 | Redis (idempotency writes) | ~30% | ~16,000 TPS | 3.2x |
| 6 | Gateway (4 replicas) | ~20% | ~25,000 TPS | 5x |
| 7 | NATS (per-partition) | ~6% | ~80,000 TPS | 16x |
| 8 | TigerBeetle | ~2.5% | ~200,000 TPS | 40x |
| 9 | Treasury (in-memory) | < 1% | ~1,000,000 TPS | 200x |
| 10 | Local cache (L1) | < 0.01% | CPU-bound | 1000x+ |

**First bottleneck: Transfer DB Postgres writes.** At ~8,000 TPS equivalent, the Transfer DB primary becomes write-bound. Mitigation path: write batching, partitioned writes across multiple primaries, or moving hot-path writes to an append-only log.

**Second bottleneck: Ledger DB sync consumer.** The TB-to-Postgres sync consumer writes 25,000 entry_lines/sec at peak. This is achievable with bulk INSERTs but leaves less headroom than other components.

---

## 10. Capacity Headroom

### Measured vs required comparison

| Metric | Required (peak) | Measured / rated capacity | Headroom multiple |
|---|---|---|---|
| Ledger write throughput | 25,000 lines/sec | 1,000,000+ TPS (TigerBeetle) | 40x |
| Auth lookup latency | < 1 ms | 107 ns (local cache) | 9,300x |
| Treasury reservation | 5,000/sec | > 1,000,000/sec (atomic ops) | 200x |
| NATS event publish | 5,000 msg/sec | 800,000+ msg/sec (cluster) | 160x |
| gRPC pool utilization | 5,000 req/sec | 200,000 streams (pool) | 40x |
| Redis operations | 10,000 ops/sec | 100,000+ ops/sec (single instance) | 10x |
| PgBouncer connections | 600 client | 600 client → 80 server (12x mux) | Multiplexing ratio 7.5-12x |

### Growth runway

At current architecture, without major changes:

```
Current target:        50M txn/day    (578 TPS sustained)
Comfortable capacity:  100M txn/day   (1,157 TPS sustained, ~6,000 TPS peak)
Stretch capacity:      200M txn/day   (2,315 TPS sustained, ~12,000 TPS peak)
Architecture ceiling:  ~400M txn/day  (4,630 TPS sustained, ~25,000 TPS peak)
```

Beyond 400M txn/day, the single-primary Postgres databases become the binding constraint and the architecture would need to evolve toward sharded writes or a distributed SQL layer.

### Cost-efficiency note

The system is deliberately over-provisioned at the write path (TigerBeetle at 2.5%, treasury at < 1%) and right-sized at the database read/write path (Postgres at 50-60%). This is intentional: the write path components (TigerBeetle, in-memory treasury) are cheap to over-provision, while Postgres instances are the primary infrastructure cost. Scaling Postgres is expensive; scaling stateless Go services and in-memory operations is not.
