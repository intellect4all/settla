# Settla Technical Brief

> Executive summary for technical evaluation. For full architecture details, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## What Is Settla

Settla is B2B stablecoin settlement infrastructure that enables fintechs to move money across borders in minutes instead of days. It converts fiat currency to stablecoins (USDT/USDC) on-chain, settles across blockchain networks (Ethereum, Tron, Base, Polygon, Arbitrum, Solana), and converts back to fiat at the destination -- all through a single API. The platform handles the full lifecycle: quoting, routing, treasury management, ledger accounting, blockchain monitoring, settlement netting, and webhook delivery, so fintechs can offer cross-border payments without building settlement infrastructure.

---

## Key Metrics

| Metric | Value |
|--------|-------|
| **Throughput** | 580 TPS sustained, 3,000-5,000 TPS peak (50M transfers/day) |
| **Ledger writes** | 15,000-25,000/sec peak (TigerBeetle, 1M+ TPS capacity) |
| **API latency** | <50ms P95 (auth: ~100ns from L1 cache) |
| **Uptime target** | 99.95% |
| **Fiat currencies** | NGN, USD, GBP, EUR, GHS, KES |
| **Stablecoins** | USDT, USDC |
| **Blockchains** | Ethereum, Tron, Base, Polygon, Arbitrum, Solana |
| **Tenant capacity** | 20K near-term, 100K with progressive sharding |
| **Settlement cycle** | T+3 net settlement (daily at 00:30 UTC) |
| **Recovery** | RPO=0 (synchronous replication), RTO <5 minutes |

---

## Architecture Highlights

- **Pure state machine engine with transactional outbox.** The core engine makes zero network calls -- it validates state, writes the new state and side-effect intents in a single database transaction, and returns in <1ms. A relay polls the outbox every 20ms and dispatches intents to 11 dedicated workers via NATS JetStream. This eliminates the dual-write problem (crash between DB write and event publish) by construction, guaranteeing exactly-once processing.

- **Dual-backend ledger (TigerBeetle + PostgreSQL CQRS).** TigerBeetle serves as the write authority at 1M+ TPS with hardware-enforced balanced postings -- it is physically impossible to create an unbalanced ledger entry. PostgreSQL serves as the read model for queries and reporting. This separation delivers both write throughput and query flexibility.

- **Sub-microsecond treasury reservations.** Treasury positions are held in memory using lock-free atomic compare-and-swap operations, achieving 5,000+ reservations/sec vs ~200/sec with database locking. A write-ahead log ensures durability; a background flush persists dirty positions to PostgreSQL every 100ms.

- **256-partition event sharding for tenant-level ordering.** Tenant IDs are FNV-1a hashed to 256 NATS partitions, preserving per-tenant event ordering (critical for saga correctness) while enabling massive parallelism. A slow tenant only blocks its ~400 partition peers, not the entire system.

---

## Multi-Tenancy Highlights

- **Complete tenant isolation** enforced at every layer: API gateway extracts `tenant_id` from auth tokens (never request bodies), every database query includes `WHERE tenant_id = $1` (compile-time enforced via SQLC code generation), treasury positions are keyed by tenant, and NATS events are partitioned by tenant hash.

- **Per-tenant rate limiting** with sliding window counters (local + Redis sync). Default limits: 1,000 req/min (quotes), 500 req/min (transfers). Per-tenant `MaxPendingTransfers` prevents resource exhaustion from any single tenant.

- **Independent settlement cycles.** Each tenant's net settlement is calculated independently with its own fee schedule (basis points, snapshotted at transfer creation for auditability). Settlement at 100K tenants completes in <3 minutes using 32-128 parallel workers.

- **Zero cross-tenant interference.** A tenant hitting rate limits, experiencing provider failures, or generating high volume cannot impact other tenants' processing latency or data access. Treasury positions, ledger accounts, and NATS partition assignment are all independent.

---

## Security Posture

| Area | Implementation |
|------|---------------|
| API authentication | HMAC-SHA256 hashed API keys, 3-tier cache (100ns local, 0.5ms Redis, DB) |
| Webhook signatures | HMAC-SHA256 with per-tenant secret + timestamp binding (300s replay window) |
| Encryption at rest | AES-256; column-level GCM encryption for PII with per-tenant DEK |
| Encryption in transit | TLS 1.3 (external), TLS 1.2+ (internal) |
| Key revocation | Redis pub/sub propagation to all gateway replicas in <1 second |
| Tenant isolation | Enforced at API, database, ledger, treasury, and event layers |
| Idempotency | Per-tenant scoped, 24-hour TTL, two-level cache (Redis + DB) |
| Rate limiting | Per-tenant sliding window, synced to Redis every 5 seconds |

---

## Competitive Comparison

| Dimension | Settla | Build In-House | Traditional PSP/Rails |
|-----------|--------|---------------|----------------------|
| **Time to integrate** | Days (REST API + webhooks) | 6-18 months | Weeks-months |
| **Settlement speed** | Minutes (on-chain) | Depends on rails | 1-5 business days |
| **Throughput** | 50M txns/day proven | Unproven at scale | Varies |
| **Ledger correctness** | TigerBeetle (hardware-enforced balanced postings) | Custom (error-prone) | Varies |
| **Tenant capacity** | 20K near-term, 100K growth | Build from scratch | Typically <1K |
| **Multi-tenant isolation** | 7-layer enforcement (compile-time + runtime) | Manual, error-prone | Varies |
| **Settlement netting** | Automatic daily T+3 net settlement | Build from scratch | Manual |
| **Blockchain support** | 6 chains, 2 stablecoins | Build + maintain | None |
| **Deposit collection** | Crypto + bank (virtual accounts) | Build + maintain | Bank only |
| **Webhook delivery** | HMAC-signed, retry with DLQ | Build from scratch | Basic |
| **Ongoing maintenance** | Managed by Settla | Full team required | Varies |
| **Cost model** | Per-transaction (25-40 bps) | Eng team + infra | Per-transaction |

---

## Integration Timeline

A typical engineering team can complete integration in **3-5 business days**:

| Day | Milestone |
|-----|-----------|
| 1 | API key provisioning, sandbox access, first test transfer |
| 2 | Webhook endpoint setup and verification, transfer lifecycle integration |
| 3 | Error handling, idempotency implementation, deposit flow integration |
| 4 | Production readiness review, fee schedule configuration |
| 5 | Go-live with monitoring and alerting |

The API follows REST conventions with idempotency keys, standard HTTP error codes, and webhook-driven async notifications. No blockchain knowledge is required from integrating teams -- Settla abstracts all on-chain complexity behind the transfer and deposit APIs.
