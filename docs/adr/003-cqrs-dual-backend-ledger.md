# ADR-003: CQRS Dual-Backend Ledger

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

With TigerBeetle as the ledger write authority (ADR-002), we needed a strategy for the read path. TigerBeetle provides no SQL interface, no ad-hoc queries, no JOINs, and no aggregation functions. Yet the business requires:

- Transaction history queries filtered by date range, currency, corridor, status
- Balance snapshots for treasury reconciliation
- Aggregated reports (daily volumes, fee totals, corridor breakdowns)
- Audit trails with full entry line detail
- Dashboard queries for real-time operational visibility

**The threshold: 200–250M entry_lines/day requires a dedicated read path.** At this volume, even if TigerBeetle supported queries, mixing read and write workloads on the same engine would risk write latency degradation. The CQRS pattern — separating the write model (optimized for throughput) from the read model (optimized for queries) — is the standard solution when read and write workloads have fundamentally different performance characteristics.

Additionally, the read path must support complex queries that TigerBeetle's API cannot express: "show me all GBP→USDT transfers for tenant Lemfi in the last 7 days, grouped by corridor, with fee totals."

## Decision

We implement **CQRS with a dual-backend ledger**: TigerBeetle owns the write path, Postgres owns the read path, and a sync consumer bridges them.

### Write Model (TigerBeetle)
- Source of truth for all balances and ledger entries
- All mutations go through `ledger.Service.PostEntry()` → TigerBeetle
- Enforces double-entry invariants at the storage level

### Read Model (Postgres — Ledger DB on port 6433 via PgBouncer)
- `journal_entries` table: entry headers (ID, tenant_id, type, timestamp, idempotency_key)
- `entry_lines` table: individual debit/credit lines (account_code, amount, currency)
- `balance_snapshots` table: periodic balance materialization for fast lookups
- Monthly partitioned tables (6 months ahead + default partition)
- SQLC-generated query code in `store/ledgerdb/`

### Sync Consumer
- Reads committed entries from TigerBeetle
- Transforms TB account/transfer records into `journal_entries` + `entry_lines` rows
- Writes to Postgres in batches (bulk INSERT for throughput)
- Tracks sync position to enable restart without reprocessing
- Publishes sync lag metrics for monitoring

### Consistency Model
- **TigerBeetle is always authoritative** for current balances
- Postgres read model is **eventually consistent**, typically <100ms behind
- For balance-critical operations (pre-transfer balance check, treasury reservation), the engine queries TigerBeetle directly
- For display, reporting, and history queries, the engine queries Postgres

## Consequences

### Benefits
- **Optimized for each workload**: writes hit a storage engine designed for 1M+ TPS accounting operations; reads hit a query engine designed for complex SQL with indexes, JOINs, and aggregations.
- **Independent scaling**: read replicas can scale the Postgres query layer without affecting write throughput. Write throughput is bounded only by TigerBeetle capacity.
- **Rich query support**: full SQL capability for reporting, dashboards, audit trails, and ad-hoc analysis — none of which TigerBeetle can provide.
- **Familiar tooling on read path**: Postgres is well-understood by every engineer, has mature monitoring (pg_stat_statements, pganalyze), and integrates with existing BI tools.

### Trade-offs
- **Eventual consistency**: the read model lags behind writes. A transfer that just completed may not appear in query results for up to ~100ms (typical) or ~1s (under heavy load). This can confuse users who expect immediate consistency on read-after-write.
- **Two data stores to operate**: TigerBeetle and Postgres have different backup procedures, different failure modes, different monitoring requirements. Operational burden is roughly doubled compared to a single-database architecture.
- **Sync consumer is a critical path**: if the sync consumer fails or falls behind, the read model becomes increasingly stale. This is a single point of failure for the read path.
- **Schema evolution complexity**: changes to the ledger data model must be coordinated across TigerBeetle's account/transfer schema and Postgres's table schema.

### Mitigations
- **Sync lag monitoring and alerting**: the sync consumer emits a `settla.ledger.sync_lag_ms` metric. Alerts fire if lag exceeds 5 seconds. PagerDuty escalation if lag exceeds 30 seconds.
- **Direct TB reads for critical paths**: `GetBalance()` reads from TigerBeetle when called from the settlement engine, ensuring balance checks are always against the source of truth.
- **Idempotent sync**: the sync consumer can be restarted safely — it tracks its position and replays are deduplicated by the `UNIQUE(entry_id)` constraint on Postgres tables.
- **Automated consistency checks**: a background job periodically compares TB balances against PG balance_snapshots and alerts on divergence.

## References

- [CQRS Pattern](https://martinfowler.com/bliki/CQRS.html) — Martin Fowler
- [Event Sourcing & CQRS](https://www.eventstore.com/cqrs-pattern) — Event Store documentation
- ADR-002 (TigerBeetle for Ledger Writes) — the write-side decision this complements
