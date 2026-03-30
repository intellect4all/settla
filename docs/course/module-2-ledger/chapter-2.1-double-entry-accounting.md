# Chapter 2.1: Double-Entry Accounting for Engineers

**Estimated reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the accounting equation and why it matters for settlement systems
2. Read and construct T-accounts for any financial transaction
3. Distinguish debits from credits without memorizing arbitrary rules
4. Walk through Settla's `ValidateEntries()` code and explain every check
5. Navigate Settla's chart of accounts and decode any account code
6. Understand why the ledger is the single most critical component in a settlement system
7. Explain how position events form an event-sourced audit trail for treasury mutations

---

## Why Engineers Need Accounting

Most engineers treat accounting as "someone else's problem." In a settlement system processing 50 million transactions per day, accounting IS the engineering problem. Every transfer of value between fintechs must be recorded with mathematical precision. Miss a penny at scale and you have created a million-dollar discrepancy by month end.

Double-entry bookkeeping was invented in 15th-century Venice. It has survived for 500 years because it contains an elegant invariant: **every transaction is recorded in two places, and the books must always balance.** This is fundamentally the same idea as a checksum -- a built-in error detection mechanism for financial data.

---

## The Accounting Equation

Every financial system rests on one equation:

```
Assets = Liabilities + Equity
```

In Settla's context, think of it this way:

- **Assets**: What the system holds (bank balances, crypto wallets, receivables)
- **Liabilities**: What the system owes (customer funds pending delivery, settlement payables)
- **Equity**: Revenue minus expenses (fees earned, operational costs)

Expanding equity into its components:

```
Assets = Liabilities + Revenue - Expenses
```

Rearranging:

```
Assets + Expenses = Liabilities + Revenue
```

This rearranged form reveals the debit/credit rule: **the left side increases with debits, the right side increases with credits.**

```
                    THE ACCOUNTING EQUATION
    +-------------------------------------------------+
    |                                                   |
    |  Assets + Expenses = Liabilities + Revenue        |
    |  ^^^^^^^^^^^^^^^^    ^^^^^^^^^^^^^^^^^^^^^        |
    |  Increase w/ DEBIT   Increase w/ CREDIT           |
    |  Decrease w/ CREDIT  Decrease w/ DEBIT            |
    |                                                   |
    +-------------------------------------------------+
```

> **Key Insight**: Debits and credits are not "good" and "bad." They are simply the two sides of every transaction. A debit increases an asset account and decreases a liability account. Once you internalize the equation above, you never need to memorize debit/credit rules again.

---

## T-Accounts: The Visual Model

A T-account is a visual representation of a single ledger account. The left side records debits; the right side records credits.

```
         tenant:lemfi:assets:bank:gbp:clearing
         =======================================
         DEBIT (DR)        |  CREDIT (CR)
         ------------------|--------------------
         GBP 1,000.00      |
                            |  GBP 200.00
         GBP 500.00        |
         ------------------|--------------------
         Total DR: 1,500   |  Total CR: 200
         =======================================
         Balance: GBP 1,300.00 (DR)
```

The **normal balance** is the side that increases the account:

| Account Type | Normal Balance | Increases With | Decreases With |
|-------------|---------------|----------------|----------------|
| Asset       | Debit         | Debit          | Credit         |
| Expense     | Debit         | Debit          | Credit         |
| Liability   | Credit        | Credit         | Debit          |
| Revenue     | Credit        | Credit         | Debit          |

In Settla, this is codified in `domain/account.go`:

```go
// NormalBalanceFor returns the normal balance direction for a given account type.
// Assets and expenses have debit normal balances; liabilities and revenue have credit.
func NormalBalanceFor(at AccountType) NormalBalance {
    switch at {
    case AccountTypeAsset, AccountTypeExpense:
        return NormalBalanceDebit
    case AccountTypeLiability, AccountTypeRevenue:
        return NormalBalanceCredit
    default:
        return NormalBalanceDebit
    }
}
```

---

## A Settlement Transaction in T-Accounts

Let's trace a GBP-to-NGN settlement through the ledger. Lemfi sends GBP 1,000, which is converted to USDT on-chain, then off-ramped to NGN.

### Step 1: Customer funds received (GBP 1,000)

```
  tenant:lemfi:assets:bank:gbp:clearing     tenant:lemfi:liabilities:customer:pending
  =========================================  =========================================
  DR              |  CR                      DR              |  CR
  ----------------|------------------------  ----------------|------------------------
  GBP 1,000.00    |                                          |  GBP 1,000.00
```

Reading this: "The clearing bank account increased (we received money), and we now owe the customer GBP 1,000."

### Step 2: On-ramp -- GBP converted to USDT (simplified)

```
  assets:crypto:usdt:tron                   tenant:lemfi:assets:bank:gbp:clearing
  =========================================  =========================================
  DR              |  CR                      DR              |  CR
  ----------------|------------------------  ----------------|------------------------
  USDT 960.00     |                                          |  GBP 1,000.00

  expenses:provider:onramp
  =========================================
  DR              |  CR
  ----------------|------------------------
  USDT 40.00      |
```

Three lines, one entry. The total debits (960 + 40 = 1,000) equal the total credits (1,000). The books balance.

### Step 3: Fee recognition

```
  tenant:lemfi:liabilities:customer:pending  tenant:lemfi:revenue:fees:settlement
  =========================================  =========================================
  DR              |  CR                      DR              |  CR
  ----------------|------------------------  ----------------|------------------------
  GBP 4.00        |                                          |  GBP 4.00
```

Lemfi's fee schedule charges 40 basis points. The liability decreases (we owe them less because they paid a fee) and revenue increases.

---

## The Balance Invariant

Here is the core rule: **for every journal entry, the sum of all debit amounts MUST equal the sum of all credit amounts, per currency.** Settla enforces this with `ValidateEntries()` in `domain/ledger.go`:

```go
// ValidateEntries is a pure function that validates a set of entry lines.
// It checks:
//   - At least 2 lines exist
//   - All amounts are positive
//   - Debits equal credits per currency
//   - No duplicate line IDs
func ValidateEntries(lines []EntryLine) error {
    if len(lines) < 2 {
        return ErrLedgerImbalance(fmt.Sprintf("need at least 2 lines, got %d", len(lines)))
    }

    // Check all amounts are positive and collect IDs for duplicate check
    seenIDs := make(map[uuid.UUID]bool, len(lines))
    for _, line := range lines {
        if !line.Amount.IsPositive() {
            return ErrLedgerImbalance(fmt.Sprintf("line amount must be positive, got %s", line.Amount))
        }
        if line.ID != uuid.Nil {
            if seenIDs[line.ID] {
                return ErrLedgerImbalance(fmt.Sprintf("duplicate line ID %s", line.ID))
            }
            seenIDs[line.ID] = true
        }
    }

    // Check debits == credits per currency
    type balance struct {
        debits  decimal.Decimal
        credits decimal.Decimal
    }
    byCurrency := make(map[Currency]*balance)
    for _, line := range lines {
        b, ok := byCurrency[line.Currency]
        if !ok {
            b = &balance{debits: decimal.Zero, credits: decimal.Zero}
            byCurrency[line.Currency] = b
        }
        switch line.EntryType {
        case EntryTypeDebit:
            b.debits = b.debits.Add(line.Amount)
        case EntryTypeCredit:
            b.credits = b.credits.Add(line.Amount)
        }
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

Let's walk through this function line by line:

**Check 1: Minimum two lines.** A single entry line cannot balance. You always need at least one debit and one credit.

**Check 2: Positive amounts.** Amounts are ALWAYS positive. The direction (debit vs credit) is determined by `EntryType`, not by the sign of the amount. This eliminates an entire category of sign-error bugs.

**Check 3: No duplicate line IDs.** Each line in a journal entry must be uniquely identifiable. Duplicates would corrupt the audit trail.

**Check 4: Balance per currency.** This is the heart of the invariant. For each currency present in the entry, the sum of debit amounts must exactly equal the sum of credit amounts. This is checked using `shopspring/decimal` -- never floating point.

> **Key Insight**: `ValidateEntries()` is a pure function. It takes data in, returns a result, and has zero side effects. This means it is trivially testable and can be called from anywhere without worrying about state. The engine calls it before any write reaches TigerBeetle.

---

## Why decimal.Decimal, Never float64

Consider this Go code:

```go
a := 0.1 + 0.2  // = 0.30000000000000004
```

At 50 million transactions per day, a rounding error of 0.00000000000000004 per transaction compounds to 2.0 per day. Over a year, that is 730 units of currency that appear or vanish from nowhere. In a regulated financial system, this is unacceptable.

Settla uses `shopspring/decimal` for ALL monetary math in Go and `decimal.js` in TypeScript. This is enforced as a critical invariant:

```
Critical Invariant #1: Decimal-only monetary math
shopspring/decimal (Go) / decimal.js (TS) for ALL monetary amounts.
Never use float/float64 for money.
```

The `Posting` type makes this explicit:

```go
type Posting struct {
    AccountCode string
    EntryType   EntryType
    Amount      decimal.Decimal // Always positive; direction is determined by EntryType.
    Currency    Currency
    Description string
}
```

---

## Settla's Chart of Accounts

A chart of accounts is the hierarchical tree of all accounts in the ledger. Settla uses a colon-delimited naming convention with two patterns:

### Tenant-Scoped Accounts

Format: `tenant:{slug}:{category}:{subcategory}:{currency}:{purpose}`

```
tenant:lemfi:assets:bank:gbp:clearing
  ^      ^      ^     ^    ^     ^
  |      |      |     |    |     +-- Purpose: what this account is for
  |      |      |     |    +-------- Currency: GBP, NGN, USD, USDT
  |      |      |     +------------- Subcategory: bank, crypto, customer
  |      |      +-------------------- Category: assets, liabilities, revenue, expenses
  |      +--------------------------- Tenant slug: lemfi, fincra, paystack
  +---------------------------------- Namespace: always "tenant" for tenant accounts
```

Examples from the codebase:
```
tenant:lemfi:assets:bank:gbp:clearing        -- Lemfi's GBP clearing account
tenant:lemfi:liabilities:customer:pending     -- Funds owed to Lemfi's customers
tenant:lemfi:revenue:fees:settlement          -- Settlement fee revenue from Lemfi
tenant:fincra:assets:bank:ngn:clearing        -- Fincra's NGN clearing account
```

### System Accounts

Format: `{category}:{subcategory}:{detail}`

System accounts have no tenant prefix and are shared across all tenants:

```
assets:crypto:usdt:tron          -- System USDT wallet on Tron
expenses:provider:onramp         -- On-ramp provider fees (system-wide)
expenses:network:tron:gas        -- Tron network gas costs
```

The helper function in `domain/account.go`:

```go
// TenantAccountCode builds a tenant-scoped account code.
// Format: tenant:{slug}:{path}
func TenantAccountCode(tenantSlug, path string) string {
    return fmt.Sprintf("tenant:%s:%s", tenantSlug, path)
}

// IsSystemAccount returns true if the account code does NOT have a "tenant:" prefix.
func IsSystemAccount(code string) bool {
    return !strings.HasPrefix(code, "tenant:")
}
```

### The Account Hierarchy for a Single Settlement

```
                        ROOT
                         |
            +------------+------------+
            |            |            |
          ASSETS    LIABILITIES    REVENUE
            |            |            |
    +-------+-------+    |        +---+---+---+
    |       |       |    |        |       |   |
  bank   crypto  recv   cust    fees   spread |
    |       |            |        |           |
  +--+--+ +-+-+       pending  +-+---+      ...
  |     | |   |                |     |
 gbp  ngn usdt balance     settle  deposit
  |       |
clear   tron
```

Note the `crypto:usdt:balance` accounts under tenant scope -- these hold crypto balances from deposit flows. When a tenant receives a crypto deposit, the `assets:crypto:usdt:balance` account is debited (increased), and when they convert to fiat, it is credited (decreased). The `revenue:fees:deposit` account captures collection fees charged on deposits.

---

## The Domain Model

The ledger domain model consists of three key types. Here is how they relate:

```
    JournalEntry (header)
    +-- ID, TenantID, IdempotencyKey
    +-- PostedAt, EffectiveDate
    +-- Description, ReferenceType, ReferenceID
    +-- ReversedBy, ReversalOf
    |
    +-- Lines []EntryLine
        |
        +-- EntryLine
            +-- ID, AccountID
            +-- Posting (embedded value object)
                +-- AccountCode: "tenant:lemfi:assets:bank:gbp:clearing"
                +-- EntryType:   DEBIT | CREDIT
                +-- Amount:      decimal.Decimal (always positive)
                +-- Currency:    GBP | NGN | USD | USDT | ...
                +-- Description: "Settlement clearing"
```

A `JournalEntry` is the envelope. It carries metadata about why this entry exists (reference to a transfer, idempotency key, timestamps). The `Lines` are the actual postings -- each one debits or credits a specific account.

The `Posting` is a value object embedded in `EntryLine`. Value objects are defined by their attributes, not by identity. Two postings with the same account code, type, amount, and currency are semantically identical.

---

## How the Outbox Carries Ledger Instructions

The settlement engine never calls the ledger directly. Instead, it writes a `LedgerPostPayload` to the outbox, which the LedgerWorker picks up and executes:

```go
// LedgerPostPayload is the payload for IntentLedgerPost.
type LedgerPostPayload struct {
    TransferID     uuid.UUID         `json:"transfer_id"`
    TenantID       uuid.UUID         `json:"tenant_id"`
    IdempotencyKey string            `json:"idempotency_key"`
    Description    string            `json:"description"`
    ReferenceType  string            `json:"reference_type"`
    Lines          []LedgerLineEntry `json:"lines"`
}

// LedgerLineEntry is a simplified posting line for serialization in outbox payloads.
type LedgerLineEntry struct {
    AccountCode string          `json:"account_code"`
    EntryType   string          `json:"entry_type"` // "DEBIT" or "CREDIT"
    Amount      decimal.Decimal `json:"amount"`
    Currency    string          `json:"currency"`
    Description string          `json:"description"`
}
```

The flow:

```
Engine.CreateTransfer()
    |
    +---> writes OutboxEntry { EventType: "ledger.post", Payload: LedgerPostPayload{...} }
    |     (atomically with state transition, same DB transaction)
    |
    V
Outbox Relay polls Transfer DB every 20ms
    |
    V
Publishes to NATS JetStream: SETTLA_LEDGER stream
    |
    V
LedgerWorker picks up the intent
    |
    +---> Converts LedgerLineEntry[] -> EntryLine[] (domain types)
    +---> Calls ledger.Service.PostEntries(JournalEntry)
    +---> ValidateEntries() runs first
    +---> If balanced, writes to TigerBeetle
    +---> Publishes result event back to engine
```

> **Key Insight**: The engine writes ONLY to the outbox. It never imports the ledger package. This is Critical Invariant #11. The LedgerWorker is the only code path that calls `PostEntries()`.

---

## Deposit Flows Generate Ledger Entries

Deposits -- both crypto and fiat -- generate their own ledger entries when crediting tenant positions. When a crypto deposit is confirmed on-chain, the deposit engine emits an `IntentCreditDeposit` outbox entry carrying a `CreditDepositPayload`:

```go
// CreditDepositPayload is the payload for IntentCreditDeposit.
type CreditDepositPayload struct {
    SessionID      uuid.UUID       `json:"session_id"`
    TenantID       uuid.UUID       `json:"tenant_id"`
    Chain          CryptoChain     `json:"chain"`
    Token          string          `json:"token"`
    GrossAmount    decimal.Decimal `json:"gross_amount"`
    FeeAmount      decimal.Decimal `json:"fee_amount"`
    NetAmount      decimal.Decimal `json:"net_amount"`
    TxHash         string          `json:"tx_hash"`
    IdempotencyKey IdempotencyKey  `json:"idempotency_key"`
}
```

The DepositWorker picks this up and posts a journal entry that credits the tenant's position while recognizing the collection fee:

```
  Crypto Deposit Credit (USDT 1,000 received, 20 bps fee):

  assets:crypto:usdt:tron                     tenant:lemfi:revenue:fees:deposit
  =========================================    =========================================
  DR              |  CR                        DR              |  CR
  ----------------|------------------------    ----------------|------------------------
                  |  USDT 1,000.00                              |  USDT 2.00

  tenant:lemfi:assets:crypto:usdt:balance
  =========================================
  DR              |  CR
  ----------------|------------------------
  USDT 998.00     |
```

Three lines, one entry. The gross amount (1,000) splits into net (998) credited to the tenant and fee (2) recognized as revenue. The same pattern applies for bank (fiat) deposits -- the `BankDepositWorker` generates equivalent entries when a virtual account receives funds.

---

## Position Event Accounting

Treasury position mutations are tracked through an event-sourced audit trail. Every time a position changes -- whether from a deposit credit, transfer reservation, or withdrawal -- an immutable `PositionEvent` is recorded:

```go
// PositionEventType identifies the kind of position mutation recorded in the
// event-sourced position ledger.
type PositionEventType string

const (
    PosEventCredit  PositionEventType = "CREDIT"  // Balance increased (deposit, top-up, compensation)
    PosEventDebit   PositionEventType = "DEBIT"   // Balance decreased (withdrawal, rebalance)
    PosEventReserve PositionEventType = "RESERVE"  // Funds reserved for in-flight transfer
    PosEventRelease PositionEventType = "RELEASE"  // Reserved funds released (transfer failed)
    PosEventCommit  PositionEventType = "COMMIT"   // Reserved moved to locked
    PosEventConsume PositionEventType = "CONSUME"  // Reserved+balance decreased (transfer completed)
)
```

These six event types capture every possible position state change:

```
    Position Event Flow (single transfer lifecycle):

    t=0   CREDIT   +1000 USDT   (deposit credited to position)
    t=1   RESERVE   -200 USDT   (transfer reserves from available)
    t=2   COMMIT    -200 USDT   (reservation moved to locked)
    t=3   CONSUME   -200 USDT   (transfer completed, locked reduced)

    Position Event Flow (failed transfer):

    t=0   RESERVE   -200 USDT   (transfer reserves from available)
    t=1   RELEASE   +200 USDT   (transfer failed, reservation returned)
```

Each event captures a complete snapshot of the position state after the mutation:

```go
type PositionEvent struct {
    ID             uuid.UUID
    PositionID     uuid.UUID
    TenantID       uuid.UUID
    EventType      PositionEventType
    Amount         decimal.Decimal
    BalanceAfter   decimal.Decimal   // position balance after this event
    LockedAfter    decimal.Decimal   // position locked after this event
    ReferenceID    uuid.UUID         // what caused this event
    ReferenceType  string            // "deposit_session", "bank_deposit", "transfer", etc.
    IdempotencyKey string
    RecordedAt     time.Time
}
```

The `BalanceAfter` and `LockedAfter` fields make each event self-contained -- you can reconstruct the position state at any point in time by reading a single event, without replaying the entire log. This is critical for:

1. **Crash recovery**: After a restart, the treasury manager replays events since the last snapshot to rebuild in-memory state
2. **Compliance audit**: Regulators can see the exact position state at any moment
3. **Tenant-facing history**: The portal displays position changes with full context (reference type, amounts, timestamps)

Events are batch-inserted every 10ms by a dedicated writer goroutine (see `treasury/event_writer.go`), handling ~20,000 events/sec at peak load without blocking the hot reservation path.

> **Key Insight**: Position events are NOT the same as ledger journal entries. Journal entries track the double-entry accounting (debits and credits across accounts). Position events track the treasury state machine (how much a tenant has available, reserved, locked). Both are immutable, both are append-only, but they serve different purposes and live in different databases (Treasury DB vs Ledger DB).

---

## The Database Schema

The Postgres read model for the ledger consists of four tables:

```sql
-- ~100K rows, non-partitioned
CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,                       -- NULL for system accounts
    code            TEXT NOT NULL UNIQUE,        -- "tenant:lemfi:assets:bank:gbp:clearing"
    type            TEXT NOT NULL CHECK (type IN ('ASSET','LIABILITY','REVENUE','EXPENSE')),
    currency        TEXT NOT NULL,
    normal_balance  TEXT NOT NULL CHECK (normal_balance IN ('DEBIT', 'CREDIT')),
    parent_id       UUID REFERENCES accounts(id),
    is_active       BOOLEAN NOT NULL DEFAULT true,
    metadata        JSONB DEFAULT '{}'
);

-- ~50M inserts/day, partitioned by posted_at (monthly)
CREATE TABLE journal_entries (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID,
    idempotency_key TEXT,
    posted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    reference_type  TEXT,
    reference_id    UUID,
    reversed_by     UUID,
    reversal_of     UUID,
    PRIMARY KEY (id, posted_at)
) PARTITION BY RANGE (posted_at);

-- ~200-250M inserts/day, partitioned by created_at (monthly/weekly)
CREATE TABLE entry_lines (
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    journal_entry_id UUID NOT NULL,
    account_id       UUID NOT NULL,
    entry_type       TEXT NOT NULL CHECK (entry_type IN ('DEBIT', 'CREDIT')),
    amount           NUMERIC(28, 8) NOT NULL CHECK (amount > 0),
    currency         TEXT NOT NULL,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- ~100K rows, non-partitioned (one row per account)
CREATE TABLE balance_snapshots (
    account_id      UUID NOT NULL REFERENCES accounts(id),
    balance         NUMERIC(28, 8) NOT NULL DEFAULT 0,
    last_entry_id   UUID,
    version         BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id)
);
```

Notice: `amount NUMERIC(28, 8) NOT NULL CHECK (amount > 0)`. The database itself enforces that amounts are positive and uses fixed-precision decimal storage. This is defense in depth -- even if application code has a bug, the database will reject invalid data.

---

## Common Mistakes

**Mistake 1: Using signed amounts instead of entry types.**
Beginners try `amount = -1000` for debits and `amount = +1000` for credits. This is fragile. Settla always stores positive amounts and uses `EntryType` (DEBIT/CREDIT) to indicate direction. The database enforces `CHECK (amount > 0)`.

**Mistake 2: Confusing debits with "money leaving."**
A debit to an asset account means money ARRIVED. A debit to a liability account means the obligation DECREASED. The word "debit" has no inherent "direction" -- it only has meaning relative to the account type.

**Mistake 3: Posting entries that cross currencies without balancing per currency.**
`ValidateEntries()` checks balance per currency. You cannot debit GBP 1,000 and credit NGN 500,000 in the same journal entry, even if the exchange rate makes them "equivalent." Each currency must independently balance. Cross-currency movements require separate entries or intermediate stablecoin accounts.

**Mistake 4: Importing the ledger package from the engine.**
The engine writes outbox entries. The LedgerWorker calls PostEntries. Breaking this boundary violates Critical Invariant #11 and creates a dual-write risk.

**Mistake 5: Forgetting the idempotency key.**
Every journal entry should have an idempotency key. Without it, NATS redelivery could cause double-posting. The key format `ledger:{transfer_id}:{phase}` ensures each phase of a transfer posts exactly once.

---

## Exercises

1. **T-Account Exercise**: Draw the T-accounts for a full GBP-to-NGN settlement through Settla. Include: customer fund receipt, on-ramp to USDT, blockchain transfer, off-ramp to NGN, fee recognition. How many journal entries are needed? How many total entry lines?

2. **Imbalance Detection**: Given this entry, what error will `ValidateEntries()` return?
   ```
   DR  tenant:lemfi:assets:bank:gbp:clearing     GBP  1,000.00
   CR  tenant:lemfi:liabilities:customer:pending  GBP    998.00
   CR  tenant:lemfi:revenue:fees:settlement       GBP      1.50
   ```

3. **Account Code Design**: A new tenant "Chipper" joins. They need accounts for NGN clearing, USD clearing, customer pending liabilities, and settlement fee revenue. Write the four account codes.

4. **Code Reading**: Trace through `ValidateEntries()` with the `multiLineEntry()` test helper from `ledger_test.go`. Does it pass? Why or why not? What are the debit and credit totals per currency?

5. **Deposit Ledger Entry**: A tenant receives a USDT 5,000 crypto deposit on Tron with a 25 bps collection fee. Draw the T-accounts for the credit journal entry. Then draw the position events that the treasury manager would emit (CREDIT to tenant position). How do the ledger entry and position event relate -- what does each capture that the other does not?

---

## What's Next

Now that you understand double-entry accounting, the next chapter explores WHY Settla cannot use Postgres alone for the write path. At 15,000-25,000 ledger writes per second, we will see exactly where Postgres breaks down and why TigerBeetle was chosen as the write authority.

**Next: [Chapter 2.2: Why TigerBeetle](./chapter-2.2-why-tigerbeetle.md)**
