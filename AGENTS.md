# AGENTS.md — Multi-Agent Coordination Guide

> This file helps AI coding agents (Claude Code, OpenCode, Codex, Cursor, etc.) work on Settla effectively.

## Project Summary

Settla is B2B stablecoin settlement infrastructure for fintechs. Polyglot monorepo: Go (domain + backend) + TypeScript (API gateway, webhooks, dashboard). Designed for 50M transactions/day (~580 TPS sustained, 5,000 TPS peak).

**Module path:** `github.com/intellect4all/settla`

## Current Status

- **Phases 1–6**: Complete (foundation, domain core, event infra, API layer, observability, integration & demo)
- **Phase 7**: Next — Benchmarking & Capacity Proof

See `TODO.md` for detailed task checklist with success criteria.

## Quick Start

```bash
cp .env.example .env
make docker-up         # Infra: TigerBeetle, 3×Postgres, 3×PgBouncer, NATS, Redis
make migrate-up        # Apply all DB migrations
make db-seed           # Seed demo tenants (Lemfi, Fincra)
make test              # Go tests with -race
make build             # Compile Go binaries
cd api/gateway && pnpm install && pnpm build   # Build gateway
```

## Repository Layout

```
cmd/
  settla-server/       # Go main server (Core + Ledger + Rail + Treasury)
  settla-node/         # Go worker (partitioned NATS consumers)
core/                  # Settlement engine + state machine
domain/                # Shared types, interfaces, value objects — NO external deps
ledger/                # Dual-backend: TigerBeetle (writes) + Postgres (reads)
treasury/              # In-memory atomic reservation + background DB flush
rail/
  router/              # Smart router with scoring (cost 40%, speed 30%, liquidity 20%, reliability 10%)
  provider/            # Provider adapters + registry (on-ramp, off-ramp, blockchain)
  provider/mock/       # Mock providers for testing
node/
  worker/              # Partitioned NATS JetStream consumers
  messaging/           # Event publishing
cache/                 # Redis + local in-process LRU cache
store/                 # SQLC-generated DB repositories
  ledgerdb/            # Ledger read-side queries
  transferdb/          # Transfer + tenant queries
  treasurydb/          # Position snapshot queries
api/
  gateway/             # TypeScript/Fastify REST API (port 3000)
  webhook/             # TypeScript/Fastify webhook dispatcher
dashboard/             # Vue 3/Nuxt ops console (Phase 5)
db/
  migrations/          # golang-migrate format, per bounded context
  queries/             # SQLC query definitions
  sqlc.yaml            # SQLC config (run from db/ dir)
proto/settla/v1/       # Protocol Buffer definitions
gen/                   # Generated protobuf code (Go + TS)
deploy/
  docker-compose.yml   # Full infrastructure
  docker/              # Dockerfiles
  nats/                # NATS JetStream config
docs/adr/              # Architecture Decision Records
tests/
  loadtest/            # Go load test harness
  chaos/               # Chaos test framework
```

## Build, Test & Lint Commands

### Go (Primary Language)

```bash
# Build
make build                          # Compile all Go binaries to bin/
go build ./cmd/settla-server/...    # Build main server
go build ./cmd/settla-node/...      # Build worker process

# Test
make test                           # Run all Go tests with race detector
go test -race ./...                 # All tests with race detector
go test -race ./core/...            # Test specific package
go test -race -run TestFunctionName ./ledger/...  # Run single test
go test -bench=Benchmark ./treasury/...           # Run benchmarks
make test-integration               # E2E integration tests (5min timeout)

# Lint
make lint                           # Run golangci-lint

# Other
go mod tidy                         # Clean up dependencies
make proto                          # buf generate (proto → gen/)
```

### TypeScript (pnpm Workspaces)

```bash
pnpm install                        # Install all workspace deps
pnpm --filter @settla/gateway dev   # Gateway dev server (port 3000)
pnpm --filter @settla/webhook dev   # Webhook dev server
pnpm --filter @settla/dashboard dev # Dashboard dev server
pnpm --filter @settla/gateway build # Build gateway
pnpm --filter @settla/gateway test  # Run gateway tests
```

### Database & Infrastructure

```bash
make migrate-up                     # Run all DB migrations
make migrate-down                   # Rollback all DB migrations
make migrate-create DB=ledger NAME=add_foo  # Create new migration
cd db && sqlc generate              # Generate Go code from SQL queries
make db-seed                        # Load seed data
make docker-up                      # Start all services
make docker-down                    # Stop all services
make docker-reset                   # Clean slate (removes volumes)
```

## Critical Rules (MUST Follow)

1. **Decimal-only money**: `shopspring/decimal` (Go), `decimal.js` (TS). NEVER float for money.
2. **Module boundaries**: `core/` imports only `domain/`. Never import sibling packages directly. All cross-module deps go through `domain/` interfaces.
3. **Tenant isolation**: ALL queries MUST filter by `tenant_id`. No cross-tenant data access.
4. **TigerBeetle is write authority**: Postgres ledger tables are read-side only (CQRS).
5. **Treasury reservations are in-memory**: `Reserve`/`Release` NEVER hit the database. Only the flush goroutine writes snapshots.
6. **Balanced postings**: Every ledger entry must balance (debits = credits).
7. **Idempotency everywhere**: Every mutation takes an idempotency key.
8. **UTC timestamps**: All timestamps in UTC.
9. **UUID identifiers**: All entity IDs are UUIDs.

## Code Conventions

### Go

**Imports:** Group stdlib → external → internal, separated by blank lines.
```go
import (
    "context"
    "fmt"
    "log/slog"

    "github.com/google/uuid"
    "github.com/shopspring/decimal"

    "github.com/intellect4all/settla/domain"
)
```

**Error Handling:** Wrap with context: `fmt.Errorf("settla-{module}: {operation}: %w", err)`

**Compile-time interface checks:** `var _ domain.Ledger = (*Service)(nil)`

**Logging:** `slog` with structured fields: `logger.With("module", "ledger.service")`

**Account codes:** `tenant:{slug}:assets:bank:gbp:clearing` (tenant), `assets:crypto:usdt:tron` (system)

**Events:** Past-tense: `transfer.initiated`, `settlement.completed`

**NATS subjects:** `settla.transfer.partition.{N}.{event_type}`

### TypeScript

- ESM modules (`"type": "module"` in package.json), `.js` extensions in imports
- Fastify 5 with `fastify-plugin` (fp) for global hooks (auth, rate-limit)
- ioredis: `import IORedis from "ioredis"` (constructor), `import type { Redis } from "ioredis"` (type)
- Proto loading: `@grpc/proto-loader` at runtime (no codegen needed for gateway)
- Schema-based serialization via Fastify JSON schemas (fast-json-stringify)

### Proto

- Definitions in `proto/settla/v1/`
- All RPCs include `tenant_id` field
- Generated code in `gen/`

## Interface Mapping (Gotchas)

### Router Adapter
`domain.Router.Route()` returns `[]domain.Route` (generic routing).
`core.Router.GetQuote()` returns a single `core.QuoteResult` (engine-facing).
Bridged by `router.CoreRouterAdapter` in `rail/router/router.go`.

### ProviderRegistry Naming
- `core.ProviderRegistry.GetBlockchainClient(chain)` — no error return
- `router.ProviderRegistry.GetBlockchain(chain)` — returns error
- Different method names allow one struct to implement both interfaces.

## Infrastructure

| Service | Port | Notes |
|---------|------|-------|
| TigerBeetle | 3001 | Ledger writes, needs `privileged: true` on macOS |
| PgBouncer (ledger) | 6433 | Connection pool for ledger DB |
| PgBouncer (transfer) | 6434 | Connection pool for transfer DB |
| PgBouncer (treasury) | 6435 | Connection pool for treasury DB |
| Postgres (ledger) | 5433 | Raw access (migrations only) |
| Postgres (transfer) | 5434 | Raw access (migrations only) |
| Postgres (treasury) | 5435 | Raw access (migrations only) |
| Redis | 6379 | Cache + rate limiting |
| NATS | 4222/8222 | JetStream messaging / monitoring |
| settla-server | 8080/9090/6060 | HTTP / gRPC / pprof |
| gateway | 3000 | REST API |

## Demo Tenants

| Tenant | ID | Slug | Fee (on/off bps) |
|--------|-----|------|-------------------|
| Lemfi | `a0...01` | `lemfi` | 40 / 35 |
| Fincra | `b0...02` | `fincra` | 25 / 20 |

## Remaining Work (Phases 5–7)

### Phase 5 — Dashboard & Observability (5.2 complete, 5.1 dashboard pending)
- 5.1: Vue 3/Nuxt dashboard with capacity monitoring page (pending)
- 5.2: Prometheus metrics (all modules), Grafana dashboards (5), structured logging (complete)

### Phase 7 — Benchmarking & Capacity Proof (Next)
- Component benchmarks (`make bench`)
- Load tests: 5,000 TPS sustained for 10 min
- Soak test: 15 min at 1,000 TPS
- Chaos tests: TigerBeetle restart, Postgres pause, NATS restart, Redis down
- Benchmark report with measured (not estimated) numbers
