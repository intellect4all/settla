# ADR-004: In-Memory Treasury Reservation

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla's treasury manages ~50 hot positions (per-tenant, per-currency liquidity pools) that are updated on every transfer. At 580 TPS sustained (5,000 TPS peak), each transfer requires a reserve-then-release cycle on the relevant treasury position to ensure sufficient liquidity before settlement.

We evaluated database-backed reservation approaches:

| Approach | Throughput | P99 latency | Failure mode |
|----------|-----------|-------------|--------------|
| `SELECT FOR UPDATE` per transfer | ~200 TPS | 15ms | Row-level lock contention, deadlocks at >100 concurrent |
| Advisory locks | ~500 TPS | 8ms | Lock table exhaustion under peak |
| Optimistic locking (version column) | ~800 TPS | 5ms | High retry rate (>30%) under contention |
| Serializable isolation | ~150 TPS | 25ms | Massive serialization failures at peak |

**The threshold: thousands of concurrent `SELECT FOR UPDATE` on the same ~50 rows creates catastrophic hot-key contention.** At 5,000 TPS peak, with ~50 positions, each position sees ~100 concurrent lock attempts. Postgres row-level locking degrades non-linearly: at 100 concurrent waiters on a single row, p99 latency exceeds 50ms and deadlock rate exceeds 5%. The database becomes the bottleneck for the entire settlement pipeline.

The fundamental problem is that database round-trips (even ~1ms each) serialized on hot keys cannot sustain the required throughput. The reservation operation itself is simple arithmetic (subtract amount from available balance), making it an ideal candidate for in-memory computation.

## Decision

We implement **in-memory atomic treasury reservation** using `sync.Mutex` per position, with a background goroutine that flushes dirty positions to Postgres every 100ms.

### In-Memory Path (Reserve / Release)
```go
type Manager struct {
    mu        sync.RWMutex
    positions map[string]*Position  // key: "{tenant_id}:{currency}"
}

type Position struct {
    mu        sync.Mutex
    Available decimal.Decimal
    Reserved  decimal.Decimal
    dirty     bool
}
```

- `Reserve(tenantID, currency, amount)` — acquires position mutex, checks `Available >= amount`, decrements Available, increments Reserved, marks dirty. Returns in <1μs.
- `Release(tenantID, currency, amount)` — acquires position mutex, decrements Reserved, increments Available, marks dirty. Returns in <1μs.
- No database call on the hot path. No network round-trip. No serialization.

### Background Flush (100ms interval)
- A goroutine scans all positions every 100ms
- Dirty positions are batched into a single `UPDATE ... WHERE` statement
- Flush resets the dirty flag on success
- Flush errors are logged and retried on the next cycle

### Startup Recovery
- On startup, `Manager.Load()` reads all positions from Postgres (`treasury_positions` table)
- Positions are populated into the in-memory map before the server accepts traffic
- Any reservations that were in-flight during a crash are lost (100ms window maximum)

## Consequences

### Benefits
- **Sub-microsecond reservation**: measured at <1μs for Reserve/Release, compared to 5–25ms for database-backed approaches. This removes treasury from the critical path latency budget entirely.
- **Eliminates hot-key contention**: no database locks, no deadlocks, no retry loops. Per-position mutex contention is negligible because lock hold time is ~100ns (just arithmetic).
- **Linear scalability with positions**: adding more tenants/currencies adds more positions, each with its own mutex. No shared lock contention.
- **Simplified error handling on hot path**: Reserve either succeeds or fails with "insufficient balance" — no transient database errors, no connection pool exhaustion, no timeout handling.

### Trade-offs
- **Crash risk window**: if the process crashes, up to 100ms of reservation state changes are lost. A transfer that was reserved but not yet flushed will have its reservation "leaked" — the amount is deducted from Available but the flush never persisted.
- **Single-process constraint**: the in-memory positions exist in one process. Running multiple `settla-server` replicas requires either sticky routing (all requests for a tenant go to the same replica) or a distributed coordination layer.
- **Memory footprint**: all positions must fit in memory. At ~50 positions × ~200 bytes each, this is ~10KB — negligible. Even at 10,000 positions, it would be ~2MB.

### Mitigations
- **Startup recovery**: on restart, positions are reloaded from the last flushed state in Postgres. The maximum data loss is 100ms of reservation changes. Since transfers are also tracked in the Transfer DB with their own state machine, the system can detect and reconcile any orphaned reservations.
- **Graceful shutdown**: the server's shutdown sequence calls `Manager.Flush()` synchronously before exiting, ensuring all dirty positions are persisted. Crash-only failures have the 100ms window; clean shutdowns have zero loss.
- **Reconciliation job**: a periodic background job compares in-memory positions against ledger balances (from TigerBeetle) and flags discrepancies for manual review.
- **Flush monitoring**: the flush goroutine emits `settla.treasury.flush_duration_ms` and `settla.treasury.flush_errors_total` metrics. Alerts fire if flush consistently fails.

## References

- [LMAX Disruptor](https://lmax-exchange.github.io/disruptor/) — same principle: keep hot data in memory, batch writes
- [Martin Thompson on Mechanical Sympathy](https://mechanical-sympathy.blogspot.com/) — memory access patterns for low-latency systems
- ADR-001 (Modular Monolith) — treasury is an extraction candidate if multi-process coordination becomes necessary
