# Chapter 9.2: Bank Deposits -- Fiat via Virtual Accounts

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain how virtual bank accounts let Settla accept fiat deposits on behalf of tenants
2. Describe the pool-based architecture for virtual account provisioning
3. Trace the full bank deposit session state machine from PENDING_PAYMENT to SETTLED or HELD
4. Implement mismatch policies for underpaid and overpaid bank transfers
5. Explain how inbound bank credits are routed from a banking partner webhook to the correct tenant session
6. Identify the difference between permanent and temporary virtual accounts

---

## The Bank Deposit Use Case

A fintech tenant (Lemfi, Fincra) wants to accept fiat deposits from their end customers. The customer might be a business paying an invoice, a trader funding an account, or a remittance sender depositing GBP before a cross-border transfer.

Without Settla, the tenant would need to:

- Integrate directly with banking partners (NIBSS in Nigeria, Faster Payments in the UK, SEPA in Europe)
- Manage virtual account provisioning and lifecycle
- Reconcile incoming bank credits against expected deposits
- Handle edge cases: underpayments, overpayments, late payments after expiry

Settla solves this by providing each tenant with pre-provisioned virtual bank accounts. The customer sends a standard bank transfer to the virtual account number, the banking partner notifies Settla, and the system credits the tenant's position automatically.

```
    BANK DEPOSIT FLOW (HAPPY PATH)

    Tenant's Customer                 Banking Partner              Settla
    ================                 ===============              ======

    1. Tenant creates a deposit session via API
       <--------- virtual account details (account number, sort code) ---------

    2. Customer sends bank transfer
       ---- GBP 500 ---->  Faster Payments
                           receives credit
                              |
                              |  3. Banking partner sends webhook
                              +------> Settla webhook endpoint
                                            |
                                            |  4. Normalize + route to session
                                            +------> BankDepositWorker
                                                         |
                                                         |  5. Credit tenant's ledger
                                                         |  6. Reserve treasury position
                                                         +------> CREDITED / SETTLED / HELD
```

---

## Virtual Account Architecture

### The Pooling Problem

Creating a virtual account at a banking partner is an asynchronous operation. If we created accounts on-demand when a tenant requests a deposit session, we would add seconds of latency to the API response. Worse, the banking partner API might be down, making deposit creation unreliable.

Settla solves this with a **pre-provisioned pool**. Virtual accounts are created in advance and assigned to sessions from the pool, making session creation a fast, local database operation.

### Account Types

**File:** `domain/bank_deposit.go`

```go
type VirtualAccountType string

const (
    // VirtualAccountTypePermanent is a reusable virtual account assigned to a tenant.
    VirtualAccountTypePermanent VirtualAccountType = "PERMANENT"
    // VirtualAccountTypeTemporary is a single-use virtual account for one deposit session.
    VirtualAccountTypeTemporary VirtualAccountType = "TEMPORARY"
)
```

These two types serve different use cases:

| Type | Lifetime | Use Case | After Session |
|------|----------|----------|---------------|
| PERMANENT | Indefinite | Tenant's "funding account" -- customers always send to the same number | Stays assigned |
| TEMPORARY | One session | Per-invoice or per-deposit -- unique account per transaction | Recycled to pool |

Permanent accounts are useful when a tenant wants a stable account number that their customers can save for repeat deposits. Temporary accounts are useful for one-off deposits where you want to guarantee that the account number uniquely identifies a single expected payment.

### The Virtual Account Pool

**File:** `domain/bank_deposit.go`

Each pool entry tracks the account's banking partner, routing details, and availability:

```go
type VirtualAccountPool struct {
    ID               uuid.UUID
    TenantID         uuid.UUID
    BankingPartnerID string
    AccountNumber    string
    AccountName      string
    SortCode         string
    IBAN             string
    Currency         Currency
    AccountType      VirtualAccountType
    Available        bool
    SessionID        *uuid.UUID
    CreatedAt        time.Time
    UpdatedAt        time.Time
}
```

The `Available` flag controls pool membership. When a session is created, the account is marked unavailable and linked to the session. When the session completes (or expires), the account is recycled back to available.

### Dispensing: SKIP LOCKED for Contention-Free Assignment

**File:** `db/queries/transfer/virtual_accounts.sql`

When multiple tenants create deposit sessions concurrently, they must not receive the same virtual account. Settla uses PostgreSQL's `SKIP LOCKED` to avoid contention:

```sql
-- name: DispenseVirtualAccount :one
UPDATE virtual_account_pool
SET available = false, session_id = $3, updated_at = now()
WHERE id = (
    SELECT vap.id FROM virtual_account_pool vap
    WHERE vap.tenant_id = $1 AND vap.currency = $2
      AND vap.available = true AND vap.account_type = 'TEMPORARY'
    ORDER BY vap.created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;
```

The `SKIP LOCKED` clause means: if another transaction has already locked an account row, skip it and take the next one. This converts contention into parallelism -- 100 concurrent session creations will each grab a different account without blocking.

> **Key Insight:** `SKIP LOCKED` is the secret weapon for pool-based resource dispensing under concurrency. Without it, `SELECT ... FOR UPDATE` would serialize all session creations, adding latency proportional to the number of concurrent requests.

### Recycling

When a session expires, is cancelled, or reaches a terminal state with a temporary account, the account is returned to the pool:

```sql
-- name: RecycleVirtualAccount :exec
UPDATE virtual_account_pool
SET available = true, session_id = NULL, updated_at = now()
WHERE account_number = $1;
```

Note that recycling intentionally omits `tenant_id` from the WHERE clause. The worker validates the tenant context before calling recycle -- the account number alone is sufficient for the pool update.

### The Account Index

Settla also maintains a `virtual_account_index` table for routing incoming bank credits to the correct tenant and session:

```sql
-- name: GetVirtualAccountIndexByNumber :one
SELECT * FROM virtual_account_index
WHERE account_number = $1
ORDER BY created_at DESC
LIMIT 1;
```

When a banking partner sends a credit notification with an account number, the index tells us which tenant owns that account and which active session (if any) it belongs to. This lookup is O(1) and happens before any session loading.

---

## The VirtualAccountProvisioner

**File:** `node/worker/virtual_account_provisioner.go`

The provisioner runs as a background goroutine that monitors pool levels and provisions new accounts when they drop below a low watermark:

```go
type VirtualAccountProvisioner struct {
    store         VirtualAccountProvisionerStore
    partnerReg    BankingPartnerRegistry
    tenantForEach TenantIterator
    logger        *slog.Logger
    lowWatermark  int
    pollInterval  time.Duration
}
```

Default configuration: low watermark of 10 accounts per currency, polled every 60 seconds.

The provisioner iterates over all active tenants in batches and checks each tenant's available account count per currency:

```go
func (p *VirtualAccountProvisioner) poll(ctx context.Context) {
    if p.partnerReg == nil {
        return // no banking partners configured
    }

    err := p.tenantForEach(ctx, 500, func(ids []uuid.UUID) error {
        for _, tenantID := range ids {
            availableByCurrency, err := p.store.CountAvailableVirtualAccountsByCurrency(
                ctx, tenantID,
            )
            if err != nil {
                p.logger.Error("settla-provisioner: failed to count available accounts",
                    "tenant_id", tenantID, "error", err)
                continue
            }

            for currency, avail := range availableByCurrency {
                if avail >= int64(p.lowWatermark) {
                    continue
                }

                needed := int64(p.lowWatermark) - avail
                p.logger.Info("settla-provisioner: pool below watermark",
                    "tenant_id", tenantID,
                    "currency", currency,
                    "available", avail,
                    "needed", needed,
                )

                // Provision via banking partner API and insert into pool
            }
        }
        return nil
    })
    if err != nil {
        p.logger.Error("settla-provisioner: tenant iteration failed", "error", err)
    }
}
```

The counting query groups available accounts by currency:

```sql
-- name: CountAvailableVirtualAccountsByCurrency :many
SELECT currency, count(*) AS available_count
FROM virtual_account_pool
WHERE tenant_id = $1 AND available = true
GROUP BY currency;
```

```
    PROVISIONER WATERMARK LOOP

    Every 60 seconds:

    For each tenant:
      +---------+--------+-------+-----------+
      | Currency | Avail  | Mark  | Action    |
      +---------+--------+-------+-----------+
      | GBP     |    12  |  10   | OK        |
      | EUR     |     3  |  10   | Need 7    |
      | NGN     |     0  |  10   | Need 10   |
      +---------+--------+-------+-----------+

    Provision via banking partner API:
      EUR: create 7 virtual accounts --> insert into pool
      NGN: create 10 virtual accounts --> insert into pool
```

This proactive provisioning ensures that session creation never fails because the pool is empty (barring extreme burst traffic that drains the pool faster than the 60-second poll interval).

---

## The Bank Deposit Session State Machine

**File:** `domain/bank_deposit.go`

The bank deposit session follows a state machine with 11 states. The happy path is linear, but branching paths handle mismatches, late payments, expiry, and cancellation.

```
                         BANK DEPOSIT SESSION STATE MACHINE

    +------------------+     +-----------------+     +------------+
    | PENDING_PAYMENT  |---->| PAYMENT_RECEIVED|---->| CREDITING  |
    +------------------+     +-----------------+     +------------+
         |       |                |        |               |
         |       |                |        |               |
         v       v                v        v               v
    +---------+ +----------+  +------+ +------+     +-----------+
    | EXPIRED | |CANCELLED |  |UNDER | |OVER  |     | CREDITED  |
    +---------+ +----------+  |PAID  | |PAID  |     +-----------+
         |            |       +------+ +------+      |         |
         |            |          |        |           |         |
         +-----+------+         v        v           v         v
               |             +--------+          +--------+ +------+
               |             | FAILED |          |SETTLING| | HELD |
               |             +--------+          +--------+ +------+
               v                                     |
        (late payment                                v
         restores to                            +---------+
         PAYMENT_RECEIVED)                      | SETTLED |
                                                +---------+
```

### State Definitions

```go
const (
    BankDepositSessionStatusPendingPayment  BankDepositSessionStatus = "PENDING_PAYMENT"
    BankDepositSessionStatusPaymentReceived BankDepositSessionStatus = "PAYMENT_RECEIVED"
    BankDepositSessionStatusCrediting       BankDepositSessionStatus = "CREDITING"
    BankDepositSessionStatusCredited        BankDepositSessionStatus = "CREDITED"
    BankDepositSessionStatusSettling        BankDepositSessionStatus = "SETTLING"
    BankDepositSessionStatusSettled         BankDepositSessionStatus = "SETTLED"
    BankDepositSessionStatusHeld            BankDepositSessionStatus = "HELD"
    BankDepositSessionStatusExpired         BankDepositSessionStatus = "EXPIRED"
    BankDepositSessionStatusFailed          BankDepositSessionStatus = "FAILED"
    BankDepositSessionStatusCancelled       BankDepositSessionStatus = "CANCELLED"
    BankDepositSessionStatusUnderpaid       BankDepositSessionStatus = "UNDERPAID"
    BankDepositSessionStatusOverpaid        BankDepositSessionStatus = "OVERPAID"
)
```

### Transition Map

The transition map encodes every legal state change:

```go
var ValidBankDepositTransitions = map[BankDepositSessionStatus][]BankDepositSessionStatus{
    BankDepositSessionStatusPendingPayment:  {PaymentReceived, Expired, Cancelled},
    BankDepositSessionStatusPaymentReceived: {Crediting, Underpaid, Overpaid, Failed},
    BankDepositSessionStatusCrediting:       {Credited, Failed},
    BankDepositSessionStatusCredited:        {Settling, Held},
    BankDepositSessionStatusSettling:        {Settled, Failed},
    BankDepositSessionStatusUnderpaid:       {Failed},
    BankDepositSessionStatusOverpaid:        {Failed},
    BankDepositSessionStatusExpired:         {PaymentReceived},  // late payment
    BankDepositSessionStatusCancelled:       {PaymentReceived},  // late payment
    BankDepositSessionStatusFailed:          {},                 // terminal
    BankDepositSessionStatusSettled:         {},                 // terminal
    BankDepositSessionStatusHeld:            {},                 // terminal
}
```

Three things to notice:

1. **Late payments are handled.** Even after EXPIRED or CANCELLED, the state machine allows a transition back to PAYMENT_RECEIVED. Bank transfers can arrive after the session's TTL -- the money is real, and the system must handle it.

2. **CREDITED branches two ways.** Based on the tenant's settlement preference: AUTO_CONVERT goes to SETTLING (convert fiat to stablecoin), while HOLD goes to HELD (keep the fiat balance).

3. **UNDERPAID/OVERPAID are pre-terminal.** Under the REJECT mismatch policy, they transition to FAILED. Under the ACCEPT policy, the engine skips these states entirely and proceeds to CREDITING with whatever amount was received.

### Transition Enforcement

The session aggregate enforces transitions with a simple lookup:

```go
func (s *BankDepositSession) TransitionTo(target BankDepositSessionStatus) error {
    if !s.CanTransitionTo(target) {
        return ErrInvalidTransition(string(s.Status), string(target))
    }
    s.Status = target
    s.Version++
    s.UpdatedAt = time.Now().UTC()
    return nil
}
```

Every transition increments the `Version` field. The store uses optimistic locking -- if two workers try to transition the same session concurrently, the second one gets `ErrOptimisticLock` and retries. This is the same pattern used in the core transfer engine.

### Mismatch Policies

**File:** `domain/bank_deposit.go`

```go
type PaymentMismatchPolicy string

const (
    PaymentMismatchPolicyAccept PaymentMismatchPolicy = "ACCEPT"
    PaymentMismatchPolicyReject PaymentMismatchPolicy = "REJECT"
)
```

When the customer's bank transfer amount does not match the expected amount:

| Policy | Underpaid | Overpaid |
|--------|-----------|----------|
| ACCEPT | Credit the received amount (less than expected) | Credit the received amount (more than expected) |
| REJECT | Transition to UNDERPAID, then FAILED; initiate refund | Transition to OVERPAID, then FAILED; initiate refund |

The tenant configures the default policy in `TenantBankConfig`, and individual sessions can override it at creation time.

> **Key Insight:** ACCEPT is the safer default for most fintechs. Customers occasionally send slightly different amounts (rounding, bank fees deducted). Rejecting every mismatch creates refund volume and frustrates customers. The tenant can always reconcile minor discrepancies on their side.

---

## The Bank Deposit Engine

**File:** `core/bankdeposit/engine.go`

The engine follows the same pure state machine pattern as the core transfer engine: every method validates state, computes the next state plus outbox entries, and persists both atomically. Zero network calls.

```go
type Engine struct {
    store       BankDepositStore
    tenantStore TenantStore
    logger      *slog.Logger
}
```

### CreateSession

The session creation flow validates the tenant, dispenses a virtual account, and persists the session atomically:

```go
func (e *Engine) CreateSession(ctx context.Context, tenantID uuid.UUID,
    req CreateSessionRequest) (*domain.BankDepositSession, error) {

    // a. Load tenant, verify active + bank deposits enabled
    tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-bank-deposit: create session: loading tenant %s: %w",
            tenantID, err)
    }
    if !tenant.IsActive() {
        return nil, domain.ErrTenantSuspended(tenantID.String())
    }
    if !tenant.BankConfig.BankDepositsEnabled {
        return nil, domain.ErrBankDepositsDisabled(tenantID.String())
    }

    // b. Validate currency is supported
    currencySupported := false
    for _, c := range tenant.BankConfig.BankSupportedCurrencies {
        if c == req.Currency {
            currencySupported = true
            break
        }
    }
    if !currencySupported {
        return nil, domain.ErrCurrencyNotSupported(string(req.Currency), tenantID.String())
    }

    // c. Validate amount > 0
    if !req.ExpectedAmount.IsPositive() {
        return nil, domain.ErrAmountTooLow(req.ExpectedAmount.String(), "0")
    }

    // d. Check idempotency
    if req.IdempotencyKey != "" {
        existing, err := e.store.GetSessionByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
        if err == nil && existing != nil {
            return existing, nil
        }
    }

    // e. Dispense virtual account from pool (TEMPORARY accounts only)
    poolAccount, err := e.store.DispenseVirtualAccount(ctx, tenantID, string(req.Currency))
    if err != nil {
        return nil, fmt.Errorf(
            "settla-bank-deposit: create session: dispensing virtual account: %w", err)
    }
    if poolAccount == nil {
        return nil, domain.ErrVirtualAccountPoolEmpty(string(req.Currency), tenantID.String())
    }

    // ... build session, outbox entries ...

    // l. Persist session + outbox atomically
    if err := e.store.CreateSessionWithOutbox(ctx, session, entries); err != nil {
        return nil, fmt.Errorf("settla-bank-deposit: create session: persisting: %w", err)
    }

    return session, nil
}
```

Key design decisions in this method:

1. **Tenant validation first** -- fail fast before touching the pool.
2. **Idempotency check before dispensing** -- if we already created a session for this key, return it without wasting a pool account.
3. **Pool dispense is the critical section** -- this is the only part that contends with other concurrent creations, and `SKIP LOCKED` keeps it fast.
4. **Atomic persist** -- `CreateSessionWithOutbox` writes the session row, the account index entry, and outbox entries in a single database transaction.

### HandleBankCreditReceived

This is the most complex method in the engine. It processes an incoming bank credit notification and advances the session through multiple states in sequence:

```go
func (e *Engine) HandleBankCreditReceived(ctx context.Context, tenantID, sessionID uuid.UUID,
    credit domain.IncomingBankCredit) error {

    session, err := e.store.GetSession(ctx, tenantID, sessionID)
    if err != nil {
        return fmt.Errorf("settla-bank-deposit: handle bank credit: loading session %s: %w",
            sessionID, err)
    }

    // Record the transaction (dedup by bank_reference)
    existing, err := e.store.GetBankDepositTxByRef(ctx, credit.BankReference)
    if err == nil && existing != nil {
        e.logger.Info("settla-bank-deposit: duplicate bank credit, skipping",
            "session_id", sessionID,
            "bank_reference", credit.BankReference,
        )
        return nil
    }

    // ... create BankDepositTransaction, accumulate received amount ...

    // Check for late payment (session expired or cancelled)
    isLatePayment := session.Status == domain.BankDepositSessionStatusExpired ||
        session.Status == domain.BankDepositSessionStatusCancelled

    // Update session with payer details
    session.ReceivedAmount = session.ReceivedAmount.Add(credit.Amount)
    session.PayerName = credit.PayerName
    session.PayerReference = credit.PayerReference
    session.BankReference = credit.BankReference

    if err := session.TransitionTo(domain.BankDepositSessionStatusPaymentReceived); err != nil {
        return fmt.Errorf("settla-bank-deposit: handle bank credit: %w", err)
    }

    // ... build outbox entries, persist transition ...

    // Now validate amount and route accordingly
    return e.validateAndRoutePayment(ctx, tenantID, sessionID)
}
```

The flow inside `HandleBankCreditReceived`:

```
    HANDLE BANK CREDIT RECEIVED

    1. Load session
    2. Dedup by bank_reference (idempotent)
    3. Record BankDepositTransaction + accumulate received amount
    4. Detect late payment (EXPIRED/CANCELLED -> PAYMENT_RECEIVED)
    5. Transition to PAYMENT_RECEIVED
    6. Persist transition + outbox entries
    7. Call validateAndRoutePayment()
         |
         +---> received < minAmount AND REJECT policy?
         |         YES --> UNDERPAID --> FAILED + refund intent
         |
         +---> received > maxAmount AND REJECT policy?
         |         YES --> OVERPAID --> FAILED + refund intent
         |
         +---> Amount acceptable (within bounds OR ACCEPT policy)
                   --> initiateCredit() --> CREDITING
```

### Payment Validation and Routing

The `validateAndRoutePayment` method checks the received amount against min/max bounds:

```go
func (e *Engine) validateAndRoutePayment(ctx context.Context,
    tenantID, sessionID uuid.UUID) error {

    session, err := e.store.GetSession(ctx, tenantID, sessionID)
    // ...

    received := session.ReceivedAmount

    // Check underpayment
    if received.LessThan(session.MinAmount) {
        if session.MismatchPolicy == domain.PaymentMismatchPolicyReject {
            return e.handleMismatch(ctx, session,
                domain.BankDepositSessionStatusUnderpaid, "underpaid")
        }
        // ACCEPT policy: proceed with the received amount
    }

    // Check overpayment
    if received.GreaterThan(session.MaxAmount) {
        if session.MismatchPolicy == domain.PaymentMismatchPolicyReject {
            return e.handleMismatch(ctx, session,
                domain.BankDepositSessionStatusOverpaid, "overpaid")
        }
        // ACCEPT policy: proceed with the received amount
    }

    // Amount is acceptable -- initiate credit
    return e.initiateCredit(ctx, tenantID, sessionID)
}
```

### Fee Calculation and Credit Initiation

When the amount is accepted, the engine calculates the collection fee and initiates ledger crediting:

```go
func (e *Engine) initiateCredit(ctx context.Context,
    tenantID, sessionID uuid.UUID) error {

    // Load tenant for fee calculation
    tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
    // ...

    // Calculate collection fee
    feeAmount := CalculateBankCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
    netAmount := session.ReceivedAmount.Sub(feeAmount)

    if err := session.TransitionTo(domain.BankDepositSessionStatusCrediting); err != nil {
        return fmt.Errorf("settla-bank-deposit: initiate credit: %w", err)
    }

    // Build credit intent
    creditPayload, _ := json.Marshal(domain.CreditBankDepositPayload{
        SessionID:      sessionID,
        TenantID:       tenantID,
        Currency:       session.Currency,
        GrossAmount:    session.ReceivedAmount,
        FeeAmount:      feeAmount,
        NetAmount:      netAmount,
        BankReference:  session.BankReference,
        IdempotencyKey: fmt.Sprintf("bank-deposit-credit:%s", sessionID),
    })

    entries := []domain.OutboxEntry{
        domain.MustNewOutboxIntent("bank_deposit", sessionID, tenantID,
            domain.IntentBankDepositCredit, creditPayload),
        domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID,
            domain.EventBankDepositSessionCrediting, eventPayload),
    }

    // Persist atomically
    if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
        return wrapTransitionError(err, "initiate credit", sessionID)
    }

    return nil
}
```

The `IntentBankDepositCredit` outbox entry is picked up by the BankDepositWorker, which executes the actual ledger post and treasury reservation. The engine never touches those systems directly.

### Settlement Routing After Credit

Once the tenant's ledger is credited, the engine routes based on the settlement preference:

```go
func (e *Engine) routeAfterCredit(ctx context.Context,
    tenantID, sessionID uuid.UUID) error {

    session, err := e.store.GetSession(ctx, tenantID, sessionID)
    // ...

    switch session.SettlementPref {
    case domain.SettlementPreferenceAutoConvert:
        return e.initiateSettlement(ctx, session)
    case domain.SettlementPreferenceHold, domain.SettlementPreferenceThreshold:
        return e.holdSession(ctx, session)
    default:
        return e.holdSession(ctx, session)
    }
}
```

- **AUTO_CONVERT**: Transitions to SETTLING and emits `IntentBankDepositSettle` for fiat-to-stablecoin conversion.
- **HOLD**: Transitions to HELD (terminal) -- the fiat sits in the tenant's ledger until they decide what to do.
- **THRESHOLD**: Also holds, but a separate process can trigger conversion when accumulated deposits cross a threshold.

### Late Payments and Account Recycling

A subtle but important design: when a session expires, the engine recycles the temporary virtual account back to the pool. But what if a bank transfer arrives *after* expiry? The state machine explicitly allows `EXPIRED -> PAYMENT_RECEIVED`:

```go
BankDepositSessionStatusExpired:   {BankDepositSessionStatusPaymentReceived},
BankDepositSessionStatusCancelled: {BankDepositSessionStatusPaymentReceived},
```

This ensures that real money is never lost. If a customer sends a transfer 5 minutes after the session expires, the system still processes it.

When a session expires or is cancelled with a TEMPORARY account, the engine emits a recycle intent:

```go
// Recycle virtual account for TEMPORARY accounts
if session.AccountType == domain.VirtualAccountTypeTemporary {
    recyclePayload, _ := json.Marshal(domain.RecycleVirtualAccountPayload{
        AccountNumber:    session.AccountNumber,
        BankingPartnerID: session.BankingPartnerID,
    })
    entries = append(entries, domain.MustNewOutboxIntent("bank_deposit",
        sessionID, tenantID, domain.IntentRecycleVirtualAccount, recyclePayload))
}
```

---

## The BankDepositWorker

**File:** `node/worker/bank_deposit_worker.go`

The BankDepositWorker consumes events from the `SETTLA_BANK_DEPOSITS` NATS JetStream stream and routes them to the engine.

```go
type BankDepositWorker struct {
    partition    int
    engine       BankDepositEngine
    treasury     domain.TreasuryManager
    recycler     BankDepositAccountRecycler
    inboundStore BankDepositInboundStore
    partnerReg   BankingPartnerRegistry
    subscriber   *messaging.StreamSubscriber
    inboundSub   *messaging.StreamSubscriber
    logger       *slog.Logger
    metrics      *observability.Metrics
}
```

### The Worker's Engine Interface

The worker depends on an interface, not the concrete engine:

```go
type BankDepositEngine interface {
    HandleBankCreditReceived(ctx context.Context, tenantID, sessionID uuid.UUID,
        credit domain.IncomingBankCredit) error
    HandleCreditResult(ctx context.Context, tenantID, sessionID uuid.UUID,
        result domain.IntentResult) error
    HandleSettlementResult(ctx context.Context, tenantID, sessionID uuid.UUID,
        result domain.IntentResult) error
    CreateSessionForPermanentAccount(ctx context.Context, tenantID uuid.UUID,
        accountNumber, bankingPartnerID string,
        credit domain.IncomingBankCredit) (*domain.BankDepositSession, error)
}
```

This compile-time check ensures the engine satisfies the interface:

```go
var _ BankDepositEngine = (*bankdeposit.Engine)(nil)
```

### Dual-Subscriber Pattern

Notice that the worker creates two subscribers:

```go
return &BankDepositWorker{
    // ...
    subscriber: messaging.NewStreamSubscriber(
        client,
        messaging.StreamBankDeposits,
        consumerName,             // "settla-bank-deposit-worker-{partition}"
        opts...,
    ),
    inboundSub: messaging.NewStreamSubscriber(
        client,
        messaging.StreamBankDeposits,
        "settla-bank-deposit-inbound",  // non-partitioned
        opts...,
    ),
    // ...
}
```

Why two subscribers on the same stream?

- **`subscriber`** (partitioned): Handles session lifecycle events -- credit results, settlement results, expiry. These are partitioned by tenant hash for ordering guarantees within a tenant.
- **`inboundSub`** (non-partitioned): Handles inbound bank credit notifications from banking partners. These arrive without a session context -- the worker must look up the session by account number first, which crosses partition boundaries.

```
    DUAL-SUBSCRIBER PATTERN

    SETTLA_BANK_DEPOSITS stream
    +---------------------------------------------------------+
    | settla.bank_deposit.partition.0.credit_result           |
    | settla.bank_deposit.partition.3.settlement_result       |
    | settla.bank_deposit.inbound.credit_received             |
    | settla.bank_deposit.partition.1.session_expired          |
    +---------------------------------------------------------+
         |                                    |
         v                                    v
    Partitioned subscriber              Inbound subscriber
    (per-tenant ordering)               (global, any partition)
         |                                    |
         v                                    v
    HandleCreditResult()              Route by account_number
    HandleSettlementResult()            --> find tenant + session
    ExpireSession()                     --> HandleBankCreditReceived()
```

### Inbound Credit Routing

When a bank credit arrives, the worker must figure out which tenant and session it belongs to:

```
    INBOUND CREDIT ROUTING

    Banking partner webhook: "GBP 500 arrived at account 12345678"
                                        |
                                        v
                              GetVirtualAccountIndexByNumber("12345678")
                                        |
                              +---------+---------+
                              |                   |
                         TEMPORARY             PERMANENT
                         (has session_id)      (no session_id)
                              |                   |
                              v                   v
                    Load session by ID      CreateSessionForPermanentAccount()
                              |                   |
                              v                   v
                    HandleBankCreditReceived()
```

For permanent accounts that receive a credit without an existing session, the engine creates a session automatically:

```go
func (e *Engine) CreateSessionForPermanentAccount(ctx context.Context,
    tenantID uuid.UUID, accountNumber, bankingPartnerID string,
    credit domain.IncomingBankCredit) (*domain.BankDepositSession, error) {

    // ... validate tenant ...

    idempotencyKey, _ := domain.NewIdempotencyKey(
        fmt.Sprintf("permanent:%s:%s", accountNumber, credit.BankReference))

    // Check idempotency
    existing, err := e.store.GetSessionByIdempotencyKey(ctx, tenantID, idempotencyKey)
    if err == nil && existing != nil {
        return existing, nil
    }

    // Build session using credit details as expected amount
    session := &domain.BankDepositSession{
        // ...
        AccountType:    domain.VirtualAccountTypePermanent,
        ExpectedAmount: credit.Amount,
        MinAmount:      credit.Amount,
        MaxAmount:      credit.Amount,
        SettlementPref: domain.SettlementPreferenceAutoConvert,
    }

    // Persist session + outbox atomically
    if err := e.store.CreateSessionWithOutbox(ctx, session, entries); err != nil {
        return nil, fmt.Errorf(
            "settla-bank-deposit: create permanent session: persisting: %w", err)
    }

    return session, nil
}
```

Notice the idempotency key: `permanent:{accountNumber}:{bankReference}`. The bank reference uniquely identifies the transfer at the banking partner level, so combining it with the account number prevents duplicate sessions for the same credit.

---

## Banking Partner Integration

### The Inbound Webhook Flow

Banking partners (NIBSS, Modulr, ClearBank) each have their own webhook format for notifying payment arrivals. Settla normalizes these into a common `IncomingBankCredit` type:

```go
type IncomingBankCredit struct {
    AccountNumber      string
    Amount             decimal.Decimal
    Currency           Currency
    PayerName          string
    PayerAccountNumber string
    PayerReference     string
    BankReference      string
    ReceivedAt         time.Time
}
```

The full flow from banking partner to session update:

```
    INBOUND WEBHOOK FLOW

    Banking Partner (e.g., Modulr)
         |
         | POST /webhooks/modulr  (raw JSON, Modulr's schema)
         v
    api/webhook (TypeScript Fastify)
         |
         | 1. Verify webhook signature (partner-specific HMAC)
         | 2. Normalize to ProviderWebhookPayload
         | 3. Publish to SETTLA_PROVIDER_WEBHOOKS stream
         v
    NATS JetStream: SETTLA_PROVIDER_WEBHOOKS
         |
         v
    InboundWebhookWorker (Go)
         |
         | 4. Detect event type: "bank_credit_received"
         | 5. Parse into IncomingBankCredit
         | 6. Publish to SETTLA_BANK_DEPOSITS stream
         v
    NATS JetStream: SETTLA_BANK_DEPOSITS
         |
         v
    BankDepositWorker (Go)
         |
         | 7. Look up account_number in virtual_account_index
         | 8. Route to correct tenant + session
         | 9. Call engine.HandleBankCreditReceived()
         v
    Engine updates session state machine
```

### Why Normalization Matters

Each banking partner has a different webhook payload:

| Partner | Amount field | Account field | Reference field |
|---------|-------------|---------------|-----------------|
| Modulr | `amount` (string) | `destination.accountNumber` | `reference` |
| ClearBank | `Amount` (number) | `IBAN` | `EndToEndId` |
| NIBSS | `tranAmount` (string with commas) | `creditAccountNumber` | `sessionId` |

Without normalization, the bank deposit engine would need to understand every partner's format. The webhook service handles the messy parsing once, producing a clean `IncomingBankCredit` that the engine works with.

This is the same normalizer pattern used for off-ramp providers in the transfer pipeline. Each banking partner implements a normalizer interface, and the webhook service dispatches by partner ID.

---

## The BankDepositStore Interface

**File:** `core/bankdeposit/store.go`

The store interface is the engine's only port to the database. It is deliberately wide -- covering session CRUD, virtual account pool management, transaction recording, and account index lookups:

```go
type BankDepositStore interface {
    // Session lifecycle
    CreateSessionWithOutbox(ctx context.Context, session *domain.BankDepositSession,
        entries []domain.OutboxEntry) error
    GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (
        *domain.BankDepositSession, error)
    TransitionWithOutbox(ctx context.Context, session *domain.BankDepositSession,
        entries []domain.OutboxEntry) error

    // Virtual account pool
    DispenseVirtualAccount(ctx context.Context, tenantID uuid.UUID,
        currency string) (*domain.VirtualAccountPool, error)
    RecycleVirtualAccount(ctx context.Context, accountNumber string) error

    // Bank deposit transactions (dedup by bank_reference)
    CreateBankDepositTx(ctx context.Context, tx *domain.BankDepositTransaction) error
    GetBankDepositTxByRef(ctx context.Context, bankReference string) (
        *domain.BankDepositTransaction, error)
    RecordBankDepositTx(ctx context.Context, tx *domain.BankDepositTransaction,
        tenantID, sessionID uuid.UUID, amount decimal.Decimal) error

    // Expiry
    GetExpiredPendingSessions(ctx context.Context, limit int) (
        []domain.BankDepositSession, error)

    // Account index (for routing inbound credits)
    GetVirtualAccountIndexByNumber(ctx context.Context, accountNumber string) (
        *VirtualAccountIndex, error)
}
```

The `VirtualAccountIndex` type maps an account number to its owning tenant and optional session:

```go
type VirtualAccountIndex struct {
    AccountNumber string
    TenantID      uuid.UUID
    SessionID     *uuid.UUID
    AccountType   domain.VirtualAccountType
}
```

This is the critical lookup structure for inbound credit routing. When a banking partner says "money arrived at account 12345678", the index tells us: this is tenant Lemfi's temporary account, linked to session `abc-123`.

> **Key Insight:** The account index is a separate table from the pool. The pool manages availability (which accounts can be dispensed). The index manages routing (which tenant/session an account belongs to). Separating these concerns means routing lookups are not blocked by pool dispense transactions.

---

## Common Mistakes

### 1. Creating virtual accounts on-demand

On-demand creation makes session creation dependent on the banking partner's API availability and latency. Pre-provisioned pools with `SKIP LOCKED` dispensing give sub-millisecond session creation regardless of partner status.

### 2. Using SELECT FOR UPDATE without SKIP LOCKED

Under concurrent session creation, `SELECT FOR UPDATE` serializes all pool dispense operations. `SKIP LOCKED` allows parallel dispensing by skipping locked rows and picking the next available account.

### 3. Ignoring late payments

Bank transfers are not instant. A customer might initiate a Faster Payment at 23:59, and it arrives at 00:02 -- after the 1-hour TTL. The state machine must handle EXPIRED -> PAYMENT_RECEIVED transitions, or real money gets orphaned.

### 4. Trusting the webhook amount without validation

Banking partners send webhook notifications, but webhooks can be replayed, forged, or malformed. The engine deduplicates by `bank_reference` and validates amounts against min/max bounds. The mismatch policy is the second line of defense after signature verification.

### 5. Recycling permanent accounts

Only TEMPORARY accounts should be recycled to the pool. A permanent account is assigned to a tenant indefinitely -- recycling it would break the tenant's stable account number.

### 6. Skipping fee calculation on accepted mismatches

When the ACCEPT policy credits a mismatched amount, the fee must be calculated on the *received* amount, not the *expected* amount. The engine correctly uses `session.ReceivedAmount` for fee calculation:

```go
feeAmount := CalculateBankCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
netAmount := session.ReceivedAmount.Sub(feeAmount)
```

---

## Putting It All Together

Here is the complete lifecycle of a bank deposit, from API call to final state:

```
    COMPLETE BANK DEPOSIT LIFECYCLE

    Tenant API Call: POST /v1/bank-deposits
         |
         v
    Gateway --> gRPC --> Engine.CreateSession()
         |  a. Validate tenant (active, bank_deposits_enabled, currency supported)
         |  b. Check idempotency
         |  c. DispenseVirtualAccount (SKIP LOCKED)
         |  d. Build session (PENDING_PAYMENT) + outbox entries
         |  e. CreateSessionWithOutbox (atomic)
         v
    Response: { session_id, account_number, sort_code, expected_amount }
         |
    [Customer sends bank transfer]
         |
    Banking partner webhook --> api/webhook --> NATS
         |
         v
    BankDepositWorker: inbound credit
         |  a. GetVirtualAccountIndexByNumber
         |  b. Engine.HandleBankCreditReceived()
         |     - Dedup by bank_reference
         |     - Record BankDepositTransaction
         |     - PENDING_PAYMENT --> PAYMENT_RECEIVED
         |     - Validate amount (min/max + mismatch policy)
         |     - PAYMENT_RECEIVED --> CREDITING
         |     - Emit IntentBankDepositCredit
         v
    BankDepositWorker: credit intent
         |  a. Post ledger entries (debit fiat pool, credit tenant)
         |  b. Reserve treasury position
         |  c. Engine.HandleCreditResult(success)
         |     - CREDITING --> CREDITED
         |     - Route by SettlementPref
         v
    [If AUTO_CONVERT]                [If HOLD]
         |                                |
    CREDITED --> SETTLING            CREDITED --> HELD (terminal)
         |
    BankDepositWorker: settle intent
         |  a. Execute fiat-to-stablecoin conversion
         |  b. Engine.HandleSettlementResult(success)
         |     - SETTLING --> SETTLED (terminal)
         v
    DONE
```

---

## Exercises

### Exercise 1: Trace a Late Payment

A tenant creates a bank deposit session for GBP 1,000 with a 30-minute TTL. The session expires. Five minutes later, the customer's bank transfer of GBP 1,000 arrives. Trace the exact sequence of state transitions, including which methods are called and which outbox entries are emitted.

**Hint:** Start from `ExpireSession`, then follow `HandleBankCreditReceived`. Pay attention to the recycled virtual account -- what happens to it?

### Exercise 2: Design a Tolerance Window

The current min/max amount system requires explicit values at session creation. Design an extension that lets tenants configure a percentage tolerance (e.g., "accept payments within 5% of expected amount"). Where would you add this logic? What changes to `CreateSessionRequest` and `BankDepositSession` are needed?

### Exercise 3: Multiple Credits Per Session

Some banking systems split a single payment into multiple credits (e.g., two transfers of GBP 250 arriving for an expected GBP 500 session). The engine already accumulates `ReceivedAmount` across multiple `BankDepositTransaction` records. But the state machine transitions to PAYMENT_RECEIVED on the first credit. Design a modification that waits for accumulated credits to reach `MinAmount` before transitioning. What new state would you add? How does this interact with the session TTL?

### Exercise 4: Pool Capacity Planning

A tenant processes 500 bank deposit sessions per day. Each session has a 1-hour TTL. The provisioner polls every 60 seconds with a low watermark of 10.

1. At peak, how many temporary accounts are simultaneously in use?
2. What happens if all 10 pool accounts are dispensed before the provisioner runs?
3. How would you calculate the optimal low watermark for this tenant?

### Exercise 5: Implement Overpayment Partial Refund

Under the ACCEPT policy, the engine currently credits the full received amount (even if overpaid). Design an alternative "ACCEPT_AND_REFUND_EXCESS" policy that credits the expected amount and refunds the difference. What new outbox intent would you emit? How would you handle the case where the refund fails?

---

## Summary

Bank deposits in Settla follow the same architectural principles as every other module:

- **Pure state machine engine** -- zero network calls, all side effects via outbox
- **Pre-provisioned resource pool** -- virtual accounts dispensed via SKIP LOCKED, recycled on session completion
- **Atomic transitions** -- state change + outbox entries in a single database transaction with optimistic locking
- **Normalization layer** -- banking partner webhook diversity hidden behind a common `IncomingBankCredit` type
- **Graceful edge cases** -- late payments, mismatched amounts, and expired sessions all have defined paths through the state machine

The bank deposit module demonstrates how Settla's outbox-driven architecture scales to new deposit channels without changing the fundamental patterns. Whether money arrives via blockchain (Chapter 9.1) or bank transfer (this chapter), the engine writes state + outbox atomically, and workers execute the side effects.

---

**Next:** Chapter 9.3 -- Payment Links
