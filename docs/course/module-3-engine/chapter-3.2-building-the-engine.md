# Chapter 3.2: Building the Settlement Engine

**Reading time:** ~30 minutes
**Prerequisites:** Chapter 3.1 (Transactional Outbox)
**Code references:** `core/engine.go`, `core/store.go`, `domain/outbox.go`

---

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why the settlement engine has zero side effects and zero network
   dependencies.
2. Describe every field in the `Engine` struct and what each dependency does.
3. Walk through `CreateTransfer()` line by line, identifying validation,
   idempotency, quoting, and atomic persistence.
4. Walk through `FundTransfer()` to see the state transition + outbox pattern
   in action.
5. Define the `TransferStore`, `TenantStore`, and `Router` interfaces from
   `core/store.go`.

---

## 3.2.1 The Zero-Side-Effect Constraint

The Settla Engine has one overriding design principle:

> **Key Insight:** The engine makes ZERO network calls. It has ZERO
> dependencies on ledger, treasury, rail, or node modules. Every side effect
> is expressed as an outbox entry written atomically with the state change.

This principle is not a suggestion or a guideline. It is a hard invariant
enforced by Go's import graph:

```
Import Graph (what core/ CAN import)
======================================

core/
  |
  +-- domain/           (types, interfaces, value objects)
  +-- observability/    (metrics)
  +-- stdlib            (context, encoding/json, fmt, log/slog, time, errors)
  +-- google/uuid
  |
  X-- ledger/           FORBIDDEN
  X-- treasury/         FORBIDDEN
  X-- rail/provider/    FORBIDDEN
  X-- node/             FORBIDDEN
  X-- store/            FORBIDDEN (uses interfaces instead)
```

If someone adds `import "github.com/intellect4all/settla/treasury"` to
`core/engine.go`, the modular monolith boundary is broken. The engine would
have a direct dependency on the treasury module, making it impossible to
test in isolation and impossible to extract to a separate service later.

Instead, the engine depends on *interfaces* defined in `core/store.go`.
Concrete implementations live in `store/transferdb/` and are injected at
startup in `cmd/settla-server/main.go`.

---

## 3.2.2 The Engine Struct

Here is the actual `Engine` struct from `core/engine.go`:

```go
// Engine is the top-level settlement orchestrator. It coordinates the transfer
// lifecycle as a pure state machine: every method validates state, computes the
// next state plus outbox entries, and persists both atomically in a single
// database transaction. The engine makes ZERO network calls and has ZERO
// dependencies on ledger, treasury, rail, or node modules.
type Engine struct {
    transferStore             TransferStore
    tenantStore               TenantStore
    router                    Router
    providerRegistry          domain.ProviderRegistry
    logger                    *slog.Logger
    metrics                   *observability.Metrics
    dailyVolumeCache          sync.Map           // fallback when volumeCounter is nil
    dailyVolumeCounter        DailyVolumeCounter // optional: atomic counter (Redis)
    dailyVolumeWarnOnce       sync.Once
    requireDailyVolumeCounter bool
}
```

Ten fields. Let us examine the key ones:

### `transferStore TransferStore`

The primary data store. Handles all transfer CRUD operations plus the critical
atomic operations:

- `CreateTransferWithOutbox` -- atomically creates a transfer + outbox entries
- `TransitionWithOutbox` -- atomically transitions status + inserts outbox entries

This is the engine's *only* write path. Every mutation goes through this store.

### `tenantStore TenantStore`

Read-only access to tenant configuration. Used to:

- Verify the tenant is active (not suspended)
- Check per-transfer limits and daily volume limits
- Load fee schedules for ledger entry calculations
- Get tenant slugs for account code generation

### `router Router`

Used **only** for quote generation -- never for provider execution. The
engine calls `router.GetQuote()` when a transfer is created without a
pre-existing quote. The router scores providers and returns the best route,
but the engine never tells the router to *execute* anything.

```
IMPORTANT DISTINCTION
======================

Engine calls:     router.GetQuote(ctx, tenantID, req)
                  "What's the best route and price?"

Engine NEVER calls: router.Execute(...)     <-- does not exist
                    provider.OnRamp(...)     <-- does not exist

Side effects go through outbox intents:
  NewOutboxIntent(..., IntentProviderOnRamp, payload)
```

### `logger *slog.Logger`

Structured logger with `"module": "core.engine"` pre-set. Every log line
includes `transfer_id` and `tenant_id` for traceability.

### `providerRegistry domain.ProviderRegistry`

Used to validate that quoted providers are still available before creating a
transfer. This is a read-only check -- the registry is never asked to execute
a provider call.

### `metrics *observability.Metrics`

Prometheus metrics. The engine records:
- `TransfersTotal` -- counter by tenant, status, and corridor

### `dailyVolumeCounter DailyVolumeCounter` (Optional)

An atomic daily volume counter (typically Redis-backed via `INCRBYFLOAT`)
for race-free daily limit enforcement. When set, the in-memory `sync.Map`
cache is bypassed. When nil, the engine falls back to an approximate
in-memory cache with a 5-second TTL. The `requireDailyVolumeCounter` flag
can be set to reject transfer creation when no atomic counter is configured
and the tenant has a daily limit -- use this in production to prevent the
non-atomic fallback from being used.

### The Constructor

```go
func NewEngine(
    transferStore TransferStore,
    tenantStore TenantStore,
    router Router,
    providerRegistry domain.ProviderRegistry,
    logger *slog.Logger,
    metrics *observability.Metrics,
    opts ...EngineOption,
) *Engine {
    e := &Engine{
        transferStore:    transferStore,
        tenantStore:      tenantStore,
        router:           router,
        providerRegistry: providerRegistry,
        logger:           logger.With("module", "core.engine"),
        metrics:          metrics,
    }
    for _, opt := range opts {
        opt(e)
    }
    return e
}
```

Pure dependency injection with functional options. No global state, no init
functions, no package-level variables (except `ErrOptimisticLock` in
`store.go`). The `EngineOption` pattern allows optional dependencies to be
injected without changing the constructor signature:

```go
engine := core.NewEngine(transferStore, tenantStore, router, providerRegistry,
    logger, metrics,
    core.WithDailyVolumeCounter(redisCounter),
    core.WithRequireDailyVolumeCounter(),
)
```

This makes the engine fully testable with mock implementations -- which is
exactly how `core/engine_test.go` works.

---

## 3.2.3 The Data Interfaces (core/store.go)

The engine depends on three interfaces. Here they are in full from
`core/store.go`:

### TransferStore

```go
type TransferStore interface {
    CreateTransfer(ctx context.Context, transfer *domain.Transfer) error
    GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*domain.Transfer, error)
    GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error)
    GetTransferByExternalRef(ctx context.Context, tenantID uuid.UUID, externalRef string) (*domain.Transfer, error)
    UpdateTransfer(ctx context.Context, transfer *domain.Transfer) error
    CreateTransferEvent(ctx context.Context, event *domain.TransferEvent) error
    GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error)
    GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
    CreateQuote(ctx context.Context, quote *domain.Quote) error
    GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error)
    ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error)
    // ListTransfersFiltered returns transfers with optional server-side filtering
    // by status and search query.
    ListTransfersFiltered(ctx context.Context, tenantID uuid.UUID,
        statusFilter, searchQuery string, limit int) ([]domain.Transfer, error)

    // CountPendingTransfers returns the number of non-terminal transfers for a tenant.
    // Used to enforce per-tenant pending transfer limits (Critical Invariant #13).
    CountPendingTransfers(ctx context.Context, tenantID uuid.UUID) (int, error)

    // TransitionWithOutbox atomically updates transfer status and inserts
    // outbox entries in a single database transaction. Uses optimistic locking
    // via version check. Returns ErrOptimisticLock if version mismatch.
    TransitionWithOutbox(ctx context.Context, transferID uuid.UUID,
        newStatus domain.TransferStatus, expectedVersion int64,
        entries []domain.OutboxEntry) error

    // CreateTransferWithOutbox atomically creates a transfer and inserts
    // outbox entries in a single database transaction.
    CreateTransferWithOutbox(ctx context.Context, transfer *domain.Transfer,
        entries []domain.OutboxEntry) error
}
```

The two outbox methods are the heart of the engine's write path. Every state
mutation goes through one of these two methods. The `CountPendingTransfers`
method was added to enforce per-tenant resource limits (see Section 3.2.4,
Step c2).

### TenantStore

```go
type TenantStore interface {
    GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
    GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
}
```

Read-only. The engine never modifies tenant data.

### Router

```go
type Router interface {
    GetQuote(ctx context.Context, tenantID uuid.UUID,
        req domain.QuoteRequest) (*domain.Quote, error)
    GetRoutingOptions(ctx context.Context, tenantID uuid.UUID,
        req domain.QuoteRequest) (*domain.RouteResult, error)
}
```

Also read-only from the engine's perspective. `GetQuote` returns the best
route with pricing; `GetRoutingOptions` returns multiple ranked alternatives
for the API to display to the user.

### DailyVolumeCounter (Optional)

```go
type DailyVolumeCounter interface {
    GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
    IncrDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time,
        amount decimal.Decimal) (decimal.Decimal, error)
    SeedDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time,
        amount decimal.Decimal) (bool, error)
}
```

An optional interface for atomic daily volume tracking. When backed by Redis
`INCRBYFLOAT`, it provides race-free daily limit enforcement across all
`settla-server` replicas. `SeedDailyVolume` initializes the counter from the
database on first access (returns true if the key was set). When this
interface is nil, the engine falls back to an in-memory `sync.Map` with a
5-second TTL -- acceptable for development but not safe under concurrent
`CreateTransfer` calls in production.

### The Optimistic Lock Sentinel

```go
var ErrOptimisticLock = errors.New("settla-core: optimistic lock conflict")
```

This sentinel error is defined in `core/store.go` (not `domain/`) because it
is specific to the engine's concurrency control mechanism. Workers check for
this error to distinguish retryable conflicts from permanent failures.

---

## 3.2.4 CreateTransfer: Line-by-Line Walkthrough

`CreateTransfer` is the engine's entry point. It takes a raw request and
produces a persisted transfer with an outbox event -- all in one atomic
operation. Let us walk through every step.

### Step a: Load and Verify Tenant

```go
func (e *Engine) CreateTransfer(ctx context.Context, tenantID uuid.UUID,
    req CreateTransferRequest) (*domain.Transfer, error) {

    // a. Load tenant, verify active
    tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-core: create transfer: loading tenant %s: %w", tenantID, err)
    }
    if !tenant.IsActive() {
        return nil, domain.ErrTenantSuspended(tenantID.String())
    }
```

The `tenantID` comes from the authenticated API key, never from the request
body. This prevents tenant impersonation. If the tenant is suspended (e.g.,
for compliance reasons), all transfer creation is blocked immediately.

### Step b: Validate Inputs

```go
    // b. Validate source amount > 0, currencies supported
    if !req.SourceAmount.IsPositive() {
        return nil, domain.ErrAmountTooLow(req.SourceAmount.String(), "0")
    }
    if err := domain.ValidateCurrency(req.SourceCurrency); err != nil {
        return nil, fmt.Errorf("settla-core: create transfer: %w", err)
    }
    if err := domain.ValidateCurrency(req.DestCurrency); err != nil {
        return nil, fmt.Errorf("settla-core: create transfer: %w", err)
    }
    if req.Recipient.Name == "" || req.Recipient.Country == "" {
        return nil, fmt.Errorf("settla-core: create transfer: recipient name and country are required")
    }
```

Validation happens at the engine boundary, not in the API gateway. The gateway
does schema validation (JSON structure, required fields), but business
validation (amount limits, currency support) lives in the engine. This ensures
all callers -- REST API, gRPC, integration tests -- get the same validation.

### Step c: Per-Transfer Limit

```go
    // c. Check per-transfer limit
    if !tenant.PerTransferLimit.IsZero() && req.SourceAmount.GreaterThan(tenant.PerTransferLimit) {
        return nil, domain.ErrAmountTooHigh(req.SourceAmount.String(), tenant.PerTransferLimit.String())
    }
```

Each tenant has a configurable maximum transfer size. Lemfi might have a
$50,000 limit; Fincra might have $100,000. The zero check skips the limit
for tenants with no configured maximum.

### Step c2: Per-Tenant Pending Transfer Limit (Critical Invariant #13)

```go
    // c2. Check per-tenant pending transfer limit
    if tenant.MaxPendingTransfers > 0 {
        count, err := e.transferStore.CountPendingTransfers(ctx, tenantID)
        if err != nil {
            return nil, fmt.Errorf("settla-core: create transfer: counting pending transfers: %w", err)
        }
        if count >= tenant.MaxPendingTransfers {
            return nil, fmt.Errorf("settla-core: create transfer: tenant %s exceeded max pending transfers (%d)",
                tenantID, tenant.MaxPendingTransfers)
        }
    }
```

This enforces a per-tenant cap on the number of non-terminal transfers. A
malicious or misconfigured tenant could otherwise create unlimited pending
transfers, exhausting treasury reservations and database resources. The zero
check (0 = unlimited) allows trusted tenants to bypass the limit.

### Step d: Daily Volume Limit

```go
    // d. Check daily volume limit
    if !tenant.DailyLimitUSD.IsZero() {
        today := time.Now().UTC().Truncate(24 * time.Hour)
        dailyVolume, err := e.transferStore.GetDailyVolume(ctx, tenantID, today)
        if err != nil {
            return nil, fmt.Errorf("settla-core: create transfer: checking daily volume: %w", err)
        }
        if dailyVolume.Add(req.SourceAmount).GreaterThan(tenant.DailyLimitUSD) {
            return nil, domain.ErrDailyLimitExceeded(tenantID.String())
        }
    }
```

This queries the sum of all transfer amounts for the current UTC day, excluding
FAILED and REFUNDED transfers. Note the use of `decimal.Add` -- never float
arithmetic for money.

When a `DailyVolumeCounter` is configured (production), the engine uses
`IncrDailyVolume` for atomic increment + check via Redis, then seeds the
counter from the database on first access. Without it, the engine falls back
to an in-memory `sync.Map` cache with 5-second TTL.

> **Key Insight:** The daily volume check is *not* atomic with the transfer
> creation. Two concurrent requests could both pass the check and both create
> transfers that together exceed the limit. This is acceptable because: (a)
> the limit is a safety guardrail, not a hard cap, and (b) making it atomic
> would require a table lock that destroys throughput at 580 TPS. The Redis
> `DailyVolumeCounter` mitigates this significantly by making the increment
> atomic across replicas.

### Step e: Idempotency Enforcement

```go
    // e. Check idempotency key
    if req.IdempotencyKey != "" {
        existing, err := e.transferStore.GetTransferByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
        if err == nil && existing != nil {
            return existing, nil
        }
    }
```

If the client sends the same idempotency key twice, the engine returns the
existing transfer without creating a new one. The lookup is scoped by
`tenantID` -- a key that belongs to Lemfi cannot match a transfer created by
Fincra. The SQL includes a 24-hour window:

```sql
WHERE tenant_id = $1 AND idempotency_key = $2
  AND created_at >= now() - INTERVAL '24 hours'
```

### Step f: Quote Resolution

```go
    // f. Fetch and validate quote
    var quote *domain.Quote
    if req.QuoteID != nil {
        quote, err = e.transferStore.GetQuote(ctx, tenantID, *req.QuoteID)
        if err != nil { /* ... */ }
        if quote.TenantID != tenantID { /* ... cross-tenant check ... */ }
        if quote.IsExpired() { /* ... */ }
    } else {
        // Get a fresh quote from the router
        quote, err = e.router.GetQuote(ctx, tenantID, domain.QuoteRequest{
            SourceCurrency: req.SourceCurrency,
            SourceAmount:   req.SourceAmount,
            DestCurrency:   req.DestCurrency,
            DestCountry:    req.Recipient.Country,
        })
        if err != nil { /* ... */ }
        if err := e.transferStore.CreateQuote(ctx, quote); err != nil { /* ... */ }
        req.QuoteID = &quote.ID
    }
```

Two paths: (1) the client already has a quote ID from a previous
`GET /v1/quotes` call, or (2) the engine generates an inline quote on the
fly. Either way, the transfer ends up with a persisted quote that captures
the agreed-upon rate, fees, and routing.

### Step g: Build Transfer Record

```go
    now := time.Now().UTC()
    transfer := &domain.Transfer{
        ID:             uuid.New(),
        TenantID:       tenantID,
        ExternalRef:    req.ExternalRef,
        IdempotencyKey: req.IdempotencyKey,
        Status:         domain.TransferStatusCreated,
        Version:        1,
        SourceCurrency: req.SourceCurrency,
        SourceAmount:   req.SourceAmount,
        DestCurrency:   req.DestCurrency,
        DestAmount:     quote.DestAmount,
        StableCoin:     quote.Route.StableCoin,
        StableAmount:   quote.StableAmount,
        Chain:          quote.Route.Chain,
        FXRate:            quote.FXRate,
        Fees:              quote.Fees,
        OnRampProviderID:  quote.Route.OnRampProvider,
        OffRampProviderID: quote.Route.OffRampProvider,
        Sender:            req.Sender,
        Recipient:         req.Recipient,
        QuoteID:           req.QuoteID,
        CreatedAt:         now,
        UpdatedAt:         now,
    }
```

All timestamps are UTC. The initial status is always `CREATED`. Version starts
at 1. The provider IDs come from the quote's routing decision -- the engine
does not choose providers, the router does.

### Step g3-g4: Fee Validation (Critical Invariant #12)

After building the transfer record, the engine validates fees:

```go
    // g3. Validate total fees are less than source amount
    if transfer.Fees.TotalFeeUSD.GreaterThanOrEqual(transfer.SourceAmount) {
        return nil, fmt.Errorf("settla-core: create transfer: total fees (%s) must be less than source amount (%s)",
            transfer.Fees.TotalFeeUSD, transfer.SourceAmount)
    }

    // g4. Reject zero fees — indicates misconfigured fee schedule or quote
    if transfer.Fees.TotalFeeUSD.IsZero() {
        return nil, fmt.Errorf("settla-core: create transfer: zero fees not permitted for tenant %s"+
            " — check fee schedule configuration", tenantID)
    }
```

The zero-fee rejection (Critical Invariant #12) prevents revenue loss from
misconfigured fee schedules. If a tenant's fee schedule is accidentally set
to zero basis points, the engine rejects all transfers rather than processing
them for free. This is a hard failure, not a warning.

### Step h: Build Outbox Event

```go
    payload, err := json.Marshal(transfer)
    if err != nil { /* ... */ }
    entries := []domain.OutboxEntry{
        domain.NewOutboxEvent("transfer", transfer.ID, tenantID,
            domain.EventTransferCreated, payload),
    }
```

Note: this is a **NewOutboxEvent**, not a NewOutboxIntent. The
`transfer.created` event is a notification -- no worker needs to execute
anything. Subscribers (analytics, audit logs) can react to it, but it is not
a command.

### Step i: Atomic Persistence

```go
    if err := e.transferStore.CreateTransferWithOutbox(ctx, transfer, entries); err != nil {
        return nil, fmt.Errorf("settla-core: create transfer: persisting: %w", err)
    }
```

One method call, one database transaction, two operations (INSERT transfer +
INSERT outbox). Either both succeed or neither does. This is where the outbox
pattern pays off -- we never have a transfer without its creation event.

### Post-Write: Metrics and Logging

```go
    corridor := observability.FormatCorridor(
        string(req.SourceCurrency), string(req.DestCurrency))
    if e.metrics != nil {
        e.metrics.TransfersTotal.WithLabelValues(
            tenantID.String(), string(domain.TransferStatusCreated), corridor).Inc()
    }

    e.logger.Info("settla-core: transfer created",
        "transfer_id", transfer.ID,
        "tenant_id", tenantID,
        "corridor", corridor,
        "source_amount", req.SourceAmount.String(),
    )

    return transfer, nil
```

Metrics and logging happen *after* the write succeeds. If the write fails,
no metric is emitted and no log is written -- preventing misleading signals.

---

## 3.2.5 FundTransfer: State Transition + Outbox in Action

`FundTransfer` is the first state transition after creation. It demonstrates
the core pattern that every subsequent transition follows:

```go
func (e *Engine) FundTransfer(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID) error {

    transfer, err := e.loadTransferForStep(ctx, tenantID, transferID,
        domain.TransferStatusCreated)
    if err != nil {
        return fmt.Errorf("settla-core: fund transfer %s: %w", transferID, err)
    }
```

### The loadTransferForStep Helper

```go
func (e *Engine) loadTransferForStep(ctx context.Context, tenantID uuid.UUID,
    transferID uuid.UUID, expectedStatus domain.TransferStatus) (*domain.Transfer, error) {

    transfer, err := e.loadTransfer(ctx, tenantID, transferID)
    if err != nil {
        return nil, err
    }
    if transfer.Status != expectedStatus {
        return nil, domain.ErrInvalidTransition(string(transfer.Status), "next")
    }
    return transfer, nil
}
```

This is a guard: if the transfer is not in the expected status, the
transition is rejected immediately. This prevents out-of-order processing
when NATS redelivers a message that has already been handled.

### Build the Intent Payload

```go
    location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

    reservePayload, err := json.Marshal(domain.TreasuryReservePayload{
        TransferID: transfer.ID,
        TenantID:   transfer.TenantID,
        Currency:   transfer.SourceCurrency,
        Amount:     transfer.SourceAmount,
        Location:   location,
    })
```

The `location` field tells the treasury worker *where* to reserve funds.
`"bank:gbp"` means "reserve GBP from the bank holding account." This allows
the treasury module to manage multiple holding locations per currency.

### Create Outbox Entries

```go
    entries := []domain.OutboxEntry{
        domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID,
            domain.IntentTreasuryReserve, reservePayload),
        domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID,
            domain.EventTransferFunded, transferEventPayload(transfer.ID, transfer.TenantID)),
    }
```

Two entries: one **intent** (treasury must execute this) and one **event**
(notification that the transfer is funded). The intent goes to
SETTLA_TREASURY stream; the event goes to SETTLA_TRANSFERS stream.

### Atomic Transition

```go
    if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID,
        domain.TransferStatusFunded, transfer.Version, entries); err != nil {
        return wrapTransitionError(err, "fund transfer", transferID)
    }
```

This is the atomic write: update status to FUNDED (with optimistic lock on
`transfer.Version`) and insert both outbox entries in the same transaction.

### The wrapTransitionError Helper

```go
func wrapTransitionError(err error, step string, transferID uuid.UUID) error {
    if err == nil {
        return nil
    }
    if errors.Is(err, ErrOptimisticLock) {
        return fmt.Errorf("settla-core: %s: concurrent modification of transfer %s: %w",
            step, transferID, ErrOptimisticLock)
    }
    return fmt.Errorf("settla-core: %s %s: %w", step, transferID, err)
}
```

Optimistic lock failures get special treatment so workers can distinguish
"try again" (retryable) from "something is broken" (permanent).

---

## 3.2.6 The Pattern: Every Transition Method Follows the Same Shape

```
Every engine method follows this template:
===========================================

1. Load transfer + verify expected status
   transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, expectedStatus)

2. Build intent/event payloads (JSON marshal)
   payload, err := json.Marshal(domain.SomePayload{...})

3. Create outbox entries
   entries := []domain.OutboxEntry{
       domain.NewOutboxIntent(...),  // 0 or more
       domain.NewOutboxEvent(...),   // 0 or more
   }

4. Atomic transition
   err := e.transferStore.TransitionWithOutbox(
       ctx, transfer.ID, newStatus, transfer.Version, entries)

5. Log + metrics
```

This consistency makes the codebase predictable. Once you understand
`FundTransfer`, you understand every other transition method. The only
differences are:

- Which status the transfer must be in (step 1)
- What payloads are built (step 2)
- Which intents/events are created (step 3)
- What the new status is (step 4)

---

## 3.2.7 The CreateTransferRequest

The input struct for `CreateTransfer`:

```go
type CreateTransferRequest struct {
    ExternalRef    string
    IdempotencyKey string
    SourceCurrency domain.Currency
    SourceAmount   decimal.Decimal
    DestCurrency   domain.Currency
    Sender         domain.Sender
    Recipient      domain.Recipient
    QuoteID        *uuid.UUID
}
```

- `ExternalRef` -- the client's own reference for this transfer (e.g., their
  internal payment ID). Optional, used for lookups via `GetTransferByExternalRef`.
- `IdempotencyKey` -- ensures at-most-once creation. Required for production
  use; the engine does not reject empty keys but cannot deduplicate without one.
- `QuoteID` -- optional pointer. If nil, the engine generates an inline quote.
  If provided, the engine validates and uses the pre-existing quote.

---

## 3.2.8 The Pattern Replicated: Sister Engines

The pure state machine pattern from the settlement engine is now replicated
across four other bounded contexts. Each follows the same design: read state,
validate, compute the next state + outbox entries, persist atomically. Zero
side effects.

```
Sister Engines
===============

Engine                       Package                   Domain Aggregate
---------------------------  ------------------------  --------------------------
Settlement Engine            core/engine.go            Transfer
Crypto Deposit Engine        core/deposit/engine.go    DepositSession
Bank Deposit Engine          core/bankdeposit/engine.go BankDepositSession
Position Transaction Engine  core/position/engine.go   PositionTransaction
Payment Link Service         core/paymentlink/service.go PaymentLink
```

Each engine has its own state machine, its own set of outbox intents, and its
own dedicated worker(s). The interfaces follow the same adapter pattern:

- `core/deposit.DepositStore` -- implemented by `store/transferdb/deposit_store_adapter.go`
- `core/bankdeposit.BankDepositStore` -- implemented by `store/transferdb/bank_deposit_store_adapter.go`
- `core/position.PositionStore` -- implemented by `store/transferdb/position_transaction_adapter.go`
- `core/paymentlink.PaymentLinkStore` -- implemented by `store/transferdb/payment_link_store_adapter.go`

The position engine (`core/position/engine.go`) manages `RequestTopUp` and
`RequestWithdrawal` flows, producing `position.credit` and `position.debit`
outbox intents that the TreasuryWorker consumes.

---

## Common Mistakes

1. **Adding a network call to the engine.** If you find yourself writing
   `http.Get(...)` or `grpc.Dial(...)` in `core/engine.go`, stop. That
   belongs in a worker. Create an intent type instead.

2. **Skipping the version parameter.** `TransitionWithOutbox` uses optimistic
   locking. If you pass `0` instead of `transfer.Version`, the UPDATE will
   fail for any transfer that has been transitioned at least once.

3. **Marshalling errors silently.** Every `json.Marshal` call in the engine
   returns an error. Do not ignore it -- a marshalling failure means the
   outbox entry would have a nil or corrupt payload, which would crash the
   worker downstream.

4. **Using `NewOutboxEvent` for something a worker must execute.** If the
   message requires action, use `NewOutboxIntent`. Events are fire-and-forget
   notifications; intents are commands.

5. **Testing with real dependencies.** The engine is designed for mock
   injection. `core/engine_test.go` uses `mockTransferStore` and
   `mockTenantStore` -- never a real database. Integration tests belong in
   `tests/integration/`.

---

## Exercises

1. **Add a validation rule:** The engine currently does not validate that
   `SourceCurrency != DestCurrency` (same-currency transfers are meaningless
   for cross-border settlement). Where in `CreateTransfer` would you add this
   check? Write the code.

2. **Trace the version:** Starting from `CreateTransfer` (version=1), trace
   the version number through `FundTransfer` (version=?), `InitiateOnRamp`
   (version=?), and so on until `CompleteTransfer`. What is the final version?

3. **Mock the router:** Write a test that verifies `CreateTransfer` calls
   `router.GetQuote` when `req.QuoteID` is nil, and does NOT call it when
   `req.QuoteID` is provided. Use the mock pattern from `engine_test.go`.

4. **Error wrapping:** Settla wraps every error with context (e.g.,
   `fmt.Errorf("settla-core: fund transfer %s: %w", ...)`). Why is the
   `%w` verb used instead of `%v`? What does this enable for callers?

---

## What's Next

In Chapter 3.3, we follow the transfer through its complete lifecycle -- from
`CreateTransfer` through on-ramp, settlement, off-ramp, and completion --
examining every state transition method and the outbox entries each produces.
