# Chapter 0.5: Regulation -- KYC, AML, and Why Compliance Shapes Architecture

**Estimated reading time:** 30 minutes

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why financial services are heavily regulated and the consequences of non-compliance
2. Describe KYC (Know Your Customer) and KYB (Know Your Business) requirements
3. Explain AML (Anti-Money Laundering) and the role of transaction monitoring
4. Understand the Travel Rule (FATF Recommendation 16) and its impact on crypto transfers
5. Identify key regulators and frameworks: MiCA, FCA, FinCEN, AMLD6
6. Connect each regulatory requirement to a specific architectural decision in Settla

---

## 1. Why Regulation Exists

If you come from a SaaS or e-commerce background, you are accustomed to a world where the worst consequence of a software bug is a bad user experience. In financial services, the worst consequence of a software bug is prison.

This is not hyperbole. Financial regulation exists because the movement of money is the single most abused capability in the global economy. The United Nations Office on Drugs and Crime estimates that $800 billion to $2 trillion is laundered annually -- between 2% and 5% of global GDP. When you include the broader costs of financial crime (fraud, sanctions evasion, terrorist financing, tax evasion), the figure rises to $2-5 trillion per year.

Regulators exist to prevent these harms. They require financial institutions to:

1. **Identify customers** before allowing transactions (Know Your Customer)
2. **Monitor transactions** for suspicious patterns (Anti-Money Laundering)
3. **Report suspicious activity** to regulatory authorities (Suspicious Activity Reports)
4. **Maintain audit trails** of all financial activity (Record Keeping)
5. **Freeze and report** transactions involving sanctioned entities (Sanctions Compliance)

### The Consequences of Non-Compliance

Non-compliance is not a product decision you can defer. It is an existential risk:

```
NON-COMPLIANCE CONSEQUENCES (REAL EXAMPLES)

+--------------------+----------------------------+---------------------------+
| Company            | What Happened              | Consequence               |
+--------------------+----------------------------+---------------------------+
| Wirecard (2020)    | EUR 1.9B fabricated on     | CEO arrested, company     |
|                    | balance sheet, no real      | bankrupt, auditors        |
|                    | customer verification       | criminally charged        |
+--------------------+----------------------------+---------------------------+
| Binance (2023)     | Processed transactions     | $4.3B fine, CEO resigned  |
|                    | without adequate AML        | and pleaded guilty,       |
|                    | controls                    | 3-year monitorship        |
+--------------------+----------------------------+---------------------------+
| Danske Bank (2018) | EUR 200B in suspicious     | $2B in fines, multiple    |
|                    | transactions through        | executives charged,       |
|                    | Estonian branch              | permanent reputational    |
|                    |                             | damage                    |
+--------------------+----------------------------+---------------------------+
| Westpac (2020)     | 23 million AML reporting   | AUD 1.3B fine (largest    |
|                    | failures, including child   | in Australian history),   |
|                    | exploitation payments       | CEO and board resigned    |
+--------------------+----------------------------+---------------------------+
| BitMEX (2022)      | No KYC program, allowed    | $100M fine, founders      |
|                    | US customers without        | pleaded guilty to BSA     |
|                    | verification                | violations                |
+--------------------+----------------------------+---------------------------+
```

Notice the pattern: the consequences are not just fines. They include criminal prosecution of individuals, loss of operating licenses, and complete business destruction. Wirecard does not exist anymore. Binance's founder went to prison.

> **Key Insight:** Compliance is not optional overhead -- it is a survival requirement. A fintech that processes a single transaction without adequate KYC, AML, and sanctions screening is accumulating legal liability with every transfer. The question is not "if" regulators will notice, but "when."

### Why This Matters for Engineers

As a backend engineer building Settla, you are not personally responsible for regulatory strategy. But you are responsible for building systems that make compliance possible. Every architectural decision you make either enables or prevents the compliance team from doing their job.

Consider a simple example: if you build a ledger that overwrites balances instead of appending entries, the compliance team cannot answer the regulator's question: "Show me every transaction that led to this balance." If you build a transfer table without a `tenant_id` column, the compliance team cannot scope an investigation to a single entity. If you store PII in plaintext, you have created a GDPR violation that no amount of policy can fix.

The architecture must be designed for compliance from the start.

---

## 2. KYC and KYB

### KYC: Know Your Customer

KYC is the process of verifying the identity of individual customers before allowing them to transact. In Settla's model, KYC is primarily the responsibility of our tenants (the fintechs). Lemfi performs KYC on its end users. Fincra performs KYC on its merchants. Settla does not interact with end users directly.

However, Settla must verify that its tenants have adequate KYC programs. This is called "downstream KYC" or "reliance on third-party KYC." Regulators hold Settla responsible for the actions of its tenants' customers, so Settla must verify that each tenant's KYC program meets regulatory standards.

Standard KYC requirements for individuals include:

```
KYC REQUIREMENTS (INDIVIDUAL)

+----------------------------+----------------------------------------------+
| Requirement                | What It Means                                |
+----------------------------+----------------------------------------------+
| Identity Verification      | Government-issued photo ID (passport,        |
|                            | driver's license, national ID card)          |
+----------------------------+----------------------------------------------+
| Proof of Address           | Utility bill, bank statement, or government  |
|                            | correspondence dated within 3 months         |
+----------------------------+----------------------------------------------+
| Source of Funds             | Documentation showing where the money        |
|                            | comes from (salary slips, business income,   |
|                            | investment returns)                          |
+----------------------------+----------------------------------------------+
| PEP Screening              | Check against Politically Exposed Person     |
|                            | databases -- heads of state, senior          |
|                            | officials, their family members, close       |
|                            | associates                                   |
+----------------------------+----------------------------------------------+
| Sanctions Screening        | Check against OFAC (US), EU Consolidated     |
|                            | List, UN Security Council, HMRC (UK)         |
|                            | sanctions lists                              |
+----------------------------+----------------------------------------------+
| Adverse Media Screening    | Search news sources for negative coverage    |
|                            | related to financial crime                   |
+----------------------------+----------------------------------------------+
```

PEP screening deserves special attention. A Politically Exposed Person is someone who holds or has recently held a prominent public function. PEPs are not automatically criminals, but they are at higher risk of being involved in corruption. Regulations require Enhanced Due Diligence (EDD) for PEPs -- more documentation, more frequent reviews, senior management approval for the relationship.

### KYB: Know Your Business

KYB is what Settla performs on its tenants. Since Settla's customers are businesses (fintechs), not individuals, the verification process is more complex:

```
KYB REQUIREMENTS (SETTLA'S TENANTS)

+------------------------------------+------------------------------------------+
| Requirement                        | What It Means                            |
+------------------------------------+------------------------------------------+
| Company Registration               | Verify the company is legally            |
|                                    | registered (Companies House in UK,       |
|                                    | SEC in US, CAC in Nigeria)               |
+------------------------------------+------------------------------------------+
| Ultimate Beneficial Ownership (UBO)| Identify every individual who owns       |
|                                    | >25% of the company, directly or         |
|                                    | indirectly. Verify their identity.       |
+------------------------------------+------------------------------------------+
| Business Nature Assessment         | What does the company do? Is it a        |
|                                    | licensed financial institution?           |
|                                    | What corridors will it use?              |
+------------------------------------+------------------------------------------+
| Expected Transaction Profile       | Estimated monthly volume, average        |
|                                    | transaction size, peak patterns.         |
|                                    | Used for anomaly detection later.        |
+------------------------------------+------------------------------------------+
| Financial Health                   | Bank statements, audited accounts,       |
|                                    | proof of capitalization. Can they        |
|                                    | meet their settlement obligations?       |
+------------------------------------+------------------------------------------+
| Regulatory Status                  | Does the tenant hold required            |
|                                    | licenses in their operating              |
|                                    | jurisdictions?                           |
+------------------------------------+------------------------------------------+
| Downstream KYC Program Review      | Does the tenant's own KYC program        |
|                                    | meet regulatory standards?               |
+------------------------------------+------------------------------------------+
```

UBO identification is particularly important and often difficult. Corporate structures can be layered to obscure true ownership:

```
    Company A (Tenant applying to Settla)
        |
        +--- 60% owned by Company B (UK)
        |        |
        |        +--- 100% owned by Person X (UBO: owns 60% of A)
        |
        +--- 40% owned by Trust C (Cayman Islands)
                 |
                 +--- Beneficiaries: Person Y (30%), Person Z (10%)
                      |
                      Person Y is UBO (owns 30% of A through Trust C)
                      Person Z is NOT UBO (owns only 10%, below 25% threshold)
```

Regulators require Settla to "look through" these structures to find the natural persons who ultimately control the business. This is not a one-time check -- UBO information must be refreshed periodically and whenever there is a material change in ownership.

### KYB in Settla's Architecture

Settla models KYB as a state machine on the tenant entity:

```go
// From domain/tenant.go
type KYBStatus string

const (
    KYBStatusPending  KYBStatus = "PENDING"
    KYBStatusInReview KYBStatus = "IN_REVIEW"
    KYBStatusVerified KYBStatus = "VERIFIED"
    KYBStatusRejected KYBStatus = "REJECTED"
)
```

The KYB lifecycle follows a strict flow:

```
    Tenant Onboarding
         |
         v
    KYBStatus: PENDING
         |
         | Tenant submits documents via Portal
         | (registration certificate, UBO declarations,
         |  bank statements, license copies)
         v
    KYBStatus: IN_REVIEW
         |
         | Compliance team reviews documents
         | Automated checks run in parallel:
         |   - Sanctions screening (all UBOs)
         |   - PEP screening (all UBOs)
         |   - Adverse media search
         |   - Company registry verification
         |
         +--------> KYBStatus: VERIFIED
         |              |
         |              | API keys issued
         |              | Tenant Status set to ACTIVE
         |              | Webhook: kyb.verified
         |              | Tenant can now make API calls
         |
         +--------> KYBStatus: REJECTED
                        |
                        | Reason provided
                        | Webhook: kyb.rejected
                        | Tenant cannot make API calls
                        | May reapply with corrected information
```

The critical architectural rule is the **dual-gate activation**:

```go
// From domain/tenant.go
// IsActive returns true if the tenant is ACTIVE and KYB VERIFIED.
func (t *Tenant) IsActive() bool {
    return t.Status == TenantStatusActive && t.KYBStatus == KYBStatusVerified
}
```

Both conditions must be true. A tenant can be `ACTIVE` but not yet `VERIFIED` (they have been onboarded but compliance review is pending). A tenant can be `VERIFIED` but `SUSPENDED` (they passed KYB but violated terms of service). Only when both gates are satisfied can the tenant process transfers.

This dual-gate pattern has a direct impact on every API request:

```
    API Request: POST /v1/transfers
         |
         v
    Gateway: Resolve API key -> tenant_id
         |
         v
    Gateway: Check tenant.IsActive()
         |
         +--- false --> 403 Forbidden: "tenant not active"
         |                (logged for compliance audit)
         |
         +--- true  --> Forward to gRPC backend
```

> **Key Insight:** KYB is not a checkbox you tick during onboarding and forget. It is an ongoing obligation. Regulators expect periodic reviews (typically annually), re-screening against updated sanctions lists, and immediate review when adverse information surfaces. The `KYBStatus` field is not just a flag -- it is a state machine that can transition back to `IN_REVIEW` at any time.

---

## 3. AML: Anti-Money Laundering

Anti-Money Laundering (AML) is the set of regulations, procedures, and technologies designed to detect and prevent the laundering of criminal proceeds through the financial system. AML rests on three pillars:

### Pillar 1: Customer Due Diligence (CDD)

This is the KYC/KYB process we described above. It happens at onboarding and is refreshed periodically. CDD establishes the baseline: who is the customer, what do they do, and what is their expected transaction profile.

Enhanced Due Diligence (EDD) is required for high-risk relationships: PEPs, tenants operating in high-risk jurisdictions, tenants with complex ownership structures, or tenants whose transaction patterns significantly deviate from their stated profile.

### Pillar 2: Transaction Monitoring

This is the ongoing surveillance of transaction patterns to detect suspicious activity. Transaction monitoring is where architecture becomes critical, because the system must be designed to support it from the ground up.

Money launderers use a variety of techniques that Settla's monitoring must detect:

```
COMMON MONEY LAUNDERING PATTERNS

Pattern: STRUCTURING (also called "smurfing")
-----------------------------------------------------------------
What it is:   Breaking a large transfer into many smaller ones
              to avoid reporting thresholds.

Thresholds:   $10,000 (US/FinCEN), EUR 15,000 (EU/AMLD),
              GBP 10,000 (UK/FCA)

Example:      Instead of one $50,000 transfer, a tenant submits
              five $9,800 transfers over three days.

Detection:    Aggregate transfers by tenant + corridor + time window.
              Flag if sum exceeds threshold but individual
              transfers are all just below it.


Pattern: VELOCITY ANOMALY
-----------------------------------------------------------------
What it is:   Sudden, unexplained spike in transaction volume
              for a tenant.

Example:      Tenant X normally processes 100 transfers/day.
              On Tuesday, they submit 2,000 transfers.

Detection:    Compare current volume against the tenant's
              expected transaction profile (established during KYB).
              Flag deviations beyond a configurable threshold
              (e.g., 3x normal volume).


Pattern: ROUND-TRIP TRANSFER
-----------------------------------------------------------------
What it is:   Money sent to a destination and immediately
              returned, often through a different corridor.

Example:      Tenant sends GBP 100,000 to NGN. The same
              day, an equivalent amount flows back from
              NGN to GBP through a different tenant.

Detection:    Cross-tenant correlation (requires careful design
              to avoid violating tenant isolation for normal
              queries while enabling compliance investigations).


Pattern: RAPID MOVEMENT
-----------------------------------------------------------------
What it is:   Funds deposited and immediately transferred
              out, with no economic purpose.

Example:      Tenant deposits $500,000 via bank deposit at 9:00 AM.
              By 9:15 AM, $495,000 has been sent across 50
              transfers to different corridors.

Detection:    Time-between-deposit-and-withdrawal analysis.
              Flag if > 90% of deposited funds are transferred
              within a configurable window (e.g., 1 hour).


Pattern: HIGH-RISK CORRIDOR CONCENTRATION
-----------------------------------------------------------------
What it is:   Disproportionate transfer volume to jurisdictions
              with weak AML controls.

Example:      Tenant's stated business is UK-to-EU remittances,
              but 80% of volume goes to a jurisdiction on
              the FATF grey list.

Detection:    Compare actual corridor distribution against
              the expected profile from KYB.
```

Here is how transaction monitoring fits into Settla's architecture:

```
TRANSACTION MONITORING PIPELINE

  Every transfer (state transition event)
       |
       v
  +-------------------+     +-------------------+     +-------------------+
  | Rule Engine        |---->| Risk Scoring      |---->| Action            |
  |                    |     |                    |     |                    |
  | - Amount threshold |     | Low (0-30):        |     | Low:   auto-approve|
  |   (per jurisdiction|     |   No rules         |     |        (proceed)   |
  |    threshold)      |     |   triggered        |     |                    |
  |                    |     |                    |     |                    |
  | - Velocity check   |     | Medium (31-70):    |     | Medium: flag for   |
  |   (vs. KYB profile)|     |   One or more      |     |         manual     |
  |                    |     |   rules triggered,  |     |         review     |
  | - Pattern match    |     |   low confidence   |     |                    |
  |   (structuring,    |     |                    |     |                    |
  |    round-trip)     |     | High (71-100):     |     | High:  block       |
  |                    |     |   Multiple rules    |     |        transfer +  |
  | - Sanctions check  |     |   triggered or      |     |        file SAR    |
  |   (address/entity  |     |   high-confidence   |     |        + alert     |
  |    screening)      |     |   single rule       |     |        compliance  |
  +-------------------+     +-------------------+     +-------------------+
```

### Pillar 3: Suspicious Activity Reporting (SAR)

When transaction monitoring identifies suspicious activity, the compliance team must file a Suspicious Activity Report with the relevant regulator. In the US, SARs are filed with FinCEN. In the UK, with the National Crime Agency (NCA). In the EU, with the national Financial Intelligence Unit (FIU).

Key facts about SARs:

- Filing must happen within a deadline (typically 30 days from detection, 24 hours for urgent cases involving terrorism financing)
- The entity being reported must NOT be notified (tipping off is a criminal offense)
- SARs must include: the suspicious activity, the persons involved, the financial instruments used, why it is suspicious
- The filing institution may be required to continue the business relationship to avoid tipping off the subject

This last point has a direct architectural consequence: the system must be able to continue processing transfers for a flagged tenant while the SAR is being prepared. You cannot simply suspend the tenant, because that would tip them off. This means the compliance status of a tenant is not the same as the operational status.

### Architectural Impact of AML

Transaction monitoring requirements drive several of Settla's core architectural decisions:

**1. Complete, immutable event history.** Every transfer state transition is recorded as an event. Every ledger entry is append-only. Every treasury mutation is logged as a position event. This is not engineering perfectionism -- it is a legal requirement. A regulator investigating a suspicious pattern needs to reconstruct exactly what happened, in what order, at what time.

```
    Why Settla stores transfer events (not just current state):

    Transfer T-12345:
      2024-01-15 10:00:00 UTC  transfer.initiated    amount=9800 USD
      2024-01-15 10:00:01 UTC  transfer.funded       treasury_reserved=true
      2024-01-15 10:00:03 UTC  transfer.on_ramping   provider=provider_a
      2024-01-15 10:00:45 UTC  transfer.settling     tx_hash=0xabc...
      2024-01-15 10:01:02 UTC  transfer.off_ramping  provider=provider_b
      2024-01-15 10:01:30 UTC  transfer.completed    total_time=90s

    A regulator can see: which providers were used, how long each
    step took, whether the amount was just below a reporting threshold,
    and the complete chain of custody for the funds.
```

**2. Tenant isolation with compliance override.** Normal queries always filter by `tenant_id` -- this is Critical Invariant #7. But compliance investigations may require cross-tenant analysis (e.g., detecting round-trip transfers). The architecture must support both: strict isolation for operational queries, authorized cross-tenant access for compliance.

**3. Append-only ledger.** The ledger in Settla is append-only by design. To reverse a transaction, you do not delete or modify the original entries -- you post a new reversal entry. This means the complete history is always available. TigerBeetle enforces this at the storage engine level; there is no "UPDATE" or "DELETE" operation on ledger entries.

**4. Position event sourcing.** Treasury position changes are logged as events (`position_events` table). This provides a complete audit trail of every reserve, release, top-up, and withdrawal that affected a treasury position. Without this, a regulator asking "how did this position go from $1M to $50K in one day?" would get no answer.

> **Key Insight:** The "extra" tables and "redundant" events in Settla are not over-engineering. Each one exists because a specific regulatory requirement demands it. The transfer events table, the append-only ledger, the position events table -- remove any of these and you have a system that cannot satisfy a regulatory investigation.

---

## 4. The Travel Rule (FATF Recommendation 16)

The Financial Action Task Force (FATF) is the global standard-setter for AML and counter-terrorist financing. FATF Recommendation 16, commonly called the "Travel Rule," requires that certain information about the originator and beneficiary "travels" with the transaction.

### What the Travel Rule Requires

For transfers above the applicable threshold (varies by jurisdiction, typically $1,000 USD for crypto transfers), the originating institution must collect and transmit:

```
TRAVEL RULE DATA REQUIREMENTS

Originator Information (collected by sending institution):
+---------------------------+
| - Full name               |
| - Account number / wallet |
| - Address (or national ID |
|   number, or date/place   |
|   of birth)               |
+---------------------------+
          |
          | Must travel with the transaction
          v
Beneficiary Information (collected by receiving institution):
+---------------------------+
| - Full name               |
| - Account number / wallet |
+---------------------------+
```

For traditional banking, this information flows through SWIFT messages. For crypto, there is no built-in mechanism to attach identity information to an on-chain transfer. This has led to the development of Travel Rule protocols like TRISA (Travel Rule Information Sharing Architecture) and the OpenVASP protocol.

### Impact on Settla's Architecture

The Travel Rule has direct consequences for how Settla stores transfer data:

```go
// Simplified transfer fields related to Travel Rule
type Transfer struct {
    // ... other fields ...

    // Travel Rule: originator information
    // These fields are encrypted with AES-256-GCM
    SenderName       EncryptedField  // "John Smith"
    SenderAccount    EncryptedField  // Account or wallet address
    SenderAddress    EncryptedField  // Physical address

    // Travel Rule: beneficiary information
    RecipientName    EncryptedField  // "Adewale Ogundimu"
    RecipientAccount EncryptedField  // Bank account or wallet

    // ... other fields ...
}
```

Three architectural decisions follow directly from the Travel Rule:

**1. PII fields are encrypted at rest.** Sender and recipient information is Personally Identifiable Information (PII). It must be protected both because the Travel Rule requires "appropriate data protection" and because GDPR (in EU/UK) imposes strict requirements on PII storage. Settla uses AES-256-GCM encryption for these fields.

**2. The crypto-shred pattern for GDPR compliance.** Here is the fundamental tension: the Travel Rule and AML regulations require you to retain transaction records for 5-7 years. GDPR gives individuals the "right to erasure" -- the right to have their personal data deleted. These two requirements directly contradict each other.

The solution is the crypto-shred pattern:

```
THE CRYPTO-SHRED PATTERN

    Storage:
    +------------------------------------------+
    | Transfer Record                          |
    | - transfer_id: T-12345                   |
    | - amount: 1000.00 GBP                    |
    | - sender_name: AES-256-GCM(key_42, ...)  |  <-- encrypted
    | - recipient_name: AES-256-GCM(key_42, ...)|  <-- encrypted
    | - created_at: 2024-01-15T10:00:00Z       |
    | - status: COMPLETED                      |
    +------------------------------------------+
                     |
                     | Encrypted with per-tenant key
                     v
    +------------------------------------------+
    | Key Store                                |
    | - key_id: key_42                         |
    | - tenant_id: lemfi-uuid                  |
    | - key_material: [256-bit AES key]        |
    +------------------------------------------+

    GDPR Erasure Request:
    +------------------------------------------+
    | Delete key_42 from Key Store             |
    |                                          |
    | Result:                                  |
    | - Transfer record still exists (amount,  |
    |   timestamps, IDs -- not PII)            |
    | - PII fields are now undecryptable       |
    |   (effectively erased)                   |
    | - Regulatory retention satisfied         |
    | - GDPR erasure satisfied                 |
    +------------------------------------------+
```

By encrypting PII with a per-tenant key and storing the key separately, you can satisfy both requirements. Delete the key, and the PII is cryptographically erased. The transaction record (amounts, timestamps, status, IDs) remains intact for regulatory retention.

**3. Travel Rule data must be transmittable.** When Settla sends a crypto transfer on behalf of a tenant, it must be prepared to transmit the Travel Rule data to the counterparty institution. This means the data must be stored in a structured, extractable format -- not buried in a free-text "notes" field.

> **Key Insight:** The Travel Rule is the single regulation that has the most direct impact on the data model of a crypto settlement system. It dictates which fields exist on the transfer entity, how they are encrypted, and how they are retained. If you design your transfer model without considering the Travel Rule, you will need to add encrypted PII fields, a key management system, and a crypto-shred capability later -- a painful retrofit.

---

## 5. Key Regulators and Frameworks

Financial regulation is jurisdiction-specific. Settla operates across multiple corridors (GBP-NGN, EUR-NGN, USD-NGN, etc.), and each corridor touches at least two regulatory regimes. Here is a map of the key regulators and frameworks:

### MiCA (Markets in Crypto-Assets Regulation) -- EU

MiCA is the EU's comprehensive crypto regulation, fully effective from December 2024. It is the most significant crypto regulatory framework globally because of the EU's market size and regulatory influence.

Key requirements:

- **Stablecoin issuers** must be authorized, maintain 1:1 reserves (held in custody at EU credit institutions), and publish a whitepaper
- **CASPs** (Crypto-Asset Service Providers) must be registered and authorized
- **Transaction monitoring** and Travel Rule compliance are mandatory
- **Consumer protection**: clear risk disclosures, complaint handling procedures
- **Reserve requirements**: stablecoin issuers with >EUR 5M daily volume must maintain additional reserves

Impact on Settla: Settla must use MiCA-compliant stablecoins (e.g., USDC issued by Circle's EU entity) for EU corridors. Tenants operating in the EU must either hold CASP authorization themselves or rely on Settla's authorization. The choice of stablecoin per corridor is not just a technical decision -- it is a regulatory one.

### FCA (Financial Conduct Authority) -- UK

The FCA regulates crypto asset businesses in the UK under the Money Laundering Regulations (MLRs). Key requirements:

- **Registration**: All crypto asset businesses operating in the UK must be FCA-registered
- **AML compliance**: Full AML program including CDD, transaction monitoring, SAR filing
- **Financial promotions**: Strict rules on how crypto services can be marketed
- **Consumer duty**: Obligation to deliver good outcomes for customers

Impact on Settla: For GBP corridors, either Settla or its tenants need FCA registration. The FCA has been notoriously strict -- rejecting the majority of crypto registration applications. This regulatory bottleneck directly affects which tenants can use GBP corridors.

### FinCEN (Financial Crimes Enforcement Network) -- US

FinCEN is the US Treasury bureau responsible for AML enforcement. Key requirements:

- **MSB registration**: Money Services Businesses must register with FinCEN
- **State licensing**: Money transmitters need licenses in each state they operate in (50 states, 50 different requirements)
- **BSA compliance**: Bank Secrecy Act requires CTRs (Currency Transaction Reports) for transactions over $10,000 and SARs for suspicious activity
- **OFAC screening**: All transactions must be screened against the OFAC Specially Designated Nationals (SDN) list

Impact on Settla: US corridor operations require MSB registration at the federal level and money transmitter licenses at the state level. The state-by-state licensing regime is one of the most expensive and time-consuming regulatory processes in the world. Many crypto companies avoid US operations entirely because of this burden.

### AMLD6 (6th Anti-Money Laundering Directive) -- EU

AMLD6 is the latest iteration of the EU's anti-money laundering framework. Its most significant feature is the expansion of criminal liability:

- **Criminal liability for legal persons**: Companies (not just individuals) can be criminally liable for AML failures
- **Extended predicate offenses**: Broader definition of what constitutes money laundering
- **Harmonized penalties**: Minimum 4 years imprisonment for money laundering offenses
- **Personal liability**: Compliance officers and senior management can be personally criminally liable

Impact on Settla: AMLD6 means that the individuals responsible for Settla's AML program face personal criminal liability if the program is inadequate. This is why compliance is a board-level concern, not a back-office function.

### FATF (Financial Action Task Force) -- Global

FATF is not a regulator but a standard-setter. Its 40 Recommendations form the basis for AML regulation worldwide. FATF maintains two lists that directly affect Settla:

- **Grey List**: Jurisdictions with strategic AML deficiencies but committed to resolving them. Enhanced Due Diligence required for transactions involving these jurisdictions.
- **Black List**: Jurisdictions with severe AML deficiencies and no commitment to reform. Transactions may be prohibited entirely.

### CBN (Central Bank of Nigeria) -- Nigeria

The CBN regulates payment service providers and foreign exchange transactions in Nigeria. Key considerations:

- **Licensed PSP requirement**: Entities processing payments in Nigeria must be licensed
- **FX regulations**: Strict controls on foreign exchange transactions, including approved rates and authorized dealers
- **Crypto policy**: CBN's stance on crypto has evolved from prohibition (2021) to regulated acceptance

Impact on Settla: NGN corridors are subject to CBN FX regulations. The approved exchange rate, transaction reporting requirements, and PSP licensing all affect how Settla's off-ramp providers operate in Nigeria.

### Regulatory Summary Table

```
+---------------------+--------+----------------------------------+---------------------------+
| Regulator/Framework | Region | Key Requirements                 | Impact on Settla          |
+---------------------+--------+----------------------------------+---------------------------+
| MiCA                | EU     | CASP authorization, stablecoin   | Must use MiCA-compliant   |
|                     |        | reserves, consumer protection    | stablecoins in EU         |
|                     |        |                                  | corridors                 |
+---------------------+--------+----------------------------------+---------------------------+
| FCA                 | UK     | Crypto registration, AML, fin.   | FCA registration needed   |
|                     |        | promotions rules, consumer duty  | for GBP corridors         |
+---------------------+--------+----------------------------------+---------------------------+
| FinCEN              | US     | MSB registration, state-by-state | MSB + state licenses for  |
|                     |        | money transmitter licenses, BSA  | US corridors              |
+---------------------+--------+----------------------------------+---------------------------+
| AMLD6               | EU     | Criminal liability for AML       | Personal criminal         |
|                     |        | failures, extended predicate     | liability for compliance  |
|                     |        | offenses, harmonized penalties   | officers                  |
+---------------------+--------+----------------------------------+---------------------------+
| FATF                | Global | Travel Rule, risk-based          | Travel Rule for crypto    |
|                     |        | approach, grey/black lists       | transfers, EDD for grey   |
|                     |        |                                  | list jurisdictions        |
+---------------------+--------+----------------------------------+---------------------------+
| CBN                 | Nigeria| PSP licensing, FX regulations    | NGN corridors subject to  |
|                     |        |                                  | CBN FX requirements       |
+---------------------+--------+----------------------------------+---------------------------+
```

> **Key Insight:** Multi-jurisdictional compliance is multiplicative, not additive. Operating a GBP-to-NGN corridor means complying with FCA (UK), CBN (Nigeria), FATF (global), and potentially MiCA (if the stablecoin bridge touches the EU). Each additional corridor multiplies the regulatory surface area. This is why Settla's architecture must be flexible enough to apply different rules per corridor.

---

## 6. Data Retention Requirements

Regulators require financial institutions to retain transaction records for years after the transaction completes. The specific period varies by jurisdiction:

```
DATA RETENTION REQUIREMENTS BY JURISDICTION

+-------------------+-------------------+-----------------------------------+
| Jurisdiction      | Retention Period  | What Must Be Retained             |
+-------------------+-------------------+-----------------------------------+
| US (BSA/FinCEN)   | 5 years           | Transaction records, CDD records, |
|                   |                   | SARs, CTRs                        |
+-------------------+-------------------+-----------------------------------+
| EU (AMLD6)        | 5 years (min)     | Transaction records, CDD records, |
|                   | 10 years (some)   | business correspondence           |
+-------------------+-------------------+-----------------------------------+
| UK (MLR 2017)     | 5 years           | CDD records, transaction records, |
|                   |                   | supporting evidence               |
+-------------------+-------------------+-----------------------------------+
| Nigeria (CBN)     | 5 years           | Transaction records, KYC records  |
+-------------------+-------------------+-----------------------------------+
| FATF Rec. 11      | 5 years (min)     | All transaction records,          |
|                   |                   | identification data               |
+-------------------+-------------------+-----------------------------------+
```

### How Settla Handles Retention

Settla's partitioned storage architecture directly serves these retention requirements:

```
SETTLA DATA RETENTION STRATEGY

+-----------------------------+------------------+----------------------------+
| Data Category               | Retention        | Reason                     |
+-----------------------------+------------------+----------------------------+
| Ledger entries              | Never dropped    | Regulatory: complete       |
| (entry_lines)               |                  | financial history required.|
|                             |                  | Weekly partitions, no      |
|                             |                  | auto-drop policy.          |
+-----------------------------+------------------+----------------------------+
| Transfers + events          | Never auto-      | Regulatory: transaction    |
|                             | dropped          | records must be retained   |
|                             |                  | 5-7 years minimum.         |
|                             |                  | Monthly partitions.        |
+-----------------------------+------------------+----------------------------+
| Outbox entries              | 48 hours         | Operational only. Not      |
|                             |                  | regulatory data. Dropped   |
|                             |                  | by partition manager.      |
+-----------------------------+------------------+----------------------------+
| Position events             | 90 days          | Operational audit trail.   |
| (treasury mutations)        |                  | Underlying ledger entries  |
|                             |                  | are retained permanently.  |
+-----------------------------+------------------+----------------------------+
| Tenant records + KYB data   | Life of tenant   | Retained as long as tenant |
|                             | + 5 years        | exists, plus 5 years       |
|                             |                  | after offboarding.         |
+-----------------------------+------------------+----------------------------+
```

### The GDPR Conflict

GDPR (and its UK equivalent, UK GDPR) gives individuals the "right to erasure" -- the right to request deletion of their personal data. This directly conflicts with the 5-7 year retention requirements for financial records.

The resolution depends on which data is classified as PII:

```
GDPR vs. RETENTION: WHAT IS PII?

+-------------------------------+---------+----------------------------------+
| Data                          | PII?    | Resolution                       |
+-------------------------------+---------+----------------------------------+
| Sender name, address          | Yes     | Crypto-shred: encrypt with       |
|                               |         | per-tenant key, delete key       |
|                               |         | on erasure request               |
+-------------------------------+---------+----------------------------------+
| Recipient name, account       | Yes     | Same crypto-shred pattern        |
+-------------------------------+---------+----------------------------------+
| Transfer amount               | No      | Retain for regulatory period     |
+-------------------------------+---------+----------------------------------+
| Transfer timestamps           | No      | Retain for regulatory period     |
+-------------------------------+---------+----------------------------------+
| Transfer ID, tenant ID        | No      | Retain for regulatory period     |
+-------------------------------+---------+----------------------------------+
| Ledger entries (debits,       | No      | Retain permanently               |
| credits, account codes)       |         |                                  |
+-------------------------------+---------+----------------------------------+
| IP addresses (in audit logs)  | Yes     | Crypto-shred or anonymize        |
|                               |         | after retention period           |
+-------------------------------+---------+----------------------------------+
```

The crypto-shred pattern (described in Section 4) resolves this conflict cleanly. After a GDPR erasure request, Settla deletes the encryption key. The transfer record survives (amounts, timestamps, status, IDs -- all non-PII), satisfying regulatory retention. The PII fields become undecryptable gibberish, satisfying GDPR erasure. Both regulators are happy.

> **Key Insight:** Data retention is not a storage problem -- it is an architecture problem. If you design your schema with PII mixed into the same columns as regulatory data, you cannot satisfy both GDPR erasure and AML retention. The crypto-shred pattern requires that PII be encrypted separately from the start. Retrofitting this onto a schema with plaintext PII fields is a migration nightmare.

---

## 7. How Compliance Shapes Architecture

Every major architectural decision in Settla has a compliance reason. This is not a coincidence -- it is the natural result of understanding regulatory requirements before designing the system.

Here is the complete mapping:

```
ARCHITECTURE DECISION <--> COMPLIANCE REASON

+------------------------------------------+------------------------------------------+
| Architecture Decision                    | Compliance Reason                        |
+------------------------------------------+------------------------------------------+
| Append-only ledger (TigerBeetle +        | Regulators require a complete,           |
| Postgres read model, no UPDATE/DELETE    | unalterable transaction history.         |
| on entries)                              | Any system that allows ledger            |
|                                          | modification cannot pass an audit.       |
+------------------------------------------+------------------------------------------+
| Tenant isolation (every query filters    | Investigation scope must be per-entity.  |
| by tenant_id, Critical Invariant #7)    | A regulator investigating Tenant X must  |
|                                          | see only Tenant X's data.               |
+------------------------------------------+------------------------------------------+
| PII encryption (AES-256-GCM on          | Travel Rule requires data protection.    |
| sender/recipient fields)                 | GDPR requires data protection by design. |
|                                          | Crypto-shred enables right to erasure.   |
+------------------------------------------+------------------------------------------+
| KYB status gate (dual-gate:             | Cannot process financial transactions    |
| Status == ACTIVE AND KYBStatus ==       | for unverified entities. This is a legal |
| VERIFIED)                                | requirement in every jurisdiction.        |
+------------------------------------------+------------------------------------------+
| Partition retention policy (transfers    | 5-7 year retention for financial records.|
| and ledger entries never auto-dropped)   | Outbox is operational data, dropped      |
|                                          | after 48h (not regulatory).              |
+------------------------------------------+------------------------------------------+
| Event-sourced position events            | Audit trail for treasury mutations.      |
| (position_events table)                  | Regulators need to trace every change    |
|                                          | to a financial position.                 |
+------------------------------------------+------------------------------------------+
| Transfer state machine with events       | Every state transition is recorded.      |
| (not just current state)                 | Regulators reconstruct the timeline of   |
|                                          | a transaction during investigation.      |
+------------------------------------------+------------------------------------------+
| Immutable outbox entries                 | Proves what the system decided at each   |
|                                          | point in time. If a regulator asks "why  |
|                                          | was this transfer sent to Provider X?"   |
|                                          | the outbox entry contains the answer.    |
+------------------------------------------+------------------------------------------+
| Net settlement with overdue              | Regulatory requirement to manage         |
| escalation                               | counterparty risk. Unsettled positions   |
|                                          | must be escalated and resolved.          |
+------------------------------------------+------------------------------------------+
| Per-tenant fee schedules                 | Fee transparency and auditability.       |
| (basis points, recorded on transfer)     | Regulators can verify that fees charged  |
|                                          | match the agreed schedule.               |
+------------------------------------------+------------------------------------------+
| Idempotency keys scoped per-tenant       | Prevents duplicate transactions, which   |
| (Critical Invariant #4)                  | could be used for laundering or fraud.   |
|                                          | Also prevents accidental double-charges. |
+------------------------------------------+------------------------------------------+
| Webhook HMAC-SHA256 signatures           | Non-repudiation: proves Settla sent the  |
|                                          | notification and it was not tampered     |
|                                          | with in transit.                         |
+------------------------------------------+------------------------------------------+
```

Notice a pattern in this table: there is no "nice to have" column. Every architectural feature maps to a specific regulatory requirement. This is what "compliance-driven architecture" means in practice.

### The Cost of Getting It Wrong

What happens if you build the system first and add compliance later?

```
SCENARIO: ADDING COMPLIANCE RETROACTIVELY

Original design (no compliance consideration):
- Ledger uses UPDATE to modify balances (mutable)
- No tenant_id on transfers table
- PII stored in plaintext
- No transfer events (only current state)
- No KYB gate on API access

Retrofit required:
1. Rewrite ledger to append-only
   - Migration: convert all existing records to append-only format
   - Risk: data loss during migration
   - Timeline: 3-6 months

2. Add tenant_id to all tables
   - Migration: backfill tenant_id for all existing records
   - Risk: incorrect tenant assignment for historical data
   - Timeline: 2-4 months

3. Encrypt PII fields
   - Migration: generate keys, encrypt all existing PII
   - Risk: key management complexity, performance impact
   - Timeline: 1-3 months

4. Add transfer event history
   - Problem: CANNOT be retrofitted for historical transfers
   - Events that already happened were never recorded
   - Regulator asks "what happened at step 3 of transfer T-12345?"
   - Answer: "We don't know" (unacceptable)
   - Timeline: impossible for historical data

5. Add KYB gate
   - Migration: all existing tenants need retroactive KYB
   - Risk: must suspend non-verified tenants (business disruption)
   - Timeline: 1-2 months engineering + months of compliance review

Total retrofit cost: 6-18 months, significant data risk,
incomplete historical records, business disruption.

vs.

Building with compliance from the start: ~10-15% additional
initial engineering effort, zero retrofit risk.
```

> **Key Insight:** If you design the architecture first and add compliance later, you will rewrite half the system. If you understand compliance first, the architecture naturally follows. Every "extra" table, every "unnecessary" event, every "redundant" audit log exists because a regulator will ask for it. The 10-15% additional effort to build compliance in from the start saves 6-18 months of painful retrofitting later.

---

## 8. Sanctions Screening and Blockchain-Specific Challenges

Crypto settlement introduces unique compliance challenges that do not exist in traditional finance.

### Blockchain Address Screening

In traditional finance, sanctions screening checks names and account numbers against lists maintained by OFAC, the EU, and the UN. In crypto, sanctions screening must also check blockchain addresses.

OFAC has added blockchain addresses to the SDN (Specially Designated Nationals) list since 2018. The most notable example is the sanctioning of Tornado Cash in August 2022, which added Ethereum smart contract addresses to the SDN list.

Settla must screen:

```
BLOCKCHAIN SANCTIONS SCREENING

What must be screened:
+-------------------------------+------------------------------------------+
| Element                       | How                                      |
+-------------------------------+------------------------------------------+
| Destination wallet address    | Check against OFAC SDN list, EU          |
|                               | sanctions list, other applicable lists   |
+-------------------------------+------------------------------------------+
| Source wallet address          | Check for known sanctioned addresses     |
| (for deposits)                | or addresses associated with illicit     |
|                               | activity (chain analysis)                |
+-------------------------------+------------------------------------------+
| Smart contract interactions   | Check if the transaction involves        |
|                               | interaction with a sanctioned smart      |
|                               | contract (e.g., Tornado Cash)            |
+-------------------------------+------------------------------------------+
| Indirect exposure             | Chain analysis: has this address          |
|                               | received funds from a sanctioned         |
|                               | address within N hops?                   |
+-------------------------------+------------------------------------------+
```

Chain analysis is a specialized discipline. Services like Chainalysis, Elliptic, and TRM Labs provide APIs that analyze blockchain transaction graphs to identify exposure to sanctioned entities, darknet markets, ransomware proceeds, and other illicit sources.

### The Immutability Problem

Blockchain transactions are irreversible. In traditional banking, if you accidentally send money to a sanctioned entity, the bank can often recall the wire transfer. On the blockchain, once a transaction is confirmed, it cannot be reversed.

This means sanctions screening must happen BEFORE the blockchain transaction is submitted, not after:

```
CORRECT: Screen before blockchain send

    Transfer reaches SETTLING state
         |
         v
    BlockchainWorker picks up intent
         |
         v
    Screen destination address against sanctions lists  <-- HERE
         |
         +--- MATCH --> Block transfer, file SAR, return to engine
         |              with failure result
         |
         +--- CLEAN --> Submit blockchain transaction
                             |
                             v
                        Confirmed on-chain (irreversible)


WRONG: Screen after blockchain send

    Submit blockchain transaction
         |
         v
    Confirmed on-chain (irreversible)
         |
         v
    Screen destination address      <-- TOO LATE
         |
         +--- MATCH --> Funds already sent to sanctioned address
                        Settla is now in violation of sanctions law
                        No way to recall the transaction
```

This ordering constraint is why the blockchain worker in Settla performs sanctions screening as part of its pre-send checks, not as a post-send verification.

---

## 9. Putting It All Together: The Compliance Lifecycle of a Transfer

Let us trace a single transfer through all the compliance checkpoints it encounters:

```
COMPLIANCE LIFECYCLE OF A TRANSFER

1. API Request Arrives
   |
   +-- Auth: Is API key valid? Is tenant Active AND KYB Verified?
   |   (dual-gate, KYB Section 2)
   |
   +-- Rate limit: Is tenant within allowed request rate?
   |   (per-tenant DoS protection)
   |
   +-- Idempotency: Has this request been seen before?
   |   (prevents duplicate transactions, per-tenant scope)
   |
   v
2. Transfer Created
   |
   +-- Amount check: Is it within per-transfer and daily limits?
   |   (configured per tenant during KYB)
   |
   +-- Pending transfer limit: Is tenant under MaxPendingTransfers?
   |   (Critical Invariant #13)
   |
   +-- Fee calculation: Fees recorded on transfer record
   |   (audit trail for fee transparency)
   |
   +-- State: CREATED (event recorded: transfer.initiated)
   |
   v
3. Treasury Reserved
   |
   +-- Position event logged (audit trail for treasury mutation)
   |
   +-- State: FUNDED (event recorded: transfer.funded)
   |
   v
4. On-Ramp
   |
   +-- Provider transaction logged (which provider, amount, rate)
   |
   +-- State: ON_RAMPING -> SETTLING (events recorded)
   |
   v
5. Blockchain Send
   |
   +-- Sanctions screening: destination address checked       <-- CRITICAL
   |   against OFAC SDN, EU sanctions, chain analysis
   |
   +-- Transaction hash recorded (immutable proof of send)
   |
   +-- State: SETTLING (event recorded: transfer.settling)
   |
   v
6. Off-Ramp
   |
   +-- Provider transaction logged
   |
   +-- State: OFF_RAMPING -> COMPLETED (events recorded)
   |
   v
7. Completion
   |
   +-- Ledger entries posted (append-only, balanced debits/credits)
   |
   +-- Webhook delivered (HMAC-signed, non-repudiation)
   |
   +-- All data retained per jurisdiction requirements
   |
   v
8. Post-Completion (ongoing)
   |
   +-- Transaction monitoring: this transfer is analyzed as part of
   |   tenant's overall pattern (structuring, velocity, etc.)
   |
   +-- Data available for regulatory investigation for 5-7 years
```

Every step in this lifecycle produces an auditable record. If a regulator asks about any transfer, Settla can reconstruct the complete story: who initiated it, when each step happened, which providers were used, what the fees were, the blockchain transaction hash, and the full ledger entries.

---

## Summary

Regulation is not a constraint imposed on an otherwise-free system. It is the operating environment. Just as a fish does not notice water, engineers who have internalized compliance requirements do not notice the "extra" effort -- it is simply how financial systems are built.

The key principles from this chapter:

1. **KYB is a gate, not a checkbox.** No tenant processes transactions without verification. The dual-gate pattern (`Status == ACTIVE AND KYBStatus == VERIFIED`) enforces this at the code level.

2. **AML requires complete history.** Transaction monitoring, SAR filing, and regulatory investigations all require the ability to reconstruct any transaction's complete lifecycle. This is why Settla's ledger is append-only, transfers have event history, and treasury has position events.

3. **The Travel Rule dictates the data model.** Encrypted sender/recipient fields, per-tenant encryption keys, and the crypto-shred pattern all exist because of FATF Recommendation 16 and its interaction with GDPR.

4. **Sanctions screening must precede irreversible actions.** Blockchain transactions cannot be recalled. Screening must happen before the on-chain send, never after.

5. **Multi-jurisdictional compliance is multiplicative.** Each corridor multiplies the regulatory surface area. The architecture must support different rules per jurisdiction.

6. **Build compliance in from day one.** Retrofitting compliance onto a non-compliant architecture costs 6-18 months and produces an inferior result (historical data gaps, migration risks, business disruption).

In the next chapters, we will see how each of these compliance requirements is concretely implemented in Settla's domain model, state machines, and storage layer.

---

## Exercises

### Exercise 1: Regulatory Data Request

A regulator requests all transactions for Tenant X from the past 3 years, including:
- Sender and recipient PII
- Complete ledger entries (debits and credits)
- Treasury position changes during the period

**Questions:**

1. Which Settla tables do you need to query? List each table and what data it provides.
2. What joins are required to produce a complete transaction report?
3. Tenant X previously requested GDPR erasure for a specific end user. The encryption key for that user's PII has been deleted. How does this affect your ability to fulfill the regulator's request? What do you report to the regulator for transfers involving that user?
4. The regulator asks for data from both the Transfer DB and the Ledger DB. These are separate databases. How do you correlate records across them?

### Exercise 2: Structuring Detection

Design a transaction monitoring rule for structuring detection.

**Requirements:**

1. Define the data inputs you need (which fields from which tables).
2. Define the detection logic: what conditions trigger an alert?
3. Where in Settla's architecture would you add this rule? Consider: should it be real-time (evaluated on every transfer) or batch (evaluated periodically)? What are the trade-offs?
4. What is the false positive rate concern? A tenant that legitimately processes many small transfers (e.g., a payroll company) will trigger naive structuring rules. How do you account for the tenant's expected transaction profile from KYB?
5. Draw an ASCII diagram showing how data flows from the transfer creation through the monitoring rule to the alert/block decision.

### Exercise 3: KYB Revocation Mid-Flight

Tenant Y has KYB status VERIFIED and 500 in-flight transfers (various states from CREATED to OFF_RAMPING). The compliance team changes the KYB status to REJECTED because adverse information was discovered about a UBO.

**Design the state transition flow:**

1. What should happen to transfers in state CREATED or FUNDED (not yet sent to a provider)?
2. What should happen to transfers in state ON_RAMPING or SETTLING (money is in motion)?
3. What should happen to transfers in state OFF_RAMPING (funds are being delivered)?
4. Should the tenant be notified? What are the tipping-off concerns?
5. Should new API requests from the tenant be rejected immediately, or should there be a grace period?
6. Write the pseudocode for the `HandleKYBRevocation(tenantID)` function, including the different treatment for each transfer state.
