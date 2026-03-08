# ADR-002: TigerBeetle for Ledger Writes

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla's ledger must sustain 15,000–25,000 writes/second at peak to support 50M transactions/day. Each transfer creates 4–5 entry lines (debit source, credit destination, fee entries), yielding 200–250M entry_lines/day.

We benchmarked single-instance Postgres with various optimizations:

| Approach | Sustained writes/sec | Notes |
|----------|---------------------|-------|
| Individual INSERTs | ~2,000 | Connection pool saturated |
| Batch INSERTs (50 rows) | ~8,000 | Acceptable for reads, not writes |
| COPY protocol | ~12,000 | Close, but no headroom for peaks |
| Partitioned + COPY | ~15,000 | Barely meets sustained, fails at peak |

**The threshold: at >10,000 writes/sec, single Postgres becomes the bottleneck.** Even with aggressive batching, Postgres hits WAL contention, fsync pressure, and MVCC bloat under sustained ledger write load. At 25,000 writes/sec peak, no single Postgres tuning configuration could maintain sub-millisecond p99 latency while preserving ACID guarantees on balance mutations.

TigerBeetle is purpose-built for double-entry accounting: it enforces balanced postings at the engine level, handles 1M+ TPS on a single node, and provides deterministic latency through io_uring and a custom storage engine.

## Decision

We use **TigerBeetle as the write authority** for all ledger mutations (account creation, balance transfers, posting entries). Postgres serves as the read model for queries, reporting, and analytics.

The architecture:

```
Write path:  Gateway → gRPC → Core Engine → Ledger.PostEntry() → TigerBeetle
Read path:   Gateway → gRPC → Core Engine → Ledger.GetBalance() → Postgres
Sync path:   TB → Sync Consumer → Postgres (journal_entries + entry_lines tables)
```

Key implementation details:
- `ledger.Service` implements `domain.LedgerService` with dual backends
- Write methods (`PostEntry`, `CreateAccount`) go to TigerBeetle
- Read methods (`GetBalance`, `GetEntries`, `GetAccountHistory`) go to Postgres
- TB account IDs are deterministically derived from UUIDs (128-bit mapping)
- TB transfer IDs are the entry UUIDs, ensuring idempotency at the storage layer
- Balanced postings are enforced by TigerBeetle's `linked` flag for multi-leg transfers

## Consequences

### Benefits
- **Headroom**: TigerBeetle handles 1M+ TPS on a single node — two orders of magnitude above our 25K peak requirement. This means no write-path scaling concerns for the foreseeable future.
- **Correctness by construction**: TigerBeetle enforces double-entry invariants (sum of debits = sum of credits) at the storage engine level. A bug in our application code cannot create an unbalanced entry.
- **Deterministic latency**: io_uring-based I/O with no garbage collection pauses. P99 write latency is <1ms even under sustained load.
- **Idempotency built-in**: TigerBeetle deduplicates by transfer ID natively, so replayed writes are safe without application-level dedup logic on the write path.

### Trade-offs
- **Operational complexity**: TigerBeetle is a newer system with a smaller ecosystem than Postgres. Fewer monitoring tools, fewer DBAs who know it, fewer Stack Overflow answers.
- **Eventual consistency on reads**: Postgres read model lags behind TigerBeetle writes by the sync consumer's processing delay (typically <100ms, worst case ~1s under load). Queries for "current balance" may be slightly stale.
- **Limited query capability**: TigerBeetle has no SQL, no ad-hoc queries, no JOINs. All reporting, filtering, and aggregation must happen against the Postgres read model.
- **Privileged container requirement**: TigerBeetle requires `privileged: true` on macOS Docker (io_uring emulation), which complicates local development security posture.

### Mitigations
- **Sync consumer with monitoring**: the TB→PG sync consumer tracks lag metrics and alerts if sync falls behind >5 seconds. In practice, sync stays under 100ms.
- **Read-after-write consistency where needed**: for critical paths (e.g., balance check before transfer), the engine reads directly from TigerBeetle, not Postgres.
- **Postgres as fallback**: if TigerBeetle is unavailable, the system fails writes (correct behavior — we never allow writes that bypass the source of truth). Reads continue from Postgres.
- **Runbook documentation**: operational procedures for TigerBeetle backup, recovery, and cluster management are documented separately.

## References

- [TigerBeetle Design Document](https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/DESIGN.md)
- [LMAX Architecture](https://martinfowler.com/articles/lmax.html) — similar single-writer pattern
- ADR-003 (CQRS Dual-Backend Ledger) — the read-side architecture that complements this decision
