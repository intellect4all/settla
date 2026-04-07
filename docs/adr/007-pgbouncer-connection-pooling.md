# ADR-007: PgBouncer Connection Pooling

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla uses three separate Postgres databases (one per bounded context: Ledger, Transfer, Treasury). In production, the system runs 6+ `settla-server` replicas, 8+ `settla-node` instances, and 4+ gateway replicas — each maintaining its own database connection pool.

We calculated the connection demand:

| Component | Instances | Pool size per DB | Connections per DB |
|-----------|-----------|-----------------|-------------------|
| settla-server | 6 | 100 | 600 |
| settla-node | 8 | 25 | 200 |
| gateway | 4 | 20 | 80 |
| webhook | 2 | 10 | 20 |
| **Total** | | | **900** |

**The threshold: 900 connections per database far exceeds Postgres's practical `max_connections` limit of ~200.** Postgres forks a new backend process per connection, each consuming ~5–10MB of memory. At 900 connections, that is 4.5–9GB of memory just for connection overhead, plus severe contention on shared buffers, lock tables, and procarray. Postgres performance degrades non-linearly beyond ~200 connections: p99 query latency doubles at 300 connections and triples at 500.

Even with aggressive connection pool tuning (smaller pools, shorter idle timeouts), the math does not work: 6 server replicas × 30 connections = 180, leaving no headroom for workers, gateways, or migrations.

The standard solution is a connection pooler that multiplexes many application connections over a small number of Postgres connections.

## Decision

We deploy **PgBouncer in transaction pooling mode**, one instance per database, between all application components and Postgres.

### Topology
```
settla-server (×6)  ─┐
settla-node   (×8)  ─┤→ PgBouncer :6433 (pool=100) → Postgres Ledger   :5433
gateway       (×4)  ─┤→ PgBouncer :6434 (pool=100) → Postgres Transfer :5434
webhook       (×2)  ─┘→ PgBouncer :6435 (pool=100) → Postgres Treasury :5435
```

### Configuration
- **Pool mode**: `transaction` — connections are returned to the pool after each transaction completes
- **Default pool size**: 100 server-side connections per database (configurable)
- **Max client connections**: 2,000 per PgBouncer instance
- **Reserve pool**: 5 connections for admin/monitoring queries
- **Server idle timeout**: 600 seconds
- **Client idle timeout**: 0 (disabled — applications manage their own timeouts)
- **Image**: `edoburu/pgbouncer:1.21.0-p2`

### Connection Routing
- Application code connects to PgBouncer ports (6433, 6434, 6435) for all runtime queries
- Migrations connect directly to Postgres ports (5433, 5434, 5435) via `SETTLA_*_DB_MIGRATE_URL` — migrations use `SET` commands and advisory locks that require session-level state
- SQLC-generated code and Go `database/sql` pools are configured to connect to PgBouncer

### Environment Variables
```
SETTLA_LEDGER_DB_URL=postgres://settla:pass@pgbouncer-ledger:6433/settla_ledger
SETTLA_TRANSFER_DB_URL=postgres://settla:pass@pgbouncer-transfer:6434/settla_transfer
SETTLA_TREASURY_DB_URL=postgres://settla:pass@pgbouncer-treasury:6435/settla_treasury

SETTLA_LEDGER_DB_MIGRATE_URL=postgres://settla:pass@postgres-ledger:5433/settla_ledger
SETTLA_TRANSFER_DB_MIGRATE_URL=postgres://settla:pass@postgres-transfer:5434/settla_transfer
SETTLA_TREASURY_DB_MIGRATE_URL=postgres://settla:pass@postgres-treasury:5435/settla_treasury
```

## Consequences

### Benefits
- **Connection multiplexing**: 900+ application connections are multiplexed over 100 Postgres connections per database. Postgres sees only 100 backends instead of 900, reducing memory usage by ~80% and eliminating connection-related performance degradation.
- **Connection surge protection**: during deployments (rolling restart of 6 replicas) or traffic spikes, PgBouncer queues client requests rather than overwhelming Postgres with connection storms. Postgres never sees more than `pool_size` connections.
- **Transparent to application code**: PgBouncer speaks the Postgres wire protocol. SQLC-generated code, Go `database/sql`, and Node.js `pg` libraries connect to PgBouncer identically to connecting to Postgres directly.
- **Per-database isolation**: three separate PgBouncer instances ensure that a connection pool exhaustion in the Ledger DB does not affect Transfer DB or Treasury DB queries. Each bounded context has independent connection capacity.

### Trade-offs
- **No prepared statements in transaction mode**: PgBouncer in `transaction` mode cannot support server-side prepared statements because the prepared statement is bound to a Postgres backend connection, which may be different on the next transaction. All queries must use the simple or extended query protocol without `PREPARE`.
- **Extra network hop**: every query traverses an additional network hop (application → PgBouncer → Postgres). In Docker networking, this adds ~0.1ms per query. At 10,000 queries/sec, this is ~1 second of cumulative latency per second — acceptable given the connection management benefits.
- **Session-level features unavailable**: `SET` commands (e.g., `SET search_path`), `LISTEN/NOTIFY`, temporary tables, and advisory locks do not work reliably in transaction mode because the backend connection changes between transactions.
- **Additional operational component**: three PgBouncer instances must be monitored, configured, and maintained. PgBouncer failure takes down all database access for the affected bounded context.

### Mitigations
- **Query protocol compatibility**: SQLC generates queries using the simple query protocol by default, which is fully compatible with PgBouncer transaction mode. Go's `database/sql` uses the extended query protocol but PgBouncer 1.21+ handles this correctly with `prepared_statements` mode disabled.
- **Migrations bypass PgBouncer**: database migrations connect directly to Postgres (raw ports 5433/5434/5435) to use `SET`, advisory locks, and other session-level features required by goose (the migration tool).
- **Health checks**: Docker Compose health checks verify PgBouncer is accepting connections. Application startup waits for PgBouncer health before serving traffic.
- **PgBouncer monitoring**: `SHOW STATS`, `SHOW POOLS`, and `SHOW CLIENTS` admin commands expose connection utilization, wait times, and query rates. These metrics are scraped by Prometheus for dashboarding and alerting.
- **Failover**: PgBouncer is stateless — it can be restarted instantly without data loss. Docker restart policy ensures automatic recovery.

## References

- [PgBouncer Documentation](https://www.pgbouncer.org/)
- [Postgres Connection Limits](https://www.postgresql.org/docs/current/runtime-config-connection.html)
- [Why Connection Pooling Matters](https://brandur.org/postgres-connections) — Brandur Leach
- ADR-006 (Two-Level Cache) — caching reduces the query volume that reaches PgBouncer
