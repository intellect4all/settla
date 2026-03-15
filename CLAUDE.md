# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Settla is B2B stablecoin settlement infrastructure for fintechs, designed for 50M transactions/day (~580 TPS sustained, 3,000-5,000 TPS peak). The ledger must sustain 15,000-25,000 writes/second at peak.

**Module path:** `github.com/intellect4all/settla`

## Current Status

Phases 1–6 are complete. Phase 7 (Benchmarking & Capacity Proof) is in progress — component benchmarks (7.1) pass all 76/76 targets. Integration load tests (7.2), soak tests (7.3), chaos tests (7.4), and benchmark reporting (7.5) remain. See `TODO.md` for the full checklist.

## Architecture

Settla is a polyglot monorepo with a single Go module and pnpm TypeScript workspaces.

### Modules

| Module | Package | Purpose |
|---|---|---|
| Settla Core | `core` | Pure state machine settlement engine — writes state + outbox entries atomically, zero side effects |
| Settla Compensation | `core/compensation` | Compensation and refund flows for partial failures (SIMPLE_REFUND, REVERSE_ONRAMP, CREDIT_STABLECOIN, MANUAL_REVIEW) |
| Settla Recovery | `core/recovery` | Stuck-transfer detector (60s interval) — re-publishes stalled intents via engine, escalates to manual review |
| Settla Reconciliation | `core/reconciliation` | 5 automated consistency checks: treasury-ledger balance, transfer state, outbox health, provider tx, daily volume |
| Settla Settlement | `core/settlement` | Net settlement calculator + daily scheduler (00:30 UTC); reduces N transfers → 1 net position per currency pair |
| Settla Maintenance | `core/maintenance` | Partition manager, vacuum manager, capacity monitor for 50M+ rows/day workloads |
| Settla Ledger | `ledger` | Dual-backend ledger: TigerBeetle (writes, 1M+ TPS) + Postgres (reads/queries, CQRS) |
| Settla Rail | `rail` (+ `rail/router`, `rail/provider`, `rail/blockchain`) | Smart router (scoring: cost 40%, speed 30%, liquidity 20%, reliability 10%), provider adapters, blockchain clients |
| Settla Treasury | `treasury` | In-memory atomic reservation + background DB flush (100ms interval) |
| Settla Outbox Relay | `node/outbox` | Polls Transfer DB for unpublished outbox entries (20ms, batch 500) and fans them out to the correct NATS JetStream stream |
| Settla Node Workers | `node/worker` | Dedicated per-domain workers: ProviderWorker, LedgerWorker, TreasuryWorker, BlockchainWorker, WebhookWorker, InboundWebhookWorker, TransferWorker |
| Settla Messaging | `node/messaging` | NATS JetStream client, publisher, subscriber, and stream definitions for all 7 Settla streams |
| Settla Cache | `cache` | Two-level cache (local LRU 30s → Redis 5min → DB), rate limiting, idempotency, quote cache |
| Settla API | `api/gateway` (TS/Fastify), `api/webhook` (TS/Fastify) | REST gateway (local tenant cache, gRPC pool, OpenAPI at /docs) + per-tenant outbound webhook dispatcher (HMAC-SHA256, retry, dead letter) |
| Settla Dashboard | `dashboard` (Vue 3/Nuxt) | Ops console + capacity monitoring (settlements, reconciliation, manual reviews) |
| Settla Portal | `portal` (planned) | Tenant self-service dashboard — API key management, transfer history, fee schedules, webhook configuration |

### Modular Monolith Pattern

This is a modular monolith — one binary, strict interface boundaries. All module dependencies flow through interfaces in `domain/`. Modules never import sibling packages directly; they only depend on `domain` types. This means any module can be extracted to a gRPC service by swapping the constructor in `cmd/settla-server/main.go`.

**Interface conventions:**
- `domain.Router` has `Route(ctx, RouteRequest) (*RouteResult, error)` — the domain-level router
- `core.Router` has `GetQuote(ctx, tenantID, QuoteRequest) (*Quote, error)` — the core-level adapter
- `router.CoreRouterAdapter` bridges domain.Router → core.Router with per-tenant fee application
- `router.ProviderRegistry` uses `GetBlockchain(chain)` (not `GetBlockchainClient`) to avoid method signature conflict with `core.ProviderRegistry`

Compile-time interface checks (e.g. `var _ domain.LedgerService = (*Service)(nil)`) ensure each module satisfies its contract.

### Outbox Flow

The engine is a pure state machine. All side effects are expressed as outbox entries written atomically with the state transition. The flow is:

```
API Request
    │
    ▼
Engine.CreateTransfer / Engine.Handle*Result
    │  writes state change + OutboxEntry rows atomically (single DB transaction)
    ▼
Transfer DB (outbox table)
    │
    ▼
node/outbox.Relay  (polls every 20ms, batch 500)
    │  publishes each entry to the correct NATS JetStream stream
    │  marks row as published
    ▼
NATS JetStream (7 streams — see Communication section)
    │
    ├─► ProviderWorker      (SETTLA_PROVIDERS)        — executes on-ramp / off-ramp
    ├─► LedgerWorker        (SETTLA_LEDGER)            — posts / reverses ledger entries
    ├─► TreasuryWorker      (SETTLA_TREASURY)          — reserve / release treasury position
    ├─► BlockchainWorker    (SETTLA_BLOCKCHAIN)        — sends on-chain stablecoin transfer
    ├─► WebhookWorker       (SETTLA_WEBHOOKS)          — delivers outbound tenant webhooks
    ├─► InboundWebhookWorker(SETTLA_PROVIDER_WEBHOOKS) — processes async provider callbacks
    └─► TransferWorker      (SETTLA_TRANSFERS)         — general transfer event fan-out
          │
          ▼
    Worker calls Engine.Handle*Result(IntentResult)
          │  engine validates result, advances state, writes next OutboxEntry rows
          ▼
    (loop continues until terminal state: COMPLETED or FAILED)
```

Each worker uses the CHECK-BEFORE-CALL pattern: it checks whether the action has already been executed (via a provider_transactions record or idempotency key) before calling the external system, so NATS redelivery never causes double-execution.

### Shared packages

- `domain` — shared domain types, interfaces (`Ledger`, `TreasuryManager`, `Router`, `ProviderRegistry`, `EventPublisher`), outbox types (`OutboxEntry`, intent/event constants, all payload structs), value objects (`Money`, `Posting`), and errors. No external deps beyond stdlib + decimal + uuid.
- `store` — database repositories; sub-packages per bounded context (`store/ledgerdb`, `store/transferdb`, `store/treasurydb`), generated by SQLC
- `cache` — Two-level cache (local in-process LRU → Redis → DB), rate limiting (sliding window), idempotency deduplication, quote caching
- `gen` — Generated protobuf Go code (`gen/settla/v1/`)

### High-Throughput Patterns

These patterns exist because the scale math demands them:

| Problem | Threshold | Solution |
|---------|-----------|----------|
| Dual-write bug (state + side effect) | Any failure window between DB write and direct call | Transactional outbox: state + outbox entries written atomically, relay delivers to NATS |
| Ledger write throughput | >10K writes/sec breaks single Postgres | TigerBeetle for write path, Postgres for read/query path |
| Ledger write batching | 25K individual INSERTs/sec | Write-ahead batching: collect 5-50ms, flush as bulk insert |
| Treasury hot-key locking | Thousands of concurrent `SELECT FOR UPDATE` on same row | In-memory atomic reservation with 100ms background flush |
| Database connections | 6+ settla-server replicas × 100 connections each | PgBouncer connection pooling |
| Gateway auth overhead | 5K TPS × Redis round-trip per request | Local in-process tenant cache (30s TTL, ~100ns lookup) |
| Event processing parallelism | 580 events/sec with per-tenant ordering | NATS stream partitioning by tenant hash (8 partitions) |
| gRPC connection overhead | Per-request connection = TCP overhead | gRPC connection pool (~50 persistent, round-robin) |
| Provider double-execution on NATS redelivery | At-least-once delivery guarantees | CHECK-BEFORE-CALL: worker checks provider_transactions table before calling external system |
| Outbox table growth at 50M rows/day | Unbounded table → query degradation | Monthly partitions + PartitionManager drops old partitions instantly (DROP TABLE, never DELETE) |

### Communication

- **gRPC + Protocol Buffers** between TypeScript and Go modules (definitions in `proto/settla/v1/`, generated Go in `gen/settla/v1/`, generated TS in `api/gateway/src/gen/`)
- **NATS JetStream** for async worker dispatch via the transactional outbox relay. 7 dedicated streams (WorkQueue retention, 7-day max age, 2-minute dedup window):

| Stream | Subject pattern | Consumer |
|--------|----------------|----------|
| `SETTLA_TRANSFERS` | `settla.transfer.partition.*.>` | TransferWorker (8 partitions by tenant hash) |
| `SETTLA_PROVIDERS` | `settla.provider.command.>` | ProviderWorker (on-ramp, off-ramp) |
| `SETTLA_LEDGER` | `settla.ledger.>` | LedgerWorker (post, reverse) |
| `SETTLA_TREASURY` | `settla.treasury.>` | TreasuryWorker (reserve, release) |
| `SETTLA_BLOCKCHAIN` | `settla.blockchain.>` | BlockchainWorker (send, confirm) |
| `SETTLA_WEBHOOKS` | `settla.webhook.>` | WebhookWorker (outbound tenant delivery) |
| `SETTLA_PROVIDER_WEBHOOKS` | `settla.provider.inbound.>` | InboundWebhookWorker (async provider callbacks) |

- **Redis** for L2 caching, rate limiting, idempotency; local in-process LRU as L1

### Data Layer

- **TigerBeetle** — ledger write authority (1M+ TPS), source of truth for balances
- **PostgreSQL** partitioned by bounded context, all behind **PgBouncer**:
  - Ledger DB (PgBouncer :6433, raw :5433): CQRS read-side (journal entries, entry lines, balance snapshots) — populated by TB→PG sync consumer
  - Transfer DB (PgBouncer :6434, raw :5434): transfers, events, quotes, tenants, API keys
  - Treasury DB (PgBouncer :6435, raw :5435): position snapshots (updated by 100ms flush goroutine)
- Migrations in `db/migrations/{ledger,transfer,treasury}/` (golang-migrate format)
- SQLC query definitions in `db/queries/{ledger,transfer,treasury}/`
- All partitioned tables use monthly partitions (6 months ahead + default)

## Critical Invariants

These MUST be preserved in all code changes:

1. **Decimal-only monetary math** — `shopspring/decimal` (Go) / `decimal.js` (TS) for ALL monetary amounts. Never use float/float64 for money.
2. **Balanced postings** — Every ledger entry must balance: sum of debits = sum of credits. TigerBeetle enforces this at the engine level.
3. **State machine transitions** — State changes must follow the valid transition map; no skipping states.
4. **Idempotency everywhere** — Every mutation accepts and enforces idempotency keys.
5. **UTC timestamps** — All timestamps stored and transmitted in UTC.
6. **UUID identifiers** — All entity IDs are UUIDs.
7. **Tenant isolation** — ALL data is tenant-scoped. No cross-tenant data leakage. Every query that returns tenant data must filter by tenant_id. Gateway always uses tenant_id from auth, never from request body.
8. **Module boundaries** — `core/` imports only `domain/`, never `ledger/`, `treasury/`, or `rail/` directly.
9. **TigerBeetle is write authority** — Postgres ledger tables are the read model, never written to directly for balance mutations.
10. **Treasury reservations are in-memory** — `Reserve`/`Release` must never hit the database. Only the flush goroutine writes to Postgres.
11. **Engine writes ONLY to outbox** — The settlement engine (`core.Engine`) makes zero network calls and has zero direct dependencies on ledger, treasury, rail, or node. Every side effect (provider call, ledger post, treasury reserve, blockchain send, webhook delivery) is expressed as an `OutboxEntry` (intent or event) written atomically with the state transition. Workers execute intents and call back via `Engine.Handle*Result`. Bypassing the outbox to call side effects directly from the engine is forbidden.

## Build & Development Commands

### Makefile targets (top-level orchestration)

```bash
make build              # Compile Go binaries to bin/
make test               # Go tests with -race
make test-integration   # End-to-end integration tests (5 min timeout)
make lint               # golangci-lint
make proto              # buf generate (Go to gen/, TS to api/gateway/src/gen/)
make migrate-up         # Run all DB migrations (needs SETTLA_*_DB_MIGRATE_URL)
make migrate-down       # Rollback all DB migrations
make migrate-create     # Create new migration (DB=ledger NAME=add_foo)
make sqlc-generate      # Generate Go code from SQL queries (cd db && sqlc generate)
make db-seed            # Load seed data into all databases
make docker-up          # Build and start all services (infra + app)
make docker-down        # Stop all services
make docker-logs        # Tail logs from all services
make docker-reset       # Clean slate: down + remove volumes + rebuild
make bench              # Run all Go benchmarks, output to bench-results.txt
make loadtest           # 5,000 TPS for 10 minutes (peak load proof)
make loadtest-quick     # 1,000 TPS for 2 minutes (CI-friendly)
make loadtest-sustained # 600 TPS for 30 minutes
make loadtest-burst     # Burst recovery: ramp 600→8000→600 TPS
make loadtest-flood     # Single tenant flood: 3,000 TPS one tenant
make loadtest-multi     # Multi-tenant scale: 50 tenants × 100 TPS
make soak               # 2-hour soak test at 1,000 TPS
make soak-short         # 15-minute soak test at 1,000 TPS
make chaos              # Run all chaos test scenarios
make report             # Generate full benchmark report (bench + loadtest-quick + soak-short)
make demo               # Run interactive demo scenarios
make clean              # Remove build artifacts
```

### Local dev setup

```bash
cp .env.example .env   # Create local env file
make docker-up         # Builds Go/TS containers + starts TigerBeetle, Postgres x3 + PgBouncer x3, NATS, Redis
```

Infrastructure ports:
- TigerBeetle :3001
- PgBouncer: ledger :6433, transfer :6434, treasury :6435
- Postgres (raw): ledger :5433, transfer :5434, treasury :5435
- Redis :6379
- NATS :4222 (client) :8222 (monitoring)

Application ports: settla-server :8080 (HTTP) :9090 (gRPC) :6060 (pprof) | gateway :3000 | webhook :3001

### Go

```bash
go build ./cmd/settla-server/...            # Build main server
go build ./cmd/settla-node/...              # Build worker process
go test ./core/...                          # Test a specific package
go test -run TestFunctionName ./ledger/...  # Run a single test
go test -race ./...                         # All tests with race detector
go test -bench=Benchmark ./treasury/...     # Run benchmarks for a package
```

### TypeScript (pnpm workspaces)

```bash
pnpm install                          # Install all workspace deps
pnpm --filter @settla/gateway dev     # Gateway dev server (port 3000)
pnpm --filter @settla/gateway build   # Build gateway
pnpm --filter @settla/gateway test    # Run gateway tests (vitest)
pnpm --filter @settla/webhook dev     # Webhook dev server (port 3001)
pnpm --filter @settla/webhook build   # Build webhook
pnpm --filter @settla/webhook test    # Run webhook tests (vitest)
pnpm --filter @settla/dashboard dev   # Dashboard dev server
```

## Code Conventions

- Single Go module — all packages import as `github.com/intellect4all/settla/{core,ledger,rail,...}`
- `cmd/` for Go entrypoints, domain packages at repo root (`core/`, `ledger/`, etc.)
- Proto definitions in `proto/settla/v1/`; generated Go code in `gen/settla/v1/`; generated TS in `api/gateway/src/gen/`
- Each bounded context owns its own database — no cross-context direct DB queries
- Event names follow past-tense convention: `transfer.initiated`, `settlement.completed`
- NATS subjects include partition: `settla.transfer.partition.{N}.{event_type}`
- Go errors wrapped with context: `fmt.Errorf("settla-ledger: crediting account %s: %w", accountID, err)`
- Use `slog` for structured Go logging with fields: `tenant_id`, `transfer_id`, `account_code`
- Use pino (via Fastify) for TS logging with fields: `request_id`, `tenant_id`, `method`, `path`, `status`, `duration_ms`
- All tenant-scoped account codes use format: `tenant:{slug}:assets:bank:gbp:clearing`
- System account codes omit tenant prefix: `assets:crypto:usdt:tron`
- TypeScript: ES modules (`"type": "module"`), Fastify 5, vitest for tests
- Fastify plugins that need global scope (auth, rate-limit) must use `fastify-plugin` (fp) to break encapsulation
- ioredis ESM import: `import IORedis from "ioredis"` for constructor, `import type { Redis } from "ioredis"` for type

## Entrypoints

- `cmd/settla-server/` — main Go server (Core + Ledger + Rail + Treasury + core/compensation + core/recovery + core/reconciliation + core/settlement + core/maintenance), 6+ replicas in production
- `cmd/settla-node/` — worker process (Outbox Relay + all 7 dedicated workers: ProviderWorker, LedgerWorker, TreasuryWorker, BlockchainWorker, WebhookWorker, InboundWebhookWorker, TransferWorker), 8+ instances
- `api/gateway/` — Fastify REST API (TypeScript), 4+ replicas. Routes: `/v1/quotes`, `/v1/transfers`, `/v1/treasury/*`, `/health`, `/docs` (OpenAPI)
- `api/webhook/` — Inbound provider webhook receiver (TypeScript), 2+ replicas. Normalises raw provider callbacks into `ProviderWebhookPayload` and publishes to `SETTLA_PROVIDER_WEBHOOKS` stream for `InboundWebhookWorker`
- `api/grpc/` — Go gRPC server implementation (`server.go`)
- `tests/loadtest/` — Go load test harness (not k6) for capacity proof, multiple scenarios
- `tests/chaos/` — Chaos test framework for failure recovery proof
- `tests/integration/` — E2E integration tests (tenant isolation, concurrency, corridors)

## Multi-Tenancy

Every fintech (Lemfi, Fincra, Paystack) is a tenant. Key concepts:
- `tenants` table lives in Transfer DB with API keys, fee schedules, limits
- All tenant data queries MUST include `tenant_id` filter (enforced by SQLC generated code)
- API authentication: `Authorization: Bearer sk_live_xxx` → SHA-256 hash → tenant resolution
- Auth cache: L1 local (30s TTL, ~100ns) → L2 Redis (5min TTL, ~0.5ms) → L3 DB (source of truth)
- Fee schedules are per-tenant (basis points), negotiated per fintech. Lemfi: 40/35 bps, Fincra: 25/20 bps
- Treasury positions are per-tenant, completely isolated
- Idempotency keys are scoped per-tenant: `UNIQUE(tenant_id, idempotency_key)`
- Rate limiting: per-tenant, local counters synced to Redis every 5 seconds
- Seed tenants: Lemfi (`a0000000-...-000000000001`), Fincra (`b0000000-...-000000000002`)

## Capacity Reference

```
50M transactions/day | ~580 TPS sustained | 3,000-5,000 TPS peak
Ledger: 200-250M entry_lines/day | 15,000-25,000 writes/sec peak
Transfer DB: 50M transfers + 50M events/day
Treasury: ~50 hot positions under constant concurrent pressure
Gateway: 5,000 auth lookups/sec at peak
Local cache: 107ns auth lookup (measured)
```
