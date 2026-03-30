# Settla

**B2B stablecoin settlement infrastructure for fintechs**

Settla is the settlement backbone that fintechs like Lemfi, Fincra, and Paystack plug into for cross-border payments. Each fintech gets its own tenant with negotiated fee schedules, isolated treasury positions, and dedicated rate limits. The platform routes payments through stablecoin rails (GBP → USDT → NGN) with smart provider selection, double-entry ledger tracking, and real-time treasury management.

Built for **50 million transactions/day** — 580 TPS sustained, 5,000 TPS peak. The ledger sustains 25,000 writes/second using TigerBeetle, treasury reservations complete in under 1 microsecond via in-memory atomics, and the API gateway resolves tenant auth in 107 nanoseconds from local cache.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────────┐
│                         Settla Dashboard                                  │
│              (Vue 3 + Nuxt ops console: transfers, treasury,              │
│               ledger, settlements, reconciliation, manual reviews)        │
└─────────────────────────────┬────────────────────────────────────────────┘
                              │
┌─────────────────────────────┴────────────────────────────────────────────┐
│                      Tyk API Gateway (:443)                               │
│        TLS · Auth key validation · Rate limiting · CORS · Analytics       │
└──────────────────────┬──────────────────────────┬────────────────────────┘
                       │ HTTP (internal)           │ Inbound provider webhooks
┌──────────────────────┴──────────────┐   ┌───────┴──────────────────────┐
│     Fastify BFF (api/gateway :3000) │   │  Webhook Dispatcher          │
│  Tenant context · Validation        │   │  (api/webhook :3001)         │
│  gRPC pool · Response transform     │   │  HMAC verify · dedup ·       │
│  Inbound webhook ingestion          │   │  NATS publish · dead letter  │
└──────────────────────┬──────────────┘   └──────────────────────────────┘
                       │ gRPC / protobuf (~50 persistent connections)
┌──────────────────────┴──────────────────────────────────────────────────┐
│                      settla-server (:9090 gRPC, :8080 HTTP)              │
│                                                                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐   │
│  │  Core       │  │  Ledger     │  │  Rail       │  │  Treasury   │   │
│  │  (pure      │  │  (TigerBeetle│  │  (router +  │  │  (in-memory │   │
│  │  state      │  │   write +   │  │  providers +│  │  reserve +  │   │
│  │  machine +  │  │  Postgres   │  │  blockchain)│  │  100ms      │   │
│  │  outbox)    │  │  CQRS read) │  │             │  │  DB flush)  │   │
│  └──────┬──────┘  └─────────────┘  └─────────────┘  └─────────────┘   │
│         │ writes state + outbox entries atomically                       │
│  ┌──────┴──────────────────────────────────────────────────────────┐    │
│  │  Transfer DB (outbox table + transfer/event tables)              │    │
│  └──────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
                              │
                   ┌──────────┴──────────┐
                   │  Outbox Relay        │  settla-node polls every 20ms,
                   │  (node/outbox)       │  publishes to NATS JetStream
                   └──────────┬──────────┘
                              │
┌─────────────────────────────┴────────────────────────────────────────────┐
│                      NATS JetStream (8 partitions)                        │
│              settla.transfer.partition.{N}.{event_type}                   │
└─────┬───────────┬───────────┬───────────┬───────────┬────────────────────┘
      │           │           │           │           │
      ▼           ▼           ▼           ▼           ▼
┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
│ Transfer │ │ Treasury │ │  Ledger  │ │ Provider │ │Blockchain│ │ Webhook  │
│  Worker  │ │  Worker  │ │  Worker  │ │  Worker  │ │  Worker  │ │  Worker  │
│ (saga    │ │(reserve/ │ │ (post /  │ │(on-ramp/ │ │(on-chain │ │(deliver  │
│ orch.)   │ │ release) │ │ reverse) │ │ off-ramp)│ │  send)   │ │webhooks) │
└────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
     │            │            │            │            │            │
┌────┴─────┐ ┌────┴─────┐ ┌────┴─────┐ ┌────┴─────┐ ┌────┴─────┐    │
│ Inbound  │ │ Deposit  │ │  Bank    │ │  Email   │ │   DLQ    │    │
│ Webhook  │ │  Worker  │ │ Deposit  │ │  Worker  │ │ Monitor  │    │
│ (provider│ │ (crypto  │ │  Worker  │ │ (notifs) │ │ (retry/  │    │
│ callbacks│ │ deposits)│ │ (fiat)   │ │          │ │  alert)  │    │
└────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘    │
     │            │            │            │            │            │
     └────────────┴────────────┴────────────┴────────────┴────────────┘
                              │ writes
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
   ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
   │   Ledger DB  │   │  Transfer DB │   │  Treasury DB │
   │  (PgBouncer  │   │  (PgBouncer  │   │  (PgBouncer  │
   │   :6433)     │   │   :6434)     │   │   :6435)     │
   └──────────────┘   └──────────────┘   └──────────────┘
                      PostgreSQL (partitioned, monthly)
```

### How the settlement pipeline works

1. **Gateway** receives `POST /v1/transfers`, authenticates the tenant (local cache → Redis → gRPC), and forwards to `settla-server` via gRPC.
2. **Engine** (pure state machine) validates the request, generates a quote, creates the transfer record, and writes a `transfer.created` outbox entry — all in a single Postgres transaction. Zero network calls.
3. **Outbox relay** polls the outbox table every 20ms (batch 500) and publishes pending entries to NATS JetStream.
4. **Transfer worker** (saga orchestrator) consumes `transfer.created` and drives the state machine: fund → on-ramp → settle → off-ramp → complete. Each step calls back into the engine, which writes the next outbox intent.
5. **Dedicated workers** execute the actual side effects: treasury reserves/releases, ledger postings, provider API calls, blockchain transactions, tenant webhook deliveries, crypto/bank deposit processing, and email notifications.
6. **Inbound provider webhooks** arrive at `api/webhook`, are HMAC-verified, deduplicated, and published to NATS where the **inbound webhook worker** maps them back to engine callbacks.
7. **Chain monitor** watches on-chain stablecoin transfers (Tron, EVM) and triggers deposit session processing for incoming crypto payments.

### Transactional Outbox

The engine never calls providers, posts ledger entries, or publishes events directly. It writes state changes and side-effect intents atomically in a single database transaction. The outbox relay then publishes those intents to NATS, where dedicated workers execute the actual side effects and call back into the engine with results. This eliminates dual-write bugs: at 50M transactions/day, even a 0.01% publish failure rate would mean 5,000 stuck transfers per day. With the outbox, the write path is NATS-independent and events are never lost.

## Why a Modular Monolith

Settla ships as a single Go binary (`settla-server`) where each module (Core, Ledger, Rail, Treasury) communicates through interfaces defined in `domain/interfaces.go`, not through direct struct access. This gives us:

- **Day-one velocity** — one repo, one build, one deploy; no distributed system overhead
- **Compile-time contracts** — Go's type system enforces module boundaries; the engine depends on `domain.LedgerService`, not on `ledger.Service` directly
- **Zero-cost extraction** — every interface is a future gRPC seam. To extract Ledger: write a gRPC server hosting `ledger.Service`, write a client implementing `domain.LedgerService`, swap one line in `main.go`
- **Honest complexity** — we don't pay the microservices tax (service discovery, distributed tracing, network partitions) until a specific module actually needs independent scaling or deployment

The worker process (`settla-node`) is already a separate binary communicating via NATS events — it validates the extraction pattern from day one.

See [ADR-001: Modular Monolith](docs/adr/001-modular-monolith.md) for extraction triggers and the full decision record.

## Platform Modules

| Module | Package | Responsibility |
|--------|---------|----------------|
| Tyk Gateway | `deploy/tyk` | Edge gateway: TLS, auth key validation, rate limiting, CORS, analytics |
| Settla Core | `core` | Pure state machine engine — transfer lifecycle, outbox intent writes, no side-effect deps |
| Core: Compensation | `core/compensation` | Refund and recovery strategies for partial failures (simple refund, reverse on-ramp, credit stablecoin, manual review) |
| Core: Recovery | `core/recovery` | Stuck-transfer detector (60s interval, panic-safe) — re-publishes intents or escalates to manual review |
| Core: Reconciliation | `core/reconciliation` | 6 automated checks: treasury-ledger balance, transfer state, outbox health, provider tx, daily volume, settlement fees |
| Core: Settlement | `core/settlement` | Net settlement calculator and daily scheduler for NET_SETTLEMENT tenants |
| Core: Maintenance | `core/maintenance` | Partition lifecycle (create ahead, drop old), vacuum manager, capacity monitor |
| Core: Deposit | `core/deposit` | Crypto deposit session engine — on-chain payment detection, confirmation tracking, auto-convert/hold strategies |
| Core: Bank Deposit | `core/bankdeposit` | Fiat deposit via virtual bank accounts — bank credit detection, partner reconciliation |
| Core: Analytics | `core/analytics` | Analytics snapshots and data exports for tenant reporting |
| Core: Payment Links | `core/paymentlink` | Payment link generation and public redemption flow for merchant collections |
| Settla Ledger | `ledger` | Immutable double-entry ledger — TigerBeetle write path, Postgres CQRS read path, write-ahead batching |
| Settla Rail | `rail` | Smart payment router (cost 40%, speed 30%, liquidity 20%, reliability 10%), provider adapters, blockchain clients |
| Settla Treasury | `treasury` | In-memory atomic reservations, 100ms DB flush, per-tenant position tracking |
| Chain Monitor | `node/chainmonitor` | Watches on-chain stablecoin transfers (Tron, EVM) and triggers deposit sessions |
| Outbox Relay | `node/outbox` | Polls outbox table every 20ms, publishes to NATS JetStream with deduplication and partition cleanup |
| Settla Node | `node/worker` | 11 dedicated workers: transfer (saga), treasury, ledger, provider, blockchain, outbound webhook, inbound webhook, deposit, bank deposit, email, DLQ monitor |
| Settla API Gateway | `api/gateway` | Fastify BFF — tenant resolution, idempotency, gRPC pool, REST→gRPC transform, OpenAPI at `/docs` |
| Settla Webhook | `api/webhook` | Outbound webhook dispatcher (HMAC-SHA256, exponential backoff, dead letter) + inbound provider webhook ingestion |
| Settla Dashboard | `dashboard` | Vue 3 + Nuxt ops console: transfers, treasury, ledger, reconciliation, net settlements, manual reviews, tenant listing |
| Settla Portal | `portal` | Vue 3 + Nuxt tenant self-service portal — auth, onboarding/KYB, deposits, payment links, analytics, crypto balances |
| Shared UI | `packages/ui` | Shared Vue component library used by Dashboard and Portal |

## Prerequisites

- **Go** 1.25+
- **Node.js** 22+ with **pnpm**
- **Docker** and **Docker Compose**
- **buf** (protobuf toolchain)
- **golangci-lint**
- **golang-migrate** (database migrations)
- **sqlc** (for regenerating database query code)

## Quickstart

```bash
# 1. Create local env file and generate required secrets
cp .env.example .env
# Generate JWT secret (required):
echo "SETTLA_JWT_SECRET=$(openssl rand -base64 32)" >> .env
# Generate API key HMAC secret (required for API key creation):
echo "SETTLA_API_KEY_HMAC_SECRET=$(openssl rand -hex 32)" >> .env

# 2. Start all infrastructure + application containers
#    (TigerBeetle, Postgres x3, PgBouncer x3, NATS, Redis, Tyk, settla-server, settla-node, gateway, webhook)
make docker-up

# 3. Build Go binaries (for local development outside Docker)
make build

# 4. Install TypeScript dependencies
pnpm install

# 5. Run the gateway in dev mode (port 3000)
pnpm --filter @settla/gateway dev

# 6. Run all Go tests with race detector
make test

# 7. Run linter
make lint

# 8. Create Tyk API keys for seed tenants (Lemfi, Fincra)
make tyk-setup
```

## Development Modes

### Provider modes

```bash
make provider-mode-mock      # Use mock providers (default, no blockchain)
make provider-mode-testnet   # Use real testnet blockchain (Tron Nile, Sepolia)
make testnet-setup           # Initialize testnet wallets and fund from faucets
make testnet-verify          # Verify testnet RPC connectivity and wallet status
make testnet-status          # Show wallet addresses and explorer links
```

### Database workflow

```bash
make migrate-up              # Run all migrations (ledger + transfer + treasury)
make migrate-down            # Rollback all migrations
make migrate-create DB=transfer NAME=add_foo   # Create new migration
make sqlc-generate           # Regenerate Go code from SQL queries
make db-seed                 # Load seed tenants (Lemfi, Fincra) and positions
```

### Protobuf

```bash
make proto                   # buf lint + generate (Go to gen/, TS to api/gateway/src/gen/)
```

## All Make Targets

| Target | Description |
|--------|-------------|
| `make build` | Compile Go binaries to `bin/` |
| `make test` | Go tests with `-race` |
| `make test-integration` | End-to-end integration tests (5 min timeout) |
| `make lint` | Run `golangci-lint` |
| `make proto` | buf generate — Go + TypeScript stubs |
| `make migrate-up` | Run all DB migrations |
| `make migrate-down` | Rollback all DB migrations |
| `make migrate-create` | Create new migration (`DB=ledger NAME=add_foo`) |
| `make sqlc-generate` | Regenerate Go DB query code |
| `make db-seed` | Load seed data into all databases |
| `make docker-up` | Build and start all services |
| `make docker-down` | Stop all services |
| `make docker-logs` | Tail logs from all services |
| `make docker-reset` | Clean slate: down + remove volumes + rebuild |
| `make tyk-setup` | Create Tyk API keys for seed tenants |
| `make bench` | Run all Go benchmarks, write to `bench-results.txt` |
| `make loadtest` | Peak load: 5,000 TPS for 10 minutes |
| `make loadtest-quick` | Quick load: 1,000 TPS for 2 minutes (CI-friendly) |
| `make loadtest-sustained` | Sustained: 600 TPS for 30 minutes |
| `make loadtest-burst` | Burst recovery: ramp 600 → 8,000 → 600 TPS |
| `make loadtest-flood` | Single-tenant flood: 3,000 TPS |
| `make loadtest-multi` | Multi-tenant: 50 tenants × 100 TPS |
| `make loadtest-daily` | Simulated daily volume: 580 TPS for 1 hour |
| `make soak` | 2-hour soak at 1,000 TPS |
| `make soak-short` | 15-minute soak at 1,000 TPS |
| `make chaos` | Run all chaos test scenarios |
| `make report` | Full benchmark report (bench + loadtest-quick + soak-short) |
| `make demo` | Run interactive demo scenarios |
| `make profile` | Capture heap/CPU/goroutine pprof profiles |
| `make api-test` | Run tenant API tests against running services |
| `make api-test-full` | Start Docker, seed, then run API tests |
| `make docs-openapi` | Export OpenAPI spec and copy to docs site |
| `make docs-dev` | Run Mintlify docs dev server |
| `make docs-build` | Build the documentation site |
| `make provider-mode-mock` | Switch provider mode to mock |
| `make provider-mode-testnet` | Switch provider mode to testnet |
| `make testnet-setup` | Initialize testnet wallets and faucet funding |
| `make testnet-verify` | Verify testnet RPC + wallet status |
| `make testnet-status` | Show wallet addresses and explorer links |
| `make clean` | Remove build artifacts |

## Project Structure

```
settla/
├── core/              # Settlement engine + state machine
│   ├── compensation/  # Refund strategies for partial failures
│   ├── recovery/      # Stuck-transfer detector and escalation
│   ├── reconciliation/# 6-check automated reconciliation engine
│   ├── settlement/    # Net settlement calculator and scheduler
│   ├── maintenance/   # Partition lifecycle, vacuum, capacity monitoring
│   ├── deposit/       # Crypto deposit session engine
│   ├── bankdeposit/   # Fiat deposit via virtual bank accounts
│   ├── analytics/     # Analytics snapshots and data exports
│   └── paymentlink/   # Payment link generation and redemption
├── ledger/            # Double-entry ledger (TigerBeetle + Postgres CQRS)
├── rail/              # Router, providers, blockchain clients
│   ├── router/        # Smart routing with scoring and tenant fee application
│   ├── provider/      # On-ramp/off-ramp provider adapters
│   └── blockchain/    # Blockchain clients (Tron, Ethereum/EVM)
├── treasury/          # In-memory position tracking + 100ms DB flush
├── node/              # NATS workers + event-driven saga processing
│   ├── outbox/        # Outbox relay: polls Transfer DB → publishes to NATS
│   ├── messaging/     # NATS client, publisher, subscriber, 12 stream definitions
│   ├── chainmonitor/  # On-chain stablecoin transfer watcher (Tron, EVM)
│   └── worker/        # 11 workers: transfer, treasury, ledger, provider, blockchain, webhook, inbound webhook, deposit, bank deposit, email, DLQ monitor
├── domain/            # Shared domain types, interfaces, outbox entry types
├── store/             # Database repositories (SQLC-generated)
│   ├── ledgerdb/
│   ├── transferdb/    # Includes outbox store and relay adapter
│   └── treasurydb/
├── cache/             # Two-level cache (LRU → Redis), rate limiting, idempotency
├── observability/     # slog logger, Prometheus metrics, gRPC interceptors
├── api/               # TypeScript + Go services
│   ├── gateway/       # Fastify BFF (REST + OpenAPI at /docs)
│   ├── webhook/       # Outbound dispatcher + inbound provider webhook ingestion
│   └── grpc/          # Go gRPC server (transfers, deposits, analytics, portal auth)
├── dashboard/         # Vue 3 + Nuxt ops console
├── portal/            # Vue 3 + Nuxt tenant self-service portal
├── packages/          # Shared libraries
│   └── ui/            # Shared Vue component library
├── cmd/               # Go entrypoints
│   ├── settla-server/ # Core + Ledger + Rail + Treasury (gRPC :9090, HTTP :8080)
│   └── settla-node/   # Outbox relay + chain monitor + worker process
├── proto/             # Protobuf definitions (settla/v1/)
├── gen/               # Generated Go protobuf code
├── db/                # Migrations + SQLC queries
│   ├── migrations/    # golang-migrate SQL files (ledger, transfer, treasury)
│   ├── queries/       # SQLC query definitions
│   └── seed/          # Seed SQL for dev tenants and positions
├── deploy/            # Docker Compose, Kubernetes manifests, Tyk config
│   ├── docker-compose.yml
│   ├── k8s/           # Kustomize base + overlays
│   ├── tyk/           # Tyk API definitions, policies, middleware
│   └── runbooks/      # Operational runbooks
├── tests/             # Test harnesses
│   ├── integration/   # E2E tests (tenant isolation, concurrency, corridors, deposits)
│   ├── loadtest/      # Go load test harness (multi-scenario, not k6)
│   ├── chaos/         # Chaos test framework (failure recovery)
│   └── benchmarks/    # Benchmark comparison tooling
├── scripts/           # Dev tooling (demo, testnet setup, report generation)
├── docs/              # Architecture docs + ADRs
│   └── adr/           # 018 decision records
└── docs-site/         # Mintlify API documentation site
```

## Capacity at a Glance

| Metric | Sustained | Peak |
|--------|-----------|------|
| Transactions/day | 50,000,000 | — |
| Transactions/sec | 580 TPS | 5,000 TPS |
| Ledger writes/sec | 2,300 | 25,000 |
| Treasury reservation | — | <1μs (in-memory) |
| Auth lookup | — | 107ns (local cache) |
| Gateway replicas | 4 | 8 |
| Server replicas | 6 | 12 |
| Node worker instances | 8 | 16 |

See [Capacity Planning](docs/capacity-planning.md) for the full math and bottleneck analysis.

## Demo

Run all 5 demo scenarios (uses in-memory stores, no infrastructure required):

```bash
make demo
```

Scenarios:
1. **GBP→NGN Corridor (Lemfi)** — full pipeline: quote → fund → on-ramp → settle → off-ramp → complete
2. **NGN→GBP Corridor (Fincra)** — reverse corridor with different fee schedule
3. **Tenant Isolation** — proves Lemfi cannot see Fincra's transfers
4. **Burst Concurrency** — 100 concurrent transfers, verifies no over-reservation
5. **Per-Tenant Fees** — demonstrates negotiated fee schedules (Lemfi: 40/35 bps, Fincra: 25/20 bps)

## Key Invariants

- **Decimal math only** — `shopspring/decimal` (Go) and `decimal.js` (TS) for all monetary amounts
- **Balanced postings** — every ledger entry must balance (debits = credits)
- **Valid state transitions** — transfers follow a strict state machine; no state skipping
- **Idempotent mutations** — every write operation uses idempotency keys (scoped per-tenant)
- **UTC timestamps** — all times stored and transmitted in UTC
- **UUID identifiers** — all entity IDs are UUIDs
- **Tenant isolation** — all data is tenant-scoped; every query filters by `tenant_id`
- **Outbox atomicity** — state changes and outbox entries are written in the same Postgres transaction
- **TigerBeetle is write authority** — Postgres ledger tables are the read model only

## Architecture Decision Records

| ADR | Decision | Key Threshold |
|-----|----------|---------------|
| [001](docs/adr/001-modular-monolith.md) | Modular Monolith | Team <10 engineers, extract when modules need independent scaling |
| [002](docs/adr/002-tigerbeetle-ledger-writes.md) | TigerBeetle for Ledger Writes | >10K writes/sec breaks single Postgres |
| [003](docs/adr/003-cqrs-dual-backend-ledger.md) | CQRS Dual-Backend Ledger | 250M entry_lines/day requires separated read/write paths |
| [004](docs/adr/004-in-memory-treasury-reservation.md) | In-Memory Treasury Reservation | SELECT FOR UPDATE deadlocks >5% at peak on ~50 hot rows |
| [005](docs/adr/005-nats-partitioned-events.md) | NATS Partitioned Events | 580 events/sec with per-tenant ordering guarantee |
| [006](docs/adr/006-two-level-cache.md) | Two-Level Cache (Local + Redis) | 5K auth lookups/sec; Redis-only adds 2.5s cumulative latency/sec |
| [007](docs/adr/007-pgbouncer-connection-pooling.md) | PgBouncer Connection Pooling | 900 connections vs Postgres limit of ~200 |
| [008](docs/adr/008-multi-database-bounded-contexts.md) | Multi-Database Bounded Contexts | Cross-context JOINs bottleneck at 50M txn/day |
| [009](docs/adr/009-grpc-typescript-go.md) | gRPC Between TypeScript and Go | JSON overhead ~2ms at 5K TPS |
| [010](docs/adr/010-decimal-monetary-math.md) | Decimal-Only Monetary Math | float64 loses precision at >15 significant digits |
| [011](docs/adr/011-per-tenant-fee-schedules.md) | Per-Tenant Fee Schedules | B2B platform with negotiated per-fintech rates |
| [012](docs/adr/012-hmac-webhook-signatures.md) | HMAC-SHA256 Webhook Signatures | Public webhook URLs require cryptographic verification |
| [013](docs/adr/013-monthly-table-partitioning.md) | Monthly Table Partitioning | 1.5B rows/month degrades queries and VACUUM |
| [014](docs/adr/014-transactional-outbox.md) | Transactional Outbox | 0.01% dual-write failure = 5,000 lost events/day |
| [015](docs/adr/015-pure-state-machine-engine.md) | Pure State Machine Engine | 32 partial failure permutations per transition |
| [016](docs/adr/016-tyk-api-gateway.md) | Tyk API Gateway | 5K TPS infra/logic separation |
| [017](docs/adr/017-inbound-provider-webhooks.md) | Inbound Provider Webhooks | 200M webhooks/day, triple-layer dedup |
| [018](docs/adr/018-partition-drop-vs-delete.md) | Partition DROP vs DELETE | 100M+ rows/day, DELETE takes hours + VACUUM |
