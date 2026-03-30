# Chapter 2.5: Ledger Reversals

**Estimated reading time: 22 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why ledger entries are immutable and why DELETE is forbidden
2. Describe the reversal pattern: a new entry that mirrors the original with swapped debits/credits
3. Walk through `ReverseEntry()` code including how it loads the original entry and constructs the reversal
4. Understand how the outbox carries reversal intents via `IntentLedgerReverse`
5. Trace the linking between original and reversal entries via `ReversalOf` and `ReversedBy`
6. Handle edge cases: partial reversals, double-reversal prevention, cross-module reversal flows

---

## The Immutability Principle

A financial ledger is an append-only data structure. Once an entry is recorded, it NEVER changes. This is not a performance optimization -- it is a regulatory and audit requirement.

```
    ┌──────────────────────────────────────────┐
    │              IMMUTABLE JOURNAL             │
    │                                            │
    │  Entry 1:  DR Assets      GBP  1,000.00   │
    │            CR Liabilities GBP  1,000.00   │
    │                                            │
    │  Entry 2:  DR Crypto      USDT   960.00   │
    │            DR Expenses    USDT    40.00   │
    │            CR Assets      USDT 1,000.00   │
    │                                            │
    │  Entry 3:  CR Assets      GBP  1,000.00   │  <-- REVERSAL of Entry 1
    │            DR Liabilities GBP  1,000.00   │
    │                                            │
    │  ╳ DELETE Entry 1  <-- FORBIDDEN           │
    │  ╳ UPDATE Entry 1  <-- FORBIDDEN           │
    │                                            │
    └──────────────────────────────────────────┘
```

Why immutability?

1. **Audit trail**: Regulators need to see what happened AND what was undone. Deleting an entry destroys evidence.
2. **Reconciliation**: If entries could be modified, point-in-time balance reconstruction becomes impossible.
3. **TigerBeetle enforcement**: TigerBeetle physically cannot delete or update transfers. The storage engine is append-only by design.
4. **Error correction transparency**: A reversal explicitly records "we made an error, here is the correction." A deletion hides the error entirely.

> **Key Insight**: In accounting, "undo" means "record that we are undoing." The original entry remains forever. The reversal entry cancels its effect by posting the exact opposite. The net effect on every account is zero, but the full history is preserved.

---

## Reversal Mechanics

A reversal is a new journal entry where every debit becomes a credit and every credit becomes a debit, with the same amounts:

```
    Original Entry (ID: abc-123):
    ┌──────────────────────────────────────────────────────┐
    │  DR  tenant:lemfi:assets:bank:gbp:clearing  GBP 1,000│
    │  CR  tenant:lemfi:liabilities:customer:pend GBP 1,000│
    └──────────────────────────────────────────────────────┘

    Reversal Entry (ID: def-456, ReversalOf: abc-123):
    ┌──────────────────────────────────────────────────────┐
    │  CR  tenant:lemfi:assets:bank:gbp:clearing  GBP 1,000│  <-- swapped!
    │  DR  tenant:lemfi:liabilities:customer:pend GBP 1,000│  <-- swapped!
    └──────────────────────────────────────────────────────┘

    Net effect on assets:bank:gbp:clearing:
      DR 1,000 (original) + CR 1,000 (reversal) = 0

    Net effect on liabilities:customer:pending:
      CR 1,000 (original) + DR 1,000 (reversal) = 0
```

The reversal entry carries metadata linking it to the original:
- `ReversalOf`: points to the original entry ID
- `IdempotencyKey`: `reversal:{original_id}` ensures at-most-once reversal
- `ReferenceType`: set to `"reversal"`
- `Description`: includes the reason for reversal

---

## The ReverseEntry() Implementation

Here is the complete `ReverseEntry()` method from `ledger/ledger.go`:

```go
// ReverseEntry creates a reversal journal entry for the given entry ID.
// The reversal mirrors all lines with swapped debit/credit types and
// references the original entry.
func (s *Service) ReverseEntry(ctx context.Context, entryID uuid.UUID, reason string) (*domain.JournalEntry, error) {
    if s.pg == nil {
        return nil, fmt.Errorf("settla-ledger: postgres backend not available for reversal lookup")
    }

    // Load original entry from Postgres.
    original, err := s.pg.GetJournalEntryWithLines(ctx, entryID)
    if err != nil {
        return nil, fmt.Errorf("settla-ledger: loading entry for reversal: %w", err)
    }

    // Build reversal: swap debit <-> credit on each line.
    reversalID := uuid.New()
    var reversalLines []domain.EntryLine
    for _, line := range original.Lines {
        reversedType := domain.EntryTypeDebit
        if line.EntryType == domain.EntryTypeDebit {
            reversedType = domain.EntryTypeCredit
        }
        reversalLines = append(reversalLines, domain.EntryLine{
            ID:        uuid.New(),
            AccountID: line.AccountID,
            Posting: domain.Posting{
                AccountCode: line.AccountCode,
                EntryType:   reversedType,
                Amount:      line.Amount,
                Currency:    line.Currency,
                Description: fmt.Sprintf("Reversal: %s", reason),
            },
        })
    }

    reversal := domain.JournalEntry{
        ID:             reversalID,
        TenantID:       original.TenantID,
        IdempotencyKey: fmt.Sprintf("reversal:%s", entryID),
        PostedAt:       time.Now().UTC(),
        EffectiveDate:  time.Now().UTC(),
        Description:    fmt.Sprintf("Reversal of %s: %s", entryID, reason),
        ReferenceType:  "reversal",
        ReferenceID:    &entryID,
        ReversalOf:     &entryID,
        Lines:          reversalLines,
        Metadata:       map[string]string{"reason": reason},
    }

    result, err := s.PostEntries(ctx, reversal)
    if err != nil {
        return nil, fmt.Errorf("settla-ledger: posting reversal: %w", err)
    }

    return result, nil
}
```

Let's trace through this step by step.

### Step 1: Load the Original from Postgres

```go
original, err := s.pg.GetJournalEntryWithLines(ctx, entryID)
```

The reversal needs to know what the original entry looked like. This query goes to Postgres (the read model), which has the full journal entry with all its lines:

```go
func (pg *pgBackend) GetJournalEntryWithLines(ctx context.Context, entryID uuid.UUID) (*domain.JournalEntry, error) {
    lines, err := pg.q.ListEntryLinesByJournal(ctx, entryID)
    if err != nil {
        return nil, fmt.Errorf("settla-ledger: loading entry lines for %s: %w", entryID, err)
    }
    if len(lines) == 0 {
        return nil, fmt.Errorf("settla-ledger: entry %s not found: %w",
            entryID, domain.ErrAccountNotFound(entryID.String()))
    }

    domainLines := make([]domain.EntryLine, len(lines))
    for i, line := range lines {
        // Resolve account ID -> code for the reversal.
        account, err := pg.q.GetAccount(ctx, line.AccountID)
        if err != nil {
            return nil, fmt.Errorf("settla-ledger: resolving account %s: %w", line.AccountID, err)
        }
        domainLines[i] = domain.EntryLine{
            ID:        line.ID,
            AccountID: line.AccountID,
            Posting: domain.Posting{
                AccountCode: account.Code,
                EntryType:   domain.EntryType(line.EntryType),
                Amount:      numericToDecimal(line.Amount),
                Currency:    domain.Currency(line.Currency),
                Description: line.Description.String,
            },
        }
    }

    return &domain.JournalEntry{
        ID:    entryID,
        Lines: domainLines,
    }, nil
}
```

Notice: the method resolves `account_id` back to `account.Code` for each line. This is necessary because `PostEntries()` works with account codes (which get hashed to TigerBeetle IDs), not with Postgres account UUIDs.

### Step 2: Swap Debits and Credits

```go
for _, line := range original.Lines {
    reversedType := domain.EntryTypeDebit
    if line.EntryType == domain.EntryTypeDebit {
        reversedType = domain.EntryTypeCredit
    }
    // ...
}
```

The swap logic is deliberately simple:
- If the original line was a DEBIT, the reversal line is a CREDIT
- If the original line was a CREDIT, the reversal line is a DEBIT
- The amount stays the same (always positive)

This guarantees the reversal entry is balanced: if the original had debits totaling X and credits totaling X, the reversal has credits totaling X and debits totaling X.

### Step 3: Construct the Reversal Entry

```go
reversal := domain.JournalEntry{
    ID:             reversalID,
    TenantID:       original.TenantID,
    IdempotencyKey: fmt.Sprintf("reversal:%s", entryID),
    PostedAt:       time.Now().UTC(),
    EffectiveDate:  time.Now().UTC(),
    Description:    fmt.Sprintf("Reversal of %s: %s", entryID, reason),
    ReferenceType:  "reversal",
    ReferenceID:    &entryID,
    ReversalOf:     &entryID,
    Lines:          reversalLines,
    Metadata:       map[string]string{"reason": reason},
}
```

Key fields:
- **IdempotencyKey**: `reversal:{original_id}` -- calling `ReverseEntry()` twice for the same original returns the same reversal. This prevents double-reversals.
- **ReversalOf**: Links to the original entry ID for audit trail navigation.
- **ReferenceType**: `"reversal"` -- enables querying all reversals across the ledger.

### Step 4: Post via the Normal Path

```go
result, err := s.PostEntries(ctx, reversal)
```

The reversal goes through the same `PostEntries()` pipeline as any other entry: validation, TigerBeetle write, PG sync. This means the reversal is subject to all the same invariants (balance check, idempotency, batching).

---

## The Outbox Reversal Intent

When the settlement engine needs a ledger reversal (e.g., a failed off-ramp requires undoing the on-ramp posting), it writes an `IntentLedgerReverse` to the outbox:

```go
const (
    IntentLedgerPost    = "ledger.post"
    IntentLedgerReverse = "ledger.reverse"
)
```

The LedgerWorker receives this intent from the `SETTLA_LEDGER` NATS stream and calls `ReverseEntry()`. The flow:

```
    Engine detects off-ramp failure
        |
        v
    Engine writes OutboxEntry:
      EventType: "ledger.reverse"
      Payload:   { entry_id: "abc-123", reason: "off-ramp failed" }
        |
        v
    Outbox Relay -> NATS: SETTLA_LEDGER stream
        |
        v
    LedgerWorker picks up intent
        |
        v
    LedgerWorker calls ledger.ReverseEntry(ctx, entryID, reason)
        |
        +---> Loads original from PG
        +---> Builds reversal (swap DR/CR)
        +---> Posts reversal via PostEntries()
        +---> Publishes result event back to engine
        |
        v
    Engine receives EventLedgerReversed
        |
        v
    Engine transitions transfer state
```

---

## Entry Linking: The Audit Chain

The `JournalEntry` type has two linking fields:

```go
type JournalEntry struct {
    // ...
    ReversedBy  *uuid.UUID  // ID of entry that reversed this one
    ReversalOf  *uuid.UUID  // ID of entry this one reverses
    // ...
}
```

After a reversal, the chain looks like:

```
    Original Entry (abc-123)              Reversal Entry (def-456)
    ┌────────────────────────┐            ┌────────────────────────┐
    │ ID:         abc-123    │            │ ID:         def-456    │
    │ ReversedBy: def-456 ───┼──────────> │ ReversalOf: abc-123 ───┼──┐
    │ ReversalOf: nil        │     ┌──────│ ReversedBy: nil        │  │
    │                        │     │      │                        │  │
    │ DR Assets    1,000     │     │      │ CR Assets    1,000     │  │
    │ CR Liab      1,000     │     │      │ DR Liab      1,000     │  │
    └────────────────────────┘     │      └────────────────────────┘  │
                                   │                                   │
                                   └───────────────────────────────────┘
                                         Bidirectional link
```

Querying reversals is straightforward:

```sql
-- Find all reversals for a given entry
SELECT * FROM journal_entries WHERE reversal_of = 'abc-123';

-- Find what reversed this entry
SELECT * FROM journal_entries WHERE id = (
    SELECT reversed_by FROM journal_entries WHERE id = 'abc-123'
);

-- Find all reversals in the system
SELECT * FROM journal_entries WHERE reference_type = 'reversal';
```

---

## Idempotency: Preventing Double Reversals

The idempotency key `reversal:{original_id}` ensures that a reversal can only be posted once per original entry:

```go
IdempotencyKey: fmt.Sprintf("reversal:%s", entryID),
```

If the LedgerWorker processes the same reversal intent twice (NATS redelivery), the second `PostEntries()` call will hit TigerBeetle's idempotency check. The deterministic transfer IDs (generated from the entry ID and line index) will match the already-created transfers, and TigerBeetle returns `TBResultExists`.

```
    First reversal:
    PostEntries(reversal) -> TB: CreateTransfers -> TBResultOK

    Second reversal (replay):
    PostEntries(reversal) -> TB: CreateTransfers -> TBResultExists (idempotent, no double-reversal)
```

---

## Reversal in the Settlement Lifecycle

Reversals occur at specific points in the transfer state machine:

```
    Transfer State Machine:

    CREATED -> FUNDED -> ON_RAMP_PENDING -> ON_RAMP_COMPLETE
                                               |
                                          (if failure)
                                               |
                                               v
                                         COMPENSATION
                                               |
                                    ┌──────────┼──────────┐
                                    v          v          v
                              SIMPLE_REFUND  REVERSE   MANUAL
                                    |       ONRAMP    REVIEW
                                    |          |
                                    v          v
                              Reverse        Reverse
                              treasury       treasury
                              posting        + ledger
                                             posting
```

When compensation triggers a `REVERSE_ONRAMP` strategy, the engine writes two outbox entries:
1. `IntentLedgerReverse` -- undo the on-ramp ledger entry
2. `IntentTreasuryRelease` -- release the treasury reservation

The LedgerWorker processes the reversal, and the TreasuryWorker processes the release. Both are idempotent, both flow through the outbox, and both report back to the engine.

---

## Deposit Refund Reversals

Deposit flows introduce a new reversal scenario. When a crypto deposit is credited to a tenant's position and the subsequent auto-convert (fiat settlement) fails, the deposit credit must be reversed:

```
    Deposit Reversal Flow:

    1. Crypto deposit confirmed → CREDITING → journal entry posted:
       DR  tenant:lemfi:assets:crypto:usdt:balance    USDT 998.00
       DR  tenant:lemfi:revenue:fees:deposit           USDT   2.00
       CR  assets:crypto:usdt:tron                     USDT 1000.00

    2. Auto-convert fails → compensation triggered → reversal posted:
       CR  tenant:lemfi:assets:crypto:usdt:balance    USDT 998.00   (swapped)
       CR  tenant:lemfi:revenue:fees:deposit           USDT   2.00   (swapped)
       DR  assets:crypto:usdt:tron                     USDT 1000.00  (swapped)

    3. Treasury position event: DEBIT (reversal of earlier CREDIT)
```

The reversal uses the same `ReverseEntry()` code path and the same idempotency mechanism (`reversal:{original_entry_id}`). The treasury manager also emits a `PosEventDebit` to reverse the position credit, ensuring the event-sourced audit trail reflects the full lifecycle.

Bank deposit reversals follow the same pattern when a fiat credit needs to be unwound (e.g., bank confirms the deposit was fraudulent).

---

## Partial Reversals

Not all reversals are full. If an on-ramp partially completed (e.g., the provider converted 800 of 1,000 GBP), the reversal should only undo the uncommitted 200 GBP.

Partial reversals are handled by creating a new journal entry (not using `ReverseEntry()`) with the partial amounts:

```
    Original Entry:
    DR  assets:crypto:usdt      USDT  960.00
    DR  expenses:provider       USDT   40.00
    CR  assets:bank:gbp         GBP  1,000.00

    Partial Reversal (only 200 GBP uncommitted):
    CR  assets:crypto:usdt      USDT  192.00   (200/1000 * 960)
    CR  expenses:provider       USDT    8.00   (200/1000 * 40)
    DR  assets:bank:gbp         GBP   200.00
```

This is NOT a call to `ReverseEntry()` -- it is a new `PostEntries()` with custom amounts. The `ReferenceType` would still be `"reversal"` and `ReversalOf` would point to the original, but the amounts are proportionally scaled.

> **Key Insight**: `ReverseEntry()` is for FULL reversals only. Partial reversals are constructed manually by the compensation module with pro-rated amounts. Both create new journal entries -- neither modifies the original.

---

## Common Mistakes

**Mistake 1: Trying to UPDATE or DELETE a ledger entry.**
Never modify an existing entry. Always create a reversal. The database schema does not even allow updates to `entry_lines` (the application role has `INSERT` and `SELECT` only for these tables).

**Mistake 2: Reversing a reversal.**
A reversal of a reversal would re-post the original amounts. This is almost always a bug. The idempotency key `reversal:{id}` prevents accidental double-reversal, but if you genuinely need to re-post, create a fresh entry with a new idempotency key.

**Mistake 3: Assuming ReverseEntry() modifies the original.**
The original entry is unchanged. `ReversedBy` on the original is updated by the Postgres sync layer for query convenience, but TigerBeetle has no knowledge of this link -- it just sees two independent (but opposite) entries.

**Mistake 4: Calling ReverseEntry() before the sync consumer has written to Postgres.**
`ReverseEntry()` loads the original from Postgres. If the sync consumer has not yet flushed the original entry (up to 100ms lag), the reversal will fail with "entry not found." The LedgerWorker handles this by retrying with NATS redelivery.

**Mistake 5: Using ReverseEntry() for partial refunds.**
`ReverseEntry()` always reverses the full amount. For partial refunds, the compensation module constructs a new journal entry with the partial amounts and posts it directly via `PostEntries()`.

---

## Exercises

1. **Reversal Construction**: Given this original entry:
   ```
   DR  tenant:fincra:assets:bank:ngn:clearing     NGN  500,000.00
   DR  tenant:fincra:expenses:provider:offramp     NGN    2,500.00
   CR  assets:crypto:usdt:tron                     USDT   950.00
   CR  tenant:fincra:revenue:fees:settlement       NGN    2,500.00
   ```
   Write the reversal entry. What is the idempotency key? What is the net effect on each account?

2. **Multi-Currency Reversal**: Can a single journal entry contain both GBP and NGN lines? If so, does the reversal need to balance per currency? Trace through `ValidateEntries()` to confirm.

3. **Timing Edge Case**: A transfer's on-ramp ledger entry is posted at t=0. The sync consumer writes it to Postgres at t=95ms. The off-ramp fails at t=80ms, triggering a reversal. What happens? How does the system eventually recover?

4. **Audit Query**: Write a SQL query that finds all journal entries for tenant `lemfi` that have been reversed, along with their reversal entries and the reason for each reversal.

5. **Idempotency Proof**: The LedgerWorker receives the same `IntentLedgerReverse` three times due to NATS redelivery. Trace through the idempotency mechanisms at each layer (outbox dedup window, TigerBeetle transfer ID dedup, Postgres idempotency key unique index). Which layer catches the duplicate?

---

## What's Next

Reversals keep the ledger mathematically correct even when transfers fail. But at 50 million transactions per day, the ledger tables grow by hundreds of millions of rows daily. The next chapter covers how Settla partitions these tables to maintain query performance and enable instant cleanup via DROP TABLE instead of DELETE.

**Next: [Chapter 2.6: Partitioning at Scale](./chapter-2.6-partitioning.md)**
