# Settla Feature Roadmap

**Status:** Living document
**Date:** 2026-03-12
**Authors:** Engineering Team

This document catalogues proposed features beyond the core settlement engine and crypto payment gateway. Each feature is described with its product rationale, architectural fit within the existing Settla codebase, implementation approach, and complexity assessment.

Features are grouped by category. Within each category the highest-leverage items appear first.

---

## Summary Table

| Feature | Category | Business value | Arch fit | Complexity |
|---|---|---|---|---|
| Virtual IBANs / VANs | Collection | Very high | Excellent | Medium |
| Bulk Disbursements | Payouts | High | Excellent | Low–Medium |
| Payment Links | Collection | Medium–High | Low | Low |
| Recurring Collections | Collection | Medium–High | Good | Medium |
| Compliance Rules Engine | Risk | High | Medium | High |
| FX Rate Locking | Treasury | High | Medium | High |
| Yield on Idle Balances | Treasury | Medium–High | Medium | High |
| Multi-Party Escrow | Payments | Medium | Medium | Medium–High |
| Smart Routing API | Developer | Medium | Low | Low |
| Real-Time Streaming API | Developer | Medium | Good | Low–Medium |
| White-Label Tenant Portal | Platform | High | Good | Medium |
| Analytics & Reporting API | Platform | Medium | Good | Low |

---

## Category 1 — Collection

### 1.1 Virtual IBANs / Virtual Account Numbers (VANs)

**Status:** Proposed

#### Product description

Issue virtual bank account numbers to merchants for fiat collection. A merchant requests a virtual account (GBP sort code + account number, EUR IBAN, NGN NUBAN) and their customer pays into it via standard bank transfer. Settla detects the incoming credit, converts to stablecoin at the prevailing rate, and credits the merchant's Settla treasury — triggering the same settlement preference logic as the crypto gateway (auto-convert, hold, or threshold).

This is the fiat-entry-point mirror of the crypto payment gateway. Together they make Settla a complete collection platform: crypto on-chain and fiat via bank rails, both arriving at the same merchant treasury.

#### Why it matters

- Lemfi, Fincra, and Paystack all process large volumes of fiat-initiated payments. Customers paying via bank transfer far outnumber customers paying in crypto.
- Removes a major integration dependency — tenants currently need a separate banking-as-a-service provider (Modulr, ClearBank, Stitch) and must build their own reconciliation between that provider and Settla.
- Creates a unified settlement view: crypto deposits and fiat deposits land in the same tenant treasury, settled through the same off-ramp rail.

#### Architecture

The implementation is architecturally near-identical to the crypto payment gateway. Replace `ChainMonitor` with a `BankAccountListener` and the rest of the flow is unchanged:

```
Banking partner (Modulr / ClearBank / Stitch)
    │  webhook: incoming credit on virtual account
    ▼
api/webhook (inbound receiver)
    │  validates HMAC signature
    │  normalises to IncomingBankCredit struct
    │  publishes to SETTLA_BANK_DEPOSITS stream
    ▼
BankDepositWorker
    │  calls DepositSessionEngine.HandleBankCreditReceived(...)
    ▼
Engine writes state + outbox entries atomically
    │
    ├─► LedgerWorker (credit merchant)
    ├─► TreasuryWorker (update position)
    └─► ProviderWorker (if AUTO_CONVERT: trigger off-ramp rail)
```

#### New components

| Component | Location | Reuses |
|---|---|---|
| `BankAccountListener` | `node/banklistener/` | Same interface as `ChainMonitor` |
| `BankDepositWorker` | `node/worker/bank_deposit_worker.go` | Same pattern as `DepositWorker` |
| `virtual_account_sessions` table | `store/transferdb/` | Same schema shape as `crypto_deposit_sessions` |
| `SETTLA_BANK_DEPOSITS` NATS stream | `node/messaging/` | 9th stream, same config as other streams |
| Banking partner adapters | `node/banklistener/{modulr,clearbank,stitch}/` | Pluggable via `BankListener` interface |

#### Key design considerations

- **Virtual account allocation:** Banking partners pre-allocate pools of account numbers. Settla requests a VAN from the partner API at session creation and maps it to the tenant + session. Partners typically charge per-account, so a pool + recycle strategy (re-issue accounts after sessions close) is preferable to one account per session.
- **Partial payments:** Same handling as crypto — accumulate credits within the session window.
- **FX conversion:** The received fiat amount is converted to stablecoin at the rate locked at credit time (not session creation time). Rate quotes cached in Redis with a 30-second TTL.
- **Banking partner failover:** Multiple partners per currency corridor. `FailoverManager` pattern (from `paydash rpc/failover.go`) applied to banking partner API calls.
- **Reconciliation:** Partner sends a daily settlement report. `DepositReconciliation` gains a `BankDepositReconciliation` check that cross-references partner reports against `virtual_account_sessions`.

#### Collection fee model

Reuses the `CryptoFeeResolver` pattern from the crypto gateway. Adds `BankCollectionBPS` + `BankCollectionMaxFeeUSD` to `FeeSchedule`. The same global default + tenant-specific override + `bank_fee_exempt` flag pattern applies.

#### Complexity assessment

**Medium.** The Settla internals are essentially identical to the crypto gateway. The complexity is entirely in the banking partner integration: partner selection per corridor, account lifecycle management, reconciling partner settlement reports, and handling edge cases (duplicate credits, credit corrections, chargebacks). Legal/regulatory overhead for holding virtual accounts varies by jurisdiction.

---

### 1.2 Payment Links

**Status:** Proposed

#### Product description

A merchant calls the API to generate a shareable URL that pre-configures a deposit session. The link opens a hosted, Settla-served payment page showing the deposit address (crypto) or bank details (fiat), QR code, countdown timer, and real-time confirmation status. No frontend work required from the merchant.

```
POST /v1/payment-links
{
  "amount": "150.00",
  "currency": "USDT",
  "chain": "TRON_TRC20",
  "description": "Invoice INV-001",
  "expires_in_seconds": 1800,
  "redirect_url": "https://merchant.com/thank-you"
}

→ { "id": "lnk_abc", "url": "https://pay.settla.io/p/abc123", "expires_at": "..." }
```

#### Architecture

A thin layer on top of deposit sessions. A `payment_links` table stores the session configuration template. When a customer visits the URL, Settla creates a fresh deposit session from the template and renders the payment page.

```sql
CREATE TABLE payment_links (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    short_code      TEXT NOT NULL UNIQUE,     -- "abc123" in the URL
    session_config  JSONB NOT NULL,           -- amount, currency, chain, settlement_pref, etc.
    use_limit       INT,                      -- null = unlimited
    use_count       INT NOT NULL DEFAULT 0,
    expires_at      TIMESTAMPTZ,
    redirect_url    TEXT,
    status          TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The hosted payment page (`dashboard/pages/pay/[code].vue`) is a public-facing Nuxt page. It:
1. Calls `GET /v1/payment-links/:code` to get the template config.
2. Creates a deposit session from the config.
3. Polls (or subscribes via WebSocket) for session status updates.
4. On `CREDITED`, redirects to `redirect_url`.

#### Complexity assessment

**Low.** The heavy lifting is entirely in existing components. New code is the `payment_links` table, a `short_code` generator (NanoID), the creation endpoint, and the public payment page UI.

---

### 1.3 Recurring Collections

**Status:** Proposed

#### Product description

A merchant defines a recurring collection schedule — weekly, monthly, on a specific day of the month. Settla automatically creates deposit sessions on schedule and notifies the merchant via webhook when each session is created and when it completes or fails.

Use cases: SaaS subscription billing, loan repayments, insurance premiums, utility bills.

#### Architecture

```
RecurringScheduler (runs in settla-node, ticks every minute)
    │  queries recurring_schedules WHERE next_run_at <= now() AND status = 'ACTIVE'
    ▼
For each due schedule:
    engine.CreateDepositSession(session_config from schedule)
    update recurring_schedules SET next_run_at = calculate_next(schedule), last_run_at = now()
```

```sql
CREATE TABLE recurring_schedules (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    idempotency_prefix  TEXT NOT NULL,          -- base for per-run idempotency keys
    session_config      JSONB NOT NULL,          -- same shape as deposit session creation request
    schedule_type       TEXT NOT NULL,           -- DAILY, WEEKLY, MONTHLY, CRON
    cron_expression     TEXT,                    -- for CRON type
    day_of_month        INT,                     -- for MONTHLY (1–28; capped at 28 to avoid Feb issues)
    day_of_week         INT,                     -- for WEEKLY (0=Sun–6=Sat)
    timezone            TEXT NOT NULL DEFAULT 'UTC',
    retry_policy        JSONB NOT NULL DEFAULT '{"max_attempts": 3, "backoff_hours": 24}',
    status              TEXT NOT NULL DEFAULT 'ACTIVE',  -- ACTIVE, PAUSED, CANCELLED
    next_run_at         TIMESTAMPTZ NOT NULL,
    last_run_at         TIMESTAMPTZ,
    total_runs          INT NOT NULL DEFAULT 0,
    total_failed        INT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE recurring_schedule_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    schedule_id     UUID NOT NULL REFERENCES recurring_schedules(id),
    tenant_id       UUID NOT NULL,
    session_id      UUID,                   -- the created deposit session
    run_at          TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL,          -- CREATED, COMPLETED, FAILED, SKIPPED
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### Retry policy

When a created session expires unpaid (no payment received), the scheduler consults `retry_policy`:
- `max_attempts`: how many times to retry before marking the schedule run as permanently failed.
- `backoff_hours`: wait N hours between retries.
- On permanent failure: emit webhook `recurring.collection_failed`, pause the schedule, create a `ManualReview` record.

#### Complexity assessment

**Medium.** Core scheduling logic is straightforward. The complexity is in retry policy design, timezone handling (DST transitions, month boundary edge cases), and idempotency for the scheduler tick (multiple `settla-node` instances must not double-create sessions for the same schedule run — solved with `SELECT FOR UPDATE SKIP LOCKED` on due schedules).

---

## Category 2 — Payouts

### 2.1 Bulk / Batch Disbursements

**Status:** Proposed

#### Product description

A merchant submits a batch of up to 10,000 recipient transfers in a single API call (JSON body or CSV upload). Settla fans them out as individual transfers, processes them concurrently through the existing engine, and provides real-time aggregate progress and per-item status.

Use cases: payroll runs, gig economy weekly payouts, mass loan disbursements, loyalty reward distributions, dividend payments.

```
POST /v1/batches
{
  "idempotency_key": "payroll-2026-03-12",
  "description": "March payroll",
  "items": [
    { "idempotency_key": "item-001", "recipient": {...}, "amount": "2500.00", "currency": "GBP" },
    { "idempotency_key": "item-002", "recipient": {...}, "amount": "1800.00", "currency": "NGN" },
    ...
  ]
}

→ {
    "id": "bat_xyz",
    "status": "PROCESSING",
    "total_items": 1000,
    "completed": 0,
    "failed": 0,
    "total_amount_usd": "2450000.00"
  }
```

#### Architecture

A `BatchTransfer` entity that decomposes into N individual `Transfer` entities. Each child transfer runs through the existing engine independently — no new transfer logic needed.

```sql
CREATE TABLE batch_transfers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    idempotency_key     TEXT NOT NULL,
    description         TEXT,
    status              TEXT NOT NULL DEFAULT 'PROCESSING',
    total_items         INT NOT NULL,
    completed_items     INT NOT NULL DEFAULT 0,
    failed_items        INT NOT NULL DEFAULT 0,
    total_amount_usd    NUMERIC(28,8),
    failure_policy      TEXT NOT NULL DEFAULT 'CONTINUE',  -- CONTINUE or HALT_ON_FAILURE
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX ON batch_transfers (tenant_id, idempotency_key);

CREATE TABLE batch_transfer_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id        UUID NOT NULL REFERENCES batch_transfers(id),
    tenant_id       UUID NOT NULL,
    transfer_id     UUID,           -- set once the child Transfer is created
    idempotency_key TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING',
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Ingestion:** Large batches (>100 items) are processed asynchronously. The API returns `202 Accepted` immediately. A `BatchWorker` in `settla-node` reads `PENDING` items in pages of 100, creates individual transfers via the engine, and updates progress counters.

**Failure policy:**
- `CONTINUE` (default): failed items are marked as failed and recorded; remaining items proceed.
- `HALT_ON_FAILURE`: first failure pauses the batch. Ops resolves the failure and resumes. Suitable for payroll runs where all-or-nothing is required.

**Pre-flight validation:** Before processing, Settla validates all items (recipient formats, currency support, per-item limits) and returns a validation report. The batch does not start until the merchant confirms.

**Treasury check:** Before the batch starts, Settla checks whether the tenant has sufficient treasury balance to cover the total batch amount. If not, the batch is held until the balance is sufficient (event-driven: treasury top-up webhook triggers batch resumption).

#### Complexity assessment

**Low–Medium.** Individual transfer logic is unchanged. New code is the batch table, `BatchWorker`, and the pre-flight validation endpoint. The interesting design decisions are the failure policy, treasury pre-check, and idempotency for the worker (partial batch restarts must not re-create transfers already created).

---

## Category 3 — Risk & Compliance

### 3.1 Programmable Compliance Rules Engine

**Status:** Proposed

#### Product description

A configurable rules engine that evaluates every transfer and deposit session against a set of compliance rules before it proceeds. Rules can be system-wide (Settla's own AML obligations) or per-tenant (the tenant's own compliance policies). Outcomes are `ALLOW`, `REVIEW` (manual review queue), or `BLOCK` (hard rejection).

Rule types:
- **Sanctions screening:** Check sender/recipient names and crypto addresses against OFAC, EU, UN, and HMT lists.
- **Crypto address screening:** Check wallet addresses against known mixers, darknet markets, exchange hack outputs (Chainalysis / TRM Labs integration).
- **Velocity rules:** N transactions per hour/day per customer; cumulative amount thresholds.
- **Counterparty country restrictions:** Block or flag transfers involving specified jurisdictions.
- **Amount thresholds:** Transfers above $X trigger enhanced due diligence or `REVIEW`.
- **New account rules:** First transfer from an unverified counterparty triggers `REVIEW`.

#### Architecture

`ComplianceEngine` runs as a synchronous gate in `engine.CreateTransfer` and `engine.CreateDepositSession`. It evaluates all applicable rules, writes a `compliance_checks` record with the outcome, and either allows the transfer to proceed or returns an error.

```go
// core/compliance/engine.go

type ComplianceEngine interface {
    Evaluate(ctx context.Context, req ComplianceRequest) (ComplianceOutcome, error)
}

type ComplianceRequest struct {
    TenantID        uuid.UUID
    TransferID      uuid.UUID
    Direction       string          // "inbound" | "outbound"
    Amount          decimal.Decimal
    Currency        string
    SenderAddress   string
    RecipientAddress string
    SenderCountry   string
    RecipientCountry string
    Metadata        map[string]string
}

type ComplianceOutcome struct {
    Decision     string   // "ALLOW" | "REVIEW" | "BLOCK"
    MatchedRules []string // rule IDs that triggered
    Reason       string   // human-readable explanation for REVIEW/BLOCK
}
```

```sql
CREATE TABLE compliance_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,           -- NULL = system-wide rule; set = tenant-specific
    rule_type       TEXT NOT NULL,  -- SANCTIONS, VELOCITY, COUNTRY_BLOCK, AMOUNT_THRESHOLD, etc.
    rule_config     JSONB NOT NULL, -- rule-type-specific configuration
    outcome         TEXT NOT NULL,  -- ALLOW, REVIEW, BLOCK
    is_active       BOOLEAN NOT NULL DEFAULT true,
    priority        INT NOT NULL DEFAULT 100, -- lower = evaluated first
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE compliance_checks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    aggregate_type  TEXT NOT NULL,  -- "transfer" | "deposit_session"
    aggregate_id    UUID NOT NULL,
    decision        TEXT NOT NULL,
    matched_rules   JSONB NOT NULL DEFAULT '[]',
    reason          TEXT,
    screened_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Sanctions list management:** Lists are synced daily from OFAC, EU, and HMT APIs into a `sanctions_entries` table. A fuzzy name-matching function (trigram index on `pg_trgm`) handles name variants. Crypto address lists are updated continuously from Chainalysis or TRM Labs via webhook.

**`REVIEW` outcome:** Creates a `ManualReview` record (reusing the existing `core/compensation` flow). The transfer is paused at `COMPLIANCE_REVIEW` state. Ops approves or rejects via the dashboard. On approval, the engine continues from the paused state.

#### Complexity assessment

**High.** The rule evaluator itself is straightforward. The complexity is in sanctions list management (keeping lists current and legally defensible), false positive rates (fuzzy name matching produces noise), the legal liability of screening decisions, and building the ops workflow for `REVIEW` outcomes. External screening API integration (Chainalysis, TRM Labs) adds cost and a new external dependency. This feature requires legal review before deployment in any regulated jurisdiction.

---

## Category 4 — Treasury

### 4.1 FX Rate Locking / Forward Contracts

**Status:** Proposed

#### Product description

A merchant requests a locked exchange rate for a corridor (e.g., USDT→GBP at 0.7912) valid for a specified window (5 minutes to 24 hours). Settla guarantees that rate for any transfer submitted within the window, up to a specified volume cap. The merchant can quote their customer a fixed price, then call Settla within the lock window knowing the rate is guaranteed.

Today `GET /v1/quotes` returns an advisory quote that is not a commitment — the rate may have moved by the time the transfer executes. Rate locking turns the quote into a contractual commitment.

#### Architecture

Extends the existing `Quote` system in `core/`:

```sql
CREATE TABLE rate_locks (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    idempotency_key     TEXT NOT NULL,
    from_currency       TEXT NOT NULL,
    to_currency         TEXT NOT NULL,
    locked_rate         NUMERIC(28,12) NOT NULL,
    valid_until         TIMESTAMPTZ NOT NULL,
    volume_cap_usd      NUMERIC(28,8) NOT NULL,   -- max total transfer volume at this rate
    used_volume_usd     NUMERIC(28,8) NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'ACTIVE',  -- ACTIVE, EXHAUSTED, EXPIRED
    provider_hedge_id   TEXT,                     -- reference to the hedge leg with banking partner
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

When `engine.CreateTransfer` receives a `rate_lock_id`, it:
1. Validates the lock is `ACTIVE` and `valid_until > now()`.
2. Validates the transfer amount does not exceed `volume_cap_usd - used_volume_usd`.
3. Atomically increments `used_volume_usd` (SELECT FOR UPDATE).
4. Routes the transfer using the locked rate, bypassing the normal quote fetch.

**Hedging:** Settla is exposed to FX risk for the duration of the lock window. Two strategies:
- **Back-to-back hedge:** On lock creation, immediately execute a forward contract with a banking partner (Currencycloud, Ebury) for the volume cap. Guaranteed margin but locks capital.
- **Net position management:** Accumulate locked positions across tenants, hedge the net exposure periodically. Lower capital requirement but requires an active treasury/risk function.

The `provider_hedge_id` field references the hedge leg. If hedging fails, lock creation fails — Settla never issues an unhedged lock.

#### Complexity assessment

**High.** The Settla internals (lock table, rate validation in the engine) are Medium complexity. The hard part is the hedging strategy: banking partner API integration, hedge lifecycle management, and the risk framework for managing unhedged windows. Requires a treasury/risk function and legal review of forward contract obligations. Recommended initial scope: short lock windows (≤ 15 minutes) at a spread that absorbs the unhedged risk, with full hedging infrastructure added in a follow-on phase.

---

### 4.2 Yield on Idle Balances

**Status:** Proposed

#### Product description

Merchants who hold crypto balances in Settla (via the `HOLD` or `THRESHOLD` settlement preference) can opt in to earn yield on those balances. Settla deploys idle stablecoin into yield-bearing strategies and credits daily yield accruals to the merchant's Settla account.

Initial strategies (in order of risk/complexity):
1. **Coinbase Prime / Anchorage institutional yield** — USDC lending, ~4.5–5.5% APY. Off-chain, regulated, instant redemption.
2. **Tokenised money-market funds** — BlackRock BUIDL, Ondo OUSG, Franklin OnChain. On-chain, regulated, T+0 or T+1 redemption.
3. **DeFi lending** — Aave, Compound. On-chain, higher yield, smart contract risk.

Settla takes a spread (e.g., gross yield 5.2% → merchant receives 4.7%, Settla retains 50 bps).

#### Architecture

```
YieldManager (runs in settla-node, daily at 02:00 UTC)
    │
    ├─► Query opted-in tenant balances from treasury
    ├─► Calculate allocatable balance (balance - reserve for 24h expected withdrawals)
    ├─► Deploy excess to yield strategy via provider API
    ├─► Record deployment in yield_positions table
    │
    └─► Daily accrual job (03:00 UTC):
        ├─► Fetch accrued yield from provider
        ├─► Post ledger credit per tenant (yield_amount to tenant account)
        └─► Emit event.yield_credited webhook
```

```sql
CREATE TABLE yield_positions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    strategy        TEXT NOT NULL,      -- COINBASE_PRIME, BUIDL, AAVE_USDC
    currency        TEXT NOT NULL,
    deployed_amount NUMERIC(36,18) NOT NULL,
    current_value   NUMERIC(36,18) NOT NULL,
    apy_at_deposit  NUMERIC(10,6),
    deployed_at     TIMESTAMPTZ NOT NULL,
    redeemed_at     TIMESTAMPTZ,
    status          TEXT NOT NULL DEFAULT 'ACTIVE'
);

CREATE TABLE yield_accruals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    position_id     UUID REFERENCES yield_positions(id),
    currency        TEXT NOT NULL,
    gross_yield     NUMERIC(36,18) NOT NULL,
    settla_spread   NUMERIC(36,18) NOT NULL,
    net_yield       NUMERIC(36,18) NOT NULL,
    accrual_date    DATE NOT NULL,
    ledger_entry_id UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Liquidity management:** Settla must always be able to honour merchant withdrawals. The `YieldManager` maintains a liquidity reserve: only balances exceeding `(30-day average daily withdrawal × safety_factor)` are deployed. Redemption requests from the strategy are initiated automatically when a merchant initiates a large withdrawal.

#### Complexity assessment

**High.** Yield strategy integration (provider APIs, on-chain transaction signing), liquidity risk management, accounting for unrealised gains, tax reporting implications for merchants, and regulatory classification (is this a collective investment scheme?). The Coinbase Prime strategy is the simplest starting point — off-chain API, no smart contract risk, established regulatory framework. DeFi strategies should be a separate phase with a full risk assessment.

---

## Category 5 — Payments

### 5.1 Multi-Party Settlement / Escrow

**Status:** Proposed

#### Product description

A transfer involving three parties: a buyer, a seller, and a platform (marketplace operator). Funds move from buyer → escrow → seller, with the platform taking a fee. The release from escrow to seller is conditional on a configurable trigger: time-based, event-based (merchant confirms delivery), or ops approval.

Use cases: B2B marketplace settlements, trade finance, logistics platforms, freelance payments.

```
POST /v1/escrow
{
  "idempotency_key": "escrow-001",
  "buyer_tenant_id": "...",
  "seller_tenant_id": "...",
  "amount": "5000.00",
  "currency": "USDT",
  "platform_fee_bps": 100,
  "release_condition": {
    "type": "TIME",
    "release_at": "2026-03-19T00:00:00Z"
  }
}
```

#### Architecture

A new transfer type `ESCROW` with its own state machine:

```
FUNDED → HELD → RELEASING → RELEASED (terminal)
                          → DISPUTED  → RESOLVED_RELEASE
                                      → RESOLVED_REFUND
```

Ledger postings:
- On `FUNDED`: DEBIT buyer account → CREDIT escrow suspense account.
- On `RELEASED`: DEBIT escrow suspense → CREDIT seller account (net of platform fee) + CREDIT platform fee account.
- On `RESOLVED_REFUND`: DEBIT escrow suspense → CREDIT buyer account.

Fee splitting at release is a multi-posting ledger entry — no new ledger concepts needed, just more entries in the same TigerBeetle batch.

**Release condition evaluator** (new component in `core/escrow/`):
- `TIME`: a background goroutine polls for held escrows whose `release_at <= now()`.
- `EVENT`: the seller calls `POST /v1/escrow/:id/confirm-delivery`; Settla verifies the caller is the seller tenant and triggers release.
- `OPS`: release requires ops approval via the dashboard (reuses `ManualReview` flow).

#### Complexity assessment

**Medium–High.** State machine and ledger entries are straightforward. The complexity is in dispute resolution (what triggers a dispute, who has authority to resolve it, and what the legal implications are) and partial release (releasing 50% on milestone 1, 50% on milestone 2). Start with simple single-release escrow and time-based triggers only.

---

## Category 6 — Developer Experience

### 6.1 Smart Routing API (Public)

**Status:** Proposed

#### Product description

Expose Settla's route scoring engine as a standalone query endpoint. A caller can request the ranked provider list for a given corridor and amount without creating a transfer — useful for pricing estimators, treasury dashboards, and audit tooling.

```
POST /v1/routes
{
  "from_currency": "GBP",
  "to_currency": "USDT",
  "amount": "10000.00"
}

→ {
    "routes": [
      {
        "provider": "provider_a",
        "score": 0.87,
        "estimated_fee_usd": "4.50",
        "estimated_settlement_seconds": 45,
        "score_breakdown": {
          "cost": 0.92,
          "speed": 0.88,
          "liquidity": 0.81,
          "reliability": 0.90
        }
      },
      ...
    ],
    "quoted_at": "2026-03-12T10:00:00Z",
    "valid_for_seconds": 30
  }
```

#### Architecture

The router already exists in `rail/router/router.go`. This endpoint is a thin REST wrapper over `router.GetQuote(ctx, tenantID, QuoteRequest)`. It is rate-limited per tenant (to prevent abuse of provider quote APIs).

```go
// api/grpc/server.go — new RPC method
rpc GetRoutingOptions(GetRoutingOptionsRequest) returns (GetRoutingOptionsResponse);
```

The gateway exposes it at `POST /v1/routes`. No new business logic; the work is entirely in the API layer.

#### Complexity assessment

**Low.** The router is already built and tested. This is a one-day implementation.

---

### 6.2 Real-Time Streaming API (WebSocket / SSE)

**Status:** Proposed

#### Product description

Merchants subscribe to a real-time event stream for their tenant. Every transfer status change, deposit session update, and webhook event is pushed to the connected client without polling.

```
WebSocket: wss://api.settla.io/v1/stream
  Authorization: Bearer sk_live_xxx

← { "type": "transfer.status_changed", "transfer_id": "...", "status": "COMPLETED", ... }
← { "type": "deposit_session.credited", "session_id": "...", "credited_amount": "149.25", ... }
← { "type": "batch.progress", "batch_id": "...", "completed": 450, "total": 1000, ... }
```

#### Architecture

The gateway (`api/gateway`) opens a NATS subscription to `settla.transfer.partition.{N}.>` and `settla.deposit.>` filtered to the authenticated tenant's partition. Messages are forwarded to the WebSocket connection in real time. Fastify 5 supports WebSocket natively via `@fastify/websocket`.

```
Merchant client (WebSocket)
    ↑
Fastify WebSocket handler
    ↑
NATS subscription: settla.*.partition.{tenantHash}.*
    ↑
Existing NATS JetStream (no changes)
```

No new event infrastructure needed — the gateway is already connected to NATS. WebSocket connections are stateless from the server's perspective (NATS does the fan-out). Multiple gateway replicas each serve their own WebSocket clients from the same NATS subjects.

Server-Sent Events (SSE) is offered as a fallback for clients that cannot use WebSocket (some corporate firewalls block WebSocket upgrades). Same NATS subscription, different transport.

#### Connection management
- Heartbeat ping every 30 seconds; disconnect client on missed pong.
- On reconnect, client provides `last_event_id`; gateway replays events from the NATS JetStream consumer's sequence. Replay window: 10 minutes.
- Maximum 5 concurrent WebSocket connections per tenant (configurable).

#### Complexity assessment

**Low–Medium.** NATS subscription and WebSocket framing are straightforward. The nuance is in replay-on-reconnect (requires a durable NATS consumer per WebSocket session, which has memory implications at scale) and connection limits. An alternative for replay is to replay from the DB rather than NATS, which is simpler and avoids durable consumer proliferation.

---

## Category 7 — Platform

### 7.1 White-Label Tenant Portal

**Status:** Proposed (partially planned — `portal/` directory exists in codebase)

#### Product description

A brandable self-service portal that Settla's tenants deploy for their own end customers. A fintech (Lemfi, Fincra) configures their logo, colours, and custom domain. Their customers interact with Lemfi's branded portal — but the underlying infrastructure is Settla.

Features exposed to end customers (configurable per tenant):
- Transfer history and status tracking
- Deposit address management (crypto gateway)
- API key management (for customers who integrate programmatically)
- Fee schedule view
- Webhook configuration
- Account settings

#### Architecture

The `portal/` directory and `proto/settla/v1/tenant_portal.proto` are already present in the codebase. The Portal gRPC service (`api/grpc/tenant_portal_service.go`) exists. The work is completing the Vue 3 / Nuxt frontend and the branding configuration system.

**Branding configuration:**

```sql
CREATE TABLE tenant_portal_config (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL UNIQUE REFERENCES tenants(id),
    logo_url        TEXT,
    primary_colour  TEXT NOT NULL DEFAULT '#6366F1',
    company_name    TEXT NOT NULL,
    custom_domain   TEXT UNIQUE,    -- e.g. "pay.lemfi.com"
    enabled_features TEXT[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Custom domains:** Settla issues a wildcard TLS certificate (`*.portal.settla.io`). Tenants with custom domains configure a CNAME `pay.lemfi.com → portals.settla.io`. The Nuxt app reads the `Host` header to look up the tenant and load their branding config.

**Authentication for end customers:** The tenant's own customer auth system via OAuth 2.0 PKCE. Settla acts as a resource server. Alternatively, Settla issues magic-link tokens (email-based, no password required) for tenants without their own auth infrastructure.

**Tenant isolation:** Every portal page is authenticated and tenant-scoped. The portal backend service only returns data for the authenticated customer within the authenticated tenant. No cross-tenant data is ever exposed.

#### Complexity assessment

**Medium.** The backend gRPC service is partially implemented. The complexity is in the frontend (Vue 3 component library with theme tokens for white-labelling), custom domain SSL management, and the end-customer authentication choices. A solid implementation in phases: Phase 1 — hosted on `{slug}.portal.settla.io` with fixed branding; Phase 2 — custom domains + full branding config.

---

### 7.2 Analytics & Reporting API

**Status:** Proposed

#### Product description

A rich query API giving tenants programmatic access to their own operational data — volumes, settlement times, provider performance, fee breakdowns, reconciliation summaries, and failed transfer analysis. Data exportable as JSON, CSV, or pushed to a data warehouse via webhook.

Endpoints:
- `GET /v1/analytics/transfers` — volume and value by corridor, time period, status
- `GET /v1/analytics/fees` — fee revenue breakdown by type and currency
- `GET /v1/analytics/providers` — provider success rate, average settlement time, cost per corridor
- `GET /v1/analytics/reconciliation` — reconciliation run results and discrepancy history
- `GET /v1/analytics/deposits` — crypto collection volumes, fee revenue, session conversion rates
- `POST /v1/analytics/export` — trigger async CSV export, delivered via webhook or download link

#### Architecture

The analytics SQLC queries already exist (`store/transferdb/analytics.sql.go`, `api/grpc/analytics.go`). The Transfer DB and Ledger DB Postgres read replicas absorb analytical queries without impacting the write path.

For heavier aggregations (e.g., monthly rollup across 50M rows), a materialised view layer is added: a background job runs aggregations nightly and writes to `analytics_daily_snapshots`. Point-in-time queries hit the snapshot; real-time queries (last 24h) hit the live tables directly.

```sql
CREATE TABLE analytics_daily_snapshots (
    tenant_id       UUID NOT NULL,
    date            DATE NOT NULL,
    metric_type     TEXT NOT NULL,   -- TRANSFER_VOLUME, FEE_REVENUE, PROVIDER_PERF, etc.
    dimensions      JSONB NOT NULL,  -- corridor, currency, provider, etc.
    values          JSONB NOT NULL,  -- count, amount_usd, avg_duration_s, etc.
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, date, metric_type, dimensions)
);
```

**Retention:** Raw transfer data retained per Postgres partition policy (existing monthly partitioning). Daily snapshots retained for 3 years. Export jobs available for full history.

#### Complexity assessment

**Low.** The queries are largely written. The main work is designing a clean, stable API surface, building the snapshot job, and the export pipeline. The risk is query performance at scale — parameterised queries with proper indexes are essential before exposing arbitrary date ranges to tenants.

---

## Implementation Priority

When sequencing implementation, the recommended order based on tenant demand, architectural fit, and risk:

**Tier 1 — High value, low risk (build next):**
1. Bulk Disbursements — immediate demand from payroll/gig economy tenants, clean architecture
2. Analytics & Reporting API — high retention value, most of the code already exists
3. Smart Routing API — one-day implementation, useful for tenant tooling
4. Payment Links — low code, removes a friction point for new tenants

**Tier 2 — High value, medium complexity (build after crypto gateway ships):**
5. Virtual IBANs / VANs — completes the collection story; banking partner procurement is the long lead item
6. Recurring Collections — high demand for subscription use cases
7. White-Label Portal — strong land-and-expand, backend largely exists
8. Real-Time Streaming API — DX improvement, clean implementation

**Tier 3 — High value, high complexity (requires dedicated planning):**
9. Multi-Party Escrow — solid demand but dispute resolution design is non-trivial
10. Compliance Rules Engine — requires legal review and sanctions data partnerships
11. FX Rate Locking — requires hedging infrastructure and treasury risk function
12. Yield on Idle Balances — requires regulatory analysis and liquidity management

---

## Open Questions

1. **Virtual IBANs banking partner selection:** Modulr (UK/EU), ClearBank (GBP), Stitch (ZA), or multiple? Partner selection determines corridor coverage and account lifecycle constraints.
2. **Compliance rules engine legal liability:** In which jurisdictions does Settla take on screening liability vs. pass it to the tenant? This determines whether the rules engine is a product feature or a mandatory system control.
3. **Yield regulatory classification:** Does offering yield on held balances require a financial services licence (e.g., e-money institution, investment firm) in Settla's operating jurisdictions? Legal review required before any yield feature ships.
4. **White-label portal customer auth:** Should Settla build its own customer identity system (adds scope significantly) or require tenants to provide OAuth 2.0? The answer determines Phase 1 scope.
5. **Analytics data residency:** Some tenants may have data residency requirements (GDPR, etc.) that affect where analytics snapshots can be stored and how long they are retained.
