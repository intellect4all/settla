# ADR-001: Modular Monolith Architecture

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla is a B2B settlement platform targeting 50M transactions/day (~580 TPS sustained, 5,000 TPS peak). The system has clear bounded contexts — ledger, settlement engine, payment routing, treasury, and async event processing — each with distinct data ownership and scaling characteristics.

We needed to decide between:

1. **Microservices from day one** — separate deployables per bounded context
2. **Traditional monolith** — single codebase with no internal boundaries
3. **Modular monolith** — single binary with strict module boundaries and interface-based wiring

## Decision

We chose a **modular monolith**: a single Go binary (`settla-server`) that composes independent modules (Core, Ledger, Rail, Treasury) through domain interfaces defined in `domain/interfaces.go`.

Each module:
- Depends only on interfaces from the `domain` package, never on concrete sibling modules
- Owns its own database schema (separate Postgres databases per bounded context)
- Communicates async events through `domain.EventPublisher` (backed by NATS JetStream)
- Can be tested in isolation by mocking its interface dependencies

The wiring happens in `cmd/settla-server/main.go`:

```go
ledgerSvc   := ledger.NewService(ledgerStore, publisher, logger)
treasurySvc := treasury.NewManager(treasuryStore, publisher, logger)
railRouter  := router.NewRouter(treasurySvc, gasOracle, providerReg, logger)
engine      := core.NewEngine(transferStore, ledgerSvc, treasurySvc, railRouter, providerReg, publisher, logger)
```

Today these are local struct pointers. The engine calls `ledgerSvc.PostEntry(...)` as a function call — zero serialization, zero network hop. But because `ledgerSvc` satisfies `domain.LedgerService` (an interface), it could be replaced with a gRPC client that talks to a remote ledger service. The engine never knows.

## Extraction Points

The architecture has four clean extraction seams. Each can be split independently:

| Module | Interface | When to Extract |
|--------|-----------|-----------------|
| Ledger | `domain.LedgerService` | Ledger write throughput exceeds what a single Postgres instance can handle (>500 TPS sustained), or the ledger team needs independent deploy cycles |
| Treasury | `domain.TreasuryManager` | Treasury needs real-time position sync across multiple regions, or regulatory requirements demand isolated deployment |
| Rail | `domain.Router` + `domain.ProviderRegistry` | Provider integrations multiply and need independent scaling/deploy (e.g., different SLAs per provider), or blockchain node management requires dedicated infrastructure |
| Node | Already separate | `settla-node` is already a separate binary; it communicates via NATS JetStream events only |

### What extraction looks like

1. Create a new `cmd/settla-ledger/main.go` that hosts the ledger service behind a gRPC server
2. Write a `ledger.NewClient(conn)` that implements `domain.LedgerService` over gRPC
3. In `settla-server/main.go`, replace `ledger.NewService(...)` with `ledger.NewClient(conn)`
4. No other code changes — Core, Treasury, and Rail continue calling the same interface

### What would trigger extraction

- **Performance**: a single module's load requires independent horizontal scaling
- **Team boundaries**: a module has a dedicated team that needs independent deploy cadence
- **Compliance**: a module handles data that must be physically isolated (e.g., ledger in a regulated jurisdiction)
- **Operational**: a module's failure blast radius needs to be contained (e.g., a provider integration crashing shouldn't take down the ledger)

## Consequences

### Benefits
- **Development velocity**: one repo, one build, one deploy — no distributed system complexity on day one
- **Refactoring safety**: module boundaries are enforced by Go's type system (interface satisfaction checked at compile time), not by hoping everyone follows conventions
- **Testing simplicity**: integration tests compose real modules in-process without Docker, service discovery, or network flakiness
- **Debuggability**: a single stack trace, a single log stream, standard Go profiling tools
- **Low-latency internal calls**: inter-module calls are function calls (~nanoseconds), not gRPC calls (~milliseconds)

### Trade-offs
- **Coupled deployments**: all Go modules ship in the same binary; a change to Ledger requires redeploying Core too. Acceptable while team is <10 engineers.
- **Shared failure domain**: a panic in Rail can crash the Ledger. Mitigated by panic recovery middleware and watchdog restarts. Acceptable until provider integrations become unpredictable.
- **Discipline required**: developers must resist the temptation to reach across module boundaries via direct struct access. The `domain` interfaces enforce this at the type level, but someone could import a sibling package's internals. Code review and linting rules are the backstop.

### Mitigations
- **Separate databases from day one**: each bounded context owns its own Postgres database. This is the hardest thing to untangle later, so we pay this cost upfront.
- **Async events from day one**: modules communicate state changes via NATS events, not synchronous calls where possible. This means the event infrastructure already exists when extraction happens.
- **`settla-node` is already separate**: the worker process demonstrates the extraction pattern and validates that our event-driven architecture works across process boundaries.

## References

- [Modular Monolith with DDD](https://github.com/kgrzybek/modular-monolith-with-ddd) — Kamil Grzybek
- [MonolithFirst](https://martinfowler.com/bliki/MonolithFirst.html) — Martin Fowler
