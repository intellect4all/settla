# Chapter 10.6: The Regulatory Landscape -- MiCA, FCA, FinCEN, and What They Mean for Code

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why software engineers -- not just compliance officers -- must understand financial regulation
2. Map specific regulations (MiCA, FCA, FinCEN, FATF, GDPR) to concrete architectural decisions in Settla
3. Describe the FATF Travel Rule and its impact on data collection, encryption, and transfer models
4. Distinguish between the regulatory approaches of the EU, UK, US, and FATF and articulate how each shapes code
5. Evaluate whether a given system design satisfies the retention, erasure, and audit requirements of overlapping regulations
6. Identify the architectural patterns that allow a single codebase to serve multiple jurisdictions simultaneously

---

## Why Engineers Need to Understand Regulation

There is a common misconception in fintech engineering: regulation is the compliance team's problem, and engineers just build what they are told. This is wrong, and it leads to expensive failures.

Every table schema, every retention policy, every audit log, every encryption decision in Settla exists because of a regulation. The `EncryptedSender` and `EncryptedRecipient` types exist because of the FATF Travel Rule. The `CryptoShredder` exists because of GDPR. The `KYBStatus` field on the `Tenant` struct exists because of anti-money laundering directives. The append-only `position_events` table exists because of the Bank Secrecy Act's 7-year retention requirement.

When engineers do not understand these connections, they make decisions that seem reasonable from a purely technical standpoint but violate regulatory requirements:

```
  The Expensive Rework Scenario
  =============================

  Sprint 1:   Engineer designs transfers table with sender_name VARCHAR(255)
              Stores names in plaintext. Works great. Tests pass.

  Sprint 5:   Legal review discovers GDPR requires right to erasure.
              "We need to delete PII on request."
              Engineer: "Easy, DELETE FROM transfers WHERE tenant_id = ..."

  Sprint 5.5: Compliance discovers BSA requires 7-year record retention.
              "You cannot delete transfer records."
              Engineer: "But legal said we need to delete PII..."

  Sprint 6:   Architecture meeting. Both requirements are valid.
              Solution: encrypt PII with per-tenant keys, delete key = erase PII,
              keep encrypted (now unreadable) transfer records for 7 years.
              This is the crypto-shred pattern.

  Cost:       6 sprints of rework. Schema migration on 200M+ rows.
              3-month delay. Angry stakeholders.

  Prevention: Engineer understood both GDPR and BSA from day one.
              Designed EncryptedSender/EncryptedRecipient from the start.
              Zero rework.
```

The rule is simple: if you design the architecture first and add compliance later, you will rewrite half the system. Settla was designed with regulation in mind from the beginning, and this chapter explains the regulations that shaped those decisions.

---

## MiCA (Markets in Crypto-Assets Regulation) -- EU

### What It Is

MiCA is the European Union's comprehensive framework for regulating crypto-assets. It entered force in June 2024, with full enforcement from December 2024. It is the most significant piece of crypto regulation worldwide because it creates a single regulatory framework across all 27 EU member states.

Before MiCA, each EU country had its own crypto rules (or none at all). A company registered in Estonia could operate across the EU, but a company registered in France faced different requirements. MiCA replaces this patchwork with a unified regime.

### What It Requires

MiCA defines two categories relevant to Settla:

**1. Stablecoin issuers (EMT and ART):**

```
  MiCA Stablecoin Classification
  ===============================

  E-Money Token (EMT):
    - Referenced to a single fiat currency (e.g., USDC -> USD)
    - Issuer must be licensed as an e-money institution in the EU
    - Must hold 1:1 reserves in EU-regulated credit institutions
    - Must publish a white paper describing the token
    - Must offer redemption at par value at any time

  Asset-Referenced Token (ART):
    - Referenced to multiple assets, commodities, or currencies
    - Stricter requirements: authorization from national authority
    - Reserve requirements: segregated, liquid assets
    - Not relevant to Settla (we use single-currency stablecoins)
```

**2. Crypto-Asset Service Providers (CASPs):**

Any entity that provides crypto-asset services (custody, exchange, transfer, execution) must register as a CASP with a national competent authority in the EU. Settla, as infrastructure that facilitates stablecoin settlement, falls into this category when operating EU corridors.

### The Travel Rule Under MiCA

MiCA makes the FATF Travel Rule mandatory for all crypto transfers with no threshold exemption. This is stricter than the US approach (which has a $3,000 threshold for banks and a $1,000 guideline from FinCEN):

```
  Travel Rule Thresholds by Jurisdiction
  =======================================

  EU (MiCA):     EUR 0  -- ALL transfers, no exemption
  US (FinCEN):   $3,000 -- transfers above this threshold
  UK (FCA):      GBP 0  -- ALL transfers (aligned with EU approach)
  Singapore:     SGD 1,500
  Japan:         JPY 0  -- ALL transfers
```

For Settla, this means every transfer through an EU corridor must collect and transmit originator and beneficiary data, regardless of amount.

### Impact on Settla

MiCA directly shaped three architectural decisions:

**Decision 1: Per-corridor stablecoin selection.**

Not all stablecoins are MiCA-compliant. Circle (USDC issuer) obtained an EU e-money license, making USDC a compliant EMT. Tether (USDT issuer) has not obtained equivalent EU authorization, making USDT's status uncertain in the EU.

The router must select stablecoins based on corridor compliance requirements:

```go
// From rail/router/router.go

// Route evaluates all possible on-ramp->chain->off-ramp corridors for the given
// request, scores them, and returns the best route.
func (r *Router) Route(ctx context.Context, req domain.RouteRequest) (*domain.RouteResult, error) {
    candidates, err := r.buildCandidates(ctx, req)
    // ...
}
```

The candidate-building phase filters stablecoins by corridor. A `GBP->EUR` corridor through the EU would prefer USDC over USDT because of MiCA compliance. A `NGN->GBP` corridor (Nigeria to UK) might use either, depending on whether the UK leg triggers FCA requirements.

**Decision 2: CASP registration status tracking.**

Settla must track its own CASP registration status per jurisdiction and restrict operations to corridors where it holds valid authorization.

**Decision 3: Mandatory Travel Rule data collection.**

The `Sender` and `Recipient` structs collect the data required by the Travel Rule:

```go
// From domain/transfer.go

// Sender identifies the party initiating the transfer.
type Sender struct {
    ID      uuid.UUID
    Name    string
    Email   string
    Country string
}

// Recipient identifies the party receiving funds.
type Recipient struct {
    Name          string
    AccountNumber string
    SortCode      string
    BankName      string
    Country       string
    IBAN          string
}
```

These fields are encrypted at rest using per-tenant DEKs (covered in Chapter 10.2), satisfying both the Travel Rule's data collection mandate and GDPR's data protection requirements simultaneously.

> **Key Insight:** MiCA's zero-threshold Travel Rule means Settla cannot implement a "skip data collection for small transfers" optimization for EU corridors. Every transfer, even EUR 0.01, must carry full originator and beneficiary data. The architecture must treat data collection as mandatory, not optional.

---

## FCA (Financial Conduct Authority) -- UK

### What It Is

The FCA is the UK's financial services regulator. Since Brexit, the UK is no longer covered by EU regulations (including MiCA), so it maintains its own crypto-asset regime. The FCA's approach is risk-based and prescriptive, with particular emphasis on anti-money laundering and counter-terrorist financing (AML/CTF).

### What It Requires

**Crypto asset registration under the Money Laundering Regulations (MLRs):**

Any business carrying on crypto-asset activity in the UK must register with the FCA under the MLRs. This is not a full license -- it is a registration that confirms the business has adequate AML/CTF controls. The FCA has rejected a high percentage of applicants, signaling a stringent approach.

**Financial promotions regime:**

Since October 2023, the FCA has enforced strict rules on marketing crypto services to UK consumers. All crypto promotions must be approved by an FCA-authorized firm, include clear risk warnings, and not be misleading. This affects Settla's tenant-facing documentation, marketing materials, and even the portal UI.

**AML/CTF requirements:**

- Customer due diligence (CDD) for all customers
- Enhanced due diligence (EDD) for high-risk customers or transactions
- Ongoing monitoring of business relationships
- Suspicious Activity Reports (SARs) to the National Crime Agency (NCA)
- Record-keeping for 5 years after the business relationship ends

### Impact on Settla

```
  FCA Requirements -> Settla Architecture
  ========================================

  FCA Registration       -> Operational prerequisite for GBP corridors
  AML/CTF controls       -> KYB verification (dual-gate tenant activation)
  CDD/EDD                -> Tenant.KYBStatus field, enhanced checks for
                            high-risk corridors
  Record-keeping (5yr)   -> Append-only ledger, no auto-purge on transfers
  SAR filing             -> Event-driven audit trail enables SAR evidence
                            collection
  Financial promotions   -> Portal and docs-site content review process
```

The dual-gate tenant activation pattern (both `Status` and `KYBStatus` must pass) is a direct result of FCA requirements:

```go
// From domain/tenant.go

// IsActive returns true if the tenant is ACTIVE and KYB VERIFIED.
// Both conditions must be met for a tenant to process transactions.
func (t *Tenant) IsActive() bool {
    return t.Status == TenantStatusActive && t.KYBStatus == KYBStatusVerified
}
```

This is checked at the engine level before any transfer is created:

```go
// From core/engine.go

tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
if err != nil {
    return nil, fmt.Errorf("settla-core: create transfer: loading tenant %s: %w", tenantID, err)
}
if !tenant.IsActive() {
    return nil, domain.ErrTenantSuspended(tenantID.String())
}
```

A tenant that has not passed KYB cannot create transfers, period. This is not a UI-level check that can be bypassed -- it is enforced in the settlement engine itself.

---

## FinCEN (Financial Crimes Enforcement Network) -- US

### What It Is

FinCEN is the US Treasury Department's bureau responsible for combating money laundering and terrorist financing. For crypto companies operating US corridors, FinCEN is the primary federal regulator. But it is not the only one -- the US has a uniquely fragmented regulatory landscape.

### The Federal Layer

**Money Services Business (MSB) registration:**

Any business that transmits money (including stablecoins) must register as an MSB with FinCEN. Registration is relatively straightforward -- it is a filing requirement, not a license. The real burden comes from the compliance obligations that follow.

**Bank Secrecy Act (BSA) compliance program:**

Once registered as an MSB, the business must implement a BSA compliance program with five pillars:

```
  BSA Compliance Program -- Five Pillars
  ========================================

  1. Internal controls     Policies and procedures for detecting and
                           reporting suspicious activity

  2. Independent testing   Annual audit by qualified third party

  3. Designated officer    Named individual responsible for BSA compliance

  4. Training              Ongoing AML training for all relevant employees

  5. CDD procedures        Customer due diligence and beneficial ownership
                           identification
```

**Reporting requirements:**

```
  BSA Reporting Thresholds
  =========================

  Currency Transaction Report (CTR):
    Threshold: $10,000 in a single transaction or related transactions
    Filing:    Within 15 days
    Applies:   Cash transactions (fiat on-ramp/off-ramp)

  Suspicious Activity Report (SAR):
    Threshold: $2,000 if the activity is suspicious
    Filing:    Within 30 days of detection
    Applies:   Any transaction, regardless of amount, if suspicious

  FinCEN Form 8300:
    Threshold: $10,000 in cash (received by non-financial businesses)
    Filing:    Within 15 days
    Applies:   Less relevant for Settla but worth noting
```

### The State Layer -- "The Nightmare"

The federal MSB registration is the easy part. The hard part is state-level money transmitter licensing. Each of the 50 US states (plus DC, Puerto Rico, Guam, and US Virgin Islands) has its own licensing requirements:

```
  State Money Transmitter Licensing -- The Patchwork
  ====================================================

  New York:      BitLicense (unique to NY, notoriously strict)
                 Separate from standard money transmitter license
                 Cost: $5,000 application + significant legal fees
                 Timeline: 12-24 months

  California:    Money Transmission Act license
                 Surety bond: $500,000 minimum
                 Net worth: $500,000 minimum

  Texas:         Money transmitter license
                 Surety bond: based on transmission volume

  Montana:       No money transmitter license required (one of few states)

  Wyoming:       Special Purpose Depository Institution (SPDI) charter
                 Crypto-friendly regulatory framework

  Total states requiring licenses: ~48 (Montana and South Carolina have
  limited exemptions)

  Typical cost per state:  $10,000 - $50,000 (legal + application)
  Typical timeline:        3-12 months per state
  Total US licensing cost: $500K - $2M+ for full 50-state coverage
```

This is why many crypto companies avoid the US market until they have significant funding, or partner with a licensed entity to "borrow" their license.

### Impact on Settla

**Record retention:** The BSA requires 7 years of record retention for all transaction data. This is why Settla's ledger is append-only and why the partition manager's auto-drop policy explicitly excludes transfer and ledger tables:

```
  Partition Retention Policies
  ============================

  Table                  Retention    Reason
  -----                  ---------    ------
  outbox_entries         90 days      Operational data, no regulatory hold
  position_events        90 days      Audit log, replayed from source
  transfer_events        7 years      BSA/AMLD6 record retention
  transfers              7 years      BSA/AMLD6 record retention
  entry_lines (ledger)   7 years      BSA/AMLD6 record retention
  journal_entries        7 years      BSA/AMLD6 record retention
```

**Transaction monitoring:** The $10,000 CTR threshold and $2,000 SAR threshold require Settla to flag transactions that cross these boundaries. The event-driven architecture (every state transition produces a `TransferEvent`) makes this possible without adding monitoring logic to the hot path:

```
  Transaction Monitoring Architecture
  =====================================

  Transfer created ($12,000 USD)
       |
       v
  TransferEvent (transfer.initiated)
       |
       v
  Event stream (SETTLA_TRANSFERS)
       |
       +---> Normal transfer flow (ProviderWorker, LedgerWorker, etc.)
       |
       +---> Compliance monitoring consumer (async)
                |
                +---> CTR threshold check ($10,000)
                |     Result: FLAGGED -- exceeds CTR threshold
                |
                +---> SAR pattern check
                |     Result: No suspicious pattern detected
                |
                +---> Generate CTR filing data
```

The monitoring consumer reads from the same event stream as the operational workers but does not block the transfer flow. This is a critical design choice: compliance monitoring must never add latency to the hot path.

---

## AMLD6 (6th Anti-Money Laundering Directive) -- EU

### What It Is

AMLD6 is the EU's latest anti-money laundering directive, strengthening the previous AMLD5. Its most significant change: criminal liability for AML failures. This means not just corporate fines -- individual officers (including CTOs and engineering leaders) can face criminal prosecution for systematic AML failures.

### What It Requires

```
  AMLD6 Key Requirements
  =======================

  Criminal liability:
    - Aiding and abetting money laundering is a criminal offense
    - Applies to legal persons (companies) AND natural persons (officers)
    - Penalties: imprisonment for natural persons, fines for legal persons
    - "Willful blindness" is no defense -- negligent AML is criminal

  Enhanced CDD for high-risk:
    - High-risk countries (FATF grey/black list)
    - Politically Exposed Persons (PEPs)
    - Complex or unusual transactions
    - Transactions with no apparent economic purpose

  Beneficial ownership:
    - Ultimate Beneficial Owner (UBO) must be identified
    - UBO: any natural person who owns >25% or exercises control
    - UBO information must be verified, not just declared
    - Central UBO registers in each member state
```

### Impact on Settla

AMLD6 drives the KYB (Know Your Business) requirements for EU tenants. The tenant onboarding flow must:

1. Collect UBO information during registration
2. Verify UBO identity against official registers
3. Screen UBOs against PEP and sanctions lists
4. Apply enhanced due diligence for high-risk indicators
5. Re-verify periodically (ongoing monitoring)

The `KYBStatus` state machine reflects this process:

```go
// From domain/tenant.go

type KYBStatus string

const (
    KYBStatusPending   KYBStatus = "PENDING"    // Documents not yet submitted
    KYBStatusInReview  KYBStatus = "IN_REVIEW"  // Documents under verification
    KYBStatusVerified  KYBStatus = "VERIFIED"   // Passed KYB checks
    KYBStatusRejected  KYBStatus = "REJECTED"   // Failed KYB checks
)
```

```
  KYB State Machine
  ==================

  PENDING ----[submit documents]----> IN_REVIEW
  IN_REVIEW --[verification pass]---> VERIFIED
  IN_REVIEW --[verification fail]---> REJECTED
  REJECTED ---[resubmit]------------> IN_REVIEW
  VERIFIED ---[periodic review]-----> IN_REVIEW  (re-verification)
```

Only `VERIFIED` tenants can process live transfers. This gate cannot be bypassed through any code path -- it is enforced in the engine's `CreateTransfer`, `GetRoutingOptions`, and `GetQuote` methods.

> **Key Insight:** AMLD6's criminal liability provision means engineering decisions have personal legal consequences for officers. If you design a system that systematically fails to detect money laundering because of architectural shortcuts, the CTO is not protected by "the compliance team approved it." This is why engineers must understand regulation: the code IS the compliance control.

---

## FATF (Financial Action Task Force) -- Global Standards

### What It Is

FATF is not a regulator. It is an intergovernmental body that sets standards for combating money laundering and terrorist financing. Its recommendations are adopted by 200+ jurisdictions worldwide. When FATF issues a standard, national regulators (FCA, FinCEN, BaFin, etc.) typically implement it in their local laws within 2-5 years.

FATF does not enforce anything directly. But its peer review process (Mutual Evaluations) and its grey/black lists create enormous pressure on countries to comply. A country on the FATF grey list faces increased transaction monitoring from every financial institution worldwide, making cross-border payments slower and more expensive.

### The Travel Rule -- Deep Dive

The Travel Rule (FATF Recommendation 16) is the single regulation with the most direct impact on Settla's data model. It requires that crypto-asset service providers transmit originator and beneficiary information with every transfer.

**What data must be collected:**

```
  Travel Rule Required Data
  ==========================

  Originator (Sender):
    Required:
      - Full name
      - Account number (or unique transaction reference)
      - One of: address, national ID number, date of birth

    For transfers above threshold:
      - All of the above, plus:
      - Address OR national ID number OR customer ID OR date and place of birth

  Beneficiary (Recipient):
    Required:
      - Full name
      - Account number (or unique transaction reference)

  Additional for crypto:
    - Blockchain address (if applicable)
    - Transaction hash (if applicable)
```

Settla collects this data in the `Sender` and `Recipient` structs. The data is validated before the transfer enters the engine:

```go
// From domain/transfer.go

// Validate checks that the sender has the minimum required fields populated.
func (s Sender) Validate() error {
    if s.Name == "" {
        return fmt.Errorf("settla-domain: sender name is required")
    }
    if s.Email != "" && !strings.Contains(s.Email, "@") {
        return fmt.Errorf("settla-domain: sender email %q is not a valid email address", s.Email)
    }
    return nil
}

// Validate checks that the recipient has the minimum required fields populated.
func (r Recipient) Validate() error {
    if r.Name == "" {
        return fmt.Errorf("settla-domain: recipient name is required")
    }
    if r.Country == "" {
        return fmt.Errorf("settla-domain: recipient country is required")
    }
    // ... additional field validation ...
}
```

**How to transmit the data:**

The Travel Rule requires that originator/beneficiary data be transmitted between VASPs (Virtual Asset Service Providers). Several protocols have emerged for this:

```
  Travel Rule Transmission Protocols
  ====================================

  Protocol      Backed By                     Approach
  --------      ---------                     --------
  TRISA         TRISA Foundation              mTLS peer-to-peer, certificate-based
                                              identity verification
  Notabene      Notabene (commercial)         API-based, hosted compliance platform
  Shyft         Veriscope/Shyft Network       Blockchain-based VASP registry
  OpenVASP      OpenVASP Association           Open standard, decentralized
  Sygna Bridge  CoolBitX                       API-based, Asia-focused

  Settla approach:
    - Store encrypted sender/recipient on each transfer
    - Expose Travel Rule data via API for VASP-to-VASP transmission
    - Protocol selection is corridor-dependent (TRISA for US, Notabene for EU)
```

**How Settla encrypts Travel Rule data:**

All Travel Rule PII is encrypted using per-tenant AES-256-GCM encryption. This satisfies both the Travel Rule (data must be available to regulators) and GDPR (data must be protected and erasable):

```go
// From domain/crypto.go

// EncryptedSender holds the encrypted form of Sender PII fields.
// Non-PII fields (ID, Country) remain in plaintext.
type EncryptedSender struct {
    ID             uuid.UUID `json:"id"`
    EncryptedName  []byte    `json:"name"`
    EncryptedEmail []byte    `json:"email"`
    Country        string    `json:"country"`
}

// EncryptedRecipient holds the encrypted form of Recipient PII fields.
// Non-PII fields (Country) remain in plaintext.
type EncryptedRecipient struct {
    EncryptedName          []byte `json:"name"`
    EncryptedAccountNumber []byte `json:"account_number"`
    EncryptedSortCode      []byte `json:"sort_code"`
    EncryptedBankName      []byte `json:"bank_name"`
    Country                string `json:"country"`
    EncryptedIBAN          []byte `json:"iban"`
}
```

Notice that `Country` remains in plaintext on both structs. This is intentional: country is needed for routing decisions and sanctions screening without decrypting PII. It is not itself PII under most data protection frameworks (it refers to the country of the bank, not the person's nationality).

### Risk-Based Approach

FATF's core principle is the Risk-Based Approach (RBA): not all customers, corridors, or transactions carry the same money laundering risk. Resources should be allocated proportionally:

```
  Risk-Based Approach in Settla
  ==============================

  Risk Level    Indicators                     Settla Response
  ----------    ----------                     ---------------
  Low           Established fintech tenant,    Standard KYB,
                low-risk corridor (UK->EU),    standard monitoring,
                small transaction amounts       simplified CDD

  Medium        New tenant, moderate corridor   Enhanced KYB,
                (UK->Nigeria), medium amounts   periodic re-verification,
                                                transaction pattern monitoring

  High          Tenant in high-risk sector,     Enhanced due diligence,
                corridor through grey-list      senior management approval,
                country, large amounts,         ongoing monitoring,
                unusual patterns                SAR filing if warranted

  Prohibited    Sanctioned entity or country    Block at engine level,
                on FATF black list              no transfer created
```

The per-tenant `MaxPendingTransfers` limit and per-tenant `DailyLimitUSD` are risk-based controls enforced in the engine:

```go
// From core/engine.go

// Check per-tenant pending transfer limit
if tenant.MaxPendingTransfers > 0 {
    count, err := e.transferStore.CountPendingTransfers(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-core: create transfer: counting pending transfers: %w", err)
    }
    if count >= tenant.MaxPendingTransfers {
        return nil, fmt.Errorf("settla-core: create transfer: tenant %s exceeded max pending transfers (%d)",
            tenantID, tenant.MaxPendingTransfers)
    }
}
```

These limits are calibrated per tenant based on risk assessment. A low-risk, established tenant (Lemfi) might have `MaxPendingTransfers = 0` (unlimited), while a newly onboarded tenant starts with a restrictive limit (e.g., 100) that is raised as the relationship matures.

### FATF Grey List and Black List

FATF maintains two lists that directly affect transaction processing:

```
  FATF Lists (as of 2025)
  ========================

  Grey List (Increased Monitoring):
    Countries with strategic AML/CFT deficiencies that have committed
    to action plans. Transfers to/from these jurisdictions require
    enhanced due diligence.

    Examples: Nigeria, South Africa, Turkey, UAE (status changes regularly)

  Black List (High-Risk Jurisdictions):
    Countries with significant strategic deficiencies. FATF calls on all
    jurisdictions to apply enhanced due diligence, and in the most serious
    cases, counter-measures.

    Examples: North Korea, Iran, Myanmar

  Impact on Settla:
    - Grey list countries: enhanced monitoring, additional data collection,
      periodic corridor risk review
    - Black list countries: transfers blocked at the engine level
    - List changes: must be reflected in configuration within regulatory
      timeframes (typically 30 days of FATF plenary announcement)
```

The country check happens before a transfer enters the state machine. Corridor-level restrictions are configured per tenant and enforced in the router's candidate filtering phase. A transfer with a recipient in a black-listed country never reaches the CREATED state -- it is rejected at validation time.

---

## Data Protection Regulations

### GDPR (General Data Protection Regulation) -- EU

GDPR's six principles all affect Settla's architecture:

```
  GDPR Principles -> Settla Implementation
  ==========================================

  1. Lawfulness          Processing based on legitimate interest (contract
                         performance for settlement) or consent

  2. Purpose limitation  PII collected for settlement only, not marketing

  3. Data minimization   Collect only what Travel Rule requires (name,
                         account, country -- not date of birth unless
                         threshold requires it)

  4. Accuracy            Validated at input (Sender.Validate(),
                         Recipient.Validate())

  5. Storage limitation  Crypto-shred after retention period expires

  6. Integrity &         AES-256-GCM encryption, per-tenant DEKs,
     confidentiality     TLS in transit, tenant isolation
```

**The Right to Erasure and the Crypto-Shred Resolution:**

GDPR Article 17 gives data subjects the right to have their personal data erased. But BSA/AMLD6 requires 7 years of transaction record retention. These requirements directly conflict.

Settla resolves this conflict with the crypto-shred pattern:

```
  Crypto-Shred: Satisfying Both GDPR and BSA
  ============================================

  Before shred:
    Transfer record:     { id: "abc", sender_name: [encrypted], amount: 1000 }
    Per-tenant DEK:      [32 bytes in KMS]
    Decrypt(encrypted, DEK) -> "John Smith"

  Shred operation:
    1. Delete DEK from KMS (irreversible)
    2. Record shred in shred_records table (audit trail)

  After shred:
    Transfer record:     { id: "abc", sender_name: [encrypted], amount: 1000 }
    Per-tenant DEK:      DESTROYED
    Decrypt(encrypted, ???) -> IMPOSSIBLE

  Result:
    - GDPR satisfied: PII is effectively erased (unrecoverable)
    - BSA satisfied:  Transaction record still exists (amount, date,
                      corridor, status -- all non-PII fields intact)
    - Audit trail:    shred_records table proves when erasure happened
```

The implementation lives in `domain/crypto.go`:

```go
// From domain/crypto.go

// KeyManager abstracts access to per-tenant data encryption keys (DEKs).
// In production this is backed by AWS KMS envelope encryption; the DEK is
// stored encrypted (wrapped) and unwrapped on demand.
type KeyManager interface {
    // GetDEK returns the plaintext DEK for the given tenant using the current
    // (latest) key version. Returns ErrTenantShredded if the tenant's key has
    // been destroyed.
    GetDEK(tenantID uuid.UUID) ([]byte, error)

    // DeleteDEK permanently destroys the tenant's DEK, making all PII
    // encrypted with it unrecoverable. This is the core of crypto-shred.
    DeleteDEK(tenantID uuid.UUID) error
}
```

After `DeleteDEK` is called, any attempt to decrypt PII for that tenant returns `RedactedPII`:

```go
// From domain/crypto.go

// Decrypt decrypts a PII field value for the given tenant using the current key.
// If the tenant has been crypto-shredded, returns RedactedPII.
func (e *PIIEncryptor) Decrypt(tenantID uuid.UUID, ciphertext []byte) (string, error) {
    if len(ciphertext) == 0 {
        return "", nil
    }
    dek, err := e.km.GetDEK(tenantID)
    if err != nil {
        if errors.Is(err, ErrTenantShredded) {
            return RedactedPII, nil
        }
        return "", fmt.Errorf("settla-domain: getting DEK for tenant %s: %w", tenantID, err)
    }
    plaintext, err := DecryptPII(ciphertext, dek)
    if err != nil {
        return "", err
    }
    return string(plaintext), nil
}
```

The system degrades gracefully: a shredded tenant's transfers still appear in reports and queries, but all PII fields show `[REDACTED]`. Financial totals, dates, statuses, and corridor information remain intact for regulatory reporting.

### NDPR (Nigeria Data Protection Regulation)

Nigeria's NDPR follows similar principles to GDPR, with some differences:

- Data Protection Impact Assessment (DPIA) required for high-risk processing
- Data controllers must file annual returns with the Nigeria Data Protection Bureau (NDPB)
- Cross-border transfer restrictions: personal data can only be transferred to countries with "adequate protection" or with appropriate safeguards

For Settla, the NDPR's cross-border requirements mean that PII originating from Nigerian tenants must be handled with the same encryption and access controls as GDPR data. The per-tenant DEK architecture naturally satisfies this -- each tenant's data is encrypted with their own key, regardless of jurisdiction.

### Data Processing Agreements (DPAs)

Both GDPR and NDPR require Data Processing Agreements between data controllers (tenants) and data processors (Settla). The DPA defines:

- What data is processed (sender/recipient PII for settlement)
- How long it is retained (7 years for regulatory compliance)
- How it is protected (AES-256-GCM, per-tenant keys)
- How it is deleted (crypto-shred procedure)
- Sub-processor obligations (cloud providers, KMS)

The DPA is a legal document, not a code artifact. But the code must implement every commitment in the DPA. If the DPA promises "data encrypted at rest with AES-256," the code must actually use AES-256 -- and the `domain/crypto.go` implementation is the evidence.

---

## Architecture Decisions Driven by Regulation

Every major architectural decision in Settla can be traced to a specific regulatory requirement. This table is the map between law and code:

```
  Regulation        Requirement                 Settla Architecture Decision
  ----------        -----------                 ----------------------------
  FATF Travel Rule  Collect originator/         Sender and Recipient structs
                    beneficiary data            with mandatory field validation

  FATF Travel Rule  Protect Travel Rule data    EncryptedSender, EncryptedRecipient
                    in transit and at rest      with AES-256-GCM per-tenant DEKs

  GDPR Art. 17      Right to erasure            CryptoShredder: delete DEK,
                                                PII becomes unrecoverable

  BSA / AMLD6       7-year record retention     Append-only ledger, no auto-drop
                                                on transfers or entry_lines

  MiCA              Compliant stablecoins       Per-corridor stablecoin selection
                    for EU corridors            in router candidate filtering

  MiCA              Travel Rule (no threshold)  Mandatory Sender/Recipient
                    for all EU transfers        validation on all transfers

  AML (multiple)    Transaction monitoring      Event-driven audit trail via
                                                TransferEvents and outbox entries

  AML (multiple)    Suspicious activity         Async compliance monitoring
                    detection                   consumer on event stream

  KYC/KYB           Customer verification       Dual-gate tenant activation:
                    before processing           Status=ACTIVE AND KYBStatus=VERIFIED

  KYC/KYB           Beneficial ownership        UBO collection in tenant
                    transparency                onboarding flow

  BSA               CTR filing ($10,000)        Amount-based flagging in
                                                compliance monitoring consumer

  GDPR              Data protection             AES-256-GCM PII encryption,
                                                per-tenant keys via KMS

  GDPR              Data minimization           Collect only Travel Rule
                                                minimum fields

  Multiple          Audit trail                 position_events, transfer_events,
                                                outbox entries, shred_records

  Multiple          Sanctions screening         Pre-transfer country validation
                                                in engine and router

  Multiple          Per-tenant risk limits      MaxPendingTransfers, DailyLimitUSD
                                                enforced in engine
```

> **Key Insight:** Notice that no single regulation drives any decision in isolation. The crypto-shred pattern exists because GDPR and BSA overlap. The encrypted sender/recipient types exist because the Travel Rule and GDPR overlap. The dual-gate activation exists because KYC/KYB requirements span every jurisdiction. A regulatory monoculture (designing for only one jurisdiction) guarantees rework when you enter a second market.

---

## The Compliance-Architecture Feedback Loop

Regulation is not static. New rules emerge, existing rules are amended, enforcement interpretations shift. The architecture must accommodate this evolution without requiring fundamental redesigns.

Settla's approach is to make compliance controls configurable rather than hard-coded:

```
  Compliance-Architecture Feedback Loop
  =======================================

  New regulation announced
       |
       v
  Legal team interprets requirements
       |
       v
  Engineering translates to technical requirements
       |
       +---> Is it a configuration change?
       |     (e.g., new threshold, new restricted country)
       |     YES -> Update configuration, no code change
       |
       +---> Is it a new data collection requirement?
       |     (e.g., new Travel Rule field)
       |     YES -> Schema migration + code change + API versioning
       |
       +---> Is it a new processing requirement?
             (e.g., new type of monitoring)
             YES -> New async consumer on existing event stream
                    No change to hot path
```

The event-driven architecture is particularly valuable here. When a new regulation requires monitoring a new pattern (say, structured transactions designed to avoid the $10,000 CTR threshold), the implementation is a new consumer on the existing `SETTLA_TRANSFERS` stream. The transfer flow does not change. The hot path does not get slower. The new monitoring logic runs asynchronously.

---

## Common Mistakes

1. **Treating compliance as a checkbox.** Compliance is ongoing, not one-time. Regulations change, enforcement evolves, FATF lists update quarterly. A system designed for "current regulations" without extensibility will require rework at every regulatory change.

2. **Assuming one jurisdiction's rules cover all.** An engineer who knows US BSA requirements may assume 7-year retention is universal. The UK requires 5 years. Some jurisdictions require 10 years. Each corridor's requirements must be tracked independently.

3. **Building first, complying later.** The most expensive words in fintech engineering: "we will add compliance later." The crypto-shred pattern, per-tenant encryption, and audit trail are foundational architecture. Retrofitting them onto a system designed without them requires touching every table, every query, and every API endpoint.

4. **Not involving legal counsel early.** Engineers should not interpret regulation alone. The difference between "must collect originator address" and "must collect originator address OR national ID number OR date of birth" is the difference between one required field and a choice of three -- and getting it wrong means either collecting too much data (GDPR minimization violation) or too little (Travel Rule violation).

5. **Ignoring the Travel Rule for crypto.** Many engineers think the Travel Rule applies only to banks and exchanges. It applies to ANY Virtual Asset Service Provider, including infrastructure providers like Settla that facilitate settlement. If you move stablecoins on behalf of others, the Travel Rule applies to you.

6. **Hard-coding jurisdiction-specific logic.** Writing `if country == "US" { checkCTRThreshold(10000) }` directly in the transfer flow creates a maintenance burden that scales linearly with the number of jurisdictions. Jurisdiction rules should be configuration, not code.

7. **Conflating data protection with data deletion.** GDPR's "right to erasure" does not mean "delete all records." It means "make personal data unrecoverable." The crypto-shred pattern satisfies this while preserving non-PII transaction records for regulatory retention. Engineers who implement erasure as `DELETE FROM transfers` violate the BSA.

8. **Forgetting that regulations interact.** MiCA's Travel Rule interacts with GDPR's data minimization. BSA's record retention interacts with GDPR's right to erasure. FinCEN's reporting requirements interact with NDPR's cross-border transfer restrictions. Each regulation must be considered in the context of all others that apply to a given corridor.

---

## Exercises

### Exercise 1: Regulatory Mapping

A new corridor is being added: `EUR -> KES` (Euro to Kenyan Shilling). The sender is a European fintech (EU-based). The recipient is a Kenyan bank account.

1. List every regulation that applies to this corridor (hint: there are at least 5).
2. For each regulation, identify the specific Settla component that satisfies the requirement.
3. Is Kenya on the FATF grey list? How would you determine this, and what additional controls would be required if it is?
4. What stablecoin should the router prefer for the EU leg, and why?

### Exercise 2: Design a CTR Threshold Monitor

Design an async compliance monitoring consumer that detects transfers requiring Currency Transaction Reports (CTR). Your design should:

1. Consume from the `SETTLA_TRANSFERS` stream without blocking the transfer flow
2. Flag individual transfers above $10,000
3. Detect "structuring" -- multiple transfers from the same sender that individually fall below $10,000 but collectively exceed it within a 24-hour window
4. Generate CTR filing data in the format required by FinCEN
5. Handle the case where thresholds differ by jurisdiction (US: $10,000, some countries: EUR 15,000)

Describe the data structures, the detection algorithm, and the storage requirements.

### Exercise 3: GDPR Erasure Request

A tenant (Fincra, EU-registered) receives a GDPR Article 17 erasure request from one of their end-users (a sender who has appeared on 47 transfers over the past 2 years). Walk through the complete erasure procedure:

1. What exactly gets erased? What gets preserved?
2. How does the crypto-shred pattern handle the fact that the sender's PII appears on 47 different transfer records?
3. What happens to reports and analytics that reference those transfers after the shred?
4. How do you prove to the data subject (or a regulator) that erasure was completed?
5. What if the 7-year BSA retention period has not yet expired for some of those transfers?

### Exercise 4: Travel Rule Protocol Selection

Settla needs to transmit Travel Rule data for a `GBP -> USD` corridor (UK sender, US recipient). Research and compare TRISA and Notabene as Travel Rule transmission protocols:

1. How does each protocol verify the identity of the counterparty VASP?
2. What data format does each protocol use?
3. What happens if the counterparty VASP does not support the same protocol?
4. Which protocol would you recommend for this corridor, and why?
5. How would you architect Settla to support multiple protocols simultaneously?

### Exercise 5: Regulatory Change Impact Assessment

FATF has just announced that the Travel Rule threshold for crypto transfers will be lowered to $0 globally (matching the EU's MiCA approach). Currently, Settla's US corridors only collect full Travel Rule data for transfers above $3,000.

1. What code changes are required?
2. What database changes are required (if any)?
3. What API changes are required (if any)?
4. How would you deploy this change without downtime?
5. What is the impact on existing transfers that were created before the threshold change? Do they need to be retroactively updated?

---

## Module 10 Complete -- Course Summary

This chapter concludes Module 10 and the entire Settla course. Over 11 modules, you have built a comprehensive understanding of production-grade fintech settlement infrastructure -- from the fundamentals of how money moves, to the regulatory landscape that governs every design decision.

Here is the complete journey:

```
  Module 0:  The Money Domain        Understanding the financial world you
                                     are building for: currencies, corridors,
                                     nostro/vostro, correspondent banking,
                                     FX mechanics, and why fintech exists

  Module 1:  Foundations             Domain modeling with Go, state machines
                                     as data, multi-tenancy from day one,
                                     idempotency keys, and the invariants
                                     that must never break

  Module 2:  The Ledger              Double-entry accounting at scale:
                                     TigerBeetle for writes, Postgres for
                                     reads, balanced postings, CQRS sync,
                                     and why ledgers are append-only

  Module 3:  The Settlement Engine   Pure state machine with zero side effects,
                                     transactional outbox for atomic delivery,
                                     the CREATED->COMPLETED lifecycle, and
                                     why the engine never calls the network

  Module 4:  Treasury & Routing      In-memory atomic reservations with 100ms
                                     background flush, smart routing with
                                     weighted scoring (cost, speed, liquidity,
                                     reliability), and corridor selection

  Module 5:  Async Workers           Event-driven execution via NATS JetStream,
                                     11 dedicated workers, the CHECK-BEFORE-CALL
                                     pattern, partition-based parallelism, and
                                     dead-letter queue handling

  Module 6:  The API Layer           gRPC between Go and TypeScript, Fastify
                                     REST gateway, three-tier auth caching,
                                     rate limiting, OpenAPI documentation,
                                     and connection pooling

  Module 7:  Operations              Reconciliation (6 automated checks),
                                     compensation strategies (4 patterns),
                                     stuck-transfer recovery, net settlement
                                     calculation, and partition management

  Module 8:  Production Readiness    Load testing at 5,000 TPS, chaos testing
                                     for failure recovery, observability with
                                     structured logging and metrics, Docker
                                     deployment, and capacity planning

  Module 9:  Deposits & Payments     Crypto deposit sessions with on-chain
                                     monitoring, bank deposits via virtual
                                     accounts, payment links for merchant
                                     collections, and blockchain integration

  Module 10: Security & Compliance   API key security (HMAC-SHA256), PII
                                     encryption (AES-256-GCM, crypto-shred),
                                     webhook signatures, secrets management,
                                     and the regulatory landscape
```

Each module built on the previous ones. The Money Domain (Module 0) gave you the vocabulary. Foundations (Module 1) gave you the modeling patterns. The Ledger (Module 2) gave you financial integrity. The Engine (Module 3) gave you the state machine. Treasury and Routing (Module 4) gave you performance. Workers (Module 5) gave you reliability. The API Layer (Module 6) gave you the interface. Operations (Module 7) gave you maintainability. Production Readiness (Module 8) gave you confidence. Deposits and Payments (Module 9) gave you product breadth. And Security and Compliance (Module 10) gave you the legal and cryptographic foundation that makes all of the above permissible.

You now have the knowledge to build, operate, and comply a production-grade fintech settlement system. The patterns here -- transactional outbox, dual-backend ledger, in-memory reservation, event-driven workers, per-tenant encryption, crypto-shred -- are not specific to Settla. They appear in every serious financial infrastructure system. The regulations are not going away. The scale requirements are only increasing. The engineering challenge is permanent.

Build systems that are correct by construction, not correct by accident.

---

## Further Reading

- [FATF Recommendations](https://www.fatf-gafi.org/en/recommendations.html) -- the global AML/CFT standards
- [MiCA Full Text (Regulation (EU) 2023/1114)](https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32023R1114) -- the EU crypto-asset regulation
- [FCA Crypto Registration](https://www.fca.org.uk/firms/cryptoassets) -- UK registration requirements
- [FinCEN MSB Registration](https://www.fincen.gov/money-services-business-msb-registration) -- US federal registration
- [FATF Travel Rule Guidance](https://www.fatf-gafi.org/en/publications/fatfrecommendations/documents/guidance-rba-virtual-assets-2021.html) -- updated guidance for VASPs
- [GDPR Full Text](https://gdpr-info.eu/) -- the EU data protection regulation
- `domain/crypto.go` -- PII encryption and crypto-shred implementation
- `domain/crypto_shred.go` -- CryptoShredder service
- `domain/tenant.go` -- KYB status, fee schedules, and tenant activation
- `domain/transfer.go` -- Sender/Recipient structs and Travel Rule data model
- `core/engine.go` -- Pre-transfer validation (tenant activation, pending limits)
- `rail/router/router.go` -- Per-corridor stablecoin and provider selection
