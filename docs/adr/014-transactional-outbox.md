# ADR-014: Transactional Outbox for Consistency

**Status:** Accepted
**Date:** 2026-03-09
**Authors:** Engineering Team

## Context

Settla's settlement engine must update transfer status in the database AND trigger async workers via NATS atomically. At 50M transactions/day (~580 TPS sustained, 5,000 TPS peak), the reliability of this coupling is critical.

The naive approach — commit the database transaction, then publish to NATS — is a classic dual-write problem:

```
1. BEGIN transaction
2. UPDATE transfer SET status = 'reserved'
3. COMMIT                          ← succeeds
4. NATS publish('transfer.reserved') ← fails (network blip, NATS restart)
```

The transfer is now in `reserved` status but no worker will ever process it. The transfer is stuck. At 50M transactions/day, even a 0.01% failure rate means **5,000 orphaned transfers per day**. These are real payments that silently stop progressing — a catastrophic failure mode for a settlement platform.

We evaluated three approaches to ensure atomicity:

| Approach | Latency | Complexity | Failure mode |
|----------|---------|-----------|--------------|
| Publish-after-commit (naive) | ~0ms | Low | Dual-write: lost events on NATS failure |
| Distributed transaction (2PC) | ~10-50ms | Very High | Coordinator failure blocks all writes |
| Transactional outbox | ~50ms (relay poll) | Medium | Relay lag (bounded, recoverable) |

The distributed transaction approach (two-phase commit across Postgres and NATS) is fragile, slow, and operationally complex. It also couples the database write path to NATS availability — a NATS outage would block all transfer state changes.

## Decision

We chose the **transactional outbox pattern**: the engine writes both the transfer state change and the corresponding outbox entries in a single Postgres transaction. A separate outbox relay process polls the outbox table and publishes to NATS.

### Write Path

```
Engine.Handle*(ctx, transferID, ...) {
    BEGIN transaction
        UPDATE transfers SET status = 'reserved', ...
        INSERT INTO outbox_entries (id, aggregate_id, event_type, payload, partition_key, created_at)
            VALUES (..., transfer_id, 'transfer.reserved', {...}, tenant_id, NOW())
    COMMIT
}
```

The outbox entry and the transfer state change are in the same transaction. If the transaction commits, both exist. If it rolls back, neither exists. There is no window where the database is inconsistent.

### Outbox Table Schema

```sql
CREATE TABLE outbox (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    aggregate_type  TEXT NOT NULL,
    aggregate_id    UUID NOT NULL,
    tenant_id       UUID NOT NULL,       -- used for NATS partition routing
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    is_intent       BOOLEAN NOT NULL DEFAULT false,
    published       BOOLEAN NOT NULL DEFAULT false,
    published_at    TIMESTAMPTZ,
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 5,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);   -- daily partitions for fast cleanup
```

> **Implementation note**: the table was named `outbox` (not `outbox_entries`) to avoid a misleading name. The schema is richer than the original design: `aggregate_type`, `is_intent`, `retry_count`, and `max_retries` were added to support intent vs event distinction and per-entry retry tracking.

### Outbox Relay

The relay runs in `settla-node` (`node/outbox/relay.go`), not `settla-server`. This keeps the relay co-located with the workers it feeds and separates the stateless settlement engine from the I/O-heavy relay process.

The relay:

1. Polls for unpublished entries: `SELECT * FROM outbox WHERE published = false ORDER BY created_at LIMIT 100`
2. Publishes each entry to NATS with the appropriate subject (routing via `node/messaging.SubjectForEventType`)
3. Marks entries as published: `UPDATE outbox SET published = true, published_at = NOW() WHERE id = $1 AND created_at = $2`
4. Uses NATS message deduplication (Nats-Msg-Id header set to outbox entry UUID) to prevent duplicate delivery on relay restarts

**Polling interval:** 50ms. This adds at most 50ms latency between a state change and the corresponding NATS event — well within the acceptable range for async settlement processing where typical end-to-end times are 2-30 seconds.

### Outbox Cleanup

The outbox table is partitioned by day (not month, because outbox rows are ephemeral). At 50M transfers/day generating ~2-3 outbox entries each, the table accumulates **100-150M rows per day**. Published entries older than 48 hours are cleaned up by dropping the daily partition:

```sql
DROP TABLE outbox_entries_y2026m03d07;  -- instant, O(1)
```

This is an instant metadata operation regardless of row count — no DELETE, no VACUUM, no table bloat. See ADR-018 for the full rationale on partition DROP vs DELETE.

## Consequences

### Benefits
- **Zero dual-write bugs**: transfer state and outbox entries are atomically consistent. There is no window where a state change exists without a corresponding event, or vice versa.
- **NATS independence for writes**: the engine's write path does not depend on NATS availability. If NATS is down, outbox entries accumulate in Postgres and are published when NATS recovers. Transfer state changes continue uninterrupted.
- **Exactly-once NATS delivery**: NATS message deduplication (using the outbox entry UUID as Nats-Msg-Id) prevents duplicate events when the relay retries after a crash or restart.
- **Auditability**: the outbox table is a complete, time-ordered log of all events the engine has produced. This is invaluable for debugging, replay, and compliance.
- **Natural batching**: the relay polls in batches of 100, which amortizes the Postgres query cost and NATS publish overhead across multiple events.

### Trade-offs
- **Added relay latency**: the 50ms polling interval adds up to 50ms latency between a state change and its corresponding NATS event. This is acceptable for settlement processing (seconds-to-minutes end-to-end) but would not be suitable for sub-millisecond event delivery.
- **Outbox table write amplification**: every transfer state change now writes an additional 1-3 rows to the outbox table. At 50M transfers/day, this is 100-150M additional rows. The daily partitioning and DROP cleanup mitigates the storage impact.
- **Relay is a single point of processing**: if the relay goroutine dies or the `settla-node` instance crashes, outbox entries stop being published. Multiple `settla-node` instances can run the relay; NATS JetStream deduplication (using entry UUID as Nats-Msg-Id) prevents double-publishing when multiple instances poll concurrently.
- **Postgres becomes the event buffer**: during NATS outages, unpublished outbox entries accumulate. At 580 TPS × 3 entries/tx = 1,740 rows/sec, a 1-hour NATS outage accumulates ~6.3M rows. Postgres can handle this, but monitoring is required.

### Mitigations
- **Relay deduplication**: multiple `settla-node` instances may run the relay concurrently. NATS JetStream's built-in message deduplication (Nats-Msg-Id = outbox entry UUID, 2-minute dedup window) prevents double-publishing. Postgres `UPDATE ... WHERE published = false` uses optimistic locking.
- **Relay health monitoring**: Prometheus metrics exposed: `settla_outbox_relay_latency_seconds`, `settla_outbox_unpublished_gauge`, `settla_outbox_poll_batch_size`, `settla_outbox_published_total`, `settla_outbox_failed_total`. Alert when unpublished gauge exceeds 10,000 (warn) or 100,000 (critical).
- **Outbox lag monitoring**: `settla_outbox_unpublished_gauge` tracks entries in the last poll batch. Alert thresholds: warn at 10,000, critical at 100,000.

## Threshold Triggers for Revisiting

- **Relay latency consistently >200ms**: indicates Postgres cannot keep up with outbox polling under load. Migration path: Change Data Capture (Debezium on the outbox table's WAL) for sub-10ms relay latency.
- **Outbox write throughput exceeds Postgres capacity**: if the additional 100-150M rows/day causes measurable write latency degradation on the Transfer DB. Migration path: dedicated outbox database or CDC-based approach.
- **Team needs sub-10ms event latency**: for real-time use cases (e.g., live dashboard updates). Migration path: CDC with Debezium or Postgres logical replication slot.

## References

- [Transactional Outbox Pattern](https://microservices.io/patterns/data/transactional-outbox.html) — Chris Richardson
- [Reliable Messaging Without Distributed Transactions](https://www.youtube.com/watch?v=7sNE0yVaO7Q) — Udi Dahan
- ADR-005 (NATS Partitioned Events) — the downstream event system the outbox publishes to
- ADR-018 (Partition DROP vs DELETE) — cleanup strategy for the outbox table
