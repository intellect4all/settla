# ADR-013: Monthly Table Partitioning

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla's high-volume tables grow at a rate that makes unpartitioned Postgres tables operationally untenable:

| Table | Daily Rows | Monthly Rows | Annual Rows |
|-------|-----------|-------------|-------------|
| `transfers` | 50M | 1.5B | 18B |
| `transfer_events` | 50M | 1.5B | 18B |
| `entry_lines` | 200-250M | 6-7.5B | 72-90B |
| `journal_entries` | 50-100M | 1.5-3B | 18-36B |

The thresholds that triggered this decision:

- **Query performance degrades with table size.** Postgres B-tree indexes on a 1.5B-row table have 5+ levels of depth. An index scan that takes 0.1ms on a 10M-row table takes 0.5-1ms on a 1.5B-row table due to additional page reads. At 580 TPS sustained, this adds 290-580ms of cumulative query time per second.
- **VACUUM becomes a bottleneck.** Autovacuum on a 1.5B-row table can run for hours, consuming I/O bandwidth and holding cleanup locks. During this time, table bloat accumulates, further degrading performance. At 50M dead tuples/day (from updates and deletes), vacuum must process the entire table to reclaim space.
- **Index maintenance slows writes.** Each INSERT into a 1.5B-row table must update multiple B-tree indexes, each with 5+ levels. At 15,000-25,000 writes/second peak on the ledger tables, index maintenance becomes a significant fraction of write latency.
- **Data retention requires full table scans.** Deleting records older than N months from a 18B-row table is effectively impossible without downtime. `DELETE FROM transfers WHERE created_at < '2025-09-01'` would generate billions of dead tuples and take days to vacuum.

## Decision

We chose **monthly range partitioning** on all high-volume tables, partitioned by `created_at` timestamp.

**Partitioned tables** (across all three databases):

Ledger DB:
- `journal_entries` — partitioned by `created_at`
- `entry_lines` — partitioned by `created_at`
- `balance_snapshots` — partitioned by `snapshot_at`

Transfer DB:
- `transfers` — partitioned by `created_at`
- `transfer_events` — partitioned by `created_at`

**Partition management**:
- **6 months ahead**: Partitions are pre-created for the next 6 months at deployment time. This ensures partitions exist before data arrives, avoiding the need for runtime partition creation under load.
- **Default partition**: A `DEFAULT` partition catches any rows that fall outside the defined ranges (e.g., far-future timestamps due to bugs). This prevents INSERT failures due to missing partitions.
- **Naming convention**: `{table}_y{YYYY}m{MM}` (e.g., `transfers_y2026m03`, `entry_lines_y2026m04`)

**Migration format** (using goose):
```sql
-- Create parent table as partitioned
CREATE TABLE transfers (
    id UUID NOT NULL,
    tenant_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ...
) PARTITION BY RANGE (created_at);

-- Create partitions
CREATE TABLE transfers_y2026m01 PARTITION OF transfers
    FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
-- ... repeat for each month
```

**Partition creation** is handled by a migration that creates 6 months of future partitions, plus a scheduled job (or manual migration) to create additional partitions before they are needed.

## Consequences

### Benefits
- **Fast data retention via partition drops**: Removing 3-month-old data is `DROP TABLE transfers_y2025m12` — an instant metadata operation, not a multi-day DELETE + VACUUM. No dead tuples, no table bloat, no I/O storm.
- **Manageable index sizes**: Each monthly partition's indexes cover ~1.5B/12 = 125M rows (for transfers) instead of the full table. B-tree depth stays at 3-4 levels, keeping index scans fast.
- **Per-partition VACUUM**: Autovacuum runs on individual partitions (125M rows) rather than the full table (1.5B+ rows). Vacuum cycles are shorter, consume less I/O, and hold locks for less time.
- **Partition pruning**: Queries that include a `created_at` filter (which most analytical and time-bounded queries do) automatically skip irrelevant partitions. A query for "March 2026 transfers" only scans `transfers_y2026m03`, ignoring all other months.
- **Parallel operations**: Multiple partitions can be vacuumed, indexed, or backed up concurrently, improving maintenance throughput.

### Trade-offs
- **Partition management overhead**: New partitions must be created before data arrives. If the partition creation job fails and no partition exists for the current month, inserts fall into the DEFAULT partition (which works but defeats the purpose). Monitoring partition availability is required.
- **Partition pruning requires awareness**: Queries without a `created_at` predicate scan ALL partitions, which is slower than scanning a single unpartitioned table (due to per-partition overhead). Developers must include time bounds in queries to benefit from partitioning.
- **Cross-partition queries are slower for small result sets**: A query like "get transfer by ID" (without `created_at`) must check every partition's index. This is mitigated by including `created_at` in the primary key or adding a covering index, but it requires deliberate schema design.
- **Migration complexity**: Creating and managing partitions adds SQL to every migration that touches partitioned tables. Adding a column requires `ALTER TABLE` on the parent (which propagates), but adding an index requires creating it on each partition individually for online index creation.
- **ORM compatibility**: Some ORMs struggle with partitioned tables. We use SQLC (raw SQL + code generation) specifically to avoid this issue, but it means we cannot easily switch to an ORM later.

### Mitigations
- **SQLC generates partition-aware queries**: All queries are hand-written SQL that includes `created_at` filters where appropriate. SQLC generates type-safe Go code from these queries, ensuring partition pruning is exercised.
- **Default partition as safety net**: The `DEFAULT` partition catches any rows that do not match an existing monthly partition. Monitoring alerts on DEFAULT partition row count (should be near zero) detect missing partitions before they become a problem.
- **6-month lookahead**: Pre-creating 6 months of partitions provides ample buffer. Even if the partition creation job fails for several weeks, there are months of runway before data starts landing in the DEFAULT partition.
- **Partition creation in migrations**: Major deployments include a migration step that ensures partitions exist for the next 6 months. This ties partition management to the deployment cadence rather than relying solely on a background job.

## References

- [PostgreSQL Table Partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html) — PostgreSQL documentation
- [Partitioning Improvements in PostgreSQL 14-16](https://www.postgresql.org/docs/current/release.html) — partition pruning and join optimizations
