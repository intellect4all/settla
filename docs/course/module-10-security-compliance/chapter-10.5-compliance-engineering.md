# Chapter 10.5: Compliance Engineering -- Building for Regulators

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Trace the KYB verification state machine from tenant onboarding through verified status and explain the dual-gate activation pattern
2. Design a transaction monitoring system that integrates with Settla's outbox-based architecture to detect structuring, velocity anomalies, and sanctions violations
3. Explain why Settla's existing architecture (append-only ledger, transfer events, position events, outbox entries) is inherently audit-ready
4. Describe the Suspicious Activity Reporting (SAR) lifecycle including filing deadlines, tipping-off prohibitions, and record-keeping requirements
5. Map every compliance checkpoint in the lifecycle of a transfer, from pre-submission validation through 7-year retention
6. Evaluate tradeoffs between real-time blocking and asynchronous monitoring for transaction surveillance

---

## Why Compliance Is an Engineering Problem

Many engineering teams treat compliance as a checkbox exercise: fill out some paperwork, upload policies, move on. In settlement infrastructure, that approach gets your company shut down.

Regulators do not evaluate your PDF policies. They evaluate your systems. When a financial intelligence unit requests "every transfer above $5,000 for Tenant X between January and March," they expect an answer in hours, not weeks. When they ask "show me the audit trail for this specific transaction," they expect every state transition, every ledger entry, every treasury mutation -- with timestamps, immutable, tamper-evident.

The good news: if you have been following this course, you have already built most of the compliance infrastructure. The transactional outbox, the append-only ledger, the event-sourced treasury -- these are not just engineering patterns. They are audit primitives.

This chapter connects the dots.

---

## KYB Verification Flow

### The Problem: Who Is Your Customer's Customer?

Settla does not serve end consumers. It serves fintechs -- Lemfi, Fincra, Paystack -- who themselves serve end users. This is a B2B relationship, which means the applicable regulation is Know Your Business (KYB), not Know Your Customer (KYC).

KYB is harder than KYC. You are not verifying a person with a passport. You are verifying a corporate entity: its legal existence, its ownership structure, its directors, its source of funds, and its regulatory status. A fintech that appears legitimate on the surface might be a shell company for money laundering. The stakes are existential -- a single unverified tenant processing illicit funds through Settla's rails can trigger enforcement action against Settla itself.

### The State Machine

Settla models KYB verification as a four-state machine defined in `domain/tenant.go`:

```go
// domain/tenant.go

// KYBStatus represents the Know-Your-Business verification state.
type KYBStatus string

const (
    // KYBStatusPending indicates verification is not yet started.
    KYBStatusPending KYBStatus = "PENDING"
    // KYBStatusInReview indicates documents are being reviewed.
    KYBStatusInReview KYBStatus = "IN_REVIEW"
    // KYBStatusVerified indicates the tenant has passed KYB checks.
    KYBStatusVerified KYBStatus = "VERIFIED"
    // KYBStatusRejected indicates the tenant failed KYB checks.
    KYBStatusRejected KYBStatus = "REJECTED"
)
```

The transitions follow a strict progression:

```
                    +----------+
                    |  PENDING |  <-- Tenant created, no docs submitted
                    +----+-----+
                         |
                    Submit KYB documents
                         |
                    +----v-----+
                    | IN_REVIEW|  <-- Compliance team reviewing
                    +----+-----+
                         |
              +----------+----------+
              |                     |
         Approved              Rejected
              |                     |
        +-----v-----+        +-----v-----+
        |  VERIFIED  |        |  REJECTED |
        +------------+        +-----------+
```

When a tenant registers through the portal, the gRPC service initializes them with `KYBStatusPending`:

```go
// api/grpc/portal_auth_service.go

tenant := &domain.Tenant{
    Name:            req.GetCompanyName(),
    Slug:            slug,
    Status:          domain.TenantStatusOnboarding,
    KYBStatus:       domain.KYBStatusPending,
    SettlementModel: domain.SettlementModelPrefunded,
    // ...
}
```

When the tenant submits their KYB documents (company registration, UBO details, bank statements), the status advances to `IN_REVIEW`:

```go
// api/grpc/portal_auth_service.go

if err := s.portalAuthStore.UpdateTenantKYB(
    ctx, tenantID, string(domain.KYBStatusInReview),
); err != nil {
    return nil, status.Error(codes.Internal, "failed to update KYB status")
}
```

An admin then reviews the submission and either approves or rejects:

```go
// api/grpc/portal_auth_service.go  (approve flow)

if err := s.portalAuthStore.UpdateTenantKYB(
    ctx, tenantID, string(domain.KYBStatusVerified),
); err != nil {
    return nil, status.Error(codes.Internal, "failed to update KYB status")
}
```

### Dual-Gate Activation

Here is the critical design decision: KYB verification alone is not sufficient. A tenant must pass **two** independent gates before they can process any transactions:

1. **Tenant Status** must be `ACTIVE` (account setup complete, not suspended)
2. **KYB Status** must be `VERIFIED` (compliance review passed)

This dual-gate check is encoded in a single function:

```go
// domain/tenant.go

// IsActive returns true if the tenant is ACTIVE and KYB VERIFIED.
// Both conditions must be met for a tenant to process transactions.
func (t *Tenant) IsActive() bool {
    return t.Status == TenantStatusActive && t.KYBStatus == KYBStatusVerified
}
```

This function is the compliance gatekeeper for the entire system. Every operation that touches money checks it:

```go
// core/engine.go

func (e *Engine) CreateTransfer(ctx context.Context, tenantID uuid.UUID,
    req CreateTransferRequest) (*domain.Transfer, error) {
    // a. Load tenant, verify active
    tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-core: create transfer: loading tenant %s: %w", tenantID, err)
    }
    if !tenant.IsActive() {
        return nil, domain.ErrTenantSuspended(tenantID.String())
    }
    // ...
}
```

The same check gates `GetQuote`, `GetRoutingOptions`, deposit session creation, bank deposit sessions, and payment link creation. No money moves without both gates passing.

> **Key Insight:** The dual-gate pattern separates operational readiness (Status) from regulatory readiness (KYBStatus). An ops team can suspend a tenant for non-payment (`Status = SUSPENDED`) without touching their KYB status. A compliance team can reject KYB (`KYBStatus = REJECTED`) without affecting the operational record. Two independent concerns, two independent state machines, one unified check.

### Why Two Separate Fields?

A common mistake is to model this as a single status enum: `ONBOARDING -> VERIFIED -> ACTIVE -> SUSPENDED`. This conflates two orthogonal concerns:

```
WRONG: Single status field
+------------+    +----------+    +--------+    +-----------+
| ONBOARDING | -> | VERIFIED | -> | ACTIVE | -> | SUSPENDED |
+------------+    +----------+    +--------+    +-----------+

Problem: If you suspend a VERIFIED tenant, what status do you set?
SUSPENDED. Now when you un-suspend, do they go back to VERIFIED or ACTIVE?
You have lost information.
```

```
RIGHT: Two independent fields (Settla's approach)
Status:    ONBOARDING | ACTIVE | SUSPENDED    (operational lifecycle)
KYBStatus: PENDING | IN_REVIEW | VERIFIED | REJECTED  (compliance lifecycle)

Suspend a verified tenant:  Status=SUSPENDED, KYBStatus=VERIFIED
Un-suspend:                 Status=ACTIVE, KYBStatus=VERIFIED  (no info lost)
Re-verify after annual KYB: Status=ACTIVE, KYBStatus=IN_REVIEW (still operational)
```

### KYB Document Requirements

For each tenant, the compliance team must collect and verify:

**1. Company Registration**
- Certificate of incorporation (or equivalent)
- Articles of association / operating agreement
- Registered address proof (utility bill, bank statement < 3 months old)

**2. Ultimate Beneficial Owner (UBO) Identification**
- Every individual with >25% ownership stake
- Government-issued photo ID for each UBO
- Proof of address for each UBO
- Source of wealth declaration

**3. Financial Documentation**
- Last 6 months of bank statements
- Audited financial statements (if available)
- Proof of regulatory authorization (e.g., money transmitter license, EMI license)

**4. Business Activity**
- Description of business model and target markets
- Expected transaction volumes and corridors
- Website and operational presence verification

### Sanctions and PEP Screening

Every UBO identified during KYB must be screened against:

| List | Maintained By | Scope |
|------|--------------|-------|
| SDN List | OFAC (US Treasury) | Specially Designated Nationals |
| EU Consolidated List | European Commission | EU sanctions targets |
| UN Consolidated List | UN Security Council | International sanctions |
| HMT Sanctions List | UK Treasury | UK-specific sanctions |

**Politically Exposed Persons (PEP) screening** adds another layer. A PEP is anyone who holds or has recently held a prominent public function: heads of state, senior politicians, judicial officials, military officers, senior executives of state-owned enterprises, and their family members and close associates.

PEP status does not automatically disqualify a tenant. It triggers **Enhanced Due Diligence (EDD)**:
- Senior management approval required for onboarding
- Source of funds must be independently verified
- Ongoing transaction monitoring thresholds are lowered
- Annual relationship review (instead of standard re-verification cycle)

### Ongoing Monitoring

KYB is not a one-time event. The regulatory expectation is continuous monitoring:

```
+------------------+     +-----------------+     +------------------+
|  Initial KYB     | --> | Ongoing         | --> | Trigger-Based    |
|  (Onboarding)    |     | Monitoring      |     | Re-Verification  |
+------------------+     +-----------------+     +------------------+
  - Full document set      - Annual review         - Ownership change
  - UBO identification     - Sanctions rescreening - Adverse media hit
  - Sanctions screening    - Volume monitoring     - Regulatory action
  - PEP screening          - Adverse media check   - Unusual activity
```

**Annual re-verification**: All tenants undergo a lightweight review every 12 months. Updated financial statements, re-screening of UBOs against sanctions lists, confirmation that business activity matches the original profile.

**Trigger-based reviews**: Certain events force an immediate re-verification:
- Change in UBO structure (new investor, ownership transfer)
- Adverse media hit on any UBO or the company
- Regulatory action against the tenant in any jurisdiction
- Transaction patterns that deviate significantly from the declared profile
- Tenant requests access to new corridors or significantly higher volumes

---

## Transaction Monitoring

### Why Monitoring Cannot Be an Afterthought

Financial crime does not announce itself. A money launderer using Settla's rails looks exactly like a legitimate fintech -- until you analyze the patterns. Transaction monitoring is the system that turns raw transfer data into suspicious activity alerts.

The regulatory framework is clear: any financial institution that processes payments must have a transaction monitoring program. For Settla, this means every transfer that flows through the engine must be evaluated against a set of detection rules.

### Rule-Based Detection Patterns

Here are the primary patterns a monitoring system must detect:

**1. Structuring (Smurfing)**

Multiple transfers deliberately kept below the $10,000 USD reporting threshold:

```
Tenant: acme-payments
Time Window: 24 hours

Transfer 1:  $9,800 USD  (14:00 UTC)
Transfer 2:  $9,500 USD  (14:15 UTC)
Transfer 3:  $9,900 USD  (14:30 UTC)
Transfer 4:  $9,700 USD  (14:45 UTC)
                          --------
Total:                    $38,900 USD  (4 transfers, all just under $10K)

ALERT: Structuring suspected -- 4 transfers in 24h, all within
       2% of $10,000 threshold. Aggregate exceeds threshold 3.9x.
```

The detection rule: count transfers in a sliding 24-hour window where each amount falls between $8,000 and $9,999. If count exceeds 2 and aggregate exceeds $20,000, flag.

**2. Velocity Anomaly**

A tenant's transfer volume suddenly spikes far above their historical baseline:

```
Tenant: steady-remit
30-day average: 50 transfers/day, $25,000 daily volume

Today:
  Transfers: 312  (6.2x average count)
  Volume:    $890,000  (35.6x average volume)

ALERT: Velocity anomaly -- daily volume exceeds 5x 30-day
       moving average. Manual review required.
```

**3. Round-Trip Detection**

Money sent to a destination and returned within a short window, potentially indicating layering:

```
Transfer A: Tenant X sends $50,000 USD -> NGN (Nigeria)
Transfer B: Tenant X receives $49,200 NGN -> USD (Nigeria) within 48 hours

Net movement: $800 (fees only)
Pattern: Round-trip with near-zero economic purpose

ALERT: Potential round-trip detected. $50,000 sent and $49,200
       returned within 48h. Net movement suggests layering.
```

**4. High-Risk Corridor**

Transfers to or from jurisdictions on the FATF grey list or blacklist:

```
FATF Grey List (as of assessment):
  - Myanmar, Nigeria, South Africa, Syria, Yemen, ...
  (List changes -- must be updated quarterly)

FATF Blacklist:
  - North Korea, Iran, Myanmar (elevated)

Rule: Any transfer involving a blacklisted jurisdiction is
      automatically blocked. Grey-listed jurisdictions trigger
      enhanced monitoring (lower alert thresholds).
```

**5. Amount Anomaly**

A single transfer that is dramatically different from the tenant's historical pattern:

```
Tenant: micro-payments-ltd
Historical profile:
  Average transfer: $500
  95th percentile:  $2,100
  Max ever:         $5,000

New transfer: $185,000

ALERT: Amount anomaly -- transfer is 370x the tenant's average
       and 37x their historical maximum.
```

**6. Sanctioned Blockchain Address**

For crypto-related flows, the destination address must be checked against the OFAC SDN list, which now includes specific cryptocurrency addresses:

```
Destination: TJnV6gKMhTxe2MbXPbe1MkLkS1RijxfCam

OFAC SDN Match: Yes
Entity: Lazarus Group (DPRK)
Listed: 2022-04-14

ACTION: Block transfer immediately. File SAR. Preserve all records.
```

### Where Monitoring Fits in Settla's Architecture

The critical question: where do you insert transaction monitoring without disrupting the engine's zero-side-effect guarantee?

The answer: the monitoring system is a **consumer of outbox events**, not an inline check in the engine. This preserves the engine's purity while ensuring every completed transfer is evaluated:

```
Transfer reaches terminal state (COMPLETED)
    |
    v
Engine writes state + outbox atomically
    |
    v
Outbox Relay publishes to NATS
    |
    v
SETTLA_TRANSFERS stream
    |
    +---> TransferWorker (existing -- saga orchestration)
    |
    +---> TransactionMonitor (new consumer)
              |
              +--- Rule Engine evaluates all detection rules
              |
              +--- Risk Score: LOW / MEDIUM / HIGH / CRITICAL
              |
              |   LOW:      Log, no action
              |   MEDIUM:   Create compliance review ticket
              |   HIGH:     Flag tenant, alert compliance team
              |   CRITICAL: Suspend tenant, create SAR draft
              |
              v
          Compliance Dashboard
              |
              v
          Human Review --> SAR Filing (if warranted)
```

This design has several important properties:

**No impact on transfer latency.** The monitoring system consumes events asynchronously. A legitimate transfer completes in the same time whether or not monitoring is running.

**No false-positive blocking.** Except for sanctions hits (which are pre-transfer checks), the monitoring system flags and alerts rather than blocks. Human compliance officers make the final call on most alerts.

**Complete coverage.** Because the monitoring system reads from the same NATS stream as the saga orchestrator, it sees every transfer event. There is no code path that bypasses monitoring.

**Replay capability.** If a new detection rule is added, it can be replayed against historical transfer events stored in the Transfer DB.

> **Key Insight:** The monitoring system is deliberately positioned as an event consumer, not an inline gate. This follows the same architectural principle as the ledger worker and treasury worker -- side effects are expressed through the outbox, and downstream consumers process them independently. The only exception is sanctions screening, which must block before money moves.

### Pre-Transfer vs Post-Transfer Checks

Not all compliance checks can be deferred to post-transfer monitoring. Some must block:

| Check | When | Action on Match |
|-------|------|----------------|
| Tenant KYB status | Pre-transfer (engine) | Reject transfer |
| Tenant suspended | Pre-transfer (engine) | Reject transfer |
| Per-tenant transfer limits | Pre-transfer (engine) | Reject transfer |
| Sanctions list (recipient) | Pre-transfer (new) | Reject transfer |
| Sanctions list (blockchain address) | Pre-transfer (new) | Reject transfer |
| Corridor restrictions | Pre-transfer (router) | No route available |
| Structuring detection | Post-transfer (monitor) | Alert compliance |
| Velocity anomaly | Post-transfer (monitor) | Alert compliance |
| Round-trip detection | Post-transfer (monitor) | Alert compliance |
| Amount anomaly | Post-transfer (monitor) | Alert compliance |

The pre-transfer checks are deterministic and fast: a sanctions list lookup against a local copy of the SDN list takes microseconds. The post-transfer checks require historical context (30-day averages, sliding windows) and produce probabilistic results that need human judgment.

---

## Audit Trail Architecture

### Why Settla Is Inherently Audit-Ready

If you have followed this course from Module 1, you have already built four independent audit trails without knowing it. Each captures a different dimension of system activity:

```
+-------------------+    +-------------------+    +-------------------+
| TRANSFER EVENTS   |    | LEDGER ENTRIES    |    | POSITION EVENTS   |
| (Transfer DB)     |    | (TigerBeetle +   |    | (Treasury DB)     |
|                   |    |  Postgres read)   |    |                   |
| Every state       |    | Every debit and   |    | Every reserve,    |
| transition with   |    | credit with       |    | release, commit   |
| timestamp and     |    | balanced postings |    | with balance-     |
| version number    |    | (append-only)     |    | after snapshot     |
+-------------------+    +-------------------+    +-------------------+
         |                        |                        |
         +------------------------+------------------------+
                                  |
                        +---------v---------+
                        | OUTBOX ENTRIES     |
                        | (Transfer DB)      |
                        |                    |
                        | Every intent and   |
                        | event the system   |
                        | decided to publish |
                        | (system decision   |
                        | log)               |
                        +--------------------+
```

**1. Transfer Events -- The State Transition Log**

Every time a transfer changes state, a `TransferEvent` is written atomically with the state change:

```go
// domain/transfer.go

// TransferEvent records a state change on a transfer for audit and event sourcing.
type TransferEvent struct {
    ID          uuid.UUID
    TransferID  uuid.UUID
    TenantID    uuid.UUID
    FromStatus  TransferStatus
    ToStatus    TransferStatus
    Timestamp   time.Time
    // ...
}
```

This gives regulators a complete chronological record of every transfer's lifecycle. They can reconstruct exactly what happened: when the transfer was created, when funds were reserved, when the on-ramp executed, when settlement completed -- or where it failed and what compensation was applied.

**2. Ledger Entries -- The Financial Record**

The TigerBeetle ledger is append-only by design. Entries are never updated or deleted. Every debit has a matching credit. The Postgres read-side mirrors this with `journal_entries` and `entry_lines` tables.

This means the financial record is inherently tamper-evident. You cannot silently modify a past entry -- you can only add a reversal entry that references the original (see Chapter 2.5 on ledger reversals). An auditor can independently verify that every entry balances and that the sum of all entries equals the current account balances.

**3. Position Events -- The Treasury Audit Log**

The position events table (introduced in Module 4) records every treasury position mutation:

```go
// domain/position_event.go

type PositionEvent struct {
    ID             uuid.UUID
    PositionID     uuid.UUID
    TenantID       uuid.UUID
    EventType      PositionEventType  // CREDIT, DEBIT, RESERVE, RELEASE, COMMIT, CONSUME
    Amount         decimal.Decimal
    BalanceAfter   decimal.Decimal
    LockedAfter    decimal.Decimal
    ReferenceID    uuid.UUID
    ReferenceType  string             // "transfer", "deposit_session", "compensation", ...
    IdempotencyKey string
    RecordedAt     time.Time
}
```

Every `RESERVE`, `RELEASE`, `COMMIT`, and `CONSUME` operation is recorded with the resulting balance. This means you can reconstruct the exact treasury position at any point in time by replaying events up to that timestamp. For a regulator asking "what was Lemfi's available balance at 14:32 UTC on March 15th?" -- you have the answer.

**4. Outbox Entries -- The System Decision Log**

This is the most underappreciated audit artifact. The outbox records not just what happened, but what the system *decided to do*. Every intent (provider.execute_onramp, blockchain.send_stablecoin, treasury.reserve) and every event (transfer.funded, transfer.completed) is persisted before it is acted upon.

If a regulator asks "why did the system send $50,000 to this blockchain address?" -- the outbox entry proves the system made that decision as part of processing transfer `abc-123`, at a specific timestamp, in response to a specific state transition.

### Retention Policy

Regulatory requirements demand long retention periods for financial records:

```
+-----------------------+------------------+---------------------------+
|  Data Type            |  Retention       |  Implementation           |
+-----------------------+------------------+---------------------------+
|  Transfer records     |  7 years         |  Monthly partitions,      |
|  + transfer events    |                  |  never auto-dropped       |
+-----------------------+------------------+---------------------------+
|  Ledger entries       |  7 years         |  Monthly partitions,      |
|  (journal + lines)    |                  |  never auto-dropped       |
+-----------------------+------------------+---------------------------+
|  Position events      |  90 days (hot)   |  Monthly partitions,      |
|                       |  7 years (cold)  |  archived to S3 Glacier   |
+-----------------------+------------------+---------------------------+
|  Outbox entries       |  90 days         |  Monthly partitions,      |
|                       |                  |  old partitions dropped   |
+-----------------------+------------------+---------------------------+
|  PII (sender/recip)   |  90 days (hot)   |  Encrypted JSONB,         |
|                       |  7 years (cold)  |  archived to S3 Glacier   |
+-----------------------+------------------+---------------------------+
|  SAR filing records   |  10 years        |  Separate encrypted       |
|                       |                  |  storage, restricted ACL  |
+-----------------------+------------------+---------------------------+
```

The partition manager (Chapter 7.5) creates monthly partitions 6 months ahead and drops old partitions for operational data like outbox entries. But financial records -- transfers, ledger entries, events -- are **never** auto-dropped. The partition manager skips these tables entirely. Archival to cold storage is a separate, manual process with compliance team approval.

### Compliance Query Patterns

When regulators request data, the queries typically follow predictable patterns. Settla's index strategy is designed for these:

**"All transfers for Tenant X in date range Y":**

```sql
SELECT t.*, array_agg(e.*) as events
FROM transfers t
LEFT JOIN transfer_events e ON e.transfer_id = t.id
WHERE t.tenant_id = $1
  AND t.created_at BETWEEN $2 AND $3
ORDER BY t.created_at;

-- Uses: idx_transfers_tenant_created (tenant_id, created_at)
```

**"Complete audit trail for transfer Z":**

```sql
-- Transfer record
SELECT * FROM transfers WHERE id = $1 AND tenant_id = $2;

-- State transitions
SELECT * FROM transfer_events WHERE transfer_id = $1 ORDER BY timestamp;

-- Ledger entries
SELECT je.*, el.*
FROM journal_entries je
JOIN entry_lines el ON el.journal_entry_id = je.id
WHERE je.reference_id = $1::text
ORDER BY je.created_at;

-- Treasury mutations
SELECT * FROM position_events
WHERE reference_id = $1 AND reference_type = 'transfer'
ORDER BY recorded_at;
```

**"Daily volume per tenant for the quarter":**

```sql
SELECT tenant_id, DATE(created_at) as day,
       COUNT(*) as transfer_count,
       SUM(source_amount) as total_volume
FROM transfers
WHERE created_at BETWEEN $1 AND $2
  AND status = 'COMPLETED'
GROUP BY tenant_id, DATE(created_at)
ORDER BY tenant_id, day;
```

These queries are fast because the data model was designed with tenant isolation and time-range partitioning from the start. The `WHERE tenant_id = $1` clause is not just a filter -- it is a fundamental invariant (Critical Invariant #7) that every SQLC-generated query enforces.

---

## Suspicious Activity Reporting (SAR)

### When to File

A SAR must be filed when the compliance team has reason to believe that a transaction:

- Involves funds derived from illegal activity
- Is designed to evade reporting requirements (structuring)
- Has no apparent lawful purpose and no reasonable explanation
- Involves a sanctioned entity or jurisdiction
- Facilitates criminal activity (fraud, terrorist financing, tax evasion)

The decision to file is always made by a human compliance officer, not by the automated monitoring system. The monitoring system generates alerts; humans evaluate them.

### Filing Deadlines

Deadlines vary by jurisdiction but follow a common pattern:

| Jurisdiction | Filing Body | Deadline | Form |
|-------------|-------------|----------|------|
| United States | FinCEN | 30 days from detection | FinCEN SAR (BSA filing) |
| United Kingdom | NCA | "As soon as practicable" | Defense Against ML (DAML) SAR |
| European Union | National FIU | Varies by member state (typically "without delay") | National SAR form |
| Nigeria | NFIU | Immediately for terrorism; 7 days for ML | STR form |

"Detection" means the moment a compliance officer determines that the activity is suspicious -- not when the automated alert fired. If an alert fires on Day 1 and the compliance officer reviews it on Day 5 and confirms suspicion, the 30-day clock starts on Day 5.

### What a SAR Contains

A SAR must answer five questions:

```
+-------+----------------------------------------------------------+
| WHO   | Subject(s) of the report:                                |
|       |   - Tenant name, registration number, jurisdiction       |
|       |   - UBO names, dates of birth, nationalities             |
|       |   - Account/API key identifiers                          |
+-------+----------------------------------------------------------+
| WHAT  | Nature of the suspicious activity:                       |
|       |   - Transaction amounts, currencies, corridors            |
|       |   - Transfer IDs and timestamps                          |
|       |   - Pattern description (structuring, velocity, etc.)    |
+-------+----------------------------------------------------------+
| WHEN  | Timeline:                                                |
|       |   - Date range of suspicious activity                    |
|       |   - Date of detection                                    |
|       |   - Date of initial alert                                |
+-------+----------------------------------------------------------+
| WHERE | Geographic information:                                   |
|       |   - Source and destination countries                      |
|       |   - Blockchain networks and addresses involved            |
|       |   - Bank details (if applicable)                         |
+-------+----------------------------------------------------------+
| WHY   | Why the activity is suspicious:                           |
|       |   - Which detection rules triggered                      |
|       |   - What makes this inconsistent with tenant profile     |
|       |   - What explanation (if any) the tenant provided        |
|       |   - Compliance officer's assessment                      |
+-------+----------------------------------------------------------+
```

### The Tipping-Off Prohibition

This is the single most important rule in SAR filing, and getting it wrong is a criminal offense in most jurisdictions:

**You must NEVER inform the subject of a SAR that a report has been or is being filed.**

This means:
- No email to the tenant saying "we filed a suspicious activity report about you"
- No log entry visible to the tenant indicating a SAR was filed
- No status change on the tenant's account that reveals the investigation
- No communication that could reasonably be interpreted as a tip-off
- Customer service staff must not reveal the existence of a SAR if the tenant calls to ask why their account was reviewed

If a tenant's account is suspended as a result of a SAR, the reason given must be generic: "Your account has been suspended pending review." Not "Your account has been suspended due to suspected structuring activity."

> **Key Insight:** The tipping-off prohibition has architectural implications. SAR records must be stored in a separate system from the main application database, with restricted access controls. The compliance dashboard must not expose SAR-related data to any API endpoint that a tenant could access. Even internal logging must be careful -- if a tenant requests their data under GDPR, SAR records are explicitly exempt from data subject access requests.

### SAR Record Keeping

SAR filing records must be retained for a minimum of 5 years (US) to 10 years (UK), separate from the main transaction database:

```
+--------------------+     +----------------------+
| Main Application   |     | SAR System           |
| Database           |     | (Separate, Encrypted)|
|                    |     |                      |
| - Transfers        |     | - SAR drafts         |
| - Events           |     | - Filing records     |
| - Ledger entries   |     | - Supporting docs    |
| - Position events  |     | - Analyst notes      |
|                    |     | - Filing receipts    |
| Accessible to:     |     |                      |
| - Engineering      |     | Accessible to:       |
| - Support          |     | - MLRO only          |
| - Analytics        |     | - Compliance team    |
+--------------------+     | - External auditors  |
                           +----------------------+
```

The Money Laundering Reporting Officer (MLRO) is typically the only person authorized to file SARs and access the SAR system. Even the CEO may not have access, depending on the organization's compliance structure.

---

## The Compliance Lifecycle of a Transfer

Let us trace a complete transfer through every compliance checkpoint, showing exactly where in Settla's architecture each check occurs:

```
PRE-TRANSFER CHECKS
====================

  1. API Authentication
     +-- Gateway auth plugin extracts tenant_id from API key
     +-- Key is HMAC-SHA256 hashed and resolved via 3-level cache
     +-- Tenant can never specify their own tenant_id

  2. KYB Gate (core/engine.go)
     +-- tenant.IsActive() checks:
     |     Status == ACTIVE  (operational gate)
     |     KYBStatus == VERIFIED  (compliance gate)
     +-- Rejected if either gate fails

  3. Transfer Limits (core/engine.go)
     +-- PerTransferLimit: single transfer cannot exceed tenant's limit
     +-- DailyLimitUSD: tenant's 24-hour rolling volume cannot be exceeded
     +-- MaxPendingTransfers: concurrent non-terminal transfer cap

  4. Sanctions Screening (pre-transfer, blocking)
     +-- Recipient details checked against OFAC SDN, EU, UN lists
     +-- Blockchain destination address checked against OFAC crypto list
     +-- Match = transfer rejected, compliance alerted

  5. Corridor Validation (rail/router)
     +-- Source/destination currency pair must have available route
     +-- Restricted corridors return no routing options


DURING TRANSFER
===============

  6. State Machine Enforcement (domain/transfer.go)
     +-- Every transition validated against ValidTransitions map
     +-- TransferEvent written atomically with state change
     +-- Version counter incremented (optimistic concurrency)

  7. Ledger Recording (ledger/)
     +-- Every debit and credit written to TigerBeetle (append-only)
     +-- Balanced postings enforced at engine level
     +-- Postgres read model populated via CQRS sync

  8. Treasury Tracking (treasury/)
     +-- Reserve, commit, consume recorded as PositionEvents
     +-- BalanceAfter and LockedAfter captured at each mutation
     +-- Full reconstruction possible from event log

  9. Outbox Decisions (core/engine.go)
     +-- Every intent written atomically with state change
     +-- System decision log: what was decided, when, why


POST-TRANSFER
=============

  10. Transaction Monitoring (async, non-blocking)
      +-- transfer.completed event consumed by monitoring system
      +-- Evaluated against all detection rules
      +-- Alerts generated for compliance review

  11. Audit Trail Complete
      +-- Transfer record + events: full state history
      +-- Ledger entries: financial proof
      +-- Position events: treasury mutations
      +-- Outbox entries: system decisions

  12. Retention
      +-- Financial records: 7-year minimum
      +-- Available for regulatory investigation at any time
      +-- Partitioned for efficient time-range queries
```

### A Concrete Example

Follow a single $50,000 GBP-to-NGN transfer through the compliance checkpoints:

```
14:00:00.000  API request arrives at gateway
14:00:00.001  Auth plugin: sk_live_abc... -> HMAC -> tenant=lemfi (ID: a000...0001)
14:00:00.002  gRPC call to settla-server CreateTransfer
14:00:00.003  Engine: GetTenant(a000...0001) -> Lemfi
14:00:00.003  Engine: tenant.IsActive() -> true (Status=ACTIVE, KYB=VERIFIED)
14:00:00.004  Engine: $50,000 < PerTransferLimit($100,000) -> pass
14:00:00.004  Engine: daily volume $1.2M + $50K < DailyLimit($5M) -> pass
14:00:00.005  Engine: pending transfers 12 < MaxPending(1000) -> pass
14:00:00.006  Router: GBP->NGN corridor available, 3 providers scored
14:00:00.007  Engine: fee = 40bps = $200 -> non-zero -> pass
14:00:00.008  DB TX: INSERT transfer (CREATED) + INSERT transfer_event + INSERT outbox
14:00:00.008  Response: transfer_id = xyz-789, status = CREATED

14:00:00.028  Outbox relay: publishes transfer.initiated to NATS
14:00:00.030  TransferWorker: receives, publishes treasury.reserve intent
14:00:00.032  TreasuryWorker: reserves $50,200, writes PositionEvent(RESERVE)
14:00:00.033  Engine: HandleTreasuryResult -> CREATED->FUNDED + outbox(provider.onramp)

14:00:00.050  ProviderWorker: executes on-ramp with Paystack
14:00:01.200  ProviderWorker: on-ramp confirmed
14:00:01.201  Engine: HandleOnRampResult -> FUNDED->ON_RAMPING->SETTLING

14:00:01.220  BlockchainWorker: sends USDT on Tron
14:00:12.000  BlockchainWorker: 19 confirmations, settlement confirmed
14:00:12.001  Engine: HandleBlockchainResult -> SETTLING->OFF_RAMPING

14:00:12.050  ProviderWorker: executes off-ramp
14:00:13.500  ProviderWorker: off-ramp confirmed, NGN delivered
14:00:13.501  Engine: HandleOffRampResult -> OFF_RAMPING->COMPLETED

14:00:13.501  At this point, the audit trail contains:
              - 1 transfer record (7 versions)
              - 7 transfer events (CREATED->FUNDED->ON_RAMPING->...->COMPLETED)
              - 4 ledger journal entries (8 entry lines, all balanced)
              - 3 position events (RESERVE, COMMIT, CONSUME)
              - 7 outbox entries (every decision the system made)

14:00:13.520  Transaction monitor consumes transfer.completed event
14:00:13.521  Rule evaluation:
              - Structuring: NO (single transfer, not near threshold)
              - Velocity: NO (within 30-day average)
              - Amount: NO (within tenant profile for Lemfi)
              - Sanctions: NO (recipient not on any list)
              - Corridor: NO (GBP-NGN is standard for Lemfi)
14:00:13.522  Risk score: LOW -> log only, no action
```

Every timestamp, every decision, every financial mutation is recorded. Seven years from now, a regulator can reconstruct this exact sequence.

---

## Common Mistakes

**1. Treating KYB as a single boolean flag.**

```go
// WRONG: Single field loses information on state changes
type Tenant struct {
    KYBVerified bool  // What happens when you need to re-verify?
}

// RIGHT: State machine with independent lifecycle
type Tenant struct {
    Status    TenantStatus  // Operational state
    KYBStatus KYBStatus     // Compliance state
}
```

A boolean cannot represent "was verified, now under re-review." A state machine can.

**2. Blocking transfers inline on probabilistic monitoring rules.**

```
WRONG: Check velocity anomaly inside Engine.CreateTransfer
  - Adds 50-100ms latency to every transfer
  - False positives block legitimate business
  - Rule tuning requires engine redeployment

RIGHT: Evaluate probabilistic rules asynchronously via event consumer
  - Zero latency impact on transfers
  - Alerts go to humans, not automated blocks
  - Rules can be tuned without touching the engine
```

Sanctions screening (deterministic, high-confidence) should block inline. Behavioral analytics (probabilistic, context-dependent) should alert asynchronously.

**3. Storing SAR records in the main application database.**

If SAR records are in the same database as tenant data, any engineer with database access can see which tenants are under investigation. This violates the tipping-off prohibition and creates a regulatory compliance failure. SAR data must live in a separate, access-restricted system.

**4. Logging the reason for a compliance-related suspension.**

```go
// WRONG: Reveals investigation details in application logs
slog.Warn("suspending tenant",
    "tenant_id", tenantID,
    "reason", "structuring detected in 14 transfers totaling $138K")

// RIGHT: Generic log entry
slog.Warn("tenant suspended",
    "tenant_id", tenantID,
    "reason", "compliance_review")
```

Application logs are accessible to engineering teams. Investigation details belong only in the SAR system.

**5. Forgetting that KYB is ongoing.**

A tenant verified 18 months ago may have changed ownership, acquired new UBOs, or been listed on a sanctions update last week. Without periodic re-screening and trigger-based reviews, your initial KYB is stale and your compliance program is deficient.

**6. Implementing transaction limits without tenant scoping.**

```sql
-- WRONG: Global transfer count (all tenants mixed)
SELECT COUNT(*) FROM transfers WHERE status = 'CREATED';

-- RIGHT: Per-tenant transfer count (tenant isolation preserved)
SELECT COUNT(*) FROM transfers
WHERE tenant_id = $1 AND status NOT IN ('COMPLETED', 'FAILED', 'REFUNDED');
```

A global limit means one tenant's volume affects another tenant's ability to transact. Limits must always be per-tenant.

---

## Exercises

### Exercise 1: Design the KYB Re-Verification Trigger

The compliance team wants to automatically trigger a KYB re-review when a tenant's monthly volume exceeds 200% of their declared expected volume (captured during initial KYB).

Design the system:
- Where is the "expected monthly volume" stored? (Hint: tenant metadata or a dedicated compliance field)
- How do you calculate actual monthly volume efficiently? (Hint: analytics snapshots from Chapter 7.1)
- What happens to the tenant during re-review? Should they be blocked?
- Write the SQL query that identifies all tenants whose current month volume exceeds 2x their declared volume

**Acceptance criteria:** The tenant should NOT be blocked during re-review (that would disrupt legitimate business growth). Instead, a compliance ticket is created and the tenant's monitoring thresholds are temporarily lowered.

### Exercise 2: Structuring Detection Rule

Implement the structuring detection rule as a SQL query:

Given the transfers table with columns `(id, tenant_id, source_amount, source_currency, created_at, status)`, write a query that finds all tenants with 3 or more COMPLETED transfers in any 24-hour window where:
- Each transfer amount is between $8,000 and $9,999 USD
- The aggregate amount exceeds $20,000

Test your query against this data:

| tenant_id | amount | created_at |
|-----------|--------|------------|
| T1 | $9,500 | 2026-03-28 10:00 |
| T1 | $9,800 | 2026-03-28 14:00 |
| T1 | $9,200 | 2026-03-28 22:00 |
| T1 | $9,900 | 2026-03-29 09:00 |
| T2 | $9,000 | 2026-03-28 10:00 |
| T2 | $9,000 | 2026-03-28 11:00 |

**Expected result:** T1 should be flagged (3 transfers within 24h totaling $28,500). T2 should NOT be flagged (only 2 transfers, aggregate $18,000 < $20,000).

### Exercise 3: Audit Trail Reconstruction

A regulator requests the complete audit trail for transfer `f47ac10b-58cc-4372-a567-0e02b2c3d479` belonging to tenant `a0000000-0000-0000-0000-000000000001` (Lemfi).

Write the set of SQL queries (against Transfer DB, Ledger DB, and Treasury DB) that would produce:
1. The transfer record with all field values
2. Every state transition event in chronological order
3. Every ledger journal entry and its constituent entry lines
4. Every treasury position event related to this transfer

For each query, identify which index supports it and estimate the query time given 50M transfers/day and 7 years of retention (~127 billion transfer records).

### Exercise 4: Tipping-Off Audit

Review the following hypothetical code and identify all tipping-off violations:

```go
func (s *Server) GetTenantStatus(ctx context.Context, tenantID uuid.UUID) (*TenantStatusResponse, error) {
    tenant, err := s.store.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, err
    }

    resp := &TenantStatusResponse{
        Status:    string(tenant.Status),
        KYBStatus: string(tenant.KYBStatus),
    }

    // Check if tenant has any active SARs
    sarCount, err := s.sarStore.CountActiveSARs(ctx, tenantID)
    if err != nil {
        return nil, err
    }
    if sarCount > 0 {
        resp.ComplianceNote = "Account under enhanced monitoring"
        resp.SARCount = sarCount
    }

    return resp, nil
}
```

List every violation and explain the fix.

---

## Summary

Compliance engineering is not a separate discipline bolted onto the system after it is built. It is a property that emerges from sound architectural decisions:

- **Append-only ledger** = tamper-evident financial record
- **Transfer events** = complete state transition history
- **Position events** = event-sourced treasury audit
- **Outbox entries** = system decision log
- **Dual-gate activation** = regulatory readiness enforced before any money moves
- **Tenant isolation** = per-tenant data scoping makes regulatory queries efficient
- **Partitioned storage** = 7-year retention without performance degradation

The key principle: **make the compliant path the easy path.** If the architecture naturally produces audit trails, immutable records, and tenant-scoped data, compliance is not extra work -- it is a side effect of building the system correctly.

---

## Further Reading

- FATF Recommendations (particularly Recommendations 10-21 on customer due diligence and suspicious transaction reporting)
- FinCEN BSA/AML Examination Manual
- Chapter 1.5 (Multi-Tenancy) for tenant isolation enforcement
- Chapter 2.5 (Ledger Reversals) for how corrections preserve audit integrity
- Chapter 7.5 (Database Maintenance) for partition retention policies
- `domain/tenant.go` for the KYB state machine and IsActive implementation
- `domain/position_event.go` for the treasury audit event schema
- `docs/security.md` for the complete security architecture reference
