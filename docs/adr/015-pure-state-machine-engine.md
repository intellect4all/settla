# ADR-015: Separate State Transitions from Side Effects

**Status:** Accepted
**Date:** 2026-03-09
**Authors:** Engineering Team

## Context

The settlement engine is the core orchestrator of Settla's transfer lifecycle. Each transfer progresses through states (initiated, quoted, funded, reserved, submitted, settled, completed) with distinct side effects at each transition: ledger postings, treasury reservations, provider API calls, blockchain transactions, webhook dispatches.

The original engine design executed side effects inline:

```go
func (e *Engine) HandleQuoteAccepted(ctx context.Context, transferID uuid.UUID) error {
    transfer := e.store.GetTransfer(ctx, transferID)
    // 1. Reserve treasury position (side effect)
    err := e.treasury.Reserve(ctx, transfer.TenantID, transfer.Amount)
    // 2. Post ledger entry (side effect)
    err = e.ledger.PostEntry(ctx, journalEntry)
    // 3. Call provider API (side effect)
    result, err := e.provider.Submit(ctx, transfer)
    // 4. Update transfer status
    err = e.store.UpdateTransfer(ctx, transferID, newStatus)
    // 5. Publish event (side effect)
    err = e.publisher.Publish(ctx, event)
    return nil
}
```

This design has compounding problems at scale:

1. **Partial failure states**: if the provider call succeeds but the ledger posting fails, the transfer is in an inconsistent state. The engine must implement complex compensation logic for every possible failure combination. With 5 side effects per transition, there are 2^5 = 32 possible failure permutations.

2. **Tight coupling**: the engine directly depends on treasury, ledger, provider, and publisher implementations. A provider API timeout (common: 5-30 seconds) blocks the engine goroutine, reducing throughput. At 580 TPS with 5% of calls timing out at 10s, that is 29 goroutines blocked at any given time — a meaningful fraction of the connection pool.

3. **Testing complexity**: testing a state transition requires mocking 4-5 dependencies and verifying complex interaction sequences. Each test exercises both the state machine logic AND the side effect execution.

4. **Inconsistent retry semantics**: some side effects are idempotent (ledger postings with idempotency keys), some are not (certain provider API calls). The engine must know the retry semantics of every dependency it calls.

## Decision

We chose to make the **engine a pure state machine** that writes **intents** (outbox entries) instead of executing side effects. Dedicated workers consume these intents and execute side effects independently.

### Engine Contract

The engine's methods now follow a strict pattern:

```go
func (e *Engine) HandleQuoteAccepted(ctx context.Context, transferID uuid.UUID) error {
    return e.store.WithTransaction(ctx, func(tx store.Tx) error {
        transfer, err := tx.GetTransferForUpdate(ctx, transferID)
        if err != nil { return err }

        if !transfer.Status.CanTransitionTo(domain.StatusReserved) {
            return domain.ErrInvalidTransition
        }

        // Write the state change
        err = tx.UpdateTransferStatus(ctx, transferID, domain.StatusReserved)
        if err != nil { return err }

        // Write intents (outbox entries) for side effects
        err = tx.InsertOutboxEntry(ctx, OutboxEntry{
            EventType: "treasury.reserve_requested",
            Payload:   reservePayload,
        })
        err = tx.InsertOutboxEntry(ctx, OutboxEntry{
            EventType: "ledger.posting_requested",
            Payload:   postingPayload,
        })
        return nil
    })
}
```

**Key properties:**
- The engine method is a single database transaction: UPDATE + INSERT(s) + COMMIT
- No network calls, no external dependencies, no blocking I/O
- Execution time is <1ms (just Postgres round-trip)
- The method either fully succeeds or fully rolls back — no partial states
- Side effects are expressed as data (outbox entries), not as executed code

### Worker Architecture

Dedicated workers consume outbox events from NATS and execute side effects:

| Worker | Consumes | Executes | Reports Back |
|--------|----------|----------|-------------|
| Treasury Worker | `treasury.reserve_requested` | `treasury.Reserve()` | `treasury.reserve_completed` or `treasury.reserve_failed` |
| Ledger Worker | `ledger.posting_requested` | `ledger.PostEntry()` | `ledger.posting_completed` or `ledger.posting_failed` |
| Provider Worker | `provider.submit_requested` | Provider API call | `provider.submit_completed` or `provider.submit_failed` |
| Blockchain Worker | `blockchain.tx_requested` | Blockchain submission | `blockchain.tx_confirmed` or `blockchain.tx_failed` |
| Webhook Worker | `webhook.dispatch_requested` | HTTP POST to tenant endpoint | `webhook.dispatch_completed` or `webhook.dispatch_failed` |

Each worker follows the same pattern:

1. Receive intent event from NATS
2. Execute the side effect (with retries, timeouts, circuit breakers as appropriate)
3. Write the result back to the engine via a `Handle*Result()` call
4. The engine's `Handle*Result()` method is itself a pure state transition that may produce further outbox entries

This creates an **event-driven saga** where the transfer progresses through its lifecycle via a chain of state transitions and side effect executions, all mediated by the outbox and NATS.

### Transfer Lifecycle Example

```
Engine.InitiateTransfer()
  → outbox: transfer.initiated
  → Router Worker: compute quote
  → Engine.HandleQuoteResult()
    → outbox: treasury.reserve_requested
    → Treasury Worker: reserve position
    → Engine.HandleReserveResult()
      → outbox: provider.submit_requested
      → Provider Worker: submit to provider
      → Engine.HandleProviderResult()
        → outbox: ledger.posting_requested, webhook.dispatch_requested
        → (parallel) Ledger Worker + Webhook Worker
        → Engine.HandleSettlementComplete()
```

## Consequences

### Benefits
- **No partial failure states**: each engine method is a single atomic transaction. Either the state changes and all intents are written, or nothing changes. There are zero intermediate states where some side effects executed but others did not.
- **Sub-millisecond engine methods**: with no network calls or external dependencies, engine methods complete in <1ms. This means the engine can handle 5,000+ state transitions per second on a single instance, well above the 580 TPS sustained requirement.
- **Trivial testing**: testing a state transition means: (1) set up initial transfer state, (2) call the engine method, (3) assert the new transfer status, (4) assert the outbox entries. No mocking of treasury, ledger, or provider dependencies required.
- **Independent worker scaling**: if provider API calls are slow (e.g., during a provider degradation), the Provider Worker queue grows but the engine is unaffected. More Provider Worker instances can be added without touching the engine.
- **Clear failure boundaries**: if a side effect fails, the worker reports failure. The engine's `Handle*Failed()` method decides the next state (retry, compensate, or fail the transfer). All failure handling is explicit state machine logic, not exception handling buried in side effect code.
- **Natural observability**: the outbox table is a complete log of every intent the engine has produced. Combined with worker result events, this provides a full audit trail of every transfer's lifecycle.

### Trade-offs
- **More moving parts**: instead of one method that does everything, there are now N workers, N event types, and N result handlers. The total codebase is larger, and the flow is distributed across multiple files.
- **Higher end-to-end latency**: a transfer that previously completed in one synchronous call now takes multiple hops through NATS (50ms relay + network per hop). A 6-step transfer lifecycle adds ~300-600ms of infrastructure latency. This is acceptable for settlement (seconds-to-minutes), but the latency budget must be tracked.
- **Saga complexity**: multi-step workflows require careful handling of compensation (e.g., if provider submission fails after treasury reservation, the reservation must be released). The state machine must model failure paths explicitly.
- **Debugging distributed flows**: tracing a transfer through multiple workers requires correlation IDs and distributed tracing. A single-method approach had one stack trace; the saga approach has N separate execution contexts.

### Mitigations
- **Correlation via transfer_id**: every outbox entry and worker result includes the `transfer_id`, enabling end-to-end tracing through structured logs and distributed tracing (OpenTelemetry).
- **State machine diagram as documentation**: the valid transitions and their associated intents are documented in `domain/state_machine.go`, providing a single source of truth for the entire lifecycle.
- **Compensation is explicit**: failed side effects trigger `Handle*Failed()` methods that produce compensating outbox entries (e.g., `treasury.release_requested`). Compensation is not hidden in try/catch blocks — it is a first-class state transition.
- **Worker health dashboards**: per-worker metrics (processing rate, error rate, queue depth) are exposed via Prometheus, enabling rapid identification of bottlenecks.

## Threshold Triggers for Revisiting

- **End-to-end transfer latency exceeds SLA**: if the cumulative saga hop latency causes transfers to exceed the agreed SLA (currently 30 seconds for standard corridors). Migration path: reduce relay polling interval, or allow certain low-risk side effects to execute inline.
- **Operational complexity exceeds team capacity**: if the team cannot effectively debug and operate the distributed saga flow. Migration path: consolidate workers or introduce a saga orchestrator service that provides centralized visibility.
- **Worker queue depth consistently >10,000**: indicates workers cannot keep up with engine output. Migration path: increase worker instance count, optimize side effect execution, or implement backpressure.

## References

- [Saga Pattern](https://microservices.io/patterns/data/saga.html) — Chris Richardson
- [Domain Events + Event Sourcing](https://martinfowler.com/eaaDev/DomainEvent.html) — Martin Fowler
- ADR-014 (Transactional Outbox) — the mechanism that makes atomic intent writing possible
- ADR-005 (NATS Partitioned Events) — the transport layer between engine and workers
