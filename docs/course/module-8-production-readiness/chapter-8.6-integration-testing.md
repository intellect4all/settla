# Chapter 8.6: Integration Testing — End-to-End Proof

**Estimated reading time:** 25 minutes

## Learning Objectives

- Build integration tests that exercise the complete settlement lifecycle
- Implement in-memory test doubles that satisfy domain interfaces
- Write tenant isolation tests that prove no cross-tenant data leakage
- Test concurrent transfers with shared treasury positions
- Assert database consistency after complex multi-step operations

---

## Integration Test Architecture

Settla's integration tests use in-memory implementations of all stores and services. No database, no NATS, no Redis — pure Go, fast execution:

```go
// From tests/integration/helpers_test.go
//go:build integration

package integration

import (
    "github.com/intellect4all/settla/core"
    "github.com/intellect4all/settla/domain"
    "github.com/intellect4all/settla/treasury"
    // ...
)

// Demo tenant IDs — deterministic for reproducible tests
var (
    LemfiTenantID        = uuid.MustParse("a0000000-0000-0000-0000-000000000001")
    FincraTenantID       = uuid.MustParse("b0000000-0000-0000-0000-000000000002")
    NetSettlementTenantID = uuid.MustParse("c0000000-0000-0000-0000-000000000003")
)
```

### In-Memory Transfer Store

```go
// From tests/integration/helpers_test.go
type memTransferStore struct {
    mu             sync.RWMutex
    transfers      map[uuid.UUID]*domain.Transfer
    idempotent     map[string]*domain.Transfer  // "tenantID:key"
    events         map[uuid.UUID][]domain.TransferEvent
    quotes         map[uuid.UUID]*domain.Quote
    outboxEntries  []domain.OutboxEntry
    eventPublisher domain.EventPublisher
}

// Compile-time interface check
var _ core.TransferStore = (*memTransferStore)(nil)
```

This store mirrors the real database behavior:
- Thread-safe with `sync.RWMutex`
- Enforces idempotency key uniqueness per tenant
- Stores outbox entries atomically with transfer state changes
- Satisfies `core.TransferStore` (verified at compile time)

### Test Setup Pattern

```go
func setupTestEngine(t *testing.T) (*core.Engine, *memTransferStore, *treasury.Manager) {
    t.Helper()

    // In-memory stores
    store := newMemTransferStore()
    tenantStore := newMemTenantStore()

    // Seed tenants with different configurations
    tenantStore.addTenant(makeLemfiTenant())   // PREFUNDED, 40/35 bps
    tenantStore.addTenant(makeFincraTenant())  // PREFUNDED, 25/20 bps

    // Real treasury manager (in-memory by design)
    treasuryMgr := treasury.NewManager(
        &memTreasuryStore{},
        nil, // no event publisher
        slog.Default(),
        nil, // no metrics
    )

    // Seed treasury positions
    treasuryMgr.LoadPosition(domain.Position{
        TenantID: LemfiTenantID,
        Currency: domain.CurrencyGBP,
        Location: "bank:gbp",
        Balance:  decimal.NewFromInt(1_000_000),
    })

    // Mock router with deterministic quotes
    router := newMockRouter()

    // Create engine
    engine := core.NewEngine(store, tenantStore, router, slog.Default(), nil)

    return engine, store, treasuryMgr
}
```

---

## Test Categories

### 1. Transfer Lifecycle Tests

Test the complete happy path:

```go
func TestTransferLifecycle_HappyPath(t *testing.T) {
    engine, store, _ := setupTestEngine(t)

    // Create transfer
    transfer, err := engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
        IdempotencyKey: "test-lifecycle-001",
        SourceCurrency: domain.CurrencyGBP,
        SourceAmount:   decimal.NewFromInt(1000),
        DestCurrency:   domain.CurrencyNGN,
        Sender:         testSender(),
        Recipient:      testRecipient(),
    })
    require.NoError(t, err)
    assert.Equal(t, domain.TransferStatusCreated, transfer.Status)

    // Process through all states
    err = engine.ProcessTransfer(ctx, LemfiTenantID, transfer.ID)
    require.NoError(t, err)

    // Verify final state
    final, err := store.GetTransfer(ctx, LemfiTenantID, transfer.ID)
    require.NoError(t, err)
    assert.Equal(t, domain.TransferStatusCompleted, final.Status)

    // Verify outbox entries were created for each step
    entries := store.GetOutboxEntries()
    assert.True(t, len(entries) >= 6, "expected at least 6 outbox entries")
}
```

### 2. Tenant Isolation Tests

Prove that one tenant cannot access another's data:

```go
func TestTenantIsolation_CrossTenantRead(t *testing.T) {
    engine, store, _ := setupTestEngine(t)

    // Create transfer for Lemfi
    transfer, err := engine.CreateTransfer(ctx, LemfiTenantID, createReq("iso-001"))
    require.NoError(t, err)

    // Try to read it as Fincra — MUST fail
    _, err = engine.GetTransfer(ctx, FincraTenantID, transfer.ID)
    assert.Error(t, err, "cross-tenant read must fail")

    // Read it as Lemfi — MUST succeed
    found, err := engine.GetTransfer(ctx, LemfiTenantID, transfer.ID)
    require.NoError(t, err)
    assert.Equal(t, transfer.ID, found.ID)
}

func TestTenantIsolation_IdempotencyKeyScoping(t *testing.T) {
    engine, _, _ := setupTestEngine(t)

    // Both tenants use the same idempotency key
    key := "shared-key-001"

    t1, err := engine.CreateTransfer(ctx, LemfiTenantID, createReq(key))
    require.NoError(t, err)

    t2, err := engine.CreateTransfer(ctx, FincraTenantID, createReq(key))
    require.NoError(t, err)

    // Must be different transfers (no cross-tenant collision)
    assert.NotEqual(t, t1.ID, t2.ID)
    assert.Equal(t, LemfiTenantID, t1.TenantID)
    assert.Equal(t, FincraTenantID, t2.TenantID)
}
```

### 3. Concurrency Tests

Prove that concurrent transfers on shared treasury positions work correctly:

```go
func TestConcurrency_ParallelReservations(t *testing.T) {
    engine, store, treasuryMgr := setupTestEngine(t)

    // Fund treasury with exactly enough for 100 transfers
    treasuryMgr.UpdateBalance(ctx, LemfiTenantID, domain.CurrencyGBP,
        "bank:gbp", decimal.NewFromInt(100_000))

    // Launch 100 concurrent CreateTransfer + FundTransfer
    var wg sync.WaitGroup
    results := make([]*domain.Transfer, 100)
    errors := make([]error, 100)

    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            t, err := engine.CreateTransfer(ctx, LemfiTenantID,
                createReqWithAmount(fmt.Sprintf("conc-%d", idx),
                    decimal.NewFromInt(1000))) // £1,000 each
            results[idx] = t
            errors[idx] = err
        }(i)
    }
    wg.Wait()

    // All should succeed (100 × £1,000 = £100,000 = exact balance)
    successCount := 0
    for _, err := range errors {
        if err == nil {
            successCount++
        }
    }
    assert.Equal(t, 100, successCount)

    // Treasury position should be fully reserved
    pos, _ := treasuryMgr.GetPosition(ctx, LemfiTenantID,
        domain.CurrencyGBP, "bank:gbp")
    assert.True(t, pos.Available().IsZero(),
        "all funds should be reserved")
}
```

### 4. Failure Path Tests

Test every failure scenario and verify compensation:

```go
func TestFailurePath_OnRampFailure(t *testing.T) {
    engine, store, _ := setupTestEngine(t)

    // Create and fund transfer
    transfer, _ := engine.CreateTransfer(ctx, LemfiTenantID, createReq("fail-001"))
    engine.FundTransfer(ctx, LemfiTenantID, transfer.ID)
    engine.InitiateOnRamp(ctx, LemfiTenantID, transfer.ID)

    // Simulate on-ramp failure
    err := engine.HandleOnRampResult(ctx, LemfiTenantID, transfer.ID,
        domain.IntentResult{
            Success:   false,
            Error:     "provider timeout",
            ErrorCode: "TIMEOUT",
        })
    require.NoError(t, err)

    // Verify transfer moved to REFUNDING
    final, _ := store.GetTransfer(ctx, LemfiTenantID, transfer.ID)
    assert.Equal(t, domain.TransferStatusRefunding, final.Status)

    // Verify treasury release intent was created
    entries := store.GetOutboxEntries()
    hasRelease := false
    for _, e := range entries {
        if e.EventType == domain.IntentTreasuryRelease {
            hasRelease = true
        }
    }
    assert.True(t, hasRelease, "treasury release intent must be created")
}
```

### 5. Idempotency Tests

Prove that duplicate requests are handled correctly:

```go
func TestIdempotency_DuplicateCreateTransfer(t *testing.T) {
    engine, _, _ := setupTestEngine(t)

    // First request
    t1, err := engine.CreateTransfer(ctx, LemfiTenantID, createReq("idemp-001"))
    require.NoError(t, err)

    // Duplicate request (same idempotency key)
    t2, err := engine.CreateTransfer(ctx, LemfiTenantID, createReq("idemp-001"))
    require.NoError(t, err)

    // Must return the SAME transfer, not create a new one
    assert.Equal(t, t1.ID, t2.ID)
    assert.Equal(t, t1.Status, t2.Status)
}
```

---

## State Transition Verification

```go
func TestStateTransitions_AllValidPaths(t *testing.T) {
    // Test every valid transition from ValidTransitions map
    for from, targets := range domain.ValidTransitions {
        for _, to := range targets {
            t.Run(fmt.Sprintf("%s_to_%s", from, to), func(t *testing.T) {
                transfer := &domain.Transfer{
                    ID:       uuid.New(),
                    TenantID: LemfiTenantID,
                    Status:   from,
                    Version:  1,
                }
                event, err := transfer.TransitionTo(to)
                require.NoError(t, err)
                assert.Equal(t, from, event.FromStatus)
                assert.Equal(t, to, event.ToStatus)
                assert.Equal(t, int64(2), transfer.Version)
            })
        }
    }
}

func TestStateTransitions_AllInvalidPaths(t *testing.T) {
    allStates := []domain.TransferStatus{
        domain.TransferStatusCreated, domain.TransferStatusFunded,
        domain.TransferStatusOnRamping, domain.TransferStatusSettling,
        domain.TransferStatusOffRamping, domain.TransferStatusCompleted,
        domain.TransferStatusFailed, domain.TransferStatusRefunding,
        domain.TransferStatusRefunded,
    }

    for _, from := range allStates {
        validTargets := domain.ValidTransitions[from]
        for _, to := range allStates {
            if contains(validTargets, to) {
                continue // skip valid transitions
            }
            t.Run(fmt.Sprintf("invalid_%s_to_%s", from, to), func(t *testing.T) {
                transfer := &domain.Transfer{Status: from, Version: 1}
                _, err := transfer.TransitionTo(to)
                assert.Error(t, err, "transition %s→%s must be rejected", from, to)
            })
        }
    }
}
```

---

## Running Integration Tests

```bash
# Run all integration tests (5 min timeout)
make test-integration

# Or directly:
go test -tags=integration -timeout=5m -race ./tests/integration/...

# Run a specific test:
go test -tags=integration -run TestTenantIsolation ./tests/integration/...

# With verbose output:
go test -tags=integration -v -run TestTransferLifecycle ./tests/integration/...
```

The `-tags=integration` build tag separates integration tests from unit tests:

```go
//go:build integration

package integration
```

This ensures `go test ./...` (without tags) runs only fast unit tests, while `make test-integration` runs the full integration suite.

---

## Common Mistakes

**Mistake 1: Testing against a real database in integration tests**
In-memory stores are faster and more deterministic. Use real databases in end-to-end tests, not integration tests.

**Mistake 2: Not testing failure paths**
Happy path tests prove the system works. Failure path tests prove it doesn't break. At 50M txn/day, even 0.01% failure rate = 5,000 failures/day.

**Mistake 3: Not testing concurrent access**
Single-threaded tests pass but production runs 100+ goroutines. Always test shared state (treasury positions) under concurrent pressure.

**Mistake 4: Not using compile-time interface checks in test doubles**
`var _ core.TransferStore = (*memTransferStore)(nil)` ensures your test double stays in sync with the real interface. Without it, interface changes silently break tests.

---

## Exercises

### Exercise 1: Write a Corridor Test
Test the complete GBP→USDT→NGN corridor:
1. Create a £5,000 transfer from Lemfi
2. Process through all states
3. Verify ledger entries balance
4. Verify treasury position changes
5. Verify the correct providers were selected

### Exercise 2: Stress Test Design
Design a stress test that:
1. Creates 1,000 concurrent transfers across 5 tenants
2. Processes all transfers to completion
3. Verifies no cross-tenant data leakage
4. Verifies all treasury positions are consistent
5. Measures latency percentiles (p50, p95, p99)

### Exercise 3: Chaos Test
Write a test where:
1. A provider fails after the 50th transfer
2. Verify affected transfers move to REFUNDING
3. Verify unaffected transfers complete normally
4. Verify treasury positions are consistent after all operations

---

## E2E Test Suite

Beyond the in-memory integration tests, Settla includes a full end-to-end test
suite that exercises the HTTP API through the Fastify gateway. These tests run
against real infrastructure (Docker Compose) and validate the complete stack:
gateway -> gRPC -> engine -> outbox -> workers -> providers.

The E2E suite lives in `tests/e2e/` and uses the `e2e` build tag:

```
tests/e2e/
  main_test.go             -- TestMain: resets address/virtual account pools
  helpers_test.go          -- HTTP client, environment helpers, assertion utils
  consistency_checker.go   -- Post-test consistency verification engine
  transfers_test.go        -- Transfer lifecycle, concurrency, corridors
  deposits_test.go         -- Crypto deposit sessions
  payment_links_test.go    -- Payment link creation, redemption, limits
  settlement_test.go       -- Position management, liquidity
  quotes_test.go           -- Quote creation and retrieval
  tenants_test.go          -- Tenant isolation, API keys
  negative_test.go         -- Error paths, rate limiting, auth errors
  consistency_test.go      -- Stuck transfers, deposit integrity, reconciliation
```

### Key Design Decision: HTTP API Only

The E2E tests use the HTTP gateway exclusively -- no direct gRPC calls, no
direct database access for mutations. Every test creates resources through
`POST /v1/transfers`, retrieves them through `GET /v1/transfers/:id`, and
asserts on HTTP response codes and JSON bodies:

```go
func TestTransfer_CreateAndRetrieve(t *testing.T) {
    skipIfNoGateway(t)
    c := newClient(seedAPIKey())

    resp, err := c.post("/v1/transfers", map[string]any{
        "idempotency_key": randomIdemKey(),
        "source_currency": "GBP",
        "source_amount":   "500.00",
        "dest_currency":   "NGN",
        "sender":          defaultSender(),
        "recipient":       map[string]any{"name": "E2E Recipient", "country": "NG"},
    })
    // ... assert status 201, retrieve, verify fields ...
}
```

This matters because it tests the real auth flow (API key -> HMAC hash ->
tenant resolution), the real rate limiter, the real gRPC connection pool,
and the real JSON serialization. In-memory integration tests skip all of
these layers.

### TestMain: Pool Reset

The `TestMain` function resets crypto address and virtual account pools before
running tests, ensuring test isolation regardless of prior runs:

```go
func TestMain(m *testing.M) {
    if err := resetPools(); err != nil {
        fmt.Fprintf(os.Stderr, "WARN: could not reset pools: %v\n", err)
    }
    os.Exit(m.Run())
}
```

### Test Categories

**Transfer Tests** (`transfers_test.go`): Create-and-retrieve, idempotency,
multi-corridor (GBP->NGN, USD->NGN), pagination, external reference lookup,
concurrent creation.

**Deposit Tests** (`deposits_test.go`): Crypto deposit session lifecycle,
address assignment, confirmation tracking.

**Payment Link Tests** (`payment_links_test.go`): Link creation, public
resolution (no auth required), redemption, disable, amount limits.

**Settlement Tests** (`settlement_test.go`): Position management, liquidity
reports, treasury balance verification.

**Tenant Tests** (`tenants_test.go`): Cross-tenant isolation (tenant A cannot
read tenant B's transfers), API key authentication, invalid key rejection.

**Negative Tests** (`negative_test.go`): Duplicate transfers (idempotency),
invalid currency pairs, missing required fields, authentication failures,
rate limit enforcement. These prove the system rejects bad input correctly.

**Consistency Tests** (`consistency_test.go`): Post-suite verification that
runs the consistency checker -- stuck transfers, outbox drain, ledger balance,
treasury reconciliation. Can be run independently:

```bash
go test -tags e2e -run TestConsistency ./tests/e2e/ -v
```

### Running E2E Tests

```bash
# Against a running Docker environment:
make api-test
# Equivalent to:
go test -tags e2e -timeout=5m -v ./tests/e2e/...

# Full lifecycle: start Docker, seed, test, report:
make api-test-full
```

The `GATEWAY_URL` environment variable controls the target (default:
`http://localhost:3100`). Seed API keys default to `sk_live_lemfi_demo_key`
and `sk_live_fincra_demo_key`.

---

## Test Coverage Summary

Settla has 128 test files across the codebase, spanning unit tests, integration
tests, benchmarks, load tests, chaos tests, and E2E tests:

```
+-----------------------+------------------------------------------+
| Category              | Location                                 |
+-----------------------+------------------------------------------+
| Unit tests            | core/, ledger/, treasury/, rail/,        |
|                       | domain/, cache/, node/                   |
| Integration tests     | tests/integration/ (build tag)           |
| E2E API tests         | tests/e2e/ (build tag)                   |
| Component benchmarks  | core/bench_test.go, treasury/bench_test, |
|                       | rail/router/bench_test, ledger/bench_test|
| Load tests            | tests/loadtest/ (standalone binary)      |
| Chaos tests           | tests/chaos/ (standalone binary)         |
+-----------------------+------------------------------------------+
```

### Makefile Quick Reference

```bash
make test               # Unit tests with -race
make test-integration   # In-memory integration tests (5 min timeout)
make api-test           # E2E API tests against running services
make api-test-full      # Start Docker, seed, then E2E tests
make bench              # Component benchmarks -> bench-results.txt
make loadtest           # 5,000 TPS for 10 minutes
make loadtest-quick     # 1,000 TPS for 2 minutes
make soak-short         # 15-minute soak test
make chaos              # All 8 chaos scenarios
make report             # Full benchmark report (bench + load + soak)
```

---

## What's Next

Module 8 -- and the entire course -- is complete. You have built a production-grade settlement system from the ground up: domain modeling, double-entry ledger, pure state machine engine, in-memory treasury, smart routing, async workers, API gateway, reconciliation, compensation, recovery, and production testing.

The system handles 50M transactions/day with mathematical proof -- not just architecture diagrams, but actual benchmarks, load tests, chaos tests, and E2E API tests that validate every throughput claim.
