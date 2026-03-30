# Chapter 0.2: Stablecoins -- The Bridge Currency

**Estimated reading time:** 30 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain what stablecoins are and how they maintain their peg to the US dollar
2. Compare USDT, USDC, and DAI by reserve mechanism, risk profile, and chain availability
3. Evaluate blockchain tradeoffs for settlement (Tron vs Ethereum vs Solana)
4. Identify the five major risk categories: depegging, regulatory, counterparty, smart contract, and chain risk
5. Explain why stablecoins are the settlement layer, not Bitcoin or ETH

---

## What Is a Stablecoin?

A stablecoin is a cryptocurrency designed to maintain a stable value, usually pegged 1:1 to the US dollar. This sounds simple, but the engineering and financial mechanisms required to hold that peg under market stress are anything but simple.

To understand why stablecoins matter, consider what happens without them. Bitcoin fluctuates 5-20% on a typical day. Ethereum moves similarly. If you are building settlement infrastructure that converts GBP to NGN through a crypto bridge, you cannot afford the bridge asset to lose 10% of its value in the 60 seconds between on-ramp and off-ramp. That would wipe out your margins and your customers' money.

Stablecoins solve this. Their price fluctuation is typically less than 0.5%, and on most days less than 0.1%. This makes them usable as a medium of exchange -- a bridge currency that holds its value long enough to complete a settlement.

### The Three Types of Stablecoins

Not all stablecoins are created equal. The mechanism that maintains the peg determines the risk profile.

```
    STABLECOIN TAXONOMY
    ====================

    1. FIAT-BACKED (Custodial)
    +---------------------------------------------------------+
    |  Issuer holds real USD (or equivalents) in bank accounts |
    |  For every 1 USDT in circulation, Tether holds ~$1      |
    |  in reserves (Treasuries, cash, commercial paper)        |
    |                                                          |
    |  Examples: USDT (Tether), USDC (Circle)                  |
    |  Market cap: USDT ~$120B, USDC ~$30B (2025)             |
    |  Peg mechanism: Arbitrage + redemption rights             |
    +---------------------------------------------------------+

    2. CRYPTO-BACKED (Over-collateralized)
    +---------------------------------------------------------+
    |  Smart contract locks crypto worth MORE than the         |
    |  stablecoin issued (e.g., $150 of ETH locked to mint    |
    |  $100 of DAI)                                            |
    |                                                          |
    |  Example: DAI (MakerDAO)                                 |
    |  Market cap: ~$5B                                        |
    |  Peg mechanism: Liquidation auctions + governance         |
    +---------------------------------------------------------+

    3. ALGORITHMIC (Uncollateralized)
    +---------------------------------------------------------+
    |  No reserves. Uses mint/burn algorithms and a companion  |
    |  token to maintain the peg through market incentives     |
    |                                                          |
    |  Example: UST (Terra/Luna) -- COLLAPSED May 2022         |
    |  Market cap: $0 (destroyed $40B in 72 hours)             |
    |  Peg mechanism: Game theory -- failed catastrophically   |
    +---------------------------------------------------------+
```

For settlement infrastructure, only fiat-backed stablecoins are viable. Crypto-backed stablecoins like DAI have a place in DeFi, but their reliance on volatile collateral and liquidation mechanics introduces risk that settlement systems cannot tolerate. Algorithmic stablecoins have been proven dangerous. Settla uses USDT and USDC exclusively.

> **Key Insight:** The stablecoin taxonomy is not academic trivia. It determines your risk model. Fiat-backed stablecoins carry counterparty risk (is the issuer solvent?). Crypto-backed stablecoins carry liquidation risk (can the collateral cover a market crash?). Algorithmic stablecoins carry death spiral risk (they can go to zero in hours). Your choice of stablecoin is a risk management decision, not a technology decision.

---

## How Fiat-Backed Stablecoins Work

The mechanism is deceptively simple: for every stablecoin in circulation, the issuer holds an equivalent amount of real-world assets in reserve.

```
    MINTING (Creating new stablecoins)
    ====================================

    CUSTOMER                         ISSUER (e.g., Tether)
    ========                         =====================

    1. Customer deposits              Issuer receives $1,000,000
       $1,000,000 USD                 in their bank account
       via wire transfer                    |
            |                               |
            |                         2. Issuer mints 1,000,000
            |                            USDT tokens on the
            |                            blockchain(s)
            |                               |
            |                         3. Issuer sends USDT to
            |                            customer's blockchain
            |                            address
            |                               |
            v                               v
    Customer now holds:              Reserves increase by:
    1,000,000 USDT                   $1,000,000

    REDEMPTION (Destroying stablecoins)
    ====================================

    1. Customer sends 1,000,000      Issuer receives USDT
       USDT to issuer's address            |
            |                               |
            |                         2. Issuer burns (destroys)
            |                            the 1,000,000 USDT tokens
            |                               |
            |                         3. Issuer wires $1,000,000
            |                            to customer's bank account
            |                               |
            v                               v
    Customer now holds:              Reserves decrease by:
    $1,000,000 USD                   $1,000,000
```

### The Arbitrage Mechanism

The mint/redeem cycle creates an arbitrage opportunity that keeps the price near $1.00:

```
    SCENARIO: USDT trades at $0.98 on exchanges

    Arbitrageur:
      1. Buy 1,000,000 USDT on exchange for $980,000
      2. Redeem 1,000,000 USDT from Tether for $1,000,000
      3. Profit: $20,000
      4. This buying pressure pushes USDT back toward $1.00

    SCENARIO: USDT trades at $1.02 on exchanges

    Arbitrageur:
      1. Deposit $1,000,000 with Tether, receive 1,000,000 USDT
      2. Sell 1,000,000 USDT on exchange for $1,020,000
      3. Profit: $20,000
      4. This selling pressure pushes USDT back toward $1.00
```

This works as long as two conditions hold: the issuer actually has the reserves to honor redemptions, and the issuer processes redemptions in a timely manner. When either condition breaks, the peg breaks.

---

## The Major Stablecoins: A Detailed Comparison

### USDT (Tether)

Tether is the oldest and largest stablecoin, launched in 2014. It dominates the stablecoin market with approximately $120 billion in circulation as of 2025.

**Reserve composition:**
- US Treasury bills (~80% of reserves)
- Overnight reverse repurchase agreements
- Cash and bank deposits
- Corporate bonds and secured loans (small percentage)
- Bitcoin and other investments (small percentage)

**Transparency:**
Tether publishes quarterly attestations (not full audits) from BDO Italia. The distinction matters: an attestation confirms that reserves existed at a single point in time. An audit examines the processes, controls, and history. Tether has never completed a full independent audit despite years of promises to do so.

**Chain availability:**
Tron (dominant), Ethereum, Solana, Avalanche, Polygon, Arbitrum, Optimism, TON, and others. Over 60% of USDT volume occurs on Tron.

**Daily volume:**
USDT regularly moves $50 billion or more per day, exceeding Visa's daily payment volume. This is the single most important fact for settlement infrastructure: USDT has more liquidity than any other digital payment mechanism.

**The controversy:**
Tether has been fined $41 million by the CFTC (2021) for misrepresenting its reserves. In 2019, the New York Attorney General found that Tether had comingled customer funds and used USDC reserves to cover an $850 million loss from a payment processor. Tether settled for $18.5 million. Despite this history, USDT has never failed to process a redemption, and the peg has held through multiple market crises (with brief deviations).

### USDC (Circle)

Circle launched USDC in 2018 as a more transparent, regulated alternative to USDT. It holds approximately $30 billion in circulation.

**Reserve composition:**
- US Treasury bills (short-duration, held at BlackRock)
- Cash in regulated US banks (including BNY Mellon)

USDC reserves are simpler and more conservative than Tether's. No corporate bonds, no Bitcoin, no "other investments."

**Transparency:**
Monthly attestation reports by Deloitte. Circle is a registered Money Services Business with FinCEN and holds state money transmitter licenses. Circle filed for an IPO in 2024, which would subject it to SEC reporting requirements.

**Chain availability:**
Ethereum (primary), Solana, Arbitrum, Base (Coinbase L2), Polygon, Avalanche, and others.

**The SVB incident:**
In March 2023, USDC depegged to $0.87 when Silicon Valley Bank collapsed. Circle held $3.3 billion of USDC reserves at SVB. Over a weekend, the market panicked and USDC traded well below $1.00. The peg recovered on Monday when the FDIC guaranteed all SVB deposits. This event demonstrated that even the "transparent" stablecoin carries real risk.

### DAI (MakerDAO)

DAI is the largest decentralized stablecoin, governed by MakerDAO (now rebranded as Sky). It maintains its peg through over-collateralization rather than fiat reserves.

**How it works:**

```
    MINTING DAI
    ============

    User deposits $150 worth of ETH into a MakerDAO "Vault"
         |
    Smart contract locks the ETH as collateral
         |
    User can mint up to $100 DAI (150% collateralization ratio)
         |
    If ETH price drops and collateral ratio falls below 150%:
         |
    Liquidation auction automatically sells the ETH
    to cover the DAI debt
```

**Market cap:** ~$5 billion

**Why not for settlement:**
DAI's collateral is volatile. In a market crash, mass liquidations can cause DAI itself to deviate from its peg. During the March 2020 crypto crash, DAI traded above $1.10 for days because liquidation auctions failed to process fast enough. For settlement infrastructure processing $50 billion/day, this unpredictability is unacceptable.

### Comparison Table

```
    +---------------+----------+----------+----------+
    | Property      |   USDT   |   USDC   |   DAI    |
    +---------------+----------+----------+----------+
    | Market Cap    |  ~$120B  |  ~$30B   |  ~$5B    |
    | Backing       |  Fiat +  |  Fiat    |  Crypto  |
    |               |  T-bills |  (T-bills|  (ETH +  |
    |               |  + other |  + cash) |  other)  |
    +---------------+----------+----------+----------+
    | Audit Level   | Quarterly| Monthly  | On-chain |
    |               | attesta- | attesta- | (fully   |
    |               | tion     | tion     | visible) |
    +---------------+----------+----------+----------+
    | Transparency  |   Low    |  Medium  |  High    |
    +---------------+----------+----------+----------+
    | Liquidity     |  Highest |   High   |  Medium  |
    +---------------+----------+----------+----------+
    | Primary Chain |   Tron   | Ethereum |  Ethereum|
    +---------------+----------+----------+----------+
    | Regulatory    |  Medium  |   Low    |  Unknown |
    | Risk          |  (off-   |  (US-    |  (decen- |
    |               |  shore)  |  licensed|  tralized)|
    +---------------+----------+----------+----------+
    | Depegging     |  Brief   | $0.87    |  $1.10+  |
    | History       |  to $0.95| (SVB     |  (March  |
    |               |  (2022)  |  2023)   |   2020)  |
    +---------------+----------+----------+----------+
    | Settlement    | PRIMARY  | SECONDARY| NOT USED |
    | Use in Settla |          |          |          |
    +---------------+----------+----------+----------+
```

> **Key Insight:** USDT and USDC present different risk profiles, not better-or-worse options. USDT has superior liquidity and lower transaction costs (via Tron), but carries higher counterparty and regulatory risk due to Tether's offshore structure and audit history. USDC is more transparent and regulated, but proved vulnerable to US banking system contagion (SVB). A robust settlement system supports both and selects per-corridor based on liquidity, cost, and regulatory requirements.

---

## Why Stablecoins for Settlement (Not BTC or ETH)

This question comes up frequently from engineers with a crypto background. Bitcoin is the most well-known cryptocurrency. Ethereum has the largest smart contract ecosystem. Why not use them as the settlement bridge?

The answer is simple: settlement requires predictable value during transit.

### The Volatility Problem

Consider a $100,000 GBP-to-NGN transfer using Bitcoin as the bridge:

```
    TIME 0s:    Convert GBP to BTC at $67,000/BTC
                Receive 1.4925 BTC

    TIME 15s:   BTC transaction broadcast to mempool
                BTC price moves to $66,400 (-0.9%)

    TIME 600s:  BTC transaction confirmed (1 confirmation)
                BTC price moves to $65,800 (-1.8%)

    TIME 3600s: BTC transaction has 6 confirmations (considered final)
                BTC price moves to $64,500 (-3.7%)

    RESULT:     1.4925 BTC is now worth $96,266
                LOSS: $3,734 (3.7%)

    On 50M transfers/day at $500 average:
    Daily loss exposure = 50,000,000 x $500 x 0.037 = $925,000,000
```

This is not a theoretical risk. Bitcoin routinely moves 3-5% within an hour. The longer the settlement takes, the greater the exposure. And Bitcoin settlement is slow -- you need 6 confirmations for finality, which takes roughly 60 minutes.

Now consider the same transfer using USDT on Tron:

```
    TIME 0s:    Convert GBP to USDT at $1.0001/USDT
                Receive 126,987 USDT

    TIME 3s:    USDT transaction confirmed on Tron
                USDT price: $1.0002 (+0.01%)

    TIME 60s:   20 confirmations reached (Settla's threshold)
                USDT price: $0.9999 (-0.02%)

    RESULT:     126,987 USDT is worth $126,962
                LOSS: $25 (0.02%)

    On 50M transfers/day at $500 average:
    Daily loss exposure = 50,000,000 x $500 x 0.0002 = $5,000,000
```

The difference is two orders of magnitude. And in practice, USDT fluctuation is even smaller -- the 0.02% figure above is conservative.

### The Finality Problem

Settlement infrastructure needs fast finality. The longer a transfer is "in flight," the more capital is tied up and the greater the exposure to price movement, chain reorgs, and counterparty risk.

```
    FINALITY COMPARISON
    ====================

    ASSET       MECHANISM           TIME TO FINALITY     REVERSIBLE?
    -----       ---------           ----------------     -----------
    Bitcoin     Proof of Work       ~60 min (6 blocks)   Yes (reorg)
    Ethereum    Proof of Stake      ~15 min (finalized)  Theoretically
    USDT/Tron   Delegated PoS       ~60 sec (20 blocks)  Extremely unlikely
    USDT/Solana Tower BFT           ~12 sec (32 slots)   Extremely unlikely

    For settlement at 580 TPS sustained:

    With Bitcoin (60 min finality):
      Transfers in flight = 580 x 3,600 = 2,088,000
      Capital locked = 2,088,000 x $500 = $1,044,000,000,000
      (Over one TRILLION dollars in flight at any moment)

    With USDT on Tron (60 sec finality):
      Transfers in flight = 580 x 60 = 34,800
      Capital locked = 34,800 x $500 = $17,400,000
      (Manageable treasury requirement)
```

### The Fee Problem

Bitcoin and Ethereum transaction fees are unpredictable and can spike dramatically during network congestion:

```
    AVERAGE TRANSACTION FEES (2024-2025)
    ======================================

    Bitcoin:
      Normal:     $1-5
      Congested:  $20-80
      Peak (2024 inscriptions): $100+

    Ethereum:
      Normal:     $2-15
      Congested:  $50-200
      Peak (NFT mints): $500+

    Tron (USDT):
      Normal:     $0.30-0.70
      Congested:  $1-2
      Peak:       $3-5

    Solana (USDC):
      Normal:     $0.001-0.01
      Congested:  $0.01-0.05
      Peak:       $0.10
```

At 50M transfers/day, even small fee differences compound:

```
    Daily gas cost comparison at 50M transfers/day:

    Bitcoin:     50,000,000 x $3.00   =  $150,000,000/day
    Ethereum:    50,000,000 x $8.00   =  $400,000,000/day
    Tron:        50,000,000 x $0.50   =   $25,000,000/day
    Solana:      50,000,000 x $0.005  =      $250,000/day
```

> **Key Insight:** Bitcoin is a store of value. Ethereum is a smart contract platform. Stablecoins are a medium of exchange. These are different tools for different jobs. Using Bitcoin for settlement is like using gold bars for daily purchases -- technically possible, but the volatility, speed, and cost make it impractical at scale. Stablecoins exist specifically to solve the bridge currency problem.

---

## Blockchain Selection for Settlement

Stablecoins are tokens that live on blockchains. The same USDT can exist on Tron, Ethereum, Solana, and many other chains. Each chain has different performance characteristics, and the choice of chain directly impacts settlement cost and speed.

### Chain Comparison

```
    +-------------+----------+-----------+--------+-----------+
    | Property    |   Tron   | Ethereum  | Solana | Arbitrum  |
    |             |          | (L1)      |        | (ETH L2)  |
    +-------------+----------+-----------+--------+-----------+
    | Consensus   | DPoS     | PoS       | Tower  | Optimistic|
    |             | (21 SRs) | (~900K    | BFT    | Rollup    |
    |             |          | validators|        |           |
    +-------------+----------+-----------+--------+-----------+
    | Block Time  | 3 sec    | 12 sec    | 400 ms | 250 ms    |
    +-------------+----------+-----------+--------+-----------+
    | Practical   | ~60 sec  | ~15 min   | ~12 sec| ~10 min   |
    | Finality    | (20      | (finalized| (32    | (fraud    |
    |             |  blocks) |  epoch)   | slots) |  window)  |
    +-------------+----------+-----------+--------+-----------+
    | Gas Cost    | ~$0.50   | $2-$50    | ~$0.001| ~$0.10    |
    | (USDT xfer) |          |           |        |           |
    +-------------+----------+-----------+--------+-----------+
    | TPS         | ~2,000   | ~15       | ~65,000| ~4,000    |
    | (practical) |          |           |        |           |
    +-------------+----------+-----------+--------+-----------+
    | USDT Volume | $10B+/day| ~$3B/day  | ~$1B/d | ~$500M/d  |
    +-------------+----------+-----------+--------+-----------+
    | Uptime      | 99.9%+   | 99.99%    | ~99%   | 99.9%     |
    | (2024)      |          |           | (had   |           |
    |             |          |           | outages|           |
    +-------------+----------+-----------+--------+-----------+
    | Decentral-  | Low      | High      | Medium | Medium    |
    | ization     | (21 super| (900K+    | (few   | (inherits |
    |             | reps)    | validators| key    | ETH sec.) |
    |             |          |           | valid.)|           |
    +-------------+----------+-----------+--------+-----------+
```

### Why Tron Dominates Stablecoin Settlement

Tron carries over 60% of all USDT transfer volume. This dominance is not accidental -- it is a direct consequence of the economics:

**1. Cost.** A USDT transfer on Tron costs approximately $0.50. The same transfer on Ethereum L1 costs $2-50 depending on gas prices. For high-volume corridors where margins are thin, this difference determines profitability.

**2. Speed.** Tron produces blocks every 3 seconds. With 20 confirmations (Settla's threshold for crediting), practical finality is approximately 60 seconds. Ethereum finality requires an epoch boundary, roughly 15 minutes.

**3. Liquidity.** Because Tron has the most USDT volume, it also has the deepest liquidity. On-ramp and off-ramp providers in emerging markets (Africa, Southeast Asia, Latin America) overwhelmingly support Tron USDT. If your off-ramp provider in Lagos only accepts USDT on Tron, your chain choice is made for you.

**4. Simplicity.** Tron's fee model uses "bandwidth" and "energy" -- resources that are more predictable than Ethereum's gas auction. Transaction fees rarely spike above $2, even during peak congestion.

### The Centralization Tradeoff

Tron is governed by 21 Super Representatives elected by TRX token holders. This is far more centralized than Ethereum's 900,000+ validators. The practical implications:

```
    CENTRALIZATION RISK ANALYSIS
    =============================

    Tron (21 Super Representatives):
      + Fast consensus (fewer nodes to coordinate)
      + Low fees (less computational overhead)
      + Predictable block times
      - Tron Foundation has significant influence
      - Regulatory action against Tron could halt the chain
      - 51% attack requires compromising ~11 nodes
      - Justin Sun (founder) is a controversial figure

    Ethereum (900,000+ validators):
      + Extremely resilient to censorship
      + No single entity controls the network
      + Battle-tested security ($400B+ at stake)
      - Slower consensus (more coordination)
      - Higher fees (more overhead)
      - Finality takes longer
```

For settlement infrastructure, this tradeoff is acceptable because:

1. Transfers are in flight for seconds to minutes, not stored long-term
2. The settlement system does not depend on any single chain (multi-chain support)
3. The value at risk is the in-flight amount, not the total treasury
4. If Tron has issues, the router can redirect to Ethereum or Solana

### Settla's Chain Architecture

Settla's chain monitor watches multiple blockchains simultaneously:

```
    SETTLA CHAIN MONITORING
    ========================

    +------------------+     +------------------+     +------------------+
    |  Tron Poller     |     |  EVM Poller      |     |  Solana Poller   |
    |                  |     |  (ETH + L2s)     |     |  (planned)       |
    |  - TRC-20 events |     |  - ERC-20 events |     |  - SPL events    |
    |  - 3 sec blocks  |     |  - 12 sec blocks |     |  - 400ms slots   |
    |  - USDT primary  |     |  - USDT + USDC   |     |  - USDC primary  |
    +--------+---------+     +--------+---------+     +--------+---------+
             |                        |                        |
             v                        v                        v
    +-----------------------------------------------------------+
    |                   Token Registry                          |
    |  Maps: (chain, contract_address) -> (symbol, decimals)    |
    |  Tron USDT:  TR7NHqjeK...  (6 decimals)                  |
    |  ETH USDT:   0xdAC17F9...  (6 decimals)                  |
    |  ETH USDC:   0xA0b869...   (6 decimals)                  |
    +-----------------------------------------------------------+
             |
             v
    +-----------------------------------------------------------+
    |                   Deposit Engine                           |
    |  Matches on-chain transfers to deposit sessions            |
    |  Tracks confirmations (chain-specific depth)               |
    |  Triggers crediting after confirmation threshold           |
    +-----------------------------------------------------------+
```

The smart router selects the optimal chain for each transfer based on cost, speed, and provider availability. A GBP-to-NGN transfer might route through Tron for cost efficiency, while a high-value EUR-to-USD transfer might route through Ethereum for its deeper institutional liquidity.

> **Key Insight:** Chain selection is not a global configuration -- it is a per-transfer routing decision. The same corridor might use Tron for a $500 remittance and Ethereum for a $500,000 corporate settlement. The router's scoring algorithm (cost 40%, speed 30%, liquidity 20%, reliability 10%) makes this decision automatically.

---

## The Risks

Stablecoins are not risk-free. Every dollar that flows through a stablecoin bridge is exposed to risks that do not exist in traditional correspondent banking. Understanding these risks -- and their mitigations -- is essential for building settlement infrastructure.

### Risk 1: Depegging

A depeg occurs when a stablecoin's market price deviates significantly from its $1.00 target. This can happen quickly and without warning.

**Case study: UST/Luna collapse (May 2022)**

UST was an algorithmic stablecoin that maintained its peg through a mint/burn mechanism with its companion token LUNA. When large holders began selling UST, the algorithm minted more LUNA to absorb the selling pressure. But LUNA's price collapsed under the new supply, which destroyed confidence in UST, which caused more selling, which required more LUNA minting -- a classic death spiral.

```
    UST DEATH SPIRAL (May 7-13, 2022)
    ===================================

    May 7:   UST at $1.00    LUNA at $80
    May 8:   UST at $0.98    LUNA at $65    <- First wobble
    May 9:   UST at $0.69    LUNA at $30    <- Panic begins
    May 10:  UST at $0.30    LUNA at $2     <- Death spiral
    May 11:  UST at $0.15    LUNA at $0.10  <- Chain halted
    May 13:  UST at $0.02    LUNA at $0.00  <- $40B destroyed

    Total value destroyed: ~$40 billion
    Time from first wobble to collapse: 6 days
    Time from panic to irreversible: ~48 hours
```

**Case study: USDC depeg (March 2023)**

When Silicon Valley Bank failed on March 10, 2023, Circle disclosed that $3.3 billion of USDC reserves were held at SVB. Over the weekend, with no way to verify whether those reserves were safe, USDC traded as low as $0.87 on exchanges.

```
    USDC SVB DEPEG TIMELINE
    ========================

    Friday March 10:
      09:00  SVB declared insolvent by FDIC
      17:00  Circle discloses $3.3B exposure to SVB
      20:00  USDC drops to $0.95 on major exchanges

    Saturday March 11:
      12:00  Panic selling accelerates
      15:00  USDC hits $0.87 (13% depeg)
      18:00  Curve 3pool becomes heavily imbalanced
             (everyone dumping USDC for USDT/DAI)

    Sunday March 12:
      18:00  US Treasury announces all SVB deposits insured
      20:00  USDC recovers to $0.97

    Monday March 13:
      09:00  USDC back at $1.00

    Duration of depeg: ~60 hours
    Maximum deviation: 13%
    Root cause: Banking system contagion
```

**Settla's mitigation:**

```go
// Conceptual -- real-time peg monitoring
type PegMonitor struct {
    Threshold decimal.Decimal  // e.g., 0.02 (2% deviation)
}

// If the stablecoin deviates more than the threshold from $1.00,
// the circuit breaker trips and halts new transfers using that
// stablecoin until the peg recovers.
//
// Active transfers in flight are still processed -- the exposure
// is limited to the in-flight amount, not the full treasury.
```

- Only use fiat-backed stablecoins (never algorithmic)
- Monitor real-time peg deviation from multiple price feeds
- Circuit breaker halts new transfers if deviation exceeds threshold
- Support multiple stablecoins so traffic can reroute (USDT to USDC or vice versa)
- Minimize time holding stablecoins -- convert immediately on receipt

### Risk 2: Regulatory Risk

Stablecoin regulation is evolving rapidly and unevenly across jurisdictions.

**MiCA (EU Markets in Crypto-Assets Regulation):**
Effective June 2024, MiCA requires stablecoin issuers operating in the EU to:
- Hold 1:1 reserves in EU-regulated banks
- Obtain authorization as an Electronic Money Institution
- Limit daily transaction volume for non-euro stablecoins (200M EUR cap)

Tether has not obtained MiCA authorization. Some EU exchanges have delisted USDT. Circle has obtained EU authorization for USDC.

**US regulation:**
Multiple competing bills (Stablecoin TRUST Act, GENIUS Act) propose federal frameworks. The regulatory landscape remains uncertain, but the direction is toward requiring bank-like reserves and licensing.

**Practical impact on settlement:**

```
    REGULATORY FRAGMENTATION
    =========================

    Corridor: EUR -> NGN

    If routed through USDT:
      - USDT may not be available on EU-regulated exchanges
      - On-ramp provider must be outside MiCA scope
      - Adds compliance risk for EU-based tenants

    If routed through USDC:
      - USDC has MiCA authorization
      - EU on-ramp providers can legally handle USDC
      - Higher gas cost (USDC volume is primarily on Ethereum)

    Settla mitigation:
      - Per-corridor stablecoin selection
      - Router considers regulatory constraints as a routing factor
      - Support stablecoin swaps (USDT <-> USDC) for cross-regime transfers
```

### Risk 3: Counterparty Risk

When you hold USDT, you are trusting Tether Limited to honor its obligation to redeem USDT for $1.00. This is counterparty risk -- the risk that the other party cannot fulfill their obligation.

**What if Tether is insolvent?**

If Tether's actual reserves are less than the USDT in circulation, a bank-run scenario becomes possible:

```
    TETHER BANK RUN SCENARIO (Hypothetical)
    =========================================

    Assumptions:
      - $120B USDT in circulation
      - Actual reserves: $100B (hypothetical 83% backing)

    Phase 1: Rumor
      - Credible report surfaces suggesting reserve shortfall
      - Large holders begin redeeming USDT for USD
      - Tether processes redemptions normally (reserves sufficient)

    Phase 2: Acceleration
      - Daily redemptions exceed $5B
      - Tether must liquidate less-liquid assets
      - Redemption processing slows from hours to days

    Phase 3: Crisis
      - Market loses confidence in redemption capability
      - USDT trades at $0.90 on exchanges
      - More holders rush to redeem -> classic bank run

    Phase 4: Resolution
      - Either: Tether demonstrates reserves and peg recovers
      - Or: Tether freezes redemptions and USDT collapses

    Note: This has NOT happened. Tether has processed all
    redemptions to date, including during extreme market stress.
    But the risk exists because Tether's reserves are not fully
    transparent.
```

**Settla's mitigation:**
- Minimize duration of stablecoin holdings. Settla is a conduit, not a vault. Stablecoins are held for seconds to minutes during settlement, not days or weeks.
- Convert immediately on receipt. When the off-ramp provider receives USDT, it converts to fiat immediately.
- Diversify across USDT and USDC. If one issuer faces a crisis, the other can absorb the volume.
- Treasury positions are denominated in fiat, not stablecoins. Tenant positions are tracked in GBP, EUR, USD -- the stablecoin is purely a transit instrument.

### Risk 4: Smart Contract Risk

USDT and USDC are smart contracts deployed on blockchains. A bug in the contract code could allow an attacker to mint unlimited tokens, freeze transfers, or drain balances.

**Mitigating factors:**
- USDT and USDC contracts have billions of dollars at stake and have been running for years without exploit
- Both contracts are relatively simple (standard token interfaces, not complex DeFi logic)
- Both issuers have admin keys that can freeze specific addresses (a feature, not a bug, for compliance purposes -- but also a centralization risk)

**The admin key concern:**

```
    STABLECOIN ADMIN CAPABILITIES
    ==============================

    Both USDT and USDC smart contracts include admin functions:

    - blacklist(address)     Freeze a specific address
    - unblacklist(address)   Unfreeze an address
    - pause()               Halt ALL transfers globally
    - mint(amount)          Create new tokens
    - burn(amount)          Destroy tokens

    These functions exist for legal compliance (law enforcement
    can request address freezes). But they also mean:

    - A government could order ALL USDT/USDC transfers paused
    - Individual addresses can be frozen without warning
    - The issuer has unilateral control over the token

    For settlement: This is generally GOOD (compliance is
    required for institutional adoption) but creates a
    dependency on the issuer's operational integrity.
```

**Settla's mitigation:**
- Only use battle-tested contracts (USDT, USDC) with years of production history
- Monitor contract events for unexpected admin actions (pauses, large mints)
- Multi-chain support means a contract issue on one chain does not halt all settlement

### Risk 5: Chain Risk

The blockchain itself can fail. Outages, congestion, and chain reorganizations all threaten settlement.

**Solana outages:**
Solana has experienced multiple full outages since launch:
- September 2021: 17-hour outage
- Multiple shorter outages in 2022-2023
- Network degradation events in 2024

During an outage, no USDT/USDC transfers can be confirmed on that chain. In-flight transfers are stuck until the chain recovers.

**Ethereum congestion:**
During popular NFT mints or market crashes, Ethereum gas prices can spike 10-50x. A USDT transfer that normally costs $5 might cost $200, destroying margins on small transfers.

**Chain reorganizations (reorgs):**
A reorg occurs when the blockchain "rewinds" and replays recent blocks differently. A transfer that appeared confirmed can disappear after a reorg.

```
    CHAIN REORG SCENARIO
    =====================

    Block 100: Your USDT transfer included
    Block 101: Confirmed (1 confirmation)
    Block 102: Confirmed (2 confirmations)

    REORG: Chain rewinds to block 100 and forks

    Block 100': Different transactions (yours NOT included)
    Block 101': Your transfer is gone
    Block 102': Your transfer never happened

    This is why Settla waits for multiple confirmations:
      Tron:     20 confirmations (~60 seconds)
      Ethereum: Finalized epoch  (~15 minutes)
      Solana:   32 slots         (~12 seconds)

    Deeper confirmation = lower reorg probability
    Tron with 20 confirmations: reorg probability < 0.0001%
```

**Settla's mitigation:**

```
    MULTI-CHAIN RESILIENCE
    =======================

    Normal operation:
      Router selects optimal chain per transfer
      GBP->NGN corridor: Tron (cheapest)

    Tron degraded (high fees or slow blocks):
      Router detects elevated chain metrics
      Scoring penalizes Tron on speed/reliability
      Traffic shifts to Ethereum or Solana

    Tron outage:
      Chain monitor marks Tron as unavailable
      All traffic routes to alternative chains
      In-flight Tron transfers: wait for recovery
      New transfers: immediately route elsewhere

    Recovery:
      Chain monitor detects Tron blocks resuming
      Router gradually restores Tron traffic
      In-flight transfers complete normally
```

> **Key Insight:** The risk model for stablecoins in settlement is fundamentally different from the risk model for holding stablecoins as an investment. In settlement, the exposure window is seconds to minutes. You do not care about whether USDT will be worth $1.00 next year -- you care about whether it will be worth $1.00 for the next 60 seconds. This radically simplifies risk management: short exposure windows, immediate conversion, multi-chain failover, and circuit breakers for extreme events.

---

## The Stablecoin Settlement Flow in Settla

Now that we understand the properties and risks of stablecoins, let us trace how they fit into Settla's settlement architecture. Compare the traditional correspondent banking flow with the stablecoin-bridged flow:

```
    TRADITIONAL CORRESPONDENT BANKING
    ===================================

    GBP 1,000 from London to Lagos

    Day 0:
      Sender's bank (Barclays) debits sender's account
           |
      Barclays sends SWIFT MT103 to correspondent bank (HSBC)
           |  Fee: $15 (SWIFT messaging)
           |
    Day 1:
      HSBC processes the payment
           |
      HSBC sends to USD intermediary (Citibank NY)
           |  Fee: $25 (correspondent fee)
           |  FX spread: 1.5% (GBP -> USD)
           |
    Day 2:
      Citibank processes and routes to Nigeria
           |
      Citibank sends to Nigerian correspondent (Zenith Bank)
           |  Fee: $30 (cross-border fee)
           |  FX spread: 2.0% (USD -> NGN)
           |
    Day 3-5:
      Zenith Bank credits recipient's account at GTBank
           |  Fee: $10 (local clearing)
           |
      Recipient receives NGN

    TOTAL TIME:  3-5 business days
    TOTAL COST:  $80 in fees + 3.5% FX spread = ~$115 on GBP 1,000
    HOPS:        4 intermediaries
    VISIBILITY:  None (sender cannot track payment between banks)


    STABLECOIN SETTLEMENT (Settla)
    ================================

    GBP 1,000 from London to Lagos

    Second 0:
      Settla API receives transfer request
           |
      Engine reserves GBP 1,000 from tenant treasury (in-memory, ~100ns)
           |
    Second 1-5:
      On-ramp provider converts GBP to USDT
           |  Fee: 0.40% ($5.08)
           |  FX: GBP 1,000 = 1,270 USD = 1,269 USDT (after fee)
           |
    Second 5-10:
      USDT sent on Tron blockchain
           |  Fee: ~$0.50 (gas)
           |  TxHash: visible on tronscan.org
           |
    Second 10-70:
      20 confirmations on Tron (~60 seconds)
           |
    Second 70-75:
      Off-ramp provider converts USDT to NGN
           |  Fee: 0.35% ($4.44)
           |  FX: 1,269 USDT x 1,580 NGN = 2,005,020 NGN
           |
    Second 75-90:
      NGN credited to recipient's bank account
           |
      Webhook sent to tenant: transfer.completed

    TOTAL TIME:  30-90 seconds
    TOTAL COST:  $10.02 in fees = ~0.75%
    HOPS:        3 (on-ramp, blockchain, off-ramp)
    VISIBILITY:  Full (every state change tracked, on-chain hash auditable)
```

The stablecoin bridge turns a multi-day, opaque, expensive process into a sub-minute, transparent, cheap one. But this simplicity is only possible because the settlement infrastructure handles the complexity: routing, treasury management, confirmation tracking, failure recovery, and reconciliation.

### Where Stablecoins Sit in the Architecture

Stablecoins appear at specific points in Settla's architecture:

```
    STABLECOIN TOUCHPOINTS IN SETTLA
    ==================================

    1. QUOTING (rail/router)
       Router evaluates stablecoin options per corridor
       GBP->NGN: USDT on Tron (cheapest) or USDC on Ethereum (regulated)
       Returns: selected stablecoin, chain, estimated gas, FX rate

    2. ON-RAMP (rail/provider)
       Provider converts fiat to stablecoin
       Input:  GBP 1,000
       Output: 1,269 USDT (TRC-20)
       Provider holds USDT in hot wallet until Settla claims it

    3. BLOCKCHAIN TRANSFER (rail/blockchain)
       Settla sends USDT on-chain from hot wallet to off-ramp provider
       Chain monitor tracks confirmation depth
       BlockchainWorker manages the send + confirmation lifecycle

    4. OFF-RAMP (rail/provider)
       Provider converts stablecoin to destination fiat
       Input:  1,269 USDT (TRC-20)
       Output: 2,005,020 NGN
       Provider triggers local bank payout

    5. TREASURY (treasury/)
       Treasury positions are in FIAT, not stablecoins
       Stablecoin is transient -- exists only during steps 2-4
       No long-term stablecoin holding = minimal depeg exposure

    6. LEDGER (ledger/)
       Records stablecoin legs as postings:
         Debit:  assets:crypto:usdt:tron     1,269 USDT
         Credit: liabilities:tenant:lemfi     1,269 USDT
       Balanced double-entry -- even the crypto leg balances

    7. DEPOSITS (core/deposit)
       Crypto deposit sessions watch for INBOUND stablecoin payments
       Chain monitor detects USDT/USDC sent to deposit addresses
       Confirmation tracking before crediting tenant
```

---

## How Stablecoins Are Represented in Code

Stablecoins are not special-cased in Settla's domain model. They are currencies like any other, which flow through the same `Money` type, `Posting` type, and `Currency` constants:

```go
// From domain/money.go -- stablecoins are just currencies
const (
    CurrencyUSDT Currency = "USDT"
    CurrencyUSDC Currency = "USDC"
    CurrencyDAI  Currency = "DAI"
    // ... alongside fiat currencies
    CurrencyGBP  Currency = "GBP"
    CurrencyNGN  Currency = "NGN"
    CurrencyUSD  Currency = "USD"
)

// From domain/crypto.go -- chain-specific details
type Chain string

const (
    ChainTron     Chain = "TRON"
    ChainEthereum Chain = "ETHEREUM"
    ChainSolana   Chain = "SOLANA"
)

// A stablecoin is identified by (currency, chain) pair:
//   USDT on Tron  = (CurrencyUSDT, ChainTron)
//   USDC on ETH   = (CurrencyUSDC, ChainEthereum)
//   USDT on ETH   = (CurrencyUSDT, ChainEthereum)  -- same currency, different chain
```

The `Corridor` type explicitly includes the stablecoin as the bridge:

```go
// From domain/provider.go
type Corridor struct {
    SourceCurrency Currency   // GBP
    StableCoin     Currency   // USDT
    DestCurrency   Currency   // NGN
}
```

This three-part structure -- source, bridge, destination -- is the fundamental model for stablecoin settlement. The bridge currency is always a stablecoin. The source and destination are always fiat. The corridor determines which providers and chains are available.

---

## Common Misconceptions

### "Stablecoins are always stable"

No. They are designed to be stable, but they can and do depeg. USDC lost 13% of its value over a weekend. USDT has dipped below $0.95. These events are rare but have occurred during market stress. Your system must handle them.

### "USDT is unsafe because Tether is shady"

Tether's transparency issues are real, but USDT has processed trillions of dollars in redemptions without default. The practical risk for settlement (where exposure is measured in seconds) is different from the risk for long-term holders. That said, supporting USDC as an alternative is not optional -- it is risk management.

### "Decentralized stablecoins are safer"

DAI is more transparent (on-chain collateral is auditable) but not necessarily safer for settlement. Its peg has deviated more than USDT's during market stress. And its dependence on volatile collateral introduces risks that fiat-backed stablecoins do not have.

### "Just use the cheapest chain"

Gas cost is one factor, not the only factor. If your off-ramp provider only accepts USDT on Tron, it does not matter that Solana is cheaper. If Tron is experiencing degraded performance, Ethereum's higher cost is worth the reliability. The router must balance cost, speed, liquidity, reliability, and provider compatibility.

### "Stablecoins will be regulated out of existence"

Regulation is increasing, but the direction is toward licensing and reserve requirements, not prohibition. MiCA, the most comprehensive crypto regulation, creates a framework for authorized stablecoin issuance. Circle has proactively pursued this licensing. The industry is moving toward regulated stablecoins, not away from stablecoins entirely.

---

## Exercises

### Exercise 1: Settlement Cost Comparison

Compare the total settlement cost for a $100,000 GBP-to-NGN transfer via two methods:

**Method A: Correspondent banking**
- Time: 3 business days
- Fees: 3% total ($3,000)
- Opportunity cost: assume the $100,000 could earn 5% annualized returns if not locked in transit

**Method B: Stablecoin settlement**
- Time: 60 seconds
- Fees: 0.75% total ($750)
- Opportunity cost: negligible (60 seconds of locked capital)

Calculate:
1. The direct fee savings (Method A fees minus Method B fees)
2. The opportunity cost of 3 days of locked capital at 5% annualized
3. The total savings (direct + opportunity cost)
4. If a fintech processes 1,000 such transfers per day, what are the annual savings?

### Exercise 2: Peg Deviation Research

Research the current market status of USDT and USDC:
1. What is the current price of USDT and USDC on major exchanges (Binance, Coinbase)?
2. Have they deviated from $1.00 in the past 30 days? What was the maximum deviation?
3. Check Tether's latest transparency report (https://tether.to/en/transparency/). What percentage of reserves are in US Treasuries?
4. Check Circle's latest attestation report. How do USDC reserves compare to Tether's?

### Exercise 3: In-Flight Exposure Calculation

Settla processes 50M transfers per day at an average value of $500. Assume:
- Average transit time: 60 seconds (from on-ramp to off-ramp)
- All transfers use USDT on Tron
- Transfers are evenly distributed throughout the day (580 TPS)

Calculate:
1. How many transfers are in flight at any given moment?
2. What is the total dollar value of USDT in transit at any given moment?
3. If USDT depegs to $0.95 (a 5% drop), what is the maximum loss on in-flight transfers?
4. If USDT depegs to $0.95 and Settla's circuit breaker halts new transfers within 10 seconds, what is the actual loss exposure? (Hint: only transfers already in flight are exposed. No new transfers enter the pipeline after the circuit breaker trips.)
5. How does this compare to the daily fee revenue at 0.75% average fee? Is the risk manageable?

### Exercise 4: Chain Selection Decision

You are configuring Settla's router for a new corridor: EUR-to-KES (Euro to Kenyan Shilling). Your off-ramp provider in Kenya supports:
- USDT on Tron
- USDC on Ethereum
- USDC on Solana (beta, occasional timeouts)

For each of the following transfer profiles, recommend which stablecoin and chain to use, and justify your choice:

1. High-volume remittances: 10,000 transfers/day, average $200, cost-sensitive
2. Corporate treasury settlement: 50 transfers/day, average $50,000, reliability-critical
3. E-commerce merchant payouts: 5,000 transfers/day, average $50, speed-sensitive

---

## What's Next

You now understand the bridge currency that makes stablecoin settlement possible -- what stablecoins are, how they maintain their peg, which ones to use, which blockchains to use them on, and what can go wrong. In the next chapter, we will look at the other side of the bridge: fiat currencies, FX rates, and the money domain primitives that Settla uses to represent monetary values without the rounding errors that destroy ledgers at scale.

---
