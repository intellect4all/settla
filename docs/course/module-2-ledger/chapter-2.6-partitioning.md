# Chapter 2.6: Partitioning for 50M+ Rows/Day

**Estimated reading time:** 25 minutes

## Learning Objectives

- Understand why table partitioning is mandatory at 50M+ rows/day
- Design partition strategies per table (daily, weekly, monthly)
- Explain why DROP TABLE is instant but DELETE is catastrophic at scale
- Implement a PartitionManager that automates creation and cleanup
- Choose retention windows based on operational and compliance needs

---

## The Growth Problem

At 50M transfers/day, database tables grow at alarming rates:

```
Daily row growth:
  transfers:              50M rows/day     (~25 GB)
  transfer_events:       300M rows/day     (~60 GB)
  outbox:                300M rows/day     (~30 GB)
  entry_lines:           250M rows/day     (~50 GB)
  position_events:        ~1.7B rows/day   (~200 GB)  [~20K/sec peak]
  provider_webhook_logs: 100M rows/day     (~40 GB)
  net_settlements:       ~100K rows/day    (~50 MB)
  position_transactions: ~500K rows/day    (~200 MB)

Monthly:
  transfers:             1.5B rows          (~750 GB)
  transfer_events:       9B rows            (~1.8 TB)
  outbox:                9B rows            (~900 GB)
  entry_lines:           7.5B rows          (~1.5 TB)
  position_events:       ~50B rows          (~6 TB)
  provider_webhook_logs: 3B rows            (~1.2 TB)

After 6 months without partitioning:
  Total: ~75 TB of data in tables that queries must scan
```

Without partitioning, queries degrade as B-tree index depth increases beyond 4-5 levels, VACUUM can't keep up with dead tuple generation, and bulk cleanup operations become impossible.

---

## Settla's Partition Configurations

```go
// From core/maintenance/partition_manager.go
type PartitionConfig struct {
    Table         string        // parent table name
    Database      string        // which database this table lives in
    Interval      string        // "daily", "weekly", or "monthly"
    CreateAhead   int           // number of future partitions to maintain
    DropOlderThan time.Duration // drop partitions older than this (0 = never)
}

func DefaultPartitionConfigs() []PartitionConfig {
    return []PartitionConfig{
        {
            Table:         "outbox",
            Database:      "transfer",
            Interval:      "daily",
            CreateAhead:   7,
            DropOlderThan: 7 * 24 * time.Hour,
        },
        {
            Table:       "transfers",
            Database:    "transfer",
            Interval:    "monthly",
            CreateAhead: 2,
        },
        {
            Table:       "transfer_events",
            Database:    "transfer",
            Interval:    "monthly",
            CreateAhead: 2,
        },
        {
            Table:       "entry_lines",
            Database:    "ledger",
            Interval:    "weekly",
            CreateAhead: 8,
        },
        {
            Table:       "position_history",
            Database:    "treasury",
            Interval:    "monthly",
            CreateAhead: 2,
        },
        {
            Table:       "bank_deposit_sessions",
            Database:    "transfer",
            Interval:    "monthly",
            CreateAhead: 2,
        },
        {
            Table:       "bank_deposit_transactions",
            Database:    "transfer",
            Interval:    "monthly",
            CreateAhead: 2,
        },
    }
}
```

Each table has a strategy tuned to its access pattern:

```
┌────────────────────────────┬──────────┬─────────┬─────────────────────────┐
│ Table                      │ Interval │ Ahead   │ Retention               │
├────────────────────────────┼──────────┼─────────┼─────────────────────────┤
│ outbox                     │ daily    │ 7 days  │ 7 days (DROP)           │
│ transfers                  │ monthly  │ 2 months│ Never auto-drop         │
│ transfer_events            │ monthly  │ 2 months│ Never auto-drop         │
│ entry_lines                │ weekly   │ 8 weeks │ Never auto-drop         │
│ position_history           │ monthly  │ 2 months│ Never auto-drop         │
│ bank_deposit_sessions      │ monthly  │ 2 months│ Never auto-drop         │
│ bank_deposit_transactions  │ monthly  │ 2 months│ Never auto-drop         │
│ position_events (treasury) │ monthly  │ 6 months│ 90 days (DROP)          │
│ provider_webhook_logs      │ monthly  │ 6 months│ Never auto-drop         │
│ net_settlements            │ monthly  │ 6 months│ Never auto-drop         │
│ position_transactions      │ monthly  │ 6 months│ Never auto-drop         │
└────────────────────────────┴──────────┴─────────┴─────────────────────────┘
```

The last four tables (`position_events`, `provider_webhook_logs`, `net_settlements`, `position_transactions`) are newer additions. Their migrations create 6 months of partitions ahead plus a default partition -- managed inline in the migration DDL rather than exclusively through the PartitionManager. The migration count now extends through 000028.

---

## Why Each Strategy

### Outbox: Daily Partitions, 7-Day Retention

The outbox table has the most aggressive strategy because:
- It grows by 300M rows/day
- Entries are only needed until published (usually within seconds)
- 7-day retention provides safety margin for relay failures and operational debugging
- After 7 days, if an entry hasn't been published, it's a bug -- the stuck-transfer detector handles it

```
outbox (parent)
├── outbox_2026_03_08   ← DROP TABLE (instant, no locks)
├── outbox_2026_03_09   ← DROP TABLE (instant)
├── ...                 ← retained 7 days
├── outbox_2026_03_15   ← CURRENT (active writes)
├── outbox_2026_03_16   ← pre-created (empty, ready)
├── ...                 ← 7 days ahead
└── outbox_2026_03_22   ← pre-created
```

### Entry Lines: Weekly Partitions, 8 Ahead

Entry lines grow by 250M rows/day but are never deleted (immutable ledger). Weekly partitions keep each partition to ~1.75B rows — large but manageable for index operations.

```
entry_lines (parent)
├── entry_lines_2026_w10   ← queries can still read
├── entry_lines_2026_w11   ← queries can still read
├── entry_lines_2026_w12   ← CURRENT (active writes)
├── entry_lines_2026_w13 through w20  ← pre-created (8 ahead)
```

### Transfers: Monthly Partitions, No Auto-Drop

Transfers are the business record of every settlement. Compliance requires retaining them for years. Monthly partitions keep queries efficient while preserving all data.

### Position Events: Monthly Partitions, 90-Day Retention

The `position_events` table lives in Treasury DB and records every treasury position mutation as an immutable event. At peak load (~20,000 events/sec), this table grows faster than any other in the system:

```
position_events (parent, Treasury DB)
├── position_events_2026_01   ← DROP TABLE (older than 90 days)
├── position_events_2026_02   ← DROP TABLE (older than 90 days)
├── position_events_2026_03   ← CURRENT (active writes)
├── position_events_2026_04   ← pre-created
├── ...                       ← 6 months ahead
└── position_events_default   ← safety net
```

90-day retention balances audit requirements against storage costs. The events serve three purposes: compliance audit, crash recovery (replay since last snapshot), and tenant-facing history. After 90 days, the position snapshots in `position_history` provide the long-term record.

### Provider Webhook Logs: Monthly Partitions

The `provider_webhook_logs` table stores raw webhook payloads from payment providers before normalization. At 50M transfers/day with ~2 webhooks per transfer, this table accumulates ~100M rows/day:

```sql
CREATE TABLE IF NOT EXISTS provider_webhook_logs (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    provider_slug   TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    transfer_id     UUID,
    tenant_id       UUID,
    raw_body        BYTEA NOT NULL,
    normalized      JSONB,
    status          TEXT NOT NULL DEFAULT 'received'
                    CHECK (status IN ('received', 'processed', 'skipped', 'failed', 'duplicate')),
    error_message   TEXT,
    http_headers    JSONB,
    source_ip       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at    TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

The migration (000024) uses a dynamic `DO $$ ... END $$` block to create 6 months of partitions ahead. This is useful for provider debugging, deduplication, and replay after normalizer fixes.

### Net Settlements: Monthly Partitions

The `net_settlements` table was converted from unpartitioned to monthly partitions in migration 000027. At 20K-100K tenants with daily settlements, this table grows by ~36.5M rows/year at scale:

```sql
CREATE TABLE net_settlements (
    -- ... columns ...
    PRIMARY KEY (id, created_at),
    UNIQUE (tenant_id, period_start, period_end, created_at)
) PARTITION BY RANGE (created_at);
```

Note the partition key (`created_at`) must be included in every unique constraint. Since `created_at` defaults to `now()` and settlements are created once per period, collisions are practically impossible.

### Position Transactions: Monthly Partitions

The `position_transactions` table (migration 000025) stores tenant-initiated position changes (top-ups, withdrawals, deposit credits, internal rebalances). Each follows a state machine: PENDING -> PROCESSING -> COMPLETED/FAILED. Monthly partitions with RLS for tenant isolation:

```sql
CREATE TABLE position_transactions (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('TOP_UP', 'WITHDRAWAL', 'DEPOSIT_CREDIT', 'INTERNAL_REBALANCE')),
    currency        TEXT NOT NULL,
    location        TEXT NOT NULL,
    amount          NUMERIC(28, 8) NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING',
    -- ...
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

---

## DROP TABLE vs DELETE: The Critical Difference

```
┌─────────────────────────────────────────────────────────┐
│           DELETE FROM vs DROP TABLE                       │
├─────────────────────────────────────────────────────────┤
│                                                          │
│  DELETE FROM outbox WHERE created_at < '2026-03-13':     │
│                                                          │
│  1. Sequential scan of 9+ BILLION rows         ~hours   │
│  2. Each row: mark as dead tuple in heap        ~hours   │
│  3. Write WAL record for each deletion          ~100 GB  │
│  4. Hold row-level locks during scan            blocks   │
│  5. VACUUM must process all dead tuples         ~hours   │
│  6. Index entries must be cleaned up            ~hours   │
│  7. Table bloat persists until VACUUM finishes           │
│  Total time: 4-12 HOURS                                  │
│  Impact: production writes blocked/degraded              │
│                                                          │
│  ─────────────────────────────────────────────────        │
│                                                          │
│  DROP TABLE outbox_2026_03_13:                           │
│                                                          │
│  1. Remove filesystem entry                     ~1ms     │
│  2. Update pg_class catalog                     ~1ms     │
│  3. Release disk blocks to OS                   ~1ms     │
│  Total time: < 10 MILLISECONDS                           │
│  Impact: zero effect on production writes                │
│                                                          │
│  Speedup: ~1,000,000x                                    │
│                                                          │
└─────────────────────────────────────────────────────────┘
```

> **Key Insight:** DROP TABLE is an O(1) metadata operation regardless of row count. It works by removing the filesystem entry that points to the table's data files. The OS reclaims the disk space. No row scanning, no WAL generation, no VACUUM needed. This is why partitioning is mandatory at scale — it converts a catastrophic DELETE into an instant DROP.

---

## The PartitionManager

The PartitionManager runs as a background service, typically on a single instance (leader election or cron):

```go
// From core/maintenance/partition_manager.go
type PartitionManager struct {
    configs  []PartitionConfig
    executor DBExecutor
    logger   *slog.Logger
}
```

### Creating Future Partitions

The manager pre-creates partitions ahead of time so that writes never fail due to a missing partition:

```sql
-- For monthly partitions:
CREATE TABLE IF NOT EXISTS transfers_2026_04
  PARTITION OF transfers
  FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

-- For weekly partitions:
CREATE TABLE IF NOT EXISTS entry_lines_2026_w14
  PARTITION OF entry_lines
  FOR VALUES FROM ('2026-03-30') TO ('2026-04-06');

-- For daily partitions:
CREATE TABLE IF NOT EXISTS outbox_2026_03_18
  PARTITION OF outbox
  FOR VALUES FROM ('2026-03-18') TO ('2026-03-19');
```

### Dropping Old Partitions

For tables with `DropOlderThan > 0`, the manager drops partitions beyond the retention window:

```sql
-- Drop outbox partitions older than 48 hours:
DROP TABLE IF EXISTS outbox_2026_03_13;
-- Instant. No matter if it held 300M rows.
```

### Default Partitions

Every partitioned table has a `DEFAULT` partition that catches rows not matching any defined range. This is a safety net — if the PartitionManager fails to create a future partition, rows still land in the default partition instead of failing with an error.

```sql
CREATE TABLE transfers_default
  PARTITION OF transfers DEFAULT;
```

The manager monitors the default partition — if it contains rows, that's an alert condition indicating partitions aren't being created fast enough.

---

## VACUUM and Maintenance

Even with partitioning, active partitions need maintenance:

**Autovacuum tuning for high-write tables:**
```sql
ALTER TABLE outbox SET (
  autovacuum_vacuum_scale_factor = 0.01,      -- vacuum at 1% dead tuples (default 20%)
  autovacuum_analyze_scale_factor = 0.005,     -- analyze at 0.5%
  autovacuum_vacuum_cost_delay = 0             -- no throttling during vacuum
);
```

**Index bloat monitoring:**
The capacity monitor tracks index size vs table size ratios. When an index exceeds 2x the table size, it triggers an alert for potential REINDEX.

---

## Common Mistakes

**Mistake 1: Not pre-creating partitions far enough ahead**
If the PartitionManager is down for several days and you only create 1 day ahead, writes fail. Settla creates 7 days ahead for daily partitions (outbox), 8 weeks ahead for weekly (entry_lines), and 2-6 months ahead for monthly tables. The default partitions provide a safety net, but triggering them is an alert condition.

**Mistake 2: Using DELETE instead of DROP for cleanup**
At 300M rows/day, DELETE generates hundreds of GB of WAL, blocks production writes, and takes hours. DROP takes milliseconds.

**Mistake 3: Partitioning by the wrong column**
Partition key must match query patterns. If you partition by `created_at` but query by `tenant_id`, every query scans all partitions (partition pruning doesn't help).

**Mistake 4: Forgetting the DEFAULT partition**
Without a DEFAULT partition, any row that doesn't match a defined range causes an INSERT error. With it, the row is caught safely and the alert system flags the gap.

---

## Exercises

### Exercise 1: Partition Strategy Design
Design partition strategies for an e-commerce system with:
- `orders` table: 5M rows/day, queried by date range, 2-year retention
- `order_events`: 30M rows/day, queried by order_id, 90-day retention
- `notifications`: 50M rows/day, queried by user_id, 7-day retention

### Exercise 2: Retention Calculator
Given 300M outbox rows/day at 300 bytes each:
1. Storage per day, per month, per year
2. If retention is 7 days, maximum rows at any time
3. If you accidentally use DELETE instead of DROP, estimate the time and WAL size

### Exercise 4: Position Events Growth
The `position_events` table receives ~20,000 events/sec at peak. Each event is ~400 bytes (UUID fields, decimals, timestamps, text).
1. Calculate daily row count and storage at sustained peak
2. With 90-day retention, what is the maximum table size?
3. The event writer batches inserts every 10ms. At 20K events/sec, how many events per batch? What is the Postgres insert rate (batches/sec)?

### Exercise 3: Migration Planning
You have an existing `transfers` table with 2B rows (unpartitioned). Design a migration plan to convert it to monthly partitions with zero downtime.

---

## What's Next

Module 2 is complete. You now understand double-entry accounting, TigerBeetle, CQRS, write batching, reversals, and partitioning. Module 3 builds the settlement engine — the pure state machine that coordinates everything through the transactional outbox pattern.
