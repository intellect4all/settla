# Chapter 1.3: Domain Modeling -- Types That Prevent Bugs

**Estimated reading time:** 30 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the design philosophy behind Settla's `domain/` package and its zero-dependency constraint
2. Use value objects (`Money`, `Currency`, `IdempotencyKey`) to make invalid states unrepresentable
3. Articulate why `float64` must never be used for money and demonstrate the failure with concrete numbers
4. Read and write compile-time interface checks in Go
5. Design entity types that carry their validation logic and enforce business rules at the type level

---

## The Philosophy: Zero-Dependency Domain Core

The `domain/` package is the center of Settla's architecture. Every other module depends on it, but it depends on nothing except the standard library, `shopspring/decimal`, and `google/uuid`.

```go
// From domain/doc.go
// Package domain defines shared domain types, interfaces, and errors
// used across all Settla modules.
//
// Dependencies are limited to stdlib + shopspring/decimal + google/uuid.
// No infrastructure imports (DB, HTTP, Redis, NATS, TigerBeetle) are
// allowed in this package.
```

This is not an accident. It is a deliberate constraint that yields three benefits:

1. **Testability**: Domain logic can be tested without spinning up databases, message queues, or external services. A test for `ValidateEntries` or `CalculateFee` runs in microseconds with zero setup.
2. **Portability**: The domain package can be extracted to a separate repository or shared library without pulling in infrastructure dependencies.
3. **Clarity**: When you read a domain type, you know it represents business concepts, not infrastructure concerns. There is no leakage of SQL, HTTP, or serialization concerns into the business vocabulary.

```
    Dependency Direction (strict, enforced):
    =========================================

    domain/          <-- depends on NOTHING (stdlib + decimal + uuid only)
       ^
       |
    core/            <-- depends on domain/ only
       ^
       |
    ledger/          <-- depends on domain/ only
    treasury/        <-- depends on domain/ only
    rail/            <-- depends on domain/ only
       ^
       |
    store/           <-- depends on domain/ (for types) + database drivers
    api/             <-- depends on domain/ + generated protos
    cmd/             <-- depends on everything (wiring point)
```

> **Key Insight:** The `domain/` package is the only package that every other module is allowed to import. Modules never import sibling packages directly. The `ledger/` package does not import `treasury/`. The `core/` engine does not import `rail/`. They communicate exclusively through `domain/` interfaces. This rule is what makes the modular monolith possible -- we explore it fully in Chapter 1.6.

---

## Value Object: Currency

The simplest value object in Settla is `Currency`. It is a typed string, not a raw `string`:

```go
// From domain/money.go
type Currency string

const (
    CurrencyNGN  Currency = "NGN"
    CurrencyUSD  Currency = "USD"
    CurrencyGBP  Currency = "GBP"
    CurrencyEUR  Currency = "EUR"
    CurrencyGHS  Currency = "GHS"
    CurrencyKES  Currency = "KES"
    CurrencyUSDT Currency = "USDT"
    CurrencyUSDC Currency = "USDC"
)
```

Why not just use `string`? Because types carry meaning and the compiler can enforce correctness:

```go
// With raw strings (dangerous):
func Transfer(from string, to string, amount float64, currency string) {
    // Which string is the currency? "GBP"? "gbp"? "British Pounds"?
    // What if someone passes a tenant ID as currency? Compiles fine.
    // Bug waits in production.
}

// With typed Currency (safe):
func Transfer(from uuid.UUID, to uuid.UUID, amount decimal.Decimal, currency Currency) {
    // Cannot accidentally pass a tenant_id (uuid.UUID) where Currency is expected.
    // Cannot accidentally pass a float64 where decimal.Decimal is expected.
    // Compiler catches type mismatches immediately.
}
```

The validation function provides a single point of truth for supported currencies:

```go
// From domain/money.go
var SupportedCurrencies = map[Currency]bool{
    CurrencyNGN: true, CurrencyUSD: true, CurrencyGBP: true,
    CurrencyEUR: true, CurrencyGHS: true, CurrencyKES: true,
    CurrencyUSDT: true, CurrencyUSDC: true,
}

func ValidateCurrency(c Currency) error {
    if !SupportedCurrencies[c] {
        return fmt.Errorf("settla-domain: unsupported currency: %s", c)
    }
    return nil
}
```

---

## Value Object: Money

`Money` is the most important value object in the codebase. It pairs a `decimal.Decimal` amount with a `Currency`, making it impossible to accidentally add GBP to NGN:

```go
// From domain/money.go
// Money is an immutable, currency-qualified decimal amount.
// All monetary math MUST use shopspring/decimal. Never use float64 for money.
type Money struct {
    Amount   decimal.Decimal
    Currency Currency
}
```

### Construction with Validation

```go
// From domain/money.go
func NewMoney(amount string, currency Currency) (Money, error) {
    if err := ValidateCurrency(currency); err != nil {
        return Money{}, err
    }
    d, err := decimal.NewFromString(amount)
    if err != nil {
        return Money{}, fmt.Errorf("settla-domain: invalid amount %q: %w", amount, err)
    }
    return Money{Amount: d, Currency: currency}, nil
}
```

Notice: the amount is accepted as a `string`, not a `float64`. This prevents floating-point corruption at the point of entry. The `decimal.NewFromString("100.50")` function parses into an exact decimal representation with no IEEE 754 approximation.

### Currency-Safe Arithmetic

The `Add` and `Sub` methods enforce currency matching at runtime:

```go
// From domain/money.go
func (m Money) Add(other Money) (Money, error) {
    if m.Currency != other.Currency {
        return Money{}, fmt.Errorf("settla-domain: cannot add %s to %s: %w",
            other.Currency, m.Currency,
            ErrCurrencyMismatch("add", string(m.Currency), string(other.Currency)))
    }
    return Money{Amount: m.Amount.Add(other.Amount), Currency: m.Currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
    if m.Currency != other.Currency {
        return Money{}, fmt.Errorf("settla-domain: cannot subtract %s from %s: %w",
            other.Currency, m.Currency,
            ErrCurrencyMismatch("subtract", string(m.Currency), string(other.Currency)))
    }
    return Money{Amount: m.Amount.Sub(other.Amount), Currency: m.Currency}, nil
}
```

Without this, adding GBP to NGN silently produces nonsense:

```go
// Without Money type (silent bug):
gbpAmount := 100.0
ngnAmount := 50000.0
total := gbpAmount + ngnAmount  // 50100.0 -- meaningless, no error

// With Money type (caught immediately):
gbp, _ := NewMoney("100", CurrencyGBP)
ngn, _ := NewMoney("50000", CurrencyNGN)
_, err := gbp.Add(ngn)
// err: "settla-domain: cannot add NGN to GBP: currency mismatch"
```

The `Mul` method does not require currency matching because multiplying by a scalar (like a fee rate) produces the same currency:

```go
// From domain/money.go
func (m Money) Mul(factor decimal.Decimal) Money {
    return Money{Amount: m.Amount.Mul(factor), Currency: m.Currency}
}
```

And the `String()` method provides consistent formatting:

```go
// From domain/money.go
func (m Money) String() string {
    return fmt.Sprintf("%s %s", m.Amount.StringFixed(2), m.Currency)
}
// Output: "1234.56 GBP"
```

---

## Why Float64 Must Never Touch Money

This is a critical invariant of the codebase, listed first in the CLAUDE.md critical invariants:

> **Decimal-only monetary math** -- `shopspring/decimal` (Go) / `decimal.js` (TS) for ALL monetary amounts. Never use float/float64 for money.

### The IEEE 754 Problem

IEEE 754 floating-point cannot represent most decimal fractions exactly. The number `0.1` in binary is a repeating fraction, similar to how `1/3` is repeating in decimal:

```
    0.1 in decimal:  0.1  (exact)
    0.1 in float64:  0.1000000000000000055511151231257827...  (approximation)
```

### Demonstration at Scale

```go
// The fundamental problem:
a := 0.1 + 0.2
fmt.Println(a)        // 0.30000000000000004
fmt.Println(a == 0.3) // false

// At Settla's scale, this compounds:
// Scenario: 50M transfers/day, each with a fee calculation
dailyFees := 0.0
for i := 0; i < 50_000_000; i++ {
    dailyFees += 0.10  // 10 cents per transfer
}
fmt.Printf("%.20f\n", dailyFees)
// Expected: 5000000.00000000000000000000
// Actual:   4999999.99999957323074340820  (or similar)
// Drift:    ~$0.0000004
```

The drift seems tiny. But consider the real-world consequences:

```
    1. Ledger entries must balance: debits == credits.
       Float drift means debits = 5000000.0000004, credits = 5000000.0000000.
       The entry is rejected or, worse, silently stored as imbalanced.

    2. Fee calculations multiply:
       On-ramp fee: $10,000 x 0.0040 = $40.0000000000000xxxx
       Off-ramp fee: $10,000 x 0.0035 = $35.0000000000000xxxx
       Each calculation introduces a new approximation.

    3. Reconciliation at end-of-day:
       Sum all debits. Sum all credits. They should match.
       With float64: they differ by some unpredictable amount.
       With decimal:  they match exactly, every time.

    4. Regulatory audit:
       "Why does your ledger not balance?"
       "Floating-point rounding errors."
       This is not an acceptable answer.
```

### The Decimal Solution

```go
// With shopspring/decimal:
a := decimal.NewFromString("0.1")
b := decimal.NewFromString("0.2")
c := a.Add(b)
fmt.Println(c.String())  // "0.3" (exact)

// Fee calculation (exact):
amount := decimal.NewFromInt(10000)
bps := decimal.NewFromInt(40)
divisor := decimal.NewFromInt(10000)
fee := amount.Mul(bps).Div(divisor)
fmt.Println(fee.String())  // "40" (exactly $40.00, no rounding)
```

> **Key Insight:** The `float64` prohibition is not academic pedantry. It is a regulatory and operational requirement. A ledger where debits do not exactly equal credits is a broken ledger. At 250M ledger entries per day, even microscopic rounding errors compound into audit failures. `shopspring/decimal` uses arbitrary-precision arithmetic that never introduces rounding unless you explicitly request it with methods like `Round()` or `StringFixed()`.

---

## Value Object: IdempotencyKey

Every mutation in Settla must be idempotent -- sending the same request twice must produce the same result, not create a duplicate transfer. The `IdempotencyKey` type ensures keys are validated at creation:

```go
// From domain/transfer.go
type IdempotencyKey string

func NewIdempotencyKey(key string) (IdempotencyKey, error) {
    if key == "" {
        return "", fmt.Errorf("settla-domain: idempotency key must not be empty")
    }
    if len(key) > 256 {
        return "", fmt.Errorf("settla-domain: idempotency key exceeds 256 characters")
    }
    return IdempotencyKey(key), nil
}

func (k IdempotencyKey) String() string { return string(k) }
```

This is the value object pattern: the constructor validates invariants (non-empty, max length), and the distinct type prevents raw strings from being used where validated keys are expected.

---

## Entity Design: The Transfer Aggregate

The `Transfer` struct is Settla's primary aggregate -- the entity that controls consistency for a settlement request:

```go
// From domain/transfer.go
type Transfer struct {
    ID             uuid.UUID
    TenantID       uuid.UUID       // Multi-tenancy: every transfer belongs to a tenant
    ExternalRef    string
    IdempotencyKey string
    Status         TransferStatus  // State machine position (typed, not string)
    Version        int64           // Optimistic locking counter

    SourceCurrency Currency
    SourceAmount   decimal.Decimal // Never float64
    DestCurrency   Currency
    DestAmount     decimal.Decimal // Never float64

    StableCoin   Currency
    StableAmount decimal.Decimal   // Never float64
    Chain        string

    FXRate decimal.Decimal         // Never float64
    Fees   FeeBreakdown

    OnRampProviderID  string
    OffRampProviderID string

    Sender    Sender
    Recipient Recipient

    QuoteID *uuid.UUID

    BlockchainTxs []BlockchainTx

    CreatedAt     time.Time
    UpdatedAt     time.Time
    FundedAt      *time.Time       // nil until funded
    CompletedAt   *time.Time       // nil until completed
    FailedAt      *time.Time       // nil until failed
    FailureReason string
    FailureCode   string
}
```

Several design decisions are encoded directly in this struct:

1. **`TenantID uuid.UUID`**: Not optional. Every transfer belongs to a tenant. The database enforces `NOT NULL` on this column, and the code enforces it at the type level. There is no `Transfer` without a `TenantID`.

2. **`Version int64`**: Optimistic locking. When two concurrent processes try to update the same transfer, the `UPDATE ... WHERE version = ?` query ensures the second one detects the version mismatch and fails cleanly instead of silently overwriting the first update's work.

3. **`decimal.Decimal` for all monetary fields**: `SourceAmount`, `DestAmount`, `StableAmount`, `FXRate` -- every field that holds money or a rate uses `decimal.Decimal`. There is no escape hatch.

4. **`*time.Time` for optional timestamps**: `FundedAt`, `CompletedAt`, `FailedAt` are pointers because they are only set when the transfer reaches that state. A nil pointer is semantically clearer than a zero-value `time.Time{}` (which is `0001-01-01 00:00:00`) that could be mistaken for a valid timestamp.

5. **`TransferStatus` not `string`**: The status field uses a typed enum, preventing typos and invalid values from being assigned without explicit type conversion.

### The Fee Breakdown

Every transfer records exactly how much was charged and where:

```go
// From domain/transfer.go
type FeeBreakdown struct {
    OnRampFee   decimal.Decimal
    NetworkFee  decimal.Decimal
    OffRampFee  decimal.Decimal
    TotalFeeUSD decimal.Decimal
}
```

All four fields are `decimal.Decimal`. The invariant is that `TotalFeeUSD` must equal `OnRampFee + NetworkFee + OffRampFee`. This is checked during quote generation and transfer creation.

---

## Domain Interfaces: Contracts Without Implementation

The domain package defines interfaces that other modules must implement. These interfaces are the contracts that hold the modular monolith together.

### The Ledger Interface

```go
// From domain/ledger.go
type Ledger interface {
    PostEntries(ctx context.Context, entry JournalEntry) (*JournalEntry, error)
    GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error)
    GetEntries(ctx context.Context, accountCode string, from, to time.Time,
               limit, offset int) ([]EntryLine, error)
    ReverseEntry(ctx context.Context, entryID uuid.UUID, reason string) (*JournalEntry, error)
}
```

The `Ledger` interface says nothing about TigerBeetle, Postgres, or CQRS. The core engine depends on this interface and never knows (or cares) how entries are posted. The actual implementation in `ledger/` uses TigerBeetle for writes and Postgres for reads, but the engine just calls `PostEntries`.

### The TreasuryManager Interface

```go
// From domain/treasury.go
type TreasuryManager interface {
    Reserve(ctx context.Context, tenantID uuid.UUID, currency Currency,
            location string, amount decimal.Decimal, reference uuid.UUID) error
    Release(ctx context.Context, tenantID uuid.UUID, currency Currency,
            location string, amount decimal.Decimal, reference uuid.UUID) error
    GetPositions(ctx context.Context, tenantID uuid.UUID) ([]Position, error)
    GetPosition(ctx context.Context, tenantID uuid.UUID, currency Currency,
                location string) (*Position, error)
    GetLiquidityReport(ctx context.Context, tenantID uuid.UUID) (*LiquidityReport, error)
}
```

The comments on this interface explicitly state: "Reserve and Release operate on in-memory atomic counters with nanosecond latency. They never hit the database directly." This is an architectural invariant encoded in the interface documentation -- implementors must preserve this property.

### The Router Interface

```go
// From domain/provider.go
type Router interface {
    Route(ctx context.Context, req RouteRequest) (*RouteResult, error)
}
```

The Router has a single method. It takes a `RouteRequest` (tenant, source currency, target currency, amount) and returns a `RouteResult` (selected providers, chain, fee, rate, score breakdown, alternatives). The core engine calls this without knowing about scoring weights, provider registries, or liquidity calculations.

---

## Compile-Time Interface Checks

Go interfaces are implicit -- a type satisfies an interface if it has the right methods. This is powerful but dangerous: you can accidentally break an interface by changing a method signature, and the compiler will not tell you unless the implementing type is actually used as that interface type somewhere in the code.

Settla uses compile-time interface checks throughout the codebase:

```go
// From ledger/ledger.go
var _ domain.Ledger = (*Service)(nil)

// From treasury/manager.go
var _ domain.TreasuryManager = (*Manager)(nil)

// From rail/router/router.go
var _ domain.Router = (*Router)(nil)

// From rail/provider/registry.go
var _ domain.ProviderRegistry = (*Registry)(nil)

// From node/messaging/publisher.go
var _ domain.EventPublisher = (*Publisher)(nil)
var _ domain.EventPublisher = (*CircuitBreakerPublisher)(nil)

// From rail/blockchain/tron/client.go
var _ domain.BlockchainClient = (*Client)(nil)

// From rail/blockchain/ethereum/client.go
var _ domain.BlockchainClient = (*Client)(nil)

// From rail/blockchain/solana/client.go
var _ domain.BlockchainClient = (*Client)(nil)

// From rail/provider/settla/onramp.go
var _ domain.OnRampProvider = (*OnRampProvider)(nil)

// From rail/provider/settla/offramp.go
var _ domain.OffRampProvider = (*OffRampProvider)(nil)

// From resilience/wrappers.go
var _ domain.Ledger = (*CircuitBreakerLedger)(nil)
```

Each line creates a package-level variable of the interface type and assigns a nil pointer of the implementing type. This costs zero at runtime (the compiler discards the variable) but provides an immediate build error if the implementation drifts from the interface.

> **Key Insight:** Without `var _ Interface = (*Impl)(nil)`, interface drift is silent. You add a method to `domain.Ledger`, and the `ledger.Service` still compiles because no code in the `ledger/` package assigns `*Service` to `domain.Ledger` directly -- that happens in `cmd/settla-server/main.go` during wiring. Without the compile-time check, the break only surfaces when you try to build the server binary. With the check, it surfaces immediately when you build the `ledger/` package.

---

## Structured Domain Errors

Settla's errors carry machine-readable codes alongside human-readable messages:

```go
// From domain/errors.go
type DomainError struct {
    code    string
    message string
    err     error
}

func (e *DomainError) Error() string {
    if e.err != nil {
        return fmt.Sprintf("%s: %v", e.message, e.err)
    }
    return e.message
}

func (e *DomainError) Code() string  { return e.code }
func (e *DomainError) Unwrap() error { return e.err }
```

Error codes are centralized constants:

```go
// From domain/errors.go (selected)
const (
    CodeQuoteExpired        = "QUOTE_EXPIRED"
    CodeInsufficientFunds   = "INSUFFICIENT_FUNDS"
    CodeInvalidTransition   = "INVALID_TRANSITION"
    CodeProviderError       = "PROVIDER_ERROR"
    CodeChainError          = "CHAIN_ERROR"
    CodeLedgerImbalance     = "LEDGER_IMBALANCE"
    CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
    CodeOptimisticLock      = "OPTIMISTIC_LOCK"
    CodeTenantSuspended     = "TENANT_SUSPENDED"
    CodeDailyLimitExceeded  = "DAILY_LIMIT_EXCEEDED"
    CodeRateLimitExceeded   = "RATE_LIMIT_EXCEEDED"
    CodeTransferNotFound    = "TRANSFER_NOT_FOUND"
    CodeProviderUnavailable = "PROVIDER_UNAVAILABLE"
    CodeBlockchainReorg     = "BLOCKCHAIN_REORG"
    CodeCompensationFailed  = "COMPENSATION_FAILED"
)
```

Constructor functions ensure consistent error formatting and carry contextual data:

```go
// From domain/errors.go
func ErrInsufficientFunds(currency, location string) *DomainError {
    return &DomainError{
        code:    CodeInsufficientFunds,
        message: fmt.Sprintf("settla-domain: insufficient %s funds at %s", currency, location),
    }
}

func ErrInvalidTransition(from, to string) *DomainError {
    return &DomainError{
        code:    CodeInvalidTransition,
        message: fmt.Sprintf("settla-domain: invalid transition from %s to %s", from, to),
    }
}

func ErrProviderError(providerID string, err error) *DomainError {
    return &DomainError{
        code:    CodeProviderError,
        message: fmt.Sprintf("settla-domain: provider %s error", providerID),
        err:     err,  // wraps the underlying provider error
    }
}
```

The API gateway maps these codes to HTTP status codes:

```
    CodeInsufficientFunds   -> 402 Payment Required
    CodeInvalidTransition   -> 409 Conflict
    CodeTenantSuspended     -> 403 Forbidden
    CodeRateLimitExceeded   -> 429 Too Many Requests
    CodeIdempotencyConflict -> 409 Conflict
    CodeOptimisticLock      -> 409 Conflict (retry with fresh version)
    CodeTransferNotFound    -> 404 Not Found
    CodeProviderError       -> 502 Bad Gateway
    CodeProviderUnavailable -> 503 Service Unavailable
```

---

## The Ledger Types: Enforcing Double-Entry

The domain package defines the types that enforce double-entry bookkeeping at the type level:

```go
// From domain/ledger.go
type EntryType string

const (
    EntryTypeDebit  EntryType = "DEBIT"
    EntryTypeCredit EntryType = "CREDIT"
)

type Posting struct {
    AccountCode string
    EntryType   EntryType
    Amount      decimal.Decimal // Always positive; direction is EntryType
    Currency    Currency
    Description string
}
```

And the validation function that enforces the fundamental accounting invariant:

```go
// From domain/ledger.go
func ValidateEntries(lines []EntryLine) error {
    if len(lines) < 2 {
        return ErrLedgerImbalance(fmt.Sprintf("need at least 2 lines, got %d", len(lines)))
    }

    // Check all amounts are positive
    for _, line := range lines {
        if !line.Amount.IsPositive() {
            return ErrLedgerImbalance(
                fmt.Sprintf("line amount must be positive, got %s", line.Amount))
        }
    }

    // Check debits == credits per currency
    byCurrency := make(map[Currency]*balance)
    for _, line := range lines {
        // ... accumulate debits and credits per currency
    }

    for currency, b := range byCurrency {
        if !b.debits.Equal(b.credits) {
            return ErrLedgerImbalance(fmt.Sprintf("%s debits %s != credits %s",
                currency, b.debits.StringFixed(2), b.credits.StringFixed(2)))
        }
    }

    return nil
}
```

This function is called before any entry is posted to the ledger. It enforces:
1. At least 2 lines (a single posting cannot be balanced)
2. All amounts are positive (direction is conveyed by `EntryType`, not sign)
3. Per-currency balance: sum of debits equals sum of credits, checked using `decimal.Equal()` (exact comparison, no floating-point tolerance)

> **Key Insight:** Combined with TigerBeetle's engine-level balance enforcement, the system has two independent layers of protection against unbalanced entries. This is defense-in-depth: even if a bug bypasses the Go validation, TigerBeetle will reject the posting.

---

## Expanded Domain Type Catalog

Beyond the core transfer types, Settla's domain package defines types for every bounded context in the system. Each follows the same principles: decimal-only monetary math, typed enums, tenant-scoped IDs, and explicit state machines.

### PositionEvent -- Event-Sourced Treasury Audit

The `PositionEvent` type forms an append-only audit log for every treasury position mutation:

```go
// From domain/position_event.go
type PositionEventType string

const (
    PosEventCredit  PositionEventType = "CREDIT"   // Balance increased (deposit, top-up, compensation)
    PosEventDebit   PositionEventType = "DEBIT"    // Balance decreased (withdrawal, rebalance)
    PosEventReserve PositionEventType = "RESERVE"  // Funds reserved for in-flight transfer
    PosEventRelease PositionEventType = "RELEASE"  // Reserved funds released (transfer failed)
    PosEventCommit  PositionEventType = "COMMIT"   // Reserved moved to locked
    PosEventConsume PositionEventType = "CONSUME"  // Reserved+balance decreased (transfer completed)
)

type PositionEvent struct {
    ID             uuid.UUID
    PositionID     uuid.UUID
    TenantID       uuid.UUID
    EventType      PositionEventType
    Amount         decimal.Decimal
    BalanceAfter   decimal.Decimal
    LockedAfter    decimal.Decimal
    ReferenceID    uuid.UUID
    ReferenceType  string // "deposit_session", "bank_deposit", "position_transaction", "transfer", "compensation"
    IdempotencyKey string
    RecordedAt     time.Time
}
```

The `ReferenceType` field links each event back to its originating entity. At peak load (~20,000 events/sec), events are batch-inserted every 10ms via a dedicated writer goroutine. The position_events table is partitioned by `recorded_at` (monthly) with 90-day retention.

### PositionTransaction -- Tenant-Initiated Position Changes

The `PositionTransaction` type represents tenant-initiated treasury changes (top-ups, withdrawals, deposit credits, internal rebalancing), each with its own state machine:

```go
// From domain/position_transaction.go
type PositionTxType string

const (
    PositionTxTopUp             PositionTxType = "TOP_UP"
    PositionTxWithdrawal        PositionTxType = "WITHDRAWAL"
    PositionTxDepositCredit     PositionTxType = "DEPOSIT_CREDIT"
    PositionTxInternalRebalance PositionTxType = "INTERNAL_REBALANCE"
)

type PositionTxStatus string

const (
    PositionTxStatusPending    PositionTxStatus = "PENDING"
    PositionTxStatusProcessing PositionTxStatus = "PROCESSING"
    PositionTxStatusCompleted  PositionTxStatus = "COMPLETED"
    PositionTxStatusFailed     PositionTxStatus = "FAILED"
)

var ValidPositionTxTransitions = map[PositionTxStatus][]PositionTxStatus{
    PositionTxStatusPending:    {PositionTxStatusProcessing, PositionTxStatusFailed},
    PositionTxStatusProcessing: {PositionTxStatusCompleted, PositionTxStatusFailed},
    PositionTxStatusCompleted:  {}, // terminal
    PositionTxStatusFailed:     {}, // terminal
}
```

Notice the same pattern as the transfer state machine: explicit `ValidPositionTxTransitions` map, `CanTransitionTo` and `TransitionTo` methods with optimistic locking, and terminal states with empty transition slices.

### DepositSession -- Crypto Deposit Lifecycle

The `DepositSession` tracks on-chain payment detection and confirmation:

```go
// From domain/deposit.go (selected states)
const (
    DepositSessionStatusPendingPayment DepositSessionStatus = "PENDING_PAYMENT"
    DepositSessionStatusDetected       DepositSessionStatus = "DETECTED"
    DepositSessionStatusConfirmed      DepositSessionStatus = "CONFIRMED"
    DepositSessionStatusCrediting      DepositSessionStatus = "CREDITING"
    DepositSessionStatusCredited       DepositSessionStatus = "CREDITED"
    DepositSessionStatusSettling       DepositSessionStatus = "SETTLING"
    DepositSessionStatusSettled        DepositSessionStatus = "SETTLED"
    DepositSessionStatusHeld           DepositSessionStatus = "HELD"
    DepositSessionStatusExpired        DepositSessionStatus = "EXPIRED"
    DepositSessionStatusFailed         DepositSessionStatus = "FAILED"
    DepositSessionStatusCancelled      DepositSessionStatus = "CANCELLED"
)
```

The deposit session includes a `SettlementPreference` (AUTO_CONVERT, HOLD, THRESHOLD) that determines what happens after crypto is credited. The full struct includes chain, token, deposit address, expected/received amounts, and confirmation tracking.

### BankDepositSession -- Fiat Deposit Lifecycle

The `BankDepositSession` handles fiat deposits via virtual bank accounts, with additional complexity for payment mismatch handling:

```go
// From domain/bank_deposit.go (selected types)
type PaymentMismatchPolicy string

const (
    PaymentMismatchPolicyAccept PaymentMismatchPolicy = "ACCEPT"
    PaymentMismatchPolicyReject PaymentMismatchPolicy = "REJECT"
)
```

Bank deposit sessions track virtual account allocation, expected vs received amounts, and handle UNDERPAID/OVERPAID states with configurable policies.

### PaymentLink -- Merchant Collection URLs

```go
// From domain/payment_link.go
type PaymentLink struct {
    ID          uuid.UUID
    TenantID    uuid.UUID
    ShortCode   string              // URL-friendly identifier
    Description string
    Status      PaymentLinkStatus   // ACTIVE, EXPIRED, DISABLED
    SessionConfig PaymentLinkSessionConfig
    UseLimit    *int                // nil = unlimited redemptions
    UseCount    int
    ExpiresAt   *time.Time
}
```

The `CanRedeem()` method enforces status, expiration, and use limit checks. Payment links are simple CRUD entities -- all heavy lifting is delegated to the deposit engine.

### VirtualAccountPool -- Pooled Bank Accounts

Virtual accounts are pre-provisioned and dispensed to tenants on demand. The `VirtualAccountPool` struct holds bank account details (account number, sort code, IBAN) and tracks availability and session binding.

### ProviderWebhookPayload -- Normalized Inbound Callbacks

Raw provider webhooks are normalized into a standard payload format before processing. The `ProviderWebhookLog` tracks the full lifecycle: received, processed, skipped, failed, or duplicate.

### ForEachTenantBatch -- Efficient Multi-Tenant Scanning

For operations that need to iterate across all active tenants (analytics snapshots, reconciliation, capacity monitoring), the domain package provides a pagination helper:

```go
// From domain/tenant_iter.go
const DefaultTenantBatchSize int32 = 500

type TenantPageFetcher func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error)

func ForEachTenantBatch(ctx context.Context, fetch TenantPageFetcher,
    batchSize int32, fn func(ids []uuid.UUID) error) error {
    // Paginates through tenants, calls fn for each batch.
    // Stops early if ctx is cancelled or fn returns an error.
}
```

This avoids loading all tenant IDs into memory at once. At 10,000+ tenants, a naive `SELECT id FROM tenants` could return a large result set. The batch iterator processes 500 at a time, respecting context cancellation.

---

## Domain Interfaces: Expanded Contract List

Beyond the core interfaces (Ledger, TreasuryManager, Router, ProviderRegistry, EventPublisher), the domain package defines interfaces for every bounded context:

```go
// Core engine interfaces
domain.TransferStore          // Persist transfer state
domain.Ledger                 // Post and reverse ledger entries
domain.TreasuryManager        // Reserve and release treasury positions
domain.Router                 // Select optimal settlement route
domain.EventPublisher         // Publish domain events
domain.ProviderRegistry       // Look up providers and blockchain clients

// Deposit interfaces
domain.DepositEngine          // Create and manage crypto deposit sessions
domain.BankDepositEngine      // Create and manage fiat bank deposit sessions

// Payment link interface
domain.PaymentLinkService     // Generate, manage, and redeem payment links

// Position interface
domain.PositionEngine         // Position transactions (top-ups, withdrawals)

// Analytics interface
domain.AnalyticsService       // Snapshots and data exports
```

Each interface follows the same conventions: context-first parameters, tenant-scoped operations, and compile-time interface checks in the implementing package.

---

## The Outbox Entry: Bridge Between Engine and Workers

```go
// From domain/outbox.go
type OutboxEntry struct {
    ID            uuid.UUID
    AggregateType string     // "transfer", "position"
    AggregateID   uuid.UUID
    TenantID      uuid.UUID  // Multi-tenancy: every outbox entry is tenant-scoped
    EventType     string     // e.g., "transfer.created", "provider.onramp.execute"
    Payload       []byte     // JSON-encoded intent/event data
    IsIntent      bool       // true = worker must execute; false = notification only
    Published     bool
    PublishedAt   *time.Time
    RetryCount    int
    MaxRetries    int
    CreatedAt     time.Time
}
```

The `IsIntent` boolean distinguishes two kinds of entries:
- **Events** (`IsIntent=false`): Notifications about what happened. Subscribers may react but no action is required.
- **Intents** (`IsIntent=true`): Instructions for a worker to execute a side effect. A specific worker picks up the intent, executes it, and publishes a result event.

The `ValidateEventType` function catches typos in event types:

```go
// From domain/outbox.go
func ValidateEventType(eventType string) error {
    if _, ok := knownEventTypes[eventType]; !ok {
        return fmt.Errorf("settla-domain: unknown outbox event type %q", eventType)
    }
    return nil
}
```

This prevents a silent failure where a typo like `"provider.onramp.exeucte"` creates an outbox entry that no worker ever picks up, causing a transfer to hang indefinitely.

---

## Common Mistakes

### Mistake 1: Using Float64 for Money

```go
// WRONG: float64 for monetary calculation
fee := amount * 0.004  // float64 multiplication introduces rounding

// RIGHT: decimal for monetary calculation
fee := amount.Mul(decimal.NewFromString("0.004"))  // exact arithmetic
```

### Mistake 2: Importing Infrastructure in Domain

```go
// WRONG: domain/money.go importing database driver
import "database/sql"

// WRONG: domain/transfer.go importing HTTP client
import "net/http"

// RIGHT: domain/ only imports stdlib + decimal + uuid
import (
    "github.com/shopspring/decimal"
    "github.com/google/uuid"
)
```

The moment `domain/` imports an infrastructure package, every test for every module that uses domain types transitively requires that infrastructure dependency to be available.

### Mistake 3: Stringly-Typed Enums

```go
// WRONG: raw strings for status
transfer.Status = "on_ramping"  // Typo? Different casing? No compiler help.

// RIGHT: typed constants
transfer.Status = TransferStatusOnRamping  // Compiler verifies the constant exists
```

### Mistake 4: Skipping Compile-Time Interface Checks

Without `var _ domain.Ledger = (*Service)(nil)`, adding a method to the `Ledger` interface does not produce a compile error in the `ledger/` package. The error only appears when building the final binary in `cmd/`, which may be a different developer or a CI pipeline discovering the break hours later.

### Mistake 5: Using Zero Values for "Not Set"

```go
// WRONG: using zero time to mean "not completed yet"
CompletedAt time.Time  // time.Time{} is 0001-01-01, looks like a real date in logs

// RIGHT: using pointer to distinguish "not set" from "set"
CompletedAt *time.Time  // nil clearly means "not completed yet"
```

---

## Exercises

### Exercise 1: Design a Value Object

Design a `TransferAmount` value object that:
1. Wraps a `decimal.Decimal`
2. Validates that the amount is positive (returns `ErrAmountTooLow`)
3. Validates that the amount does not exceed $1,000,000 (returns `ErrAmountTooHigh`)
4. Returns a `DomainError` with the appropriate error code on validation failure
5. Provides a `String()` method that formats with 2 decimal places

### Exercise 2: Float64 Failure Lab

Write a Go program that:
1. Adds `0.10` to a `float64` variable 50,000,000 times
2. Adds `"0.10"` (string) to a `decimal.Decimal` variable 50,000,000 times using `decimal.NewFromString`
3. Prints both results with 20 decimal places
4. Calculate the difference and explain why it matters for a ledger that processes 50M entries/day

### Exercise 3: Interface Design

Design a `NotificationService` interface for the domain package that:
1. Can send transfer status updates to tenants
2. Supports multiple channels (webhook, email, SMS) through a `channel` parameter
3. Is idempotent (accepts a notification ID for deduplication)
4. Is tenant-scoped (accepts `tenantID uuid.UUID`)

Write the interface definition, then write the compile-time check for a hypothetical `webhookNotifier` implementation.

### Exercise 4: Error Code Analysis

Examine the error codes in `domain/errors.go`:
1. Categorize each error code as "client error" (caller's fault) or "system error" (internal failure)
2. For each error code, determine the appropriate HTTP status code
3. Identify which error codes should trigger automatic retry by the API client and which should not

---

## What's Next

The domain types you have just studied define the vocabulary of Settla. Every monetary amount is decimal. Every entity is tenant-scoped. Every interface is compile-time checked. In Chapter 1.4, we will see how the `Transfer` aggregate's state machine works -- the `ValidTransitions` map, the `TransitionTo` method, and how transfers flow through the happy path and failure path.

---
