# Chapter 1.4: The Transfer State Machine

**Estimated reading time:** 25 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why explicit state machines are mandatory in financial systems
2. Read and interpret Settla's `ValidTransitions` map as both code and documentation
3. Describe each transfer state's operational meaning and the side effects it triggers
4. Trace the happy path and every failure path through the state machine
5. Implement transition validation with optimistic locking and event generation

---

## Why Explicit State Machines

Most web applications represent status as a string column that gets updated ad-hoc throughout the codebase:

```go
// The ad-hoc approach (dangerous in financial systems):
transfer.Status = "processing"  // What does "processing" mean exactly?
transfer.Status = "done"        // Can we go from "processing" to "done" directly?
transfer.Status = "error"       // Can we go from "done" to "error"? What if money already moved?
```

This approach has four specific failure modes in financial systems:

1. **No validation**: Any status string can transition to any other. A bug can skip `FUNDED` and go directly from `CREATED` to `SETTLING`, bypassing the treasury reservation. Money moves without being reserved.
2. **Implicit rules**: The valid transitions are scattered across dozens of functions in different files. No single developer can see the complete lifecycle.
3. **No documentation**: The valid lifecycle exists only in developers' heads. New team members guess at valid transitions.
4. **Testing gaps**: Without an explicit map, you cannot exhaustively test all valid and invalid transitions.

Settla uses an **explicit state machine** where valid transitions are defined as data -- a single map that IS the documentation, the validation logic, and the test specification.

---

## The States

```go
// From domain/transfer.go
type TransferStatus string

const (
    TransferStatusCreated    TransferStatus = "CREATED"
    TransferStatusFunded     TransferStatus = "FUNDED"
    TransferStatusOnRamping  TransferStatus = "ON_RAMPING"
    TransferStatusSettling   TransferStatus = "SETTLING"
    TransferStatusOffRamping TransferStatus = "OFF_RAMPING"
    TransferStatusCompleted  TransferStatus = "COMPLETED"
    TransferStatusFailed     TransferStatus = "FAILED"
    TransferStatusRefunding  TransferStatus = "REFUNDING"
    TransferStatusRefunded   TransferStatus = "REFUNDED"
)
```

Nine states. Two terminal (`COMPLETED`, `REFUNDED`). Seven non-terminal. Each state represents a precise operational condition.

---

## The ValidTransitions Map

This is the most important data structure in the entire codebase. It defines every legal state transition:

```go
// From domain/transfer.go
var ValidTransitions = map[TransferStatus][]TransferStatus{
    TransferStatusCreated:    {TransferStatusFunded, TransferStatusFailed},
    TransferStatusFunded:     {TransferStatusOnRamping, TransferStatusRefunding},
    TransferStatusOnRamping:  {TransferStatusSettling, TransferStatusRefunding, TransferStatusFailed},
    TransferStatusSettling:   {TransferStatusOffRamping, TransferStatusFailed},
    TransferStatusOffRamping: {TransferStatusCompleted, TransferStatusFailed},
    TransferStatusFailed:     {TransferStatusRefunding},
    TransferStatusRefunding:  {TransferStatusRefunded},
    // COMPLETED and REFUNDED are absent -- they are terminal states with no outbound transitions.
}
```

> **Key Insight:** This map IS the state machine documentation. You do not need a separate diagram (though we will draw one below for illustration). Any question about "can a transfer go from X to Y?" is answered by looking at this map. If the target state is in the slice for the source state, the transition is legal. If not, it is forbidden. Period.

---

## The State Machine Diagram

```
    TRANSFER STATE MACHINE
    ======================

    HAPPY PATH (top to bottom):

    +----------+
    | CREATED  |-----> Treasury reserve requested
    +----+-----+
         |
         v
    +----------+
    |  FUNDED  |-----> On-ramp provider call requested
    +----+-----+
         |
         v
    +------------+
    | ON_RAMPING |---> GBP being converted to USDT by provider
    +----+-------+
         |
         v
    +----------+
    | SETTLING |-----> USDT sent on-chain (e.g., Tron)
    +----+-----+
         |
         v
    +-------------+
    | OFF_RAMPING |---> USDT being converted to NGN by provider
    +----+--------+
         |
         v
    +-----------+
    | COMPLETED |  (TERMINAL - recipient has the money)
    +-----------+


    FAILURE PATHS (any non-terminal state can reach FAILED):

    CREATED -------> FAILED    (treasury reservation failed)
    ON_RAMPING ----> FAILED    (on-ramp provider error)
    SETTLING ------> FAILED    (blockchain error)
    OFF_RAMPING ---> FAILED    (off-ramp provider error)

    REFUND PATH:

    FUNDED --------> REFUNDING  (pre-emptive refund, no side effects yet)
    ON_RAMPING ----> REFUNDING  (on-ramp failed, reverse what was done)
    FAILED --------> REFUNDING  (initiate compensation for failed transfer)
    REFUNDING -----> REFUNDED   (TERMINAL - refund complete)
```

### Which Transitions Are NOT Allowed

Equally important is understanding what transitions the map forbids:

```
    CREATED  -/-> ON_RAMPING    (cannot skip treasury reservation)
    CREATED  -/-> SETTLING      (cannot skip on-ramp)
    FUNDED   -/-> SETTLING      (cannot skip on-ramp)
    FUNDED   -/-> COMPLETED     (cannot skip the entire settlement)
    SETTLING -/-> COMPLETED     (cannot skip off-ramp)
    COMPLETED -> (nothing)      (terminal: money has been delivered)
    REFUNDED  -> (nothing)      (terminal: refund has been completed)
    COMPLETED -/-> FAILED       (you cannot "fail" after delivery)
    REFUNDED  -/-> FAILED       (you cannot "fail" after refund)
```

Each forbidden transition prevents a specific class of bug. The `CREATED -> ON_RAMPING` prohibition prevents money from being sent to a provider before treasury funds are reserved. The `COMPLETED -> FAILED` prohibition prevents marking a transfer as failed after the recipient already received the money.

---

## Transition Validation Code

The `Transfer` type carries its own transition validation:

```go
// From domain/transfer.go
func (t *Transfer) CanTransitionTo(target TransferStatus) bool {
    allowed, ok := ValidTransitions[t.Status]
    if !ok {
        return false  // Current state is terminal -- no transitions allowed
    }
    for _, a := range allowed {
        if a == target {
            return true
        }
    }
    return false
}
```

For terminal states (`COMPLETED`, `REFUNDED`), `ValidTransitions[t.Status]` returns `ok=false` because those keys do not exist in the map. This means `CanTransitionTo` returns `false` for any target, making terminal states truly terminal.

### The TransitionTo Method

`TransitionTo` validates and applies a transition atomically, producing an audit event:

```go
// From domain/transfer.go
func (t *Transfer) TransitionTo(target TransferStatus) (*TransferEvent, error) {
    if !t.CanTransitionTo(target) {
        return nil, ErrInvalidTransition(string(t.Status), string(target))
    }
    from := t.Status
    t.Status = target
    t.Version++                    // Increment optimistic lock counter
    t.UpdatedAt = time.Now().UTC() // Always UTC

    event := &TransferEvent{
        ID:         uuid.New(),
        TransferID: t.ID,
        TenantID:   t.TenantID,
        FromStatus: from,
        ToStatus:   target,
        OccurredAt: t.UpdatedAt,
    }
    return event, nil
}
```

This method does three things atomically (on the in-memory object):
1. **Validates** the transition against the map
2. **Applies** the new status and increments the version
3. **Produces** a `TransferEvent` that records the transition for the audit trail

The `TransferEvent` struct captures the full context of every state change:

```go
// From domain/transfer.go
type TransferEvent struct {
    ID          uuid.UUID
    TransferID  uuid.UUID
    TenantID    uuid.UUID
    FromStatus  TransferStatus
    ToStatus    TransferStatus
    OccurredAt  time.Time
    Metadata    map[string]string
    ProviderRef string
}
```

> **Key Insight:** Every state transition produces an event. This is not optional -- it is a regulatory requirement. Financial regulators need to see every status change, what triggered it, and when it occurred. The `TransferEvent` table is the audit trail that regulators examine during compliance reviews.

---

## Each State Explained Operationally

### CREATED

**What happened:** The transfer request has been validated, a quote has been obtained from the router, and the transfer record has been persisted.

**What has NOT happened:** No money has moved. No external systems have been called. No treasury funds have been reserved.

**Outbox entries written:** `transfer.created` event (notification to subscribers)

**Valid next states:**
- `FUNDED` -- treasury reservation succeeded
- `FAILED` -- treasury reservation failed (insufficient funds, tenant suspended)

### FUNDED

**What happened:** Treasury funds have been reserved for this transfer. The tenant's available balance has been decremented and locked balance incremented by the transfer amount.

**What has NOT happened:** No provider has been called. No fiat-to-stablecoin conversion has started.

**Outbox entries written:** `treasury.reserve` intent, `transfer.funded` event

**Valid next states:**
- `ON_RAMPING` -- on-ramp provider call initiated
- `REFUNDING` -- pre-emptive cancellation (release treasury reservation)

### ON_RAMPING

**What happened:** The on-ramp provider has been called to convert fiat (e.g., GBP) to stablecoin (e.g., USDT). The provider is processing the conversion.

**What has NOT happened:** The conversion may not be complete. No on-chain transfer has occurred.

**Outbox entries written:** `provider.onramp.execute` intent

**Valid next states:**
- `SETTLING` -- on-ramp succeeded, stablecoin ready for on-chain transfer
- `REFUNDING` -- on-ramp failed, need to reverse treasury reservation
- `FAILED` -- on-ramp failed, error recorded

### SETTLING

**What happened:** Stablecoin is being transferred on-chain from the on-ramp wallet to the off-ramp wallet. A blockchain transaction has been submitted.

**What has NOT happened:** The blockchain transaction may not be confirmed yet. The off-ramp has not started.

**Outbox entries written:** `blockchain.send` intent, `ledger.post` intent

**Valid next states:**
- `OFF_RAMPING` -- blockchain transaction confirmed, off-ramp initiated
- `FAILED` -- blockchain transaction failed (gas error, network congestion)

### OFF_RAMPING

**What happened:** The off-ramp provider has been called to convert stablecoin (e.g., USDT) back to fiat (e.g., NGN) and deposit to the recipient's bank account.

**What has NOT happened:** The recipient may not have received the funds yet. The provider is processing.

**Outbox entries written:** `provider.offramp.execute` intent

**Valid next states:**
- `COMPLETED` -- off-ramp succeeded, recipient received funds
- `FAILED` -- off-ramp failed (invalid bank details, provider error)

### COMPLETED (Terminal)

**What happened:** The recipient has received the fiat currency in their bank account. The settlement is finished.

**Side effects:** Treasury reservation released, final ledger entries posted, webhook delivered to tenant.

**Outbox entries written:** `webhook.deliver` intent, `ledger.post` intent (final entries)

**No valid transitions.** This state is terminal. The money has arrived.

### FAILED

**What happened:** An error occurred during the transfer. The specific failure is recorded in `FailureReason` and `FailureCode`.

**Outbox entries written:** `webhook.deliver` intent (notify tenant of failure)

**Valid next states:**
- `REFUNDING` -- compensation process initiated to reverse completed steps

### REFUNDING

**What happened:** The system is reversing the steps that were completed before the failure. This may involve reversing ledger entries, releasing treasury reservations, or requesting provider refunds.

**Outbox entries written:** `ledger.reverse` intent, `treasury.release` intent

**Valid next states:**
- `REFUNDED` -- all compensation steps completed

### REFUNDED (Terminal)

**What happened:** All compensation steps have been completed. Treasury funds have been released. Ledger entries have been reversed.

**No valid transitions.** This state is terminal. The refund is complete.

---

## The Happy Path in Detail

```
    Time 0ms:    API request arrives
                 Engine validates, gets quote, creates transfer
                 Status: CREATED
                 Outbox: [transfer.created event]

    Time 1ms:    Outbox relay picks up entry, publishes to NATS
                 TransferWorker calls Engine.FundTransfer()
                 Engine writes: status -> FUNDED + treasury.reserve intent

    Time 50ms:   TreasuryWorker picks up intent
                 Calls treasury.Reserve() (in-memory, ~100ns)
                 Publishes treasury.reserved result
                 Engine writes: status -> ON_RAMPING + provider.onramp.execute intent

    Time 100ms:  ProviderWorker picks up intent
                 CHECK-BEFORE-CALL: checks provider_transactions table
                 Calls on-ramp provider API
                 Provider converts GBP to USDT
                 Publishes provider.onramp.completed result

    Time 5sec:   Engine writes: status -> SETTLING + blockchain.send intent
                 BlockchainWorker picks up intent
                 Submits Tron TRC-20 transfer
                 Waits for confirmation (~3 seconds)
                 Publishes blockchain.confirmed result

    Time 8sec:   Engine writes: status -> OFF_RAMPING + provider.offramp.execute intent
                 ProviderWorker picks up intent
                 Calls off-ramp provider API
                 Provider converts USDT to NGN, initiates bank transfer

    Time 30sec:  Provider confirms off-ramp complete
                 Engine writes: status -> COMPLETED + webhook.deliver intent
                 WebhookWorker delivers transfer.completed to tenant

    Total: ~30 seconds for complete settlement
```

---

## Failure Paths

### Failure During ON_RAMPING

The most complex failure scenario. The on-ramp provider has been called but the conversion failed:

```
    CREATED -> FUNDED -> ON_RAMPING -> FAILED -> REFUNDING -> REFUNDED

    What must be reversed:
    1. Treasury reservation (reserved in FUNDED step) -> Release
    2. Ledger entries (if any were posted) -> Reverse
    3. No blockchain transaction to reverse (hasn't happened yet)
    4. No off-ramp to reverse (hasn't happened yet)
```

### Failure During SETTLING

The blockchain transaction failed (insufficient gas, network congestion):

```
    CREATED -> FUNDED -> ON_RAMPING -> SETTLING -> FAILED -> REFUNDING -> REFUNDED

    What must be reversed:
    1. Treasury reservation -> Release
    2. On-ramp conversion -> Request provider refund
    3. Ledger entries -> Reverse
    4. No blockchain tx to reverse (it failed, nothing happened on-chain)
```

### Failure During OFF_RAMPING

The off-ramp provider rejected the recipient's bank details:

```
    CREATED -> FUNDED -> ON_RAMPING -> SETTLING -> OFF_RAMPING -> FAILED -> REFUNDING -> REFUNDED

    What must be reversed:
    1. Treasury reservation -> Release
    2. On-ramp conversion -> Request provider refund
    3. Blockchain transaction -> May need reverse transfer
    4. Ledger entries -> Reverse
    5. Off-ramp -> Nothing to reverse (it failed before completing)
```

> **Key Insight:** The deeper into the state machine a failure occurs, the more compensation steps are required. This is why Settla has four compensation strategies: `SIMPLE_REFUND` (release treasury only), `REVERSE_ONRAMP` (reverse the provider conversion), `CREDIT_STABLECOIN` (credit USDT to the tenant's stablecoin balance), and `MANUAL_REVIEW` (escalate to a human operator). The strategy is selected based on which states were successfully completed before the failure.

---

## Optimistic Locking: Preventing Concurrent Corruption

The `Version` field on the `Transfer` struct prevents two concurrent processes from advancing the same transfer:

```
    Scenario without optimistic locking:

    Time 0ms:    Worker A reads transfer (status=SETTLING, version=4)
    Time 0ms:    Worker B reads transfer (status=SETTLING, version=4)
    Time 5ms:    Worker A writes: status=OFF_RAMPING, version=5
    Time 10ms:   Worker B writes: status=FAILED, version=5
                 Worker B overwrites Worker A's update!
                 Transfer is now FAILED but off-ramp provider was called.

    Scenario WITH optimistic locking:

    Time 0ms:    Worker A reads transfer (status=SETTLING, version=4)
    Time 0ms:    Worker B reads transfer (status=SETTLING, version=4)
    Time 5ms:    Worker A writes: UPDATE ... SET status='OFF_RAMPING', version=5
                                  WHERE id=? AND version=4
                 Succeeds (version matches)
    Time 10ms:   Worker B writes: UPDATE ... SET status='FAILED', version=5
                                  WHERE id=? AND version=4
                 FAILS: 0 rows affected (version is now 5, not 4)
                 Worker B knows the transfer was modified, retries with fresh state
```

The `TransitionTo` method increments the version:

```go
t.Version++  // Now the database UPDATE will include WHERE version = old_version
```

---

## The TransferEvent Audit Trail

Every transition produces a `TransferEvent`. Over the lifecycle of a successful transfer, this creates 6 events:

```
    Event 1: CREATED    -> FUNDED       at 2026-03-15T10:00:00.001Z
    Event 2: FUNDED     -> ON_RAMPING   at 2026-03-15T10:00:00.050Z
    Event 3: ON_RAMPING -> SETTLING     at 2026-03-15T10:00:05.200Z
    Event 4: SETTLING   -> OFF_RAMPING  at 2026-03-15T10:00:08.100Z
    Event 5: OFF_RAMPING -> COMPLETED   at 2026-03-15T10:00:30.500Z

    For a failed transfer with refund, 4+ events:
    Event 1: CREATED    -> FUNDED       at 2026-03-15T10:00:00.001Z
    Event 2: FUNDED     -> ON_RAMPING   at 2026-03-15T10:00:00.050Z
    Event 3: ON_RAMPING -> FAILED       at 2026-03-15T10:00:05.200Z
    Event 4: FAILED     -> REFUNDING    at 2026-03-15T10:00:05.300Z
    Event 5: REFUNDING  -> REFUNDED     at 2026-03-15T10:00:06.100Z
```

At 50M transfers/day, this generates 250-300M events per day. Each event is stored in the `transfer_events` table and is immutable -- events are never updated or deleted.

---

## State Machine Pattern Reuse Across the Codebase

The transfer state machine pattern -- explicit transition maps, validation functions, optimistic locking, and event emission on transition -- is reused for every lifecycle entity in Settla. This consistency means once you understand the transfer state machine, you understand them all.

### Deposit Sessions

Crypto deposit sessions follow a longer chain with more states, but the same structural pattern:

```
    PENDING_PAYMENT -> DETECTED -> CONFIRMED -> CREDITING -> CREDITED
                                                                |
                                                  +-------------+----------+
                                                  |                        |
                                                  v                        v
                                              SETTLING                   HELD (terminal)
                                                  |
                                                  v
                                              SETTLED (terminal)

    Alternative paths:
    PENDING_PAYMENT -> EXPIRED / CANCELLED
    EXPIRED / CANCELLED -> DETECTED  (late payment recovery)
    DETECTED -> PENDING_PAYMENT      (reorg/unconfirm)
    Any non-terminal -> FAILED
```

```go
// From domain/deposit.go
var ValidDepositTransitions = map[DepositSessionStatus][]DepositSessionStatus{
    DepositSessionStatusPendingPayment: {Detected, Expired, Cancelled},
    DepositSessionStatusDetected:       {Confirmed, PendingPayment, Failed},
    DepositSessionStatusConfirmed:      {Crediting, Failed},
    DepositSessionStatusCrediting:      {Credited, Failed},
    DepositSessionStatusCredited:       {Settling, Held},
    DepositSessionStatusSettling:       {Settled, Failed},
    DepositSessionStatusExpired:        {Detected}, // late payment
    DepositSessionStatusCancelled:      {Detected}, // late payment
    // Settled, Held, Failed: terminal
}
```

### Bank Deposit Sessions

Bank deposits add payment mismatch states (UNDERPAID, OVERPAID) not present in crypto deposits:

```
    PENDING_PAYMENT -> PAYMENT_RECEIVED -> CREDITING -> CREDITED -> SETTLING -> SETTLED
                                      |                     |
                                      v                     v
                                UNDERPAID/OVERPAID        HELD (terminal)

    Late payment: EXPIRED/CANCELLED -> PAYMENT_RECEIVED
```

### Position Transactions

Position transactions (top-ups, withdrawals, deposit credits, internal rebalancing) follow the simplest state machine in the codebase:

```
    PENDING -> PROCESSING -> COMPLETED (terminal)
                         \-> FAILED    (terminal)
    PENDING -> FAILED (terminal, immediate rejection)
```

```go
// From domain/position_transaction.go
var ValidPositionTxTransitions = map[PositionTxStatus][]PositionTxStatus{
    PositionTxStatusPending:    {PositionTxStatusProcessing, PositionTxStatusFailed},
    PositionTxStatusProcessing: {PositionTxStatusCompleted, PositionTxStatusFailed},
    PositionTxStatusCompleted:  {}, // terminal
    PositionTxStatusFailed:     {}, // terminal
}
```

### Payment Links

Payment links use the simplest lifecycle -- they are not a state machine in the traditional sense but have status transitions enforced by `CanRedeem()`:

```
    ACTIVE -> DISABLED  (manually disabled by tenant)
    ACTIVE -> EXPIRED   (time-based expiration)
```

Payment links have no `ValidTransitions` map because they are CRUD entities, not saga-driven. The status is checked at redemption time, not advanced through a worker pipeline.

> **Key Insight:** Every state machine in Settla follows the same structural pattern: a `map[Status][]Status` for valid transitions, a `CanTransitionTo(target)` method for validation, a `TransitionTo(target)` method that increments a version counter and timestamps the change, and terminal states represented as keys with empty slices (or absent from the map entirely). This uniformity means a developer who understands the transfer state machine can immediately read and modify any other state machine in the codebase.

---

## Common Mistakes

### Mistake 1: String-Based Status Comparisons

```go
// WRONG: typos are silent bugs
if transfer.Status == "complted" {  // Typo: "complted" instead of "completed"
    // This branch NEVER executes. No compiler warning.
}

// RIGHT: typed constants catch typos at compile time
if transfer.Status == TransferStatusCompleted {
    // Compiler verifies TransferStatusCompleted is a valid constant
}
```

### Mistake 2: Not Recording State Transitions

Without `TransferEvent` records, you cannot answer "what happened to transfer X?" when a customer complains. Regulators require this audit trail for compliance. In a dispute, the event history is the evidence.

### Mistake 3: Allowing Transitions from Terminal States

`COMPLETED` and `REFUNDED` must be truly terminal. If you can transition from `COMPLETED` to `FAILED`, you create a paradox: the recipient already has the money, but the system says the transfer failed. The resulting accounting mess requires manual intervention to resolve.

### Mistake 4: Not Using Optimistic Locking

Without the `Version` field, two concurrent workers processing the same transfer can both succeed, advancing it through two different paths simultaneously. One worker transitions to `OFF_RAMPING` while another transitions to `FAILED`. The transfer ends up in an inconsistent state.

### Mistake 5: Direct Status Mutation

```go
// WRONG: bypassing the state machine
transfer.Status = TransferStatusCompleted  // No validation, no event, no version increment

// RIGHT: using TransitionTo
event, err := transfer.TransitionTo(TransferStatusCompleted)
if err != nil {
    // Invalid transition caught
}
// event contains the audit record
```

---

## Exercises

### Exercise 1: State Machine Completeness Test

Write a Go test that verifies:
1. Every non-terminal state has at least one valid transition
2. Terminal states (`COMPLETED`, `REFUNDED`) have zero valid transitions (they do not appear as keys in `ValidTransitions`)
3. Every state reachable from `CREATED` can eventually reach either `COMPLETED` or `REFUNDED` (no dead ends)
4. There are no transitions from a terminal state to any other state

### Exercise 2: Enumerate Invalid Transitions

There are 9 states and 81 possible (source, target) pairs. The `ValidTransitions` map defines ~15 valid transitions. Write a test that:
1. Generates all 81 pairs
2. Checks each against `CanTransitionTo`
3. Verifies that exactly the transitions in the map return `true`
4. Verifies that all others return `false`

### Exercise 3: Design Your Own State Machine

Design a state machine for an online order fulfillment system:
1. Define states: `PLACED`, `PAYMENT_PENDING`, `PAID`, `PICKING`, `PACKED`, `SHIPPED`, `DELIVERED`, `RETURNED`, `CANCELLED`
2. Define the `ValidTransitions` map
3. Identify terminal states
4. Draw the diagram showing happy path and cancellation paths
5. Consider: can an order be cancelled after it has been shipped? Should `DELIVERED` be truly terminal?

### Exercise 4: Trace a Failure Scenario

A transfer reaches `SETTLING` (blockchain transaction submitted) and then the blockchain network experiences a reorganization (reorg), invalidating the transaction.

1. What state transition occurs? (`SETTLING -> FAILED`)
2. What compensation steps are needed?
3. Which outbox intents must be written?
4. How does the system know which steps to reverse?
5. What error code from `domain/errors.go` applies? (Hint: `CodeBlockchainReorg`)

---

## What's Next

The state machine controls individual transfer lifecycles. But every transfer belongs to a tenant, and every tenant has different fee schedules, limits, and settlement models. In Chapter 1.5, we will study Settla's multi-tenancy design -- how tenant isolation is enforced at every layer, from API authentication to database queries to cache entries.

---
