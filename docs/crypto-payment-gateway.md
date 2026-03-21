# Crypto Payment Gateway

**Status:** Proposed
**Date:** 2026-03-11
**Authors:** Engineering Team

---

## Code Reuse from paydash_backend

A production-grade crypto monitor and crypto service already exists in the sibling project at `../paydash_backend/`. Large portions of that implementation can be ported directly into Settla rather than rebuilt from scratch. The table below maps each Settla component to its paydash source.

| Settla component | Reuse from paydash_backend | Notes |
|---|---|---|
| `node/chainmonitor/` — EVM poller | `crypto_monitor/internal/monitor/poller_v2.go` | Port `PollerV2` wholesale: `eth_getLogs` for ERC-20, native tx scanning, reorg detection, checkpoint/block-hash strategy |
| `node/chainmonitor/` — orchestrator | `crypto_monitor/internal/monitor/monitor_v2.go` | Port `MonitorV2`: multi-chain poller management, incremental address sync (10s), full reconciliation (5min), token registry reload |
| `node/chainmonitor/` — address set | `crypto_monitor/internal/monitor/address_set.go` | Port `AddressSet` + `AddressSnapshot` directly: two-layer mutable/immutable design with `atomic.Pointer` for lock-free poller reads |
| `node/chainmonitor/` — token registry | `crypto_monitor/internal/tokens/registry.go` | Port lock-free `Registry` using `atomic.Value` + copy-on-write token map |
| `node/chainmonitor/` — Tron client | `crypto_monitor/internal/blockchain/tron/client.go` | Port Tron HTTP client with dual-provider failover |
| `node/chainmonitor/` — EVM client | `crypto_monitor/internal/blockchain/evm/client.go` | Port EVM JSON-RPC client; swap `FailoverManager` to point at Alchemy endpoints |
| `crypto/wallet/` — HD address generator | `crypto_service/internal/address/generator.go` | Port chain-specific generators (ETH, Tron). BIP-44 coin types are identical: 60=ETH, 195=Tron, 501=Solana, 0=BTC |
| `crypto/signing/` — key management | `crypto_service/internal/keymgmt/` | Port `FileStore` / `EnvStore` for dev/staging; swap production backend to AWS KMS |
| `node/chainmonitor/` — RPC failover | `crypto_monitor/internal/rpc/failover.go` | Port `FailoverManager` + per-provider circuit breaker and token-bucket rate limiter |
| Chain config YAML | `crypto_monitor/internal/config/mainnet.yaml` + `testnet.yaml` | Adapt chain/provider config structure; replace provider endpoints with Alchemy URLs |
| Block checkpoint table | `crypto_monitor/internal/db/` (`block_checkpoints`) | Port schema and SQLC queries directly |
| Token table | `crypto_monitor/internal/db/` (`tokens`) | Port schema; seed with Settla's supported token set |

### What is NOT reused

- The RabbitMQ outbox publisher (`crypto_monitor/internal/outbox/publisher.go`) — Settla already has a superior transactional outbox writing to Postgres + NATS JetStream. The paydash outbox is replaced entirely by Settla's existing `node/outbox.Relay`.
- The JWT auth layer from `crypto_service` — Settla uses its own tenant auth (HMAC API key → SHA-256 → tenant resolution).
- The treasury sweeper (`crypto_service/internal/treasury/sweeper.go`) — out of scope for this feature phase.
- The `crypto_service` REST server — Settla exposes deposits via its existing Fastify gateway + gRPC.

---

## Overview

The Crypto Payment Gateway allows Settla merchants (tenants) to accept cryptocurrency payments from their customers through a time-bounded deposit address, and to settle those payments either as fiat (any supported currency) or to hold and manage the crypto balance directly within Settla.

This document covers the full feature: product behaviour, system design, new components, data model changes, integration with existing Settla architecture, and edge cases.

---

## Product Behaviour

### From the merchant's perspective

1. A merchant calls the Settla API to create a **deposit session** for a specific payment — e.g., "I need to collect 150 USDT from a customer for invoice INV-001."
2. Settla returns a **deposit address** and a **session expiry time** (default 30 minutes, configurable per tenant).
3. The merchant displays the address (and optionally a QR code) to their customer.
4. The customer sends the exact amount in cryptocurrency to that address.
5. Settla detects the on-chain transaction, waits for sufficient confirmations, and credits the merchant's Settla account.
6. Depending on the merchant's configured settlement preference:
   - **Auto-convert to fiat**: Settla converts the received crypto to the merchant's preferred fiat currency (GBP, NGN, USD, etc.) via the existing off-ramp rail and settles to their linked bank account or Settla balance.
   - **Hold as crypto**: The crypto sits in the merchant's Settla treasury position. The merchant can view, transfer, or manually convert it later via the portal.
   - **Hybrid (threshold-based)**: amounts above a configured threshold auto-convert; amounts below are held.
7. Settla fires a webhook to the merchant's configured endpoint at each status change: `session.created`, `transaction.detected`, `transaction.confirmed`, `session.credited`, `session.expired`, `session.failed`.

### Session states

```
PENDING_PAYMENT
    │  on-chain tx detected (unconfirmed)
    ▼
TRANSACTION_DETECTED
    │  required confirmations reached
    ▼
TRANSACTION_CONFIRMED
    │  amount validation passes
    ▼
CREDITING
    │  ledger + treasury updated
    ▼
CREDITED
    │  if settlement_preference = AUTO_CONVERT
    ▼
SETTLING  ──► SETTLED
    │  (off-ramp rail runs)
    │
    │  if settlement_preference = HOLD or threshold not reached
    └──► HELD (terminal)

PENDING_PAYMENT ──► (expires with no tx) ──► EXPIRED (terminal)
CREDITING ──► (amount mismatch) ──► FAILED (terminal, triggers compensation)
TRANSACTION_DETECTED ──► (session expires mid-flight) ──► LATE_PAYMENT (manual review)
```

---

## Architecture

### Guiding principle: fit into the existing outbox pattern

The Crypto Payment Gateway is not a new architectural paradigm — it is a new domain that uses the same patterns as the rest of Settla:

- The engine is a pure state machine. Session state changes + outbox entries are written atomically.
- Workers execute side effects (chain monitoring subscriptions, ledger credits, off-ramp) by consuming outbox-driven NATS events.
- The existing compensation flows (`SIMPLE_REFUND`, `MANUAL_REVIEW`) handle edge cases.
- A new 8th NATS stream (`SETTLA_CRYPTO_DEPOSITS`) carries deposit-specific events.

### New components

| Component | Language | Location | Purpose |
|---|---|---|---|
| `DepositSessionEngine` | Go | `core/deposit/` | Pure state machine for deposit session lifecycle |
| `ChainMonitor` | Go | `node/chainmonitor/` | Subscribes to blockchain events; detects incoming txns on active addresses |
| `DepositWorker` | Go | `node/worker/deposit_worker.go` | Processes `SETTLA_CRYPTO_DEPOSITS` stream; calls `DepositSessionEngine.Handle*` |
| `HDWallet` | Go | `crypto/wallet/` | BIP-32/BIP-44 HD wallet for deterministic address derivation |
| `SigningService` | Go | `crypto/signing/` | HSM-backed (or KMS-backed in dev) transaction signing; the only component with key material access |
| Deposit session DB | SQL | `store/transferdb/` | New tables: `crypto_deposit_sessions`, `crypto_deposit_addresses` |
| Portal deposit UI | Vue | `dashboard/pages/` | Session history, live status, balance management |
| Gateway API routes | TS | `api/gateway/src/routes/` | `POST /v1/deposits`, `GET /v1/deposits/:id` |
| gRPC service | Go + Proto | `proto/settla/v1/`, `api/grpc/` | `DepositService` RPC methods |

---

## Key Design Decisions

### 1. HD wallet address derivation

**Decision:** Use BIP-32/BIP-44 hierarchical deterministic (HD) wallets to derive a unique address per deposit session.

**Derivation path:**
```
m / purpose' / coin_type' / tenant_index' / session_index
m / 44'      / 195'       / {tenant_idx}' / {session_seq}
```

- `coin_type 195` = TRON (for USDT-TRC20). `coin_type 60` = Ethereum (for USDC/USDT-ERC20).
- `tenant_index` is a stable integer assigned to each tenant at onboarding (never reused).
- `session_index` is a monotonically incrementing counter per tenant, stored in `crypto_deposit_addresses`.

**Why HD derivation instead of generating individual wallets:**
- No address reuse — each session gets a fresh address.
- The derivation path is the lookup key. We never need to scan all known addresses; we rederive on demand.
- A single master seed (held securely in the `SigningService`) produces all addresses. Key management is simple.
- Auditable: any derived address can be independently verified from the master seed + path.

**Master seed storage:**
- Production: AWS KMS or HashiCorp Vault (HSM-backed). The raw seed never lives in application memory beyond the signing operation.
- Development: environment variable (never committed).
- The `SigningService` is the only process that ever holds key material. All other components request address derivation or tx signing via internal gRPC calls to the `SigningService`.

### 2. Chain monitoring strategy — Alchemy RPC + block polling

**Decision:** Use Alchemy as the primary RPC provider across all EVM chains, with TronGrid for Tron. Detection uses block-by-block polling (`eth_getLogs` for ERC-20, transaction scanning for native) — the same strategy proven in `paydash_backend/crypto_monitor/internal/monitor/poller_v2.go`.

```
Alchemy (EVM) / TronGrid (Tron)
    │  JSON-RPC (eth_getLogs, eth_getBlockByNumber, etc.)
    ▼
ChainMonitor — MonitorV2 orchestrator (ported from paydash MonitorV2)
    │  per-chain PollerV2 goroutine (EVM) / GenericPoller (Tron)
    │  reads immutable AddressSnapshot at start of each poll cycle
    │  ERC-20: eth_getLogs filtered by Transfer topic + recipient
    │  Native: scan block txs, filter by tx.To against snapshot
    │  on match: upsert crypto_deposit_transactions + publish outbox entry atomically
    ▼
node/outbox.Relay (existing, unchanged)
    │  publishes outbox entry to NATS
    ▼
NATS JetStream: SETTLA_CRYPTO_DEPOSITS
    ▼
DepositWorker
    │  calls DepositSessionEngine.HandleTransactionDetected(...)
    ▼
Engine writes state + outbox entries atomically
```

**Alchemy configuration (per chain):**

| Chain | Alchemy network slug | Endpoint pattern |
|---|---|---|
| Ethereum mainnet | `eth-mainnet` | `https://eth-mainnet.g.alchemy.com/v2/{API_KEY}` |
| Polygon | `polygon-mainnet` | `https://polygon-mainnet.g.alchemy.com/v2/{API_KEY}` |
| Base | `base-mainnet` | `https://base-mainnet.g.alchemy.com/v2/{API_KEY}` |
| Arbitrum One | `arb-mainnet` | `https://arb-mainnet.g.alchemy.com/v2/{API_KEY}` |
| Optimism | `opt-mainnet` | `https://opt-mainnet.g.alchemy.com/v2/{API_KEY}` |
| Ethereum Sepolia (testnet) | `eth-sepolia` | `https://eth-sepolia.g.alchemy.com/v2/{API_KEY}` |

Alchemy supports Compute Units (CUs). `eth_getLogs` costs 75 CU, `eth_getBlockByNumber` costs 16 CU. The growth plan (660 CU/s) is sufficient for Settla's polling cadence across 5 chains. The `FailoverManager` (ported from `paydash_backend/crypto_monitor/internal/rpc/failover.go`) handles failover to a secondary RPC (e.g., Infura or public endpoint) if Alchemy's circuit breaker trips.

**ERC-20 deposit detection (from `paydash PollerV2`):**

```go
// eth_getLogs filter for ERC-20 Transfer events
// Topic 0: Transfer(address,address,uint256)
// Topic 2: recipient address (padded to 32 bytes)
// Alchemy supports up to 2,000 blocks per eth_getLogs call (paid tier)
// Use batch size = 10 blocks for free tier safety (same as paydash default)

filter := ethereum.FilterQuery{
    FromBlock: big.NewInt(lastBlock + 1),
    ToBlock:   big.NewInt(lastBlock + batchSize),
    Topics: [][]common.Hash{
        {common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")},
        nil,                          // from: any
        addressTopics(snapshot),      // to: our monitored addresses
    },
}
```

**Address set — immutable snapshot pattern (from `paydash AddressSet`):**

The `AddressSet` (ported from `paydash_backend/crypto_monitor/internal/monitor/address_set.go`) maintains two layers:

```
Mutable layer (RWMutex):           Immutable snapshot (atomic.Pointer):
  map[chain][address] → bool         map[chain][address] → bool
  map[address] → tenant_id           version uint64
  (updated every 10s)                (pollers capture this at poll start)
```

Each `PollerV2` captures a snapshot at the start of each poll cycle and works against the immutable copy for the entire cycle — no lock contention during scanning. The monitor reconciles the full address list from DB every 5 minutes to catch any updates/deletions missed by the incremental sync.

**RPC failover (from `paydash FailoverManager`):**

Each chain's `PollerV2` uses a `FailoverManager` (ported from `paydash_backend/crypto_monitor/internal/rpc/failover.go`) with per-provider circuit breaker and token-bucket rate limiter:

```go
// Before each RPC call:
if circuitBreaker.CanAttempt() && rateLimiter.Allow() {
    use Alchemy
} else {
    failover to secondary provider
}
// Circuit breaker: open after 5 consecutive failures, reset after 30s
// Rate limiter: token bucket, 300 CU/s (configurable per plan)
```

**Tron monitoring:**

TronGrid webhook push is used for Tron (USDT-TRC20). TronGrid sends a POST to `api/webhook` when a TRC-20 transfer lands on a monitored address. The `GenericPoller` (ported from `paydash_backend/crypto_monitor/internal/monitor/generic_poller.go`) acts as fallback: it polls Tron full-node RPC if TronGrid webhook delivery fails.

### 3. Time-bounded sessions

"Session expiry" is entirely application-side — the blockchain has no concept of an expiry. The constraints:

- Sessions have a configurable TTL (default 30 minutes, range 5 min–24 hours, set per tenant in `tenant_config`).
- A background goroutine in `DepositWorker` polls for `PENDING_PAYMENT` sessions whose `expires_at < now()` and transitions them to `EXPIRED` via the engine.
- The active address Redis cache uses TTL = `expires_at + 10min` so late-arriving transactions (detected after expiry but submitted on-chain before) are still picked up and routed to `LATE_PAYMENT` state for manual review rather than silently dropped.
- The `ChainMonitor` always reports what it sees. The engine decides what to do with a transaction that arrives on an expired session.

### 4. Settlement preferences

Settlement preference is stored per tenant in `tenant_config` and can be overridden per deposit session at creation time.

| Preference | Behaviour |
|---|---|
| `AUTO_CONVERT` | On credit, immediately queue an off-ramp via the existing rail. Merchant never holds crypto. |
| `HOLD` | Credit merchant's crypto treasury position. No conversion. |
| `THRESHOLD` | Auto-convert if received amount ≥ `auto_convert_threshold_usd`. Hold otherwise. |

When `AUTO_CONVERT` or `THRESHOLD` triggers conversion, the engine emits an `OutboxEntry` with `event_type = intent.off_ramp`. The existing `ProviderWorker` picks this up and executes through the normal rail routing — no new code path needed.

### 5. Underpayment and overpayment

These are common in real-world crypto payments (user pays wrong amount, gas deducted from transfer, etc.).

| Scenario | Handling |
|---|---|
| **Exact payment** | Normal flow: credit full amount. |
| **Underpayment (< 99% of expected)** | Wait for top-up until `expires_at`. If none arrives, transition to `UNDERPAID` — trigger `SIMPLE_REFUND` compensation (refund to `refund_address`), emit webhook `session.underpaid`. |
| **Overpayment (> 100% + tolerance)** | Credit expected amount. Flag excess as `OVERPAID` and enqueue `MANUAL_REVIEW` compensation for the delta. |
| **Payment tolerance** | Configurable per tenant: `payment_tolerance_bps` (default 100 = 1%). Amounts within tolerance of expected are treated as exact. |
| **Multiple partial payments** | Accumulate received amounts within the session window. A session is fully paid when cumulative received ≥ expected (within tolerance). |

### 6. Refund address collection

When a session is created, the merchant can optionally provide a `refund_address` (the on-chain address to return funds to in case of underpayment or overpayment). If not provided:
- The session's own deposit address is used as the refund destination (funds returned to sender).
- If chain analysis cannot determine a sender address (e.g., exchange withdrawal), the session goes to `MANUAL_REVIEW`.

### 7. Key custody boundary

**Critical:** Settla must never be a de facto custodian of customer funds without the appropriate regulatory framework.

The `SigningService` boundary enforces this:
- Only the `SigningService` holds key material (master seed, derived private keys).
- No other Settla component ever touches private key bytes.
- The `SigningService` only exposes two operations via internal gRPC: `DeriveAddress(path)` and `SignTransaction(unsignedTx)`.
- All key operations are logged with full audit trail.
- In production: keys live in AWS KMS or HashiCorp Vault. The `SigningService` never stores the decrypted key in memory beyond the duration of a single signing operation.

This boundary also makes regulatory compliance clearer: the `SigningService` is the regulated custody component; the rest of Settla is settlement infrastructure.

---

## Data Model

### New tables (Transfer DB)

#### `crypto_deposit_sessions`

```sql
CREATE TABLE crypto_deposit_sessions (
    id                      UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id               UUID NOT NULL REFERENCES tenants(id),
    idempotency_key         TEXT NOT NULL,
    invoice_reference       TEXT,                     -- merchant's own invoice ID
    deposit_address         TEXT NOT NULL,
    derivation_path         TEXT NOT NULL,            -- e.g. m/44'/195'/3'/1042
    chain                   TEXT NOT NULL,            -- TRON_TRC20, ETH_ERC20, etc.
    token                   TEXT NOT NULL,            -- USDT, USDC, etc.
    expected_amount         NUMERIC(36,18) NOT NULL,
    expected_currency       TEXT NOT NULL,            -- matches token (USDT)
    received_amount         NUMERIC(36,18) NOT NULL DEFAULT 0,
    payment_tolerance_bps   INT NOT NULL DEFAULT 100,
    status                  TEXT NOT NULL DEFAULT 'PENDING_PAYMENT',
    settlement_preference   TEXT NOT NULL DEFAULT 'AUTO_CONVERT',
    settle_to_currency      TEXT,                     -- e.g. GBP, NGN (for AUTO_CONVERT)
    auto_convert_threshold  NUMERIC(36,18),           -- for THRESHOLD preference
    refund_address          TEXT,
    expires_at              TIMESTAMPTZ NOT NULL,
    credited_at             TIMESTAMPTZ,
    settled_at              TIMESTAMPTZ,
    metadata                JSONB NOT NULL DEFAULT '{}',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX ON crypto_deposit_sessions (tenant_id, idempotency_key);
CREATE INDEX ON crypto_deposit_sessions (deposit_address, status);
CREATE INDEX ON crypto_deposit_sessions (tenant_id, status, expires_at);
```

#### `crypto_deposit_transactions`

Records each on-chain transaction seen for a session (there may be multiple partial payments).

```sql
CREATE TABLE crypto_deposit_transactions (
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    session_id          UUID NOT NULL,
    tenant_id           UUID NOT NULL,
    tx_hash             TEXT NOT NULL,
    chain               TEXT NOT NULL,
    from_address        TEXT,
    to_address          TEXT NOT NULL,
    amount              NUMERIC(36,18) NOT NULL,
    currency            TEXT NOT NULL,
    block_number        BIGINT,
    block_hash          TEXT,
    confirmations       INT NOT NULL DEFAULT 0,
    required_confirmations INT NOT NULL DEFAULT 12,
    status              TEXT NOT NULL DEFAULT 'DETECTED',  -- DETECTED, CONFIRMING, CONFIRMED, FAILED
    detected_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX ON crypto_deposit_transactions (tx_hash, chain);
CREATE INDEX ON crypto_deposit_transactions (session_id);
```

#### `crypto_deposit_address_index`

Maps HD wallet derivation paths to tenants. Used to quickly determine the next available session index and to rederive addresses for audit.

```sql
CREATE TABLE crypto_deposit_address_index (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    tenant_index    INT NOT NULL,     -- stable, assigned once at onboarding
    chain           TEXT NOT NULL,
    last_session_index BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX ON crypto_deposit_address_index (tenant_id, chain);
```

### Tenant config additions

New columns on `tenants` (or a new `tenant_crypto_config` table if preferred):

```sql
ALTER TABLE tenants ADD COLUMN crypto_enabled            BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tenants ADD COLUMN default_settlement_pref   TEXT NOT NULL DEFAULT 'AUTO_CONVERT';
ALTER TABLE tenants ADD COLUMN default_settle_currency   TEXT;             -- GBP, NGN, etc.
ALTER TABLE tenants ADD COLUMN auto_convert_threshold    NUMERIC(36,18);   -- for THRESHOLD pref
ALTER TABLE tenants ADD COLUMN payment_tolerance_bps     INT NOT NULL DEFAULT 100;
ALTER TABLE tenants ADD COLUMN default_session_ttl_secs  INT NOT NULL DEFAULT 1800; -- 30 min
ALTER TABLE tenants ADD COLUMN supported_chains          TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE tenants ADD COLUMN min_confirmations_tron    INT NOT NULL DEFAULT 19;
ALTER TABLE tenants ADD COLUMN min_confirmations_eth     INT NOT NULL DEFAULT 12;
```

---

## API

### `POST /v1/deposits` — Create deposit session

**Request:**
```json
{
  "idempotency_key": "inv-001-attempt-1",
  "invoice_reference": "INV-001",
  "expected_amount": "150.00",
  "expected_currency": "USDT",
  "chain": "TRON_TRC20",
  "settlement_preference": "AUTO_CONVERT",
  "settle_to_currency": "GBP",
  "refund_address": "TXxxxx...",
  "expires_in_seconds": 1800,
  "metadata": { "customer_id": "cust_123", "order_id": "ord_456" }
}
```

**Response:**
```json
{
  "id": "d8a3f...",
  "deposit_address": "TRxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "chain": "TRON_TRC20",
  "token": "USDT",
  "expected_amount": "150.00",
  "expires_at": "2026-03-11T14:30:00Z",
  "status": "PENDING_PAYMENT",
  "qr_code_uri": "data:image/png;base64,..."
}
```

**Validation:**
- `expected_currency` must be a token supported on `chain`.
- `chain` must be in tenant's `supported_chains`.
- `settle_to_currency` required when `settlement_preference = AUTO_CONVERT`.
- `expires_in_seconds` clamped to tenant's allowed range (300–86400).
- `tenant_id` always sourced from auth token, never from request body.

### `GET /v1/deposits/:id` — Get session status

Returns the full session object including received transactions and current status.

### `GET /v1/deposits` — List sessions

Paginated, filterable by `status`, `chain`, date range. Always tenant-scoped.

### `POST /v1/deposits/:id/cancel` — Cancel a pending session

Allowed only in `PENDING_PAYMENT` state. Transitions to `CANCELLED`. If a payment arrives after cancellation, it routes to `MANUAL_REVIEW`.

### `GET /v1/deposits/balance` — Crypto balances

Returns the tenant's current held crypto balances (for `HOLD` / `THRESHOLD` sessions).

```json
{
  "balances": [
    { "currency": "USDT", "chain": "TRON_TRC20", "amount": "4250.00" },
    { "currency": "USDC", "chain": "ETH_ERC20",  "amount": "1100.50" }
  ]
}
```

### `POST /v1/deposits/convert` — Manual conversion (held balances)

For tenants with held crypto balances, trigger an on-demand conversion to fiat.

```json
{
  "idempotency_key": "convert-001",
  "from_currency": "USDT",
  "from_chain": "TRON_TRC20",
  "amount": "1000.00",
  "to_currency": "GBP"
}
```

This creates a normal Settla transfer using the existing off-ramp rail — no new code path.

---

## Collection Fee Model

### Fee structure

Every crypto deposit session charges a **collection fee** deducted from the received amount before the merchant is credited:

```
fee             = received_amount × (collection_bps / 10000)
fee             = min(fee, collection_max_fee_usd)   ← cap
credited_amount = received_amount − fee
```

Both the percentage and the cap are configurable. A tenant-specific schedule takes precedence; when unset the global default applies.

**Example** — global default 50 bps capped at $25:
- $200 USDT received → fee = $1.00 → merchant credited $199.00
- $10,000 USDT received → uncapped fee = $50.00 → capped to $25.00 → merchant credited $9,975.00
- $10 USDT received → fee = $0.05 → merchant credited $9.95

---

### Extending the existing `FeeSchedule`

`FeeSchedule` in `domain/tenant.go` already implements BPS + cap for on-ramp/off-ramp fees. Two new fields are added following the identical pattern:

```go
// domain/tenant.go

type FeeSchedule struct {
    OnRampBPS  int             `json:"onramp_bps"`
    OffRampBPS int             `json:"offramp_bps"`
    MinFeeUSD  decimal.Decimal `json:"min_fee_usd"`
    MaxFeeUSD  decimal.Decimal `json:"max_fee_usd"`

    // CryptoCollectionBPS is the fee in basis points applied to received_amount
    // on every deposit session (1 bps = 0.01%). Zero means "use global default".
    CryptoCollectionBPS int `json:"crypto_collection_bps"`

    // CryptoCollectionMaxFeeUSD caps the collection fee in USD.
    // Zero means no cap (not recommended for production).
    CryptoCollectionMaxFeeUSD decimal.Decimal `json:"crypto_collection_max_fee_usd"`
}
```

Since `fee_schedule` is stored as JSONB in the `tenants` table, this is fully backward-compatible — existing rows have `crypto_collection_bps` absent from the JSON object (reads as zero), which triggers the global default fallback. No column migration needed.

`CalculateFee` gains a new `"crypto_collection"` branch:

```go
func (f FeeSchedule) CalculateFee(amount decimal.Decimal, feeType string) decimal.Decimal {
    var bps int
    var maxFee decimal.Decimal
    switch feeType {
    case "onramp":
        bps, maxFee = f.OnRampBPS, f.MaxFeeUSD
    case "offramp":
        bps, maxFee = f.OffRampBPS, f.MaxFeeUSD
    case "crypto_collection":
        bps, maxFee = f.CryptoCollectionBPS, f.CryptoCollectionMaxFeeUSD
    default:
        return decimal.Zero
    }
    fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)
    if !maxFee.IsZero() && fee.GreaterThan(maxFee) {
        fee = maxFee
    }
    return fee
}
```

---

### Global default fee schedule

A `global_fee_config` table holds the system-wide defaults. It is append-only — inserting a new row changes the rate; existing rows are never updated. This gives a full audit log and supports scheduled rate changes without deployments.

```sql
-- db/migrations/transfer/000008_create_global_fee_config.up.sql

CREATE TABLE global_fee_config (
    id                            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    crypto_collection_bps         INT NOT NULL DEFAULT 50,        -- 0.50%
    crypto_collection_max_fee_usd NUMERIC(28,8) NOT NULL DEFAULT 25.00,
    effective_from                TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by                    TEXT NOT NULL,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Application always reads the row with the latest effective_from <= now()
CREATE INDEX ON global_fee_config (effective_from DESC);

-- Seed
INSERT INTO global_fee_config (crypto_collection_bps, crypto_collection_max_fee_usd, created_by)
VALUES (50, 25.00, 'system');
```

---

### Fee resolution

```go
// core/deposit/fees.go

// CryptoFeeResolver resolves the applicable fee for a session.
// Resolution order:
//   1. Tenant CryptoCollectionBPS > 0  →  use tenant schedule
//   2. Tenant crypto_fee_exempt = true →  zero fee (explicit agreement)
//   3. Neither set                     →  use global_fee_config (latest effective row)
type CryptoFeeResolver struct {
    globalConfig GlobalFeeConfigStore
}

func (r *CryptoFeeResolver) Resolve(ctx context.Context, tenant domain.Tenant) (bps int, maxFeeUSD decimal.Decimal, err error) {
    if tenant.CryptoFeeExempt {
        return 0, decimal.Zero, nil
    }
    if tenant.FeeSchedule.CryptoCollectionBPS > 0 {
        return tenant.FeeSchedule.CryptoCollectionBPS,
               tenant.FeeSchedule.CryptoCollectionMaxFeeUSD, nil
    }
    cfg, err := r.globalConfig.GetEffective(ctx)
    if err != nil {
        return 0, decimal.Zero, fmt.Errorf("deposit fee resolver: %w", err)
    }
    return cfg.CryptoCollectionBPS, cfg.CryptoCollectionMaxFeeUSD, nil
}
```

A tenant with a genuine zero-fee agreement must have `crypto_fee_exempt = true` set explicitly by ops. A `CryptoCollectionBPS = 0` with no exempt flag simply falls through to the global default — this prevents accidental zero-fee billing from an unset JSONB field.

```sql
-- add to tenants table
ALTER TABLE tenants ADD COLUMN crypto_fee_exempt BOOLEAN NOT NULL DEFAULT false;
```

---

### Application in the engine

Fee calculation happens in `DepositSessionEngine.HandleTransactionConfirmed` — the only point where the exact confirmed `received_amount` is known. Everything is written atomically in one DB transaction.

```go
// core/deposit/engine.go

func (e *Engine) HandleTransactionConfirmed(ctx context.Context, sessionID uuid.UUID, txID uuid.UUID) error {
    session, _ := e.store.GetDepositSession(ctx, sessionID)
    tenant, _  := e.tenantStore.GetTenant(ctx, session.TenantID)

    // Resolve fee schedule (tenant-specific → global default)
    bps, maxFee, _ := e.feeResolver.Resolve(ctx, tenant)

    // Calculate fee — shopspring/decimal, never float
    fee := session.ReceivedAmount.
        Mul(decimal.NewFromInt(int64(bps))).
        Div(decimal.NewFromInt(10000))
    if !maxFee.IsZero() && fee.GreaterThan(maxFee) {
        fee = maxFee
    }
    creditedAmount := session.ReceivedAmount.Sub(fee)

    // Guard: fee must never exceed received amount
    if creditedAmount.LessThanOrEqual(decimal.Zero) {
        return fmt.Errorf("deposit engine: fee %s >= received_amount %s for session %s",
            fee, session.ReceivedAmount, sessionID)
    }

    return e.store.WithTx(ctx, func(tx Store) error {
        // Persist fee breakdown on the session row (immutable receipt)
        _ = tx.UpdateDepositSession(ctx, UpdateSessionParams{
            ID:             sessionID,
            Status:         StatusTransactionConfirmed,
            FeeBPS:         bps,
            FeeMaxUSD:      maxFee,
            FeeAmount:      fee,
            CreditedAmount: creditedAmount,
        })
        // Outbox carries both amounts so LedgerWorker can post the correct entries
        return tx.InsertOutboxEntry(ctx, OutboxEntry{
            EventType: IntentCreditDeposit,
            Payload: CreditDepositPayload{
                SessionID:      sessionID,
                TenantID:       session.TenantID,
                ReceivedAmount: session.ReceivedAmount,
                FeeAmount:      fee,
                CreditedAmount: creditedAmount,
                Currency:       session.Token,
            },
        })
    })
}
```

---

### Ledger double-entry for fees

`LedgerWorker` processing `intent.credit_deposit` posts two balanced entries from the `CreditDepositPayload`:

```
Posting 1 — credit merchant (credited_amount):
  DEBIT   assets:crypto:{token}:suspense          credited_amount
  CREDIT  tenant:{slug}:assets:crypto:{token}     credited_amount

Posting 2 — credit Settla fee revenue (fee_amount):
  DEBIT   assets:crypto:{token}:suspense          fee_amount
  CREDIT  assets:fees:crypto:collection           fee_amount

Invariant: debits = credits = received_amount  ✓
```

`assets:crypto:{token}:suspense` is debited in full when an on-chain deposit is confirmed, then split across both credit entries. The Settla fee lands in `assets:fees:crypto:collection`, which feeds the existing net-settlement and revenue reporting flows.

---

### Data model additions to `crypto_deposit_sessions`

```sql
ALTER TABLE crypto_deposit_sessions
    ADD COLUMN fee_bps          INT,             -- schedule used: bps at confirmation time
    ADD COLUMN fee_max_usd      NUMERIC(28,8),   -- schedule used: cap at confirmation time
    ADD COLUMN fee_amount       NUMERIC(36,18),  -- actual fee deducted (in received token units)
    ADD COLUMN credited_amount  NUMERIC(36,18);  -- received_amount - fee_amount (what merchant receives)
```

Storing `fee_bps` and `fee_max_usd` on the row freezes the applicable schedule at the moment of confirmation — future fee schedule changes do not retroactively alter the recorded fee. Required for dispute resolution and regulatory reporting.

---

### API exposure

`GET /v1/deposits/:id` and post-confirmation responses include:

```json
{
  "id": "d8a3f...",
  "expected_amount":  "150.00",
  "received_amount":  "150.00",
  "fee_bps":          50,
  "fee_max_usd":      "25.00",
  "fee_amount":       "0.75",
  "credited_amount":  "149.25",
  "status": "CREDITED"
}
```

Before confirmation, `fee_amount` and `credited_amount` are `null`. Session creation returns an **estimated fee** based on `expected_amount` using the currently applicable schedule:

```json
{
  "id": "d8a3f...",
  "expected_amount":           "150.00",
  "estimated_fee":             "0.75",
  "estimated_credited_amount": "149.25",
  "status": "PENDING_PAYMENT"
}
```

`estimated_fee` is advisory — if the customer pays a different amount, the actual fee will differ proportionally (subject to the cap).

---

### Seed fee schedules

```sql
-- db/seed/transfer_seed.sql

-- Global default: 50 bps, $25 cap
INSERT INTO global_fee_config (crypto_collection_bps, crypto_collection_max_fee_usd, created_by)
VALUES (50, 25.00, 'system_seed');

-- Lemfi: negotiated 30 bps, $15 cap
UPDATE tenants
SET fee_schedule = fee_schedule || '{"crypto_collection_bps": 30, "crypto_collection_max_fee_usd": "15.00"}'
WHERE slug = 'lemfi';

-- Fincra: negotiated 40 bps, $20 cap
UPDATE tenants
SET fee_schedule = fee_schedule || '{"crypto_collection_bps": 40, "crypto_collection_max_fee_usd": "20.00"}'
WHERE slug = 'fincra';
```

---

## gRPC Service

```protobuf
// proto/settla/v1/deposit.proto

service DepositService {
  rpc CreateDepositSession  (CreateDepositSessionRequest)  returns (DepositSession);
  rpc GetDepositSession     (GetDepositSessionRequest)     returns (DepositSession);
  rpc ListDepositSessions   (ListDepositSessionsRequest)   returns (ListDepositSessionsResponse);
  rpc CancelDepositSession  (CancelDepositSessionRequest)  returns (DepositSession);
  rpc GetCryptoBalances     (GetCryptoBalancesRequest)     returns (CryptoBalancesResponse);
  rpc ConvertCryptoBalance  (ConvertCryptoBalanceRequest)  returns (Transfer);
}
```

---

## NATS Streams

### New stream: `SETTLA_CRYPTO_DEPOSITS`

| Property | Value |
|---|---|
| Stream name | `SETTLA_CRYPTO_DEPOSITS` |
| Subject pattern | `settla.deposit.>` |
| Retention | WorkQueue |
| Max age | 7 days |
| Dedup window | 2 minutes |
| Consumer | `DepositWorker` |

**Subjects used:**

| Subject | Published by | Consumed by | Purpose |
|---|---|---|---|
| `settla.deposit.session.created` | Engine outbox relay | DepositWorker | Trigger ChainMonitor subscription |
| `settla.deposit.tx.detected` | ChainMonitor | DepositWorker | Inbound on-chain tx notification |
| `settla.deposit.tx.confirmed` | DepositWorker | DepositWorker | Confirmation threshold reached |
| `settla.deposit.session.expiry_check` | DepositWorker (timer) | DepositWorker | Trigger expiry evaluation |
| `settla.deposit.session.credit` | Engine outbox relay | LedgerWorker + TreasuryWorker | Credit merchant account |
| `settla.deposit.session.settle` | Engine outbox relay | ProviderWorker | Trigger off-ramp |

---

## Outbox Event Types

New `event_type` constants added to `domain/outbox.go`:

| Constant | Value | Intent or Event |
|---|---|---|
| `IntentMonitorAddress` | `intent.monitor_address` | Intent — tells ChainMonitor to subscribe |
| `IntentUnmonitorAddress` | `intent.unmonitor_address` | Intent — tells ChainMonitor to unsubscribe |
| `IntentCreditDeposit` | `intent.credit_deposit` | Intent — credit ledger + treasury |
| `EventDepositSessionCreated` | `event.deposit_session.created` | Event — session opened |
| `EventDepositTxDetected` | `event.deposit_tx.detected` | Event — on-chain tx seen |
| `EventDepositTxConfirmed` | `event.deposit_tx.confirmed` | Event — confirmations reached |
| `EventDepositSessionCredited` | `event.deposit_session.credited` | Event — merchant balance updated |
| `EventDepositSessionExpired` | `event.deposit_session.expired` | Event — TTL elapsed with no payment |
| `EventDepositSessionSettled` | `event.deposit_session.settled` | Event — auto-convert completed |
| `EventDepositSessionFailed` | `event.deposit_session.failed` | Event — unrecoverable failure |

---

## Engine: `core/deposit/`

### `DepositSessionEngine`

```go
// core/deposit/engine.go

type Engine struct {
    store  DepositStore
    wallet HDWalletDeriving   // interface — derives addresses only, no private keys
}

// CreateDepositSession creates a new session and writes the first outbox entry (intent.monitor_address).
func (e *Engine) CreateDepositSession(ctx context.Context, tenantID uuid.UUID, req CreateSessionRequest) (*DepositSession, error)

// HandleTransactionDetected advances session to TRANSACTION_DETECTED.
// Validates the tx is for a known active session. Writes outbox entry for confirmation polling.
func (e *Engine) HandleTransactionDetected(ctx context.Context, sessionID uuid.UUID, tx IncomingTransaction) error

// HandleTransactionConfirmed advances session to TRANSACTION_CONFIRMED.
// Checks amount vs expected (underpayment/overpayment logic). Writes intent.credit_deposit outbox entry.
func (e *Engine) HandleTransactionConfirmed(ctx context.Context, sessionID uuid.UUID, txID uuid.UUID) error

// HandleDepositCredited advances session to CREDITED.
// If settlement_preference triggers auto-convert, writes intent.off_ramp outbox entry.
func (e *Engine) HandleDepositCredited(ctx context.Context, sessionID uuid.UUID) error

// HandleSessionExpiry checks if session should expire. Transitions to EXPIRED if no payment received.
// Transitions to LATE_PAYMENT if a tx was detected but not yet confirmed.
func (e *Engine) HandleSessionExpiry(ctx context.Context, sessionID uuid.UUID) error

// HandleSettlementCompleted transitions SETTLING → SETTLED.
func (e *Engine) HandleSettlementCompleted(ctx context.Context, sessionID uuid.UUID) error
```

All methods follow the same contract as `core.Engine`: they read current state, validate the transition, write new state + outbox entries atomically, and return. Zero network calls. Zero direct dependency on workers, ledger, or treasury.

---

## Worker: `node/worker/deposit_worker.go`

```go
type DepositWorker struct {
    engine        *deposit.Engine
    chainMonitor  chainmonitor.Monitor
    js            nats.JetStreamContext
}
```

The `DepositWorker` handles all `SETTLA_CRYPTO_DEPOSITS` messages:

| Message | Action |
|---|---|
| `intent.monitor_address` | Calls `chainMonitor.Subscribe(address, chain)`. Updates Redis active address cache. |
| `event.deposit_tx.detected` | Calls `engine.HandleTransactionDetected(...)`. Schedules confirmation polling. |
| `event.deposit_tx.confirmed` | Calls `engine.HandleTransactionConfirmed(...)`. |
| `intent.credit_deposit` | Validates idempotency key, calls ledger and treasury to credit merchant. Calls `engine.HandleDepositCredited(...)`. |
| `intent.unmonitor_address` | Calls `chainMonitor.Unsubscribe(address, chain)`. Removes from Redis cache. |
| Session expiry ticker | Polls DB for expired `PENDING_PAYMENT` sessions every 60 seconds. Calls `engine.HandleSessionExpiry(...)` for each. |

---

## ChainMonitor: `node/chainmonitor/`

This package is a direct port of `paydash_backend/crypto_monitor/internal/monitor/` adapted to publish outbox entries instead of RabbitMQ messages.

### Package structure

```
node/chainmonitor/
├── monitor.go          ← MonitorV2 orchestrator (port of paydash monitor_v2.go)
├── poller_evm.go       ← PollerV2 for EVM chains (port of paydash poller_v2.go)
├── poller_generic.go   ← GenericPoller for Tron (port of paydash generic_poller.go)
├── address_set.go      ← AddressSet + AddressSnapshot (port of paydash address_set.go)
├── token_registry.go   ← Lock-free token registry (port of paydash tokens/registry.go)
├── rpc_failover.go     ← FailoverManager + circuit breaker (port of paydash rpc/failover.go)
├── rpc_rate_limiter.go ← Token-bucket rate limiter (port of paydash rpc/rate_limiter.go)
└── config.go           ← Chain config loader (adapted from paydash config/mainnet.yaml)
```

### Key interfaces

```go
// node/chainmonitor/monitor.go

// IncomingTransaction is published to SETTLA_CRYPTO_DEPOSITS when detected.
// Mirrors paydash DepositMessage but uses shopspring/decimal for amounts.
type IncomingTransaction struct {
    TxHash        string
    Chain         Chain
    FromAddress   string
    ToAddress     string
    TokenAddress  string          // empty for native transfers
    TokenSymbol   string
    Amount        decimal.Decimal // never float64
    Decimals      int
    BlockNumber   int64
    BlockHash     string
    DetectedAt    time.Time
}

// Monitor manages all chain pollers and the address set.
// Difference from paydash: no RabbitMQ publisher — outbox entries are written
// directly to Postgres in the same tx as crypto_deposit_transactions, then
// picked up by the existing node/outbox.Relay.
type Monitor struct {
    addressSet    *AddressSet
    tokenRegistry *TokenRegistry
    pollers       map[Chain]Poller
    store         DepositStore
    outboxStore   OutboxStore
}
```

### Deposit detection loop (EVM — PollerV2)

Adapted from `paydash_backend/crypto_monitor/internal/monitor/poller_v2.go`:

1. Capture immutable `AddressSnapshot` at start of poll cycle.
2. Call `eth_getBlockNumber` via Alchemy to get latest block.
3. Chunk `[lastCheckpointBlock+1 .. latestBlock]` into batches of `batchSize` (default 10).
4. For each batch:
   - Call `eth_getLogs` with ERC-20 Transfer topic + recipient address filter (against snapshot).
   - Scan raw block transactions for native transfers (ETH/MATIC/etc.) with `tx.To` in snapshot.
5. For each matching transfer:
   - Check `UNIQUE(tx_hash, chain)` in `crypto_deposit_transactions` — skip if already recorded (idempotency).
   - `BEGIN` → insert `crypto_deposit_transactions` row + insert `outbox` entry (same as Settla's standard outbox pattern) → `COMMIT`.
6. Update `block_checkpoints` with new `last_block` + `last_hash`.
7. On next cycle: if stored `last_hash` ≠ actual block hash at that number → reorg detected → roll back `reorgDepth` blocks (chain-specific), un-publish affected transactions.

### Token registry

Lock-free design ported from `paydash_backend/crypto_monitor/internal/tokens/registry.go`:

```go
// Uses atomic.Value for zero-lock reads during reload
type TokenRegistry struct {
    tokens atomic.Value // holds map[chain]map[tokenAddress]*Token
}

// Reloaded from tokens table every 5 minutes (same as paydash)
// Native tokens stored with empty-string address key
// Unknown tokens quarantined for manual review (same as paydash "unknown_token" status)
```

### Chain configuration

Adapted from `paydash_backend/crypto_monitor/internal/config/mainnet.yaml`:

```yaml
# node/chainmonitor/config/chains.yaml
chains:
  ethereum:
    type: evm
    coin_type: 60
    poll_interval_seconds: 12
    confirmations: 12
    batch_size: 10
    reorg_depth: 20
    providers:
      - name: alchemy
        url: https://eth-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}
        rate_limit_cu_per_sec: 300
      - name: infura_fallback
        url: https://mainnet.infura.io/v3/${INFURA_PROJECT_ID}
        rate_limit_cu_per_sec: 100
  polygon:
    type: evm
    coin_type: 60
    poll_interval_seconds: 2
    confirmations: 128
    batch_size: 10
    reorg_depth: 50
    providers:
      - name: alchemy
        url: https://polygon-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}
  base:
    type: evm
    coin_type: 60
    poll_interval_seconds: 2
    confirmations: 15
    batch_size: 10
    providers:
      - name: alchemy
        url: https://base-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}
  tron:
    type: tron
    coin_type: 195
    poll_interval_seconds: 3
    confirmations: 19
    providers:
      - name: trongrid
        url: https://api.trongrid.io
      - name: tronstack
        url: https://api.tronstack.io
```

---

## HD Wallet: `crypto/wallet/`

Port the chain-specific generators from `paydash_backend/crypto_service/internal/address/generator.go`. The BIP-44 coin types used in paydash are identical to Settla's needs:

| Chain | BIP-44 coin type | Address format | paydash generator |
|---|---|---|---|
| Ethereum / EVM chains | 60 | EIP-55 checksummed hex | `address/generator.go` (ETH) |
| Tron | 195 | Base58Check | `address/generator.go` (Tron) |
| Solana | 501 | Base58 | `address/generator.go` (Solana) |
| Bitcoin | 0 | Bech32 (P2WPKH) | `address/generator.go` (BTC) |

Note: All EVM-compatible chains (Ethereum, Polygon, Base, Arbitrum, Optimism) share `coin_type=60` and produce the same address from the same derivation path. A single derived address works across all EVM chains simultaneously. Only the `chain` column in `crypto_deposit_sessions` distinguishes which network is being monitored.

```go
// crypto/wallet/hd.go

type HDWalletDeriving interface {
    // DeriveAddress returns the deposit address for the given derivation path.
    // Does NOT return or expose the private key.
    // Port of paydash crypto_service/internal/address/generator.go Generate()
    DeriveAddress(path DerivationPath, chain Chain) (string, error)

    // NextPath atomically increments and returns the next session index for a tenant+chain.
    // Uses SELECT FOR UPDATE on crypto_deposit_address_index (same approach as paydash derivation_index).
    NextPath(ctx context.Context, tenantID uuid.UUID, chain Chain) (DerivationPath, error)
}

type DerivationPath struct {
    Purpose    uint32 // 44 (BIP-44)
    CoinType   uint32 // 60 = EVM, 195 = Tron, 501 = Solana, 0 = BTC
    TenantIdx  uint32 // stable, assigned once at onboarding, never reused
    SessionIdx uint64 // monotonically incrementing per tenant per chain
}

func (p DerivationPath) String() string {
    return fmt.Sprintf("m/44'/%d'/%d'/%d", p.CoinType, p.TenantIdx, p.SessionIdx)
}
```

The `HDWallet` implementation calls the `SigningService` via internal gRPC for address derivation. It never has direct access to key material.

---

## Signing Service: `crypto/signing/`

The `SigningService` is a separate internal gRPC service, the sole holder of private key material.

```go
// Internal gRPC only — never exposed externally

service SigningService {
  rpc DeriveAddress    (DeriveAddressRequest)     returns (DeriveAddressResponse);
  rpc SignTransaction  (SignTransactionRequest)    returns (SignTransactionResponse);
  rpc GetPublicKey     (GetPublicKeyRequest)       returns (GetPublicKeyResponse);
}
```

**Key management** (port from `paydash_backend/crypto_service/internal/keymgmt/`):

The paydash `keymgmt` package provides a pluggable `KeyStore` interface with two ready implementations:

| Environment | Backend | paydash source |
|---|---|---|
| Development | `EnvStore` — reads master seed from `SETTLA_SIGNING_MASTER_SEED` env var | `keymgmt/env_store.go` |
| Staging | `FileStore` — AES-256-GCM encrypted file on disk, key from env | `keymgmt/file_store.go` |
| Production | Swap to AWS KMS or HashiCorp Vault Transit (not yet in paydash; new implementation) | — |

Both paydash stores implement `Closeable` — on shutdown they zero the in-memory key material via `runtime.KeepAlive` + explicit zero pass. Port this behaviour unchanged.

**Operational controls:**
- All signing operations are logged to an immutable audit log (append-only table, separate DB).
- The `SigningService` enforces a per-tenant, per-chain rate limit on address derivation.
- The `SigningService` has no inbound connections from outside the Settla cluster.
- Horizontal scaling: stateless, multiple replicas safe (key is fetched from KMS per operation).

---

## ID and Key Generation at Scale

At 50M transactions/day there are three separate ID/key generation concerns, each with different characteristics and failure modes. They must be designed independently.

---

### 1. Entity IDs — deposit sessions and transactions

**Problem:** `gen_random_uuid()` (UUID v4) produces random 128-bit values. Postgres B-tree indexes on random UUIDs suffer page fragmentation and poor cache locality at high insert rates because each new row lands in a random leaf page rather than the rightmost page.

**Scale math:** 580 deposit sessions/sec sustained, ~1,160 rows/sec across `crypto_deposit_sessions` + `crypto_deposit_transactions`. This is within tolerable range for UUID v4, but as volume grows the write amplification compounds.

**Decision:** Use **UUID v7** (time-ordered UUID, RFC 9562) for all deposit entity IDs. UUID v7 embeds a millisecond-precision Unix timestamp in the most significant bits, so inserts are naturally monotonically increasing. This gives sequential leaf-page writes — the same cache-friendly access pattern as a `BIGSERIAL`.

```sql
-- Postgres 17+: gen_random_uuid() v7 natively
-- Postgres 14-16: use pg_uuidv7 extension or generate in application layer

-- Application layer (Go):
// Use github.com/google/uuid v1.6+ which supports UUIDv7
id, _ := uuid.NewV7()
```

UUID v7 is a drop-in replacement — the column type stays `UUID`, existing indexes work, no schema changes needed. The only difference is the value distribution.

**For `crypto_deposit_transactions` (potentially 2–3 rows per session):** Same UUID v7 strategy. The `UNIQUE(tx_hash, chain)` index uses text columns, so UUID strategy has no bearing on that index's performance.

---

### 2. HD derivation index — the real throughput bottleneck

**Problem:** The initial design uses `SELECT FOR UPDATE` on `crypto_deposit_address_index.last_session_index` to atomically increment the derivation counter per `(tenant_id, chain)`. This serialises all session creation requests for a given tenant on a given chain through a single row lock.

**Scale math at peak:**
```
50M sessions/day ÷ 86,400 seconds = 578 sessions/sec total
At 50 tenants: ~11.6 sessions/sec per tenant per chain (average)
At peak (5,000 TPS): 100 sessions/sec per tenant (concentrated burst)
```

100 sequential lock acquisitions per second on a single Postgres row is survivable but adds measurable latency under bursts. More critically, it means session creation latency is directly tied to DB row lock wait time.

**Solution: Postgres `SEQUENCE` + address pool pre-generation**

Replace the `SELECT FOR UPDATE` counter with two complementary mechanisms:

**a) Postgres SEQUENCE per (tenant, chain):**

```sql
-- One sequence created at tenant onboarding per supported chain
-- e.g.: CREATE SEQUENCE deposit_idx_seq_tenant_abc_eth START 1;
-- Sequences are non-transactional — NEXTVAL never blocks, never rolls back
-- At 100 NEXTVAL/sec per sequence, Postgres sequences have no measurable overhead
```

Sequences are the fastest way to get a monotonically incrementing integer in Postgres. They are not locked like rows — `NEXTVAL` is O(1) regardless of concurrent callers.

**b) Address pool table (pre-generated, hot-dispense):**

At the scale of 50M sessions/day (~578/sec), deriving each address on-demand from the `SigningService` (which involves a BIP-32 HMAC-SHA512 + optional KMS call) at session creation time adds latency in the hot path. The solution used at production crypto exchanges is an **address pool**:

```sql
CREATE TABLE crypto_address_pool (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    chain           TEXT NOT NULL,
    address         TEXT NOT NULL,
    derivation_path TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'AVAILABLE',  -- AVAILABLE, ASSIGNED, RETIRED
    assigned_to     UUID,           -- session_id once assigned
    assigned_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id)
);

CREATE UNIQUE INDEX ON crypto_address_pool (tenant_id, chain, address);
CREATE INDEX ON crypto_address_pool (tenant_id, chain, status)
    WHERE status = 'AVAILABLE';  -- partial index for fast pool queries
```

**Pool dispense at session creation (hot path):**
```sql
-- Atomic claim: no lock contention, SKIP LOCKED handles concurrency
UPDATE crypto_address_pool
SET status = 'ASSIGNED', assigned_to = $session_id, assigned_at = NOW()
WHERE id = (
    SELECT id FROM crypto_address_pool
    WHERE tenant_id = $tenant_id AND chain = $chain AND status = 'AVAILABLE'
    ORDER BY created_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING address, derivation_path;
```

`SKIP LOCKED` means concurrent session creations never block each other — each picks a different available row. This is the same pattern Settla uses elsewhere (e.g., outbox relay). No signing service call in the session creation hot path.

**Pool refill background job** (runs every 60 seconds, or when pool level drops below threshold):
```
1. Query pool depth per (tenant_id, chain): COUNT(*) WHERE status = 'AVAILABLE'
2. If depth < low_watermark (e.g., 500):
   a. Get next N derivation paths from SEQUENCE (NEXTVAL × N — instant, non-blocking)
   b. Call SigningService.DeriveAddress(path) for each — batched gRPC call
   c. INSERT all derived addresses into crypto_address_pool in a single bulk INSERT
3. Alert if pool depth hits critical_watermark (e.g., 50) — refill is lagging
```

**Pool sizing:**
```
Target pool depth per (tenant, chain): 1,000 addresses
Refill trigger (low watermark): 500
Critical alert (critical watermark): 50
Refill batch size: 600 (restores to 1,100 with headroom)

At 578 sessions/sec total across 50 tenants × 5 chains:
  ~2.3 sessions/sec per (tenant, chain) on average
  Pool of 1,000 addresses = ~7 minutes of runway before refill needed
  Refill batch of 600 derives in < 1 second (BIP-32 is fast; ~10k derivations/sec on modern hardware)
```

This means the `SigningService` is completely off the critical path for session creation. It runs only in the background refill job, which has generous time budgets.

---

### 3. The SigningService as a dedicated key management service

**Is a separate signing service necessary?** Yes, and it already fits Settla's architecture. But at this scale its role needs to be precisely scoped.

**What it does NOT need to do:**
- Generate session IDs or entity UUIDs (handled by UUID v7 in the application layer)
- Manage the derivation index counter (handled by Postgres SEQUENCE)
- Run in the hot path of session creation (address pool decouples it)

**What it DOES do:**
- Hold the master seed securely (KMS-backed in production)
- Run BIP-32 derivations in the background refill job
- Sign refund/sweep transactions (required when Settla sends funds back to a customer)

**Throughput requirement after pool introduction:**

| Operation | Rate | Latency budget |
|---|---|---|
| Address derivation (background refill) | ~600 derivations/batch, triggered every 60s | < 1 second per batch |
| Transaction signing (refunds/sweeps) | Rare — only on underpayment/overpayment resolution | < 5 seconds acceptable |
| Address derivation at peak burst | If pool runs dry: 100/sec emergency derivation | < 100ms per call |

This is well within the capacity of a single `SigningService` replica doing local BIP-32 derivation (no KMS round-trip needed for derivation, only for key access on startup). Multiple replicas can be run safely — all share the same master seed from KMS and produce identical outputs for the same derivation path.

**SigningService failure modes and mitigations:**

| Failure | Impact | Mitigation |
|---|---|---|
| SigningService crash | Refill job stops. Existing pool drains over ~7 min | Alert at critical watermark (50 addresses left); automatic restart via Kubernetes |
| KMS unavailable | SigningService cannot start / load key | Pool continues serving from existing addresses while KMS recovers; alert immediately |
| Pool fully exhausted | Session creation fails — cannot allocate address | Return `503 Service Unavailable` with `Retry-After: 60s`; never create a session without an address |
| Derivation path collision | Impossible — Postgres SEQUENCE is strictly monotonic; never recycles values | N/A |

---

### 4. Summary: no separate ID generation service needed

A dedicated distributed ID generation service (e.g., Twitter Snowflake, Sonyflake) is **not required** at Settla's scale for deposit sessions. The reasons:

| Concern | Settla's approach | Sufficient for 580/sec? |
|---|---|---|
| Unique entity IDs | UUID v7 (time-ordered, generated in application) | Yes — UUID v7 generates millions/sec with no coordination |
| Monotonic derivation index | Postgres SEQUENCE per (tenant, chain) | Yes — Postgres sequences handle 100k+ NEXTVAL/sec |
| Signing throughput in hot path | Removed from hot path via address pool | Yes — hot path is just a `SELECT FOR UPDATE SKIP LOCKED` |
| Address uniqueness | Enforced by `UNIQUE(tenant_id, chain, address)` + HD derivation path uniqueness | Yes — BIP-44 derivation is deterministic; SEQUENCE guarantees no path reuse |

A Snowflake-style service would only be warranted if Settla needed to generate IDs across multiple independent databases with no coordination. Since all deposit sessions write to a single Transfer DB cluster (behind PgBouncer), Postgres SEQUENCE + UUID v7 is the correct solution.

---

Confirmation requirements by chain (same values used in `paydash_backend/crypto_monitor/internal/config/mainnet.yaml`):

| Chain | Required confirmations | Approx. time | Reorg depth | Rationale |
|---|---|---|---|---|
| TRON (TRC-20) | 19 | ~57 seconds | 20 | TRON recommends 19 for finality |
| Ethereum (ERC-20) | 12 | ~2.4 minutes | 20 | Industry standard for ERC-20 |
| Polygon (ERC-20) | 128 | ~4.5 minutes | 50 | High reorg frequency; paydash uses 128 |
| Base (ERC-20) | 15 | ~30 seconds | 20 | OP Stack finality considerations |
| Arbitrum One | 1 | ~2 seconds | 10 | Sequencer finality; on-chain finality ~1 week but fraud proof window is separate |
| Optimism (ERC-20) | 1 | ~2 seconds | 10 | Same as Arbitrum; sequencer provides immediate finality |
| Solana SPL | 31 | ~15 seconds | 32 | One epoch (approximate) |

Configurable per tenant via `min_confirmations_{chain}` in tenant config. Tenants with enhanced KYC may reduce thresholds; higher-risk flows may increase them.

`reorg_depth` is the number of blocks the monitor rolls back when a block hash mismatch is detected (ported from paydash `PollerV2.reorgDepth`).

---

## Reconciliation

A new reconciliation check is added to `core/reconciliation/`:

**DepositReconciliation** (runs every 5 minutes, same cadence as existing checks):

1. **Session completeness**: every session in `CREDITED` or `SETTLED` state must have a corresponding ledger credit entry. Any discrepancy is flagged as a critical alert.
2. **Orphaned transactions**: on-chain transactions detected for Settla addresses that have no matching session record (e.g., payment to an expired address that somehow bypassed the LATE_PAYMENT flow). These are queued for manual review.
3. **Pending confirmation timeout**: sessions in `TRANSACTION_DETECTED` state for > 2 hours are flagged. Typically indicates a chain fork or network issue.
4. **Underpaid session cleanup**: sessions in `UNDERPAID` state for > 24 hours with no top-up trigger the `SIMPLE_REFUND` compensation flow automatically.
5. **Treasury-ledger balance check**: the sum of all tenant crypto treasury positions must equal the sum of all credited-but-not-yet-converted session amounts. Discrepancy triggers critical alert.

---

## Compensation Flows

Deposit sessions reuse the existing `core/compensation` flows:

| Scenario | Compensation flow |
|---|---|
| Underpayment, no top-up within TTL | `SIMPLE_REFUND` — sends `received_amount` back to `refund_address` |
| Overpayment | `MANUAL_REVIEW` — credit expected, flag delta for ops review |
| Late payment (arrives after expiry) | `MANUAL_REVIEW` — ops decides: credit, refund, or hold |
| Credit failure (ledger error) | `MANUAL_REVIEW` — funds held in suspense account |
| Unknown sender address | `MANUAL_REVIEW` — cannot auto-refund, ops contacts merchant |
| Chain reorg (tx reverted post-credit) | `REVERSE_ONRAMP` — reverses the ledger credit, flags for ops |

---

## Portal UI: Crypto Management

New pages in `dashboard/pages/`:

### `deposits.vue` — Deposit session management

- Live table of all deposit sessions with real-time status updates (WebSocket or polling).
- Per-session detail view: deposit address, QR code, on-chain tx links, confirmation progress bar, timeline.
- Create test session (dev/staging only).
- Session filters: status, chain, date range, settlement preference.

### `crypto-balances.vue` — Held crypto balance management

- Per-currency, per-chain balance cards.
- "Convert to fiat" button triggers `POST /v1/deposits/convert`.
- Conversion history table.
- Auto-convert threshold configuration per currency.

### `deposit-settings.vue` — Crypto payment configuration (per tenant)

- Enable/disable chains.
- Default settlement preference.
- Default session TTL.
- Payment tolerance basis points.
- Refund address.
- Minimum confirmations per chain.

---

## Observability

### Prometheus metrics (new)

| Metric | Type | Description |
|---|---|---|
| `settla_deposit_sessions_created_total` | Counter | Sessions created, labelled by chain, tenant |
| `settla_deposit_sessions_completed_total` | Counter | Sessions reaching CREDITED/SETTLED |
| `settla_deposit_sessions_expired_total` | Counter | Sessions that expired with no payment |
| `settla_deposit_sessions_failed_total` | Counter | Sessions in FAILED state |
| `settla_deposit_confirmation_duration_seconds` | Histogram | Time from detection to confirmation |
| `settla_deposit_session_duration_seconds` | Histogram | Time from creation to credit |
| `settla_deposit_amount_received_total` | Counter | Cumulative crypto received, labelled by currency/chain |
| `settla_chainmonitor_events_total` | Counter | Blockchain events processed, labelled by chain/status |
| `settla_chainmonitor_active_addresses` | Gauge | Number of currently watched addresses |
| `settla_signing_operations_total` | Counter | Signing service calls, labelled by operation/status |

### Alerts

| Alert | Threshold | Severity |
|---|---|---|
| Sessions stuck in TRANSACTION_DETECTED | > 2 hours | Warning |
| Sessions stuck in CREDITING | > 15 minutes | Critical |
| Reconciliation: treasury-ledger mismatch | Any | Critical |
| ChainMonitor events dropping | > 5% error rate over 5 min | Critical |
| Active address count unexpectedly drops | > 20% drop in < 1 min | Warning |
| SigningService error rate | > 1% over 5 min | Critical |

### Structured log fields

All deposit log lines include:
```
session_id, tenant_id, chain, tx_hash (where applicable), status, amount
```

---

## Security Considerations

### Double-spend / chain reorg protection

- Credits are only issued after `required_confirmations` (chain-dependent, see above).
- The `crypto_deposit_transactions` table tracks each tx hash uniquely (`UNIQUE(tx_hash, chain)`). A redelivered NATS event for the same tx_hash is a no-op.
- A background reconciliation job monitors for chain reorganisations by re-checking block finality for recent `CONFIRMED` transactions. If a previously confirmed block is orphaned, the `REVERSE_ONRAMP` compensation is triggered.

### Address reuse prevention

- HD derivation ensures each session gets a unique address.
- `crypto_deposit_address_index.last_session_index` is incremented atomically in the DB (SELECT FOR UPDATE) before each session is created.
- The same derivation path is never reused, even if a session is cancelled or expired.

### Input validation

- All amounts use `shopspring/decimal` — no float arithmetic.
- Chain and token values validated against a strict allowlist per tenant.
- Deposit addresses are validated for chain-specific format (Tron base58, ETH checksum) before being stored or activated.

### Tenant isolation

- `crypto_deposit_sessions.tenant_id` is set from the auth token, never from the request body.
- The active address Redis cache stores the session ID only; the full session (including tenant) is always fetched from DB before any credit operation.
- All SQLC-generated queries for deposit sessions require `tenant_id` as a parameter.

---

## Multi-Chain Rollout Plan

The implementation is designed for incremental chain rollout. All EVM chains (phases 1–4) share the same `PollerV2` implementation — only the Alchemy endpoint URL and confirmation depth differ.

| Phase | Chains | Tokens | RPC provider | paydash poller |
|---|---|---|---|---|
| 1 | Tron TRC-20 | USDT | TronGrid + Tron full node | `GenericPoller` (tron client) |
| 2 | Ethereum mainnet | USDT, USDC | Alchemy `eth-mainnet` | `PollerV2` |
| 3 | Polygon | USDT, USDC | Alchemy `polygon-mainnet` | `PollerV2` |
| 4 | Base | USDC | Alchemy `base-mainnet` | `PollerV2` |
| 5 | Arbitrum One | USDC | Alchemy `arb-mainnet` | `PollerV2` |
| 6 | Optimism | USDC | Alchemy `opt-mainnet` | `PollerV2` |
| 7 | Solana | USDC (SPL) | Alchemy `solana-mainnet` | `GenericPoller` (solana client) |

**What each new EVM chain requires** (phases 2–6):
1. One new entry in `chains.yaml` with the Alchemy endpoint URL, poll interval, confirmations, and reorg depth.
2. One new `min_confirmations_{chain}` column in tenant config.
3. Chain-specific address format validation (EVM chains share the same format; no code change needed for phases 3–6).
4. Updated `supported_chains` enum in DB and API.

**What adding Solana (phase 7) requires additionally:**
1. A new `SolanaClient` implementing the `blockchain.Client` interface (paydash has a partial implementation in `crypto_monitor/internal/blockchain/`).
2. BIP-44 `coin_type=501` in `DerivationPath` constants.
3. Solana address format validation (Base58, 32-byte public key).

No changes to the engine, outbox relay, or NATS dispatch logic are required for any new chain.

---

## Integration with Existing Settla Architecture

| Existing component | How it's used |
|---|---|
| `core.Engine` | `DepositSessionEngine` follows identical patterns — pure state machine, outbox-only side effects |
| `domain.OutboxEntry` | New intent/event types added; relay and routing unchanged |
| `node/outbox.Relay` | Unchanged — picks up new event types automatically via `SubjectForEventType` routing |
| `node/worker/ProviderWorker` | Handles `intent.off_ramp` emitted by `HandleDepositCredited` for AUTO_CONVERT sessions |
| `node/worker/LedgerWorker` | Handles `intent.credit_deposit` to post the ledger credit |
| `node/worker/TreasuryWorker` | Handles `intent.credit_deposit` to update treasury position |
| `api/webhook` inbound receiver | Extended to accept TronGrid / chain indexer webhooks alongside provider webhooks |
| `core/compensation` | Reused unchanged for underpayment, overpayment, and MANUAL_REVIEW flows |
| `core/reconciliation` | Extended with `DepositReconciliation` check |
| `store/transferdb` | New tables added; existing migrations pattern followed |
| `treasury` | Crypto positions held and managed alongside fiat positions |
| `rail` | Off-ramp path used for AUTO_CONVERT settlement; no rail changes needed |
| Tenant portal | New pages for deposit management and crypto balance |

---

## Open Questions / Future Considerations

1. **Custodial vs non-custodial model**: The current design is custodial (Settla holds keys on behalf of merchants). A future non-custodial mode could use merchant-provided addresses for monitoring-only, with Settla acting purely as an observer and settlement layer.
2. **NFT / non-fungible receipts**: Out of scope for this feature; would require a different session model.
3. **Lightning Network**: BTC Lightning requires a different address model (invoice-based, single-use payment hashes). Feasible as a future chain type.
4. **Merchant-to-merchant crypto transfers**: Enabling tenants to transfer crypto between Settla accounts without on-chain movement (internal ledger transfers). Reuses existing transfer engine.
5. **Regulatory reporting**: Crypto receipts may require VASP (Virtual Asset Service Provider) reporting in certain jurisdictions. This is a compliance concern, not an engineering one, but the audit log from the `SigningService` and the `crypto_deposit_transactions` table provide the raw data needed.
6. **Fee model**: Crypto payment sessions need a fee structure (flat fee per session, percentage of received amount, or both). This should be added to the per-tenant fee schedule alongside existing fiat fee configuration.
