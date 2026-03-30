# Chapter 0.3: Foreign Exchange -- How Rates Work, Who Sets Them, and Where the Money Is

**Estimated reading time:** 25 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain how FX rates are determined (interbank market, bid/ask spread)
2. Calculate the real cost of an FX conversion including spread and markup
3. Distinguish between mid-market rate, retail rate, and provider rate
4. Explain basis points and how fee schedules work in settlement
5. Understand rate volatility and slippage risk during settlement

---

## The FX Market

The foreign exchange market is the largest financial market on Earth. It trades approximately $7.5 trillion per day -- more than every stock exchange combined. If you are building settlement infrastructure, every transfer you process touches this market at least once, usually twice (on-ramp and off-ramp).

Understanding how FX works is not optional. It is the difference between a profitable settlement platform and one that bleeds money on every transfer.

### Market Structure

The FX market is **decentralized**. There is no single exchange, no opening bell, no closing price. Instead, it is a network of participants trading directly with each other:

```
    THE FOREIGN EXCHANGE MARKET
    ===========================

    Tier 1: Interbank Market
    +-----------------------------------------------------------+
    |  JPMorgan  <-->  Citi  <-->  Deutsche Bank  <-->  HSBC    |
    |                                                           |
    |  These ~15 banks handle 60%+ of daily FX volume.          |
    |  They trade with each other at "interbank" rates.         |
    |  Minimum trade: typically $1M+ per transaction.           |
    +-----------------------------------------------------------+
                          |
                          | (markup applied)
                          v
    Tier 2: Institutional Market
    +-----------------------------------------------------------+
    |  Hedge Funds  |  Asset Managers  |  Corporate Treasuries  |
    |  Brokers      |  Payment Cos     |  Central Banks         |
    |                                                           |
    |  Access interbank rates + small spread via prime brokers. |
    |  Trade sizes: $100K to $100M.                             |
    +-----------------------------------------------------------+
                          |
                          | (further markup applied)
                          v
    Tier 3: Retail / Provider Market
    +-----------------------------------------------------------+
    |  Wise  |  Revolut  |  On-ramp providers  |  Off-ramp     |
    |  Banks |  Fintechs |  Settlement infra   |  providers    |
    |                                                           |
    |  Get rates from Tier 1/2, add their own spread/fees.     |
    |  This is where Settla's providers operate.                |
    +-----------------------------------------------------------+
                          |
                          | (Settla adds basis point fees)
                          v
    Tier 4: End Users
    +-----------------------------------------------------------+
    |  Lemfi's customers  |  Fincra's merchants  |  End users  |
    |                                                           |
    |  See a single "exchange rate" that includes all markups.  |
    +-----------------------------------------------------------+
```

The market runs **24 hours a day, 5 days a week** -- from Sunday 5:00 PM EST (Wellington opens) to Friday 5:00 PM EST (New York closes). It follows the sun across time zones:

```
    FX Market Hours (UTC)
    =====================

    00:00   04:00   08:00   12:00   16:00   20:00   00:00
    |-------|-------|-------|-------|-------|-------|
    |  Sydney/Tokyo Session  |
    |       |  London Session       |
    |               |  New York Session     |
    |                                               |
              ^                ^
              |                |
         Highest liquidity     Second highest
         (London + NY overlap) (London + Tokyo overlap)
         12:00 - 16:00 UTC    07:00 - 09:00 UTC
```

> **Key Insight:** Liquidity varies throughout the day. The London/New York overlap (12:00-16:00 UTC) has the tightest spreads and deepest liquidity. If your settlement platform processes transfers during the Asian session (00:00-08:00 UTC) for major currency pairs like GBP/USD, spreads will be wider and providers may quote worse rates. This is not academic -- at 50M transfers/day, the time-of-day effect on spreads can cost or save millions annually.

### Currency Pairs

FX rates are always quoted as **pairs**. The pair notation tells you how much of the **quote currency** you need to buy one unit of the **base currency**.

```
    GBP/USD = 1.2700
    ^^^  ^^^   ^^^^^^
    |    |     |
    |    |     +-- Rate: 1 GBP costs 1.2700 USD
    |    +-------- Quote currency (what you pay)
    +------------- Base currency (what you get)

    Reading it: "1 GBP equals 1.2700 USD"
    Or:         "It costs $1.27 to buy 1 pound"
```

The convention matters because inverting the pair inverts the math:

```
    GBP/USD = 1.2700  means  "1 GBP buys 1.2700 USD"
    USD/GBP = 0.7874  means  "1 USD buys 0.7874 GBP"

    These are reciprocals: 1 / 1.2700 = 0.7874
```

In settlement, you will deal with **cross rates** -- rates that do not involve USD directly. For example, GBP/NGN is not directly traded on the interbank market. Instead, it is derived:

```
    GBP/NGN (cross rate) = GBP/USD x USD/NGN
                         = 1.2700  x 1,580.00
                         = 2,006.60

    This means: 1 GBP buys 2,006.60 NGN
    But the cross-rate calculation introduces TWO spreads,
    one from each leg.
```

Settla's corridor model (`domain.Corridor`) makes this explicit. Every transfer goes through a stablecoin bridge, which means every transfer involves two FX conversions:

```go
// From domain/provider.go
type Corridor struct {
    SourceCurrency Currency    // GBP
    StableCoin     Currency    // USDT (pegged to USD)
    DestCurrency   Currency    // NGN
}
```

The stablecoin in the middle (USDT, pegged to USD) is effectively the USD leg of a cross-rate calculation. The two conversions are:

1. **On-ramp**: GBP -> USDT (effectively GBP/USD)
2. **Off-ramp**: USDT -> NGN (effectively USD/NGN)

---

## How Rates Are Set

There is no single "correct" FX rate. What exists is a continuous stream of prices at which market participants are willing to trade. The key rates you need to understand:

### The Mid-Market Rate

The **mid-market rate** (also called the interbank rate or mid-rate) is the midpoint between the best available buy price and the best available sell price on the wholesale market. This is the rate you see on Google, XE, or Wise's comparison page.

```
    Best bid (someone willing to buy GBP): 1.2698
    Best ask (someone willing to sell GBP): 1.2702

    Mid-market rate = (1.2698 + 1.2702) / 2 = 1.2700
```

The mid-market rate is a **reference point**, not a rate you can actually trade at. No dealer will give you this rate because they would make zero profit. It exists to measure how much markup everyone else charges.

### Bid and Ask

Every market participant quotes two prices:

- **Bid**: the price at which the dealer will **buy** the base currency from you
- **Ask** (or offer): the price at which the dealer will **sell** the base currency to you

The dealer always makes money: they buy low (bid) and sell high (ask).

```
    GBP/USD Dealer Quotes
    ======================

    Mid-market rate: 1.2700

    Dealer's bid:    1.2680  (they buy GBP from you at this rate)
    Dealer's ask:    1.2720  (they sell GBP to you at this rate)

    Spread = Ask - Bid = 1.2720 - 1.2680 = 0.0040

    If you SELL GBP 1,000 to the dealer (converting GBP to USD):
      At mid-market:    GBP 1,000 x 1.2700 = $1,270.00
      At dealer's bid:  GBP 1,000 x 1.2680 = $1,268.00
      Hidden cost:      $2.00 (you got $2 less than mid-market)

    If you BUY GBP 1,000 from the dealer (converting USD to GBP):
      At mid-market:    GBP 1,000 x 1.2700 = $1,270.00 cost
      At dealer's ask:  GBP 1,000 x 1.2720 = $1,272.00 cost
      Hidden cost:      $2.00 (you paid $2 more than mid-market)
```

### The Spread

The **spread** is the difference between bid and ask. It represents the dealer's profit margin and is the most fundamental cost in FX.

Spreads are measured in **pips** (percentage in point). For most currency pairs, 1 pip = 0.0001 (the fourth decimal place). For JPY pairs, 1 pip = 0.01.

```
    Spread Examples by Pair and Market Tier
    ========================================

    GBP/USD (major pair, deep liquidity):
      Interbank:    0.5 - 1.0 pips    ($0.50 - $1.00 per $10,000)
      Institutional: 1.0 - 2.0 pips
      Provider:      2.0 - 10.0 pips
      Retail bank:   50 - 300 pips     ($50 - $300 per $10,000)

    USD/NGN (emerging market, thin liquidity):
      Interbank:    50 - 200 pips      ($50 - $200 per $10,000)
      Institutional: 200 - 500 pips
      Provider:      500 - 2000 pips
      Retail bank:   2000 - 5000 pips  ($2,000 - $5,000 per $10,000)

    Why the difference? Liquidity. GBP/USD trades ~$1 trillion/day.
    USD/NGN trades a tiny fraction of that.
```

> **Key Insight:** The spread on emerging market currency pairs (NGN, KES, GHS) is often 10-100x wider than on major pairs (GBP/USD, EUR/USD). This is why Settla's corridor-based architecture matters. The GBP-to-NGN corridor goes through USDT (pegged to USD) as an intermediary. This means you pay the GBP/USD spread (tight, 2-10 pips) and the USD/NGN spread (wide, 500-2000 pips) separately, rather than a single GBP/NGN spread that might be even worse due to illiquidity in the direct pair.

---

## Retail vs Wholesale: Who Gets What Rate

Not all market participants see the same rate. The rate you get depends on who you are and how much you are trading.

### The Rate Stack

```
    THE RATE STACK: GBP -> USD conversion
    ======================================

    Level 0: Mid-Market Rate                  1.2700
             (theoretical perfect rate)
                |
                | Bank's spread: -20 pips
                v
    Level 1: Interbank Rate                   1.2680 - 1.2720
             (only for Tier 1 banks)
                |
                | Prime broker markup: -5 pips
                v
    Level 2: Institutional Rate               1.2675 - 1.2725
             (hedge funds, large corporates)
                |
                | Provider margin: -15 pips
                v
    Level 3: Provider Rate                    1.2660 - 1.2740
             (on-ramp/off-ramp providers)      <-- Settla operates here
                |
                | Settla's basis point fee
                v
    Level 4: Tenant Rate                      (provider rate + Settla fee)
             (Lemfi, Fincra see this)          <-- tenant sees this
                |
                | Tenant's own markup
                v
    Level 5: End-User Rate                    (tenant rate + tenant markup)
             (Lemfi's customer sees this)
```

Each level adds its margin. The total markup from mid-market to end-user can be:

```
    Rate Markup Comparison
    ======================

    Traditional (correspondent banking):
      Mid-market rate:    1.2700
      Bank retail rate:   1.2400   (300 pips / ~2.4% markup)
      SWIFT fee:          $25-50
      Total cost:         3-7%

    Stablecoin settlement (Settla):
      Mid-market rate:    1.2700
      Provider rate:      1.2660   (40 pips / ~0.3% markup)
      Settla fee:         0.75%    (75 bps for Lemfi)
      Gas fee:            ~$0.50
      Total cost:         ~1.05%

    Savings:              2-6% per transfer
    At GBP 1,000:         $20-60 saved per transfer
    At 50M transfers/day: $1B-3B/day in savings across the network
```

### What Settla's Providers Return

When Settla's smart router queries a provider for a quote, the provider returns a rate and a fee:

```go
// From domain/provider.go
type ProviderQuote struct {
    ProviderID       string
    Rate             decimal.Decimal   // the FX rate the provider offers
    Fee              decimal.Decimal   // the provider's fixed fee
    EstimatedSeconds int               // how long the conversion takes
}
```

The `Rate` field is the provider's all-in conversion rate. It already includes the provider's spread -- the provider does not break out bid/ask separately. The `Fee` is any additional fixed charge the provider applies.

This means Settla can compare providers on a total-cost basis:

```
    Provider A:  Rate = 1.2660,  Fee = $2.00
    Provider B:  Rate = 1.2680,  Fee = $5.00

    For GBP 1,000:
      Provider A total: 1000 x 1.2660 - 2.00 = $1,264.00
      Provider B total: 1000 x 1.2680 - 5.00 = $1,263.00

    Provider A is better despite the worse rate (lower fee wins).

    For GBP 10,000:
      Provider A total: 10000 x 1.2660 - 2.00 = $12,658.00
      Provider B total: 10000 x 1.2680 - 5.00 = $12,675.00

    Provider B is better at higher amounts (better rate wins).
```

This crossover effect is why the router's cost scoring considers the specific transfer amount, not just the rate.

---

## Basis Points: The Language of Fees

If you work in settlement infrastructure, you will speak in basis points every day. A **basis point** (abbreviated **bps**, pronounced "bips") is one hundredth of a percentage point.

```
    Basis Point Reference
    =====================

    1 bps    = 0.01%     = 0.0001
    5 bps    = 0.05%     = 0.0005
    10 bps   = 0.10%     = 0.001
    25 bps   = 0.25%     = 0.0025
    50 bps   = 0.50%     = 0.005
    75 bps   = 0.75%     = 0.0075
    100 bps  = 1.00%     = 0.01
    250 bps  = 2.50%     = 0.025
    1000 bps = 10.00%    = 0.1
```

### Why Not Just Use Percentages?

Because percentages get ambiguous with small numbers. Consider this conversation:

```
    "We need to increase the fee by 0.5%."

    Does that mean:
      (a) Add 0.5 percentage points (e.g., from 1.0% to 1.5%)  ?
      (b) Increase by 0.5% of the current fee (e.g., from 1.0% to 1.005%)  ?

    In basis points, there is no ambiguity:
      (a) "Increase by 50 basis points" -> from 100 bps to 150 bps
      (b) "Increase by 0.5% of the current fee" -> from 100 bps to 100.5 bps

    These are wildly different outcomes. At 50M transfers/day
    with $500 average value, the difference between (a) and (b)
    is $124,750,000 per day in revenue.
```

Basis points eliminate this ambiguity entirely. When someone says "increase by 50 bps," there is exactly one interpretation.

### Settla's Fee Schedule

Every tenant in Settla has a negotiated `FeeSchedule` that defines fees in basis points:

```go
// From domain/tenant.go
type FeeSchedule struct {
    OnRampBPS  int             `json:"onramp_bps"`
    OffRampBPS int             `json:"offramp_bps"`
    MinFeeUSD  decimal.Decimal `json:"min_fee_usd"`
    MaxFeeUSD  decimal.Decimal `json:"max_fee_usd"`
    // ... additional fields for crypto/bank collection fees
}
```

The fee calculation uses a constant divisor of 10,000 (since 1 bps = 1/10,000):

```go
// From domain/tenant.go
var bpsDivisor = decimal.NewFromInt(10000)

func (f FeeSchedule) CalculateFee(amount decimal.Decimal, feeType string) (decimal.Decimal, error) {
    // ... select bps based on feeType ...
    fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)
    // ... apply min/max clamping ...
    return fee, nil
}
```

The formula: `fee = amount * bps / 10,000`

### Per-Tenant Fee Negotiation

Different tenants get different rates. This is standard in B2B infrastructure -- larger tenants negotiate lower fees because they bring higher volume:

```
    Tenant Fee Schedules
    ====================

    Lemfi (high volume, UK remittances):
      On-ramp:   40 bps  (0.40%)
      Off-ramp:  35 bps  (0.35%)
      Combined:  75 bps  (0.75%)
      Min fee:   $1.00
      Max fee:   $50.00

    Fincra (medium volume, African payments):
      On-ramp:   25 bps  (0.25%)
      Off-ramp:  20 bps  (0.20%)
      Combined:  45 bps  (0.45%)
      Min fee:   $0.50
      Max fee:   $100.00
```

Wait -- Lemfi has higher basis point rates but processes more volume? Why would the bigger customer pay more per transaction?

Because fee schedules are negotiated holistically. Lemfi has a lower max fee cap ($50 vs $100), which matters enormously on large transfers. And the min fee affects small transfer economics differently. The total revenue depends on the transfer size distribution, not just the rate.

```
    Fee Comparison: Lemfi vs Fincra
    ================================

    Transfer size: $100
      Lemfi:  100 x 75/10000 = $0.75 -> clamped to min $2.00  ($1.00 + $1.00)
      Fincra: 100 x 45/10000 = $0.45 -> clamped to min $1.00  ($0.50 + $0.50)
      Winner: Fincra pays less

    Transfer size: $5,000
      Lemfi:  on-ramp: 5000 x 40/10000 = $20.00
              off-ramp: 5000 x 35/10000 = $17.50  -> total $37.50
      Fincra: on-ramp: 5000 x 25/10000 = $12.50
              off-ramp: 5000 x 20/10000 = $10.00  -> total $22.50
      Winner: Fincra pays less

    Transfer size: $100,000
      Lemfi:  on-ramp: 100000 x 40/10000 = $400 -> clamped to $50
              off-ramp: 100000 x 35/10000 = $350 -> clamped to $50  -> total $100
      Fincra: on-ramp: 100000 x 25/10000 = $250 -> clamped to $100
              off-ramp: 100000 x 20/10000 = $200 -> clamped to $100 -> total $200
      Winner: Lemfi pays less (by half!)
```

This is the economics of fee schedule negotiation. Lemfi's schedule is optimized for their transfer distribution (many large transfers), while Fincra's is optimized for theirs (many medium transfers).

---

## A Complete Fee Calculation

Let us trace the full cost of a real transfer through the system. This is the kind of calculation your settlement infrastructure must perform correctly on every single transfer.

### Transfer: GBP 1,000 to NGN via USDT on Tron (Lemfi schedule)

```
    COMPLETE FEE BREAKDOWN
    ======================

    Input: GBP 1,000 (Lemfi tenant, 40/35 bps schedule)

    Step 1: On-Ramp (GBP -> USDT)
    +---------------------------------------------------------+
    |  Source amount:        GBP 1,000.00                     |
    |  Provider FX rate:     1 GBP = 1.2700 USD               |
    |  Gross USD value:      1,000.00 x 1.2700 = $1,270.00   |
    |  Provider fee:         $2.00 (fixed)                    |
    |  Settla on-ramp fee:   1,270.00 x 40/10000 = $5.08     |
    |  USDT received:        1,270.00 - 2.00 = 1,268.00 USDT |
    +---------------------------------------------------------+

    Step 2: Blockchain Transfer (USDT on Tron)
    +---------------------------------------------------------+
    |  USDT to send:         1,268.00 USDT                    |
    |  Tron gas fee:         ~0.50 USDT                       |
    |  USDT arriving:        1,267.50 USDT                    |
    |  Confirmation time:    ~3 seconds                       |
    +---------------------------------------------------------+

    Step 3: Off-Ramp (USDT -> NGN)
    +---------------------------------------------------------+
    |  USDT available:       1,267.50 USDT                    |
    |  Provider FX rate:     1 USDT = 1,580.00 NGN            |
    |  Provider fee:         $1.50 (fixed)                    |
    |  Settla off-ramp fee:  1,267.50 x 35/10000 = $4.44     |
    |  Net USDT for conv:    1,267.50 - 1.50 = 1,266.00 USDT |
    |  NGN received:         1,266.00 x 1,580.00              |
    |                        = NGN 2,000,280.00               |
    +---------------------------------------------------------+

    FEE SUMMARY
    +---------------------------------------------------------+
    |  Provider on-ramp fee:     $2.00                        |
    |  Settla on-ramp fee:       $5.08                        |
    |  Blockchain gas fee:       $0.50                        |
    |  Provider off-ramp fee:    $1.50                        |
    |  Settla off-ramp fee:      $4.44                        |
    |  -------------------------------------------            |
    |  Total fees:               $13.52                       |
    |  Total fee percentage:     13.52 / 1,270.00 = 1.06%    |
    |                                                         |
    |  Settla revenue:           $5.08 + $4.44 = $9.52       |
    |  Provider revenue:         $2.00 + $1.50 = $3.50       |
    |  Network fee:              $0.50                        |
    +---------------------------------------------------------+
```

Note several things about this calculation:

1. **All math uses `decimal.Decimal`**, never floating point. The Settla codebase enforces this invariant everywhere. At 50M transfers/day, even a $0.001 rounding error per transfer accumulates to $50,000/day of unreconcilable drift.

2. **The on-ramp fee is calculated on the USD-equivalent amount**, not the GBP amount. This is because Settla's fee schedule is denominated in USD (bps applied to USD value).

3. **The off-ramp fee is calculated on the USDT amount after gas**, because gas has already been deducted. You cannot charge fees on money that does not exist.

4. **Settla keeps $9.52. The rest goes to providers and the network.** At 50M transfers/day averaging $500, this is $9.52 * 50M = $476M/day in Settla revenue. Even at 1% of capacity (500K/day), that is $4.76M/day.

### The Zero-Fee Protection

Settla's engine enforces a critical invariant: `TotalFeeUSD` must never be zero.

```
    // From the engine's CreateTransfer validation:
    // A transfer with zero fees means either:
    //   (a) The fee schedule is misconfigured (all bps = 0)
    //   (b) A bug in the fee calculation
    //   (c) Someone bypassed the fee logic
    // All three are unacceptable. Every transfer must generate revenue.
```

This is Critical Invariant #12 from the project: "Zero-fee transfers rejected." At scale, even a single misconfigured tenant processing zero-fee transfers could cost millions before anyone notices.

---

## Rate Volatility and Slippage

FX rates move constantly. Between the moment a user sees a quote and the moment the provider executes the conversion, the rate can change. This difference is called **slippage**.

### How Fast Do Rates Move?

```
    GBP/USD Rate Movement: Typical Day
    ====================================

    Time (UTC)   Rate      Change from 08:00
    08:00        1.2700    --
    08:01        1.2702    +2 pips  (+0.016%)
    08:05        1.2695    -5 pips  (-0.039%)
    08:30        1.2710    +10 pips (+0.079%)
    09:00        1.2680    -20 pips (-0.157%)
    10:00        1.2720    +20 pips (+0.157%)
    12:00        1.2650    -50 pips (-0.394%)
    14:00        1.2740    +40 pips (+0.315%)

    During major events (interest rate decisions, elections):
    The rate can move 200-500 pips (1.5-4%) in MINUTES.
```

For a settlement platform processing 580 TPS sustained, every second of delay between quote and execution is a second where the rate might move.

### The Quote-to-Execution Window

```
    TIMELINE OF A TRANSFER
    ======================

    T+0s:    User requests quote
             Router queries providers, selects best rate
             Quote created: rate = 1.2700, expires in 30s

    T+2s:    User confirms transfer
             Engine creates transfer, writes to outbox

    T+3s:    Outbox relay publishes to NATS

    T+4s:    TransferWorker picks up, publishes treasury intent

    T+5s:    TreasuryWorker reserves position

    T+6s:    TransferWorker publishes on-ramp intent

    T+7s:    ProviderWorker picks up on-ramp intent
             Calls provider's Execute() with QuotedRate = 1.2700

    T+7s:    Provider checks live rate vs quoted rate
             Live rate: 1.2695 (moved -5 pips / -0.039%)
             Within 2% tolerance -> EXECUTE

    --------- DANGER ZONE ---------

    If the rate had moved to 1.2450 (-250 pips / -1.97%):
             Still within 2% tolerance -> EXECUTE (barely)

    If the rate had moved to 1.2440 (-260 pips / -2.05%):
             EXCEEDS 2% tolerance -> REJECT
             Transfer fails, treasury releases, tenant retries
```

### Slippage Protection in the Codebase

Settla carries the quoted rate through the entire transfer lifecycle via the `QuotedRate` field on provider requests:

```go
// From domain/provider.go
type OnRampRequest struct {
    Amount       decimal.Decimal
    FromCurrency Currency
    ToCurrency   Currency
    Reference    string
    // QuotedRate is the FX rate presented to the user at quote time. When set,
    // the provider must reject execution if the live rate has moved more than
    // the configured slippage tolerance (default 2%).
    QuotedRate decimal.Decimal
}
```

The same field exists on `OffRampRequest`. This means both legs of the transfer are slippage-protected.

### Why 2% Tolerance?

The 2% default is a balance between two risks:

```
    TOO TIGHT (e.g., 0.1%):
    +----------------------------------------------------+
    |  Problem: Most transfers get rejected               |
    |  GBP/USD moves 0.1% in normal trading within       |
    |  seconds. Your execution rate drops below 50%.      |
    |  Tenants are furious: "Why do my transfers keep     |
    |  failing?"                                          |
    +----------------------------------------------------+

    TOO LOOSE (e.g., 10%):
    +----------------------------------------------------+
    |  Problem: You eat massive losses on volatile days   |
    |  If the rate moves 5% against you between quote     |
    |  and execution, and you execute anyway, you are     |
    |  subsidizing the difference.                        |
    |  At $500 avg x 50M transfers: 5% x $500 x 50M     |
    |  = $1.25 BILLION/day in worst case.                 |
    +----------------------------------------------------+

    2% TOLERANCE:
    +----------------------------------------------------+
    |  Rejects only during extreme events (flash crashes, |
    |  central bank surprises, geopolitical shocks).      |
    |  Normal trading: 99.9%+ of transfers execute.       |
    |  Maximum slippage cost per transfer: 2% of value.   |
    |  Worst case at $500 avg: $10 per transfer.          |
    +----------------------------------------------------+
```

### Quote Expiry

Quotes have a time-to-live, typically 30-60 seconds:

```go
// From domain/quote.go
type Quote struct {
    // ...
    FXRate    decimal.Decimal
    ExpiresAt time.Time
    // ...
}

func (q *Quote) IsExpired() bool {
    return time.Now().UTC().After(q.ExpiresAt)
}
```

If a tenant tries to create a transfer with an expired quote, the engine rejects it. The tenant must request a fresh quote. This is the first line of defense against slippage -- if the rate has moved significantly, the new quote will reflect the current market.

The combination of quote expiry (30-60s) and slippage tolerance (2%) provides defense in depth:

```
    DEFENSE IN DEPTH AGAINST RATE RISK
    ===================================

    Layer 1: Quote Expiry (30-60 seconds)
      - Prevents stale quotes from being used
      - Forces fresh rate discovery before execution
      - Catches slow-moving rate drift

    Layer 2: Slippage Tolerance (2%)
      - Catches sudden rate jumps between quote and execution
      - Even with a fresh 30-second-old quote, the rate
        can spike during execution
      - Protects against flash crashes and news events

    Layer 3: Decimal Precision
      - All rate comparisons use shopspring/decimal
      - No floating-point drift in slippage calculations
      - The 2% boundary is exact, not approximate
```

### The Scale Problem with Slippage

At low volume, slippage is a rounding error. At Settla's target throughput, it is an existential risk:

```
    Slippage Impact at Scale
    ========================

    Average transfer value:  $500
    Daily transfers:         50,000,000

    If average slippage is 0.01% (favorable or unfavorable):
      Daily impact: $500 x 0.0001 x 50M = $2,500,000/day

    If average slippage is 0.05%:
      Daily impact: $500 x 0.0005 x 50M = $12,500,000/day

    If average slippage is 0.10%:
      Daily impact: $500 x 0.001 x 50M = $25,000,000/day

    Even 1 basis point of average slippage = $2.5M/day.
    This is why quote expiry and slippage tolerance are not
    nice-to-haves. They are revenue protection mechanisms.
```

> **Key Insight:** Slippage is bidirectional -- sometimes the rate moves in your favor, sometimes against you. Over millions of transfers, the law of large numbers means the average slippage approaches zero. But the variance can be enormous on any single day. The 2% tolerance cap limits your worst-case exposure on individual transfers, while the quote expiry ensures your average slippage stays small by keeping quotes fresh.

---

## The Economics of a Corridor

Not all currency corridors are created equal. The profitability, reliability, and speed of a corridor depends on a complex interaction of market factors.

### What Makes a Corridor Work

```
    CORRIDOR VIABILITY FACTORS
    ==========================

    1. Provider Availability
       - How many on-ramp providers serve Source -> Stablecoin?
       - How many off-ramp providers serve Stablecoin -> Dest?
       - More providers = better rates, more redundancy

    2. Liquidity Depth
       - How much volume trades in this pair daily?
       - Deep liquidity = tight spreads, stable rates
       - Thin liquidity = wide spreads, volatile rates

    3. Regulatory Access
       - Does the destination country allow stablecoin conversion?
       - Are there capital controls (e.g., Nigeria's CBN regulations)?
       - What licenses does the off-ramp provider need?

    4. Demand Pattern
       - Is flow mostly one-directional (remittances) or bidirectional?
       - One-directional flow = harder to rebalance treasury positions
       - Bidirectional flow = natural netting, lower capital requirements

    5. Settlement Infrastructure
       - Does the destination have real-time payment rails (e.g., UK Faster Payments)?
       - Or is it batch settlement (e.g., ACH, next-day)?
       - Real-time rails = faster off-ramp, better user experience
```

### Corridor Comparison

```
    CORRIDOR SCORECARD
    ==================

    GBP -> USDT -> NGN  (UK to Nigeria)
    +--------------------------------------------------+
    |  Provider availability:  HIGH (3+ on-ramp, 2+ off-ramp)
    |  Liquidity depth:        HIGH (massive remittance flow)
    |  Regulatory access:      MEDIUM (NGN has capital controls)
    |  Demand:                 ONE-DIRECTIONAL (UK -> Nigeria)
    |  Spread:                 MEDIUM (30-80 pips on GBP/USD,
    |                                  500-2000 pips on USD/NGN)
    |  Verdict:                PROFITABLE, well-served
    +--------------------------------------------------+

    EUR -> USDT -> KES  (Europe to Kenya)
    +--------------------------------------------------+
    |  Provider availability:  MEDIUM (2 on-ramp, 1-2 off-ramp)
    |  Liquidity depth:        MEDIUM
    |  Regulatory access:      HIGH (Kenya is crypto-progressive)
    |  Demand:                 MOSTLY ONE-DIRECTIONAL
    |  Spread:                 WIDE (50-100 pips on EUR/USD,
    |                                1000-3000 pips on USD/KES)
    |  Verdict:                VIABLE, less competitive
    +--------------------------------------------------+

    USD -> USDC -> EUR  (US to Europe)
    +--------------------------------------------------+
    |  Provider availability:  HIGH (many on both sides)
    |  Liquidity depth:        VERY HIGH (EUR/USD most traded pair)
    |  Regulatory access:      HIGH (MiCA framework in EU)
    |  Demand:                 BIDIRECTIONAL (trade + remittances)
    |  Spread:                 VERY TIGHT (1-3 pips on EUR/USD)
    |  Verdict:                HIGH VOLUME, low margin
    +--------------------------------------------------+
```

### How the Smart Router Evaluates Corridors

Settla's router does not pick corridors manually. It evaluates all available provider combinations and scores them:

```go
// From rail/router/router.go
var (
    weightCost        = decimal.NewFromFloat(0.40)  // 40% weight
    weightSpeed       = decimal.NewFromFloat(0.30)  // 30% weight
    weightLiquidity   = decimal.NewFromFloat(0.20)  // 20% weight
    weightReliability = decimal.NewFromFloat(0.10)  // 10% weight
)
```

For each candidate route (on-ramp provider + blockchain + off-ramp provider), the router:

1. Requests a quote from the on-ramp provider
2. Estimates gas cost on the blockchain
3. Requests a quote from the off-ramp provider
4. Calculates a composite score

```
    ROUTE SCORING EXAMPLE: GBP 1,000 -> NGN
    =========================================

    Candidate A: Provider-X (on-ramp) + Tron + Provider-Y (off-ramp)
      Cost score:        0.85  (competitive rates, low fees)
      Speed score:       0.90  (Tron: 3s finality)
      Liquidity score:   0.80  (good depth on both sides)
      Reliability score: 0.95  (99.5% success rate last 7 days)

      Composite = 0.85 x 0.40 + 0.90 x 0.30 + 0.80 x 0.20 + 0.95 x 0.10
               = 0.340 + 0.270 + 0.160 + 0.095
               = 0.865

    Candidate B: Provider-X (on-ramp) + Ethereum + Provider-Z (off-ramp)
      Cost score:        0.60  (high gas fees on Ethereum)
      Speed score:       0.70  (Ethereum: 15s finality)
      Liquidity score:   0.90  (deeper EVM liquidity)
      Reliability score: 0.85  (97% success rate)

      Composite = 0.60 x 0.40 + 0.70 x 0.30 + 0.90 x 0.20 + 0.85 x 0.10
               = 0.240 + 0.210 + 0.180 + 0.085
               = 0.715

    Winner: Candidate A (0.865 > 0.715)
    Reason: Tron's lower gas cost and faster finality dominate.
    Candidate B becomes an alternative route (fallback).
```

The score breakdown is stored with the quote and travels through the entire transfer lifecycle. This means operators can audit every routing decision after the fact: "Why did this transfer use Tron instead of Ethereum? Because the cost score was 0.85 vs 0.60."

```go
// From domain/provider.go
type ScoreBreakdown struct {
    Cost        decimal.Decimal `json:"cost"`
    Speed       decimal.Decimal `json:"speed"`
    Liquidity   decimal.Decimal `json:"liquidity"`
    Reliability decimal.Decimal `json:"reliability"`
}
```

---

## Hidden Costs: What the Rate Does Not Tell You

The FX rate is only part of the cost. Several hidden costs can erode margins:

### 1. The Stablecoin Peg Deviation

USDT and USDC are pegged to $1.00, but they trade on secondary markets and the peg is not perfect:

```
    USDT Price History (typical range)
    ===================================

    Normal conditions:   $0.9995 - $1.0005  (0.05% deviation)
    Mild stress:         $0.9980 - $1.0020  (0.20% deviation)
    Severe stress:       $0.9900 - $1.0100  (1.00% deviation)
    Black swan (UST):    $0.00               (total depegging)

    At $500 avg x 50M transfers/day:
    0.05% deviation = $12,500,000/day in hidden cost
```

Settla treats stablecoins as USD-equivalent at 1:1. Any peg deviation between on-ramp (buy USDT) and off-ramp (sell USDT) becomes an unpriced cost. This is acceptable at 0.05% but dangerous at 1%+.

### 2. Rebalancing Costs

Unidirectional corridors (like GBP -> NGN) create a rebalancing problem. Settla accumulates NGN at the off-ramp end and depletes GBP at the on-ramp end. Periodically, the treasury needs to be rebalanced -- moving value back from NGN to GBP. This rebalancing has its own FX cost.

```
    REBALANCING THE TREASURY
    ========================

    Day 1-30: Process GBP 50M -> NGN (one-directional)
      Result: GBP position depleted, NGN accumulated at off-ramp

    Day 31: Rebalance
      Convert NGN back to GBP (or to USDT, then to GBP)
      Pay the NGN -> USD spread (wide, 500-2000 pips)
      Pay the USD -> GBP spread (tight, 2-10 pips)
      Total rebalancing cost: ~0.3-1.0% of the rebalanced amount

    This cost must be factored into corridor pricing.
    A corridor that looks profitable at 0.75% total fees
    becomes unprofitable if rebalancing costs 1.0%.
```

### 3. Failed Transfer Costs

When a transfer fails after the on-ramp has executed (GBP already converted to USDT), the USDT must be converted back to GBP (or credited as stablecoin). This "reverse on-ramp" has its own FX cost:

```
    FAILED TRANSFER COST
    ====================

    Original on-ramp:  GBP 1,000 -> 1,268 USDT at rate 1.2700
    Transfer fails at off-ramp.
    Reverse on-ramp:   1,268 USDT -> GBP at rate 1.2650 (rate moved)
                       1,268 / 1.2650 = GBP 1,002.37

    Wait -- the customer got MORE GBP back?
    Not exactly. The provider charges a fee on the reverse too.
    After provider fees: GBP 997.50
    Net loss to the system: GBP 2.50

    At 0.1% failure rate and 50M transfers/day:
    50,000 failed transfers x $2.50 avg loss = $125,000/day
```

This is why Settla's compensation strategies (`core/compensation`) exist. The system must decide the cheapest way to unwind a failed transfer: simple refund, reverse on-ramp, credit stablecoin, or escalate to manual review.

---

## Putting It All Together: The True Cost of a Transfer

Here is the complete cost picture for a single GBP 1,000 -> NGN transfer:

```
    TRUE COST BREAKDOWN
    ====================

    VISIBLE COSTS (shown to tenant):
      Settla on-ramp fee:       $5.08  (40 bps)
      Settla off-ramp fee:      $4.44  (35 bps)
      Subtotal Settla:          $9.52

    VISIBLE COSTS (shown in quote):
      Provider on-ramp fee:     $2.00
      Provider off-ramp fee:    $1.50
      Blockchain gas:           $0.50
      Subtotal external:        $4.00

    HIDDEN COSTS (embedded in rates):
      On-ramp spread:           ~$3.15  (25 pips on GBP/USD)
      Off-ramp spread:          ~$8.00  (~50 pips on USD/NGN equivalent)
      Subtotal spread:          ~$11.15

    AMORTIZED COSTS (per transfer share of ongoing costs):
      Treasury rebalancing:     ~$1.00  (estimated)
      Failed transfer losses:   ~$0.25  (at 0.1% failure rate)
      Stablecoin peg risk:      ~$0.05  (at 0.005% avg deviation)
      Subtotal amortized:       ~$1.30

    TOTAL TRUE COST:            ~$26.00  (2.05% of transfer value)
    Settla gross margin:        $9.52 - $1.30 amortized = $8.22 net
    Settla net margin per tx:   ~$8.22 (0.65% of transfer value)
```

> **Key Insight:** The visible fee (0.75% or $9.52) is not the true cost. The true cost includes provider spreads, gas, and amortized operational costs. Settla's actual margin per transfer is the fee minus the amortized costs -- roughly $8.22 on a $1,270 transfer (0.65%). At 50M transfers/day, this is $411M/day in gross margin. But if any of the hidden costs are miscalculated or unmonitored, margins can evaporate quickly. This is why reconciliation (Chapter 7.1) runs 6 automated checks daily.

---

## Common Mistakes

### Mistake 1: Quoting a Rate Without an Expiry

If you let a user hold a quote indefinitely, you are giving them a free option. They can wait and see if the market moves in their favor, then execute. If it moves against them, they request a new quote. You always lose.

```
    Without quote expiry (YOU LOSE):
      User gets quote at 1.2700
      Rate moves to 1.2800 -> user executes (you pay the better rate)
      Rate moves to 1.2600 -> user requests new quote (free for them)

    With 30-second expiry (FAIR):
      User gets quote at 1.2700 (valid 30 seconds)
      Must decide quickly -> rate risk is shared
      If they wait too long -> must get fresh quote at current rate
```

### Mistake 2: Ignoring the Bid/Ask When Comparing Providers

Two providers might quote the same mid-market rate but have very different effective rates because their spreads differ:

```
    Provider A: quotes mid-market rate 1.2700, charges 0.5% fee
    Provider B: quotes "no fee!" but their rate is 1.2575 (1% worse)

    For GBP 10,000:
      Provider A: 10,000 x 1.2700 - 0.5% = $12,636.50
      Provider B: 10,000 x 1.2575 - 0   = $12,575.00

    Provider A is $61.50 cheaper despite charging a fee.
    The "no fee" provider hid their fee in the rate.
```

Always compare on total output amount, never on rate or fee alone. Settla's router does this by computing the total destination amount for each candidate route.

### Mistake 3: Using Float for Basis Point Calculations

```go
// WRONG: float64
fee := amount * float64(bps) / 10000.0
// At bps=35, amount=1267.50:
// fee = 1267.50 * 35.0 / 10000.0
// fee = 4.436249999999999... (floating point noise)

// RIGHT: decimal
fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)
// fee = 4.43625000 (exact, rounded to 8 decimal places)
```

The difference seems trivial on one transaction. Over 50M transactions per day, floating-point errors accumulate unpredictably. The ledger will not balance. Reconciliation will flag phantom discrepancies. Engineers will spend weeks chasing rounding ghosts.

### Mistake 4: Applying the Same Fee Schedule to All Transfer Sizes

Without min/max fee clamping, small transfers are unprofitable and large transfers are uncompetitive:

```
    Without clamping (75 bps flat):
      $10 transfer:       $0.075 fee  (costs more to process than you earn)
      $1,000,000 transfer: $7,500 fee  (tenant goes to a competitor)

    With clamping (75 bps, min $1.00, max $50.00):
      $10 transfer:       $2.00 fee   (min clamp, covers processing cost)
      $1,000,000 transfer: $100.00 fee (max clamp, stays competitive)
```

---

## Exercises

### Exercise 1: Spread Calculation

A provider quotes GBP/USD at 1.2650 (bid) / 1.2750 (ask). The mid-market rate is 1.2700.

1. What is the spread in pips?
2. What is the spread as a percentage of the mid-market rate?
3. What is the spread in basis points?
4. If you convert GBP 50,000 to USD at the bid rate, what is the total cost of the spread compared to mid-market?
5. If the provider also charges a $15 fixed fee, what is the all-in cost as a percentage?

### Exercise 2: Fee Schedule Crossover

Lemfi's fee schedule is 40/35 bps (on-ramp/off-ramp) with min $1.00 and max $50.00 per leg. Fincra's is 25/20 bps with min $0.50 and max $100.00 per leg.

1. At what transfer size does Lemfi's total fee equal Fincra's total fee? (Ignore min/max clamping first, then check if clamping changes the answer.)
2. Below that crossover, which tenant pays more?
3. Above that crossover, which tenant pays more?
4. At what monthly transfer volume (assuming $2,000 average transfer size) does the fee difference between the two schedules equal $10,000/month?

### Exercise 3: Slippage Analysis

A transfer is quoted at rate 1.2700 with a 30-second expiry. The provider executes 45 seconds later at a live rate of 1.2730.

1. What is the slippage in pips?
2. What is the slippage in basis points?
3. What is the slippage as a percentage?
4. Should Settla's 2% protection threshold catch this? Why or why not?
5. On a GBP 10,000 transfer, what is the dollar impact of this slippage?
6. If this average slippage occurred on all 50M daily transfers ($500 avg), what is the daily impact?

### Exercise 4: Corridor Economics

You are evaluating whether to add a new corridor: EUR -> USDT -> ZAR (Europe to South Africa).

Given:
- EUR/USD mid-market rate: 1.0850
- USD/ZAR mid-market rate: 18.50
- On-ramp provider quotes: rate 1.0830, fee $3.00, estimated 30 seconds
- Off-ramp provider quotes: rate 18.20, fee $2.50, estimated 120 seconds
- Tron gas: $0.50
- Your fee schedule: 30/25 bps

Calculate:
1. The end-to-end conversion: EUR 5,000 -> ZAR (show all intermediate steps)
2. Total fees (yours, provider, gas)
3. The effective spread from mid-market cross rate (EUR/ZAR = EUR/USD x USD/ZAR)
4. Your revenue per transfer
5. Whether this corridor is viable if the off-ramp provider has only 92% reliability (hint: consider the compensation cost of the 8% failures)

---

## What Comes Next

You now understand the mechanics of FX: how rates are determined, how spreads create costs at every layer, how basis points structure fee schedules, and how slippage threatens margins at scale. These are the economic forces that shape every design decision in a settlement platform.

In Chapter 0.4, we will turn to the regulatory landscape: what licenses you need, what compliance obligations exist, and why multi-tenancy is not just an architecture pattern but a regulatory requirement. Understanding regulation is essential because it constrains which corridors you can serve, which providers you can use, and how you must handle customer funds.

---
