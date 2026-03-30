# Chapter 9.1: Crypto Deposits -- On-Chain Payment Detection

**Reading time:** ~30 minutes
**Prerequisites:** Module 1 (Domain Model), Module 3 (Engine & Outbox), Module 4 (Treasury), Module 5 (Workers)
**Code references:** `domain/deposit.go`, `core/deposit/engine.go`, `core/deposit/store.go`, `node/chainmonitor/evm_poller.go`, `node/chainmonitor/tron_poller.go`, `node/chainmonitor/token_registry.go`, `node/worker/deposit_worker.go`

---

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain the crypto deposit use case and how it differs from outbound transfers.
2. Trace the full deposit session state machine from creation through crediting.
3. Describe how chain monitors detect on-chain stablecoin payments using checkpoint-based scanning.
4. Explain confirmation depth, reorg risk, and why different chains need different confirmation counts.
5. Implement the pure-state-machine pattern for a deposit engine that makes zero network calls.
6. Describe the address pool pattern and why deposit addresses are pre-generated via HD wallet derivation.

---

## 9.1.1 The Deposit Use Case

Everything in Modules 1 through 8 focused on *outbound* money movement: a
fintech tenant creates a transfer, Settla routes it through providers, and funds
arrive in a bank account. But money has to enter the system before it can leave.

The crypto deposit flow handles *inbound* money movement. Here is the scenario:

```
Merchant Deposit Flow
======================

1. Merchant (via API) creates a deposit session:
   "I want to accept 500 USDT on Tron"

2. Settla responds with a deposit address:
   "Send USDT to TN7x...4kGf (valid for 1 hour)"

3. Customer sends 500 USDT to that address on-chain

4. Settla's chain monitor detects the transaction

5. After 19 block confirmations, Settla credits the merchant:
   Gross: 500 USDT
   Fee:   2 USDT (40 bps)
   Net:   498 USDT credited to merchant's treasury position

6. Based on settlement preference:
   AUTO_CONVERT: Settla converts 498 USDT to fiat
   HOLD:         498 USDT stays as crypto balance
```

This flow has two properties that make it fundamentally different from outbound
transfers:

**Property 1: You cannot control when the payment arrives.** An outbound
transfer starts when the engine writes the first outbox entry. A deposit starts
when an unknown third party (the customer) sends an on-chain transaction at an
unpredictable time. The system must continuously watch the blockchain.

**Property 2: The blockchain is the source of truth.** For outbound transfers,
the provider confirms execution. For deposits, the blockchain itself confirms
that funds have arrived. But blockchains can *reorganize* -- the chain tip can
be replaced by a longer fork, reversing previously confirmed transactions.

> **Key Insight:** A deposit system is fundamentally a monitoring problem.
> Unlike outbound transfers where you initiate the action, deposits require
> you to passively watch blockchains and react to events you did not control.
> The two biggest engineering challenges are (1) detecting payments reliably
> and (2) waiting long enough to avoid crediting funds from a reorged block.

---

## 9.1.2 The Deposit Session State Machine

The deposit session lifecycle is defined in `domain/deposit.go`. Like the
transfer state machine from Chapter 1.4, it uses explicit states with validated
transitions.

### States

```
State             Description
-----------       --------------------------------------------------
PENDING_PAYMENT   Created, waiting for on-chain payment
DETECTED          On-chain transaction found, not yet confirmed
CONFIRMED         Enough block confirmations accumulated
CREDITING         Ledger credit + treasury update in progress
CREDITED          Tenant's ledger has been credited
SETTLING          Crypto-to-fiat conversion in progress (AUTO_CONVERT)
SETTLED           Terminal: fiat conversion complete
HELD              Terminal: crypto held without conversion (HOLD)
EXPIRED           TTL exceeded without payment
FAILED            Unrecoverable error occurred
CANCELLED         Cancelled before payment arrived
```

### Transition Diagram

```
PENDING_PAYMENT --> DETECTED --> CONFIRMED --> CREDITING --> CREDITED
      |                |^              |            |       /        \
      |           (reorg)|             v            v   SETTLING    HELD
      v             PENDING_        FAILED       FAILED    |     (terminal)
   EXPIRED          PAYMENT                                v
   CANCELLED -----> DETECTED  (late payment)           SETTLED
                                                      (terminal)
```

The transition map is defined as a Go map in `domain/deposit.go`:

```go
// domain/deposit.go

var ValidDepositTransitions = map[DepositSessionStatus][]DepositSessionStatus{
    DepositSessionStatusPendingPayment: {DepositSessionStatusDetected, DepositSessionStatusExpired, DepositSessionStatusCancelled},
    DepositSessionStatusDetected:       {DepositSessionStatusConfirmed, DepositSessionStatusPendingPayment, DepositSessionStatusFailed},
    DepositSessionStatusConfirmed:      {DepositSessionStatusCrediting, DepositSessionStatusFailed},
    DepositSessionStatusCrediting:      {DepositSessionStatusCredited, DepositSessionStatusFailed},
    DepositSessionStatusCredited:       {DepositSessionStatusSettling, DepositSessionStatusHeld},
    DepositSessionStatusSettling:       {DepositSessionStatusSettled, DepositSessionStatusFailed},
    DepositSessionStatusExpired:        {DepositSessionStatusDetected}, // late payment after expiry
    DepositSessionStatusFailed:         {},                             // terminal
    DepositSessionStatusSettled:        {},                             // terminal
    DepositSessionStatusHeld:           {},                             // terminal
    DepositSessionStatusCancelled:      {DepositSessionStatusDetected}, // late payment after cancel
}
```

Two transitions deserve special attention:

**EXPIRED -> DETECTED and CANCELLED -> DETECTED.** A customer may send payment
after the session has expired or been cancelled. The system still detects the
on-chain transfer because the chain monitor does not check session status -- it
checks addresses. The state machine allows these "late payment" transitions so
the funds are not lost. The engine tags these with a `late_payment` reason so
the tenant can be notified.

**DETECTED -> PENDING_PAYMENT.** This handles blockchain reorganizations. If a
transaction was detected but then reorged out (the block was replaced by a
longer fork that does not include the transaction), the session reverts to
PENDING_PAYMENT.

### The DepositSession Aggregate

The `DepositSession` struct in `domain/deposit.go` groups payment details
(chain, token, deposit address, expected/received amounts), fee details
(collection fee in basis points, calculated fee and net amounts), settlement
preference, HD wallet derivation index, and lifecycle timestamps (detected,
confirmed, credited, settled, expired, failed). See the full struct definition
in `domain/deposit.go`.

The `Version` field enables optimistic locking. Every state transition
increments the version and the store checks it during `TransitionWithOutbox`:

```go
// domain/deposit.go

func (s *DepositSession) TransitionTo(target DepositSessionStatus) error {
    if !s.CanTransitionTo(target) {
        return ErrInvalidTransition(string(s.Status), string(target))
    }
    s.Status = target
    s.Version++
    s.UpdatedAt = time.Now().UTC()
    return nil
}
```

If two workers try to advance the same session concurrently, one will fail with
`ErrOptimisticLock` and retry via NATS redelivery.

### Three Settlement Preferences

After a deposit is credited, the tenant has three options configured via
`SettlementPreference`:

| Preference     | Behavior                                         |
|----------------|--------------------------------------------------|
| `AUTO_CONVERT` | Convert crypto to fiat immediately (SETTLING)    |
| `HOLD`         | Keep as crypto balance (HELD -- terminal)        |
| `THRESHOLD`    | Accumulate until a threshold, then convert       |

The preference is set per-session or falls back to the tenant's default:

```go
// core/deposit/engine.go -- CreateSession

settlementPref := req.SettlementPref
if settlementPref == "" {
    settlementPref = tenant.CryptoConfig.DefaultSettlementPref
}
```

---

## 9.1.3 The Deposit Engine

The deposit engine (`core/deposit/engine.go`) follows the same pure-state-machine
pattern as the transfer engine from Chapter 3.2. Zero network calls. Every side
effect is an outbox entry. All mutations are atomic: state change plus outbox
entries in a single database transaction.

### Engine Structure

```go
// core/deposit/engine.go

type Engine struct {
    store       DepositStore
    tenantStore TenantStore
    logger      *slog.Logger
}
```

Two dependencies, both interfaces. The engine never touches the blockchain,
never talks to NATS, never calls the treasury directly. Compare this to the
transfer engine: the pattern is identical.

### CreateSession

`CreateSession` is the entry point for a new deposit. Let us trace the
validation sequence:

```go
// core/deposit/engine.go

func (e *Engine) CreateSession(ctx context.Context, tenantID uuid.UUID,
    req CreateSessionRequest) (*domain.DepositSession, error) {

    // a. Load tenant, verify active + crypto enabled
    tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
    // ...
    if !tenant.IsActive() {
        return nil, domain.ErrTenantSuspended(tenantID.String())
    }
    if !tenant.CryptoConfig.CryptoEnabled {
        return nil, domain.ErrCryptoDisabled(tenantID.String())
    }

    // b. Validate chain is supported
    if !tenant.ChainSupported(req.Chain) {
        return nil, domain.ErrChainNotSupported(string(req.Chain), tenantID.String())
    }

    // c. Validate amount > 0
    if !req.ExpectedAmount.IsPositive() {
        return nil, domain.ErrAmountTooLow(req.ExpectedAmount.String(), "0")
    }

    // d. Check per-tenant pending deposit session limit
    if tenant.MaxPendingTransfers > 0 {
        count, err := e.store.CountPendingSessions(ctx, tenantID)
        // ...
        if count >= tenant.MaxPendingTransfers {
            return nil, fmt.Errorf("... exceeded max pending sessions (%d)", ...)
        }
    }

    // e. Check idempotency
    if req.IdempotencyKey != "" {
        existing, err := e.store.GetSessionByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
        if err == nil && existing != nil {
            return existing, nil // return existing session
        }
    }

    // f. Dispense address from pool
    poolAddr, err := e.store.DispenseAddress(ctx, tenantID, string(req.Chain), uuid.Nil)
    // ...
```

Step (d) reuses the same `MaxPendingTransfers` limit as regular transfers --
this is the per-tenant resource limit from Critical Invariant 13. Step (e)
ensures idempotency: if the same request is sent twice, the same session is
returned. Step (f) dispenses a pre-generated address from the pool (more on
this in section 9.1.7).

After validation, the engine builds the session (status `PENDING_PAYMENT`,
version 1, deposit address from pool, fee BPS from tenant schedule, expiry from
TTL) and persists it atomically with two outbox entries via
`CreateSessionWithOutbox`:

Two outbox entries are written:

1. **IntentMonitorAddress** -- tells the chain monitor to start watching this
   address for incoming stablecoin transfers.
2. **EventDepositSessionCreated** -- notifies downstream consumers (webhooks,
   emails) that a new session exists.

> **Key Insight:** Notice that `CreateSession` does not start the blockchain
> monitor itself. It writes an *intent* to monitor the address, and the outbox
> relay delivers that intent to NATS, where the chain monitor picks it up.
> This is the same outbox pattern from Chapter 3.1, applied to a different
> domain.

### HandleTransactionDetected

When the chain monitor finds an on-chain payment (section 9.1.4 explains how),
it writes a `deposit.tx.detected` event. The deposit worker calls
`HandleTransactionDetected`:

```go
// core/deposit/engine.go

func (e *Engine) HandleTransactionDetected(ctx context.Context, tenantID,
    sessionID uuid.UUID, tx domain.IncomingTransaction) error {

    session, err := e.store.GetSession(ctx, tenantID, sessionID)
    // ...

    // Record the transaction (idempotent -- duplicate txHash is skipped)
    existing, err := e.store.GetDepositTxByHash(ctx, string(tx.Chain), tx.TxHash)
    if err == nil && existing != nil {
        return nil // already processed
    }

    // Atomically: record deposit tx + accumulate received amount
    if err := e.store.RecordDepositTx(ctx, depositTx, tenantID, sessionID, tx.Amount); err != nil {
        return fmt.Errorf("... recording tx and accumulating: %w", err)
    }

    // Check for late payment (session expired or cancelled)
    isLatePayment := session.Status == domain.DepositSessionStatusExpired ||
        session.Status == domain.DepositSessionStatusCancelled

    // Transition to DETECTED
    if !session.CanTransitionTo(domain.DepositSessionStatusDetected) {
        // Already DETECTED or further along -- just record the tx
        return nil
    }

    session.ReceivedAmount = session.ReceivedAmount.Add(tx.Amount)
    if err := session.TransitionTo(domain.DepositSessionStatusDetected); err != nil {
        return fmt.Errorf("settla-deposit: handle tx detected: %w", err)
    }

    // Build outbox entries (plus late_payment event if applicable)
    // ...
    if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
        return wrapTransitionError(err, "handle tx detected", sessionID)
    }
```

Three design decisions here:

1. **Idempotent transaction recording.** The same `txHash` can arrive multiple
   times (NATS redelivery, poller re-scan). The engine checks for duplicates
   before recording.

2. **Late payment handling.** If the session is EXPIRED or CANCELLED, the
   state machine still accepts the transition to DETECTED. The funds are on-chain
   and belong to someone -- ignoring them would be a bug.

3. **Graceful degradation for already-advanced sessions.** If the session is
   already DETECTED or further along (CONFIRMED, CREDITING, etc.), the engine
   records the transaction but does not try to transition -- it just returns nil.

### HandleTransactionConfirmed and the Credit Flow

Once enough block confirmations have accumulated, the chain confirms the
transaction. The engine then triggers the credit flow:

```
DETECTED  --HandleTransactionConfirmed-->  CONFIRMED
CONFIRMED --initiateCredit-->              CREDITING (emits IntentCreditDeposit)
CREDITING --HandleCreditResult(success)--> CREDITED
CREDITED  --routeAfterCredit-->            SETTLING or HELD
```

The credit calculation in `initiateCredit` is where fees are applied:

```go
// core/deposit/engine.go -- initiateCredit

// Load tenant for fee calculation
tenant, err := e.tenantStore.GetTenant(ctx, tenantID)

// Calculate collection fee
feeAmount := CalculateCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
netAmount := session.ReceivedAmount.Sub(feeAmount)

// Build credit intent
creditPayload, err := json.Marshal(domain.CreditDepositPayload{
    SessionID:      sessionID,
    TenantID:       tenantID,
    Chain:          session.Chain,
    Token:          session.Token,
    GrossAmount:    session.ReceivedAmount,
    FeeAmount:      feeAmount,
    NetAmount:      netAmount,
    TxHash:         txHash,
    IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("deposit-credit:%s", sessionID)),
})

entries := []domain.OutboxEntry{
    domain.MustNewOutboxIntent("deposit", sessionID, tenantID,
        domain.IntentCreditDeposit, creditPayload),
}
```

The `IntentCreditDeposit` is consumed by the deposit worker, which calls the
treasury manager to credit the tenant's position. The idempotency key
`deposit-credit:{sessionID}` ensures the credit happens exactly once even
under NATS redelivery.

### Route After Credit

After the tenant is credited, `routeAfterCredit` checks `session.SettlementPref`.
For `AUTO_CONVERT`, `initiateSettlement` writes an `IntentSettleDeposit` outbox
entry to trigger fiat conversion (CREDITED -> SETTLING -> SETTLED). For `HOLD`
or `THRESHOLD`, `holdSession` transitions directly to the HELD terminal state.

---

## 9.1.4 Chain Monitors -- How Settla Detects On-Chain Payments

The chain monitors are the eyes of the deposit system. They continuously poll
blockchain RPCs, looking for stablecoin transfers to watched deposit addresses.
When they find one, they write a deposit transaction record and an outbox entry
atomically.

### Architecture

```
Chain Monitors
===============

    +------------------+                    +------------------+
    |   EVM Poller     | -- eth_getLogs --> |  Ethereum / Base |
    |  (evm_poller.go) |                    |   RPC Node       |
    +------------------+                    +------------------+
           |
           |  match: to_address in watched_set
           |
           v
    +--------------------+          +-------------------+
    |  OutboxWriter      | -------> | Transfer DB       |
    | (atomic write)     |          | - deposit_txs     |
    +--------------------+          | - outbox entries   |
           |                        +-------------------+
           |
    +------------------+                    +------------------+
    |  Tron Poller     | -- TRC20 API ---> |   TronGrid       |
    | (tron_poller.go) |                    |                  |
    +------------------+                    +------------------+
           |
           |  match: to_address in watched_set
           |
           v
    +--------------------+
    |  Outbox Relay      | -----> NATS: SETTLA_CRYPTO_DEPOSITS
    +--------------------+
           |
           v
    +--------------------+
    |  DepositWorker     | -----> Engine.HandleTransactionDetected
    +--------------------+
```

### The Poll Cycle

Both pollers follow a four-step algorithm (shown for the EVM poller from
`node/chainmonitor/evm_poller.go`):

```go
// node/chainmonitor/evm_poller.go -- Poll (simplified)

func (p *EVMPoller) Poll(ctx context.Context) error {
    // 1. Load checkpoint (last scanned block + hash)
    lastBlock, lastHash, err := p.checkpoint.Load(ctx, p.chain)

    // 2. Calculate safe block (current - confirmations)
    currentBlock, _ := p.client.GetLatestBlockNumber(ctx)
    safeBlock := currentBlock - int64(p.cfg.Confirmations)

    // Re-scan reorgDepth blocks for safety
    startBlock := lastBlock + 1
    if lastBlock > 0 && p.cfg.ReorgDepth > 0 {
        reorgStart := lastBlock - int64(p.cfg.ReorgDepth)
        if reorgStart > 0 && reorgStart < startBlock {
            startBlock = reorgStart
        }
    }

    // 3. Scan for ERC-20 transfers to watched addresses
    processed, _ := p.scanTransfers(ctx, addrSnap, startBlock, safeBlock)

    // 4. Save checkpoint at the safe block
    p.checkpoint.Save(ctx, p.chain, safeBlock, block.Hash)
}
```

**Step 1** resumes from the last saved position after crashes. **Step 2**
only scans confirmed blocks (below `currentBlock - confirmations`). **Step 3**
uses `eth_getLogs` to batch-query all watched addresses in one RPC call.
**Step 4** advances the checkpoint so the next cycle starts correctly.

### EVM vs Tron Scanning

The EVM poller uses `eth_getLogs` to batch-query for all ERC-20 Transfer events
to any watched address in the block range. A single RPC call covers all
addresses and contracts, making it efficient even with hundreds of watched
addresses. Address matching uses case-insensitive comparison because Ethereum
addresses use mixed-case EIP-55 checksums.

The Tron poller uses TronGrid's TRC-20 transfer API, which is per-address
rather than per-block. It approximates timestamps from block numbers (Tron
~3 seconds per block) because TronGrid accepts time ranges rather than block
ranges.

Both pollers iterate over every matched transfer and call `processIncomingTx`.

### Processing an Incoming Transaction

Both pollers use identical logic in `processIncomingTx`:

1. **Idempotency check:** Look up `txHash` in the database. If already recorded, skip.
2. **Session lookup:** Find the active session for the `toAddress`.
3. **Token verification:** Confirm the contract address is a watched token.
4. **Atomic write:** Insert the `DepositTransaction` record and an outbox entry
   (`EventDepositTxDetected`) in a single database transaction via `OutboxWriter.WriteDetectedTx`.

The `OutboxWriter` interface (defined in `node/chainmonitor/tron_poller.go`)
encapsulates this atomic write:

```go
// node/chainmonitor/tron_poller.go

type OutboxWriter interface {
    WriteDetectedTx(ctx context.Context, dtx *domain.DepositTransaction,
        entries []domain.OutboxEntry) error
    GetDepositTxByHash(ctx context.Context, chain, txHash string) (*domain.DepositTransaction, error)
    GetSessionByAddress(ctx context.Context, address string) (*domain.DepositSession, error)
}
```

> **Key Insight:** The chain monitor is *not* part of the deposit engine. It is
> a separate component that writes directly to the database (deposit
> transactions table + outbox). The engine processes the resulting events. This
> separation means the chain monitor can be scaled independently from the
> engine, and the engine never needs to know about RPC endpoints or blockchain
> protocols.

---

## 9.1.5 Confirmation Depth and Reorg Risk

### What Is a Blockchain Reorganization?

A blockchain is a linked list of blocks, where each block references the hash
of its parent. When two miners produce valid blocks at the same height, the
chain temporarily forks:

```
Blockchain Reorganization
==========================

Normal chain:
    Block 99 --> Block 100 --> Block 101 --> Block 102
                    ^
                    |
                 Checkpoint

Fork occurs (two miners produce block 100 simultaneously):

    Block 99 --> Block 100a --> Block 101a
                   \
                    Block 100b --> Block 101b --> Block 102b

Block 100b's fork becomes longer, so all nodes switch to it.
Block 100a and 101a are "reorged out" -- their transactions are reverted.

If you credited a deposit based on a transaction in Block 100a,
that deposit NO LONGER EXISTS after the reorg.
```

This is not theoretical. Tron reorgs happen regularly at depth 1-3. Ethereum
reorgs are rarer after the merge to proof-of-stake but not impossible.

### Confirmation Requirements Per Chain

Settla waits for different confirmation depths per chain, configured in the
engine's `requiredConfirmations` method:

```go
// core/deposit/engine.go

func (e *Engine) requiredConfirmations(session *domain.DepositSession) int32 {
    switch session.Chain {
    case domain.ChainTron:
        return 19
    case domain.ChainEthereum:
        return 12
    case domain.ChainBase:
        return 12
    case domain.ChainPolygon:
        return 12
    case domain.ChainSolana:
        return 12
    default:
        return 20
    }
}
```

The poller calculates `safeBlock = currentBlock - confirmations`. The chain
config (`node/chainmonitor/config.go`) also specifies `ReorgDepth` (blocks to
re-scan per cycle) and `PollInterval`. The confirmation-to-wait-time mapping:

| Chain    | Confirmations | Block time | Wait time  |
|----------|--------------|------------|------------|
| Tron     | 19           | ~3s        | ~57s       |
| Ethereum | 12           | ~12s       | ~144s      |
| Base     | 12           | ~2s        | ~24s       |

### Reorg Safety: Re-Scanning

The pollers re-scan the last `ReorgDepth` blocks on every cycle by setting
`startBlock = lastBlock - ReorgDepth`. This detects transactions that
disappeared from the canonical chain. The pollers also verify the parent hash
chain via `detectReorg`: if the block after the last checkpoint has a different
parent hash than expected, a reorg has occurred and a warning is logged.
Currently this is observational; a production system would re-scan affected
blocks and revert DETECTED sessions back to PENDING_PAYMENT.

> **Key Insight:** Confirmation depth is a tradeoff between speed and safety.
> Too few confirmations and you risk crediting funds from a reorged block. Too
> many and the customer waits too long. The numbers (19 for Tron, 12 for
> Ethereum) are industry standard thresholds that balance these concerns.

---

## 9.1.6 The Deposit Worker

The deposit worker consumes events from the `SETTLA_CRYPTO_DEPOSITS` stream
and routes them to the deposit engine. It lives in
`node/worker/deposit_worker.go`.

### Worker Structure

```go
// node/worker/deposit_worker.go

type DepositWorker struct {
    partition  int
    engine     DepositEngine
    treasury   domain.TreasuryManager
    subscriber *messaging.StreamSubscriber
    logger     *slog.Logger
}
```

The worker holds both the engine (for state transitions) and the treasury
manager (for crediting balances). It subscribes to a NATS partition:

```go
func (w *DepositWorker) Start(ctx context.Context) error {
    filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixDeposit, w.partition)
    return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}
```

### Event Routing

The worker routes four event types:

```go
// node/worker/deposit_worker.go

func (w *DepositWorker) handleEvent(ctx context.Context, event domain.Event) error {
    switch event.Type {
    case domain.EventDepositTxDetected:
        return w.handleTxDetected(ctx, event)
    case domain.EventDepositTxConfirmed:
        return w.handleTxConfirmed(ctx, event)
    case domain.IntentCreditDeposit:
        return w.handleCreditResult(ctx, event)
    case domain.IntentSettleDeposit:
        return w.handleSettlementResult(ctx, event)
    default:
        return nil // ACK unknown events
    }
}
```

### Treasury Credit

The `handleCreditResult` handler is where money actually moves. It derives the
treasury position location from chain and token (`crypto:tron:usdt` for a USDT
deposit on Tron), then credits the tenant's position:

```go
// node/worker/deposit_worker.go -- handleCreditResult (simplified)

location := fmt.Sprintf("crypto:%s:%s",
    strings.ToLower(string(payload.Chain)),
    strings.ToLower(payload.Token))

err = w.treasury.CreditBalance(ctx, payload.TenantID, currency,
    location, payload.NetAmount, payload.SessionID, "deposit_session")
```

The worker then calls `engine.HandleCreditResult` with the result. On success,
the state machine advances to CREDITED and routes based on settlement preference.
On failure, it transitions to FAILED.

---

## 9.1.7 Address Pool Management

### Why Pre-Generate Addresses?

Every deposit session needs a unique blockchain address. Generating an address
requires HD wallet key derivation (BIP-44), which involves cryptographic
operations. If you derive an address during `CreateSession`, you add latency
and create a dependency on the key management system being available at request
time.

Settla pre-generates addresses and stores them in a pool. `CreateSession`
simply dispenses the next available address, which is a fast database read.

### HD Wallet Derivation

Addresses are derived using BIP-44 paths per chain:

```go
// rail/wallet/derivation.go -- DeriveWallet

// Uses BIP-44 paths:
// - Tron:           m/44'/195'/0'/0/{index}
// - Solana:         m/44'/501'/0'/0/{index}
// - Ethereum/Base:  m/44'/60'/0'/0/{index}
```

Each address corresponds to a unique derivation index. The private key is
derived deterministically from the master seed, meaning Settla can always
reconstruct the key for any address using just the index.

### The Address Pool

The pool is a table of pre-generated addresses:

```go
// domain/deposit.go

type CryptoAddressPool struct {
    ID              uuid.UUID
    TenantID        uuid.UUID
    Chain           CryptoChain
    Address         string
    DerivationIndex int64
    Dispensed       bool
    DispensedAt     *time.Time
    SessionID       *uuid.UUID
    CreatedAt       time.Time
}
```

### Dispensing an Address

When `CreateSession` needs an address, it calls `DispenseAddress`:

```go
// core/deposit/store.go -- DepositStore interface

// DispenseAddress obtains a deposit address from the pre-generated pool.
// Uses SKIP LOCKED to avoid contention under concurrent session creation.
DispenseAddress(ctx context.Context, tenantID uuid.UUID, chain string,
    sessionID uuid.UUID) (*domain.CryptoAddressPool, error)
```

The `SKIP LOCKED` hint is critical for concurrency. If two `CreateSession`
calls arrive simultaneously, they must not receive the same address. `SKIP
LOCKED` causes the database to skip rows locked by other transactions rather
than waiting -- each session gets a different address with no contention.

### Keeping the Pool Topped Up

The `VirtualAccountProvisioner` (`node/worker/virtual_account_provisioner.go`)
runs as a background goroutine in settla-node. It periodically checks pool
levels per tenant and chain with a low watermark (default: 10), generating new
addresses when the count drops below it. This ensures `CreateSession` always
has an address available.

> **Key Insight:** Address dispensing uses `SKIP LOCKED`, not `SELECT FOR
> UPDATE`. This is the same pattern used in the outbox relay (Chapter 5.2).
> Under concurrent load, `SELECT FOR UPDATE` serializes all transactions.
> `SKIP LOCKED` gives each transaction a different row with zero contention.

---

## 9.1.8 The Token Registry

The chain monitors need to know which tokens to watch and how to parse their
on-chain amounts. The `TokenRegistry` provides this lookup using a lock-free
atomic copy-on-write pattern:

```go
// node/chainmonitor/token_registry.go

type TokenRegistry struct {
    data atomic.Pointer[tokenMap]
}

type tokenMap struct {
    byContract map[string]domain.Token // key: "chain:contractAddress" (lowercased)
    byChain    map[string][]domain.Token
}
```

The registry is loaded from the database and reloaded periodically:

```go
func (r *TokenRegistry) Reload(tokens []domain.Token) {
    m := &tokenMap{
        byContract: make(map[string]domain.Token, len(tokens)),
        byChain:    make(map[string][]domain.Token),
    }
    for _, t := range tokens {
        if !t.IsActive {
            continue
        }
        key := tokenKey(string(t.Chain), t.ContractAddress)
        m.byContract[key] = t
        m.byChain[string(t.Chain)] = append(m.byChain[string(t.Chain)], t)
    }
    r.data.Store(m) // atomic pointer swap
}
```

`Reload` builds a completely new map and swaps the pointer atomically. Readers
never block, there is no mutex on the hot path, and a single `data.Load()` gives
a consistent snapshot even if `Reload` is called mid-cycle.

Each `domain.Token` carries a `Decimals` field (6 for USDT/USDC) that tells the
poller how to parse raw on-chain integer amounts. Getting this wrong is
catastrophic: a raw value of `500000000` means 500 USDT with `Decimals=6` but
500,000,000 USDT with `Decimals=0` -- a nine-order-of-magnitude error.

---

## 9.1.9 Putting It All Together

The complete flow for a 500 USDT deposit on Tron with 40 bps collection fee:

```
Complete Crypto Deposit Flow
==============================

 1. POST /v1/deposits --> Engine.CreateSession
    Writes session (PENDING_PAYMENT) + IntentMonitorAddress + outbox

 2. Outbox Relay --> NATS --> Chain monitor adds address to watch set

 3. Customer sends 500 USDT to deposit address on-chain

 4. Tron Poller detects transfer, writes deposit_tx + outbox atomically

 5. DepositWorker --> Engine.HandleTransactionDetected
    PENDING_PAYMENT --> DETECTED

 6. (~57 seconds, 19 confirmations)
    DepositWorker --> Engine.HandleTransactionConfirmed
    DETECTED --> CONFIRMED --> CREDITING (emits IntentCreditDeposit)
    Fee: 500 * 40/10000 = 2.00 USDT, Net: 498.00 USDT

 7. DepositWorker credits treasury (crypto:tron:usdt += 498)
    --> Engine.HandleCreditResult
    CREDITING --> CREDITED --> routeAfterCredit

 8. AUTO_CONVERT: CREDITED --> SETTLING --> SETTLED
    HOLD:         CREDITED --> HELD (terminal)
```

---

## Common Mistakes

### Mistake 1: Crediting Before Sufficient Confirmations

"The transaction is in a block, so it is confirmed."

No. A transaction in a block at the chain tip could be reorged out. You must
wait for the configured confirmation depth (19 blocks on Tron, 12 on Ethereum)
before considering the deposit final. The pollers enforce this by only scanning
up to `currentBlock - confirmations`.

### Mistake 2: Using Float for On-Chain Amounts

On-chain amounts are integers (e.g., `500000000` for 500 USDT with 6 decimals).
Parsing them as float64 introduces rounding errors. Use `shopspring/decimal`.

### Mistake 3: Ignoring Late Payments

The chain monitor watches *addresses*, not session status. If you only process
payments for active sessions, you lose funds sent after expiry. The state
machine allows `EXPIRED -> DETECTED` and `CANCELLED -> DETECTED` for this.

### Mistake 4: Using SELECT FOR UPDATE for Address Dispensing

`SELECT FOR UPDATE` serializes concurrent dispensing. `SKIP LOCKED` gives each
transaction a different row with zero contention.

### Mistake 5: Calling External Systems from the Engine

All side effects must be outbox entries. Direct calls reintroduce the dual-write
problem (Chapter 3.1).

---

## Exercises

### Exercise 1: Trace a Complete Deposit

Trace a complete crypto deposit from a customer sending 1,000 USDT on Tron to
the merchant's position being credited. For each step, identify:

- Which component executes the step (engine, poller, worker, relay)
- What database writes happen
- What outbox entries are produced
- What NATS stream/subject carries the message

Assume the tenant uses `AUTO_CONVERT` settlement preference and has a 40 bps
collection fee. What is the final net amount credited?

### Exercise 2: Checkpoint Recovery

The EVM poller for Base crashes after processing block 50,000 but before saving
the checkpoint. When it restarts:

1. What block number does `checkpoint.Load` return?
2. What is `startBlock` if `ReorgDepth` is 5?
3. Will any transactions be processed twice? If so, what prevents double-crediting?
4. What is the maximum number of blocks that could be re-scanned?

### Exercise 3: Expired Session + Late Payment

A tenant creates a deposit session with a 1-hour TTL. The customer does not
send payment within the hour, and the session transitions to EXPIRED. Two hours
later, the customer sends USDT to the deposit address.

1. Will the chain monitor detect this payment? Why or why not?
2. What state transition occurs on the session?
3. How is the tenant notified that this is a late payment?
4. What would happen if the state machine did NOT allow `EXPIRED -> DETECTED`?

### Exercise 4: Design a Reorg Handler

Currently, `detectReorg` only logs a warning. Design a handler that detects
which deposit transactions were in reorged blocks, reverts affected sessions,
and handles the case where a session has already been CREDITED. What state
transitions and outbox events would you need to add?

### Exercise 5: Token Decimal Precision

DAI uses 18 decimal places on Ethereum (unlike USDT/USDC which use 6). If a
customer sends 500 DAI and the `Token.Decimals` field is wrong (set to 6),
how much would be incorrectly credited? Where would you verify decimals?

---

## Summary

The crypto deposit system inverts the control flow of outbound transfers.
Instead of initiating actions, it watches for external events on the blockchain
and reacts to them. The key architectural decisions are:

1. **Pure state machine engine** -- no network calls, only outbox entries. The
   same pattern as the transfer engine, applied to a different domain.

2. **Checkpoint-based chain scanning** -- pollers track their last processed
   block and resume from there after restarts.

3. **Confirmation depth** -- transactions are only considered final after N
   block confirmations, preventing loss from blockchain reorganizations.

4. **Pre-generated address pools** -- `SKIP LOCKED` dispensing gives zero-contention
   address assignment under concurrent load.

5. **Lock-free token registry** -- atomic pointer swap gives consistent reads
   without mutex overhead on the hot path.

6. **Late payment recovery** -- the state machine accepts payments even after
   expiry or cancellation, because on-chain funds must never be lost.

In the next chapter, we will examine bank deposits -- the fiat equivalent of
this flow, where virtual bank accounts replace blockchain addresses and partner
banks replace chain monitors.
