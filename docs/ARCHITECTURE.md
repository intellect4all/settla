# Settla Architecture

This document describes the high-level architecture of Settla, including the transactional outbox pattern that ensures consistency between state changes and async event processing.

For architecture decisions and their rationale, see the [ADR index](#architecture-decision-records) below.

## System Overview

Settla is a modular monolith (ADR-001) built as a single Go binary (`settla-server`) with strict module boundaries enforced through domain interfaces. Each module communicates through interfaces defined in `domain/`, never through direct struct imports.

```
┌──────────────────────────────────────────────────────────────────────┐
│                         Settla Dashboard                             │
│                      (Vue 3 + Nuxt ops console)                      │
└─────────────────────────────┬────────────────────────────────────────┘
                              │
┌─────────────────────────────┴────────────────────────────────────────┐
│                      Tyk API Gateway (:443)                          │
│          TLS · Auth · Rate Limiting · CORS · Analytics               │
└─────────────────────────────┬────────────────────────────────────────┘
                              │ HTTP (internal)
┌─────────────────────────────┴────────────────────────────────────────┐
│                  Fastify BFF (api/gateway :3000)                     │
│        Tenant context · Validation · gRPC pool · Response transform  │
└──────┬──────────────┬───────────────┬──────────────┬─────────────────┘
       │   gRPC/Proto │               │              │
       ▼              ▼               ▼              ▼
┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────────┐
│   Settla   │ │   Settla   │ │   Settla   │ │    Settla      │
│    Core    │ │   Ledger   │ │    Rail    │ │   Treasury     │
│  (pure     │ │ (double-   │ │  (router + │ │  (in-memory    │
│   state    │ │  entry +   │ │ providers +│ │  reservations  │
│  machine)  │ │   CQRS)    │ │ blockchain)│ │  + DB flush)   │
└─────┬──────┘ └─────┬──────┘ └─────┬──────┘ └───────┬────────┘
      │              │              │                 │
      └──────────────┴──────┬───────┴─────────────────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
   ┌──────────────┐  ┌──────────┐  ┌──────────────┐
   │   Outbox     │  │   NATS   │  │   Workers    │
   │   Relay      │──│ JetStream│──│ (settla-node)│
   │  (50ms poll) │  │(8 parts) │  │  5 worker    │
   └──────────────┘  └──────────┘  │  types       │
                                   └──────┬───────┘
                                          │
                            ┌─────────────┼─────────────┐
                            ▼             ▼             ▼
                      ┌──────────┐ ┌──────────┐ ┌──────────┐
                      │ Ledger   │ │ Transfer │ │ Treasury │
                      │   DB     │ │   DB     │ │   DB     │
                      └──────────┘ └──────────┘ └──────────┘
                                PostgreSQL (partitioned)
```

## Transactional Outbox Pattern

The settlement engine is a **pure state machine** (ADR-015). It never calls providers, posts ledger entries, or publishes events directly. Instead, it writes state changes and side-effect intents atomically in a single database transaction using the **transactional outbox pattern** (ADR-014).

### Flow

```
Engine (pure state machine)
  │ BEGIN: UPDATE transfer + INSERT outbox entries → COMMIT
  ▼
Outbox Relay (polls every 50ms)
  │ Publishes to NATS with message ID dedup
  ▼
NATS JetStream (7 streams, 8 partitions)
  │ Routes to dedicated workers
  ▼
Workers (provider, ledger, treasury, blockchain, webhook)
  │ Execute side effects, write results back via outbox
  ▼
Transfer State Worker → Engine.Handle*Result() → more outbox entries → ...
```

### Why This Exists

At 50M transactions/day, the engine must update transfer status AND trigger async workers atomically. The naive approach (commit DB, then publish to NATS) is a dual-write problem: if NATS publish fails after DB commit, the transfer is stuck. Even a 0.01% failure rate means 5,000 lost events per day.

The outbox pattern eliminates this: both the state change and the event intent are written in a single Postgres transaction. The outbox relay polls for unpublished entries and publishes them to NATS. If the relay crashes, it resumes from where it left off — no events are lost.

### Key Properties

- **Engine methods complete in <1ms** (just a DB transaction, no network calls)
- **Zero dual-write bugs** (atomic DB transaction guarantees consistency)
- **NATS-independent write path** (NATS outage does not block state changes)
- **Exactly-once delivery** via NATS Msg-Id dedup on outbox entry UUID
- **Outbox cleanup via partition DROP** (daily partitions, instant O(1) cleanup — ADR-018)

### Worker Types

| Worker | Consumes | Side Effect |
|--------|----------|-------------|
| Treasury Worker | `treasury.reserve_requested` | Reserve/release treasury positions |
| Ledger Worker | `ledger.posting_requested` | Post double-entry ledger entries |
| Provider Worker | `provider.submit_requested` | Call external payment provider APIs |
| Blockchain Worker | `blockchain.tx_requested` | Submit/monitor blockchain transactions |
| Webhook Worker | `webhook.dispatch_requested` | Dispatch tenant webhook notifications |

Workers report results back to the engine via `Handle*Result()` methods, which are themselves pure state transitions that may produce further outbox entries. This creates an event-driven saga that progresses each transfer through its lifecycle.

## Inbound Provider Webhooks

External providers send async notifications to Settla when operations complete. These are processed through a dedicated webhook ingestion pipeline (ADR-017):

1. **Immediate ack**: always return HTTP 200 within <10ms
2. **Triple dedup**: Redis SETNX (72h TTL) + NATS Msg-Id + engine idempotency keys
3. **Per-provider normalization**: each provider has a dedicated normalizer for payload format and signature verification
4. **Async processing**: normalized events published to NATS, consumed by workers that advance transfer state via the engine

## Data Layer

Settla uses three separate PostgreSQL databases (ADR-008), each behind PgBouncer (ADR-007):

| Database | Purpose | Write Authority | Partitioning |
|----------|---------|----------------|-------------|
| Ledger DB | Journal entries, entry lines, balance snapshots | TigerBeetle (ADR-002), synced to PG (ADR-003) | Monthly (ADR-013) |
| Transfer DB | Transfers, events, quotes, tenants, outbox | settla-server | Monthly + Daily (ADR-013, ADR-018) |
| Treasury DB | Position snapshots | In-memory flush every 100ms (ADR-004) | Monthly (ADR-013) |

## Architecture Decision Records

| ADR | Decision | Key Threshold |
|-----|----------|---------------|
| [001](adr/001-modular-monolith.md) | Modular Monolith | Team <10 engineers |
| [002](adr/002-tigerbeetle-ledger-writes.md) | TigerBeetle for Ledger Writes | >10K writes/sec |
| [003](adr/003-cqrs-dual-backend-ledger.md) | CQRS Dual-Backend Ledger | 250M entry_lines/day |
| [004](adr/004-in-memory-treasury-reservation.md) | In-Memory Treasury Reservation | SELECT FOR UPDATE deadlocks >5% |
| [005](adr/005-nats-partitioned-events.md) | NATS Partitioned Events | 580 events/sec ordering |
| [006](adr/006-two-level-cache.md) | Two-Level Cache | 5K auth lookups/sec |
| [007](adr/007-pgbouncer-connection-pooling.md) | PgBouncer Connection Pooling | 900 connections |
| [008](adr/008-multi-database-bounded-contexts.md) | Multi-Database Bounded Contexts | Cross-context JOIN bottleneck |
| [009](adr/009-grpc-typescript-go.md) | gRPC Between TS and Go | JSON overhead ~2ms at 5K TPS |
| [010](adr/010-decimal-monetary-math.md) | Decimal-Only Monetary Math | float64 precision loss |
| [011](adr/011-per-tenant-fee-schedules.md) | Per-Tenant Fee Schedules | Negotiated per-fintech rates |
| [012](adr/012-hmac-webhook-signatures.md) | HMAC-SHA256 Webhook Signatures | Public URL verification |
| [013](adr/013-monthly-table-partitioning.md) | Monthly Table Partitioning | 1.5B rows/month |
| [014](adr/014-transactional-outbox.md) | Transactional Outbox | Dual-write at 50M tx/day |
| [015](adr/015-pure-state-machine-engine.md) | Pure State Machine Engine | Partial failure in 32 permutations |
| [016](adr/016-tyk-api-gateway.md) | Tyk API Gateway | 5K TPS infra/logic separation |
| [017](adr/017-inbound-provider-webhooks.md) | Inbound Provider Webhooks | 200M webhooks/day reliability |
| [018](adr/018-partition-drop-vs-delete.md) | Partition DROP vs DELETE | 100M+ rows/day cleanup |

