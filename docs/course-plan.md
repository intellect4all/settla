# Settla: Building Production-Grade Fintech Infrastructure — A System Design Course

## Course Overview

**Title:** *Settla — Building Production-Grade Fintech Infrastructure from Scratch*

**Tagline:** Build a real B2B stablecoin settlement system that handles 50M transactions/day. Not theory — working code, production patterns, and battle-tested architecture.

**Target Audience:**
- Senior backend engineers wanting to break into fintech
- System design interview candidates seeking depth beyond surface-level answers
- Fintech engineers looking to understand settlement infrastructure end-to-end
- Engineering leads evaluating architecture patterns for high-throughput financial systems

**Prerequisites:**
- Comfortable with Go or willingness to learn (primary language)
- Basic understanding of databases, APIs, and distributed systems
- Familiarity with Docker and containerized development

**What Makes This Course Different:**
- You build a *complete, working system* — not toy examples
- Every pattern exists because the math demands it (50M txn/day, 580 TPS sustained, 5K TPS peak)
- Real fintech domain: multi-tenant settlement, ledger accounting, treasury management, compliance
- Polyglot architecture: Go backend, TypeScript API gateway, Vue.js dashboards, Protocol Buffers
- Full production stack: TigerBeetle, PostgreSQL, NATS JetStream, Redis, Kubernetes

**Format:** 11 modules, 62 chapters, project-based (each chapter produces working code that integrates into the final system)

---

## Course Progression

```
Module 0:  The Money Domain       → Payment rails, stablecoins, FX, settlement, regulation, corridors
Module 1:  Foundations             → Domain modeling, state machines, the "why" behind everything
Module 2:  The Ledger             → Double-entry accounting at scale (TigerBeetle + Postgres CQRS)
Module 3:  The Settlement Engine  → Pure state machine, transactional outbox, zero side effects
Module 4:  Treasury & Routing     → In-memory atomic reservations, smart provider routing
Module 5:  Async Workers          → NATS JetStream, outbox relay, 11 dedicated workers
Module 6:  The API Layer          → gRPC services, REST gateway, multi-tenant auth, caching
Module 7:  Operations             → Reconciliation, compensation, recovery, settlement cycles
Module 8:  Production Readiness   → Load testing, chaos engineering, observability, deployment
Module 9:  Deposits & Payments    → Crypto deposits, bank deposits, payment links, blockchain ops
Module 10: Security & Compliance  → API key security, PII encryption, secrets, regulation
```

---

## Module 1: Foundations — Why Fintech Systems Are Different

*You can't build what you don't understand. This module establishes the domain, the constraints, and the architectural decisions that everything else depends on.*

### Chapter 1.1: The Business of Settlement
**Focus:** Understanding the problem space before writing code
- What B2B stablecoin settlement actually is
- The lifecycle of a cross-border payment (GBP → USDT → NGN)
- Players: fintechs (tenants), on-ramp/off-ramp providers, blockchain networks, liquidity pools
- Revenue model: basis-point fees on each leg
- Why settlement infrastructure is a "picks and shovels" business
- **Exercise:** Map out 3 real-world settlement corridors and identify the providers involved

### Chapter 1.2: Capacity Math — Deriving Architecture from Requirements
**Focus:** How 50M transactions/day drives every design decision
- Doing the math: 50M/day → 580 TPS sustained → 5,000 TPS peak
- Ledger writes: each transfer = 4-6 entry lines → 15,000-25,000 writes/sec at peak
- Why single-Postgres breaks (connection limits, lock contention, WAL bottleneck)
- The "hot key" problem in treasury (thousands of concurrent updates to same row)
- Deriving the technology stack from throughput requirements
- **Key insight:** Architecture isn't chosen — it's *derived from constraints*
- **Exercise:** Given a set of business requirements, calculate the throughput needs and identify which "standard" patterns will break

### Chapter 1.3: Domain Modeling — Types That Prevent Bugs
**Focus:** Building the `domain/` package — shared types, interfaces, value objects
- Why `domain/` has zero external dependencies (stdlib + decimal + uuid only)
- Value objects: `Money`, `CurrencyPair`, `Posting`
- Entity design: `Transfer`, `Tenant`, `JournalEntry`
- The `shopspring/decimal` rule: why `float64` is forbidden for money (with real failure examples)
- UUID identifiers and UTC timestamps everywhere
- Compile-time interface checks: `var _ domain.LedgerService = (*Service)(nil)`
- **Build:** The complete `domain/` package with 15+ types and 8+ interfaces

### Chapter 1.4: The Transfer State Machine
**Focus:** Modeling the transfer lifecycle as an explicit state machine
- States: CREATED → FUNDED → ON_RAMPING → SETTLING → OFF_RAMPING → COMPLETED
- Alternative paths: → FAILED → REFUNDING → REFUNDED
- The `ValidTransitions` map: why state transitions must be explicitly enumerated
- Preventing invalid transitions at the type level
- State machine as documentation (the diagram IS the code)
- Why "status" fields with string comparisons are a fintech anti-pattern
- **Build:** `domain/transfer.go` with state machine, transition validation, and comprehensive tests

### Chapter 1.5: Multi-Tenancy — Designing for Isolation
**Focus:** Every fintech is a tenant. Every query must be scoped.
- Tenant model: API keys, fee schedules, limits, settlement models (PREFUNDED vs NET_SETTLEMENT)
- Fee schedules in basis points (Lemfi: 40/35 bps, Fincra: 25/20 bps)
- KYB status: PENDING → IN_REVIEW → VERIFIED → REJECTED
- The `CalculateFee()` function with min/max clamping
- Tenant isolation invariant: ALL data queries MUST filter by `tenant_id`
- Why `tenant_id` comes from auth, NEVER from request body
- Idempotency keys scoped per-tenant: `UNIQUE(tenant_id, idempotency_key)`
- **Build:** `domain/tenant.go` with fee calculation, validation, and the multi-tenancy contract

### Chapter 1.6: The Modular Monolith Pattern
**Focus:** One binary, strict interface boundaries, extractable modules
- Why microservices are premature for most startups (and most fintechs)
- The modular monolith: modules communicate through interfaces, not network calls
- Dependency rule: `core/` imports only `domain/`, never `ledger/`, `treasury/`, or `rail/`
- How any module can be extracted to a gRPC service by swapping one constructor
- Interface conventions: `domain.Router` vs `core.Router` vs `router.CoreRouterAdapter`
- **Exercise:** Diagram the dependency graph and verify no circular imports exist

---

## Module 2: The Ledger — Double-Entry Accounting at Scale

*The ledger is the financial system of record. Get it wrong and you lose money. Get it slow and you lose customers.*

### Chapter 2.1: Double-Entry Accounting for Engineers
**Focus:** Accounting fundamentals that every fintech engineer must know
- The accounting equation: Assets = Liabilities + Equity
- Debits and credits: why they exist and how they prevent errors
- Journal entries and the "balanced posting" invariant (sum of debits = sum of credits)
- Chart of accounts for a settlement system
- Account code conventions: `tenant:{slug}:assets:bank:gbp:clearing` vs `assets:crypto:usdt:tron`
- **Key insight:** Double-entry isn't bureaucracy — it's a distributed consistency mechanism that's worked for 700 years
- **Build:** `domain/ledger.go` with `ValidateEntries()` pure function and entry types

### Chapter 2.2: Why TigerBeetle — The Case for a Purpose-Built Financial Database
**Focus:** Understanding TigerBeetle and why generic databases fail at ledger scale
- The ledger write problem: 15,000-25,000 writes/sec with strict consistency
- Why Postgres alone can't handle this (WAL bottleneck, lock contention, fsync overhead)
- TigerBeetle's architecture: deterministic simulation testing, io_uring, zero-copy
- TigerBeetle's guarantees: strict serializability, no partial writes, balance enforcement
- The 1M+ TPS claim and what it means in practice
- `AmountScale = 10^8` — matching Postgres NUMERIC(28,8) precision
- **Exercise:** Benchmark Postgres INSERT throughput vs TigerBeetle and measure the gap

### Chapter 2.3: CQRS Ledger — TigerBeetle Writes, Postgres Reads
**Focus:** Building the dual-backend ledger with command-query separation
- Why CQRS: TigerBeetle is fast but query-limited; Postgres is slow for writes but query-rich
- Write path: `PostEntries()` → TigerBeetle (source of truth for balances)
- Read path: `GetBalance()`, `GetEntries()` → Postgres (dashboards, reports, queries)
- The sync consumer: tailing TigerBeetle and populating Postgres read model
- `AccountIDFromCode()`: SHA-256(code)[:16] for deterministic TigerBeetle account IDs
- `DecimalToTBAmount()` / `TBAmountToDecimal()`: precision-safe conversions
- Why Postgres ledger tables are NEVER written to directly for balance mutations
- **Build:** `ledger/tigerbeetle.go`, `ledger/postgres.go`, `ledger/sync.go`

### Chapter 2.4: Write-Ahead Batching
**Focus:** Collecting individual writes into bulk inserts for throughput
- The problem: 25K individual INSERTs/sec overwhelms any database
- Solution: collect writes for 5-50ms, then flush as a single bulk insert
- Configurable flush interval and batch size
- Error handling: what happens when a batch partially fails?
- Backpressure: rejecting new writes when the buffer is full
- **Build:** `ledger/batch.go` with configurable batching and metrics

### Chapter 2.5: Ledger Entry Reversals
**Focus:** Immutability and how to "undo" in an append-only ledger
- Why ledger entries are never updated or deleted
- Reversals: create a new entry that mirrors the original with swapped debits/credits
- Linking reversals to original entries for audit
- Partial reversals and their complexity
- **Build:** `ReverseEntry()` implementation with full test coverage

### Chapter 2.6: Partitioning for 50M+ Rows/Day
**Focus:** Table partitioning strategies for unbounded growth
- Weekly partitions for `entry_lines` (8 partitions maintained ahead)
- Monthly partitions for transfers and events
- Daily partitions for the outbox table (48-hour retention, then DROP TABLE)
- Why DROP TABLE is instant but DELETE FROM is catastrophic at scale
- Default partitions as safety nets
- **Build:** Partition-aware migrations and the `PartitionManager` service

---

## Module 3: The Settlement Engine — A Pure State Machine

*The engine is the brain of the system. It makes decisions but never executes them. This separation is what makes the system reliable at scale.*

### Chapter 3.1: The Transactional Outbox Pattern
**Focus:** Solving the dual-write problem — the most critical pattern in the system
- The dual-write bug: state change succeeds, side effect fails (or vice versa)
- Why "just use a transaction" doesn't work across databases and message brokers
- The outbox pattern: write state change + side effect instructions atomically in one DB transaction
- Outbox entries: intents (commands for workers) vs events (notifications)
- `NewOutboxIntent()` and `NewOutboxEvent()` constructors
- Intent payloads: `TreasuryReservePayload`, `ProviderOnRampPayload`, `LedgerPostPayload`, `BlockchainSendPayload`, `WebhookDeliverPayload`
- Why the engine writes ONLY to the outbox — zero direct network calls
- **Key insight:** The outbox pattern converts a distributed transaction into a local transaction + reliable delivery
- **Build:** `domain/outbox.go` with all intent/event types and payload structures

### Chapter 3.2: Building the Engine — Zero Side Effects
**Focus:** Implementing the core settlement engine as a pure state machine
- Design philosophy: the engine reads state, validates transitions, writes new state + outbox entries
- No dependencies on ledger, treasury, rail, or node modules
- `CreateTransfer()`: validate tenant, check limits, enforce idempotency, create transfer + outbox event
- `FundTransfer()`: CREATED → FUNDED, write treasury.reserve intent
- The atomic write: `CreateWithOutbox()` — state change + outbox entries in one transaction
- Data interfaces: `TransferStore`, `TenantStore`, `OutboxStore`
- **Build:** `core/engine.go` with CreateTransfer and FundTransfer

### Chapter 3.3: The Full Transfer Lifecycle
**Focus:** Implementing every state transition in the engine
- `InitiateOnRamp()`: FUNDED → ON_RAMPING, write provider.onramp intent
- `SettleTransfer()`: ON_RAMPING → SETTLING, write blockchain.send intent
- `InitiateOffRamp()`: SETTLING → OFF_RAMPING, write provider.offramp intent
- `CompleteTransfer()`: OFF_RAMPING → COMPLETED, write webhook intent
- Result handlers: `HandleTreasuryResult()`, `HandleOnRampResult()`, `HandleSettlementResult()`, `HandleOffRampResult()`
- How workers call back into the engine with `IntentResult`
- **Build:** Complete engine implementation with all state transitions

### Chapter 3.4: Failure Paths and Idempotency
**Focus:** What happens when things go wrong (they always do)
- `FailTransfer()`: move to FAILED, initiate refund compensation
- `InitiateRefund()`: FAILED → REFUNDING, create compensation outbox entries
- `HandleRefundResult()`: finalize refund
- Idempotency enforcement: `GetByIdempotencyKey()` check before every mutation
- Why idempotency keys are scoped per-tenant
- Testing failure paths: simulating every possible failure point
- **Build:** Failure handling, refund flows, and comprehensive test suite

### Chapter 3.5: The Engine's Data Layer
**Focus:** SQLC-generated repositories and the store pattern
- Why SQLC over ORMs: compiled SQL, type safety, no runtime reflection
- Writing SQL queries in `db/queries/transfer/transfers.sql`
- SQLC generates: `models.go`, `querier.go`, `transfers.sql.go`
- The adapter pattern: `store/transferdb/adapter.go` bridges SQLC queries → domain interfaces
- Atomic operations: `CreateWithOutbox()` using database transactions
- **Build:** SQL queries, SQLC generation, and store adapters

---

## Module 4: Treasury & Smart Routing

*Treasury manages liquidity in real-time. The router selects the optimal path for each settlement. Both must be fast — treasury at nanosecond scale, routing at millisecond scale.*

### Chapter 4.1: The Hot-Key Problem
**Focus:** Why naive treasury implementation fails at scale
- The scenario: 5,000 concurrent transfers all trying to `SELECT FOR UPDATE` on the same treasury position row
- Lock contention, connection pool exhaustion, cascading timeouts
- Why "just shard the data" doesn't work for treasury (positions ARE the hot key)
- The solution: move the hot path entirely to memory
- **Exercise:** Benchmark the naive approach and measure where it breaks

### Chapter 4.2: In-Memory Atomic Treasury
**Focus:** Lock-free position management with CAS operations
- `PositionState` with atomic int64 counters: `balanceMicro`, `lockedMicro`, `reservedMicro`
- `Available() = balance - locked - reserved`
- Micro-unit representation: int64 at 10^6 scale (avoids floating point, enables atomics)
- `Reserve()`: CAS loop on `reservedMicro` — no mutex, no database, ~100ns
- `Release()`: CAS loop to return reserved funds
- `Commit()`: move reserved → locked (idempotent via operation log)
- Why mutex-based approaches are 10-100x slower than CAS
- **Build:** `treasury/manager.go` with Reserve/Release/Commit

### Chapter 4.3: Crash Recovery — WAL + Background Flush
**Focus:** Making in-memory state durable without sacrificing speed
- The problem: in-memory state is lost on crash
- Write-Ahead Log (WAL): every reserve/release/commit logged to DB synchronously via `ReserveOpStore`
- Sync threshold: large reservations (≥ threshold) also flush position to DB immediately
- Background flush goroutine: every 100ms, batch-insert pending ops, flush dirty positions
- Startup recovery: `LoadPositions()` replays from last DB snapshot + WAL entries
- Idempotency map: `{transferID}:{operation}` → prevents double-execution from NATS redelivery
- Sliding window cleanup for the idempotency map (default 1 hour)
- **Build:** WAL implementation, background flush, crash recovery tests

### Chapter 4.4: Smart Provider Routing
**Focus:** Selecting the optimal settlement path across multiple providers
- The routing problem: multiple on-ramp providers, off-ramp providers, blockchains, and stablecoins
- Scoring weights: cost 40%, speed 30%, liquidity 20%, reliability 10%
- Route selection algorithm:
  1. Enumerate all valid corridor combinations
  2. Get quotes from each on-ramp and off-ramp provider
  3. Calculate gas fees per blockchain
  4. Score each combination across all dimensions
  5. Select the highest-scored route
- All scoring uses `shopspring/decimal` — never float
- **Build:** `rail/router/router.go` with the complete routing algorithm

### Chapter 4.5: Provider Adapters and Registry
**Focus:** Abstracting provider differences behind clean interfaces
- `OnRampProvider` interface: `ID()`, `SupportedPairs()`, `GetQuote()`, `Execute()`, `GetStatus()`
- `OffRampProvider` interface: same pattern for stablecoin → fiat
- `ProviderRegistry`: lookup providers by ID, get all providers for a corridor
- Mock providers for testing with configurable latency and failure rates
- Per-tenant fee application: router wraps quotes with tenant's basis points
- Fallback alternatives stored in quotes (workers can switch without re-consulting engine)
- **Build:** Provider interfaces, mock implementations, and the registry

### Chapter 4.6: Pluggable Scoring — Liquidity and Reliability
**Focus:** Optional scoring dimensions that improve routing over time
- `LiquidityScorer`: scores provider liquidity for a given currency and amount (0-1)
- `ReliabilityScorer`: scores provider historical reliability (0-1)
- Default behavior: score = 1.0 when no scorer is configured
- How to build a reliability scorer from provider_transactions success/failure history
- How to build a liquidity scorer from real-time provider balance APIs
- **Exercise:** Implement a simple reliability scorer that uses a sliding window of recent transactions

---

## Module 5: Async Workers — Event-Driven Execution

*The engine decides what should happen. Workers make it happen. NATS JetStream connects them with guaranteed delivery and per-tenant ordering.*

### Chapter 5.1: NATS JetStream — Choosing the Right Message Broker
**Focus:** Why NATS JetStream over Kafka, RabbitMQ, or SQS
- Requirements: at-least-once delivery, per-tenant ordering, deduplication, 7-day retention
- NATS JetStream vs Kafka: operational simplicity, built-in dedup, lower latency
- Stream configuration: WorkQueue retention, 7-day max age, 2-minute dedup window
- 7 dedicated streams for domain isolation
- **Build:** `node/messaging/streams.go` with all stream definitions

### Chapter 5.2: The Outbox Relay
**Focus:** Bridging the database outbox to the message broker
- Poll loop: every 20ms, fetch up to 500 unpublished outbox entries
- For each entry: determine NATS subject, publish with message ID = outbox UUID, mark as published
- Subject routing: `SubjectForEventType()` maps event types to NATS subjects
- Retry handling: increment `retry_count`, expire at `MaxRetries`
- Error scenarios: NATS down, slow consumers, backpressure
- **Build:** `node/outbox/relay.go` with polling, publishing, and error handling

### Chapter 5.3: Per-Tenant Stream Partitioning
**Focus:** Ensuring per-tenant ordering while enabling parallel processing
- The ordering problem: transfers for the same tenant must be processed in order
- The parallelism problem: processing all transfers on one worker doesn't scale
- Solution: `TenantPartition(tenantID) = hash(tenantID) % numPartitions` (default 8)
- Subject pattern: `settla.transfer.partition.{N}.{event_type}`
- Each partition has its own NATS consumer → dedicated goroutine
- Result: per-tenant ordering preserved, 8x parallelism across tenants
- **Build:** `node/messaging/subjects.go` with partitioning logic

### Chapter 5.4: The CHECK-BEFORE-CALL Pattern
**Focus:** Making workers idempotent in the face of at-least-once delivery
- The problem: NATS redelivers messages on timeout, crash, or rebalance
- Naive worker: executes the provider call again → double-charges the customer
- CHECK-BEFORE-CALL:
  1. Check `provider_transactions` table: has this transfer+operation been executed?
  2. If yes (terminal status): ACK the message, skip execution
  3. If no: claim via `INSERT ON CONFLICT DO NOTHING`, execute, update result
- Why this works: the claim is idempotent, the execution is guarded by the claim
- **Build:** `ProviderWorker` with CHECK-BEFORE-CALL implementation

### Chapter 5.5: Building the 7 Workers
**Focus:** Implementing each dedicated worker with its domain logic
- **ProviderWorker**: on-ramp/off-ramp execution with circuit breakers and fallback routing
  - Circuit breaker config: failure threshold 15, reset 10s, half-open max 2
  - Fallback: try alternative provider from quote on primary failure
- **LedgerWorker**: calls `ledger.PostEntries()` or `ledger.ReverseEntry()`
- **TreasuryWorker**: calls `treasury.Reserve()` or `treasury.Release()`
- **BlockchainWorker**: sends on-chain transaction, polls for confirmation
- **WebhookWorker**: delivers outbound tenant webhooks with HMAC-SHA256
  - Exponential backoff retry policy + dead-letter queue
- **InboundWebhookWorker**: normalizes provider-specific callbacks → canonical payload
- **TransferWorker**: general event fan-out (analytics, audit, dashboard updates)
- **Build:** All 7 workers in `node/worker/`

### Chapter 5.6: Circuit Breakers and Fallback Routing
**Focus:** Resilience patterns for external provider calls
- Why circuit breakers: a failing provider shouldn't block all transfers
- Three states: Closed (normal), Open (fast-fail), Half-Open (probe)
- Configuration: failure threshold, reset timeout, half-open request limit
- Fallback routing: when primary provider fails, try alternative from quote
- Per-provider circuit breakers (not global)
- Metrics: circuit breaker state changes, fallback rates
- **Build:** Circuit breaker implementation with fallback routing in ProviderWorker

---

## Module 6: The API Layer — Gateway, gRPC, and Auth

*The API layer is where external traffic meets internal systems. It must be fast, secure, and tenant-aware.*

### Chapter 6.1: Protocol Buffers and gRPC Services
**Focus:** Defining the API contract with Protocol Buffers
- Proto definitions in `proto/settla/v1/`
- Service definitions: SettlementService, TreasuryService, LedgerService, AuthService
- Message types: CreateTransferRequest, TransferResponse, QuoteResponse
- Code generation: `buf generate` → Go in `gen/`, TypeScript in `api/gateway/src/gen/`
- Why gRPC between gateway and server: type safety, streaming, HTTP/2 performance
- **Build:** Proto definitions and generated code

### Chapter 6.2: The gRPC Server
**Focus:** Implementing service methods in Go
- `api/grpc/server.go`: dependency injection of engine, ledger, treasury, router
- Request validation and error mapping (gRPC status codes)
- Tenant context propagation via gRPC metadata
- Streaming endpoints for real-time updates
- **Build:** Complete gRPC server implementation

### Chapter 6.3: The REST Gateway (Fastify/TypeScript)
**Focus:** Building a thin BFF that translates REST to gRPC
- Why a separate TypeScript gateway: ecosystem (OpenAPI, middleware), team expertise, thin layer
- Fastify 5 with ES modules
- Route structure: `/v1/quotes`, `/v1/transfers`, `/v1/treasury/*`, `/v1/ledger/*`
- Request transformation: REST JSON → gRPC protobuf → REST JSON
- OpenAPI documentation at `/docs`
- Health endpoint for Kubernetes probes
- **Build:** Gateway routes and gRPC client integration

### Chapter 6.4: Multi-Level Auth Caching
**Focus:** Making auth lookups fast enough for 5,000 TPS
- The auth flow: `Authorization: Bearer sk_live_xxx` → SHA-256 hash → tenant resolution
- Three-level cache:
  - L1: Local in-process LRU (30s TTL, 10K entries, ~100ns lookup, ~95% hit rate)
  - L2: Redis (5min TTL, ~0.5ms lookup)
  - L3: Database query (source of truth)
- Why local cache is critical: 5,000 TPS × Redis round-trip = unacceptable latency
- Cache invalidation strategy: TTL-based (no active invalidation needed for auth)
- **Build:** `cache/local.go`, auth plugin, and cache integration

### Chapter 6.5: Rate Limiting and Load Shedding
**Focus:** Protecting the system from traffic spikes and abuse
- Per-tenant rate limiting: sliding window algorithm
- Local counters synced to Redis every 5 seconds (distributed state)
- Load shedding: max concurrent requests, target latency, adaptive limit
- Graceful drain: SIGTERM handling for zero-downtime deployments
- Why rate limiting is per-tenant (not global): one tenant's spike shouldn't affect others
- **Build:** `cache/rate_limiter.go` and load shedding middleware

### Chapter 6.6: gRPC Connection Pooling
**Focus:** Eliminating per-request connection overhead
- The problem: creating a new gRPC connection per request adds TCP handshake overhead
- Solution: pool of ~50 persistent connections with round-robin selection
- Connection health checking and automatic reconnection
- Why 50 connections: math based on expected TPS and connection capacity
- **Build:** gRPC connection pool in the gateway

---

## Module 7: Operations — Running a Financial System

*Building the system is half the battle. Operating it is the other half. This module covers the systems that keep the settlement engine healthy.*

### Chapter 7.1: Reconciliation — Automated Consistency Checking
**Focus:** 5 checks that catch discrepancies before they become incidents
- **Check 1: Treasury-Ledger Balance** — Do treasury positions match ledger balances?
- **Check 2: Transfer State Validity** — Are any transfers in impossible states?
- **Check 3: Outbox Health** — Are any outbox entries stuck (unpublished for >5 minutes)?
- **Check 4: Provider Transaction Consistency** — Do provider records match transfer states?
- **Check 5: Daily Volume Tracking** — Do computed volumes match expected ranges?
- `CheckResult`: status (pass/warn/fail), details, discrepancies found
- `ReconciliationReport`: aggregated results with timestamp
- Feature-gating via `FeatureFlagChecker`
- **Build:** `core/reconciliation/reconciler.go` with all 5 checks

### Chapter 7.2: Compensation — Handling Partial Failures
**Focus:** What happens when a transfer partially succeeds
- Scenario: on-ramp succeeds, blockchain send fails → customer's money is stuck
- Four compensation strategies:
  - `SIMPLE_REFUND`: Return fiat to sender (most common)
  - `REVERSE_ONRAMP`: Reverse the on-ramp on-chain
  - `CREDIT_STABLECOIN`: Send stablecoin directly to recipient
  - `MANUAL_REVIEW`: Escalate to ops team (last resort)
- `CompensationRecord`: tracks steps completed/failed, FX loss on swaps
- Strategy selection logic: based on which step failed and what's reversible
- **Build:** `core/compensation/executor.go` with all strategies

### Chapter 7.3: Stuck-Transfer Recovery
**Focus:** Automatically detecting and recovering stalled transfers
- The detector runs every 60 seconds
- Per-status thresholds (configurable):
  - FUNDED: warn 5m, recover 10m, escalate 30m
  - ON_RAMPING: warn 10m, recover 15m, escalate 60m
  - SETTLING: warn 5m, recover 10m, escalate 30m
- Recovery action: re-publish stalled intents via engine
- Escalation: create `manual_reviews` record for human intervention
- Why automatic recovery is critical: at 580 TPS, even 0.01% stuck = 5,000 transfers/day need attention
- **Build:** `core/recovery/detector.go` with detection, recovery, and escalation

### Chapter 7.4: Net Settlement — Daily Position Calculation
**Focus:** Reducing N individual transfers to 1 net position per corridor
- The scheduler runs daily at 00:30 UTC
- For each NET_SETTLEMENT tenant:
  1. Aggregate all completed transfers in the period
  2. Group by corridor (GBP→NGN, EUR→USDT, etc.)
  3. Compute net position per currency
  4. Generate `SettlementInstructions` ("Fincra owes Settla 1.35B NGN")
- Overdue payment escalation:
  - 3 days: send reminder
  - 5 days: send warning
  - 7 days: suspend tenant
- Settlement cycle status: pending → approved → settled → overdue
- **Build:** `core/settlement/scheduler.go` with aggregation and escalation

### Chapter 7.5: Database Maintenance at Scale
**Focus:** Keeping the database healthy at 50M+ rows/day
- Partition management: create future partitions ahead, drop old ones
- Monthly partitions for transfers (6 months + default)
- Daily partitions for outbox (48h retention, then DROP TABLE)
- Vacuum management: ANALYZE scheduling, dead tuple monitoring
- Capacity monitoring: table sizes, index bloat, connection counts
- Why DELETE is forbidden for bulk data removal (table scan + WAL bloat)
- **Build:** `core/maintenance/partition_manager.go` and `vacuum_manager.go`

---

## Module 8: Production Readiness — Proving It Works

*A system isn't production-ready until you've proven it handles the load, recovers from failures, and you can observe its behavior.*

### Chapter 8.1: Component Benchmarks
**Focus:** Proving individual components meet throughput targets
- Go benchmarking: `go test -bench=Benchmark ./...`
- Key benchmarks:
  - Engine: CreateTransfer throughput and latency
  - Treasury: Reserve/Release CAS operations (target: <1µs)
  - Router: Route scoring (target: <5ms)
  - Cache: local lookup (target: <200ns)
  - Ledger: batch write throughput
- Results format: `bench-results.txt` with BenchmarkXxx ns/op
- 76 benchmark targets (all must pass)
- **Build:** Comprehensive benchmark suite across all modules

### Chapter 8.2: Load Testing — Proving the System Scales
**Focus:** End-to-end load testing with realistic traffic patterns
- Custom Go load test harness (not k6 — tighter integration with domain)
- Test phases: ramp-up (30s) → sustained (configurable) → drain (60s) → verify
- Scenarios:
  - Peak load: 5,000 TPS for 10 minutes
  - Sustained: 600 TPS for 30 minutes
  - Burst recovery: ramp 600 → 8,000 → 600 TPS
  - Single-tenant flood: 3,000 TPS one tenant
  - Multi-tenant scale: 50 tenants × 100 TPS each
- Metrics: transfer count, success rate, latency percentiles (p50, p95, p99)
- Verification phase: check DB consistency after drain
- Error rate threshold: max 1%
- **Build:** `tests/loadtest/` with all scenarios

### Chapter 8.3: Chaos Engineering — Proving Recovery
**Focus:** Injecting failures and verifying the system recovers
- Failure scenarios:
  - Network partitions (NATS, Redis, Postgres disconnections)
  - Provider failures (timeout, error, partial response)
  - Blockchain delays (confirmation takes 10x longer than expected)
  - Database slowness (queries take 5-10x longer)
  - Node crashes (kill worker processes mid-operation)
- For each scenario: inject failure → verify degraded behavior → remove failure → verify recovery
- Key assertions: no data loss, no double-execution, eventual consistency
- **Build:** `tests/chaos/` with all failure scenarios

### Chapter 8.4: Observability — Seeing Inside the System
**Focus:** Structured logging, metrics, dashboards, and alerting
- **Structured logging**: `slog` (Go) + `pino` (TypeScript)
  - Standard fields: `tenant_id`, `transfer_id`, `request_id`, `duration_ms`
- **Prometheus metrics**:
  - `settlement_transfers_total` (counter, by status)
  - `settla_ledger_tb_write_latency_seconds` (histogram)
  - `settla_treasury_reserve_latency_histogram`
  - `settla_outbox_relay_publish_total` (counter)
  - `settla_engine_state_transition_total` (counter, by from/to)
- **Grafana dashboards**: bank-deposit-health, deposit-health, system overview
- **SLI-based alerts**: latency, error rate, throughput breaches
- **Build:** `observability/metrics.go`, Prometheus config, Grafana dashboards

### Chapter 8.5: Deployment — Docker, Kubernetes, and Production Config
**Focus:** Deploying the complete system for production
- Docker Compose for local development (all services + infrastructure)
- Multi-stage Dockerfiles for Go and TypeScript services
- Kubernetes manifests:
  - settla-server: 6+ replicas, resource limits, readiness probes
  - settla-node: 8+ instances, affinity rules
  - gateway: 4+ replicas, HPA
  - PgBouncer: connection pooling sidecars
- Infrastructure: TigerBeetle, PostgreSQL × 3, NATS JetStream, Redis
- Tyk API Gateway configuration
- PgBouncer connection pooling configuration
- **Build:** Complete deployment manifests

### Chapter 8.6: Integration Testing — End-to-End Proof
**Focus:** Tests that exercise the complete system
- Tenant isolation tests: verify no cross-tenant data leakage
- Concurrency tests: parallel transfers with shared treasury positions
- Corridor tests: end-to-end transfer through all settlement steps
- Database consistency assertions after each test
- Transfer lifecycle validation (every state transition)
- **Build:** `tests/integration/` with comprehensive E2E tests

---

## Standout Features & Recommendations

### 1. Interactive Architecture Explorer (Web App)
Build an interactive web visualization that lets learners:
- Click on any component (engine, ledger, treasury, worker) to see its code
- Trace a transfer through the entire system in real-time
- Toggle failure modes and see how the system compensates
- View the outbox entries, NATS messages, and state transitions for any transfer
- **Why it stands out:** No other course lets you SEE the system working in real-time

### 2. "Break It to Understand It" Exercises
Each chapter includes a "break it" exercise:
- Remove the balanced posting check and see what happens
- Use float64 instead of decimal for money and observe rounding errors
- Bypass the outbox and call providers directly — then crash mid-operation
- Remove tenant_id filters and demonstrate cross-tenant data leakage
- Use mutex instead of CAS in treasury and benchmark the difference
- **Why it stands out:** Learning what goes wrong teaches more than learning what goes right

### 3. Capacity Planning Calculator
A spreadsheet/tool that students customize with their own requirements:
- Input: transactions/day, average entry lines per transfer, peak multiplier
- Output: required TPS, ledger writes/sec, treasury ops/sec, partition schedule
- Shows which "standard" patterns break at their scale and which patterns to adopt
- **Why it stands out:** Students leave with a tool they can use for their own projects

### 4. Production Incident Simulations
Scripted scenarios based on real fintech incidents:
- "A tenant's transfers are completing but the ledger balance doesn't match treasury" → reconciliation
- "Provider X is returning 500s but transfers keep being sent to it" → circuit breaker investigation
- "The outbox table is growing and relay throughput is dropping" → partition maintenance
- "A transfer has been in ON_RAMPING state for 2 hours" → stuck-transfer recovery
- **Why it stands out:** Bridges the gap between "I built it" and "I can operate it"

### 5. Design Decision Journal
A companion document where each architectural decision is captured as an ADR (Architecture Decision Record):
- **Decision:** Use TigerBeetle for ledger writes
- **Context:** 15K-25K writes/sec at peak, Postgres maxes out at ~5K
- **Alternatives considered:** Postgres with batch inserts, CockroachDB, custom WAL
- **Consequences:** Need sync consumer, two data stores to maintain, operational complexity
- **Why it stands out:** Teaches the *thinking process*, not just the solution

### 6. Multi-Language Comparison Chapters
For key components, show equivalent implementations in:
- Go (primary)
- Rust (for performance-critical learners)
- TypeScript (for full-stack learners)
- This highlights why certain languages shine for certain components
- **Why it stands out:** Broadens appeal beyond Go-only engineers

### 7. Compliance & Regulation Module (Bonus)
A bonus module covering:
- KYB/KYC integration points in the system
- Transaction monitoring and suspicious activity detection
- Audit trail requirements (why the ledger is append-only)
- Data retention policies (why partition TTLs matter)
- Cross-border regulatory considerations
- **Why it stands out:** Real fintech systems must be compliant — most courses ignore this entirely

### 8. "Fork and Customize" Challenges
End-of-module challenges where students extend the system:
- Add a new provider adapter (e.g., Stripe for on-ramp)
- Implement a new settlement corridor (e.g., EUR → KES)
- Build a new reconciliation check
- Add a new compensation strategy
- Create a custom Grafana dashboard
- **Why it stands out:** Students practice extending a real codebase, not writing from scratch

### 9. Live System Dashboard
A hosted demo environment where students can:
- Send transfers through the API and watch them flow through the system
- View the ops dashboard with real metrics
- Trigger chaos scenarios and observe recovery
- Compare their local builds against the reference implementation
- **Why it stands out:** Immediate visual feedback accelerates learning

### 10. System Design Interview Prep Pack
Map each module back to common interview questions:
- "Design a payment system" → Modules 1-3
- "Design a ledger" → Module 2
- "How do you handle distributed transactions?" → Chapter 3.1 (outbox pattern)
- "How do you scale writes?" → Chapters 2.2-2.4 (TigerBeetle + batching)
- "How do you handle partial failures?" → Chapter 7.2 (compensation)
- Sample interview answers with architecture diagrams drawn from the actual system
- **Why it stands out:** Direct ROI for career advancement

---

## Pricing & Packaging Recommendations

| Tier | Contents | Price Point |
|------|----------|-------------|
| **Core** | All 8 modules, exercises, source code | $199-299 |
| **Pro** | Core + incident simulations, ADR journal, interview prep | $399-499 |
| **Enterprise** | Pro + team license (5 seats), office hours, private Discord | $1,499-2,499 |

---

## Delivery Format Options

1. **Written Course (Primary):** Long-form chapters with code blocks, diagrams, and exercises. Each chapter is a self-contained lesson with working code. Think "The Book" (Rust Programming Language) quality.

2. **Video Companion:** 5-15 minute videos per chapter covering the "why" and live coding of key sections. Total: ~20-30 hours.

3. **GitHub Repository:** Complete source code with branches per chapter. Students can `git checkout chapter-3.2` to see the code at any point.

4. **Discord Community:** Peer discussion, code review, and Q&A.

---

## Suggested Course Timeline

| Week | Module | Effort |
|------|--------|--------|
| 1 | Module 0: The Money Domain | 8-10 hours |
| 2-3 | Module 1: Foundations | 10-15 hours |
| 4-5 | Module 2: The Ledger | 12-18 hours |
| 6-7 | Module 3: The Engine | 12-18 hours |
| 8-9 | Module 4: Treasury & Routing | 10-15 hours |
| 10-11 | Module 5: Async Workers | 12-18 hours |
| 12-13 | Module 6: API Layer | 10-15 hours |
| 14-15 | Module 7: Operations | 10-15 hours |
| 16-17 | Module 8: Production Readiness | 12-18 hours |
| 18-19 | Module 9: Deposits & Payments | 8-12 hours |
| 20 | Module 10: Security & Compliance | 10-15 hours |
| **Total** | | **~115-170 hours** |

---

## Competitive Positioning

| Feature | Settla Course | Typical System Design Course |
|---------|---------------|------------------------------|
| Working codebase | Complete, production-grade | Pseudocode or toy examples |
| Scale proof | Load tests at 5K TPS, benchmarks | "This would scale because..." |
| Domain depth | Real fintech (ledger, treasury, settlement, deposits, payment links) | Generic (URL shortener, chat) |
| Financial domain education | Module 0: payment rails, stablecoins, FX, regulation | Assumes domain knowledge |
| Failure handling | Compensation, recovery, reconciliation | "Use retries" |
| Multi-tenancy | Full implementation with auth, fees, isolation | Mentioned but not built |
| Security | PII encryption, HMAC auth, webhook signatures, secrets management | "Use JWT" |
| Compliance | KYB, AML, Travel Rule, GDPR crypto-shred, regulatory landscape | Not mentioned |
| Observability | Prometheus, Grafana, OpenTelemetry, structured logging | "Add monitoring" |
| Deployment | Docker, K8s, multi-machine clusters, connection pooling | "Deploy to cloud" |
| State machine rigor | Explicit transitions, outbox pattern | Ad-hoc status fields |
| Financial precision | Decimal-only, balanced postings, audit trail | float64 everywhere |
| Deposit flows | Crypto on-chain detection, bank virtual accounts, payment links | Not covered |