# Chapter 5.6: Circuit Breakers and Fallback Routing

**Estimated reading time:** 25 minutes

## Learning Objectives

- Implement the circuit breaker pattern for external provider calls
- Understand the three states: Closed, Open, Half-Open
- Configure per-provider circuit breakers with appropriate thresholds
- Use fallback alternatives from quotes when primary providers fail
- Track circuit breaker metrics for operational visibility

---

## Why Circuit Breakers

When a provider starts failing (network issues, rate limits, downtime), naive retry logic makes things worse:

```
Without circuit breaker:
  Request 1 → Provider (timeout 30s) → fail
  Request 2 → Provider (timeout 30s) → fail
  Request 3 → Provider (timeout 30s) → fail
  ...
  Request 5000 → Provider (timeout 30s) → fail

  Result: 5,000 requests × 30s timeout = 150,000 seconds of wasted time
  All worker goroutines blocked waiting on a dead provider
  Backlog grows, NATS redelivery timer fires, cascading failure
```

```
With circuit breaker:
  Request 1 → Provider → fail (circuit records failure)
  Request 2 → Provider → fail (circuit records failure)
  ...
  Request 15 → Provider → fail (threshold reached!)

  CIRCUIT OPENS:
  Request 16 → FAST FAIL (no network call, ~1µs)
  Request 17 → FAST FAIL
  ...

  After 10 seconds (reset timeout):
  Request 500 → Provider → success! (half-open probe)
  CIRCUIT CLOSES (resume normal operation)
```

---

## The Three States

```
┌──────────────────────────────────────────────────────┐
│              CIRCUIT BREAKER STATE MACHINE             │
├──────────────────────────────────────────────────────┤
│                                                       │
│  ┌──────────┐                    ┌──────────┐        │
│  │  CLOSED  │─── 15 failures ──▶│   OPEN   │        │
│  │ (normal) │    in window       │(fast-fail)│        │
│  └────┬─────┘                    └─────┬────┘        │
│       │                                │              │
│       │ success                        │ 10s timeout  │
│       │                                │              │
│       │                          ┌─────▼─────┐       │
│       │                          │ HALF-OPEN │       │
│       │◄─── probe succeeds ─────│ (1-2 probe │       │
│       │                          │  requests) │       │
│       │                          └─────┬─────┘       │
│       │                                │              │
│       │                          probe fails          │
│       │                                │              │
│       │                          ┌─────▼────┐        │
│       │                          │   OPEN   │        │
│       │                          │(fast-fail)│        │
│       │                          └──────────┘        │
└──────────────────────────────────────────────────────┘
```

### Configuration

```
Settla's circuit breaker settings:
  Failure threshold:     15 consecutive failures
  Reset timeout:         10 seconds
  Half-open max probes:  2 requests

  Why 15 (not 3 or 5)?
  Providers have transient errors. A threshold of 3 would
  open the circuit on a brief network hiccup. 15 consecutive
  failures indicates a real outage, not a blip.

  Why 10 seconds (not 60)?
  Provider outages are often brief (deploy, rate limit reset).
  10s means we probe quickly and resume within seconds of recovery.
```

---

## Per-Provider Circuit Breakers

Each provider gets its own circuit breaker. A failure in Provider A does not affect Provider B:

```
ProviderWorker
├── CircuitBreaker["ramp-network"]     → CLOSED (healthy)
├── CircuitBreaker["moonpay"]          → OPEN (15 failures)
├── CircuitBreaker["yellow-card"]      → CLOSED (healthy)
├── CircuitBreaker["kotani-pay"]       → HALF-OPEN (probing)
└── CircuitBreaker["transak"]          → CLOSED (healthy)

Transfer using moonpay → FAST FAIL → try alternative from quote
Transfer using yellow-card → normal execution
```

---

## Fallback Routing

When a primary provider's circuit breaker is open, the worker tries alternatives from the quote:

```go
// Simplified fallback logic in ProviderWorker:
func (w *ProviderWorker) executeOnRamp(ctx context.Context, payload domain.ProviderOnRampPayload) error {
    // Try primary provider
    err := w.callProvider(payload.ProviderID, payload)
    if err == nil {
        return nil // success
    }

    // Primary failed — try alternatives from the quote
    for _, alt := range payload.Alternatives {
        if w.circuitBreakers[alt.ProviderID].IsOpen() {
            continue // skip providers with open circuits
        }
        err = w.callProvider(alt.ProviderID, rebuildPayload(payload, alt))
        if err == nil {
            return nil // alternative succeeded
        }
    }

    return fmt.Errorf("all providers failed for transfer %s", payload.TransferID)
}
```

The alternatives were stored in the quote at routing time:

```go
// From domain/outbox.go
type OnRampFallback struct {
    ProviderID      string          `json:"provider_id"`
    OffRampProvider string          `json:"off_ramp_provider"`
    Chain           string          `json:"chain"`
    StableCoin      Currency        `json:"stablecoin"`
    Fee             Money           `json:"fee"`
    Rate            decimal.Decimal `json:"rate"`
    StableAmount    decimal.Decimal `json:"stable_amount"`
}

type ProviderOnRampPayload struct {
    // ... primary provider fields ...
    Alternatives []OnRampFallback `json:"alternatives,omitempty"`
}
```

> **Key Insight:** Alternatives travel inside the outbox payload. The worker can switch providers without consulting the engine or the router. This is critical because the engine is a pure state machine with no network calls — it can't be consulted mid-execution. The quote pre-computed the alternatives, and the worker uses them autonomously.

---

## Off-Ramp Fallback Constraints

Off-ramp alternatives are more constrained than on-ramp alternatives:

```go
// From domain/outbox.go
type OffRampFallback struct {
    ProviderID string          `json:"provider_id"`
    Fee        Money           `json:"fee"`
    Rate       decimal.Decimal `json:"rate"`
}
```

Only alternatives with the **same chain and stablecoin** qualify for off-ramp fallback, because:
1. The on-ramp already converted to a specific stablecoin (e.g., USDT)
2. The blockchain send already delivered to a specific chain (e.g., Tron)
3. The off-ramp alternative must accept USDT on Tron — it can't switch to USDC on Ethereum

```go
// From core/engine.go — HandleSettlementResult
// Filter alternatives: only same chain+stablecoin qualify
for _, alt := range quote.Route.AlternativeRoutes {
    if alt.Chain == transfer.Chain && alt.StableCoin == transfer.StableCoin {
        offRampAlts = append(offRampAlts, domain.OffRampFallback{
            ProviderID: alt.OffRampProvider,
            Fee:        alt.Fee,
            Rate:       alt.Rate,
        })
    }
}
```

---

## Metrics

Circuit breaker state changes should be tracked for operational visibility:

```
Metrics to track:
  circuit_breaker_state{provider="ramp-network", state="closed|open|half_open"}
  circuit_breaker_failures_total{provider="ramp-network"}
  circuit_breaker_fallback_total{provider="ramp-network"}
  circuit_breaker_fast_fail_total{provider="ramp-network"}

Alerts:
  - Any circuit breaker OPEN for > 5 minutes → page on-call
  - Fallback rate > 10% for any provider → warning
  - All providers for a corridor OPEN → critical (corridor down)
```

---

## WebhookWorker: Per-Tenant Semaphores and Circuit Breakers

The ProviderWorker uses per-provider circuit breakers because there are a handful of known providers. The WebhookWorker faces a different problem: thousands of tenants, each with their own webhook endpoint. Two per-tenant resources are needed:

1. **Per-tenant semaphore** — limits concurrent webhook deliveries to a single tenant (e.g., 10 concurrent requests). Without this, a high-volume tenant could monopolise all 100 global HTTP slots, starving every other tenant.
2. **Per-tenant circuit breaker** — prevents one broken tenant endpoint (returning 500s, timing out) from opening a global circuit breaker that blocks deliveries to all tenants.

```
WebhookWorker
├── Global HTTP semaphore (100 slots)
├── tenantSemaphore (per-tenant, 10 slots each)
│   ├── tenant-a0000... → semEntry{ch: buffered(10), lastUsed: 1711670400}
│   ├── tenant-b0000... → semEntry{ch: buffered(10), lastUsed: 1711670380}
│   └── ... (created on demand)
└── tenantCBs (per-tenant circuit breakers)
    ├── tenant-a0000... → cbEntry{cb: CircuitBreaker, lastUsed: 1711670400}
    ├── tenant-b0000... → cbEntry{cb: CircuitBreaker, lastUsed: 1711670380}
    └── ... (created on demand)
```

The delivery flow acquires both before making the HTTP call:

```go
// Per-tenant fair queueing: acquire tenant slot before global semaphore
// to prevent a single high-volume tenant from monopolising all slots.
tenantKey := payload.TenantID.String()
if err := w.tenantSem.acquire(ctx, tenantKey); err != nil {
    return err
}
defer w.tenantSem.release(tenantKey)

// Backpressure: limit concurrent HTTP calls
select {
case w.httpSem <- struct{}{}:
case <-ctx.Done():
    return ctx.Err()
}
defer func() { <-w.httpSem }()
```

### Idle Eviction (Critical Invariant #10)

Per-tenant maps grow without bound as new tenants are encountered. With 20,000 tenants, each maintaining a circuit breaker struct and a buffered channel semaphore, memory adds up. Settla solves this by evicting entries that have been idle for more than 5 minutes.

Each entry tracks its last-used timestamp as an atomic int64 (unix seconds). A background goroutine runs on a ticker and scans both maps:

```go
// cleanupTenantResources evicts per-tenant semaphores and circuit breakers
// that have been idle for more than 5 minutes. This prevents unbounded memory
// growth as new tenants are encountered over time.
func (w *WebhookWorker) cleanupTenantResources(ctx context.Context) {
    const idleTimeout = 5 * time.Minute
    ticker := time.NewTicker(idleTimeout)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            cutoff := time.Now().Add(-idleTimeout).Unix()

            // Evict idle semaphores (only if no in-flight webhooks).
            w.tenantSem.mu.Lock()
            for tenantID, entry := range w.tenantSem.sems {
                if entry.lastUsed.Load() < cutoff && len(entry.ch) == 0 {
                    delete(w.tenantSem.sems, tenantID)
                }
            }
            w.tenantSem.mu.Unlock()

            // Evict idle circuit breakers.
            w.tenantCBs.Range(func(key, value any) bool {
                entry := value.(*cbEntry)
                if entry.lastUsed.Load() < cutoff {
                    w.tenantCBs.Delete(key)
                }
                return true
            })
        }
    }
}
```

Two details worth noting:

1. **Semaphore eviction checks `len(entry.ch) == 0`** — a semaphore is only evicted if no goroutines hold a slot. Evicting a semaphore with in-flight requests would cause a panic when `release` tries to drain a deleted channel.
2. **Circuit breakers have no in-flight check** — a circuit breaker is stateless from the caller's perspective. If a tenant sends a new webhook after eviction, `getTenantCB` lazily creates a fresh breaker. The only cost is losing the failure count, which resets the breaker to Closed — acceptable because 5 minutes of inactivity means the endpoint may have recovered.

This cleanup goroutine is started in `Start()` and exits when the context is cancelled:

```go
func (w *WebhookWorker) Start(ctx context.Context) error {
    w.logger.Info("settla-webhook-worker: starting", "partition", w.partition)
    go w.cleanupTenantResources(ctx)
    // ...
}
```

---

## Common Mistakes

**Mistake 1: Global circuit breaker (not per-provider)**
A single breaker for all providers means one failing provider disables all providers. Always use per-provider breakers.

**Mistake 2: Threshold too low**
A threshold of 3 opens the circuit on transient network errors. 15 consecutive failures indicates a real outage.

**Mistake 3: No fallback path**
Without alternatives in the quote, a circuit breaker opening means immediate failure. The alternatives provide graceful degradation.

**Mistake 4: Not tracking circuit breaker state**
Without metrics, you don't know when a provider is experiencing issues until customers complain. Circuit breaker state changes should be alertable events.

**Mistake 5: Not evicting per-tenant resources after idle**
Per-tenant maps (circuit breakers, semaphores, rate limiters) grow without bound as new tenants are encountered. Without a cleanup goroutine that evicts idle entries, 20,000 tenants x (circuit breaker + semaphore) = significant memory leak that only manifests after weeks in production.

---

## Exercises

### Exercise 1: Configure Breakers
Design circuit breaker settings for:
1. A provider with 99.9% uptime but occasional 5-second blips
2. A provider with 95% uptime and frequent 30-second outages
3. A blockchain RPC node that rate-limits at 100 req/sec

### Exercise 2: Fallback Scoring
Given 3 alternative routes scored at 0.85, 0.72, and 0.61:
1. Which alternative should be tried first?
2. What if the 0.85 alternative's circuit breaker is open?
3. What if all alternatives have open circuit breakers?

### Exercise 3: Implement a Circuit Breaker
Write a Go circuit breaker that:
1. Tracks consecutive failures
2. Transitions through Closed → Open → Half-Open → Closed
3. Is safe for concurrent use (multiple goroutines)
4. Exposes `Allow() bool` and `RecordResult(success bool)`

---

## What's Next

Module 5 is complete. You now understand NATS JetStream, the outbox relay, tenant partitioning, CHECK-BEFORE-CALL, all 11 workers, and circuit breakers. Module 6 builds the API layer — Protocol Buffers, the gRPC server, the REST gateway, and the multi-level auth cache.
