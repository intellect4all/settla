# Chapter 4.4: Smart Routing

**Reading time:** ~25 minutes
**Prerequisites:** Chapter 4.3 (Crash Recovery), basic understanding of scoring algorithms
**Code references:** `rail/router/router.go`, `rail/router/bench_test.go`, `domain/provider.go`

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Describe the full route selection pipeline: enumerate, score, sort, select
2. Explain each scoring weight and its normalization to [0, 1]
3. Trace the `scoreRoute()` function through a real-world example
4. Calculate composite scores for candidate routes by hand
5. Explain how fallback alternatives are selected and packaged

---

## The Routing Problem

A Settla transfer from GBP to NGN does not move money directly. It flows
through a three-hop corridor:

```
GBP (fiat) ──> USDT (stablecoin on-chain) ──> NGN (fiat)
     |                    |                         |
  On-Ramp             Blockchain                Off-Ramp
  Provider             Transfer                 Provider
```

At each hop, there are multiple providers and chains to choose from:

```
On-Ramps:     onramp-gbp (GBP→USDT), onramp-ngn (NGN→USDT)
Off-Ramps:    offramp-ngn (USDT→NGN), offramp-gbp (USDT→GBP)
Chains:       tron (gas: $0.50), ethereum (gas: $2.50)
Stablecoins:  USDT, USDC
```

The router must select the optimal combination across all dimensions.

---

## The Router Struct

```go
type Router struct {
    registry          domain.ProviderRegistry
    tenants           TenantStore
    logger            *slog.Logger
    liquidityScorer   LiquidityScorer   // optional -- nil = assume 1.0
    reliabilityScorer ReliabilityScorer // optional -- nil = assume 1.0
}

var _ domain.Router = (*Router)(nil)
```

The router depends only on interfaces. `ProviderRegistry` provides access to
on-ramps, off-ramps, and blockchain clients. The scorers are optional plugins
(covered in Chapter 4.6).

---

## Route Selection Pipeline

The `Route()` method orchestrates four steps:

```go
func (r *Router) Route(ctx context.Context,
    req domain.RouteRequest) (*domain.RouteResult, error) {

    // Step 1: Build all viable candidates
    candidates, err := r.buildCandidates(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("settla-rail: building route candidates: %w", err)
    }
    if len(candidates) == 0 {
        return nil, fmt.Errorf(
            "settla-rail: no routes available for %s→%s",
            req.SourceCurrency, req.TargetCurrency)
    }

    // Step 2: Sort by score descending (highest = best)
    sort.Slice(candidates, func(i, j int) bool {
        return candidates[i].Score.GreaterThan(candidates[j].Score)
    })

    // Step 3: Select the best
    best := candidates[0]

    // Step 4: Package up to 2 fallback alternatives
    var alternatives []domain.RouteAlternative
    for i := 1; i < len(candidates) && len(alternatives) < 2; i++ {
        // ... build alternative from candidates[i]
    }

    return &domain.RouteResult{
        ProviderID:       best.OnRamp.ID(),
        OffRampProvider:  best.OffRamp.ID(),
        BlockchainChain:  best.Chain,
        // ... all fields
        Alternatives:     alternatives,
    }, nil
}
```

```
                    +──────────────────+
                    |  RouteRequest    |
                    |  GBP → NGN      |
                    |  Amount: 1000   |
                    +──────────────────+
                             |
                    Step 1: buildCandidates
                             |
          +──────────────────+──────────────────+
          |                  |                  |
    Route A              Route B            Route C
    onramp-gbp           onramp-gbp         onramp-gbp
    tron/USDT            eth/USDT           tron/USDC
    offramp-ngn          offramp-ngn        offramp-ngn
    score: 0.89          score: 0.82        score: 0.87
          |                  |                  |
                    Step 2: Sort
                             |
          +──────────────────+──────────────────+
          |                  |                  |
    Route A (0.89)     Route C (0.87)     Route B (0.82)
       BEST             Alt #1             Alt #2
```

---

## Step 1: Building Candidates

The `buildCandidates` method enumerates every combination of stablecoin,
on-ramp, off-ramp, and chain:

```go
func (r *Router) buildCandidates(ctx context.Context,
    req domain.RouteRequest) ([]Route, error) {

    onRampIDs := r.registry.ListOnRampIDs(ctx)
    offRampIDs := r.registry.ListOffRampIDs(ctx)
    chains := r.registry.ListBlockchainChains()
    stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}

    var candidates []Route

    for _, stable := range stables {
        for _, onID := range onRampIDs {
            onRamp, err := r.registry.GetOnRamp(onID)
            if err != nil { continue }

            // Does the on-ramp support source→stable?
            onQuote, err := onRamp.GetQuote(ctx, domain.QuoteRequest{
                SourceCurrency: req.SourceCurrency,
                SourceAmount:   req.Amount,
                DestCurrency:   stable,
            })
            if err != nil { continue }  // Unsupported pair, skip

            for _, offID := range offRampIDs {
                offRamp, err := r.registry.GetOffRamp(offID)
                if err != nil { continue }

                // Does the off-ramp support stable→target?
                offQuote, err := offRamp.GetQuote(ctx, domain.QuoteRequest{
                    SourceCurrency: stable,
                    SourceAmount:   req.Amount.Mul(onQuote.Rate).Sub(onQuote.Fee),
                    DestCurrency:   req.TargetCurrency,
                })
                if err != nil { continue }

                for _, chain := range chains {
                    bc, err := r.registry.GetBlockchain(chain)
                    if err != nil { continue }

                    gasFee, err := bc.EstimateGas(ctx, domain.TxRequest{
                        Token:  string(stable),
                        Amount: req.Amount.Mul(onQuote.Rate),
                    })
                    if err != nil { continue }

                    route := Route{
                        OnRamp: onRamp, OffRamp: offRamp,
                        Chain: chain, Stable: stable,
                        OnQuote: onQuote, OffQuote: offQuote,
                        GasFee: gasFee,
                    }
                    route.Score, route.ScoreBreakdown = r.scoreRoute(
                        ctx, route, req.Amount)
                    candidates = append(candidates, route)
                }
            }
        }
    }
    return candidates, nil
}
```

The total number of candidates is:

```
|stablecoins| x |on-ramps| x |off-ramps| x |chains|

With the benchmark setup:
2 stablecoins x 2 on-ramps x 2 off-ramps x 2 chains = 16 combinations

But many are invalid (wrong currency pairs), so the actual count is lower.
Only combinations where the on-ramp supports source→stable AND the off-ramp
supports stable→target survive the quote step.
```

Unsupported combinations are filtered by the `GetQuote` error handling
(`continue` on error). This is not an exception-based flow -- it is the
normal mechanism for discovering which providers serve which corridors.

---

## Step 2: The Scoring Algorithm

The scoring weights are defined as module-level decimals:

```go
var (
    weightCost        = decimal.NewFromFloat(0.40)  // 40%
    weightSpeed       = decimal.NewFromFloat(0.30)  // 30%
    weightLiquidity   = decimal.NewFromFloat(0.20)  // 20%
    weightReliability = decimal.NewFromFloat(0.10)  // 10%
)
```

The full `scoreRoute` function:

```go
func (r *Router) scoreRoute(ctx context.Context, route Route,
    amount decimal.Decimal) (decimal.Decimal, domain.ScoreBreakdown) {

    totalFee := route.OnQuote.Fee.Add(route.GasFee).Add(route.OffQuote.Fee)

    // Cost score: 1 - (fee / amount), clamped to [0, 1]
    costScore := decimal.NewFromInt(1)
    if amount.IsPositive() {
        feeRatio := totalFee.Div(amount)
        costScore = decimal.NewFromInt(1).Sub(feeRatio)
        if costScore.IsNegative() {
            costScore = decimal.Zero
        }
    }

    // Speed score: 1 - (seconds / 3600), clamped to [0, 1]
    totalSeconds := route.OnQuote.EstimatedSeconds + route.OffQuote.EstimatedSeconds
    speedScore := decimal.NewFromInt(1).Sub(
        decimal.NewFromInt(int64(totalSeconds)).Div(decimal.NewFromInt(3600)),
    )
    if speedScore.IsNegative() {
        speedScore = decimal.Zero
    }

    // Liquidity score: from scorer if available, else 1.0
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

    // Reliability score: from scorer if available, else 1.0
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

    breakdown := domain.ScoreBreakdown{
        Cost:        costScore,
        Speed:       speedScore,
        Liquidity:   liquidityScore,
        Reliability: reliabilityScore,
    }

    composite := costScore.Mul(weightCost).
        Add(speedScore.Mul(weightSpeed)).
        Add(liquidityScore.Mul(weightLiquidity)).
        Add(reliabilityScore.Mul(weightReliability))

    return composite, breakdown
}
```

### Score Component Formulas

Each component is normalized to [0, 1] where 1 is best:

```
COST SCORE = 1 - (totalFee / amount)

    Intuition: If fees are 0, cost score = 1.0 (perfect).
               If fees equal the amount, cost score = 0.0 (terrible).
               If fees exceed the amount, clamped to 0.0.

SPEED SCORE = 1 - (totalSeconds / 3600)

    Intuition: If settlement takes 0 seconds, speed score = 1.0.
               If settlement takes 1 hour (3600s), speed score = 0.0.
               If settlement takes > 1 hour, clamped to 0.0.

LIQUIDITY SCORE = LiquidityScorer.LiquidityScore() or 1.0

    Intuition: 1.0 = provider has ample liquidity for this amount.
               0.0 = provider is dry. Clamped to [0, 1].

RELIABILITY SCORE = ReliabilityScorer.ReliabilityScore() or 1.0

    Intuition: 1.0 = 100% success rate in recent history.
               0.0 = 100% failure rate. Clamped to [0, 1].
```

The composite score is a weighted sum:

```
COMPOSITE = (cost x 0.40) + (speed x 0.30) + (liquidity x 0.20) + (reliability x 0.10)
```

---

## Worked Example: Scoring 3 Routes for GBP to NGN

A Lemfi tenant sends 1,000 GBP to Nigeria. Three candidate routes survive
the quote phase:

### Route A: onramp-gbp / tron / offramp-ngn / USDT

```
On-ramp quote:    rate=1.25, fee=$1.00, estimated=60s
Off-ramp quote:   rate=830.0, fee=$0.50, estimated=60s
Blockchain:       tron, gas=$0.50
```

### Route B: onramp-gbp / ethereum / offramp-ngn / USDT

```
On-ramp quote:    rate=1.25, fee=$1.00, estimated=60s
Off-ramp quote:   rate=830.0, fee=$0.50, estimated=60s
Blockchain:       ethereum, gas=$2.50
```

### Route C: onramp-gbp / tron / offramp-ngn / USDC

```
On-ramp quote:    rate=1.24, fee=$1.50, estimated=90s
Off-ramp quote:   rate=828.0, fee=$0.75, estimated=90s
Blockchain:       tron, gas=$0.50
```

### Step-by-Step Scoring

**Route A:**

```
Total fee = 1.00 + 0.50 + 0.50 = $2.00
Cost score = 1 - (2.00 / 1000) = 1 - 0.002 = 0.998

Total seconds = 60 + 60 = 120
Speed score = 1 - (120 / 3600) = 1 - 0.0333 = 0.9667

Liquidity score = 1.0  (no scorer wired)
Reliability score = 1.0  (no scorer wired)

Composite = (0.998 x 0.40) + (0.9667 x 0.30) + (1.0 x 0.20) + (1.0 x 0.10)
          = 0.3992 + 0.2900 + 0.2000 + 0.1000
          = 0.9892
```

**Route B:**

```
Total fee = 1.00 + 2.50 + 0.50 = $4.00
Cost score = 1 - (4.00 / 1000) = 0.996

Total seconds = 60 + 60 = 120
Speed score = 0.9667  (same providers, same speed)

Composite = (0.996 x 0.40) + (0.9667 x 0.30) + (1.0 x 0.20) + (1.0 x 0.10)
          = 0.3984 + 0.2900 + 0.2000 + 0.1000
          = 0.9884
```

**Route C:**

```
Total fee = 1.50 + 0.50 + 0.75 = $2.75
Cost score = 1 - (2.75 / 1000) = 0.99725

Total seconds = 90 + 90 = 180
Speed score = 1 - (180 / 3600) = 1 - 0.05 = 0.95

Composite = (0.99725 x 0.40) + (0.95 x 0.30) + (1.0 x 0.20) + (1.0 x 0.10)
          = 0.3989 + 0.2850 + 0.2000 + 0.1000
          = 0.9839
```

### Final Ranking

```
Rank  Route   Composite  Cost     Speed    Why?
────  ─────   ─────────  ────     ─────    ────
1     A       0.9892     0.998    0.967    Cheapest (tron gas) + fastest
2     B       0.9884     0.996    0.967    Higher gas (ethereum) slightly penalized
3     C       0.9839     0.997    0.950    Slower + slightly more expensive
```

Route A wins because Tron's gas fee ($0.50) is much lower than Ethereum's
($2.50), and the USDT providers are faster than the USDC providers.

The spread is small (0.9892 vs 0.9884) because for a $1,000 transfer, even
$2.50 in extra gas is only 0.25% of the amount. For a $10 transfer, the
spread would be much larger.

---

## Fallback Alternatives

The router packages up to 2 alternatives alongside the primary route. These
travel in the outbox payload so that if the primary route's provider fails,
the worker can retry with an alternative without round-tripping back through
the engine:

```go
var alternatives []domain.RouteAlternative
for i := 1; i < len(candidates) && len(alternatives) < 2; i++ {
    alt := candidates[i]
    altFee := alt.OnQuote.Fee.Add(alt.GasFee).Add(alt.OffQuote.Fee)
    altStable := req.Amount.Mul(alt.OnQuote.Rate).Sub(alt.OnQuote.Fee).Round(8)
    if !altStable.IsPositive() {
        continue  // Skip routes where fees exceed the amount
    }
    alternatives = append(alternatives, domain.RouteAlternative{
        OnRampProvider:  alt.OnRamp.ID(),
        OffRampProvider: alt.OffRamp.ID(),
        Chain:           alt.Chain,
        StableCoin:      alt.Stable,
        Fee:             domain.Money{Amount: altFee, Currency: domain.CurrencyUSD},
        Rate:            alt.OnQuote.Rate.Mul(alt.OffQuote.Rate),
        StableAmount:    altStable,
        Score:           alt.Score,
        ScoreBreakdown:  alt.ScoreBreakdown,
    })
}
```

The `RouteAlternative` struct carries everything needed to execute:

```go
type RouteAlternative struct {
    OnRampProvider  string          `json:"on_ramp_provider"`
    OffRampProvider string          `json:"off_ramp_provider"`
    Chain           string          `json:"chain"`
    StableCoin      Currency        `json:"stablecoin"`
    Fee             Money           `json:"fee"`
    Rate            decimal.Decimal `json:"rate"`
    StableAmount    decimal.Decimal `json:"stable_amount"`
    Score           decimal.Decimal `json:"score"`
    ScoreBreakdown  ScoreBreakdown  `json:"score_breakdown"`
}
```

---

## Decimal-Only Scoring

All scoring uses `shopspring/decimal`, not `float64`. This is a deliberate
choice:

```go
// These are decimal.Decimal, not float64:
weightCost        = decimal.NewFromFloat(0.40)
weightSpeed       = decimal.NewFromFloat(0.30)
weightLiquidity   = decimal.NewFromFloat(0.20)
weightReliability = decimal.NewFromFloat(0.10)
```

Why does this matter for scoring? Consider fee ratio calculation:

```go
feeRatio := totalFee.Div(amount)
costScore = decimal.NewFromInt(1).Sub(feeRatio)
```

With float64:
```
fee = 2.00, amount = 1000.00
feeRatio = 0.0020000000000000000416...  (IEEE 754 representation error)
```

With decimal:
```
feeRatio = 0.002  (exact)
```

For scoring, the float64 error is negligible. But Settla enforces decimal-only
math everywhere as a project invariant (Critical Invariant #1), and the router
is no exception. This eliminates an entire category of "works 99.99% of the
time" bugs.

---

## Performance

From `rail/router/bench_test.go`:

```
BenchmarkRoute                <100us/op    (full pipeline: build + score + sort)
BenchmarkRoute_MultiChain     <100us/op    (with multiple blockchain options)
BenchmarkRouteConcurrent      >5,000 routes/sec total
BenchmarkScoreRoute           <1us/op      (scoring in isolation)
BenchmarkScoreRouteConcurrent >100,000 scores/sec
BenchmarkGetQuote             <200us/op    (routing + tenant fee calculation)
```

The router is fast enough that it is never a bottleneck, even at 5,000 TPS.
Each route evaluation touches only in-memory data structures (provider quotes
are mocked in benchmarks; in production, providers cache quotes).

---

## Key Insight

> The router treats provider selection as a multi-objective optimization
> problem with four dimensions (cost, speed, liquidity, reliability),
> normalized to a common scale [0, 1] and combined via configurable weights.
> This makes the scoring transparent, debuggable (every score includes a
> breakdown), and extensible (new dimensions can be added as scorers).

---

## Common Mistakes

1. **Using float64 for scores.** Even though scoring precision is less critical
   than monetary math, mixing float64 and decimal in the same codebase creates
   conversion bugs. Stay decimal throughout.

2. **Not clamping scores to [0, 1].** A buggy liquidity scorer might return
   1.5 or -0.3. The router explicitly clamps all external scores.

3. **Hardcoding corridor logic.** The router does not know which corridors
   exist. It discovers them by trying every combination and letting `GetQuote`
   errors filter out unsupported pairs. This makes adding new corridors a
   provider-level change, not a router-level change.

4. **Ignoring the stablecoin amount positivity check.** After fees, the
   intermediate stablecoin amount might be zero or negative for small
   transfers. The router checks `!stableAmount.IsPositive()` and rejects
   such routes.

5. **Scoring speed linearly above 1 hour.** Speeds above 3600 seconds are
   clamped to 0.0. A settlement that takes 2 hours is not "twice as bad"
   as one hour in Settla's model -- both are equally unacceptable.

---

## Exercises

### Exercise 4.4.1: Score Sensitivity Analysis

Using the worked example routes, calculate the composite score for Route A
with a $10 transfer instead of $1,000. How does the fee-to-amount ratio
change? At what transfer amount does Route A's cost score drop below 0.95?

### Exercise 4.4.2: Weight Rebalancing

A fintech tells you they care more about speed than cost because their
customers are sending urgent remittances. Propose new weights and re-score
the three example routes. Does the ranking change?

### Exercise 4.4.3: Add a New Scoring Dimension

Design a "compliance score" that penalizes routes through jurisdictions with
weak AML regulations. Define:
1. The score formula (what inputs, how normalized)
2. Where the weight comes from (global? per-tenant?)
3. How you would modify `scoreRoute()` to include it

### Exercise 4.4.4: Benchmark Route Evaluation

Using the benchmark setup from `bench_test.go`, add a benchmark that
measures routing with 5 on-ramps, 5 off-ramps, and 4 chains (100 potential
candidates). Does the <100us target still hold?

---

## What's Next

Chapter 4.5 examines the provider interfaces (`OnRampProvider`,
`OffRampProvider`, `BlockchainClient`) and the `ProviderRegistry` that
manages them. We explore how to add a new provider to the system without
modifying the router.

---

## Further Reading

- `rail/router/router.go` for the complete routing implementation
- `domain/provider.go` for `RouteResult`, `ScoreBreakdown`, `RouteAlternative`
- `rail/router/bench_test.go` for the full benchmark suite
- `domain/provider.go` `Corridor` type for the `GBP→USDT→NGN` format
