# Chapter 0.4: Settlement, Clearing, and Netting -- The Financial Concepts

**Estimated reading time:** 30 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Distinguish between payment, clearing, and settlement
2. Explain gross settlement vs net settlement with concrete examples
3. Calculate net positions from a set of transfers
4. Explain why Settla supports both PREFUNDED and NET_SETTLEMENT models
5. Understand counterparty risk and credit risk in net settlement

---

## 1. Three Distinct Concepts

Most engineers treat "payment" and "settlement" as synonyms. They are not. The
financial industry distinguishes three phases in every transfer of value, and
confusing them leads to broken reconciliation, incorrect balances, and subtle
bugs that only surface under production load.

```
    PAYMENT:    "I owe you $100"            (the instruction)
    CLEARING:   "Let me verify and prepare"  (validation, netting, queuing)
    SETTLEMENT: "The money has moved"        (final, irrevocable transfer of value)
```

### Payment: The Instruction

A payment is an intent to move money. It is a message, not a movement. When
you tap "Send" in Venmo, you have created a payment instruction. No money has
moved yet. Venmo records the obligation in its internal ledger, updates both
users' displayed balances, and queues the actual fund movement for later.

When a fintech calls Settla's API with `POST /v1/transfers`, that request is a
payment instruction. Settla validates the request, checks the tenant's balance,
and creates a transfer record in `CREATED` state. But the GBP has not left the
tenant's treasury, the stablecoin has not moved on-chain, and the NGN has not
arrived at the recipient's bank. Those are all settlement steps.

### Clearing: The Preparation

Clearing is everything that happens between the payment instruction and the
actual movement of funds. It includes:

- **Validation**: Is the instruction well-formed? Does the sender have funds?
  Are the recipient details correct?
- **Netting**: Can multiple instructions be combined to reduce the total
  amount that needs to move? (More on this below.)
- **Queuing**: In what order should instructions be processed? Are there
  priority levels?
- **Risk assessment**: Does this transfer exceed any limits? Is it flagged
  for compliance review?

In the stock market, clearing is handled by a Central Counterparty (CCP) like
the DTCC. When you buy 100 shares of Apple, the CCP interposes itself between
buyer and seller, guaranteeing both sides of the trade. If either party
defaults, the CCP absorbs the loss. Clearing happens at T+0 (trade day); the
shares and cash do not actually change hands until settlement at T+1.

In Settla, the clearing phase corresponds roughly to the `CREATED -> FUNDED`
transition: the engine validates the transfer, checks corridor availability,
verifies the tenant has sufficient treasury balance, and reserves funds. The
transfer is "cleared" -- approved for execution -- but settlement has not yet
occurred.

### Settlement: The Movement

Settlement is the final, irrevocable transfer of value. After settlement, the
transaction cannot be reversed through normal means. The money has moved.

In traditional banking, settlement means the central bank has updated its
ledger. When the Bank of England debits one bank's reserve account and credits
another's, that is settlement -- it is final and irrevocable.

On a blockchain, settlement means the transaction has been confirmed by a
sufficient number of blocks. A USDT transfer on Tron is "settled" after enough
block confirmations that a chain reorganization is astronomically unlikely.

In Settla, a transfer is settled when it reaches `COMPLETED` state: the
on-ramp converted fiat to stablecoin, the blockchain transferred the
stablecoin, the off-ramp converted stablecoin to fiat, the recipient received
the funds, and all ledger entries balance. The transfer is now final.

> **Key Insight:** These three phases can happen at very different times. Your
> Venmo payment is instant (the instruction is recorded immediately), but
> settlement happens hours later when Venmo batches ACH transfers to move real
> bank funds. A SWIFT message is a payment instruction that might take 2-5
> business days to settle through the correspondent banking chain. Understanding
> which phase you are in at any given moment is essential for building correct
> financial infrastructure.

---

## 2. Real-World Settlement Systems

Before diving into Settla's model, it is worth understanding how settlement
works in systems you interact with every day. Each one illustrates different
trade-offs between speed, cost, risk, and liquidity.

### Card Networks (Visa, Mastercard)

When you tap your credit card at a coffee shop, three things happen at
different times:

```
    Time 0 (milliseconds):     AUTHORIZATION
    +-----------------------------------------------------------+
    | Visa sends an auth request to your bank                   |
    | Bank checks your credit limit and approves                |
    | Merchant gets an approval code                            |
    | Your "available credit" decreases by $4.50                |
    | NO MONEY HAS MOVED YET                                   |
    +-----------------------------------------------------------+

    Time +1 day:               CLEARING
    +-----------------------------------------------------------+
    | Merchant submits the day's transactions in a batch        |
    | Visa's clearing system matches auths to settlements       |
    | Interchange fees are calculated                           |
    | Transactions are netted across all merchants and banks    |
    +-----------------------------------------------------------+

    Time +1-2 days:            SETTLEMENT
    +-----------------------------------------------------------+
    | Visa calculates net positions for each bank               |
    | Instead of 10 million individual transfers:               |
    |   Bank A owes Visa: $12.3M                                |
    |   Bank B is owed by Visa: $8.1M                           |
    |   Bank C owes Visa: $3.7M                                 |
    | Net positions are settled through the Federal Reserve     |
    | MONEY ACTUALLY MOVES                                      |
    +-----------------------------------------------------------+
```

The entire card network runs on deferred net settlement. Billions of dollars
in daily card transactions are reduced to a handful of net positions between
banks. This is why your credit card statement shows a "pending" charge that
later becomes "posted" -- authorization is not settlement.

### ACH (Automated Clearing House)

ACH is the backbone of US bank-to-bank transfers: direct deposits, bill
payments, payroll. It settles in batches, typically twice per day.

```
    Batch 1 (morning):
      Company A sends payroll:    1,000 credits totaling $2.5M
      Company B collects bills:   5,000 debits totaling $800K
      Company C sends refunds:    200 credits totaling $150K

    Clearing:
      All entries are sorted by receiving bank
      Entries between the same banks are netted
      Bank X net position: +$1.2M (receives more than it sends)
      Bank Y net position: -$800K (sends more than it receives)

    Settlement:
      Federal Reserve adjusts bank reserve accounts
      One transfer per net position
      Total: ~$3.45M gross reduced to a few net movements
```

ACH processes over 30 billion transactions per year in the US. Without netting,
the banking system would collapse under the volume.

### Fedwire (Real-Time Gross Settlement)

Fedwire is the opposite of ACH. Every transfer settles individually, in real
time, with immediate finality. There is no netting, no batching, no delay.

```
    10:00:00.000  Bank A sends $50M to Bank B
                  Federal Reserve debits A, credits B
                  SETTLED. FINAL. IRREVOCABLE.

    10:00:00.150  Bank C sends $20M to Bank A
                  Federal Reserve debits C, credits A
                  SETTLED. FINAL. IRREVOCABLE.
```

Each Fedwire transfer requires the sending bank to have sufficient reserves
at the moment of transfer. This eliminates counterparty risk entirely -- you
cannot owe money through Fedwire because the money moves before the message
completes. But it demands enormous liquidity. Banks must maintain large reserve
balances at the Federal Reserve to cover their peak daily Fedwire volume.

> **Key Insight:** There is a fundamental trade-off between liquidity
> requirements and counterparty risk. Gross settlement (Fedwire) eliminates
> counterparty risk but demands massive liquidity. Net settlement (ACH, card
> networks) reduces liquidity requirements dramatically but introduces the risk
> that a participant cannot meet their net obligation. Every settlement system
> in the world sits somewhere on this spectrum. So does Settla.

---

## 3. Gross Settlement: The PREFUNDED Model

In gross settlement, every transaction is settled individually. There is no
accumulation, no netting, and no deferred obligations. Each transfer moves the
full amount at the time of execution.

Settla's PREFUNDED model is effectively gross settlement:

```
    GROSS SETTLEMENT (PREFUNDED in Settla)

    Lemfi pre-deposits GBP 500,000 into their Settla treasury position.

    Transfer 1: Lemfi sends GBP 1,000     -> Settla reserves GBP 1,000
    Transfer 2: Lemfi sends GBP 2,000     -> Settla reserves GBP 2,000
    Transfer 3: Lemfi sends GBP 500       -> Settla reserves GBP 500
                                              ────────────────────────
                                              Total reserved: GBP 3,500
                                              Remaining: GBP 496,500

    Each transfer is settled independently.
    Each reservation is backed by real, pre-deposited funds.
    No credit risk. No counterparty exposure.
```

### How It Works in Settla

When a PREFUNDED tenant calls `POST /v1/transfers`, the engine performs an
atomic treasury reservation:

```
    Engine.CreateTransfer()
        |
        +-> Validate transfer details
        +-> Check corridor availability
        +-> Get quote (FX rate, fees)
        +-> Treasury.Reserve(tenantID, currency, amount)
        |       |
        |       +-> In-memory atomic decrement (~100ns)
        |       +-> If insufficient: return INSUFFICIENT_FUNDS immediately
        |       +-> If sufficient: balance decremented, transfer proceeds
        |
        +-> Write transfer record + outbox entries atomically
        +-> Return transfer ID to caller
```

The reservation is in-memory and atomic. There is no database round-trip, no
row lock, no contention. This is critical because at 580 TPS sustained, a
database `SELECT FOR UPDATE` on the treasury position row would create a serial
bottleneck. (We will explore this "hot key problem" in detail in Module 4.)

The key property of gross settlement: **at no point does Settla extend credit
to the tenant.** Every dollar that moves through the system was pre-deposited.
If the tenant's balance hits zero, new transfers are rejected instantly. Settla
bears zero credit risk.

### The Liquidity Cost

The downside of gross settlement is capital efficiency. Consider a tenant
processing $10M per day in transfers:

```
    Scenario: Fincra processes $10M/day, average transfer completes in 60 seconds

    At any given moment:
      In-flight transfers: ~$10M / (86,400 sec/day) * 60 sec = ~$6,944
      Peak in-flight (5x average): ~$34,722

    But the tenant must pre-fund for the DAILY total, not the in-flight amount:
      Required pre-funded position: $10M+ (enough to cover a full day)
      Plus safety buffer: $12-15M recommended

    That is $12-15M of working capital locked in Settla's treasury,
    earning nothing, unavailable for other business purposes.
```

For a small fintech processing $500K/day, locking up $600K in a treasury
position is manageable. For a large fintech processing $50M/day, locking up
$60M is a serious constraint on their business.

This is exactly why net settlement exists.

---

## 4. Net Settlement: Reducing Liquidity Requirements

In net settlement, transactions accumulate over a defined period (typically 24
hours). At the end of the period, all transactions are "netted" -- amounts
flowing in opposite directions cancel each other out -- and only the net
difference is settled.

### A Detailed Example

Let us trace a full day of transfers for Fincra, a tenant on the
NET_SETTLEMENT model:

```
    NET SETTLEMENT PERIOD: 2024-03-15 00:00 UTC to 2024-03-16 00:00 UTC

    ================================================================
    FINCRA -> SETTLA TRANSFERS (fiat out, stablecoin in)
    ================================================================

    Transfer 1:  GBP 10,000 -> NGN    ($12,700 USD equivalent)
    Transfer 2:  GBP 5,000  -> NGN    ($6,350 USD equivalent)
    Transfer 3:  GBP 8,000  -> NGN    ($10,160 USD equivalent)
    Transfer 4:  GBP 3,000  -> GHS    ($3,810 USD equivalent)
    Transfer 5:  EUR 6,000  -> NGN    ($6,540 USD equivalent)

    Subtotal (Fincra owes Settla):     $39,560

    ================================================================
    SETTLA -> FINCRA TRANSFERS (reverse corridor: fiat in, stablecoin out)
    ================================================================

    Transfer 6:  NGN -> GBP 3,000     ($3,810 USD equivalent)
    Transfer 7:  NGN -> GBP 2,000     ($2,540 USD equivalent)
    Transfer 8:  GHS -> GBP 1,500     ($1,905 USD equivalent)

    Subtotal (Settla owes Fincra):     $8,255

    ================================================================
    NETTING CALCULATION
    ================================================================

    Fincra owes Settla:   $39,560
    Settla owes Fincra:    $8,255
    ─────────────────────────────────────
    NET: Fincra owes Settla $31,305

    Instead of 8 individual settlements totaling $47,815 gross,
    ONE payment of $31,305 settles everything.
```

### Per-Currency Netting

In practice, netting is more nuanced. Settla nets per currency pair, not
just in aggregate USD terms, because actual fund movements happen in specific
currencies:

```
    NETTING BY CURRENCY PAIR

    GBP Corridor:
      Fincra -> Settla:  GBP 10,000 + 5,000 + 8,000 = GBP 23,000
      Settla -> Fincra:  GBP 3,000 + 2,000           = GBP 5,000
      NET GBP: Fincra owes GBP 18,000

    EUR Corridor:
      Fincra -> Settla:  EUR 6,000
      Settla -> Fincra:  (none)
      NET EUR: Fincra owes EUR 6,000

    GHS Corridor:
      Fincra -> Settla:  (GBP 3,000 equivalent -- source is GBP)
      Settla -> Fincra:  (GBP 1,500 equivalent -- dest is GBP)
      (These net within the GBP totals above)

    SETTLEMENT INSTRUCTIONS:
      1. Fincra pays Settla GBP 18,000
      2. Fincra pays Settla EUR 6,000
```

This is how Settla's `Calculator.CalculateNetSettlement()` works. It groups
completed transfers by corridor (source currency, destination currency),
computes net positions per currency, and generates `SettlementInstruction`
records:

```go
// From core/settlement/calculator.go (simplified)

// Steps:
//  1. Query completed transfers for tenant in [periodStart, periodEnd)
//  2. Group by corridor (source_currency -> dest_currency)
//  3. Net per currency (sum inflows - sum outflows)
//  4. Apply fee schedule from tenant
//  5. Generate settlement instructions
//  6. Store in net_settlements table
//  7. Return the settlement record
```

The settlement scheduler triggers this calculation daily at 00:30 UTC for every
tenant on the NET_SETTLEMENT model.

> **Key Insight:** Netting does not change the total value transferred. Every
> individual transfer still executes in full -- the on-ramp converts the full
> fiat amount, the blockchain moves the full stablecoin amount, the off-ramp
> pays the full destination amount. Netting only changes **how the tenant
> settles with Settla**. Instead of pre-funding each transfer, the tenant
> accumulates a net obligation and settles it once per day.

---

## 5. Why Netting Matters at Scale

The mathematics of netting become dramatic at Settla's target scale:

```
    WITHOUT NETTING (Gross Settlement for all tenants)
    ──────────────────────────────────────────────────
    50,000,000 transfers/day
    x $500 average value
    = $25,000,000,000 daily gross volume

    Each transfer requires pre-funded liquidity.
    Total liquidity locked across all tenants: $25B+
    Individual settlements: 50,000,000 per day

    WITH NETTING (Net Settlement for large tenants)
    ──────────────────────────────────────────────────
    Same 50M transfers, but with bidirectional flows:
    Assume 30% of transfers flow in the reverse direction.

    Gross volume: $25B
    Reverse flows: $7.5B
    Net volume: $17.5B  (30% reduction)

    But the real savings are in settlement operations:
    Instead of 50M individual settlements:
    ~500 tenants x ~5 currency pairs = ~2,500 net positions/day

    Settlement operations reduced by 99.995%.
```

This is not theoretical. Card networks like Visa process billions of
transactions per day and reduce them to a few thousand net positions between
banks. Without netting, the global financial system would require orders of
magnitude more liquidity and operational capacity than it currently uses.

For Settla specifically:
- The net settlement calculator runs daily at 00:30 UTC
- It iterates over all NET_SETTLEMENT tenants using cursor-based pagination
- For each tenant, it calls `AggregateCompletedTransfersByPeriod()`, which
  uses a server-side `GROUP BY` to avoid materializing 50M individual rows
- It generates `NetSettlement` records with `CorridorPosition` breakdowns
  and `SettlementInstruction` directives
- Settlement is due T+3 (three business days after the period end)

---

## 6. The PREFUNDED vs NET_SETTLEMENT Decision

Settla supports both models because different tenants have fundamentally
different needs. This is not a technical preference -- it is a business
decision driven by risk tolerance, capital structure, and volume.

```
    TENANT LIFECYCLE AND SETTLEMENT MODEL

    New Tenant (Onboarding)
        |
        +-> KYB verification (PENDING -> IN_REVIEW -> VERIFIED)
        +-> Settlement model: PREFUNDED (default)
        +-> Small credit line: $0 (must pre-fund everything)
        |
    Growing Tenant (6+ months, good history)
        |
        +-> Volume: $1M+/day
        +-> Payment history: no missed settlements
        +-> Request upgrade to NET_SETTLEMENT
        +-> Credit assessment: financial statements, volume history
        +-> Credit limit assigned: e.g., $5M net exposure
        |
    Large Tenant (established)
        |
        +-> Volume: $10M+/day
        +-> Settlement model: NET_SETTLEMENT
        +-> Credit limit: $20M
        +-> Negotiated fee schedule (lower bps for higher volume)
        +-> Daily net settlement cycle
```

### Comparison Table

```
    +------------------+-------------------------+---------------------------+
    | Aspect           | PREFUNDED               | NET_SETTLEMENT            |
    +------------------+-------------------------+---------------------------+
    | Liquidity        | Tenant pre-deposits     | Settla extends credit     |
    |                  | funds before trading    | for the settlement period |
    +------------------+-------------------------+---------------------------+
    | Credit risk      | Zero for Settla         | Settla bears counterparty |
    |                  |                         | risk until settlement     |
    +------------------+-------------------------+---------------------------+
    | Capital          | Tenant's capital is     | Tenant's capital is free  |
    | efficiency       | locked in treasury      | for other business uses   |
    +------------------+-------------------------+---------------------------+
    | Fee schedule     | Standard rates          | Premium pricing for the   |
    |                  |                         | credit facility           |
    +------------------+-------------------------+---------------------------+
    | Settlement       | Real-time (each         | Daily (net positions at   |
    | cycle            | transfer is backed)     | 00:30 UTC, due T+3)      |
    +------------------+-------------------------+---------------------------+
    | Max exposure     | Enforced by treasury    | Enforced by credit limit  |
    | control          | balance (hard cap)      | (configurable per tenant) |
    +------------------+-------------------------+---------------------------+
    | Failure mode     | Transfer rejected with  | Tenant may default on     |
    |                  | INSUFFICIENT_FUNDS      | net obligation            |
    +------------------+-------------------------+---------------------------+
    | Typical tenant   | Smaller fintechs, new   | Large fintechs with       |
    |                  | tenants, low volume     | track record, high volume |
    +------------------+-------------------------+---------------------------+
```

### Why Both Must Exist

Consider two real tenant profiles:

**Tenant A: Small Fintech Startup**
- Monthly volume: $2M
- Company age: 8 months
- No audited financials
- No credit history with Settla

Offering this tenant net settlement would be reckless. They have no track
record, no financial cushion if things go wrong, and no collateral. If they
accumulate $500K in net obligations and cannot pay, Settla absorbs the loss.
PREFUNDED is the only responsible option.

**Tenant B: Established Payment Processor (e.g., Fincra)**
- Monthly volume: $500M
- Company age: 5 years
- Audited financials, profitable
- 18-month track record with Settla, zero missed settlements

Requiring Fincra to pre-fund every transfer would mean locking up $15-20M
in Settla's treasury. That capital could be earning returns, funding growth,
or meeting regulatory capital requirements elsewhere. The opportunity cost is
enormous. Net settlement frees that capital while Settla manages the modest
credit risk of a proven counterparty.

> **Key Insight:** The PREFUNDED model prioritizes safety (zero credit risk)
> at the cost of capital efficiency. The NET_SETTLEMENT model prioritizes
> capital efficiency at the cost of credit risk. Both are correct for their
> respective use cases. A settlement platform that only offers one model will
> either lose large tenants (PREFUNDED-only) or take on unmanaged risk
> (NET_SETTLEMENT-only). Settla's per-tenant configuration in `domain.Tenant`
> with `SettlementModel` is not a convenience feature -- it is a risk
> management tool.

---

## 7. Counterparty Risk in Net Settlement

The defining risk of net settlement is counterparty risk: the possibility that
a participant cannot meet their net obligation when settlement is due.

### The Anatomy of a Default

Let us trace a worst-case scenario:

```
    Day 1 (00:00-23:59 UTC): Normal operations
    ──────────────────────────────────────────────
    Fincra processes 12,000 transfers through Settla.
    Gross volume: $8.5M
    Reverse flows: $1.2M
    Net obligation: $7.3M (Fincra owes Settla)

    During this day, Settla has:
    - Paid on-ramp providers to convert GBP/EUR to USDT:     $8.5M
    - Paid blockchain gas fees:                                $6K
    - Paid off-ramp providers to convert USDT to NGN/GHS:    $8.5M
    - Received from reverse off-ramps:                        $1.2M

    Settla's cash outlay: ~$7.3M (net of reverse flows)

    Day 2 (00:30 UTC): Settlement calculation
    ──────────────────────────────────────────────
    Calculator runs. NetSettlement record created:
      - Net amount: $7,300,000
      - Status: "pending"
      - Due date: Day 5 (T+3)

    Day 5 (due date): Fincra does not pay
    ──────────────────────────────────────────────
    Settlement status: "overdue"
    Settla has spent $7.3M of its own capital on provider payments.
    Fincra has not reimbursed.

    Day 8 (T+3 after due): Escalation
    ──────────────────────────────────────────────
    Fincra still has not paid.
    Settla suspends Fincra's account.
    Legal recovery process begins.

    FINANCIAL IMPACT ON SETTLA:
      Capital at risk:     $7,300,000
      Earned fees:            $54,750  (75 bps on $7.3M)
      Net loss if default:  $7,245,250

    It takes 132 days of Fincra's fees to recover this loss.
```

This is why counterparty risk management is not optional for net settlement.
A single default can wipe out months of revenue.

### Mitigations in Settla

Settla implements multiple layers of protection against counterparty default:

```
    Layer 1: CREDIT LIMITS
    ──────────────────────────────────────────────
    Each NET_SETTLEMENT tenant has a maximum net exposure limit.

    Example: Fincra's credit limit = $10M

    If Fincra's accumulated net obligation reaches $10M during
    the day, new transfers are REJECTED until the current
    settlement cycle completes and Fincra pays.

    This caps Settla's maximum loss per tenant per cycle.


    Layer 2: SHORT SETTLEMENT CYCLES
    ──────────────────────────────────────────────
    Daily settlement (not weekly, not monthly).

    Maximum exposure window: 24 hours of accumulation + 3 days
    to pay = 4 days total.

    Compare: if settlement were monthly, Settla could accumulate
    30 days of exposure before discovering a problem.


    Layer 3: OVERDUE ESCALATION
    ──────────────────────────────────────────────
    Automated escalation timeline:

    Due date + 0 days:   Status -> "overdue"
    Due date + 3 days:   Automated reminder to tenant
    Due date + 5 days:   Warning: account suspension imminent
    Due date + 7 days:   Account SUSPENDED, no new transfers
    Due date + 14 days:  Escalation to legal/collections


    Layer 4: KYB VERIFICATION
    ──────────────────────────────────────────────
    NET_SETTLEMENT is only available to KYB-verified tenants.

    KYBStatus must be VERIFIED before a tenant can be upgraded
    from PREFUNDED to NET_SETTLEMENT.

    This ensures Settla has:
    - Verified the company's legal identity
    - Reviewed financial statements
    - Confirmed the business is legitimate


    Layer 5: COLLATERAL REQUIREMENTS
    ──────────────────────────────────────────────
    For large credit lines, Settla may require collateral:
    - Cash deposit held in escrow
    - Bank guarantee or letter of credit
    - Parent company guarantee

    If the tenant defaults, collateral covers part or all
    of the loss.
```

> **Key Insight:** No single mitigation eliminates counterparty risk. Credit
> limits cap the maximum loss. Short cycles limit the exposure window. KYB
> filters out obviously risky counterparties. Escalation catches problems
> early. Collateral provides recovery. Together, they reduce the probability
> and severity of loss to acceptable levels. This layered approach is standard
> practice in financial infrastructure -- the same pattern is used by
> clearinghouses, card networks, and interbank settlement systems.

---

## 8. Multilateral Netting

The examples above are all bilateral: one tenant netting against Settla. But
when multiple tenants are involved, netting becomes even more powerful.

### Bilateral vs Multilateral

Consider three tenants all settling through Settla:

```
    BILATERAL NETTING (current Settla model)
    ──────────────────────────────────────────────

    Tenant A <-> Settla:
      A owes Settla: $100,000
      Settla owes A:  $30,000
      NET: A pays Settla $70,000

    Tenant B <-> Settla:
      B owes Settla:  $80,000
      Settla owes B:  $60,000
      NET: B pays Settla $20,000

    Tenant C <-> Settla:
      C owes Settla:  $50,000
      Settla owes C:  $90,000
      NET: Settla pays C $40,000

    Total payments: 3 transfers
      A -> Settla: $70,000
      B -> Settla: $20,000
      Settla -> C: $40,000
    Total moved: $130,000
```

```
    MULTILATERAL NETTING (theoretical optimization)
    ──────────────────────────────────────────────

    Same obligations, but netted across all participants:

    Gross obligations:
      A owes:  $100,000    A is owed:  $30,000     A net: -$70,000
      B owes:   $80,000    B is owed:  $60,000     B net: -$20,000
      C owes:   $50,000    C is owed:  $90,000     C net: +$40,000

    Multilateral net positions:
      A pays:  $70,000  (split: $40,000 to C, $30,000 to Settla)
      B pays:  $20,000  (to Settla)
      C receives: $40,000 (from A)
      Settla net: +$50,000

    Optimal routing might reduce to:
      A -> C:      $40,000
      A -> Settla: $30,000
      B -> Settla: $20,000
    Total moved: $90,000  (vs $130,000 bilateral)
    Reduction: 31%
```

### Why Settla Uses Bilateral Netting

Settla currently uses bilateral netting (each tenant settles independently
with Settla) rather than multilateral netting. The reasons are practical:

1. **Simplicity**: Bilateral netting is straightforward to implement and audit.
   Each tenant's net position is independent of every other tenant.

2. **Isolation**: Tenants do not need to know about each other. In multilateral
   netting, Tenant A's settlement depends on Tenant C's ability to pay, which
   introduces dependencies between unrelated parties.

3. **Risk containment**: If Tenant B defaults in bilateral netting, only
   Settla is affected. In multilateral netting, Tenant B's default could
   cascade to affect Tenant C's settlement.

4. **Regulatory simplicity**: Multilateral netting often requires a formal
   Central Counterparty (CCP) structure with specific regulatory approvals.
   Bilateral netting between a platform and its customers is standard
   commercial practice.

5. **Sufficient at current scale**: With a few hundred tenants, the liquidity
   savings from multilateral netting are modest compared to the complexity it
   introduces.

> **Key Insight:** Multilateral netting is a classic example of premature
> optimization in financial infrastructure. The theoretical liquidity savings
> are real, but the operational complexity, regulatory burden, and cascading
> default risk are significant. Bilateral netting captures most of the benefit
> (the majority of netting savings come from offsetting a single tenant's
> bidirectional flows) without the systemic risk.

---

## 9. Settlement Finality

When is a transaction truly "done"? This question matters far more than it
seems, because the answer determines when you can safely update your books,
release reserved funds, and tell the customer their money has arrived.

### Finality in Different Systems

```
    SYSTEM             FINALITY POINT                  REVERSIBLE?
    ──────────────────────────────────────────────────────────────────
    Fedwire            Instant (upon processing)       No
    ACH                T+1 or T+2 settlement           Yes (returns up to 60 days)
    SWIFT              When beneficiary bank credits    Recall possible (not guaranteed)
    Credit card        When batch settles (T+1-2)      Chargebacks up to 120 days
    Bitcoin            ~6 confirmations (~60 min)       Theoretically, but astronomically unlikely
    Tron               ~20 confirmations (~60 sec)      Same as Bitcoin (probabilistic)
    Ethereum           ~15 confirmations (~3 min)       Same (probabilistic)
    Settla transfer    COMPLETED state                  No (compensating transaction required)
```

### Finality in Settla

A Settla transfer achieves finality when it reaches `COMPLETED` state. At that
point:

1. The on-ramp has converted fiat to stablecoin (confirmed by provider)
2. The blockchain has transferred the stablecoin (confirmed by block depth)
3. The off-ramp has converted stablecoin to fiat (confirmed by provider)
4. The recipient has been credited (confirmed by off-ramp provider)
5. All ledger entries balance (debits equal credits)
6. The treasury reservation has been released

Once a transfer is `COMPLETED`, it cannot be reversed through the normal
state machine. If the transfer needs to be undone (e.g., the recipient
was wrong, the amount was incorrect), a new compensating transaction must
be created. This is a deliberate design choice: finality prevents the
reconciliation nightmare of retroactively modifying settled transactions.

### Why Finality Matters for Reconciliation

Reconciliation -- the process of verifying that all records agree -- depends
critically on finality:

```
    RECONCILIATION WITH CLEAR FINALITY
    ──────────────────────────────────────────────
    Treasury ledger says: Lemfi GBP position = 496,500
    Transfer records say: Started at 500,000, settled 3,500 in transfers
    500,000 - 3,500 = 496,500  MATCH

    RECONCILIATION WITHOUT CLEAR FINALITY
    ──────────────────────────────────────────────
    Treasury ledger says: Lemfi GBP position = 496,500
    Transfer records say: 3,500 in transfers... but wait:
      - Transfer 2 might be reversed (ACH return pending)
      - Transfer 3 has a chargeback window open
      - Transfer 1 was partially reversed yesterday
    What is the TRUE position? 496,500? 498,500? 499,000?

    This ambiguity makes reconciliation impossible at scale.
```

Settla's reconciliation module (`core/reconciliation/`) runs six automated
consistency checks. All of them depend on the guarantee that `COMPLETED`
transfers are final and `FAILED` transfers have had all reservations released.
If transfers could silently change state after reaching a terminal state,
every reconciliation check would need to account for an unbounded window of
potential reversals.

> **Key Insight:** Settlement finality is not just a nice property -- it is
> the foundation that makes automated reconciliation possible. At 50M
> transfers per day, you cannot reconcile by hand. If even 0.01% of
> "completed" transfers could spontaneously reverse, that is 5,000 exceptions
> per day requiring manual investigation. Clear, unambiguous finality is what
> allows reconciliation to be a programmatic check rather than an army of
> accountants.

---

## 10. Putting It All Together: The Settlement Lifecycle in Settla

Now let us see how payment, clearing, and settlement map to the actual Settla
architecture:

```
    Phase 1: PAYMENT (the instruction)
    ──────────────────────────────────────────────
    Tenant calls POST /v1/transfers

    Gateway receives REST request
    -> Authenticates tenant (local cache, ~100ns)
    -> Forwards to gRPC backend
    -> Engine.CreateTransfer() called
    -> Transfer created in CREATED state

    This is the payment instruction. No money has moved.


    Phase 2: CLEARING (validation and preparation)
    ──────────────────────────────────────────────
    Engine validates the transfer:
    -> Quote obtained (FX rate, route, fees)
    -> Tenant balance checked
    -> Treasury reservation made (in-memory, ~100ns)
    -> Transfer moves to FUNDED state
    -> Outbox entries written for on-ramp execution

    The transfer is "cleared" -- approved for settlement.
    For PREFUNDED tenants, funds are now locked.
    For NET_SETTLEMENT tenants, the obligation is recorded.


    Phase 3: SETTLEMENT (the money moves)
    ──────────────────────────────────────────────
    Workers execute the outbox intents:

    ProviderWorker -> On-ramp: GBP to USDT          (ON_RAMPING)
    BlockchainWorker -> Send USDT on-chain           (SETTLING)
    ProviderWorker -> Off-ramp: USDT to NGN          (OFF_RAMPING)
    LedgerWorker -> Record balanced ledger entries
    TreasuryWorker -> Release reservation

    Transfer reaches COMPLETED state.
    Settlement is final. Irrevocable.

    For PREFUNDED: settlement is complete.
    For NET_SETTLEMENT: the transfer is complete, but the
    tenant's net obligation accumulates until the daily
    settlement calculation at 00:30 UTC.


    Phase 4: NET SETTLEMENT (for NET_SETTLEMENT tenants only)
    ──────────────────────────────────────────────
    Daily at 00:30 UTC, the settlement scheduler runs:

    Calculator.CalculateNetSettlement()
    -> Queries all completed transfers for the period
    -> Groups by corridor (source_currency, dest_currency)
    -> Nets per currency
    -> Generates SettlementInstructions
    -> Creates NetSettlement record (status: "pending", due: T+3)

    Tenant pays the net amount.
    Settlement record moves to "settled".
    Cycle complete.
```

---

## 11. Common Misconceptions

### Misconception 1: "Net settlement means transfers are delayed"

No. Every individual transfer executes in real time, just as in the PREFUNDED
model. The on-ramp, blockchain, and off-ramp all happen immediately. The
recipient receives their funds within seconds or minutes.

What is deferred is not the transfer itself, but the financial settlement
between the tenant and Settla. The recipient does not know or care whether
their payment was funded by a pre-funded treasury or an unsettled net
obligation. They received their NGN.

### Misconception 2: "Gross settlement is always better because there is no risk"

Gross settlement eliminates credit risk, but it introduces liquidity risk.
If a tenant's pre-funded position runs out at 2 PM on a Friday, all transfers
stop until they can wire more funds -- which might not be until Monday. In the
meantime, their customers cannot send money.

Net settlement allows the tenant to continue processing even when their
available balance is temporarily low, because the obligation is not due until
T+3. This can be the difference between a good customer experience and
thousands of failed transactions.

### Misconception 3: "Netting is just an optimization"

Netting is not an optimization -- it is a structural requirement for operating
a payment network at scale. Without netting, the global financial system would
require approximately 10x more liquidity than currently exists. It is not about
saving a few database writes; it is about making the entire system viable.

---

## Exercises

### Exercise 1: Calculate Net Positions

Given the following 20 transfers between Tenant A and Settla over a 24-hour
period, calculate the net position for each currency.

```
    TENANT A -> SETTLA:
    ─────────────────────────────────────
    #1   GBP 12,000  -> NGN
    #2   GBP  8,500  -> NGN
    #3   GBP  3,200  -> GHS
    #4   EUR  5,000  -> NGN
    #5   GBP 15,000  -> NGN
    #6   EUR  7,500  -> NGN
    #7   GBP  6,800  -> NGN
    #8   GBP  2,100  -> GHS
    #9   EUR  4,200  -> NGN
    #10  GBP  9,000  -> NGN

    SETTLA -> TENANT A (reverse flows):
    ─────────────────────────────────────
    #11  NGN -> GBP  4,000
    #12  NGN -> GBP  2,500
    #13  GHS -> GBP  1,800
    #14  NGN -> EUR  3,000
    #15  NGN -> GBP  6,000
    #16  NGN -> GBP  1,200
    #17  GHS -> GBP  2,400
    #18  NGN -> EUR  2,000
    #19  NGN -> GBP  3,500
    #20  NGN -> GBP    800
```

Tasks:
1. Calculate the gross volume in each direction (GBP and EUR separately).
2. Calculate the net position for GBP.
3. Calculate the net position for EUR.
4. Write out the settlement instructions (who pays whom, how much, in what
   currency).
5. What percentage reduction does netting achieve compared to gross settlement?

### Exercise 2: Default Impact Analysis

A NET_SETTLEMENT tenant with a $500,000 credit limit processes the following
over 5 business days before defaulting:

```
    Day 1: Net obligation accumulates to $420,000
    Day 2: Net obligation accumulates to $310,000 (lighter day)
    Day 3: Net obligation accumulates to $480,000
    Day 4: Net obligation accumulates to $495,000 (near limit)
    Day 5: Net obligation accumulates to $390,000
```

Settlement is due T+3 for each day's net position.

The tenant defaults on Day 8 (when Day 1 and Day 2 settlements are due).

Tasks:
1. What is Settla's total exposure at the moment of default? (Hint: which
   days' settlements are still outstanding?)
2. If Settla's average fee is 75 bps, how much fee revenue was earned from
   this tenant over the 5 days?
3. How many days of this tenant's fee revenue would be needed to cover the
   loss from default?
4. What credit limit would have prevented the total exposure from exceeding
   $1,000,000?
5. Propose three specific controls that could have reduced Settla's exposure.

### Exercise 3: Liquidity Comparison

A tenant processes $10M per day in transfers. Compare the capital requirements
under each model:

**PREFUNDED model:**
- Transfers are spread evenly throughout the day
- Average transfer takes 60 seconds to complete
- Peak hour has 3x average throughput
- The tenant must maintain enough balance to cover peak in-flight transfers
  plus a full day's buffer

**NET_SETTLEMENT model:**
- Same $10M daily volume
- 30% reverse flows (bidirectional)
- Settlement due T+3
- Credit limit must cover peak 1-day net obligation plus safety margin

Tasks:
1. Calculate the minimum pre-funded treasury balance needed for the PREFUNDED
   model. Show your reasoning for peak-hour calculations.
2. Calculate the minimum credit limit needed for the NET_SETTLEMENT model.
3. What is the capital savings for the tenant under NET_SETTLEMENT?
4. If the tenant's cost of capital is 8% annually, what is the dollar value
   of the capital freed by switching to NET_SETTLEMENT?
5. At what daily volume does the capital savings exceed $1M per year?

---

## What is Next

You now understand the three phases of financial transactions (payment,
clearing, settlement), the mechanics and trade-offs of gross vs net
settlement, and the counterparty risk that net settlement introduces. These
are not abstract concepts -- they directly determine the architecture of
systems like Settla. The PREFUNDED model's need for real-time treasury
reservations drives the in-memory atomic treasury design. The NET_SETTLEMENT
model's daily netting calculation drives the settlement scheduler and
calculator. Counterparty risk drives the credit limit enforcement, KYB
requirements, and escalation workflows.

In the next chapter, we will explore foreign exchange: how FX rates work,
what spread and slippage mean, and why Settla quotes rates with a time-limited
validity window. These concepts complete the financial foundation you need
before we turn to engineering the system itself in Module 1.

---
