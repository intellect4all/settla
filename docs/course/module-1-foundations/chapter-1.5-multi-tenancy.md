# Chapter 1.5: Multi-Tenancy -- Designing for Isolation

**Estimated reading time:** 25 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why multi-tenancy is a security requirement, not a feature, in financial infrastructure
2. Trace the authentication flow from API key to tenant_id resolution
3. Implement per-tenant fee schedules with basis-point math and min/max clamping
4. Describe the KYB lifecycle and dual-gate activation rule (Status AND KYBStatus)
5. Enforce tenant isolation at every layer: API, cache, database, rate limiting, idempotency

---

## Why Multi-Tenancy Is Non-Negotiable

In Settla, every fintech customer -- Lemfi, Fincra, Paystack -- is a **tenant**. Multi-tenancy is not a feature to be added later. It is a security requirement that must be present from the first line of code.

Consider what happens if tenant isolation fails:

```
    Scenario: A query missing WHERE tenant_id = ?

    SELECT * FROM transfers WHERE status = 'COMPLETED' LIMIT 100;

    Returns: transfers from ALL tenants mixed together.

    Consequences:
    - Lemfi sees Fincra's transfer data (financial data breach)
    - Compliance violation (each tenant's data is subject to different regulations)
    - Loss of customer trust (instant contract termination)
    - Potential regulatory fines
```

A single missing `tenant_id` filter in a single query is a data breach. At 50M transfers/day across multiple tenants, the blast radius of such a bug is enormous.

> **Key Insight:** Multi-tenancy cannot be "bolted on." Every database table, every cache key, every rate limiter, every idempotency check, every webhook delivery must be tenant-scoped from day one. Retrofitting tenant isolation into a single-tenant system requires touching every query and every cache operation -- it is effectively a rewrite.

---

## The Tenant Entity

```go
// From domain/tenant.go
type Tenant struct {
    ID               uuid.UUID
    Name             string             // "Lemfi"
    Slug             string             // "lemfi" (URL-safe, used in account codes)
    Status           TenantStatus       // ACTIVE, SUSPENDED, ONBOARDING
    FeeSchedule      FeeSchedule        // Per-tenant fee rates in basis points
    SettlementModel  SettlementModel    // PREFUNDED or NET_SETTLEMENT
    WebhookURL       string             // Outbound webhook endpoint
    WebhookSecret    string             // HMAC-SHA256 signing key
    DailyLimitUSD    decimal.Decimal    // Daily volume cap
    PerTransferLimit decimal.Decimal    // Single transfer maximum
    MaxPendingTransfers int            // 0 = unlimited, caps non-terminal transfers
    KYBStatus        KYBStatus          // PENDING, IN_REVIEW, VERIFIED, REJECTED
    KYBVerifiedAt    *time.Time         // nil until verification completes
    Metadata         map[string]string  // Flexible key-value data
    CreatedAt        time.Time
    UpdatedAt        time.Time
}
```

Every field on this struct affects how transfers are processed for this tenant. The `FeeSchedule` determines pricing. The `SettlementModel` determines funding flow. The `DailyLimitUSD` caps daily volume. The `WebhookURL` determines where completion notifications are delivered.

### Per-Tenant Pending Transfer Limits

The `MaxPendingTransfers` field is Critical Invariant #13. It caps the number of non-terminal transfers (transfers not in `COMPLETED`, `FAILED`, or `REFUNDED` state) that a tenant can have at any time:

```go
// From the Tenant struct
MaxPendingTransfers int  // 0 = unlimited
```

This prevents a malicious or misconfigured tenant from creating unlimited pending transfers that consume database resources, NATS queue depth, and worker capacity. The check is enforced atomically in `Engine.CreateTransfer`:

```
    Lemfi:    MaxPendingTransfers = 10,000  (high-volume, trusted)
    Fincra:   MaxPendingTransfers = 5,000   (medium volume)
    New tenant: MaxPendingTransfers = 100   (conservative during onboarding)

    When Lemfi has 10,000 non-terminal transfers in-flight:
    -> Next CreateTransfer returns ErrMaxPendingTransfersExceeded
    -> Lemfi must wait for existing transfers to complete
    -> Prevents resource exhaustion DoS
```

Without this limit, a single tenant could create millions of pending transfers that consume queue depth and worker capacity, degrading service for all tenants.

---

## The Dual-Gate Activation Rule

A tenant must pass two independent gates before processing any transactions:

```go
// From domain/tenant.go
func (t *Tenant) IsActive() bool {
    return t.Status == TenantStatusActive && t.KYBStatus == KYBStatusVerified
}
```

Both conditions must be true simultaneously:

```
    +----------------+----------------+------------------+
    | Status         | KYBStatus      | Can Process?     |
    +----------------+----------------+------------------+
    | ACTIVE         | VERIFIED       | YES              |
    | ACTIVE         | PENDING        | NO (KYB needed)  |
    | ACTIVE         | IN_REVIEW      | NO (KYB pending) |
    | ACTIVE         | REJECTED       | NO (KYB failed)  |
    | SUSPENDED      | VERIFIED       | NO (suspended)   |
    | SUSPENDED      | PENDING        | NO (both gates)  |
    | ONBOARDING     | VERIFIED       | NO (not active)  |
    | ONBOARDING     | PENDING        | NO (both gates)  |
    +----------------+----------------+------------------+
```

Only one combination allows transaction processing. This prevents the common mistake of checking only one condition.

### Tenant Status Lifecycle

```go
// From domain/tenant.go
type TenantStatus string

const (
    TenantStatusActive     TenantStatus = "ACTIVE"
    TenantStatusSuspended  TenantStatus = "SUSPENDED"
    TenantStatusOnboarding TenantStatus = "ONBOARDING"
)
```

```
    +-------------+                    +--------+
    | ONBOARDING  | -- setup complete -> | ACTIVE |
    +-------------+                    +----+---+
                                            |
                                 suspend    |    reactivate
                                    +-------v-------+
                                    |  SUSPENDED    |
                                    +---------------+
```

A tenant starts in `ONBOARDING` during initial setup, moves to `ACTIVE` when configuration is complete, and can be `SUSPENDED` by operations if there are compliance concerns or payment issues.

### KYB (Know Your Business) Lifecycle

```go
// From domain/tenant.go
type KYBStatus string

const (
    KYBStatusPending  KYBStatus = "PENDING"
    KYBStatusInReview KYBStatus = "IN_REVIEW"
    KYBStatusVerified KYBStatus = "VERIFIED"
    KYBStatusRejected KYBStatus = "REJECTED"
)
```

```
    +----------+   docs submitted   +------------+   approved   +----------+
    | PENDING  | -----------------> | IN_REVIEW  | -----------> | VERIFIED |
    +----------+                    +------+-----+              +----------+
                                          |
                                          | rejected
                                          v
                                    +----------+
                                    | REJECTED |
                                    +----------+
```

KYB verification is a regulatory requirement. Every fintech that uses Settla's settlement infrastructure must prove they are a legitimate business with proper licensing. Without `VERIFIED` KYB status, no transfers can be created.

---

## Settlement Models

Each tenant selects a settlement model that determines how funds flow:

```go
// From domain/tenant.go
type SettlementModel string

const (
    SettlementModelPrefunded     SettlementModel = "PREFUNDED"
    SettlementModelNetSettlement SettlementModel = "NET_SETTLEMENT"
)
```

### PREFUNDED

The tenant wires funds to Settla in advance. Each transfer atomically reserves from the pre-funded position:

```
    Lemfi (PREFUNDED):
    +---------------------------------------------------+
    | GBP Position:  Available: 450,000  Locked: 50,000 |
    | USD Position:  Available: 200,000  Locked: 10,000 |
    | EUR Position:  Available: 100,000  Locked:  5,000 |
    +---------------------------------------------------+
    Each transfer: Reserve() decrements Available, increments Locked
    On completion: Release() decrements Locked (funds consumed)
    On failure:    Release() decrements Locked, increments Available
```

### NET_SETTLEMENT

Transfers execute without pre-funding. At 00:30 UTC daily, net positions are calculated:

```
    Fincra (NET_SETTLEMENT):
    +---------------------------------------------------+
    | Day's activity:                                     |
    |   GBP -> NGN: 500 transfers, total GBP 2,000,000  |
    |   NGN -> GBP: 200 transfers, total GBP   800,000  |
    |   Net position: GBP 1,200,000 owed by Fincra      |
    +---------------------------------------------------+
    Settlement at 00:30 UTC: Fincra pays GBP 1,200,000
```

---

## Per-Tenant Fee Schedules

Fees are the core revenue mechanism. Each tenant negotiates their own schedule:

```go
// From domain/tenant.go
type FeeSchedule struct {
    OnRampBPS  int             `json:"onramp_bps"`
    OffRampBPS int             `json:"offramp_bps"`
    MinFeeUSD  decimal.Decimal `json:"min_fee_usd"`
    MaxFeeUSD  decimal.Decimal `json:"max_fee_usd"`
}
```

### CalculateFee Walkthrough

The fee calculation logic with full min/max clamping:

```go
// From domain/tenant.go
var bpsDivisor = decimal.NewFromInt(10000)

func (f FeeSchedule) CalculateFee(amount decimal.Decimal, feeType string) (decimal.Decimal, error) {
    var bps int
    var maxFee decimal.Decimal
    switch feeType {
    case "onramp":
        bps = f.OnRampBPS
        maxFee = f.MaxFeeUSD
    case "offramp":
        bps = f.OffRampBPS
        maxFee = f.MaxFeeUSD
    case "crypto_collection":
        bps = f.CryptoCollectionBPS
        maxFee = f.CryptoCollectionMaxFeeUSD
    case "bank_collection":
        bps = f.BankCollectionBPS
        maxFee = f.BankCollectionMaxFeeUSD
    default:
        return decimal.Zero, fmt.Errorf("settla-domain: unknown fee type %q", feeType)
    }

    // Core calculation: fee = amount * bps / 10000
    fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)

    // Floor: ensure minimum fee
    minFee := f.MinFeeUSD
    if feeType == "bank_collection" && !f.BankCollectionMinFeeUSD.IsZero() {
        minFee = f.BankCollectionMinFeeUSD
    }
    if !minFee.IsZero() && fee.LessThan(minFee) {
        fee = minFee
    }

    // Cap: ensure maximum fee
    if !maxFee.IsZero() && fee.GreaterThan(maxFee) {
        fee = maxFee
    }

    return fee, nil
}
```

### Worked Examples

**Lemfi schedule: 40/35 bps, min $1.00, max $50.00**

```
    Amount      Type      Raw Fee           Clamped Fee    Effective Rate
    ------      ----      -------           -----------    --------------
    $50         onramp    50 x 40/10000     = $0.20        -> $1.00 (min)     2.00%
    $250        onramp    250 x 40/10000    = $1.00        $1.00              0.40%
    $1,000      onramp    1000 x 40/10000   = $4.00        $4.00              0.40%
    $5,000      offramp   5000 x 35/10000   = $1.75        $1.75              0.035%
    $10,000     onramp    10000 x 40/10000  = $40.00       $40.00             0.40%
    $100,000    onramp    100000 x 40/10000 = $400.00      -> $50.00 (max)    0.05%
```

Notice the effective rate curve: it is high on small transfers (min fee dominates), constant in the middle range (percentage applies), and low on large transfers (max fee caps it). This creates a natural incentive for tenants to send mid-range transfers where the fee is proportional.

**Fincra schedule: 25/20 bps, min $0.50, max $100.00**

```
    Amount      Type      Raw Fee           Clamped Fee    Effective Rate
    ------      ----      -------           -----------    --------------
    $50         onramp    50 x 25/10000     = $0.125       -> $0.50 (min)     1.00%
    $200        onramp    200 x 25/10000    = $0.50        $0.50              0.25%
    $10,000     onramp    10000 x 25/10000  = $25.00       $25.00             0.25%
    $50,000     offramp   50000 x 20/10000  = $100.00      $100.00            0.20%
    $500,000    offramp   500000 x 20/10000 = $1000.00     -> $100.00 (max)   0.02%
```

> **Key Insight:** The fee schedule is a business negotiation tool. High-volume tenants get lower basis-point rates (Fincra at 25 bps vs Lemfi at 40 bps) because their volume compensates for the lower per-transaction revenue. The min/max clamping ensures Settla never loses money on tiny transfers and remains competitive on large ones.

---

## Authentication and Tenant Resolution

The authentication flow ensures `tenant_id` always comes from the API key, never from the request body:

```
    Client Request:
    POST /v1/transfers
    Authorization: Bearer sk_live_abc123defgh456...

    Gateway Processing:
    +------------------------------------------------------------+
    |  1. Extract key from Authorization header                   |
    |     key = "sk_live_abc123defgh456..."                       |
    |                                                             |
    |  2. HMAC-SHA256 hash the key (with server-side secret)       |
    |     hash = HMAC(secret, key) = "a1b2c3d4..."                |
    |                                                             |
    |  3. Three-level cache lookup:                                |
    |     L1: Local in-process LRU (30s TTL, ~107ns)              |
    |         -> HIT: return cached tenant                        |
    |         -> MISS: check L2                                   |
    |     L2: Redis (5min TTL, ~0.5ms)                            |
    |         -> HIT: populate L1, return tenant                  |
    |         -> MISS: check L3                                   |
    |     L3: Postgres (source of truth, ~3ms)                    |
    |         -> HIT: populate L1+L2, return tenant               |
    |         -> MISS: return 401 Unauthorized                    |
    |                                                             |
    |  4. Verify tenant.IsActive()                                |
    |     -> If false: return 403 Forbidden                       |
    |                                                             |
    |  5. Attach tenant_id to request context                     |
    |     ctx = context.WithValue(ctx, "tenant_id", tenant.ID)    |
    |                                                             |
    |  6. Forward to gRPC backend with tenant_id from context     |
    +------------------------------------------------------------+
```

### API Key Design

```go
// From domain/tenant.go
type APIKey struct {
    ID          uuid.UUID
    TenantID    uuid.UUID
    KeyHash     string     // HMAC-SHA256 hash of the raw key -- raw key NEVER stored
    KeyPrefix   string     // First 8 chars for identification (e.g., "sk_live_")
    Environment string     // "live" or "test"
    Name        string     // Human-readable label ("Production Key #1")
    IsActive    bool       // Can be deactivated without deletion
    ExpiresAt   *time.Time // Optional expiration
    CreatedAt   time.Time
}
```

Key security properties:

1. **Raw keys are never stored.** Only the HMAC-SHA256 hash (keyed with `SETTLA_API_KEY_HMAC_SECRET`) is persisted. This is a keyed hash, not a plain hash -- without the server-side HMAC secret, an attacker who compromises the database cannot verify candidate keys offline. The hash enables constant-time lookup: `SELECT * FROM api_keys WHERE key_hash = $1 AND is_active = true`.
2. **Key prefix is stored separately.** The `sk_live_` prefix allows identifying the key environment in logs and dashboards without exposing the key itself.
3. **Keys can be deactivated.** `IsActive = false` instantly revokes access without deleting the key record (which preserves audit trail).
4. **Optional expiration.** `ExpiresAt` enables time-limited keys for testing or third-party integrations.

---

## Tenant Isolation at Every Layer

### Database Layer

Every table that holds tenant data includes a `tenant_id` column:

```sql
-- Transfers table
CREATE TABLE transfers (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL REFERENCES tenants(id),
    status      TEXT NOT NULL,
    -- ...
);

-- Every query includes tenant_id
SELECT * FROM transfers WHERE tenant_id = $1 AND id = $2;
UPDATE transfers SET status = $2 WHERE tenant_id = $1 AND id = $3;
```

SQLC generates typed Go methods that require `tenant_id` as a parameter:

```go
// Generated by SQLC -- tenant_id is a required parameter
func (q *Queries) GetTransfer(ctx context.Context, arg GetTransferParams) (Transfer, error)

type GetTransferParams struct {
    TenantID uuid.UUID  // Cannot be omitted
    ID       uuid.UUID
}
```

### Idempotency Key Scoping

Idempotency keys are scoped per-tenant using a composite unique constraint:

```sql
UNIQUE(tenant_id, idempotency_key)
```

This means:
- Lemfi can use idempotency key `"order-12345"` for their transfer
- Fincra can also use `"order-12345"` for a completely different transfer
- No conflict, because the composite key includes `tenant_id`

Without this scoping, one tenant's idempotency key could collide with another's, causing a transfer to be silently deduplicated when it should have been created as a new transfer.

### Cache Layer

Cache keys include tenant_id to prevent cross-tenant contamination:

```
    Auth cache key:     auth:{key_hash}  -> tenant object
    Rate limit key:     ratelimit:{tenant_id}:{window}
    Quote cache key:    quote:{tenant_id}:{corridor}:{amount}
    Idempotency key:    idemp:{tenant_id}:{key}
```

If cache keys did not include `tenant_id`, a cache hit from Lemfi's request could serve data to Fincra's request.

### Rate Limiting

Rate limits are per-tenant:

```
    Lemfi:   1,000 requests/minute
    Fincra:  2,000 requests/minute  (higher tier)
    Paystack:  500 requests/minute  (standard tier)

    Rate limit counters:
    ratelimit:lemfi-uuid:2026-03-15T10:05   = 847
    ratelimit:fincra-uuid:2026-03-15T10:05  = 1,203
    ratelimit:paystack-uuid:2026-03-15T10:05 = 312
```

Local counters are kept in-process and synced to Redis every 5 seconds. This avoids a Redis round-trip on every request while maintaining approximate accuracy across gateway replicas.

### Webhook Delivery

Each tenant has a unique webhook URL and HMAC signing secret:

```go
// From the Tenant struct
WebhookURL    string  // "https://api.lemfi.com/webhooks/settla"
WebhookSecret string  // Used for HMAC-SHA256 signature
```

The webhook worker signs each delivery with the tenant's secret, so the tenant can verify the webhook came from Settla:

```
    POST https://api.lemfi.com/webhooks/settla
    X-Settla-Signature: sha256=a1b2c3d4e5f6...
    Content-Type: application/json

    {
      "event": "transfer.completed",
      "transfer_id": "...",
      "tenant_id": "...",
      ...
    }
```

### Ledger Account Codes

Ledger accounts embed tenant identity in their account code:

```
    Tenant-scoped accounts (include tenant slug):
      tenant:lemfi:assets:bank:gbp:clearing
      tenant:lemfi:liabilities:customer:pending
      tenant:fincra:revenue:fees:settlement
      tenant:paystack:assets:bank:ngn:clearing

    System accounts (no tenant prefix):
      assets:crypto:usdt:tron
      expenses:provider:onramp
      liabilities:settlement:pending
```

This naming convention makes it immediately visible in logs, dashboards, and audit trails which tenant owns which account. It also makes accidental cross-tenant ledger posting obvious -- posting to `tenant:lemfi:...` from a Fincra transfer would be caught by the account code mismatch.

---

## Daily and Per-Transfer Limits

Each tenant has configurable volume limits:

```go
// From the Tenant struct
DailyLimitUSD    decimal.Decimal  // Maximum total volume per day
PerTransferLimit decimal.Decimal  // Maximum single transfer amount
```

These limits are checked during transfer creation:

```
    Lemfi limits:
      DailyLimitUSD:    $5,000,000
      PerTransferLimit: $100,000

    Transfer request: $150,000
    -> Rejected: ErrAmountTooHigh (exceeds per-transfer limit)

    Transfer request: $50,000 (daily total already at $4,980,000)
    -> Rejected: ErrDailyLimitExceeded (would exceed daily cap)
```

The domain errors for these cases carry the tenant context:

```go
// From domain/errors.go
func ErrDailyLimitExceeded(tenantID string) *DomainError {
    return &DomainError{
        code:    CodeDailyLimitExceeded,
        message: fmt.Sprintf("settla-domain: daily limit exceeded for tenant %s", tenantID),
    }
}
```

---

## Tenant Self-Service Portal

The tenant portal (`portal/`) is a Vue 3 + Nuxt application that provides tenants with self-service access to their Settla resources. Every action in the portal is authenticated and tenant-scoped:

```
    Portal Capabilities:
    =========================================
    Auth:             Registration, login, email verification, token refresh
    Onboarding/KYB:   Document submission, verification status tracking
    Deposits:         Create and monitor crypto deposit sessions
    Bank Deposits:    Create and monitor bank deposit sessions (virtual accounts)
    Payment Links:    Generate, manage, disable, and track redemptions
    Analytics:        Volume metrics, fee breakdowns, corridor performance
    Crypto Balances:  View stablecoin positions across chains
    Treasury:         Request top-ups and withdrawals, view position history
```

The portal communicates with the gRPC backend through the same gateway as the API, using the same authentication and tenant isolation mechanisms. Portal auth uses a separate email/password flow with JWT tokens, but all backend calls include `tenant_id` extracted from the authenticated session -- never from user input.

> **Key Insight:** The portal does not introduce new data access patterns. Every portal query hits the same SQLC-generated methods with the same `tenant_id` filters. The portal is a UI layer over the existing tenant-scoped API, not a parallel data access path that might bypass isolation.

---

## Common Mistakes

### Mistake 1: Forgetting tenant_id in a Query

```sql
-- WRONG: returns data from ALL tenants
SELECT * FROM transfers WHERE status = 'FAILED' ORDER BY created_at DESC LIMIT 100;

-- RIGHT: scoped to the requesting tenant
SELECT * FROM transfers WHERE tenant_id = $1 AND status = 'FAILED' ORDER BY created_at DESC LIMIT 100;
```

SQLC helps prevent this by generating typed query methods where `tenant_id` is a required parameter. But ad-hoc queries (admin dashboards, debugging scripts) are the common source of this mistake.

### Mistake 2: Accepting tenant_id from Request Body

```go
// WRONG: tenant_id from user input
tenantID := req.Body.TenantID  // Attacker controls this value

// RIGHT: tenant_id from authenticated context
tenantID := auth.TenantIDFromContext(ctx)  // Set during API key validation
```

The gateway must never trust the client to identify themselves. The API key in the `Authorization` header is the only source of tenant identity.

### Mistake 3: Global Idempotency Keys

```sql
-- WRONG: global uniqueness
UNIQUE(idempotency_key)
-- Tenant A's key "order-123" collides with Tenant B's "order-123"
-- Tenant B's transfer is silently deduplicated -- they never know

-- RIGHT: per-tenant uniqueness
UNIQUE(tenant_id, idempotency_key)
-- Each tenant has their own namespace for idempotency keys
```

### Mistake 4: Hardcoding Fee Rates

```go
// WRONG: hardcoded fee
fee := amount.Mul(decimal.NewFromFloat(0.004))  // 40 bps for everyone

// RIGHT: per-tenant fee schedule
fee, err := tenant.FeeSchedule.CalculateFee(amount, "onramp")
```

Hardcoded rates cannot accommodate enterprise negotiations. When a new tenant signs on with a custom fee schedule, the system must support it without code changes.

### Mistake 5: Checking Only One Gate

```go
// WRONG: only checking status
if tenant.Status == TenantStatusActive {
    // Allows processing even if KYB is PENDING
}

// RIGHT: checking both gates
if tenant.IsActive() {
    // Requires Status == ACTIVE AND KYBStatus == VERIFIED
}
```

---

## Exercises

### Exercise 1: Fee Schedule Comparison

Given these fee schedules:

```
    Tenant A: 40/35 bps, min $1.00, max $50.00
    Tenant B: 25/20 bps, min $0.50, max $100.00
```

For each transfer amount ($50, $500, $5,000, $50,000, $500,000):
1. Calculate the total fee (on-ramp + off-ramp) for each tenant
2. Calculate the effective rate (total fee / amount) for each tenant
3. At what transfer amount does Tenant A become more expensive than Tenant B?
4. At what transfer amount does the max fee cap matter for each tenant?

### Exercise 2: Tenant Isolation Audit

Imagine you are reviewing a pull request that adds a new "search transfers" endpoint. The query is:

```sql
SELECT * FROM transfers
WHERE source_currency = $1 AND created_at > $2
ORDER BY created_at DESC LIMIT 50;
```

1. What is wrong with this query?
2. Write the corrected query
3. What SQLC convention would prevent this mistake?
4. What test would catch this in CI?

### Exercise 3: Design Tenant Onboarding Flow

Design the complete tenant onboarding flow:
1. What information is collected during `ONBOARDING` status?
2. What documents are required for KYB verification?
3. What automated checks should run during `IN_REVIEW`?
4. What is the process for moving from `IN_REVIEW` to `VERIFIED`?
5. How does a rejected tenant appeal?
6. What happens to in-flight transfers if a tenant is suspended?

### Exercise 4: Multi-Tenant Cache Key Design

Design cache key schemas for the following scenarios. Each key must include tenant isolation and prevent cross-tenant data leakage:
1. Caching a tenant's current daily volume total
2. Caching a quote for a specific corridor and amount
3. Caching the result of a rate limit check
4. Caching a tenant's active webhooks configuration

---

## What's Next

Every piece of data is tenant-scoped. Every fee calculation uses the tenant's schedule. Every authentication resolves tenant identity from the API key, never from user input. In Chapter 1.6, we will study the modular monolith pattern -- how Settla organizes multiple bounded contexts (core, ledger, treasury, rail) into a single binary with strict interface boundaries, and how any module can be extracted to a separate service without changing the core engine.

---
