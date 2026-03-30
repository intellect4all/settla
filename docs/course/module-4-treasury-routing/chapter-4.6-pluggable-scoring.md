# Chapter 4.6: Pluggable Scoring

**Reading time:** ~22 minutes
**Prerequisites:** Chapter 4.4 (Smart Routing), Chapter 4.5 (Provider Adapters)
**Code references:** `rail/router/router.go`, `rail/router/bench_test.go`, `domain/provider.go`

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Describe the `LiquidityScorer` and `ReliabilityScorer` interfaces
2. Explain the default behavior when no scorer is wired (1.0 fallback)
3. Design a reliability scorer from `provider_transactions` history
4. Understand how scorers are injected via `RouterOption`
5. Implement a reliability scorer as a hands-on exercise

---

## The Scorer Interfaces

The router defines two optional scoring interfaces. When not provided, the
router falls back to 1.0 (optimistic assumption). When provided, they
supply real data to the scoring algorithm.

### LiquidityScorer

```go
type LiquidityScorer interface {
    LiquidityScore(ctx context.Context, providerID string,
        currency domain.Currency, amount decimal.Decimal) decimal.Decimal
}
```

This scores a provider's ability to handle a specific amount in a specific
currency right now. The inputs are:

```
providerID:  Which provider (e.g., "onramp-gbp")
currency:    Which stablecoin (e.g., USDT)
amount:      How much is being transferred

Returns: 0.0 to 1.0
    1.0 = Provider has ample liquidity for this amount
    0.5 = Provider is getting thin
    0.0 = Provider is completely dry
```

### ReliabilityScorer

```go
type ReliabilityScorer interface {
    ReliabilityScore(ctx context.Context, providerID string) decimal.Decimal
}
```

This scores a provider's recent success/failure track record. Simpler than
the liquidity scorer -- it only needs the provider ID:

```
providerID:  Which provider

Returns: 0.0 to 1.0
    1.0 = 100% success rate in recent history
    0.5 = 50% success rate (half of transactions failing)
    0.0 = 100% failure rate (provider is down)
```

---

## Default Behavior: Optimistic Fallback

When no scorer is wired, the router uses 1.0 for both dimensions:

```go
// In scoreRoute():
liquidityScore := decimal.NewFromInt(1)
if r.liquidityScorer != nil {
    liquidityScore = r.liquidityScorer.LiquidityScore(
        ctx, route.OnRamp.ID(), route.Stable, amount)
    // Clamp to [0, 1]
    if liquidityScore.IsNegative() {
        liquidityScore = decimal.Zero
    } else if liquidityScore.GreaterThan(decimal.NewFromInt(1)) {
        liquidityScore = decimal.NewFromInt(1)
    }
}

reliabilityScore := decimal.NewFromInt(1)
if r.reliabilityScorer != nil {
    reliabilityScore = r.reliabilityScorer.ReliabilityScore(
        ctx, route.OnRamp.ID())
    if reliabilityScore.IsNegative() {
        reliabilityScore = decimal.Zero
    } else if reliabilityScore.GreaterThan(decimal.NewFromInt(1)) {
        reliabilityScore = decimal.NewFromInt(1)
    }
}
```

This design has three important properties:

1. **Graceful degradation**: The router works without any scorers. Cost and
   speed scoring alone produce reasonable results.

2. **Progressive enhancement**: Wire a scorer when you have the data to feed
   it. No code changes to the router required.

3. **Defensive clamping**: Even if a scorer returns a buggy value (negative
   or > 1.0), the router clamps it to [0, 1] before using it in the
   composite calculation.

---

## Injecting Scorers via RouterOption

Scorers are wired using the functional options pattern:

```go
type RouterOption func(*Router)

func WithLiquidityScorer(s LiquidityScorer) RouterOption {
    return func(r *Router) { r.liquidityScorer = s }
}

func WithReliabilityScorer(s ReliabilityScorer) RouterOption {
    return func(r *Router) { r.reliabilityScorer = s }
}
```

Usage at construction time:

```go
router := NewRouter(
    registry,
    tenantStore,
    logger,
    WithLiquidityScorer(myLiquidityScorer),
    WithReliabilityScorer(myReliabilityScorer),
)
```

Or without scorers (testing, early development):

```go
router := NewRouter(registry, tenantStore, logger)
// liquidityScorer = nil, reliabilityScorer = nil
// Both dimensions default to 1.0
```

---

## Impact of Scorers on Routing

To understand why scorers matter, consider a scenario with two on-ramp
providers for the same corridor (GBP to USDT):

### Without Scorers

```
Provider A:  fee=$1.00, estimated=60s
Provider B:  fee=$1.50, estimated=45s

Cost score A:   1 - 1.00/1000 = 0.999
Cost score B:   1 - 1.50/1000 = 0.9985
Speed score A:  1 - 120/3600  = 0.9667
Speed score B:  1 - 105/3600  = 0.9708

Composite A = (0.999 x 0.40) + (0.9667 x 0.30) + (1.0 x 0.20) + (1.0 x 0.10)
            = 0.3996 + 0.2900 + 0.2000 + 0.1000 = 0.9896

Composite B = (0.9985 x 0.40) + (0.9708 x 0.30) + (1.0 x 0.20) + (1.0 x 0.10)
            = 0.3994 + 0.2912 + 0.2000 + 0.1000 = 0.9906

Winner: B (slightly faster, slightly more expensive -- speed wins at 30% weight)
```

### With Reliability Scorer

Now suppose Provider B has been failing 30% of the time recently:

```
Reliability A: 0.98 (98% success rate)
Reliability B: 0.70 (70% success rate)

Composite A = (0.999 x 0.40) + (0.9667 x 0.30) + (1.0 x 0.20) + (0.98 x 0.10)
            = 0.3996 + 0.2900 + 0.2000 + 0.0980 = 0.9876

Composite B = (0.9985 x 0.40) + (0.9708 x 0.30) + (1.0 x 0.20) + (0.70 x 0.10)
            = 0.3994 + 0.2912 + 0.2000 + 0.0700 = 0.9606

Winner: A (reliability penalty drops B by 0.03)
```

The reliability scorer flipped the ranking. Without it, Settla would route
30% of traffic through a failing provider.

### With Liquidity Scorer

Suppose Provider A is running low on USDT liquidity for this amount:

```
Liquidity A: 0.30 (only 30% of typical capacity available)
Liquidity B: 0.95 (plenty of liquidity)

Composite A = (0.999 x 0.40) + (0.9667 x 0.30) + (0.30 x 0.20) + (0.98 x 0.10)
            = 0.3996 + 0.2900 + 0.0600 + 0.0980 = 0.8476

Composite B = (0.9985 x 0.40) + (0.9708 x 0.30) + (0.95 x 0.20) + (0.70 x 0.10)
            = 0.3994 + 0.2912 + 0.1900 + 0.0700 = 0.9506

Winner: B (liquidity penalty devastates A despite reliability issues)
```

The 20% weight on liquidity means a dry provider is heavily penalized. Even
Provider B's 30% failure rate is preferable to a near-empty Provider A.

---

## Designing a Reliability Scorer

A practical reliability scorer draws from the `provider_transactions` table.
Each transaction records the provider, status, and timestamp:

```
provider_transactions:
    provider_id   TEXT
    status        TEXT      -- "COMPLETED", "FAILED", "TIMEOUT"
    created_at    TIMESTAMP
```

### The Sliding Window Approach

```
                    SCORING WINDOW (last 1 hour)
    ├────────────────────────────────────────────┤
    |  success  success  FAIL  success  FAIL    |
    |  success  success  success  success       |
    ├────────────────────────────────────────────┤

    Total transactions:     10
    Successful:              8
    Failed:                  2

    Reliability = 8/10 = 0.80
```

### The Design

```go
type ProviderReliabilityScorer struct {
    store         ProviderTxStore
    window        time.Duration    // e.g., 1 hour
    minSamples    int              // minimum transactions before scoring
    cache         map[string]cachedScore
    cacheMu       sync.RWMutex
    cacheTTL      time.Duration    // e.g., 30 seconds
}

type cachedScore struct {
    score     decimal.Decimal
    expiresAt time.Time
}

type ProviderTxStore interface {
    CountRecentTransactions(ctx context.Context,
        providerID string, since time.Time) (total int, success int, err error)
}
```

### The Score Calculation

```go
func (s *ProviderReliabilityScorer) ReliabilityScore(
    ctx context.Context, providerID string) decimal.Decimal {

    // Check cache first
    s.cacheMu.RLock()
    if cached, ok := s.cache[providerID]; ok && time.Now().Before(cached.expiresAt) {
        s.cacheMu.RUnlock()
        return cached.score
    }
    s.cacheMu.RUnlock()

    // Query provider_transactions for the scoring window
    since := time.Now().Add(-s.window)
    total, success, err := s.store.CountRecentTransactions(ctx, providerID, since)
    if err != nil {
        // On error, return 1.0 (optimistic -- don't penalize on query failure)
        return decimal.NewFromInt(1)
    }

    // Not enough data -- return 1.0 (benefit of the doubt)
    if total < s.minSamples {
        return decimal.NewFromInt(1)
    }

    // Calculate score
    score := decimal.NewFromInt(int64(success)).
        Div(decimal.NewFromInt(int64(total)))

    // Cache the result
    s.cacheMu.Lock()
    s.cache[providerID] = cachedScore{
        score:     score,
        expiresAt: time.Now().Add(s.cacheTTL),
    }
    s.cacheMu.Unlock()

    return score
}
```

### Design Decisions

1. **Minimum sample size**: With fewer than `minSamples` transactions in the
   window, the score defaults to 1.0. A single failure on a new provider
   should not give it a 0.0 score.

2. **Cache with TTL**: The reliability score does not need to be real-time.
   A 30-second cache means we query the database at most twice per minute
   per provider, not once per route evaluation.

3. **Optimistic on error**: If the database query fails, return 1.0 rather
   than 0.0. Do not penalize a provider because of a scoring infrastructure
   failure.

4. **Sliding window**: Use the last hour, not lifetime statistics. A provider
   that had issues last week but is healthy now should not be penalized.

---

## Designing a Liquidity Scorer

The liquidity scorer is more complex because it depends on the amount:

```go
type ProviderLiquidityScorer struct {
    treasury domain.TreasuryManager
    cache    map[string]cachedLiquidity
    cacheMu  sync.RWMutex
    cacheTTL time.Duration
}

type cachedLiquidity struct {
    available decimal.Decimal
    expiresAt time.Time
}

func (s *ProviderLiquidityScorer) LiquidityScore(
    ctx context.Context, providerID string,
    currency domain.Currency, amount decimal.Decimal) decimal.Decimal {

    // Get the provider's available liquidity from treasury
    available := s.getAvailableLiquidity(ctx, providerID, currency)

    // Score = min(available / amount, 1.0)
    if amount.IsZero() {
        return decimal.NewFromInt(1)
    }

    score := available.Div(amount)
    if score.GreaterThan(decimal.NewFromInt(1)) {
        return decimal.NewFromInt(1)
    }
    if score.IsNegative() {
        return decimal.Zero
    }
    return score
}
```

The intuition: if the provider has 5x the requested amount available, score
is 1.0 (clamped). If it has exactly the requested amount, score is 1.0. If
it has half the requested amount, score is 0.5. If it has nothing, score
is 0.0.

---

## Scorer Composition

Multiple scorers can coexist. The router independently queries each one:

```
                        scoreRoute()
                            |
              +─────────────+─────────────+
              |             |             |
         costScore     speedScore    liquidityScore   reliabilityScore
         (computed     (computed     (from scorer     (from scorer
          directly)     directly)     or 1.0)          or 1.0)
              |             |             |             |
              v             v             v             v
         x 0.40        x 0.30        x 0.20        x 0.10
              |             |             |             |
              +─────────────+─────────────+─────────────+
                                |
                           COMPOSITE
```

Scorers are independent -- they do not know about each other. The router
combines their outputs via the weighted sum formula. This makes it easy to
add new scoring dimensions:

```go
// Future: Add a compliance scorer
type ComplianceScorer interface {
    ComplianceScore(ctx context.Context, providerID string,
        sourceCurrency, destCurrency domain.Currency) decimal.Decimal
}

// In scoreRoute():
complianceScore := decimal.NewFromInt(1)
if r.complianceScorer != nil {
    complianceScore = r.complianceScorer.ComplianceScore(...)
}

// Adjust weights to sum to 1.0:
// cost=0.35, speed=0.25, liquidity=0.20, reliability=0.10, compliance=0.10
```

---

## Testing Scorers

Scorers should be tested in isolation and then integration-tested with the
router:

### Unit Test Pattern

```go
func TestReliabilityScorer_HighSuccess(t *testing.T) {
    store := &mockTxStore{
        counts: map[string]txCounts{
            "provider-a": {total: 100, success: 98},
        },
    }
    scorer := NewReliabilityScorer(store, 1*time.Hour, 10, 30*time.Second)

    score := scorer.ReliabilityScore(context.Background(), "provider-a")

    expected := decimal.NewFromFloat(0.98)
    if !score.Equal(expected) {
        t.Errorf("expected %s, got %s", expected, score)
    }
}

func TestReliabilityScorer_InsufficientData(t *testing.T) {
    store := &mockTxStore{
        counts: map[string]txCounts{
            "provider-a": {total: 3, success: 1},  // below minSamples
        },
    }
    scorer := NewReliabilityScorer(store, 1*time.Hour, 10, 30*time.Second)

    score := scorer.ReliabilityScore(context.Background(), "provider-a")

    // Should return 1.0 (benefit of the doubt)
    if !score.Equal(decimal.NewFromInt(1)) {
        t.Errorf("expected 1.0 for insufficient data, got %s", score)
    }
}
```

### Integration Test with Router

```go
func TestRouterWithReliabilityScorer(t *testing.T) {
    scorer := &fixedReliabilityScorer{
        scores: map[string]decimal.Decimal{
            "provider-a": decimal.NewFromFloat(0.95),
            "provider-b": decimal.NewFromFloat(0.50),
        },
    }
    router := NewRouter(registry, tenants, logger,
        WithReliabilityScorer(scorer))

    result, err := router.Route(ctx, req)
    // Verify that provider-a is preferred despite similar cost/speed
    if result.ProviderID != "provider-a" {
        t.Errorf("expected provider-a (reliability 0.95), got %s", result.ProviderID)
    }
}
```

---

## Benchmark Impact

From `rail/router/bench_test.go`, scoring in isolation:

```
BenchmarkScoreRoute             <1us/op     (without scorers)
BenchmarkScoreRouteConcurrent   >100,000 scores/sec
```

Adding scorers introduces:
- Cache lookup: ~50ns (map access under RWMutex)
- Cache miss (DB query): ~1-5ms (amortized over 30s TTL)

Net impact on scoring: negligible when cached (50ns on a 1us operation).
The cache TTL ensures the DB is queried at most 2x/minute per provider.

---

## Key Insight

> The pluggable scoring system follows the **Open/Closed Principle**: the
> router is open for extension (wire new scorers) but closed for modification
> (the scoring loop does not change). The nil-check fallback to 1.0 means
> the system works without any scorers, making them a progressive enhancement
> rather than a hard dependency.

---

## Common Mistakes

1. **Returning 0.0 on insufficient data.** A brand-new provider with zero
   transactions should score 1.0 (benefit of the doubt), not 0.0. Returning
   0.0 means no traffic is ever routed to it, so it never builds a track
   record.

2. **Querying the database on every score call.** At 5,000 TPS with 4
   candidate routes each, that is 20,000 scorer calls per second. Use a cache
   with a reasonable TTL (30s-60s).

3. **Not clamping to [0, 1].** The router clamps defensively, but the scorer
   should also self-clamp. Defense in depth.

4. **Using lifetime statistics instead of a sliding window.** A provider
   that failed 50% of the time six months ago but has been perfect for the
   last month should not carry a 0.75 score. Use a window (1 hour, 1 day).

5. **Coupling scorer to router internals.** Scorers receive only the
   `providerID` and (for liquidity) `currency` and `amount`. They should not
   know about routes, candidates, or scoring weights.

---

## Exercises

### Exercise 4.6.1: Implement a Reliability Scorer

Build a complete `ProviderReliabilityScorer` that:

1. Implements the `ReliabilityScorer` interface
2. Queries a mock store for transaction counts (total and successful)
3. Uses a 1-hour sliding window
4. Requires a minimum of 20 samples before scoring (returns 1.0 otherwise)
5. Caches scores for 30 seconds
6. Returns 1.0 on database errors

Write tests for:
- Provider with 100% success rate
- Provider with 70% success rate
- Provider with insufficient data (< 20 transactions)
- Cache hit (second call within TTL returns cached value)
- Database error (returns 1.0)
- Concurrent calls (no race conditions)

### Exercise 4.6.2: Weighted Window Scorer

Design a reliability scorer that weights recent transactions more heavily
than older ones. For example, transactions in the last 10 minutes count
double compared to transactions from 50 minutes ago.

Define:
1. The weighting function
2. The data structure for efficient weighted counting
3. How this changes the cache invalidation strategy

### Exercise 4.6.3: Liquidity Scorer with Treasury Integration

Design a liquidity scorer that reads from Settla's treasury manager. The
scorer should:

1. Map each provider to its treasury position(s)
2. Query `Available()` for the relevant position
3. Score as `min(available / amount, 1.0)`
4. Handle the case where a provider has multiple positions (sum them)

How do you handle the circular dependency? The router needs the scorer, and
the scorer needs the treasury manager. Draw the dependency graph and propose
a solution.

### Exercise 4.6.4: Dynamic Weight Adjustment

Design a system where scoring weights are per-tenant rather than global.
A tenant that prioritizes cost over speed would configure `cost=0.60,
speed=0.15, liquidity=0.15, reliability=0.10`.

What changes are needed to:
1. The `Tenant` domain model
2. The `scoreRoute()` function
3. The `CoreRouterAdapter`

What are the risks of per-tenant weights?

---

## Module Summary

This completes Module 4: Treasury & Smart Routing. Here is what we covered:

```
Chapter 4.1  The Hot-Key Problem
             Why SELECT FOR UPDATE fails at 5,000 TPS
             Why sharding does not solve treasury contention
             The insight: move hot state to memory

Chapter 4.2  In-Memory Treasury Manager
             PositionState with atomic int64 counters
             Micro-unit representation (10^6)
             The Reserve() CAS loop
             Why CAS beats mutex

Chapter 4.3  Crash Recovery
             WAL pattern with logOp()
             Sync flush for large amounts (>=$100K)
             Background flush goroutine (100ms)
             Startup replay with LoadPositions()

Chapter 4.4  Smart Routing
             Scoring algorithm: cost, speed, liquidity, reliability
             Weighted composite scoring
             Worked example: 3 routes for GBP to NGN
             Fallback alternatives

Chapter 4.5  Provider Adapters
             OnRampProvider / OffRampProvider interfaces
             ProviderRegistry and capability discovery
             Per-tenant fee wrapping via CoreRouterAdapter
             How to add a new provider

Chapter 4.6  Pluggable Scoring
             LiquidityScorer / ReliabilityScorer interfaces
             Default 1.0 fallback
             Building scorers from provider_transactions
             Hands-on: implement a reliability scorer
```

---

## What's Next

Module 5 covers the **Outbox Pattern & Worker Architecture**: the
transactional outbox that connects the settlement engine to the NATS-based
worker system, stream definitions, consumer configuration, and the
CHECK-BEFORE-CALL pattern that prevents double-execution.

---

## Further Reading

- `rail/router/router.go` lines 46-68 for the scorer interfaces and options
- `rail/router/router.go` `scoreRoute()` for the full scoring implementation
- `rail/router/bench_test.go` for scoring performance benchmarks
- Martin Fowler, "Plugin" pattern in *Patterns of Enterprise Application Architecture*
