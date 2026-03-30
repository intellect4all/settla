# Failure Mode Matrix

> Last updated: 2026-03-29
>
> This document catalogs every external dependency failure mode, the system's expected behavior, detection mechanisms, recovery characteristics, and data safety guarantees.

## How to Read This Matrix

- **Severity**: P0 (revenue loss / data corruption), P1 (degraded service), P2 (minor impact)
- **Detection**: How fast the system identifies the failure
- **Recovery**: Automatic (A) or Manual (M), with estimated time
- **Data Safe**: Whether financial data integrity is preserved (no lost transfers, no double-credits)
- **Runbook**: Reference to existing operational runbook in `deploy/runbooks/`

---

## 1. PostgreSQL (Transfer DB)

The Transfer DB is the most critical dependency — it holds transfer state, outbox entries, tenant data, and API keys.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | API returns 503 for all mutations. Reads from cache (L1/L2) continue briefly for auth. No new transfers created. Outbox relay polls fail with backoff (2x, max 5s). | P0 | Health check: 5s interval, 5 retries = ~25s. Gateway `/ready` fails within 2s (gRPC pool unhealthy). | A: PgBouncer reconnects automatically. If Postgres restarted, recovery < 30s. If failover needed: M, ~2-5 min. See `database-failover.md`. | Yes — no partial writes possible (atomic transactions). | `database-failover.md` |
| **Slow queries (10x latency)** | Transfer creation latency spikes. Outbox relay batch size effectively shrinks. Gateway gRPC deadline (5s) starts timing out. Load shedder (AIMD, 50ms target) begins rejecting requests. | P1 | `settla_gateway_request_duration` p99 > 500ms alert fires. PgBouncer `server_idle_timeout` (300s) prevents connection leak. | A: Load shedder limits blast radius. Autovacuum or query plan regression requires M investigation. | Yes | `high-latency.md` |
| **Slow queries (100x latency)** | All gRPC calls timeout (5s deadline). Gateway returns 504. Outbox relay falls into max backoff (5s). Treasury flush fails (consecutive failure counter rises). | P0 | `SettlaLatencySLOBreach` fires (p99 > 500ms). `SettlaHighErrorRateFastBurn` fires within 5 min. | M: Requires root cause analysis (lock contention, missing index, disk I/O). See `disk-io-saturation.md`. | Yes — timeouts prevent partial commits. | `high-latency.md`, `disk-io-saturation.md` |
| **Error responses (connection refused after PgBouncer up)** | PgBouncer returns errors to application. pgx pool retries with backoff. New connections fail; existing connections in pool may be stale. | P0 | PgBouncer health check fails. `settla_pgx_pool_*` metrics show saturation. | A: pgx pool replaces dead connections. If PgBouncer config issue: M. | Yes | `pgbouncer-saturation.md` |
| **Network partition (reachable from server, not from node)** | Server creates transfers and outbox entries. Node cannot poll outbox (relay fails). Outbox backlog grows. No side effects execute (no provider calls, no ledger posts). | P1 | `settla_outbox_relay_lag` rises. `SettlaOutboxBacklogHigh` alert (> 100K entries). | A: Partition heals, relay drains backlog. NATS dedup (5-min window) prevents duplicates. | Yes — outbox pattern guarantees no data loss. | `outbox-relay-lag.md` |
| **Connection pool exhaustion** | PgBouncer `max_client_conn=2000` reached. New connections rejected with "too many clients." Existing in-flight queries complete. | P1 | `settla_pgx_pool_*` metrics. PgBouncer `SHOW POOLS` shows waiting > 0. | A: Connections release after transaction completes (~ms). If leaked: M, kill long-running queries. | Yes — rejected connections return errors, no partial state. | `pgbouncer-saturation.md` |
| **Disk full** | Postgres enters read-only mode. All writes fail. Transfers cannot be created. Outbox entries cannot be written. | P0 | Disk usage alert. Postgres logs `PANIC: could not write to file`. | M: Free disk space, restart if needed. Partition manager drops old partitions instantly (DROP TABLE). | Yes — read-only mode prevents corruption. | `high-disk-usage.md` |
| **Corrupt/unexpected data** | SQLC generated code returns errors on unexpected column types. Application panics on nil pointer if not checked. | P0 | Error rate spike. Application panic logs. | M: Restore from backup, investigate root cause. | Depends — see `data-loss-recovery.md`. | `data-loss-recovery.md` |

---

## 2. PostgreSQL (Ledger DB)

The Ledger DB is the CQRS read model. TigerBeetle is the write authority for balances.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Ledger read queries fail. Balance lookups return errors. Write path (TigerBeetle) unaffected. Server degrades to stub if Ledger DB unavailable at startup. | P1 | Health check on ledger PgBouncer (5s interval). `SettlaHealthCheckFailing{dependency="postgres_ledger"}`. | A: PgBouncer reconnects. Stub mode preserves write path. | Yes — TB is write authority; PG is read replica. | `database-failover.md` |
| **Slow queries (10x)** | Balance lookups slow. Dashboard/portal degraded. Transfer processing unaffected (uses TB). | P2 | `settla_ledger_pg_sync_lag_seconds` rises. Latency metrics on ledger queries. | A: Query performance returns when load drops. | Yes | `ledger-sync-lag-critical.md` |
| **TB-to-PG sync lag** | Read model falls behind write model. Balances shown in API/portal are stale. No impact on correctness. | P2 | `settla_ledger_pg_sync_lag_seconds > 120` triggers `SettlaLedgerSyncLagCritical`. | A: Sync consumer catches up when bottleneck clears. M if consumer is down. | Yes — eventual consistency by design. | `ledger-sync-lag-critical.md` |

---

## 3. PostgreSQL (Treasury DB)

Treasury DB stores position snapshots. The in-memory state is authoritative.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Treasury flush fails (100ms loop). `TreasuryConsecutiveFlushFailures` counter rises. In-memory positions remain authoritative. Reservations continue working. Server degrades to stub at startup if unavailable. | P1 | Flush failure counter > 5 logged as error. `settla_treasury_flush_lag` rises. `treasury-flush-lag` alert. | A: Flush resumes when DB returns. Dirty positions flushed. | Yes — in-memory state is authoritative. Risk: process crash + DB down = lost unflushed reservations. | `treasury-flush-lag.md` |
| **Slow queries** | Flush takes longer than 100ms interval. Flush lag increases. Positions still accurate in-memory. | P2 | `settla_treasury_flush_duration` histogram. Consecutive failure tracking. | A: Flush catches up when DB performance improves. | Yes | `treasury-flush-lag.md` |

---

## 4. TigerBeetle

TigerBeetle is the ledger write authority (1M+ TPS).

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Ledger posts fail. LedgerWorker returns errors, NATS redelivers (backoff: 1s→5s→30s→2m→10m). Transfers stall in FUNDED/ON_RAMPING states. No balance mutations possible. | P0 | `SettlaHealthCheckFailing{dependency="tigerbeetle"}`. LedgerWorker error rate spike. Stuck transfer gauge rises. | A: TB restart, workers resume, NATS redelivers queued intents. Recovery < 30s for single-node restart. Cluster quorum loss: M, see dedicated runbook. | Yes — TB WAL guarantees durability. No partial commits. | `tigerbeetle-recovery.md`, `tigerbeetle-cluster-quorum-loss.md` |
| **Slow responses (10x)** | Ledger posts take 10x longer. LedgerWorker throughput drops. Backpressure through NATS (MaxAckPending reached). Transfer completion time increases. | P1 | `settla_ledger_write_duration` histogram. NATS consumer lag. | A: Backpressure is self-regulating. TB performance returns when load drops. | Yes | `high-latency.md` |
| **Network partition** | LedgerWorker cannot reach TB. Same as complete outage from worker perspective. Server gRPC calls to ledger fail. | P0 | Same as complete outage detection. | A: Partition heals, connections re-established. | Yes | `tigerbeetle-recovery.md` |
| **Corrupt data** | TB detects corruption via checksums. Refuses to start or returns errors. | P0 | TB process crash/error logs. Health check failure. | M: Restore from snapshot. Reconcile with PG read model. See `data-loss-recovery.md` (Step 4). | Depends on snapshot age — some recent writes may be lost. | `data-loss-recovery.md` |

---

## 5. NATS JetStream

NATS is the async event bus between outbox relay and all workers.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Outbox relay publish fails. Relay enters exponential backoff (2x, max 5s). Outbox entries accumulate in Transfer DB. No worker processing occurs. API still accepts transfers (engine writes atomically to DB + outbox). | P1 | `settla_outbox_relay_lag` rises. `SettlaOutboxBacklogHigh` alert. NATS exporter goes unreachable. | A: NATS restarts, relay reconnects (`RetryOnFailedConnect=true`, `ReconnectWait=2s`, unlimited retries). Outbox drains (~200K entries/sec). NATS dedup (5-min window, Nats-Msg-Id) prevents duplicates. Recovery typically < 30s. | Yes — transactional outbox guarantees no data loss. | `nats-recovery.md` |
| **Slow consumers** | MaxAckPending (min 100, typically poolSize*2) reached. NATS stops delivering to slow consumer. Other consumers unaffected (per-stream isolation). Backlog queues in stream. | P1 | `settla_nats_partition_queue_depth > 1000`. `SettlaNATSPartitionSkew` alert (max/mean > 3x). | A: Consumer catches up when downstream dependency recovers. M: Scale workers if sustained. | Yes — messages are durable in JetStream (7-day retention). | `nats-consumer-lag.md` |
| **Disk full** | NATS `DiscardOld` policy kicks in. Oldest messages dropped from streams. Affected transfers may need recovery detector intervention. | P1 | NATS monitoring (:8222). Disk usage alerts. | M: Free disk, verify no dropped critical messages. Recovery detector catches stuck transfers (60s cycle). | Mostly — dropped messages may delay transfers but recovery detector prevents permanent stalling. | `high-disk-usage.md` |
| **Network partition (relay → NATS)** | Same as complete outage from relay perspective. Outbox accumulates. | P1 | Same as complete outage. | A: Partition heals, relay reconnects. | Yes | `nats-recovery.md` |
| **Network partition (NATS → workers)** | Workers disconnected. In-flight messages timeout (AckWait=30s), redeliver on reconnect. Outbox relay can still publish. Stream accumulates. | P1 | Worker error logs. NATS consumer lag metrics. | A: Workers reconnect, NATS redelivers. Idempotency keys prevent double-execution. | Yes | `nats-recovery.md` |

---

## 6. Redis

Redis is L2 cache, rate limiting, and idempotency store. It is NOT an authority for any data.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Auth degrades: L1 (in-process, 30s TTL) serves cached keys. Cache misses fall through to L3 (gRPC → DB). Rate limiting falls back to local-only counters. Idempotency checks fall through to DB UNIQUE constraint. Quote cache unavailable (quotes recalculated from scratch). Gateway `/ready` returns 503 (Redis PING fails). | P1 | Gateway `/ready` health check (2s timeout). `SettlaHealthCheckFailing{dependency="redis"}`. | A: Redis restarts, connections re-established. Sentinel failover (if configured) is automatic. Cache rebuilds organically (30s L1 TTL, 5min L2 TTL). | Yes — Redis is never write authority. All data reconstructable from DB. | N/A (no dedicated runbook needed) |
| **Slow responses (10x)** | Auth lookup latency increases from ~0.5ms to ~5ms. Still within gRPC deadline (5s). L1 cache absorbs most reads (~100ns). | P2 | Latency metrics on Redis operations. | A: Performance returns when Redis load drops. | Yes | `high-latency.md` |
| **Network partition** | Same as complete outage from application perspective. | P1 | Same as complete outage. | A: Sentinel failover or partition heal. | Yes | N/A |
| **Memory exhaustion** | Redis evicts keys per configured policy. Cache hit rate drops. More requests fall through to DB. | P2 | Redis `used_memory` metric. `info memory` command. | A: Eviction is self-regulating. M: Increase maxmemory or add shards. | Yes | `high-memory-usage.md` |

---

## 7. Blockchain RPC Endpoints

RPC endpoints connect chain monitors to EVM/Tron networks for deposit detection.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Chain monitor stops detecting new deposits. Deposit sessions remain in PENDING state. No existing deposits affected. BlockchainWorker circuit breaker trips (3 failures, 60s reset). | P1 | `settla_chain_monitor_block_lag` rises. `settla_circuit_breaker_state{name="blockchain_*"}` goes to 1 (open). | A: Circuit breaker half-open probes every 60s. On recovery, chain monitor resumes from last checkpoint (reorg-safe depth). No missed deposits — catches up from checkpoint block. | Yes — checkpoint + reorg-safe scanning prevents missed deposits. | `provider-circuit-breaker-open.md` |
| **Slow responses** | Chain monitor poll interval effectively increases. Block lag grows. Deposit detection delayed but not missed. | P2 | `settla_chain_monitor_poll_duration` histogram. Block lag metric. | A: Returns to normal when RPC recovers. | Yes | N/A |
| **Returning errors (500s)** | Chain monitor logs warnings, continues to next contract. Circuit breaker tracks failures. After threshold (3), trips open. | P1 | Circuit breaker state metric. Error rate in chain monitor logs. | A: Circuit breaker resets after 60s. | Yes | `provider-circuit-breaker-open.md` |
| **Returning stale/wrong block data** | Reorg safety: chain monitor re-scans `ReorgDepth` blocks each poll. Checkpoint verification via block hash chain detects reorgs. Deposits require confirmation threshold before processing. | P1 | Block hash mismatch detection in poller. Confirmation counter tracks depth. | A: Reorg detected, re-scan from safe checkpoint. | Yes — confirmation threshold prevents acting on reorged blocks. | N/A |

---

## 8. Payment Providers (On-ramp / Off-ramp)

External payment providers for fiat on/off ramps.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | Provider circuit breaker trips (15 failures, 10s reset). ProviderWorker attempts fallback routing (iterative loop through alternatives). If all providers down, transfer → FAILED, compensation triggers. | P1 | `settla_circuit_breaker_state{name="provider_*_onramp"}` → open. `SettlaCircuitBreakerOpen` alert. Transfer success rate drops. | A: Circuit breaker half-open probes every 10s. Fallback routing to alternative providers. | Yes — CHECK-BEFORE-CALL pattern prevents double-execution. Compensation flow handles partial failures. | `provider-circuit-breaker-open.md`, `transfer-success-rate.md` |
| **Slow responses (5s+)** | ProviderWorker timeout budget (30s total: 5s claim + 20s execute + 5s report). Retry with backoff (200ms, 2x, 3 attempts). If all retries timeout, circuit breaker increments failure counter. | P1 | Provider latency metrics. Retry exhaustion counter. | A: Retry exhaustion → fail transfer → compensation. Circuit breaker prevents cascading slow calls. | Yes — timeout budget prevents unbounded waits. | `provider-circuit-breaker-open.md` |
| **Returning errors (500s)** | ProviderWorker retries (3 attempts, 200ms initial, 2x backoff). After exhaustion, reports failure to engine. Transfer → FAILED. Compensation flow triggered (SIMPLE_REFUND, REVERSE_ONRAMP, etc.). | P1 | Retry exhaustion counter. Transfer failure rate. | A: Automatic compensation. Per-tenant circuit breaker isolation. | Yes | `transfer-success-rate.md` |
| **Returning incorrect data** | Provider normalizer validates response structure. Unexpected formats logged as errors. Transfer fails with provider error. | P1 | Error logs with provider response details. | M: Provider-specific investigation. | Yes — validation prevents acting on bad data. | N/A |
| **Partial success (on-ramp succeeds, off-ramp fails)** | Engine detects off-ramp failure. Compensation strategy selected: REVERSE_ONRAMP → CREDIT_STABLECOIN → MANUAL_REVIEW (escalating). | P1 | Compensation flow metrics. Manual review count. | A: Compensation flow. M: Manual review for complex cases. | Yes — compensation strategies ensure funds are not lost. | `stuck-transfers.md` |

---

## 9. PgBouncer (Connection Pooler)

PgBouncer sits between all application services and PostgreSQL instances.

| Failure Type | Behavior | Severity | Detection | Recovery | Data Safe | Runbook |
|---|---|---|---|---|---|---|
| **Complete outage** | All database operations fail. Equivalent to database outage from application perspective. | P0 | PgBouncer health check (5s interval). Application connection errors. | A: PgBouncer restart (< 5s). Existing connections re-established. | Yes | `pgbouncer-saturation.md` |
| **Connection pool exhaustion** | `max_client_conn=2000` reached. New connections rejected. In-flight queries complete normally. Reserve pool (20 connections, 5s timeout) provides brief overflow capacity. | P1 | PgBouncer `SHOW POOLS` metrics. `settla_pgx_pool_*` utilization > 85%. `SettlaPgxPoolUtilHigh` alert. | A: Connections release after transaction completes. M: Kill long-running transactions if leaked. | Yes — rejection is clean, no partial state. | `pgbouncer-saturation.md` |
| **Server-side connection timeout** | `server_idle_timeout=300s`, `server_lifetime=3600s`. Stale connections recycled. Brief error during recycle. | P2 | Transient error spikes in application logs. | A: pgx pool creates replacement connection. | Yes | N/A |

---

## 10. Cross-Cutting Failure Modes

| Failure Mode | Affected Components | Behavior | Severity | Detection | Recovery | Data Safe |
|---|---|---|---|---|---|---|
| **Clock skew (> 5 min)** | Settlement scheduler, TTLs, idempotency windows | Settlement window calculation shifts. NATS dedup window (5 min) may miss duplicates or falsely dedup. Idempotency keys may expire early/late. | P1 | NTP monitoring. Settlement time drift alerts. | M: Fix NTP, re-run affected settlement window. | Generally yes — UTC timestamps in DB are correct; only runtime comparisons affected. |
| **Memory pressure (> 90%)** | All Go services | GC pressure increases. Latency spikes. If OOM: container killed, Kubernetes restarts. Treasury in-memory positions lost (recovered from DB on restart). Treasury pending ops channel was previously vulnerable to saturation on crash recovery (fixed — see CHAOS_TESTING.md). | P1 | `SettlaPodMemoryHigh` alert. Container OOM events. | A: Kubernetes restart. Treasury reloads from DB. NATS redelivers in-flight messages. | Yes — treasury WAL + DB flush. NATS idempotency. |
| **Network partition (server ↔ node)** | Outbox relay, all workers | Outbox accumulates on server side. Node cannot poll. Workers idle. Transfers stall in intermediate states. | P1 | Outbox relay lag. Worker idle metrics. | A: Partition heals, outbox drains. Recovery detector catches any stragglers (60s cycle). | Yes |
| **Cascading circuit breaker failure** | Multiple providers/dependencies | Multiple circuit breakers open simultaneously. Load shedder rejects new requests. System enters degraded mode. | P0 | Multiple `SettlaCircuitBreakerOpen` alerts. Load shedding metric. | M: Investigate root cause (shared dependency, network issue). | Yes — load shedding prevents overload. |
| **Concurrent settlement trigger** | Settlement scheduler | Two instances trigger settlement for same tenant. `settlement_idempotency` table (UNIQUE constraint on tenant_id + window) prevents duplicate calculations. Second attempt fails with unique violation, logs warning. | P2 | Duplicate settlement warning logs. | A: Idempotency constraint handles it. | Yes — exactly-one-settlement guaranteed. |

---

## Recovery Time Objectives (RTO) Summary

| Dependency | Outage RTO | Degraded RTO | Data Loss Risk |
|---|---|---|---|
| Transfer DB | < 30s (restart), 2-5 min (failover) | Automatic (load shedding) | None (atomic transactions) |
| Ledger DB | < 30s (stub mode) | Automatic | None (TB is authority) |
| Treasury DB | < 30s (in-memory continues) | Automatic | Minimal (unflushed positions at crash) |
| TigerBeetle | < 30s (single-node), 5-15 min (quorum) | Automatic (NATS queues) | None (WAL) |
| NATS | < 30s (reconnect + drain) | Automatic (outbox accumulates) | None (transactional outbox) |
| Redis | Instant (L1 cache + DB fallback) | Automatic | None (cache only) |
| Blockchain RPC | < 60s (circuit breaker reset) | Automatic (CB half-open) | None (checkpoint scanning) |
| Payment Providers | < 10s (CB reset) + fallback routing | Automatic (alternative providers) | None (CHECK-BEFORE-CALL) |
| PgBouncer | < 5s (restart) | Automatic (connection release) | None |

---

## Alert Reference

Key alerts from `deploy/prometheus/alerts/settla-sli-alerts.yml` and `supplemental-rules.yml`:

| Alert | Condition | Severity | Action |
|---|---|---|---|
| `SettlaHighErrorRateFastBurn` | 14.4x error budget burn over 5m+1m | Critical (page) | Immediate investigation |
| `SettlaTransferSuccessCritical` | Success rate < 95% for 5m | Critical | Check providers, check DB |
| `SettlaLatencySLOCritical` | p99 > 2s for 5m | Critical | Check DB, check load |
| `SettlaHealthCheckFailing` | Any dependency health check failing > 1m | Warning | Check specific dependency |
| `SettlaCircuitBreakerOpen` | Any CB in open state > 5m | Warning | Check downstream dependency |
| `SettlaOutboxBacklogHigh` | Unpublished > 100K | Warning | Check NATS, check relay |
| `SettlaOutboxBacklogCritical` | Unpublished > 500K | Critical | Immediate: NATS down? |
| `SettlaPgxPoolUtilHigh` | Pool utilization > 85% | Warning | Check long-running queries |
| `SettlaPgxPoolUtilCritical` | Pool utilization > 95% | Critical | Kill leaked connections |
| `SettlaRetryExhaustionHigh` | > 1 exhausted retry/sec | Warning | Provider degradation |
| `SettlaStuckTransferMaxAge` | Stuck > 5 min | Warning | Check workers, check NATS |
