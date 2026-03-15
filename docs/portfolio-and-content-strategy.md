# Settla — Portfolio Potential, Content Strategy & Demo Plan

> A strategic analysis of Settla's value as a portfolio project, a technical article series plan,
> and a robust demo strategy to prove real-world applicability.

---

## Part 1: Portfolio Potential Assessment

### Is This a Good Portfolio Project?

**Yes — this is an exceptional portfolio project.** It sits in a rare intersection that most
portfolio projects never reach: real financial infrastructure, distributed systems engineering,
and blockchain integration, all at a scale that demands non-trivial architectural decisions.

### Why It Stands Out

Most portfolio projects are CRUD apps with a framework. Settla is none of that:

| Dimension | Typical Portfolio Project | Settla |
|-----------|--------------------------|--------|
| Architecture | Monolith or microservices (by tutorial) | Modular monolith with extraction-ready interfaces, 18 ADRs justifying every decision |
| Scale thinking | "It works on localhost" | Designed for 50M txn/day with measured benchmarks proving capacity |
| Data layer | Single Postgres | TigerBeetle (1M+ TPS) + 3 Postgres DBs + PgBouncer + Redis + NATS JetStream |
| Consistency | Hope and prayers | Transactional outbox, CAS loops, double-entry ledger, 5 reconciliation checks |
| Testing | Unit tests maybe | Unit + integration + benchmark + load (5K TPS) + chaos + soak tests |
| Blockchain | "I deployed a smart contract" | 4 testnets (Tron, Ethereum, Solana, Base), real wallet management, circuit breakers |
| Multi-tenancy | Single user | Full tenant isolation with per-tenant fees, limits, API keys, webhooks |
| DevOps | Dockerfile | Docker Compose (14 services), K8s manifests (48 YAMLs), CI/CD, Grafana dashboards |
| Documentation | README | 18 ADRs, architecture doc, SLA, security, DR, cost estimation, compliance |
| Codebase size | 500-2000 LOC | ~69,000 LOC across Go + TypeScript + Vue |

### What It Demonstrates to Employers/Investors

1. **Systems thinking** — Not just "can you code?" but "can you design systems that handle
   failure, scale, and money correctly?"

2. **Financial domain expertise** — Double-entry accounting, FX loss calculation, net settlement,
   compensation strategies, fee schedules. This is real fintech, not a toy.

3. **Distributed systems mastery** — Transactional outbox, event-driven sagas, CAS loops,
   CQRS, partitioned messaging, circuit breakers. Each pattern exists because the math demands it.

4. **Production readiness mindset** — SLAs, runbooks, disaster recovery, cost estimation,
   security hardening. This isn't a demo; it's a system designed to run in production.

5. **Polyglot capability** — Go (high-throughput core), TypeScript (API gateway, webhooks),
   Vue/Nuxt (dashboard), Protocol Buffers (cross-language contracts), SQL (SQLC-generated).

6. **Blockchain integration** — Not theoretical. Real testnet transactions on 4 chains with
   wallet management, RPC failover, and explorer URL generation.

### Target Audiences

| Audience | What They'd Notice |
|----------|--------------------|
| **Senior/Staff engineer hiring managers** | The 18 ADRs. Every decision is threshold-driven ("at X TPS, pattern Y breaks"). This is how staff engineers think. |
| **Fintech companies** | Domain accuracy: double-entry ledger, multi-tenant fee schedules, compliance docs, settlement flows. This person understands the business. |
| **Infrastructure/Platform teams** | K8s manifests, observability stack, chaos testing, DR runbooks. Production-grade thinking. |
| **Blockchain/Web3 companies** | Real testnet integration (not just Hardhat), multi-chain support, wallet key management, circuit breakers on RPCs. |
| **VCs / Technical due diligence** | Cost estimation ($0.000004/txn), SLA commitments, scaling math. This person can build AND reason about unit economics. |

### Potential Weaknesses to Address

| Gap | Mitigation |
|-----|------------|
| No production traffic | Load test + chaos test results with real numbers close this gap |
| Mock providers (no real banking APIs) | Explicitly frame as "testnet-grade" — the provider interface is clean, real providers plug in |
| Single developer | Frame as a strength: "I built the entire system solo, including all infrastructure" |
| No CI running publicly | Set up GitHub Actions with the existing pipeline definition |

---

## Part 2: Technical Article Series

### Content Strategy: "Building Settlement Infrastructure at Scale"

The goal is not to write a tutorial. The goal is to write **engineering essays** that demonstrate
depth of thinking. Each article should:

1. Start with a **real problem** at scale (not "here's how to use library X")
2. Show the **math** that drives the decision (thresholds, not opinions)
3. Present the **trade-offs** considered (what you rejected and why)
4. Include **measured results** (benchmarks, not estimates)
5. End with **production implications** (what breaks next, what you'd change)

### Article Categorization

```
Category 1: FINANCIAL INFRASTRUCTURE (domain depth)
Category 2: DISTRIBUTED SYSTEMS (architecture depth)
Category 3: HIGH-THROUGHPUT ENGINEERING (performance depth)
Category 4: BLOCKCHAIN INTEGRATION (Web3 depth)
Category 5: PRODUCTION READINESS (operational depth)
Category 6: SYSTEM DESIGN (big-picture thinking)
```

### Full Article List (22 Topics)

---

#### CATEGORY 1: FINANCIAL INFRASTRUCTURE (5 articles)

**1.1 — "Double-Entry Accounting at 25,000 Writes/Second"**
- Problem: Every financial transaction needs balanced debits and credits. At 50M txn/day,
  that's 200-250M entry lines/day. Single Postgres can't keep up.
- Solution: TigerBeetle as write authority (1M+ TPS), Postgres as read model (CQRS)
- Key content: Why TigerBeetle over Postgres, write batching (5-50ms windows), sync consumer,
  idempotent posting, balance snapshots
- Hot angle: "Why your fintech's ledger will break at 10K TPS and how to fix it"
- Depth: Show the batch write code, benchmark results, sync lag metrics
- **Target: Fintech engineering blogs, HackerNews**

**1.2 — "Multi-Tenant Fee Schedules: When Every Fintech Pays Differently"**
- Problem: B2B settlement means each client (Lemfi, Fincra) has negotiated different fee
  structures. Basis points, minimums, per-corridor rates.
- Solution: Per-tenant fee schedules stored in DB, applied by CoreRouterAdapter at routing time
- Key content: Fee calculation (basis points vs flat), FX spread modeling, why fees must be
  applied before routing (not after), tenant-scoped idempotency
- Hot angle: "The hidden complexity of B2B pricing in payment infrastructure"
- **Target: Fintech product/engineering teams**

**1.3 — "Compensation Strategies When Cross-Border Transfers Partially Fail"**
- Problem: GBP→NGN transfer. On-ramp succeeded (fiat→USDT), but off-ramp failed (USDT→NGN).
  You can't just "reverse" — the FX rate has moved.
- Solution: Compensation engine with 4 strategies (simple refund, reverse on-ramp with FX loss,
  stablecoin credit, manual review)
- Key content: FX loss calculation (£2,847 → $3,582 at 1.2581, reversal at 1.2656 = £17 loss),
  who bears the loss, audit trail, outbox-based execution
- Hot angle: "What happens when a cross-border payment half-succeeds"
- **Target: Payments engineering, fintech risk teams**

**1.4 — "Net Settlement: Why B2B Fintechs Don't Settle Per-Transaction"**
- Problem: Settling every transaction individually is expensive. Enterprise fintechs settle
  daily — net all flows, calculate what's owed, settle the difference.
- Solution: Daily netting calculator, per-corridor aggregation, receivables/payables ledger
  entries, payment tracking with overdue escalation
- Key content: Netting math, settlement windows, credit risk, ledger entries for receivables
- Hot angle: "How Stripe, Wise, and settlement networks actually move money"
- **Target: Fintech engineering, payment ops**

**1.5 — "Reconciliation at Scale: 5 Automated Checks That Prevent Financial Discrepancies"**
- Problem: At 50M txn/day, even 0.001% discrepancy = 500 wrong transactions. Manual
  reconciliation is impossible.
- Solution: 5 automated checks (ledger, provider, treasury, blockchain, cross-tenant) running
  on schedule with auto-correction via outbox
- Key content: TB vs PG balance drift, missed webhook detection, treasury position drift,
  on-chain vs expected balance, cross-tenant contamination check
- Hot angle: "How to build a reconciliation engine that catches errors before your clients do"
- **Target: Fintech ops, compliance engineering**

---

#### CATEGORY 2: DISTRIBUTED SYSTEMS (5 articles)

**2.1 — "The Transactional Outbox Pattern: Eliminating Dual-Write Bugs at 50M Transactions/Day"**
- Problem: Engine updates DB + publishes to NATS. If NATS publish fails after DB commit,
  you have an orphaned state change. At 50M txn/day with 0.01% failure = 5,000 lost events/day.
- Solution: Single DB transaction writes state change + outbox entry. Relay polls outbox →
  publishes to NATS. If relay crashes, it resumes from DB.
- Key content: Outbox schema (daily partitions for cleanup), relay poll loop (50ms),
  NATS Msg-Id dedup, partition DROP vs DELETE (ADR-018), wire format challenges
- Hot angle: "Stop publishing events from your application code"
- Depth: Show the actual SQL transaction, relay code, measured throughput
- **Target: HackerNews, distributed systems blogs, Backend Engineering Weekly**

**2.2 — "Pure State Machines: Why Your Engine Should Never Make Network Calls"**
- Problem: Settlement engine calls treasury, ledger, providers, blockchain directly.
  Any failure mid-flow leaves transfer in inconsistent state. Testing requires mocking 6 services.
- Solution: Engine writes intents to outbox. Workers execute side effects. Engine is
  deterministic — same input always produces same output.
- Key content: Before/after comparison, 32 valid state transitions, outbox entry payloads,
  worker routing, recovery from any intermediate state
- Hot angle: "The single refactor that made our payment engine 10x more reliable"
- **Target: Software architecture blogs, DDD community**

**2.3 — "Event-Driven Sagas with NATS JetStream: Partitioned, Ordered, Exactly-Once"**
- Problem: 580 events/sec needs parallel processing, but transfers for the same tenant
  must be processed in order. Standard pub/sub gives neither guarantee.
- Solution: 7 JetStream streams, 8 partitions by tenant hash, WorkQueue policy,
  message dedup, dead letter after 5 retries with exponential backoff
- Key content: Partition key design, consumer group semantics, exactly-once via Msg-Id,
  backpressure per partition, 5-step retry with backoff (1s, 5s, 30s, 2min, 10min)
- Hot angle: "NATS JetStream vs Kafka for payment event processing"
- **Target: NATS community, event-driven architecture blogs**

**2.4 — "Check-Before-Call: Preventing Double-Execution in Idempotent Workers"**
- Problem: NATS redelivers after timeout. Provider worker receives "send payment" twice.
  Without protection, you send the payment twice and lose money.
- Solution: Check-before-call pattern: (1) check provider_transactions for existing result,
  (2) if completed → skip, (3) if pending → wait, (4) if not found → execute with idempotency
  reference, (5) record result
- Key content: Provider transaction store, status state machine, blockchain tx dedup,
  idempotency key propagation, what happens when check itself fails
- Hot angle: "The pattern that prevents double-charging in payment systems"
- **Target: Payment engineering, distributed systems**

**2.5 — "Modular Monolith: One Binary, Zero Coupling, Extraction-Ready"**
- Problem: Microservices add network hops, deployment complexity, and distributed transaction
  headaches. Monolith couples everything. Neither works at scale.
- Solution: Single Go binary with strict interface boundaries. All cross-module dependencies
  flow through `domain/` interfaces. Compile-time checks enforce boundaries.
  `go list -f '{{join .Imports "\n"}}' ./core/...` must show only domain/ and stdlib.
- Key content: Module boundary enforcement, interface segregation, why core/ can't import
  ledger/, extraction path (swap constructor, add gRPC), measured import graph
- Hot angle: "Why we chose a modular monolith over microservices for payment infrastructure"
- **Target: Architecture community, Golang blogs, HackerNews**

---

#### CATEGORY 3: HIGH-THROUGHPUT ENGINEERING (4 articles)

**3.1 — "Lock-Free Treasury Reservations: CAS Loops at Nanosecond Latency"**
- Problem: 5,000 concurrent reservation requests on ~50 hot treasury positions.
  `SELECT FOR UPDATE` causes 5% deadlock rate at this contention level.
- Solution: In-memory atomic int64 counters with Compare-and-Swap loops. Reserve/Release
  never touch the database. Background goroutine flushes dirty positions every 100ms.
- Key content: micro-unit fixed-point arithmetic (int64, 6dp precision, $9.2T max),
  CAS loop implementation, dirty flag, flush goroutine, crash recovery from DB
- Benchmark: <2 microseconds per reservation, >500K/sec throughput
- Hot angle: "How we made treasury reservations 1000x faster by removing the database"
- **Target: Performance engineering, Go community, HackerNews**

**3.2 — "Three-Level Caching for 5,000 Auth Lookups Per Second"**
- Problem: Every API request needs tenant resolution. At 5K TPS, that's 5K Redis calls/sec
  minimum, plus DB fallback pressure.
- Solution: L1 local in-process LRU (30s TTL, ~107ns), L2 Redis (5min TTL, ~0.5ms),
  L3 Postgres (source of truth). 99.9% of lookups hit L1.
- Key content: Cache invalidation strategy (TTL-based, no active invalidation needed for auth),
  stampede protection, local cache benchmarks, why not just Redis
- Benchmark: 107ns measured auth lookup (local cache hit)
- Hot angle: "107 nanoseconds: the auth lookup that handles 5,000 requests per second"
- **Target: Backend performance, caching patterns**

**3.3 — "Write Batching: Turning 25,000 Individual INSERTs into Bulk Operations"**
- Problem: TigerBeetle handles 1M+ TPS but individual writes from 5K concurrent transfers
  waste round-trips. Postgres read-side needs 25K INSERTs/sec for entry_lines.
- Solution: Write-ahead batching — collect postings for 5-50ms, flush as single bulk operation.
  Configurable batch size and time window.
- Key content: Batch collector design, timer vs size threshold, back-pressure when batch full,
  error handling (partial batch failure), benchmark comparison (individual vs batched)
- Hot angle: "The batching pattern that turned our database bottleneck into headroom"
- **Target: Database performance, Go community**

**3.4 — "PgBouncer, Partitioning, and the Art of Not Running Out of Connections"**
- Problem: 6+ server replicas × 100 connections each = 600 connections. Postgres max_connections
  defaults to 100. Monthly tables with 50M rows each need partition pruning.
- Solution: 3 PgBouncer instances (one per bounded context DB), transaction-mode pooling,
  monthly partitions (6 months ahead + default), daily partitions for outbox (DROP, not DELETE)
- Key content: Connection math, PgBouncer config, partition creation schedule,
  why DROP is O(1) vs DELETE O(N), default partition as safety net
- Hot angle: "How to handle 600 connections and 1.5B rows/month in Postgres"
- **Target: Postgres community, database engineering**

---

#### CATEGORY 4: BLOCKCHAIN INTEGRATION (3 articles)

**4.1 — "Multi-Chain Stablecoin Settlement: Tron, Ethereum, Solana, and Base"**
- Problem: Different stablecoins live on different chains. USDT is cheapest on Tron,
  USDC works on Ethereum/Base/Solana. You need a unified interface across all of them.
- Solution: BlockchainClient interface with per-chain implementations, registry with
  circuit breaker, RPC failover (3+ nodes per chain), explorer URL generation
- Key content: Chain-specific quirks (Tron's energy model, Solana's ATA creation,
  Ethereum's gas estimation), failover design, how the router picks the cheapest chain
- Hot angle: "Building a multi-chain stablecoin bridge that actually works"
- **Target: Web3 engineering, DeFi infrastructure, crypto Twitter**

**4.2 — "HD Wallets, Key Encryption, and Why Private Keys Never Touch Logs"**
- Problem: Managing hot wallets across 4 chains. Keys must be secure at rest,
  derived deterministically, and never exposed in logs or error messages.
- Solution: BIP-44 HD derivation per chain, AES-256-GCM encryption at rest,
  system wallets vs tenant wallets, faucet integration for testnet funding
- Key content: Key derivation paths, encryption implementation, log scrubbing,
  wallet hierarchy (system/hot vs tenant/chain), faucet automation
- Hot angle: "Secure wallet management for payment infrastructure (not DeFi)"
- **Target: Web3 security, blockchain engineering**

**4.3 — "Smart Routing: Choosing the Cheapest Stablecoin Rail in Real-Time"**
- Problem: GBP→NGN can go through USDT-on-Tron (cheap, fast) or USDC-on-Ethereum (expensive,
  slow). The router must score routes by cost, speed, liquidity, and reliability.
- Solution: Weighted scoring (cost 40%, speed 30%, liquidity 20%, reliability 10%),
  per-tenant fee application, liquidity filtering, route caching
- Key content: Scoring algorithm, why weights matter, corridor-specific routing,
  FX oracle with jitter for realistic pricing, fallback when preferred route exhausted
- Hot angle: "How payment routers decide which blockchain to use"
- **Target: Payments engineering, Web3 infrastructure**

---

#### CATEGORY 5: PRODUCTION READINESS (3 articles)

**5.1 — "Chaos Testing a Payment System: What Breaks When Everything Fails"**
- Problem: In production, components fail. TigerBeetle restarts, Postgres pauses,
  NATS loses messages, Redis goes down. Does money get lost?
- Solution: 7 chaos scenarios with post-failure verification: ledger balanced,
  treasury consistent, zero duplicates, zero lost transfers
- Key content: Each failure scenario, what happened, how the system recovered,
  measured recovery times, what surprised us
- Hot angle: "We killed every component in our payment system. Here's what survived."
- **Target: HackerNews, SRE community, chaos engineering**

**5.2 — "Stuck Transfer Detection: Automated Recovery at 50M Transactions/Day"**
- Problem: At scale, transfers WILL get stuck. Missed webhooks, provider outages,
  network partitions. Manual intervention doesn't scale.
- Solution: Stuck detector runs every 60s, per-state time thresholds (warn/recover/escalate),
  recovery actions (query provider status, republish outbox intents), escalation to manual review
- Key content: Threshold design, recovery strategies per state, idempotent recovery,
  manual review queue, alert escalation
- Hot angle: "How we automatically recover stuck payments without human intervention"
- **Target: Payments ops, SRE, fintech engineering**

**5.3 — "The $0.000004 Transaction: Cost Engineering for Payment Infrastructure"**
- Problem: Payment infrastructure must be cheaper than the fees you charge. At $0.000004/txn,
  Settla's infrastructure cost is negligible compared to provider fees.
- Solution: TigerBeetle (free, open-source) instead of commercial ledger, PgBouncer to reduce
  DB instances, local caching to eliminate Redis pressure, NATS instead of Kafka
- Key content: Component cost breakdown, scaling math (sub-linear cost growth),
  RI/spot optimization, comparison to commercial alternatives
- Hot angle: "We built payment infrastructure that costs $0.000004 per transaction"
- **Target: Fintech founders, engineering leadership, HackerNews**

---

#### CATEGORY 6: SYSTEM DESIGN (2 articles)

**6.1 — "Designing B2B Stablecoin Settlement Infrastructure from Scratch"**
- The capstone article. Full system design walkthrough.
- Problem statement → requirements (50M txn/day, multi-tenant, multi-chain) →
  architecture decisions → trade-offs → measured results
- Covers: Why modular monolith, why TigerBeetle, why outbox, why NATS, why 3 databases,
  why CAS loops, why not microservices, why not Kafka
- Includes architecture diagram, data flow, module dependency graph
- Hot angle: "A complete system design for processing $10B/day in cross-border payments"
- **Target: System design community, HackerNews, engineering blogs, conference talks**

**6.2 — "18 Architecture Decision Records: How Threshold-Driven Thinking Shapes Infrastructure"**
- Meta-article about the ADR process itself. How each decision was driven by a measurable
  threshold, not opinion.
- Examples: "At >10K writes/sec, single Postgres breaks → TigerBeetle."
  "At 5% deadlock rate on SELECT FOR UPDATE → in-memory CAS."
  "At 0.01% dual-write failure × 50M txn = 5,000 lost events → outbox."
- Hot angle: "Stop arguing about architecture. Start measuring thresholds."
- **Target: Architecture community, engineering leadership, conference talks**

---

### Publishing Strategy

#### Platform Priority

| Platform | Best For | Frequency |
|----------|----------|-----------|
| **Personal blog / dev.to** | All articles (canonical URL) | 2/month |
| **HackerNews** | Articles 2.1, 2.5, 3.1, 5.1, 5.3, 6.1, 6.2 | Submit when published |
| **Medium / Fintech publications** | Category 1 (fintech depth) | Cross-post |
| **Hashnode** | Categories 2-3 (distributed systems, performance) | Cross-post |
| **Twitter/X threads** | Key insights from each article (5-10 tweets) | With each publish |
| **LinkedIn** | Business-facing angle (5.3, 6.1) | 1/month |
| **Reddit r/golang, r/programming** | Go-specific articles (3.1, 2.5) | When relevant |
| **Conference talks** | Articles 6.1, 6.2, 5.1 | Apply to GopherCon, FinTech DevCon |

#### Suggested Publication Order

**Phase 1 — Hook articles (high virality, establish credibility):**
1. Article 6.1 — "Designing B2B Stablecoin Settlement Infrastructure" (the big picture)
2. Article 3.1 — "Lock-Free Treasury Reservations at Nanosecond Latency" (the wow factor)
3. Article 2.1 — "The Transactional Outbox Pattern at 50M Transactions/Day" (the depth)

**Phase 2 — Domain depth (establish fintech expertise):**
4. Article 1.1 — "Double-Entry Accounting at 25,000 Writes/Second"
5. Article 1.3 — "Compensation When Cross-Border Transfers Partially Fail"
6. Article 5.3 — "The $0.000004 Transaction"

**Phase 3 — Architecture depth (establish systems thinking):**
7. Article 2.2 — "Pure State Machines"
8. Article 2.5 — "Modular Monolith"
9. Article 6.2 — "18 ADRs: Threshold-Driven Architecture"

**Phase 4 — Blockchain + operations (complete the picture):**
10. Article 4.1 — "Multi-Chain Stablecoin Settlement"
11. Article 5.1 — "Chaos Testing a Payment System"
12. Remaining articles as appetite demands

---

### Hot Topics in Fintech Engineering Circles (2025-2026)

These are the topics fintech engineers, CTOs, and VCs are actively discussing.
Settla articles should position against these conversations:

| Hot Topic | Settla's Angle | Why It's Hot |
|-----------|---------------|--------------|
| **Stablecoin rails replacing SWIFT** | Settla IS this infrastructure | Circle/USDC, Tether dominance, regulatory clarity emerging |
| **Real-time cross-border payments** | Full E2E flow: fiat→stablecoin→fiat in minutes | ISO 20022 migration, instant payment schemes globally |
| **Embedded finance / BaaS** | Multi-tenant B2B API with per-tenant fees | Every fintech wants to become a platform |
| **TigerBeetle hype** | Real integration with benchmarks | TigerBeetle is the new hot database in fintech circles |
| **Outbox pattern / event-driven** | Production implementation with measured results | Microservices fatigue → back to monolith + events |
| **Africa fintech infrastructure** | GBP↔NGN corridor, Lemfi/Fincra as tenant examples | African fintech is the fastest-growing segment globally |
| **Cost of payment infrastructure** | $0.000004/txn breakdown | Fintech margins are shrinking; infra cost matters |
| **Blockchain for payments (not DeFi)** | Real testnet txs, not theoretical | "Practical blockchain" narrative gaining traction post-hype |
| **Multi-chain strategy** | 4 chains, smart routing by cost/speed | Chain fragmentation is a real problem for payment companies |
| **Operational resilience** | Chaos testing, stuck detector, reconciliation | UK FCA/PRA operational resilience rules (March 2025 deadline) |
| **Go for financial infrastructure** | Full system in Go with benchmarks | Go is becoming the default for payment backends |
| **NATS vs Kafka** | NATS JetStream at payment scale | NATS gaining mind-share as simpler alternative to Kafka |
| **Modular monolith renaissance** | 18 ADRs + extraction readiness | Shopify, Gusto, others publicly advocating modular monolith |
| **AI-assisted development** | 69K LOC system built with Claude Code | AI-assisted engineering is the meta-topic everywhere |

---

## Part 3: Robust Demo Plan

### Demo Goals

The demo must prove Settla is **real infrastructure**, not a toy. Every demo scenario should
show something that would break in a naive implementation.

### Demo Environment

```
Docker Compose (14 services):
  TigerBeetle | Postgres ×3 | PgBouncer ×3 | Redis | NATS |
  settla-server | settla-node | gateway | webhook | dashboard
```

### Demo Scenarios (12 Scenarios, 4 Categories)

---

#### Category A: Happy Path (prove it works)

**A1 — "Lemfi sends GBP to NGN"**
- Create quote (GBP→NGN, show FX rate + fee breakdown)
- Create transfer (show idempotency key)
- Watch transfer progress: CREATED → FUNDED → ON_RAMPING → SETTLING → OFF_RAMPING → COMPLETED
- Show: ledger entries (balanced), treasury position changes, blockchain explorer URL
- **What it proves:** Full saga works end-to-end, fees are correct, money balances

**A2 — "Fincra sends NGN to GBP (reverse corridor, different fees)"**
- Same flow, reverse direction
- Show: Fincra pays 25 bps (vs Lemfi's 40 bps)
- **What it proves:** Multi-tenant fee isolation, bi-directional corridors

**A3 — "100 concurrent transfers (no over-reservation)"**
- Fire 100 transfers simultaneously
- Show: treasury position decreases correctly (no double-spend)
- Show: all 100 complete with balanced ledger
- **What it proves:** CAS loops prevent over-reservation under contention

---

#### Category B: Failure & Recovery (prove it's reliable)

**B1 — "Off-ramp fails → automatic compensation"**
- Create transfer, on-ramp succeeds, off-ramp deliberately fails
- Show: compensation engine determines strategy (reverse on-ramp with FX loss)
- Show: FX loss calculation (e.g., £17 on £2,847 transfer)
- Show: audit trail in compensation_records
- **What it proves:** Partial failure doesn't lose money, compensation is auditable

**B2 — "Kill the worker mid-transfer → NATS redelivers → no duplicates"**
- Start a transfer, kill settla-node while ON_RAMPING
- Restart settla-node
- Show: transfer completes (NATS redelivered the intent)
- Show: provider was NOT called twice (check-before-call pattern)
- **What it proves:** Crash safety, exactly-once execution via check-before-call

**B3 — "Stuck transfer → automatic detection and recovery"**
- Start transfer, simulate provider webhook never arriving
- Wait for stuck detector (60s scan interval)
- Show: detector queries provider status, writes outbox entry, transfer advances
- **What it proves:** Self-healing at scale, no manual intervention needed

**B4 — "Outbox relay crash → restart → catches up"**
- Create 50 transfers, kill outbox relay mid-flight
- Show: transfers are stuck (outbox entries unpublished)
- Restart relay
- Show: relay catches up from DB, all transfers complete
- **What it proves:** Outbox pattern survives relay crashes (DB is source of truth)

---

#### Category C: Scale & Performance (prove it's fast)

**C1 — "1,000 TPS for 2 minutes (load test)"**
- Run loadtest-quick
- Show: live Grafana dashboard with throughput, latency percentiles, error rate
- Show: post-test verification (ledger balanced, treasury consistent, outbox drained)
- **What it proves:** Measured capacity, not theoretical claims

**C2 — "Treasury reservation benchmark: <2 microseconds"**
- Run `go test -bench=BenchmarkReserve ./treasury/...`
- Show: sub-microsecond per operation, >500K/sec
- Compare: vs `SELECT FOR UPDATE` (milliseconds, deadlocks)
- **What it proves:** The CAS loop design is 1000x faster than database locking

**C3 — "Auth cache: 107 nanoseconds"**
- Run `go test -bench=BenchmarkLocalCache ./cache/...`
- Show: 107ns per auth lookup (local cache hit)
- Math: 5,000 TPS × 200ns overhead = 1ms total, negligible
- **What it proves:** Three-level cache eliminates auth as a bottleneck

---

#### Category D: Tenant Isolation & Security (prove it's safe)

**D1 — "Lemfi can't see Fincra's transfers (404, not 403)"**
- Create transfer as Lemfi
- Try to read it with Fincra's API key → 404 (not 403, don't leak existence)
- **What it proves:** Tenant isolation at data layer, not just auth layer

**D2 — "Webhook HMAC verification"**
- Show: outbound webhook to tenant URL with HMAC-SHA256 signature
- Verify: signature matches using tenant's webhook secret
- Show: tampered payload → signature mismatch → rejection
- **What it proves:** Webhook integrity, tenant-specific secrets

---

### Demo Script Execution Plan

```
Total demo time: ~25 minutes

Setup (2 min):
  make docker-up && sleep 25 && make migrate-up && make db-seed

Act 1 — "It works" (5 min):
  A1: Lemfi GBP→NGN (show full saga, ledger entries, blockchain URL)
  A2: Fincra NGN→GBP (show different fees)

Act 2 — "It's fast" (5 min):
  C2: Treasury benchmark (sub-microsecond)
  C3: Auth cache benchmark (107ns)
  C1: 1,000 TPS load test with live Grafana (2 min run)

Act 3 — "It survives failure" (8 min):
  B1: Off-ramp failure + compensation
  B2: Worker crash + NATS redelivery
  B3: Stuck transfer + auto-recovery
  B4: Relay crash + catchup

Act 4 — "It's secure" (3 min):
  D1: Tenant isolation (404 not 403)
  A3: 100 concurrent transfers (no over-reservation)
  D2: Webhook HMAC verification

Closing (2 min):
  Show dashboard, Grafana, ADR list
  Recap: 50M txn/day design, $0.000004/txn, 18 ADRs, 69K LOC
```

### Demo Recording Tips

For a video demo or conference talk:

1. **Pre-warm Docker** — Have everything running before you start recording
2. **Split terminal** — Left: API calls (curl/httpie), Right: docker logs with grep
3. **Grafana on second monitor** — Show real-time metrics during load test
4. **Use httpie over curl** — Prettier output, colored JSON
5. **Script the chaos** — `docker stop settla-node` is more dramatic than describing it
6. **Show the code briefly** — Flash the CAS loop, the outbox transaction, the compensation
   strategy. Don't explain line-by-line; let the audience see it's real.
7. **End with numbers** — "69,000 lines of code. 18 architecture decisions. 50 million
   transactions per day. $0.000004 each."

---

## Part 4: Recommended Next Steps

### Immediate (this week)
1. Complete Milestone 8 load test consistency verification
2. Record a 5-minute demo video (scenarios A1, C1, B2)
3. Write Article 6.1 (the capstone system design article)

### Short-term (2-4 weeks)
4. Publish articles 6.1, 3.1, 2.1 (the hook articles)
5. Submit to HackerNews, share on Twitter/X
6. Record full 25-minute demo

### Medium-term (1-3 months)
7. Complete article series (2/month cadence)
8. Submit conference talk proposals (GopherCon, FinTech DevCon)
9. Open-source consideration (public repo, if desired)

### Long-term
10. Convert to real provider integrations (Flutterwave, Paystack APIs)
11. Mainnet deployment path
12. Commercial viability assessment
