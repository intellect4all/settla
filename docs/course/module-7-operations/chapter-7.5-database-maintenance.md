# Chapter 7.5: Database Maintenance at Scale

**Reading time: 28 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why partition management is critical at 50M+ rows/day
2. Describe the three partition strategies (daily, weekly, monthly) and when each applies
3. Analyze the performance difference between DROP TABLE and DELETE for data cleanup
4. Trace the partition manager's create-drop-verify cycle through code
5. Design partition naming conventions that prevent ambiguity and enable automation

---

## The Scale Problem

Settla writes to its databases at extreme volume:

```
DAILY WRITE VOLUMES

  Transfer DB:
    transfers:        50,000,000 rows/day
    transfer_events:  50,000,000 rows/day (1 event per transfer minimum)
    outbox:          100,000,000 rows/day (2 outbox entries per transfer average)
    provider_transactions: 100,000,000 rows/day

  Ledger DB:
    entry_lines:     200,000,000 rows/day (4 postings per transfer average)

  Transfer DB (continued):
    position_transactions: 500,000 rows/day (tenant top-ups, withdrawals, rebalancing)
    provider_webhook_logs: 10,000,000 rows/day (inbound provider callbacks)
    net_settlements:       50,000 rows/day (daily settlement cycles)

  Treasury DB:
    position_history:  5,000,000 rows/day (100ms flush interval)
    position_events:  20,000,000 rows/day (event-sourced audit trail, ~20K events/sec peak)
```

Without partition management, these tables would degrade rapidly:

```
  TABLE GROWTH WITHOUT PARTITIONING

  Day 1:     50M rows     -- queries: < 100ms
  Day 7:    350M rows     -- queries: 200ms, autovacuum takes 2 hours
  Day 30:  1.5B rows      -- queries: 800ms, index bloat 40%
  Day 90:  4.5B rows      -- queries: 3s, autovacuum never finishes
  Day 180: 9.0B rows      -- queries: timeout, disk full

  WITH MONTHLY PARTITIONING + 48h OUTBOX DROP:

  transfers:   always ~50M-100M rows per partition (fast queries)
  outbox:      always ~200M rows (2 days), old data dropped instantly
  entry_lines: always ~1.4B rows per partition (weekly, manageable)
```

---

## Partition Strategy

**File:** `core/maintenance/partition_manager.go`

### Configuration

Each table has a partition configuration:

```go
type PartitionConfig struct {
    Table         string        // parent table name
    Database      string        // which database this table lives in
    Interval      string        // "daily", "weekly", or "monthly"
    CreateAhead   int           // number of future partitions to maintain
    DropOlderThan time.Duration // drop partitions older than this (0 = never drop)
}
```

### Default Configurations

```go
func DefaultPartitionConfigs() []PartitionConfig {
    return []PartitionConfig{
        {
            Table:         "outbox",
            Database:      "transfer",
            Interval:      "daily",
            CreateAhead:   3,
            DropOlderThan: 48 * time.Hour,
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
        {
            Table:         "net_settlements",
            Database:      "transfer",
            Interval:      "monthly",
            CreateAhead:   2,
        },
        {
            Table:         "position_events",
            Database:      "treasury",
            Interval:      "monthly",
            CreateAhead:   2,
            DropOlderThan: 90 * 24 * time.Hour, // 90-day retention
        },
        {
            Table:         "provider_webhook_logs",
            Database:      "transfer",
            Interval:      "monthly",
            CreateAhead:   2,
        },
        {
            Table:         "position_transactions",
            Database:      "transfer",
            Interval:      "monthly",
            CreateAhead:   2,
        },
    }
}
```

Why different intervals per table?

| Table | Interval | Rationale |
|-------|----------|-----------|
| `outbox` | **Daily**, drop after 48h | Highest write volume (100M/day). Data is ephemeral: once published to NATS, the relay marks it done. After 48h, entries are useless. Daily partitions keep each partition small (~100M rows) for fast DROP. |
| `transfers` | **Monthly**, no auto-drop | Business data, needed for compliance and settlement. Monthly gives ~1.5B rows per partition, which PostgreSQL handles well with proper indexes. Archival is future work. |
| `transfer_events` | **Monthly**, no auto-drop | Audit trail, must be retained. Same rationale as transfers. |
| `entry_lines` | **Weekly**, 8 ahead | Second-highest volume (200M/day). Weekly partitions give ~1.4B rows each. 8 weeks ahead provides a 2-month buffer for partition creation. No auto-drop because ledger entries are the regulatory record. |
| `position_history` | **Monthly**, no auto-drop | Moderate volume (5M/day). Monthly is sufficient. Treasury position history is used for auditing and trend analysis. |
| `bank_deposit_*` | **Monthly**, no auto-drop | Business data for bank deposit flows, needed for reconciliation. |
| `net_settlements` | **Monthly**, no auto-drop | Settlement records for compliance. Monthly partitioned by `created_at`. |
| `position_events` | **Monthly**, drop after 90 days | Event-sourced treasury audit trail. High volume (~20M/day) but only needed for recent audit. 90-day retention balances audit needs with storage. |
| `provider_webhook_logs` | **Monthly**, no auto-drop | Webhook audit trail for provider callback debugging and reconciliation. |
| `position_transactions` | **Monthly**, no auto-drop | Position top-up/withdrawal records for tenant self-service portal. |

```
  PARTITION TIMELINE VISUALIZATION

  outbox (daily, 3 ahead, drop after 48h):

  ... | D-3 | D-2 | D-1 | TODAY | D+1 | D+2 | D+3 |
       DROP   DROP  keep   keep  create create create


  transfers (monthly, 2 ahead, no drop):

  ... | Jan | Feb | Mar  | Apr  | May  |
       keep  keep  THIS   create create
                   MONTH


  entry_lines (weekly, 8 ahead, no drop):

  ... | W-2 | W-1 | THIS | W+1 | W+2 | ... | W+8 |
       keep  keep  WEEK  create create     create
```

---

## The Partition Manager

```go
type PartitionManager struct {
    db      DBExecutor
    logger  *slog.Logger
    configs []PartitionConfig
}
```

### The Management Cycle

The `ManagePartitions` method runs three operations:

```go
func (pm *PartitionManager) ManagePartitions(ctx context.Context) error {
    var errs []error

    for _, config := range pm.configs {
        // 1. Create future partitions
        if err := pm.createFuturePartitions(ctx, config); err != nil {
            errs = append(errs, err)
        }

        // 2. Drop old partitions (if configured)
        if config.DropOlderThan > 0 {
            if err := pm.dropOldPartitions(ctx, config); err != nil {
                errs = append(errs, err)
            }
        }
    }

    // 3. Verify default partitions are empty
    if err := pm.verifyDefaultPartitions(ctx); err != nil {
        errs = append(errs, err)
    }

    if len(errs) > 0 {
        return fmt.Errorf("settla-maintenance: %d partition errors: %v", len(errs), errs[0])
    }
    return nil
}
```

**Important:** The cycle continues even if one table fails. If the outbox partition creation fails, the manager still creates partitions for transfers, entry_lines, etc. This prevents a single table's issue from blocking maintenance on all tables.

### Creating Future Partitions

```go
func (pm *PartitionManager) createFuturePartitions(ctx context.Context, config PartitionConfig) error {
    now := time.Now().UTC()

    for i := 0; i <= config.CreateAhead; i++ {
        var partStart, partEnd time.Time
        var partName string

        switch config.Interval {
        case "daily":
            partStart = now.AddDate(0, 0, i).Truncate(24 * time.Hour)
            partEnd = partStart.AddDate(0, 0, 1)
            partName = DailyPartitionName(config.Table, partStart)
        case "weekly":
            // Align to Monday
            weekday := int(now.Weekday())
            if weekday == 0 {
                weekday = 7
            }
            mondayOffset := 1 - weekday
            thisMonday := now.AddDate(0, 0, mondayOffset).Truncate(24 * time.Hour)
            partStart = thisMonday.AddDate(0, 0, 7*i)
            partEnd = partStart.AddDate(0, 0, 7)
            partName = WeeklyPartitionName(config.Table, partStart)
        case "monthly":
            partStart = time.Date(now.Year(), now.Month()+time.Month(i),
                1, 0, 0, 0, 0, time.UTC)
            partEnd = partStart.AddDate(0, 1, 0)
            partName = MonthlyPartitionName(config.Table, partStart)
        }

        sql := CreatePartitionSQL(config.Table, partName, partStart, partEnd)
        pm.db.Exec(ctx, sql)
    }

    return nil
}
```

The SQL generated is idempotent:

```go
func CreatePartitionSQL(parentTable, partitionName string, from, to time.Time) string {
    return fmt.Sprintf(
        "CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
        partitionName, parentTable,
        from.Format("2006-01-02"), to.Format("2006-01-02"),
    )
}
```

`CREATE TABLE IF NOT EXISTS` means the manager can run repeatedly without error. This is critical because the manager runs on a schedule, and multiple server replicas may run it concurrently.

### Partition Naming Convention

```go
// Daily:   outbox_y2026m03d15
func DailyPartitionName(table string, date time.Time) string {
    return fmt.Sprintf("%s_y%dm%02dd%02d", table, date.Year(), date.Month(), date.Day())
}

// Weekly:  entry_lines_y2026w12
func WeeklyPartitionName(table string, date time.Time) string {
    _, week := date.ISOWeek()
    return fmt.Sprintf("%s_y%dw%02d", table, date.Year(), week)
}

// Monthly: transfers_y2026m03
func MonthlyPartitionName(table string, date time.Time) string {
    return fmt.Sprintf("%s_y%dm%02d", table, date.Year(), date.Month())
}
```

The naming convention is designed for:
- **Sortability:** Names sort lexicographically in chronological order
- **Parseability:** The date components can be extracted from the name for automation
- **Uniqueness:** The combination of table name + date components is globally unique
- **Human readability:** `outbox_y2026m03d15` is immediately understandable

---

## DROP TABLE vs DELETE: Why Partitions Exist

This is the most important concept in database maintenance at scale.

```
  DELETE 100M rows from outbox WHERE created_at < '2026-03-13':

  Step 1: Scan 100M rows to find matching records        ~30 seconds
  Step 2: Mark each row as deleted (write dead tuples)   ~60 seconds
  Step 3: Write WAL entries for each delete              ~45 seconds
  Step 4: Autovacuum reclaims dead tuples                ~120 seconds
  Step 5: Index maintenance (remove dead index entries)  ~90 seconds
  ---------------------------------------------------------------
  TOTAL:                                                 ~345 seconds (5.75 min)
  WAL generated:                                         ~50 GB
  Table bloat during operation:                          ~40%
  CPU utilization during vacuum:                         80-100%
  Impact on concurrent queries:                          SEVERE


  DROP TABLE outbox_y2026m03d13:

  Step 1: Remove filesystem reference                    ~10 ms
  Step 2: Update pg_class catalog                        ~5 ms
  ---------------------------------------------------------------
  TOTAL:                                                 ~15 ms
  WAL generated:                                         ~1 KB
  Table bloat:                                           0%
  CPU utilization:                                       0%
  Impact on concurrent queries:                          NONE
```

DROP TABLE is **23,000x faster** than DELETE for the same data removal. More importantly:

- No dead tuples means no vacuum pressure
- No WAL bloat means replication stays healthy
- No table bloat means queries on remaining data stay fast
- No index maintenance means write throughput is unaffected

> **Key Insight:** Partitioning is not primarily about query performance (though it helps). Partitioning is about making data lifecycle management O(1) instead of O(n). At 100M outbox rows/day, the difference between O(1) DROP and O(n) DELETE is the difference between a system that runs indefinitely and one that degrades within weeks.

### The Drop Implementation

```go
func (pm *PartitionManager) dropOldPartitions(ctx context.Context, config PartitionConfig) error {
    cutoff := time.Now().UTC().Add(-config.DropOlderThan)

    maxLookback := 30
    if config.Interval == "monthly" {
        maxLookback = 12
    }

    for i := 0; i < maxLookback; i++ {
        var partDate time.Time
        var partName string

        switch config.Interval {
        case "daily":
            partDate = cutoff.AddDate(0, 0, -i).Truncate(24 * time.Hour)
            partName = DailyPartitionName(config.Table, partDate)
        case "weekly":
            partDate = cutoff.AddDate(0, 0, -7*i).Truncate(24 * time.Hour)
            partName = WeeklyPartitionName(config.Table, partDate)
        case "monthly":
            partDate = time.Date(cutoff.Year(), cutoff.Month()-time.Month(i),
                1, 0, 0, 0, 0, time.UTC)
            partName = MonthlyPartitionName(config.Table, partDate)
        }

        sql := DropPartitionSQL(partName)
        if _, err := pm.db.Exec(ctx, sql); err != nil {
            pm.logger.Warn("settla-maintenance: failed to drop partition (may not exist)",
                "partition", partName, "error", err)
            // Non-fatal: partition may not exist
        }
    }

    return nil
}
```

The drop SQL is also idempotent:

```go
func DropPartitionSQL(partitionName string) string {
    return fmt.Sprintf("DROP TABLE IF EXISTS %s", partitionName)
}
```

The lookback of 30 days (or 12 months for monthly) ensures that even if the manager was down for an extended period, it catches up and drops all stale partitions in a single run.

Drop errors are logged as warnings, not errors, because the most common "failure" is attempting to drop a partition that was already dropped by a previous run.

---

## Default Partition Verification

The third step in the management cycle verifies that default partitions contain no data:

```go
func (pm *PartitionManager) verifyDefaultPartitions(ctx context.Context) error {
    tablesToCheck := []string{"outbox"}

    for _, table := range tablesToCheck {
        defaultName := table + "_default"
        sql := DefaultPartitionCheckSQL(defaultName)

        rows, err := pm.db.Query(ctx, sql)
        // ...

        var count int64
        if rows.Next() {
            rows.Scan(&count)
        }
        rows.Close()

        if count > 0 {
            pm.logger.Warn(
                "settla-maintenance: data in default partition -- future partitions may be missing",
                "table", defaultName,
                "row_count", count,
            )
        }
    }

    return nil
}
```

The query uses `ONLY` to scan just the default partition, not the parent table:

```go
func DefaultPartitionCheckSQL(defaultPartitionName string) string {
    return fmt.Sprintf("SELECT COUNT(*) FROM ONLY %s", defaultPartitionName)
}
```

**Why this matters:** PostgreSQL range-partitioned tables have a default partition that catches rows not matching any range. If the partition manager fails to create tomorrow's daily partition for the outbox, all outbox entries created tomorrow will silently land in `outbox_default`. The outbox relay queries by date range and will miss these rows entirely, causing transfers to stall.

```
  DEFAULT PARTITION AS CANARY

  Normal operation (all partitions exist):

  INSERT INTO outbox (created_at='2026-03-15', ...)
    --> routed to outbox_y2026m03d15  (correct)

  Missing tomorrow's partition:

  INSERT INTO outbox (created_at='2026-03-16', ...)
    --> routed to outbox_default  (WRONG!)
    --> outbox relay query: WHERE created_at >= '2026-03-16'
    --> scans outbox_y2026m03d16 (doesn't exist yet)
    --> returns 0 rows
    --> transfers stall silently
```

The verification check catches this before it causes operational impact.

---

## The DBExecutor Interface

The partition manager uses a minimal interface for database operations:

```go
type DBExecutor interface {
    Exec(ctx context.Context, sql string, args ...interface{}) (CommandTag, error)
    Query(ctx context.Context, sql string, args ...interface{}) (Rows, error)
}

type CommandTag interface {
    RowsAffected() int64
}

type Rows interface {
    Next() bool
    Scan(dest ...interface{}) error
    Close()
    Err() error
}
```

This interface uses raw SQL intentionally. Partition management requires DDL statements (`CREATE TABLE`, `DROP TABLE`) that ORMs and query builders cannot express. The interface is narrow enough to be easily mocked for testing and implemented by any database driver.

---

## Vacuum Management

While partitioning eliminates the need for DELETE-based cleanup, PostgreSQL's autovacuum is still needed for tables that receive updates (not just inserts). Key considerations:

```
  VACUUM IMPACT AT SCALE

  Table: transfers (50M inserts/day + ~10M updates/day for state changes)

  Without tuning:
    autovacuum_vacuum_threshold = 50 (default)
    autovacuum_vacuum_scale_factor = 0.2 (default)
    Trigger: 50 + 0.2 * 1,500,000,000 = 300,000,050 dead tuples
    --> vacuum never runs until 300M dead tuples accumulate
    --> then takes 4+ hours to complete
    --> blocks index creation and other maintenance

  With tuning (per-table):
    autovacuum_vacuum_threshold = 10000
    autovacuum_vacuum_scale_factor = 0.01
    Trigger: 10000 + 0.01 * 50,000,000 = 510,000 dead tuples per partition
    --> vacuum runs frequently on small batches
    --> completes in seconds
    --> no operational impact
```

The key insight is that per-partition vacuuming is dramatically faster than whole-table vacuuming, because each partition is a separate physical table with its own dead tuple count. A monthly partition with 1.5B rows and 10M updates generates 10M dead tuples, which autovacuum can process in under a minute.

---

## Capacity Monitoring

Beyond partition management, the maintenance module monitors database capacity:

```
  CAPACITY THRESHOLDS

  Disk usage:
    70%  --> WARN: plan expansion
    85%  --> CRITICAL: expand immediately or drop non-essential data
    95%  --> EMERGENCY: system will halt within hours

  Partition count per table:
    < 20   --> healthy
    20-50  --> review retention policy
    > 50   --> performance impact likely, investigate

  Table bloat:
    < 10%  --> healthy
    10-30% --> schedule manual vacuum
    > 30%  --> immediate vacuum + reindex
```

---

## Common Mistakes

1. **Not creating partitions far enough ahead.** If the partition manager is down for maintenance and `CreateAhead` is 1, the system runs out of partitions within a day. The outbox uses `CreateAhead: 3` (3 days buffer), and entry_lines uses `CreateAhead: 8` (8 weeks buffer).

2. **Using DELETE instead of DROP for partition cleanup.** At 100M rows/day, a DELETE statement generates enough WAL to fill a 1TB replication slot in under a week. DROP TABLE generates no WAL.

3. **Forgetting the `IF NOT EXISTS` / `IF EXISTS` clauses.** Without idempotent DDL, concurrent partition manager instances would conflict. Multiple replicas running `ManagePartitions` simultaneously must not produce errors.

4. **Not checking the default partition.** Data in the default partition is invisible to range-scoped queries. The outbox relay, reconciliation checks, and settlement calculator all use date ranges that skip the default partition. Unchecked default partition data means silent data loss.

5. **Setting weekly partition alignment to Sunday.** The code explicitly aligns weekly partitions to Monday (ISO 8601 standard). Sunday alignment creates partition boundaries that split weekends, which complicates weekly reporting.

6. **Dropping partitions for tables that need regulatory retention.** The transfers and entry_lines tables have no `DropOlderThan` configured. Financial regulations typically require 7-year retention. Only the outbox (ephemeral relay data) and similar operational tables should have auto-drop policies.

---

## Recent Migration Highlights

Migrations 000024-000028 add several new tables and capabilities:

- **000024:** `provider_webhook_logs` -- Immutable log of all inbound provider webhooks with `ON CONFLICT DO NOTHING` deduplication by `(provider, external_id)`.
- **000025:** `position_transactions` -- Tenant-initiated position changes (top-ups, withdrawals) with state machine (PENDING -> PROCESSING -> COMPLETED/FAILED).
- **000026:** Settlement idempotency -- Adds unique constraint on settlement cycle parameters to prevent duplicate settlement calculations.
- **000027:** `net_settlements` partitioning -- Converts `net_settlements` to a range-partitioned table by `created_at` (monthly). Critical for settlement query performance as the table grows.
- **000028:** `max_pending_transfers` column on `tenants` -- Per-tenant limit on non-terminal transfers to prevent resource exhaustion DoS (Critical Invariant #13).

Treasury migration 000003 creates the `position_events` table for event-sourced treasury auditing with monthly partitions and 90-day retention.

---

## Exercises

1. **Partition math:** The outbox generates 100M rows/day with a daily partition strategy. Each row is approximately 500 bytes. Calculate: (a) the size of each daily partition, (b) the total disk used at any time with 48h retention, and (c) how much disk would be needed without partitioning after 30 days.

2. **Custom interval:** You need to add a new table `webhook_delivery_attempts` that generates 10M rows/day and should be retained for 7 days. Write the `PartitionConfig` for this table. Should you use daily or weekly partitions? Justify your choice.

3. **Failure recovery:** The partition manager has been down for 5 days. The outbox has `CreateAhead: 3`. What is the system state? Which queries are failing? Write the sequence of operations the manager performs when it starts up again.

4. **DROP vs DETACH:** PostgreSQL also supports `ALTER TABLE ... DETACH PARTITION`. When would you use DETACH instead of DROP? (Hint: consider archival requirements for regulatory compliance.)

5. **Cross-database challenge:** The partition configs span three databases (transfer, ledger, treasury). The current `DBExecutor` is a single connection. How would you modify the `PartitionManager` to manage partitions across multiple database connections? What interface changes are needed?

---

## What's Next

This concludes Module 7: Operations. You have now covered:

- **7.1 Reconciliation:** 6 automated consistency checks that verify system integrity
- **7.2 Compensation:** 4 strategies for recovering from partial transfer failures
- **7.3 Stuck Transfer Recovery:** Automated detection, recovery, and escalation
- **7.4 Net Settlement:** Reducing thousands of transfers to handful of net positions
- **7.5 Database Maintenance:** Partition management at 50M+ rows/day

Together, these systems form the operational backbone that allows Settla to run at 50M transactions/day without human intervention for routine operations. The reconciler detects problems, the recovery detector fixes them, the compensation system handles partial failures, the settlement calculator reduces operational overhead, and the partition manager keeps the databases healthy. Each module follows the same pattern: observe, act, record, and alert.
