# ADR-018: Partition DROP vs DELETE for Cleanup at Scale

**Status:** Accepted
**Date:** 2026-03-09
**Authors:** Engineering Team

## Context

Settla generates ephemeral high-volume data that must be cleaned up regularly. The primary example is the outbox table (ADR-014), but this pattern applies to any table where old rows have no ongoing value:

| Table | Daily Rows | Retention | Cleanup Volume |
|-------|-----------|-----------|---------------|
| `outbox_entries` | 100-150M | 48 hours | 100-150M rows/day |
| `transfer_events` | 50M | 90 days | 50M rows/day (after 90 days) |
| `rate_limit_counters` | ~500M | 24 hours | ~500M rows/day |

The outbox table is the most extreme case: 100-150M rows written per day, with published entries valueless after 48 hours. At this scale, traditional cleanup approaches fail:

### Why DELETE Does Not Work

```sql
DELETE FROM outbox_entries WHERE published_at < NOW() - INTERVAL '48 hours';
```

At 100M rows per day, this DELETE targets ~100M rows. The consequences:

1. **Execution time**: Postgres must scan 100M rows, write 100M dead tuple markers to the heap, and update all associated indexes. At ~10,000 deletes/sec (typical for indexed tables), this takes **2.8 hours**.

2. **Dead tuple accumulation**: the 100M deleted rows become dead tuples that consume disk space and degrade sequential scan performance. They remain until VACUUM reclaims them.

3. **VACUUM overhead**: autovacuum must process the entire table (including live rows) to reclaim dead tuple space. On a table with 200-300M rows (2-3 days of data), VACUUM can take **1-4 hours** and consumes significant I/O bandwidth, competing with production writes.

4. **Table bloat**: between DELETE and VACUUM completion, the table is bloated with dead tuples. This increases the table's physical size, degrades cache efficiency (dead tuples pollute shared_buffers), and slows index scans.

5. **Write amplification**: each DELETE writes to the heap page AND every index on the table. With 3 indexes on the outbox table, each row deletion generates 4 I/O operations. For 100M rows, that is 400M I/O operations.

Even with batch DELETEs (e.g., 10,000 rows per batch with short sleeps), the total I/O and VACUUM cost is the same — batching only spreads it over time to reduce lock contention.

### Why TRUNCATE Does Not Work

`TRUNCATE` removes all rows instantly but cannot selectively remove old rows while preserving recent ones. It requires an exclusive lock that blocks all concurrent reads and writes — unacceptable for a table under constant write load.

## Decision

We chose **daily table partitioning with DROP TABLE for cleanup** on all high-volume ephemeral tables.

### Partitioning Scheme

The outbox table uses daily partitions (unlike the monthly partitions in ADR-013 for long-lived data):

```sql
CREATE TABLE outbox (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    aggregate_type  TEXT NOT NULL,
    aggregate_id    UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    is_intent       BOOLEAN NOT NULL DEFAULT false,
    published       BOOLEAN NOT NULL DEFAULT false,
    published_at    TIMESTAMPTZ,
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 5,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Daily partitions (naming: outbox_y{YYYY}m{MM}d{DD})
CREATE TABLE outbox_y2026m03d09
    PARTITION OF outbox
    FOR VALUES FROM ('2026-03-09') TO ('2026-03-10');

CREATE TABLE outbox_y2026m03d10
    PARTITION OF outbox
    FOR VALUES FROM ('2026-03-10') TO ('2026-03-11');

-- Default partition catches any overflow
CREATE TABLE outbox_default
    PARTITION OF outbox DEFAULT;
```

### Cleanup via DROP

```sql
-- Instant, O(1), regardless of row count
DROP TABLE IF EXISTS outbox_y2026m03d07;
```

`DROP TABLE` is a metadata operation. It removes the table from the catalog, releases the underlying file system space, and updates the partition list. It does not scan any rows, generate any dead tuples, or require VACUUM. On a partition with 150M rows, it completes in **<100 milliseconds**.

### Partition Lifecycle

```
Day N-3: Partition created (3 days ahead)
Day N:   Partition receives writes (active)
Day N+1: Partition is read-only (outbox relay may still read recent entries)
Day N+2: Partition is eligible for DROP (all entries published >24h ago)
Day N+2: Cron job executes DROP TABLE
```

### Partition Management

A cron job (or Kubernetes CronJob) runs daily and performs two operations:

1. **Create future partitions**: ensure partitions exist for the next 3 days
2. **Drop expired partitions**: drop partitions older than 48 hours

Partition management is implemented in `node/outbox.Cleanup` (Go, runs in `settla-node`). It creates daily partitions 3 days ahead and drops partitions older than 48 hours, using the naming convention `outbox_y{YYYY}m{MM}d{DD}`. The equivalent shell script for reference:

```bash
#!/bin/bash
# Create partitions for next 3 days
for i in 1 2 3; do
    date=$(date -d "+${i} days" +%Y-%m-%d)
    next=$(date -d "+$((i+1)) days" +%Y-%m-%d)
    psql -c "CREATE TABLE IF NOT EXISTS outbox_y$(date -d "+${i} days" +%Ym%md%d)
             PARTITION OF outbox FOR VALUES FROM ('${date}') TO ('${next}');"
done

# Drop partitions older than 48 hours
for partition in $(psql -t -c "SELECT tablename FROM pg_tables
    WHERE tablename LIKE 'outbox_y%' AND tablename < 'outbox_y$(date -d "-2 days" +%Ym%md%d)'"); do
    psql -c "DROP TABLE IF EXISTS ${partition};"
done
```

### Query Requirements

All queries against daily-partitioned tables MUST include a `created_at` predicate to enable partition pruning:

```sql
-- Good: partition pruning active, scans only today's partition
SELECT * FROM outbox_entries
WHERE published_at IS NULL AND created_at >= CURRENT_DATE
ORDER BY created_at LIMIT 100;

-- Bad: scans ALL partitions (no created_at filter)
SELECT * FROM outbox_entries WHERE published_at IS NULL LIMIT 100;
```

The outbox relay query filters by `published = false` and relies on the `idx_outbox_unpublished` partial index (`WHERE published = false`) for fast scans on the current day's partition. The partition key `created_at` is part of the primary key, enabling partition pruning on point lookups.

## Consequences

### Benefits
- **Instant cleanup regardless of data volume**: DROP TABLE completes in <100ms whether the partition has 1 row or 150M rows. There is no O(n) cost, no dead tuples, and no VACUUM. Cleanup time is constant as data volume grows.
- **Zero table bloat**: since no rows are ever DELETEd, there are no dead tuples. Each partition's physical size exactly matches its live data. shared_buffers are not polluted with dead tuples, improving cache hit rates.
- **No VACUUM contention**: the most operationally painful aspect of high-volume Postgres — autovacuum competing with production workloads — is eliminated for partitioned tables. VACUUM still runs on active partitions (for transaction ID wraparound prevention) but processes only 1 day of data, not the full table.
- **Predictable disk usage**: each daily partition holds a known volume of data (100-150M rows for outbox, ~1.5-2GB). Total disk usage = retention period in days x daily partition size. No surprises from table bloat or vacuum lag.
- **Independent partition operations**: each partition can be independently analyzed (ANALYZE), backed up, or moved to different tablespaces (e.g., faster SSD for recent partitions, cheaper storage for older ones).

### Trade-offs
- **Partition management cron**: the system depends on a cron job to create future partitions and drop expired ones. If the cron job fails:
  - Missing future partition: new rows land in the DEFAULT partition (continues working but defeats partition pruning). Monitoring detects this via DEFAULT partition row count.
  - Missing DROP: old partitions accumulate, consuming disk space. Monitoring detects this via partition count alerts.
- **Query discipline**: developers must include `created_at` in all queries to benefit from partition pruning. Queries without `created_at` perform a cross-partition scan, which is slower than scanning a single unpartitioned table due to per-partition planning overhead.
- **More complex schema migrations**: adding columns or indexes to a daily-partitioned table requires consideration of existing partitions. `ALTER TABLE parent ADD COLUMN` propagates to all partitions (correct behavior). `CREATE INDEX` on the parent creates indexes on all existing partitions (can be slow if many partitions exist).
- **Short retention ceiling**: daily partitioning with 48-hour retention is appropriate for ephemeral data (outbox, rate limit counters) but not for data with longer retention requirements. Long-lived data uses monthly partitioning (ADR-013).

### Mitigations
- **DEFAULT partition monitoring**: alert when `SELECT count(*) FROM outbox_entries_default > 0`. Any rows in the DEFAULT partition indicate a missing daily partition.
- **Partition count monitoring**: alert when the number of outbox partitions exceeds expected count (retention days + lookahead days + 1 default = ~6). More partitions indicate the DROP cron is failing.
- **SQLC-enforced query patterns**: all queries are written in SQLC query files with explicit `created_at` filters. Code review catches queries missing the partition key. SQLC's generated Go code makes it impossible to call a query without providing the `created_at` parameter.
- **Dual partitioning strategy**: ephemeral tables (outbox, rate limits) use daily partitions with short retention. Long-lived tables (transfers, journal entries) use monthly partitions with longer retention. The partitioning strategy matches the data lifecycle.

## Threshold Triggers for Revisiting

- **Partition count exceeds 365**: if retention requirements extend beyond 1 year for daily-partitioned tables, partition count causes query planner overhead (Postgres evaluates each partition during planning). Migration path: TimescaleDB for automatic partition management, or switch to monthly partitions for longer-retention data.
- **Cron reliability becomes an operational burden**: if partition management failures occur frequently despite monitoring. Migration path: pg_partman extension for automated partition management, or TimescaleDB hypertables.
- **Cross-partition queries become common**: if business requirements increasingly need queries across the full time range (e.g., "find all outbox entries for transfer X across all time"). Migration path: add a covering index on the parent table, or maintain a separate lookup table for cross-time queries.

## References

- [PostgreSQL Table Partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html) — PostgreSQL documentation
- [pg_partman Extension](https://github.com/pgpartman/pg_partman) — automated partition management
- [TimescaleDB](https://www.timescale.com/) — automatic time-series partitioning
- ADR-013 (Monthly Table Partitioning) — partitioning strategy for long-lived data
- ADR-014 (Transactional Outbox) — the primary consumer of daily partitioning
