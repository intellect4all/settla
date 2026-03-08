# Settla

**B2B stablecoin settlement infrastructure for fintechs**

Settla is the settlement backbone that fintechs like Lemfi, Fincra, and Paystack plug into for cross-border payments. Each fintech gets its own tenant with negotiated fee schedules, isolated treasury positions, and dedicated rate limits. The platform routes payments through stablecoin rails (GBP → USDT → NGN) with smart provider selection, double-entry ledger tracking, and real-time treasury management.

Built for **50 million transactions/day** — 580 TPS sustained, 5,000 TPS peak. The ledger sustains 25,000 writes/second using TigerBeetle, treasury reservations complete in under 1 microsecond via in-memory atomics, and the API gateway resolves tenant auth in 107 nanoseconds from local cache.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        Settla Dashboard                         │
│                     (Vue 3 + Nuxt ops console)                  │
└──────────────────────────────┬──────────────────────────────────┘
                               │
┌──────────────────────────────┴──────────────────────────────────┐
│                   Settla API (Fastify Gateway)                  │
│                  REST endpoints + Webhooks                      │
└──────┬──────────────┬───────────────┬──────────────┬────────────┘
       │   gRPC/Proto │               │              │
       ▼              ▼               ▼              ▼
┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────────┐
│   Settla   │ │   Settla   │ │   Settla   │ │    Settla      │
│    Core    │ │   Ledger   │ │    Rail    │ │   Treasury     │
│  (engine + │ │ (double-   │ │  (router + │ │  (positions +  │
│   state    │ │  entry +   │ │ providers +│ │  liquidity)    │
│  machine)  │ │   CQRS)    │ │ blockchain)│ │                │
└─────┬──────┘ └─────┬──────┘ └─────┬──────┘ └───────┬────────┘
      │              │              │                 │
      └──────────────┴──────┬───────┴─────────────────┘
                            │ NATS JetStream
                     ┌──────┴──────┐
                     │   Settla    │
                     │    Node     │
                     │  (workers + │
                     │   sagas)    │
                     └──────┬──────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │ Ledger   │ │ Transfer │ │ Treasury │
        │   DB     │ │   DB     │ │   DB     │
        └──────────┘ └──────────┘ └──────────┘
                   PostgreSQL (partitioned)
```

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
| Settla Core | `core` | Settlement engine, transfer lifecycle, state machine |
| Settla Ledger | `ledger` | Immutable double-entry ledger, CQRS, balance snapshots |
| Settla Rail | `rail` | Smart payment router, provider abstraction, blockchain clients |
| Settla Treasury | `treasury` | Position tracking, liquidity management, rebalancing |
| Settla Node | `node` | NATS JetStream workers, event-driven saga processing |
| Settla API | `api/gateway`, `api/webhook` | REST gateway (Fastify), webhook dispatcher |
| Settla Dashboard | `dashboard` | Vue 3 + Nuxt ops console |

## Prerequisites

- **Go** 1.22+
- **Node.js** 22+ with **pnpm**
- **Docker** and **Docker Compose**
- **buf** (protobuf toolchain)
- **golangci-lint**
- **golang-migrate** (database migrations)

## Quickstart

```bash
# Start infrastructure (Postgres x3, NATS, Redis)
make docker-up

# Build Go binaries
make build

# Install TypeScript dependencies
pnpm install

# Run the gateway in dev mode
pnpm --filter @settla/gateway dev

# Run all Go tests
make test

# Run linter
make lint
```

## Project Structure

```
settla/
├── core/              # Settlement engine + state machine
├── ledger/            # Double-entry ledger + CQRS
├── rail/              # Router, providers, blockchain
│   ├── router/
│   ├── provider/
│   └── blockchain/
├── treasury/          # Position tracking + liquidity
├── node/              # NATS workers + saga processing
│   ├── worker/
│   └── messaging/
├── domain/            # Shared domain types + interfaces
├── store/             # Database repositories (SQLC)
│   ├── ledgerdb/
│   ├── transferdb/
│   └── treasurydb/
├── api/               # TypeScript services
│   ├── gateway/       # Fastify REST API
│   └── webhook/       # Webhook dispatcher
├── dashboard/         # Vue 3 + Nuxt ops console
├── cmd/               # Go entrypoints
│   ├── settla-server/ # Core + Ledger + Rail + Treasury
│   └── settla-node/   # Worker process
├── proto/             # Protobuf definitions
├── db/                # Migrations + SQLC queries
├── deploy/            # Docker + compose
├── scripts/           # Dev tools + seed data
└── docs/              # Architecture docs + ADRs
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
- **Valid state transitions** — transfers follow a strict state machine
- **Idempotent mutations** — every write operation uses idempotency keys
- **UTC timestamps** — all times stored and transmitted in UTC
- **UUID identifiers** — all entity IDs are UUIDs
- **Tenant isolation** — all data is tenant-scoped, no cross-tenant leakage

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
