# Chapter 1.6: The Modular Monolith Pattern

**Estimated reading time:** 25 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Articulate why a modular monolith outperforms microservices for settlement infrastructure
2. Draw the dependency graph and verify no circular imports
3. Explain the interface convention hierarchy (domain.Router vs core.Router vs router.Router)
4. Describe the module extraction path -- how any module becomes a gRPC service with one constructor swap
5. Navigate the `store/` adapter pattern that bridges SQLC-generated code to domain interfaces

---

## Why Not Microservices?

The default answer for scaling systems in 2026 is "use microservices." For settlement infrastructure, this is wrong. Here is why.

### The Concrete Comparison

| Concern | Modular Monolith (Settla) | Microservices |
|---------|---------------------------|---------------|
| **Deployment** | One binary, one deploy | 24+ services, 24+ deploys, 24+ rollback procedures |
| **Debugging** | Stack traces work end-to-end | Distributed tracing required (Jaeger/Zipkin) |
| **Function call latency** | ~1-10ns (in-process) | ~1-5ms (network round-trip, 100,000x slower) |
| **Transactions** | Single DB transaction (ACID) | Saga pattern or 2PC (complex, partial-failure-prone) |
| **Consistency** | Strong consistency by default | Eventual consistency by default |
| **Team size** | 1 team manages all modules | 1 team per service (minimum 7+ teams) |
| **Operational cost** | 2 binaries (server + node) | 24+ containers, service mesh, API gateway |
| **Schema changes** | One migration per DB | Coordinated schema migrations across services |

### The Settlement-Specific Problem

Consider the core engine's workflow for creating a transfer:

```
    1. Validate the request (domain types)
    2. Get a quote from the router (routing logic)
    3. Check tenant limits (tenant store)
    4. Create the transfer record (transfer store)
    5. Write outbox entries atomically (outbox table)
```

Steps 4 and 5 MUST be a single database transaction. If the transfer record is created but the outbox entries fail, the transfer is stuck -- no worker will pick it up. If the outbox entries succeed but the transfer record fails, workers will try to process a non-existent transfer.

With microservices, the "transfer service" and "outbox service" would need a distributed transaction. This is exactly the dual-write problem that the outbox pattern was designed to solve. Using microservices here would re-introduce the problem that the architecture was built to avoid.

> **Key Insight:** Settla handles 50M transfers/day from a single binary type (6+ replicas of `settla-server`, 8+ instances of `settla-node`). The modular monolith gives all the benefits of loose coupling (independent development, clean interfaces, testability) without the operational cost of distributed systems. The modules can be extracted to services later if organizational scaling demands it -- but the architecture does not force premature distribution.

---

## The Dependency Rule

The single most important rule in the codebase:

> Modules depend on `domain/` interfaces, never on sibling packages directly.

```
    SETTLA DEPENDENCY GRAPH
    =======================

              +---------------+
              |   domain/     |  Zero external deps (stdlib + decimal + uuid)
              | (interfaces,  |  Every other package depends on this.
              |  types,       |  This depends on nothing.
              |  errors)      |
              +-------+-------+
                      |
       +--------------+---------------+--------------+
       |              |               |              |
       v              v               v              v
    +-----------+  +--------+  +----------+  +---------+
    |   core/   |  |ledger/ |  |treasury/ |  |  rail/  |
    | engine    |  | CQRS   |  | in-mem   |  | router  |
    | deposit   |  | TB+PG  |  | atomic   |  | provs   |
    | bankdep   |  |        |  | flush    |  | chain   |
    | paylink   |  |        |  | events   |  |         |
    | position  |  |        |  |          |  |         |
    | analytics |  |        |  |          |  |         |
    | compen    |  |        |  |          |  |         |
    | recovery  |  |        |  |          |  |         |
    | reconc    |  |        |  |          |  |         |
    | settle    |  |        |  |          |  |         |
    | maint     |  |        |  |          |  |         |
    +-----------+  +--------+  +----------+  +---------+
       |              |            |              |
       |              |            |              |
       +--------------+------------+--------------+
                      |
                      v
    +---------------------------------------------+
    |                  store/                       |
    | transferdb/ ledgerdb/ treasurydb/            |
    | Adapters: SQLC -> domain types               |
    +---------------------+-----------------------+
                          |
                          v
    +---------------------------------------------+
    |                  node/                       |
    | outbox/ (relay)                              |
    | worker/ (11 dedicated workers)               |
    | messaging/ (NATS JetStream, 12 streams)      |
    | chainmonitor/ (EVM + Tron pollers)           |
    +---------------------+-----------------------+
                          |
                          v
    +---------------------------------------------+
    |     api/ + observability/ + cache/           |
    | gateway/ (Fastify REST + SSE)                |
    | webhook/ (inbound provider receiver)         |
    | grpc/ (9+ gRPC service implementations)      |
    | observability/ (metrics, tracing, pool)      |
    | cache/ (two-level, rate limit, idempotency)  |
    +---------------------+-----------------------+
                          |
                          v
    +---------------------------------------------+
    |                  portal/ + dashboard/        |
    | portal/ (Vue 3 tenant self-service)          |
    | dashboard/ (Vue 3 ops console)               |
    +---------------------+-----------------------+
                          |
                          v
    +---------------------------------------------+
    |                   cmd/                       |
    | settla-server (wiring: core + ledger + rail  |
    |    + treasury + deposit + analytics + ...)   |
    | settla-node (wiring: relay + 11 workers)     |
    +---------------------------------------------+
```

### What This Means in Practice

The `core/` engine (the settlement state machine) depends on these interfaces from `domain/`:

```go
// Interfaces that core/ depends on (all defined in domain/)
domain.TransferStore      // Persist transfer state
domain.Ledger             // Post and reverse ledger entries
domain.TreasuryManager    // Reserve and release treasury positions
domain.Router             // Select optimal settlement route
domain.EventPublisher     // Publish domain events
domain.ProviderRegistry   // Look up providers and blockchain clients
```

The `core/` engine DOES NOT know about:
- TigerBeetle (that is an implementation detail of `ledger/`)
- In-memory atomic counters (that is an implementation detail of `treasury/`)
- Scoring weights (that is an implementation detail of `rail/router/`)
- NATS JetStream (that is an implementation detail of `node/messaging/`)
- PostgreSQL (that is an implementation detail of `store/`)

Each implementation satisfies its domain interface and is wired together in `cmd/settla-server/main.go`.

---

## Interface Conventions

Settla has a careful naming convention to prevent interface name collisions across the package hierarchy.

### domain.Router vs core.Router

The domain-level `Router` has a single generic method:

```go
// From domain/provider.go
type Router interface {
    Route(ctx context.Context, req RouteRequest) (*RouteResult, error)
}
```

The core engine needs a richer interface that includes quote generation:

```go
// From core/store.go -- the core-level router contract
type Router interface {
    GetQuote(ctx context.Context, tenantID uuid.UUID,
             req domain.QuoteRequest) (*domain.Quote, error)
    GetRoutingOptions(ctx context.Context, tenantID uuid.UUID,
                      req domain.QuoteRequest) (*domain.RouteResult, error)
}
```

The `router.CoreRouterAdapter` bridges between these interfaces, applying per-tenant fee schedules when generating quotes.

### ProviderRegistry Naming

The `ProviderRegistry` interface uses `GetBlockchain(chain)` instead of `GetBlockchainClient(chain)`:

```go
// From domain/provider.go
type ProviderRegistry interface {
    ListOnRampIDs(ctx context.Context) []string
    ListOffRampIDs(ctx context.Context) []string
    GetOnRamp(id string) (OnRampProvider, error)
    GetOffRamp(id string) (OffRampProvider, error)
    GetBlockchain(chain string) (BlockchainClient, error)   // NOT GetBlockchainClient
    ListBlockchainChains() []string
}
```

The method is named `GetBlockchain` (not `GetBlockchainClient`) to avoid a method name collision with `core.ProviderRegistry` which has different semantics. This naming discipline prevents confusion when both interfaces are used in the same scope.

---

## Compile-Time Interface Checks: The Complete List

Every module in Settla includes a compile-time assertion that verifies it correctly implements its domain interface:

```go
// Ledger module
var _ domain.Ledger = (*Service)(nil)              // ledger/ledger.go

// Treasury module
var _ domain.TreasuryManager = (*Manager)(nil)      // treasury/manager.go

// Router module
var _ domain.Router = (*Router)(nil)                // rail/router/router.go

// Provider registry
var _ domain.ProviderRegistry = (*Registry)(nil)    // rail/provider/registry.go

// Event publisher (two implementations)
var _ domain.EventPublisher = (*Publisher)(nil)                  // node/messaging/publisher.go
var _ domain.EventPublisher = (*CircuitBreakerPublisher)(nil)    // node/messaging/publisher.go

// Blockchain clients (one per chain)
var _ domain.BlockchainClient = (*Client)(nil)      // rail/blockchain/tron/client.go
var _ domain.BlockchainClient = (*Client)(nil)      // rail/blockchain/ethereum/client.go
var _ domain.BlockchainClient = (*Client)(nil)      // rail/blockchain/solana/client.go

// Provider implementations
var _ domain.OnRampProvider = (*OnRampProvider)(nil)   // rail/provider/settla/onramp.go
var _ domain.OffRampProvider = (*OffRampProvider)(nil)  // rail/provider/settla/offramp.go

// Resilience wrappers
var _ domain.Ledger = (*CircuitBreakerLedger)(nil)    // resilience/wrappers.go

// Test doubles
var _ domain.EventPublisher = (*eventCollector)(nil)  // tests/integration/helpers_test.go
```

This comprehensive list tells you several things:

1. **Every domain interface has at least one implementation.** No dead interfaces.
2. **Some interfaces have multiple implementations.** `domain.EventPublisher` has a direct publisher and a circuit-breaker-wrapped publisher. `domain.BlockchainClient` has implementations for Tron, Ethereum, and Solana.
3. **Test doubles also verify interface compliance.** The `eventCollector` in integration tests satisfies `domain.EventPublisher`, so if the interface changes, the test helper breaks at compile time too.
4. **Resilience wrappers satisfy the same interface.** `CircuitBreakerLedger` wraps `domain.Ledger`, implementing the same interface. This means circuit breaker logic can be injected transparently.

---

## Module Extraction Path

The modular monolith's key promise: any module can become a gRPC service without changing business logic. Here is how:

### Step 1: The Interface Stays in domain/

The `domain.TreasuryManager` interface does not change:

```go
// domain/treasury.go -- unchanged
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

### Step 2: Create a gRPC Client Implementation

```go
// treasury/grpcclient/client.go (new file)
type GRPCClient struct {
    conn treasurypb.TreasuryServiceClient
}

var _ domain.TreasuryManager = (*GRPCClient)(nil)  // compile-time check

func (c *GRPCClient) Reserve(ctx context.Context, tenantID uuid.UUID,
    currency domain.Currency, location string, amount decimal.Decimal,
    reference uuid.UUID) error {

    _, err := c.conn.Reserve(ctx, &treasurypb.ReserveRequest{
        TenantId:  tenantID.String(),
        Currency:  string(currency),
        Location:  location,
        Amount:    amount.String(),
        Reference: reference.String(),
    })
    return err
}
// ... implement remaining methods
```

### Step 3: Swap the Constructor in cmd/

```go
// cmd/settla-server/main.go

// BEFORE extraction (in-process):
treasuryMgr := treasury.NewManager(treasuryDB, flushInterval)

// AFTER extraction (gRPC client):
treasuryConn, _ := grpc.Dial("treasury-service:9090")
treasuryMgr := treasurygrpc.NewClient(treasuryConn)

// The engine does not change. It still calls treasuryMgr.Reserve().
// It does not know or care whether Reserve() is in-process or over gRPC.
engine := core.NewEngine(transferStore, treasuryMgr, ledger, router)
```

**What changed:** One line in `main.go`. The constructor.

**What did NOT change:** The `core/` engine, the `domain/` interfaces, the worker logic, the API layer. Zero lines of business logic changed.

> **Key Insight:** This extraction path works because the core engine depends on `domain.TreasuryManager` (the interface), not `*treasury.Manager` (the implementation). The dependency inversion principle is not just a design pattern here -- it is the mechanism that enables zero-downtime module extraction.

---

## The store/ Adapter Pattern

The `store/` package bridges SQLC-generated code (which has its own types) to domain interfaces (which use `domain/` types):

```
    store/
    +-- transferdb/
    |   +-- models.go              # SQLC-generated models (store-specific types)
    |   +-- querier.go             # SQLC-generated interface
    |   +-- transfers.sql.go       # SQLC-generated query implementations
    |   +-- settlement.sql.go      # SQLC-generated settlement queries
    |   +-- analytics.sql.go       # SQLC-generated analytics queries
    |   +-- adapter.go             # Bridges SQLC types -> domain.Transfer
    |   +-- settlement_adapter.go  # Bridges SQLC types -> domain.Settlement
    |   +-- reconciliation_adapter.go  # Bridges for reconciliation
    +-- ledgerdb/
    |   +-- ledger.sql.go          # SQLC-generated
    |   +-- querier.go             # SQLC-generated
    +-- treasurydb/
        +-- ...
```

### The Adapter in Action

SQLC generates Go structs that match the database schema exactly. Domain types are different -- they use `decimal.Decimal` instead of `string`, `domain.TransferStatus` instead of `string`, etc. The adapter translates between them:

```go
// store/transferdb/adapter.go (simplified)
type Adapter struct {
    queries *Queries
    db      *sql.DB
}

func (a *Adapter) Get(ctx context.Context, tenantID uuid.UUID,
    id uuid.UUID) (*domain.Transfer, error) {

    // Call SQLC-generated query (returns store-specific type)
    row, err := a.queries.GetTransfer(ctx, GetTransferParams{
        TenantID: tenantID,
        ID:       id,
    })
    if err != nil {
        return nil, err
    }

    // Convert store type to domain type
    return &domain.Transfer{
        ID:             row.ID,
        TenantID:       row.TenantID,
        Status:         domain.TransferStatus(row.Status),
        SourceAmount:   decimal.RequireFromString(row.SourceAmount),
        // ... map all fields
    }, nil
}
```

This adapter pattern keeps the domain types clean (no `database/sql` imports) while allowing SQLC to generate efficient, type-safe database code.

---

## The Entrypoints: Where Wiring Happens

Settla has two binary entrypoints that wire everything together:

### cmd/settla-server/

The main server binary. Creates instances of all modules and injects dependencies:

```go
// cmd/settla-server/main.go (simplified structure)
func main() {
    // Infrastructure
    transferDB := connectDB(cfg.TransferDBURL)
    ledgerDB := connectDB(cfg.LedgerDBURL)
    redisClient := connectRedis(cfg.RedisURL)
    natsConn := connectNATS(cfg.NATSURL)
    tbClient := connectTigerBeetle(cfg.TigerBeetleAddr)

    // Store adapters (SQLC -> domain)
    transferStore := transferdb.NewAdapter(transferDB)
    ledgerStore := ledgerdb.NewAdapter(ledgerDB)

    // Module implementations
    ledger := ledger.NewService(tbClient, ledgerStore)
    treasury := treasury.NewManager(treasuryDB, 100*time.Millisecond)
    router := router.NewRouter(providerRegistry, cfg.RouterWeights)
    publisher := messaging.NewPublisher(natsConn)

    // Core engine (depends only on interfaces)
    engine := core.NewEngine(transferStore, treasury, ledger, router, publisher)

    // gRPC server
    grpcServer := grpc.NewServer(engine, transferStore)

    // HTTP server (health, pprof)
    httpServer := http.NewServer(engine)

    // Start everything
    go grpcServer.Serve(":9090")
    go httpServer.Serve(":8080")
    waitForShutdown()
}
```

### cmd/settla-node/

The worker process. Creates the outbox relay and all 11 dedicated workers:

```go
// cmd/settla-node/main.go (simplified structure)
func main() {
    // Same infrastructure connections...

    // Workers (each depends on domain interfaces)
    transferWorker := worker.NewTransferWorker(engine)
    providerWorker := worker.NewProviderWorker(engine, providerRegistry)
    ledgerWorker := worker.NewLedgerWorker(engine, ledger)
    treasuryWorker := worker.NewTreasuryWorker(engine, treasury)
    blockchainWorker := worker.NewBlockchainWorker(engine, providerRegistry)
    webhookWorker := worker.NewWebhookWorker(engine, httpClient)
    inboundWebhookWorker := worker.NewInboundWebhookWorker(engine, providerRegistry)
    depositWorker := worker.NewDepositWorker(depositEngine, ledger, treasury)
    bankDepositWorker := worker.NewBankDepositWorker(bankDepositEngine, ledger, treasury)
    emailWorker := worker.NewEmailWorker(emailSender)
    dlqMonitor := worker.NewDLQMonitor(publisher, alerter)

    // Outbox relay (polls DB, publishes to NATS)
    relay := outbox.NewRelay(transferDB, publisher, 20*time.Millisecond, 500)

    // NATS subscribers (route messages to 12 streams)
    subscriber := messaging.NewSubscriber(natsConn)
    subscriber.Subscribe("SETTLA_TRANSFERS", transferWorker.Handle)
    subscriber.Subscribe("SETTLA_PROVIDERS", providerWorker.Handle)
    subscriber.Subscribe("SETTLA_LEDGER", ledgerWorker.Handle)
    subscriber.Subscribe("SETTLA_TREASURY", treasuryWorker.Handle)
    subscriber.Subscribe("SETTLA_BLOCKCHAIN", blockchainWorker.Handle)
    subscriber.Subscribe("SETTLA_WEBHOOKS", webhookWorker.Handle)
    subscriber.Subscribe("SETTLA_PROVIDER_WEBHOOKS", inboundWebhookWorker.Handle)
    subscriber.Subscribe("SETTLA_CRYPTO_DEPOSITS", depositWorker.Handle)
    subscriber.Subscribe("SETTLA_BANK_DEPOSITS", bankDepositWorker.Handle)
    subscriber.Subscribe("SETTLA_EMAILS", emailWorker.Handle)
    subscriber.Subscribe("SETTLA_DLQ", dlqMonitor.Handle)

    // Start relay + wait
    go relay.Start(ctx)
    waitForShutdown()
}
```

---

## Verifying Module Boundaries

### No Circular Imports

Go's compiler enforces no circular imports at the package level. But you should also verify that the dependency direction is correct (downward only, toward `domain/`).

```bash
# List all imports for the core/ package
go list -f '{{.ImportPath}}: {{join .Imports ", "}}' ./core/...

# Verify core/ does not import ledger/, treasury/, or rail/
# It should only import domain/ and stdlib
```

### The Import Rule Checker

You can write a test that verifies module boundaries:

```go
// build_test.go
func TestNoCoreImportsOfSiblingModules(t *testing.T) {
    forbidden := []string{
        "github.com/intellect4all/settla/ledger",
        "github.com/intellect4all/settla/treasury",
        "github.com/intellect4all/settla/rail",
        "github.com/intellect4all/settla/store",
        "github.com/intellect4all/settla/node",
    }

    pkgs, _ := packages.Load(&packages.Config{Mode: packages.NeedImports},
        "github.com/intellect4all/settla/core/...")

    for _, pkg := range pkgs {
        for _, imp := range pkg.Imports {
            for _, f := range forbidden {
                if strings.HasPrefix(imp.PkgPath, f) {
                    t.Errorf("core/ imports forbidden package %s", imp.PkgPath)
                }
            }
        }
    }
}
```

---

## The Module Inventory

Each module has a clear responsibility and a clean interface:

| Module | Package | Depends On | Implements | Purpose |
|--------|---------|-----------|------------|---------|
| Domain | `domain/` | stdlib + decimal + uuid | N/A (defines contracts) | Types, interfaces, errors |
| Core Engine | `core/` | `domain/` | N/A (orchestrator) | Pure state machine, zero side effects |
| Deposit Engine | `core/deposit/` | `domain/` | N/A | Crypto deposit session engine |
| Bank Deposit Engine | `core/bankdeposit/` | `domain/` | N/A | Fiat deposit via virtual bank accounts |
| Payment Links | `core/paymentlink/` | `domain/` | N/A | Payment link generation and redemption |
| Position Engine | `core/position/` | `domain/` | N/A | Position transactions (top-ups, withdrawals) |
| Analytics | `core/analytics/` | `domain/` | N/A | Analytics snapshots and data exports |
| Compensation | `core/compensation/` | `domain/` | N/A | Refund and reversal flows |
| Recovery | `core/recovery/` | `domain/` | N/A | Stuck transfer detection |
| Reconciliation | `core/reconciliation/` | `domain/` | N/A | 6 automated consistency checks |
| Settlement | `core/settlement/` | `domain/` | N/A | Net settlement calculator + daily scheduler |
| Maintenance | `core/maintenance/` | `domain/` | N/A | Partition manager, vacuum, capacity monitor |
| Ledger | `ledger/` | `domain/` | `domain.Ledger` | Dual-backend: TigerBeetle (writes) + Postgres (reads) |
| Treasury | `treasury/` | `domain/` | `domain.TreasuryManager` | In-memory atomic reservation + event-sourced audit |
| Router | `rail/router/` | `domain/` | `domain.Router` | Smart route selection |
| Providers | `rail/provider/` | `domain/` | `domain.ProviderRegistry` | Provider adapters |
| Blockchain | `rail/blockchain/` | `domain/` | `domain.BlockchainClient` | Tron, Ethereum, Solana |
| Messaging | `node/messaging/` | `domain/` | `domain.EventPublisher` | NATS JetStream client, 12 streams |
| Workers | `node/worker/` | `domain/` | N/A | 11 dedicated per-domain workers |
| Chain Monitor | `node/chainmonitor/` | `domain/` | N/A | Watches on-chain stablecoin transfers (EVM + Tron) |
| Store | `store/` | `domain/` + DB drivers | Various store interfaces | SQLC adapters |
| Gateway | `api/gateway/` | Generated protos | REST API + SSE | TypeScript/Fastify |
| Webhook | `api/webhook/` | Generated protos | Inbound webhooks | TypeScript/Fastify |
| gRPC | `api/grpc/` | `domain/` + protos | 9+ gRPC services | Go gRPC server |
| Cache | `cache/` | `domain/` + Redis | N/A | Two-level cache, rate limiting, idempotency, daily volume, tenant index |
| Observability | `observability/` | N/A | N/A | Prometheus metrics, OpenTelemetry tracing, pool metrics |
| Portal | `portal/` | N/A | N/A | Vue 3 tenant self-service portal |
| Dashboard | `dashboard/` | N/A | N/A | Vue 3 ops console |

---

## Common Mistakes

### Mistake 1: Importing Sibling Packages Directly

```go
// WRONG: core/ importing ledger/ directly
import "github.com/intellect4all/settla/ledger"

func (e *Engine) PostToLedger(entry domain.JournalEntry) {
    ledger.Post(entry)  // Direct dependency on implementation
}

// RIGHT: core/ importing domain/ interface
import "github.com/intellect4all/settla/domain"

func (e *Engine) PostToLedger(entry domain.JournalEntry) {
    e.ledger.PostEntries(ctx, entry)  // Through interface, injected in constructor
}
```

If `core/` imports `ledger/` directly, extracting the ledger to a gRPC service requires changing `core/` code. With the interface approach, only `cmd/main.go` changes.

### Mistake 2: Putting Infrastructure in domain/

```go
// WRONG: domain/ importing database driver
package domain
import "database/sql"

type TransferStore interface {
    Get(ctx context.Context, db *sql.DB, id uuid.UUID) (*Transfer, error)
    //                        ^^^^^^^^ infrastructure leaked into domain
}

// RIGHT: domain/ has no infrastructure imports
type TransferStore interface {
    Get(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*Transfer, error)
    // Pure domain types only
}
```

### Mistake 3: Premature Extraction to Microservices

The modular monolith gives you the extraction OPTION without the operational COST. Do not extract a module to a service until you have a concrete reason:
- Different scaling requirements (the ledger needs 100x more instances than the router)
- Different deployment cadences (the treasury team deploys daily, the router team deploys weekly)
- Different technology requirements (the blockchain module needs GPU access)
- Team autonomy (separate teams need independent deployment)

Without one of these reasons, extraction adds complexity for zero benefit.

### Mistake 4: Skipping the Adapter Layer

```go
// WRONG: domain types with database tags
type Transfer struct {
    ID     uuid.UUID `db:"id"`        // Database concern in domain
    Status string    `db:"status"`     // Raw string, not typed
}

// RIGHT: domain types are pure, adapters handle translation
// domain/transfer.go
type Transfer struct {
    ID     uuid.UUID
    Status TransferStatus
}

// store/transferdb/adapter.go
func rowToTransfer(row GetTransferRow) *domain.Transfer {
    return &domain.Transfer{
        ID:     row.ID,
        Status: domain.TransferStatus(row.Status),
    }
}
```

---

## Exercises

### Exercise 1: Verify No Circular Imports

Using Go's tooling:

```bash
go list -f '{{.ImportPath}}: {{join .Imports "\n"}}' ./core/...
```

1. List every import in the `core/` package
2. Verify that no import path contains `ledger/`, `treasury/`, `rail/`, or `store/`
3. Verify that the only external import is `domain/` (plus stdlib and decimal/uuid)
4. Repeat for `ledger/`, `treasury/`, and `rail/` -- verify they import `domain/` but not each other

### Exercise 2: Design a Module Extraction

You need to extract the `treasury/` module to a separate gRPC service because it needs to run on machines with faster memory for the in-memory positions.

1. Write the protobuf service definition for `TreasuryService` with `Reserve`, `Release`, `GetPositions`, `GetPosition`, and `GetLiquidityReport` RPCs
2. Write the Go gRPC client struct that implements `domain.TreasuryManager`
3. Include the compile-time interface check: `var _ domain.TreasuryManager = (*GRPCClient)(nil)`
4. Show the one line that changes in `cmd/settla-server/main.go`
5. What operational concerns does this extraction introduce that the monolith did not have? (Hint: network latency, service discovery, health checks)

### Exercise 3: Add a New Module

You need to add a `notifications/` module that sends email and SMS notifications for transfer events:

1. Design the `domain.NotificationService` interface (what methods does it need?)
2. Where does the interface definition go? (Answer: `domain/`)
3. Where does the implementation go? (Answer: `notifications/`)
4. Where does the SQLC-generated notification preferences store go? (Answer: `store/notificationdb/`)
5. Where is it wired into the application? (Answer: `cmd/settla-server/main.go`)
6. Write the compile-time interface check
7. Draw the updated dependency graph showing `notifications/` depends on `domain/` only

### Exercise 4: Dependency Graph Drawing

Draw the complete dependency graph for Settla showing:
1. Every top-level package
2. Every dependency arrow (direction matters)
3. Which packages depend on `domain/`
4. Which packages are terminal (depend on everything, depended on by nothing)
5. Verify there are no upward arrows (no package depends on a package above it in the hierarchy)

---

## What's Next

Module 1 is complete. You now understand the foundations that every other module builds on:

- **Chapter 1.1**: The business -- corridors, players, revenue model
- **Chapter 1.2**: The capacity math -- why the architecture looks the way it does
- **Chapter 1.3**: The domain model -- types that prevent bugs at compile time
- **Chapter 1.4**: The state machine -- explicit transitions with audit trails
- **Chapter 1.5**: Multi-tenancy -- isolation at every layer
- **Chapter 1.6**: The modular monolith -- one binary, strict boundaries, extraction-ready

In Module 2, we dive into the ledger -- double-entry accounting at scale with TigerBeetle for the write path (1M+ TPS) and PostgreSQL for the read path (CQRS), with the `ValidateEntries` function ensuring every posting balances.

---
