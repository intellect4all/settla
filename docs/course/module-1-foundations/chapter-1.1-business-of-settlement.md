# Chapter 1.1: The Business of Settlement

**Estimated reading time:** 25 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain what B2B stablecoin settlement is and why fintechs need it
2. Trace the full lifecycle of a cross-border payment through the GBP-to-NGN corridor
3. Identify the players in a settlement transaction and their roles
4. Calculate revenue from basis-point fee schedules
5. Map settlement corridors with source, stablecoin bridge, and destination currencies

---

## What Is Settlement?

Settlement is the actual movement of money between counterparties. When a fintech like Lemfi lets a user in London send money to a recipient in Lagos, the user sees a simple "Send Money" button. Behind that button, multiple financial systems must coordinate to move value across currencies, countries, and banking networks.

Traditional cross-border settlement works through correspondent banking -- a chain of banks that maintain accounts with each other (nostro/vostro accounts). A GBP payment to Nigeria might pass through 3-4 intermediary banks, each taking a cut, each adding latency. Settlement takes 2-5 business days and costs 3-7% in fees.

Stablecoin settlement replaces the correspondent banking chain with a blockchain bridge. Instead of GBP flowing through HSBC to Citibank to GTBank, the flow becomes:

1. Convert GBP to USDT (on-ramp)
2. Send USDT on-chain (settlement -- seconds, not days)
3. Convert USDT to NGN (off-ramp)

This is what Settla provides as infrastructure: the complete pipeline for fintechs to settle cross-border payments using stablecoins, without building the settlement layer themselves. Settla also provides a deposit gateway (crypto on-chain payments and fiat via virtual bank accounts), payment link generation for merchant collections, a tenant self-service portal, and analytics and data exports.

> **Key Insight:** Settla is a "picks and shovels" business. During a gold rush, the most reliable profits come from selling tools to miners, not mining yourself. In fintech, the most reliable profits come from providing settlement infrastructure to consumer-facing apps. Revenue scales with transaction volume across all tenants, not with end-user acquisition.

---

## The Settlement Lifecycle

Let us trace a real transfer through the system. A user on Lemfi's platform in London sends 1,000 GBP to a recipient's bank account in Lagos, Nigeria.

### The Full Payment Flow

```
    SENDER (London)                    SETTLA                         RECIPIENT (Lagos)
    ===============              ==================                  ==================

    1. User initiates          2. Lemfi calls Settla API
       GBP 1,000 transfer         POST /v1/transfers
           |                           |
           |                     3. Quote & Route
           |                        - Best FX rate: 1 GBP = 1.27 USD
           |                        - On-ramp fee: 0.40% (Lemfi schedule)
           |                        - Off-ramp fee: 0.35%
           |                        - Stablecoin: USDT on Tron
           |                           |
           |                     4. Treasury Reserve
           |                        - Lock GBP 1,000 from Lemfi's
           |                          pre-funded position
           |                           |
           |                     5. On-Ramp (GBP -> USDT)
           |                        - Provider converts GBP to USDT
           |                        - Settla receives ~1,260 USDT
           |                           |
           |                     6. Blockchain Settlement
           |                        - Send USDT on Tron network
           |                        - Confirmed in ~3 seconds
           |                        - Gas fee: ~0.50 USDT
           |                           |
           |                     7. Off-Ramp (USDT -> NGN)
           |                        - Provider converts USDT to NGN
           |                        - At rate: 1 USDT = 1,580 NGN
           |                           |
           |                     8. Webhook to Lemfi                 9. Recipient receives
           |                        transfer.completed                  NGN ~1,985,000
           |                           |                                to bank account
           v                           v                                     v
    ============================================================================
                         TOTAL TIME: 30-90 seconds
                         TOTAL COST: ~0.75% (vs 3-7% traditional)
    ============================================================================
```

Every step in this flow maps to a specific state in Settla's transfer state machine: `CREATED -> FUNDED -> ON_RAMPING -> SETTLING -> OFF_RAMPING -> COMPLETED`. We will study this state machine in detail in Chapter 1.4.

Beyond cross-border transfers, Settla also supports:
- **Crypto deposits**: Merchants accept on-chain stablecoin payments via deposit sessions (chain monitoring, confirmation tracking, auto-convert or hold)
- **Bank deposits**: Fiat deposits via virtual bank accounts (bank credit matching, partner reconciliation)
- **Payment links**: Shareable merchant collection URLs that create deposit sessions on redemption
- **Tenant self-service portal**: Auth, onboarding/KYB, deposits, payment links, analytics, and crypto balance management

### What Happens Behind Each Step

**Step 2: API Request Arrives.** Lemfi's server sends a REST request to the Settla gateway with the transfer details, authenticated by an API key (`sk_live_xxx`). The gateway validates the key (resolved from a local 30-second cache in ~100ns), extracts the `tenant_id`, and forwards to the gRPC backend.

**Step 3: Quote & Route.** The smart router evaluates all available provider combinations for the GBP-to-NGN corridor, scoring each on cost (40%), speed (30%), liquidity (20%), and reliability (10%). It returns the best route with the selected on-ramp provider, off-ramp provider, blockchain chain, and FX rate.

**Step 4: Treasury Reserve.** The engine atomically reserves GBP 1,000 from Lemfi's pre-funded treasury position. This is an in-memory operation (~100ns), not a database lock. If Lemfi's balance is insufficient, the transfer is rejected immediately with `INSUFFICIENT_FUNDS`.

**Steps 5-7: Side Effects via Outbox.** The engine does not call providers or blockchains directly. Instead, it writes outbox entries (intents) atomically with each state transition. Workers pick up these intents from NATS JetStream and execute them. This eliminates dual-write bugs -- the pattern where a state update succeeds but the side effect fails (or vice versa).

**Step 8: Webhook Delivery.** When the transfer reaches `COMPLETED`, an outbox intent instructs the webhook worker to deliver a `transfer.completed` event to Lemfi's configured webhook URL, signed with HMAC-SHA256.

---

## The Players

Each transfer involves multiple distinct parties:

```
    +------------------+     +------------------+     +------------------+
    |     TENANT       |     |      SETTLA      |     |    PROVIDERS     |
    |  (e.g., Lemfi)   |     |   (This System)  |     |                  |
    |                  |     |                  |     |  On-Ramp:        |
    |  - Has users     |     |  - Routes        |     |    GBP -> USDT   |
    |  - Collects GBP  |     |  - Orchestrates  |     |                  |
    |  - Pre-funds     |     |  - Tracks state  |     |  Off-Ramp:       |
    |    treasury      |     |  - Records ledger|     |    USDT -> NGN   |
    |  - Pays fees     |     |  - Manages risk  |     |                  |
    +--------+---------+     +--------+---------+     |  Blockchain:     |
             |                        |               |    Tron/ETH      |
             |   API calls (REST)     |               +--------+---------+
             +------------------------+                        |
                                      |    Provider API calls  |
                                      +------------------------+
```

| Player | Role | Example |
|--------|------|---------|
| **Tenant** | The fintech building a consumer product. Pre-funds treasury, pays per-transaction fees. | Lemfi, Fincra, Paystack |
| **Settla** | Infrastructure layer. Routes, orchestrates, records, reconciles. Never touches end-user funds directly. | This system |
| **On-Ramp Provider** | Converts fiat to stablecoin. Licensed money transmitter or exchange. | Circle, MoonPay, local partners |
| **Off-Ramp Provider** | Converts stablecoin to fiat. Has local banking relationships in destination country. | Local payment processors |
| **Blockchain Network** | Transfers stablecoin on-chain. Provides finality and transparency. | Tron (TRC-20), Ethereum (ERC-20), Solana |

### The Provider Interface

In Settla's domain, on-ramp and off-ramp providers implement clean interfaces:

```go
// From domain/provider.go
type OnRampProvider interface {
    ID() string
    SupportedPairs() []CurrencyPair
    GetQuote(ctx context.Context, req QuoteRequest) (*ProviderQuote, error)
    Execute(ctx context.Context, req OnRampRequest) (*ProviderTx, error)
    GetStatus(ctx context.Context, txID string) (*ProviderTx, error)
}

type OffRampProvider interface {
    ID() string
    SupportedPairs() []CurrencyPair
    GetQuote(ctx context.Context, req QuoteRequest) (*ProviderQuote, error)
    Execute(ctx context.Context, req OffRampRequest) (*ProviderTx, error)
    GetStatus(ctx context.Context, txID string) (*ProviderTx, error)
}
```

Every provider -- regardless of which company's API it wraps -- exposes the same methods. This means adding a new provider is just implementing the interface. The router does not care whether it is talking to Circle, MoonPay, or a local African exchange.

---

## Corridors: The Geography of Settlement

A **corridor** is a specific path that money travels: source currency, through a stablecoin bridge, to a destination currency. Settla represents corridors as a value object in the domain layer:

```go
// From domain/provider.go
type Corridor struct {
    SourceCurrency Currency
    StableCoin     Currency
    DestCurrency   Currency
}

func (c Corridor) String() string {
    return fmt.Sprintf("%s->%s->%s", c.SourceCurrency, c.StableCoin, c.DestCurrency)
}
```

Each corridor requires three things:
- An **on-ramp provider** that can convert the source currency to the stablecoin
- A **blockchain network** that can transfer the stablecoin
- An **off-ramp provider** that can convert the stablecoin to the destination currency

### Primary Corridors

| Corridor | Use Case | Typical Volume |
|----------|----------|----------------|
| GBP -> USDT -> NGN | UK diaspora remittances to Nigeria | High volume, competitive |
| EUR -> USDT -> NGN | EU remittances to Nigeria | Growing market |
| GBP -> USDT -> GHS | UK to Ghana corridor | Medium volume |
| USD -> USDT -> KES | US to Kenya corridor | Emerging |
| EUR -> USDC -> NGN | Alternative stablecoin path | USDC for compliance-sensitive tenants |

### Why the Stablecoin Bridge Matters

The stablecoin sits in the middle for three reasons:

1. **Speed**: On-chain transfer takes seconds, not days via correspondent banking
2. **Cost**: Blockchain gas fees are fractions of a cent on Tron, versus $25-50 SWIFT fees
3. **Transparency**: Every transfer has an on-chain hash -- auditable, immutable, verifiable

The choice of stablecoin and chain affects cost and speed:

```
    Chain Comparison for 1,000 USDT Transfer
    =========================================

    Tron (TRC-20):
      Gas fee:    ~$0.50
      Finality:   ~3 seconds
      Best for:   High-volume corridors

    Ethereum (ERC-20):
      Gas fee:    ~$2-15 (variable)
      Finality:   ~15 seconds
      Best for:   High-value transfers where EVM ecosystem matters

    Solana (SPL):
      Gas fee:    ~$0.01
      Finality:   ~0.4 seconds
      Best for:   Micro-transfers, cost-sensitive corridors
```

Settla's smart router automatically selects the optimal chain based on a weighted scoring formula: cost (40%), speed (30%), liquidity (20%), and reliability (10%). The score breakdown is returned with every route decision so operators can audit why a particular provider/chain combination was chosen:

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

## The Revenue Model: Basis Points

Settla charges tenants per-transaction fees expressed in **basis points** (bps). One basis point is 1/100th of a percent, or 0.01%.

```
    1 basis point   = 0.01%  = 0.0001
    10 basis points  = 0.10%  = 0.001
    100 basis points = 1.00%  = 0.01
```

Each tenant has a negotiated fee schedule. Here is the actual `FeeSchedule` type from the codebase:

```go
// From domain/tenant.go
type FeeSchedule struct {
    OnRampBPS  int             `json:"onramp_bps"`
    OffRampBPS int             `json:"offramp_bps"`
    MinFeeUSD  decimal.Decimal `json:"min_fee_usd"`
    MaxFeeUSD  decimal.Decimal `json:"max_fee_usd"`
}
```

The `bpsDivisor` is a package-level constant that converts basis points to a decimal fraction:

```go
// From domain/tenant.go
var bpsDivisor = decimal.NewFromInt(10000)
```

### Fee Calculation Walkthrough

The `CalculateFee` method computes fees with min/max clamping:

```go
// From domain/tenant.go
func (f FeeSchedule) CalculateFee(amount decimal.Decimal, feeType string) (decimal.Decimal, error) {
    var bps int
    var maxFee decimal.Decimal
    switch feeType {
    case "onramp":
        bps = f.OnRampBPS
        maxFee = f.MaxFeeUSD
    case "offramp":
        bps = f.OffRampBPS
        maxFee = f.MaxFeeUSD
    default:
        return decimal.Zero, fmt.Errorf("settla-domain: unknown fee type %q", feeType)
    }

    fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)

    if !f.MinFeeUSD.IsZero() && fee.LessThan(f.MinFeeUSD) {
        fee = f.MinFeeUSD
    }
    if !maxFee.IsZero() && fee.GreaterThan(maxFee) {
        fee = maxFee
    }

    return fee, nil
}
```

The formula is straightforward: `fee = amount * bps / 10000`. But the min/max clamping is critical for business viability.

### Example: Lemfi vs Fincra Fee Schedules

Tenants negotiate different rates based on volume commitments:

```
    Lemfi Fee Schedule (higher volume):
      On-ramp:  40 bps (0.40%)
      Off-ramp: 35 bps (0.35%)
      Min fee:  $1.00
      Max fee:  $50.00

    Fincra Fee Schedule (lower volume, standard rates):
      On-ramp:  25 bps (0.25%)
      Off-ramp: 20 bps (0.20%)
      Min fee:  $0.50
      Max fee:  $100.00
```

Notice that Lemfi has higher basis-point rates but a lower max fee cap. This reflects a negotiation: Lemfi sends high volume but wants predictable costs on large transfers. Fincra has better rates per transaction but a higher max cap.

### Revenue Math: Worked Examples

**Large transfer -- Lemfi sends $10,000 USD equivalent:**

```
    On-ramp fee (40 bps):      $10,000 x 40 / 10,000 = $40.00
    Off-ramp fee (35 bps):     $10,000 x 35 / 10,000 = $35.00
                               --------------------------------
    Total Settla revenue:                               $75.00

    Both fees are within min ($1) and max ($50): no clamping needed.
```

**Small transfer -- Lemfi sends $50 USD equivalent:**

```
    On-ramp fee (40 bps):      $50 x 40 / 10,000 = $0.20
      Min fee is $1.00:        Clamped UP to $1.00
    Off-ramp fee (35 bps):     $50 x 35 / 10,000 = $0.175
      Min fee is $1.00:        Clamped UP to $1.00
                               -------------------------
    Total Settla revenue:                          $2.00
```

**Huge transfer -- Lemfi sends $100,000 USD equivalent:**

```
    On-ramp fee (40 bps):      $100,000 x 40 / 10,000 = $400.00
      Max fee is $50.00:       Clamped DOWN to $50.00
    Off-ramp fee (35 bps):     $100,000 x 35 / 10,000 = $350.00
      Max fee is $50.00:       Clamped DOWN to $50.00
                               --------------------------------
    Total Settla revenue:                               $100.00
```

> **Key Insight:** The min/max fee clamping serves two purposes. The minimum fee ensures Settla does not lose money processing tiny transfers (where the infrastructure cost exceeds the percentage-based fee). The maximum fee keeps Settla competitive on large transfers, where a pure percentage would be prohibitively expensive for the tenant.

### Revenue at Scale

At Settla's target throughput:

```
    50M transfers/day
    x $500 average transfer value (conservative)
    x 0.0075 combined fee rate (75 bps average)
    ------------------------------------------------
    = $187,500,000 daily gross revenue potential

    Even at 10% of capacity (5M transfers/day):
    = $18,750,000 daily gross revenue
```

---

## Money Movement: Where Funds Actually Live

Understanding where money sits at each stage is critical for building settlement infrastructure. This is not abstract -- regulators, auditors, and reconciliation processes all need to know exactly where funds are at every moment.

```
    Stage 1: PRE-TRANSFER
    +---------------------------------------------+
    | Tenant Treasury (Settla-managed)             |
    |   Lemfi GBP position: 500,000 GBP           |
    |   (pre-funded by wire transfer)              |
    +---------------------------------------------+

    Stage 2: FUNDED (treasury reserved)
    +---------------------------------------------+
    | Tenant Treasury                              |
    |   Available: 499,000 GBP                     |
    |   Locked:      1,000 GBP  <-- reserved      |
    +---------------------------------------------+

    Stage 3: ON-RAMPING
    +---------------------------------------------+
    | On-Ramp Provider                             |
    |   Receives: 1,000 GBP                        |
    |   Returns:  1,260 USDT                       |
    |   (after conversion at 1.27 rate)            |
    +---------------------------------------------+

    Stage 4: SETTLING (on-chain)
    +---------------------------------------------+
    | Tron Blockchain                              |
    |   From: Settla hot wallet                    |
    |   To:   Off-ramp provider wallet             |
    |   Amount: 1,260 USDT (TRC-20)               |
    |   TxHash: 0xabc...def                        |
    +---------------------------------------------+

    Stage 5: OFF-RAMPING
    +---------------------------------------------+
    | Off-Ramp Provider                            |
    |   Receives: 1,260 USDT                       |
    |   Pays out: 1,990,800 NGN                    |
    |   (at 1,580 NGN per USDT)                    |
    |   To: Recipient bank account                 |
    +---------------------------------------------+

    Stage 6: COMPLETED
    +---------------------------------------------+
    | Tenant Treasury                              |
    |   Available: 499,000 GBP                     |
    |   Locked:          0 GBP  <-- released       |
    |                                              |
    | Recipient Bank (GTBank Lagos)                |
    |   Credited: 1,990,800 NGN                    |
    +---------------------------------------------+
```

> **Key Insight:** The tenant's treasury position is the critical constraint. If Lemfi's GBP position is depleted, no transfers can proceed -- the system rejects them immediately with `INSUFFICIENT_FUNDS`. This is why the treasury reservation must be atomic and in-memory (nanosecond latency), not a database row lock that would bottleneck under concurrent pressure.

---

## The Settlement Model: Prefunded vs Net Settlement

Settla supports two settlement models per tenant:

```go
// From domain/tenant.go
type SettlementModel string

const (
    SettlementModelPrefunded     SettlementModel = "PREFUNDED"
    SettlementModelNetSettlement SettlementModel = "NET_SETTLEMENT"
)
```

### Prefunded Model

The tenant deposits funds upfront. Each transfer deducts from their pre-funded position in real time.

```
    Day 1: Lemfi wires GBP 500,000 to Settla's bank account
           Settla credits Lemfi's treasury position

    Day 1-30: Each transfer atomically reserves from the position
              Position shrinks with each transfer

    Day 15: Lemfi wires another GBP 300,000 (top-up)
            Position increases

    If position hits zero: new transfers are rejected immediately
```

**Advantages:** No credit risk for Settla. Instant rejection when funds are exhausted. Simple accounting.

**Disadvantage:** Requires the tenant to tie up working capital.

### Net Settlement Model

Transfers are processed during the day and netted at end-of-day. Settla calculates the net position and the tenant settles the difference.

```
    Day 1 (during business hours):
      - 500 transfers GBP -> NGN totaling GBP 2,000,000
      - 200 transfers NGN -> GBP totaling GBP 800,000
      - Net: Lemfi owes GBP 1,200,000

    Day 1 (00:30 UTC - settlement window):
      - Settla's settlement scheduler calculates net positions
      - Generates settlement report
      - Lemfi pays GBP 1,200,000 net (instead of gross GBP 2,000,000)
```

**Advantages:** Reduces working capital requirements. Bidirectional flows naturally offset.

**Disadvantage:** Settla takes on credit risk (the tenant might not settle). Requires more sophisticated risk management.

---

## The FX Rate Problem: Slippage

Between when a quote is generated and when the on-ramp/off-ramp executes, the FX rate can move. This is called **slippage**, and it can eat into margins or cause unexpected costs.

Settla handles this by including the `QuotedRate` in the provider request:

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

If the live rate has moved more than 2% from the quoted rate, the provider rejects the execution. The transfer fails gracefully, the treasury reservation is released, and the tenant can retry with a fresh quote.

---

## Expanded Product Surface

Settla is not limited to cross-border transfers. The platform has expanded to cover the full spectrum of money movement that fintechs need:

### Crypto Deposits

Merchants accepting on-chain stablecoin payments use Settla's crypto deposit gateway. A tenant creates a deposit session, receives a unique deposit address (derived via HD wallet), and Settla's chain monitor watches for on-chain payments:

```
    Merchant creates deposit session
         |
    Settla derives unique deposit address (HD wallet)
         |
    Customer sends USDT on Tron to deposit address
         |
    Chain Monitor detects on-chain transfer
         |
    Confirmation tracking (chain-specific block depth)
         |
    Ledger credit + treasury position update
         |
    Settlement preference:
       AUTO_CONVERT -> convert crypto to fiat automatically
       HOLD         -> keep crypto balance
       THRESHOLD    -> accumulate until threshold, then convert
```

The deposit session follows its own state machine: `PENDING_PAYMENT -> DETECTED -> CONFIRMED -> CREDITING -> CREDITED -> SETTLING/HELD -> SETTLED/HELD`. Each transition produces outbox entries for the chain monitor, ledger worker, and treasury worker.

### Bank Deposits (Virtual Accounts)

For fintechs that need fiat deposit rails, Settla provisions virtual bank accounts from a pooled inventory. A tenant requests a deposit session, receives a virtual account number (sort code + account number or IBAN), and waits for a bank credit:

```
    Tenant creates bank deposit session
         |
    Virtual account dispensed from pool (PERMANENT or TEMPORARY)
         |
    Customer sends bank transfer to virtual account
         |
    Banking partner webhook notifies Settla of credit
         |
    Payment matching (amount, reference, account)
         |
    Handle mismatch: UNDERPAID/OVERPAID -> ACCEPT or REJECT policy
         |
    Ledger credit + treasury position update
```

The bank deposit state machine handles real-world complexity: late payments after expiry, underpayment/overpayment reconciliation, and mismatch policies per tenant.

### Payment Links

Payment links are shareable URLs that create deposit sessions on redemption. A merchant generates a link with a short code, amount, currency, and chain configuration. When a customer visits the link, a new deposit session is created automatically:

```go
// From domain/payment_link.go
type PaymentLink struct {
    ID          uuid.UUID
    TenantID    uuid.UUID
    ShortCode   string              // URL-friendly identifier
    Description string
    Status      PaymentLinkStatus   // ACTIVE, EXPIRED, DISABLED
    SessionConfig PaymentLinkSessionConfig  // amount, currency, chain, token, settlement pref
    UseLimit    *int                // nil = unlimited redemptions
    UseCount    int
    ExpiresAt   *time.Time
}
```

The `CanRedeem()` method enforces expiration and use limits. Payment links delegate all heavy lifting (address dispensing, chain monitoring, crediting, settlement) to the existing deposit engine.

### Tenant Self-Service Portal

The tenant portal (`portal/`) is a Vue 3 application that provides self-service capabilities:

- **Auth and onboarding**: Registration, email verification, KYB document submission
- **Deposits**: Create and monitor crypto and bank deposit sessions
- **Payment links**: Generate, manage, and track payment link redemptions
- **Analytics**: Volume metrics, fee breakdowns, settlement reports
- **Crypto balances**: View and manage stablecoin positions across chains
- **Treasury**: Request top-ups and withdrawals, view position history

> **Key Insight:** Each new product surface (deposits, bank deposits, payment links) reuses the same architectural patterns as transfers: explicit state machines, transactional outbox, worker-based side effects, and per-tenant isolation. The deposit engine, bank deposit engine, and payment link service all depend on `domain/` interfaces and emit outbox entries -- they never call external systems directly. This consistency means adding a new product feature does not require inventing a new architecture.

---

## Common Mistakes

### Mistake 1: Treating Settlement as a Simple API Call

Settlement is not `POST /send-money`. It is a multi-step, multi-party process where any step can fail. The on-ramp provider might be down. The blockchain might be congested. The off-ramp provider might reject the recipient's bank details. Each failure mode requires a different compensation strategy (simple refund, reverse on-ramp, credit stablecoin, or escalate to manual review).

### Mistake 2: Using Float64 for Money

```go
// NEVER do this
amount := 0.1 + 0.2  // = 0.30000000000000004

// At 50M transfers/day, floating-point drift accumulates.
// $0.000000000000004 x 50,000,000 = unreconcilable ledger.
```

Settla uses `shopspring/decimal` for ALL monetary math. This is enforced by the `Money` value object and the `decimal.Decimal` type throughout the codebase. We will explore this in depth in Chapter 1.3.

### Mistake 3: Ignoring the Time Zone Problem

Settlement windows, daily limits, and reconciliation all depend on consistent time handling. Settla stores ALL timestamps in UTC. The settlement scheduler runs at 00:30 UTC. Daily limits reset at 00:00 UTC. Mixing time zones causes reconciliation nightmares where $50M/day of transfers do not add up.

### Mistake 4: Building for a Single Tenant

It is tempting to start with a single-tenant architecture and "add multi-tenancy later." This never works in financial infrastructure because tenant isolation is not a feature -- it is a security requirement. Every query, every cache entry, every rate limit must be tenant-scoped from day one. A single query missing a `WHERE tenant_id = ?` clause leaks another customer's financial data. We cover this in Chapter 1.5.

---

## Exercises

### Exercise 1: Map Three Settlement Corridors

For each of the following corridors, identify:
- The source currency and country
- The optimal stablecoin (USDT or USDC) and blockchain (Tron, Ethereum, or Solana)
- The destination currency and country
- Which type of provider is needed on each side

Corridors to map:
1. A UK fintech sending remittances to Ghana
2. A European payroll company paying contractors in Kenya
3. A US-based platform settling merchant payouts in Nigeria

### Exercise 2: Calculate Fee Revenue

Given the following tenant fee schedule:
- On-ramp: 30 bps
- Off-ramp: 25 bps
- Min fee: $0.75 USD
- Max fee: $75.00 USD

Calculate the total fee Settla earns on:
1. A $100 transfer (does min clamping apply?)
2. A $5,000 transfer (straight percentage)
3. A $50,000 transfer (does max clamping apply?)
4. 10,000 transfers averaging $2,000 each per day (daily revenue)

### Exercise 3: Prefunded Position Tracking

A tenant starts the day with a GBP 100,000 prefunded position. They process the following transfers:
1. GBP 5,000 transfer -- succeeds
2. GBP 12,000 transfer -- succeeds
3. GBP 8,000 transfer -- fails after on-ramp (funds must be released back)
4. GBP 25,000 transfer -- succeeds
5. GBP 60,000 transfer -- what happens?

Track the available and locked balances after each step. At which step does a business decision need to be made?

---

## What's Next

Now that you understand the business of settlement -- corridors, players, money movement, and revenue -- we need to understand why this business demands unusual architecture. In Chapter 1.2, we will derive every architectural decision from the capacity math: 50 million transfers per day, 580 TPS sustained, 5,000 TPS peak. You will see exactly where standard architectures break and why Settla's design looks the way it does.

---
