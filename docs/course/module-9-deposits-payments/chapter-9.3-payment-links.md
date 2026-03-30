# Chapter 9.3: Payment Links -- Merchant Collection URLs

**Reading time: 20 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why payment links exist and how they simplify merchant collections
2. Trace the full lifecycle of a payment link from creation through redemption
3. Describe how the Service delegates heavy lifting to the deposit engine
4. Distinguish public endpoints from authenticated endpoints and explain why the split matters
5. Evaluate the short code design tradeoffs (NanoID vs UUID, alphabet selection, collision handling)

---

## The Payment Link Use Case

Consider a merchant on a fintech platform powered by Settla. They want to collect a crypto payment from a customer. Without payment links, the integration flow looks like this:

```
Merchant Backend                  Settla API                    Customer
     |                                |                            |
     |-- POST /v1/deposits ---------> |                            |
     |<-- { session_id, address } --- |                            |
     |                                |                            |
     |-- render checkout page ------> |                            |
     |                                |                            |
     |                                | <-- customer sends crypto  |
     |                                |                            |
     |<-- webhook: deposit confirmed  |                            |
```

This requires the merchant to build a backend integration, handle session creation, render a checkout page, and process webhooks. For a small merchant selling goods on social media, this is prohibitive.

Payment links collapse this to a single step:

```
Merchant                          Settla                         Customer
     |                                |                            |
     |-- Create link (dashboard) ---> |                            |
     |<-- https://pay.settla.io/p/Abc123xyz456                    |
     |                                |                            |
     |-- share URL (WhatsApp, email, QR code) ------------------> |
     |                                |                            |
     |                                | <-- GET /resolve/Abc123... |
     |                                | --> { amount, chain, ... } |
     |                                |                            |
     |                                | <-- POST /redeem/Abc123... |
     |                                | --> { deposit_address }    |
     |                                |                            |
     |                                | <-- customer sends crypto  |
     |                                |                            |
     |<-- webhook: deposit confirmed  |                            |
```

The merchant creates a link once (via the portal dashboard or API) and shares the URL. The customer opens the link, sees the payment details, and clicks "Pay." Settla creates a deposit session behind the scenes, assigns a blockchain address, and monitors for the incoming payment. The merchant's treasury position is credited when the payment confirms.

No backend integration. No checkout page. Just a URL.

---

## The Domain Model

**File:** `domain/payment_link.go`

The payment link is a simple CRUD entity with a session template attached:

```go
// PaymentLinkStatus represents the lifecycle state of a payment link.
type PaymentLinkStatus string

const (
    PaymentLinkStatusActive   PaymentLinkStatus = "ACTIVE"
    PaymentLinkStatusExpired  PaymentLinkStatus = "EXPIRED"
    PaymentLinkStatusDisabled PaymentLinkStatus = "DISABLED"
)
```

Three terminal-ish states, but note: there is no `REDEEMED` status. A payment link can be redeemed multiple times (up to its use limit). The link stays `ACTIVE` until it expires, is disabled, or exhausts its use limit.

```go
// PaymentLink is a shareable URL template that creates deposit sessions on redemption.
// It is a simple CRUD entity -- no state machine, no outbox. The heavy lifting
// (address dispensing, chain monitoring, crediting, settlement) is delegated
// entirely to the existing deposit engine.
type PaymentLink struct {
    ID          uuid.UUID
    TenantID    uuid.UUID
    ShortCode   string
    Description string
    RedirectURL string
    Status      PaymentLinkStatus

    SessionConfig PaymentLinkSessionConfig

    UseLimit *int
    UseCount int

    ExpiresAt *time.Time
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

The `SessionConfig` is the template that gets stamped into a deposit session every time someone redeems the link:

```go
// PaymentLinkSessionConfig holds the template configuration for deposit sessions
// created when a payment link is redeemed. Stored as JSONB in the database.
type PaymentLinkSessionConfig struct {
    Amount         decimal.Decimal      `json:"amount"`
    Currency       Currency             `json:"currency"`
    Chain          CryptoChain          `json:"chain"`
    Token          string               `json:"token"`
    SettlementPref SettlementPreference `json:"settlement_pref,omitempty"`
    TTLSeconds     int32                `json:"ttl_seconds,omitempty"`
}
```

This is a key design decision: the payment link does not process payments itself. It is a factory that produces deposit sessions. All the complex machinery -- address pool management, chain monitoring, confirmation tracking, auto-convert vs hold strategies -- lives in the deposit engine (Chapter 9.1). The payment link simply stamps out sessions from a template.

> **Key Insight:** Payment links follow the "thin wrapper, deep engine" pattern. The PaymentLink entity has no state machine and no outbox entries. It is pure CRUD. The moment a customer redeems a link, the system delegates to the deposit engine, which has all the state machine complexity, outbox integration, and chain monitoring infrastructure. This avoids duplicating deposit logic in a second code path.

---

## Payment Link Lifecycle

The lifecycle is simpler than most Settla entities because there is no state machine:

```
                  +------------------+
                  |     ACTIVE       |
                  +------------------+
                   /       |        \
                  /        |         \
    [use_limit   /  [merchant  \  [expiry time
     reached]   /   disables]   \   passes]
               /       |         \
              v        v          v
    +----------+  +-----------+  +---------+
    | EXHAUSTED|  | DISABLED  |  | EXPIRED |
    | (still   |  +-----------+  +---------+
    |  ACTIVE) |
    +----------+

    Note: "EXHAUSTED" is not a separate status.
    The link stays ACTIVE but CanRedeem() returns
    ErrPaymentLinkExhausted when use_count >= use_limit.
```

The `CanRedeem()` method enforces all three exit conditions:

```go
// CanRedeem returns true if the payment link can be used to create a new deposit session.
func (pl *PaymentLink) CanRedeem() error {
    if pl.Status == PaymentLinkStatusDisabled {
        return ErrPaymentLinkDisabled(pl.ID.String())
    }
    if pl.Status == PaymentLinkStatusExpired {
        return ErrPaymentLinkExpired(pl.ID.String())
    }
    if pl.ExpiresAt != nil && time.Now().UTC().After(*pl.ExpiresAt) {
        return ErrPaymentLinkExpired(pl.ID.String())
    }
    if pl.UseLimit != nil && pl.UseCount >= *pl.UseLimit {
        return ErrPaymentLinkExhausted(pl.ID.String())
    }
    return nil
}
```

Notice the double expiry check: first against the stored `Status` field (set by a background job or migration), then against the actual clock. This is a defensive pattern -- even if a background job has not yet flipped the status to `EXPIRED`, the real-time clock check catches it. The system never serves a link past its expiry time.

---

## The Payment Link Service

**File:** `core/paymentlink/service.go`

The service is the orchestration layer. It coordinates validation, short code generation, persistence, and deposit session creation.

### Dependencies

```go
type Service struct {
    store         PaymentLinkStore
    depositEngine *depositcore.Engine
    tenantStore   TenantStore
    logger        *slog.Logger
    baseURL       string // e.g. "https://pay.settla.io/p"
}
```

Three dependencies, each with a clear role:

| Dependency | Purpose |
|------------|---------|
| `PaymentLinkStore` | CRUD persistence for payment links |
| `depositcore.Engine` | Creates deposit sessions when links are redeemed |
| `TenantStore` | Validates tenant is active and crypto-enabled |

The `baseURL` is a configuration value that determines the public URL prefix. In production this might be `https://pay.settla.io/p`; in development it could be `http://localhost:3000/v1/payment-links/resolve`.

### Create: Validation, Short Code, Persist

The `Create` method follows a three-step pattern:

```go
func (s *Service) Create(ctx context.Context, tenantID uuid.UUID, req CreateRequest) (*CreateResult, error) {
    // 1. Validate tenant is active and crypto-enabled
    tenant, err := s.tenantStore.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-paymentlink: create: loading tenant %s: %w", tenantID, err)
    }
    if !tenant.IsActive() {
        return nil, domain.ErrTenantSuspended(tenantID.String())
    }
    if !tenant.CryptoConfig.CryptoEnabled {
        return nil, domain.ErrCryptoDisabled(tenantID.String())
    }

    // 2. Validate amount
    if !req.Amount.IsPositive() {
        return nil, domain.ErrAmountTooLow(req.Amount.String(), "0")
    }

    // 3. Generate short code with collision retry
    var link *domain.PaymentLink
    var lastErr error

    for attempt := range maxShortCodeAttempts {
        shortCode, err := generateShortCode()
        if err != nil {
            return nil, fmt.Errorf("settla-paymentlink: create: generating short code: %w", err)
        }

        link = &domain.PaymentLink{
            TenantID:    tenantID,
            ShortCode:   shortCode,
            Description: req.Description,
            RedirectURL: req.RedirectURL,
            Status:      domain.PaymentLinkStatusActive,
            UseLimit:    req.UseLimit,
            SessionConfig: domain.PaymentLinkSessionConfig{
                Amount:         req.Amount,
                Currency:       req.Currency,
                Chain:          req.Chain,
                Token:          req.Token,
                SettlementPref: req.SettlementPref,
                TTLSeconds:     req.TTLSeconds,
            },
        }

        if req.ExpiresAt != nil {
            t := time.Unix(*req.ExpiresAt, 0).UTC()
            link.ExpiresAt = &t
        }

        lastErr = s.store.Create(ctx, link)
        if lastErr == nil {
            break
        }

        if !domain.IsShortCodeCollision(lastErr) {
            return nil, fmt.Errorf("settla-paymentlink: create: persisting: %w", lastErr)
        }

        s.logger.Warn("settla-paymentlink: short code collision, retrying",
            "attempt", attempt+1,
            "tenant_id", tenantID,
        )
    }

    if lastErr != nil {
        return nil, fmt.Errorf(
            "settla-paymentlink: create: failed to generate unique short code after %d attempts",
            maxShortCodeAttempts,
        )
    }

    url := fmt.Sprintf("%s/%s", s.baseURL, link.ShortCode)
    return &CreateResult{Link: link, URL: url}, nil
}
```

The collision retry loop is the most interesting part. The database has a `UNIQUE` constraint on `short_code`. If the randomly generated code collides with an existing one, the store returns `ErrShortCodeCollision` (detected by checking for Postgres error code `23505` on the `payment_links_short_code_key` constraint). The service retries up to 3 times with a fresh random code. At the alphabet and length we use (56 characters, length 12), collisions are astronomically rare -- but the retry loop costs nothing and makes the system correct even at scale.

### Resolve: Public Lookup

```go
func (s *Service) Resolve(ctx context.Context, shortCode string) (*domain.PaymentLink, error) {
    link, err := s.store.GetByShortCode(ctx, shortCode)
    if err != nil {
        return nil, fmt.Errorf("settla-paymentlink: resolve: %w", err)
    }
    if link == nil {
        return nil, domain.ErrPaymentLinkNotFound(shortCode)
    }

    if err := link.CanRedeem(); err != nil {
        return nil, err
    }

    return link, nil
}
```

Resolve is a read-only operation. It loads the link by short code (no tenant ID needed -- the short code is globally unique) and checks that it can be redeemed. This powers the customer-facing page that shows "You are about to pay 100 USDT on Tron to Acme Corp."

Note that `GetByShortCode` in the store adapter deliberately bypasses RLS (Row-Level Security). This is correct: the customer does not have a tenant context. The short code itself is the authorization token.

### Redeem: Create a Deposit Session

```go
func (s *Service) Redeem(ctx context.Context, shortCode string) (*RedeemResult, error) {
    link, err := s.Resolve(ctx, shortCode)
    if err != nil {
        return nil, err
    }

    // Create deposit session from link template
    idempotencyKey, err := domain.NewIdempotencyKey(
        fmt.Sprintf("plink:%s:%d", link.ID, link.UseCount+1),
    )
    if err != nil {
        return nil, fmt.Errorf("settla-paymentlink: redeem: generating idempotency key: %w", err)
    }

    session, err := s.depositEngine.CreateSession(ctx, link.TenantID, depositcore.CreateSessionRequest{
        IdempotencyKey: idempotencyKey,
        Chain:          link.SessionConfig.Chain,
        Token:          link.SessionConfig.Token,
        ExpectedAmount: link.SessionConfig.Amount,
        SettlementPref: link.SessionConfig.SettlementPref,
        TTLSeconds:     link.SessionConfig.TTLSeconds,
    })
    if err != nil {
        return nil, fmt.Errorf("settla-paymentlink: redeem: creating deposit session: %w", err)
    }

    // Increment use count
    if err := s.store.IncrementUseCount(ctx, link.ID); err != nil {
        s.logger.Warn("settla-paymentlink: redeem: failed to increment use count",
            "link_id", link.ID,
            "error", err,
        )
    }

    return &RedeemResult{Session: session, Link: link}, nil
}
```

Three things to notice:

**1. Idempotency key construction.** The key is `plink:{linkID}:{useCount+1}`. This means if the same customer hits "Pay" twice in quick succession before the use count increments, the second call gets the same deposit session back (idempotency deduplication in the deposit engine). But the next legitimate redemption (use count has incremented) gets a fresh session.

**2. Deposit engine delegation.** The `CreateSession` call does everything: picks an address from the pool, starts chain monitoring, sets up the TTL timer. The payment link service does not need to know any of these details.

**3. Graceful use count increment.** If `IncrementUseCount` fails, the service logs a warning but does not fail the redemption. The customer already has their deposit session. The worst case is that one extra redemption might slip through before the count catches up. This is a deliberate availability-over-consistency tradeoff -- it is better to accept one extra payment than to reject a legitimate customer because of a transient DB error.

### Disable: Merchant Cancellation

```go
func (s *Service) Disable(ctx context.Context, tenantID, linkID uuid.UUID) error {
    link, err := s.store.GetByID(ctx, tenantID, linkID)
    if err != nil {
        return fmt.Errorf("settla-paymentlink: disable %s: %w", linkID, err)
    }
    if link == nil {
        return domain.ErrPaymentLinkNotFound(linkID.String())
    }

    if err := s.store.UpdateStatus(ctx, tenantID, linkID, domain.PaymentLinkStatusDisabled); err != nil {
        return fmt.Errorf("settla-paymentlink: disable %s: %w", linkID, err)
    }

    return nil
}
```

Disable is straightforward -- it flips the status to `DISABLED`. Any subsequent `Resolve` or `Redeem` call will fail with `ErrPaymentLinkDisabled`. Note that this does not cancel any in-flight deposit sessions that were already created from this link. Those sessions have their own lifecycle and will complete or expire independently.

---

## Public vs Authenticated Endpoints

The gateway splits payment link routes into two groups:

```
AUTHENTICATED (require API key)          PUBLIC (no auth, short code IS the auth)
-----------------------------------------+---------------------------------------
POST   /v1/payment-links                 | GET  /v1/payment-links/resolve/:code
GET    /v1/payment-links                 | POST /v1/payment-links/redeem/:code
GET    /v1/payment-links/:id             |
DELETE /v1/payment-links/:id             |
```

This split exists in the gateway's auth plugin as an explicit bypass list:

```typescript
// From api/gateway/src/auth/plugin.ts
if (
    request.url.startsWith("/v1/payment-links/resolve/") ||
    request.url.startsWith("/v1/payment-links/redeem/") ||
    // ...
) {
    // Skip auth -- these are public endpoints
}
```

Why this split matters:

**Authenticated endpoints** use the tenant's API key (`Authorization: Bearer sk_live_xxx`). The gateway resolves the key to a tenant ID and passes it to the gRPC server. Every query is scoped to that tenant. These are called by the merchant's backend or dashboard.

**Public endpoints** have no API key. The customer clicking a payment link does not have (and should not have) the merchant's API key. The short code itself serves as a capability token -- knowing the code grants permission to view and redeem the link, but nothing else.

This creates a security boundary:

```
                 +---------------------------+
                 |     Gateway Auth Plugin    |
                 +---------------------------+
                        |            |
            +-----------+            +-----------+
            |                                    |
    Authenticated Path                   Public Path
    (API key required)               (short code = auth)
            |                                    |
    tenant_id from key               no tenant_id
    full CRUD access                 resolve + redeem only
    RLS-enforced queries             non-RLS short code lookup
    standard rate limit              strict per-IP rate limit
            |                                    |
    +-------+-------+               +-----------+-----------+
    | Create | List  |               | Resolve   | Redeem    |
    | Get    | Delete|               +-----------+-----------+
    +--------+-------+
```

Public endpoints get stricter rate limiting (20 requests/second per IP) to prevent enumeration attacks. An attacker trying to brute-force short codes would need to guess from a space of 56^12 possibilities (~1.5 x 10^20), but the rate limit adds defense in depth.

> **Key Insight:** The public/authenticated split is a common pattern in payment systems. Stripe's payment links work the same way: creating a link requires an API key, but the customer-facing URL is public. The short code acts as a bearer token with minimal capabilities -- it can view payment details and initiate a payment, but it cannot list other links, access tenant data, or modify anything.

---

## Short Code Design

**File:** `core/paymentlink/service.go`

### Why Not UUIDs?

A payment link URL must be shared by humans -- pasted into WhatsApp, printed on invoices, encoded in QR codes. Compare:

```
UUID:       https://pay.settla.io/p/a1b2c3d4-e5f6-7890-abcd-ef1234567890
Short code: https://pay.settla.io/p/Abc123xyz456
```

The UUID is 36 characters (with hyphens), the short code is 12. For QR codes, shorter URLs produce smaller, more scannable codes. For manual entry (rare but possible), 12 characters is manageable; 36 is not.

### Alphabet Selection

```go
// NanoID alphabet: URL-safe, unambiguous characters (no lookalikes like 0/O, l/1).
const shortCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
const shortCodeLength = 12
```

The alphabet has 56 characters. Notable exclusions:

| Excluded | Reason |
|----------|--------|
| `0` (zero) | Looks like `O` (capital O) |
| `O` (capital O) | Looks like `0` (zero) |
| `1` (one) | Looks like `l` (lowercase L) or `I` (capital I) |
| `l` (lowercase L) | Looks like `1` (one) or `I` (capital I) |
| `I` (capital I) | Looks like `1` (one) or `l` (lowercase L) |

This is the same approach used by NanoID's URL-safe alphabet and by systems like Base58 (used in Bitcoin addresses). When a customer reads a code from a physical printout or a low-resolution screenshot, they should never be confused about whether a character is a zero or an O.

### Collision Resistance

With 56 characters and length 12, the keyspace is:

```
56^12 = 1.506 x 10^20  (about 150 quintillion)
```

For comparison, at 1 million payment links per day (far beyond what even a large deployment would produce), the probability of a collision after 10 years of operation is approximately:

```
n = 1,000,000 * 365 * 10 = 3.65 billion links
p(collision) ~ n^2 / (2 * keyspace)
             ~ (3.65 x 10^9)^2 / (2 * 1.506 x 10^20)
             ~ 4.4 x 10^-2
             ~ about 4.4%
```

That is still non-trivial over a decade, which is why the retry loop exists. The database enforces uniqueness, and the service retries up to 3 times:

```go
const maxShortCodeAttempts = 3

for attempt := range maxShortCodeAttempts {
    shortCode, err := generateShortCode()
    // ...
    lastErr = s.store.Create(ctx, link)
    if lastErr == nil {
        break
    }
    if !domain.IsShortCodeCollision(lastErr) {
        return nil, fmt.Errorf("settla-paymentlink: create: persisting: %w", lastErr)
    }
    s.logger.Warn("settla-paymentlink: short code collision, retrying",
        "attempt", attempt+1,
        "tenant_id", tenantID,
    )
}
```

The collision detection uses Postgres error codes at the store layer:

```go
// From store/transferdb/payment_link_store_adapter.go
var pgErr *pgconn.PgError
if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
    pgErr.ConstraintName == "payment_links_short_code_key" {
    return domain.ErrShortCodeCollision()
}
```

This is precise -- it only retries on the specific unique constraint violation for the short code column, not on any other database error.

### Cryptographic Randomness

```go
func generateShortCode() (string, error) {
    alphabetLen := big.NewInt(int64(len(shortCodeAlphabet)))
    code := make([]byte, shortCodeLength)
    for i := range code {
        n, err := rand.Int(rand.Reader, alphabetLen)
        if err != nil {
            return "", fmt.Errorf("generating random index: %w", err)
        }
        code[i] = shortCodeAlphabet[n.Int64()]
    }
    return string(code), nil
}
```

The function uses `crypto/rand` (not `math/rand`). This matters because short codes are capability tokens -- knowing a code grants payment access. If codes were generated with a predictable PRNG, an attacker who observed a few codes could predict future ones and access payment links belonging to other merchants.

Each character is selected independently using `crypto/rand.Int` with uniform distribution over the alphabet. The `math/big` approach avoids modulo bias that would occur with a naive `randomByte % 56` implementation.

---

## The Full Request Flow

Putting it all together, here is the complete flow from link creation to payment confirmation:

```
MERCHANT (authenticated)                    CUSTOMER (public)
         |                                       |
    1. POST /v1/payment-links                    |
         |                                       |
         +-- Gateway: auth plugin resolves       |
         |   API key to tenant_id                |
         |                                       |
         +-- gRPC: CreatePaymentLink             |
         |     |                                 |
         |     +-- Validate tenant active        |
         |     +-- Validate crypto enabled       |
         |     +-- Generate short code           |
         |     +-- Persist to Transfer DB        |
         |     +-- Return { link, url }          |
         |                                       |
         +<-- { url: "pay.settla.io/p/Abc..." }  |
         |                                       |
    2. Share URL via WhatsApp/email/QR --------> |
         |                                       |
         |                                  3. GET /v1/payment-links/resolve/Abc...
         |                                       |
         |                                       +-- Gateway: skip auth (public)
         |                                       +-- Per-IP rate limit check
         |                                       +-- gRPC: ResolvePaymentLink
         |                                       |     |
         |                                       |     +-- Load by short code
         |                                       |     +-- CanRedeem() check
         |                                       |     +-- Return link details
         |                                       |
         |                                       +<-- { amount, chain, token, ... }
         |                                       |
         |                                       | (customer reviews details)
         |                                       |
         |                                  4. POST /v1/payment-links/redeem/Abc...
         |                                       |
         |                                       +-- Gateway: skip auth (public)
         |                                       +-- gRPC: RedeemPaymentLink
         |                                       |     |
         |                                       |     +-- Resolve (+ CanRedeem)
         |                                       |     +-- Build idempotency key
         |                                       |     +-- depositEngine.CreateSession()
         |                                       |     |     |
         |                                       |     |     +-- Dispense address
         |                                       |     |     +-- Start chain monitor
         |                                       |     |     +-- Write outbox entries
         |                                       |     |
         |                                       |     +-- IncrementUseCount
         |                                       |     +-- Return { session, link }
         |                                       |
         |                                       +<-- { deposit_address, session_id }
         |                                       |
         |                                  5. Customer sends crypto to address
         |                                       |
         |                              (chain monitor detects payment)
         |                              (deposit engine confirms + credits)
         |                                       |
    6. Webhook: deposit.confirmed <------------- |
         |
    Done. Merchant position credited.
```

Steps 1-2 are authenticated. Steps 3-5 are public. Step 6 is a webhook delivered to the merchant's registered endpoint.

---

## Common Mistakes

1. **Adding state machine logic to the payment link.** The payment link is intentionally a thin CRUD wrapper. If you find yourself adding outbox entries or state transitions to `PaymentLink`, stop. The deposit engine already handles all that complexity. The link just stamps out sessions.

2. **Using `math/rand` for short code generation.** Short codes are capability tokens. Predictable codes let attackers enumerate payment links. Always use `crypto/rand`.

3. **Forgetting the double expiry check.** Checking only the `Status` field means a link remains usable between its expiry time and whenever a background job flips the status. The real-time `time.Now().UTC().After(*pl.ExpiresAt)` check in `CanRedeem()` closes this gap.

4. **Making the use count increment a hard failure.** If `IncrementUseCount` fails, the customer already has a deposit session. Failing the request would leave an orphaned session. The correct behavior is to log a warning and continue.

5. **Putting tenant_id in the Resolve/Redeem request.** These are public endpoints. The customer does not know (and should not know) the merchant's tenant ID. The short code lookup is global, and the link itself carries the tenant ID internally.

6. **Skipping rate limiting on public endpoints.** Without per-IP rate limits, an attacker could enumerate short codes at high speed. The gateway enforces 20 req/s per IP on all public payment link routes.

---

## Exercises

1. **Collision probability:** A deployment generates 10,000 payment links per day. Calculate the expected number of days until the first short code collision occurs (assume birthday paradox approximation: `n ~ sqrt(2 * keyspace * p)` where `p = 0.5` for 50% probability). How does this compare to the age of the universe?

2. **Idempotency key analysis:** The redeem method constructs `plink:{linkID}:{useCount+1}` as the idempotency key. Consider this scenario: two concurrent redeem requests arrive for the same link (use_count = 3, use_limit = 10). Both read use_count as 3 and construct the key `plink:abc:4`. What happens? Trace through the deposit engine's idempotency handling. Is the outcome correct?

3. **Design extension -- multi-currency links:** A merchant wants to create a link where the customer can choose to pay in USDT-on-Tron or USDC-on-Ethereum. The current `SessionConfig` has a single `Chain` and `Token`. Design a schema change that supports multiple payment options. What changes are needed in `Resolve` (to show options) and `Redeem` (to accept the customer's choice)?

4. **Security audit:** An attacker discovers a valid short code `Abc123xyz456`. What can they do with it? What can they NOT do? List every API endpoint they can and cannot call. Could they discover other valid short codes from this one?

5. **Trace the store layer:** Read `store/transferdb/payment_link_store_adapter.go`. The `GetByShortCode` method does not use RLS, but `GetByID` does. Explain why this asymmetry exists and why it is correct. What would break if `GetByShortCode` enforced RLS?

---

## What's Next

Chapter 9.4 ties the deposit and payment link systems together with the gateway API layer, showing how the TypeScript REST gateway routes map to gRPC calls and how the public/authenticated endpoint split is enforced in practice across the full request lifecycle.
