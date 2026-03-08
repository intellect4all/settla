# ADR-008: Multi-Database Bounded Contexts

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla processes 50M transactions/day. Each transaction generates ledger entries, transfer records, events, and treasury position updates. At this volume:

- **Cross-context JOINs become a bottleneck.** A single Postgres instance handling ledger entries (200-250M entry_lines/day), transfer records (50M/day), and treasury snapshots simultaneously creates lock contention across unrelated workloads. A `SELECT ... JOIN` between transfers and ledger entries under load causes row-level lock waits that cascade into query timeouts.
- **Schema migrations in one context lock tables in others.** Adding an index to the `entry_lines` table (1.5B rows/month) takes hours with `CREATE INDEX CONCURRENTLY`. During that time, if the transfer and treasury tables share the same database, autovacuum contention and I/O saturation degrade all three workloads. At 50M rows/day, even a brief migration-induced slowdown causes visible latency spikes.
- **Connection pool exhaustion.** With 6+ settla-server replicas, each needing connections for three distinct workload patterns (high-write ledger, mixed transfer, bursty treasury), a single connection pool cannot be tuned for all three without compromise.

We needed to decide between:

1. **Single database with schema separation** — one Postgres instance, separate schemas per context
2. **Separate databases per bounded context** — independent Postgres instances with isolated connection pools
3. **Single database, accept the coupling** — optimize later if needed

## Decision

We chose **3 separate PostgreSQL databases**, one per bounded context:

| Database | PgBouncer Port | Raw Port | Workload Profile |
|----------|---------------|----------|-----------------|
| Ledger DB | 6433 | 5433 | High-write CQRS read model (200-250M entry_lines/day from TigerBeetle sync) |
| Transfer DB | 6434 | 5434 | Mixed read/write (50M transfers + 50M events/day, plus tenant/API key lookups) |
| Treasury DB | 6435 | 5435 | Bursty write (~50 hot positions, 100ms flush interval from in-memory reservations) |

Each bounded context owns:
- Its own database and connection pool (via PgBouncer)
- Its own migrations in `db/migrations/{ledger,transfer,treasury}/`
- Its own SQLC query definitions in `db/queries/{ledger,transfer,treasury}/`
- Its own generated Go repository code in `store/{ledgerdb,transferdb,treasurydb}/`

No module may query another context's database directly. Cross-context data aggregation happens through:
- Event-driven sync via NATS JetStream (e.g., TigerBeetle writes propagated to Ledger DB read model)
- Application-level joins in the gateway or core engine (fetch from each store independently, combine in memory)

## Consequences

### Benefits
- **Independent scaling**: Ledger DB can be vertically scaled or replicated independently when write throughput demands it, without affecting Transfer or Treasury workloads.
- **Isolated maintenance windows**: Schema migrations, vacuum operations, and index rebuilds on one database have zero impact on the other two.
- **Tuned connection pools**: Each PgBouncer instance is configured for its workload — Ledger DB optimized for high-throughput bulk inserts, Transfer DB for mixed OLTP, Treasury DB for bursty small writes.
- **Failure isolation**: A runaway query or connection leak in one context cannot exhaust connections or I/O capacity for the others.
- **Clear ownership**: Each bounded context's data model is fully self-contained, making it easier to reason about, test, and eventually extract into a separate service.

### Trade-offs
- **No cross-database JOINs**: Queries like "show me all ledger entries for a transfer" require two separate database calls and application-level assembly. This adds latency (~1-2ms per additional round-trip) and code complexity.
- **Separate connection pools**: 6 replicas x 3 databases = 18 PgBouncer connections to manage, monitor, and configure. More operational surface area.
- **Distributed consistency**: There is no cross-database transaction. If a transfer record is written to Transfer DB but the corresponding ledger sync to Ledger DB fails, the system must handle this inconsistency. This is acceptable because TigerBeetle is the write authority for balances, and the Ledger DB is an eventually-consistent read model.
- **Migration coordination**: Schema changes that span contexts (rare, but possible during major refactors) require coordinated deployments across multiple migration sets.

### Mitigations
- **SQLC generates type-safe queries per context**: Each database has its own SQLC configuration, generating strongly-typed Go code. Developers cannot accidentally query the wrong database — the types won't match.
- **Event-driven sync where needed**: The TigerBeetle-to-Postgres sync consumer populates the Ledger DB read model from events, keeping the CQRS pattern clean. Any future cross-context sync follows the same pattern.
- **PgBouncer connection pooling**: Each database gets its own PgBouncer instance, configured for its specific workload pattern. This keeps per-database connection counts manageable despite multiple replicas.
- **Monitoring per database**: Each database has independent metrics (connections, query latency, replication lag), making it straightforward to identify which context is under pressure.

## References

- [ADR-001: Modular Monolith Architecture](001-modular-monolith.md) — establishes the bounded context boundaries that this ADR enforces at the data layer
- [Database per Service pattern](https://microservices.io/patterns/data/database-per-service.html) — Chris Richardson
