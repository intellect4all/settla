# Settla Regulatory Compliance Architecture

## Overview

Settla operates as infrastructure for Virtual Asset Service Providers (VASPs). While Settla itself may not hold a VASP license, it must enable tenant fintechs to meet their regulatory obligations. This document covers how Settla's architecture satisfies common VASP regulatory requirements.

---

## Regulatory Framework

### Applicable Regulations

| Regulation | Jurisdiction | Relevance |
|-----------|-------------|-----------|
| FATF Travel Rule | Global | Originator/beneficiary information must accompany transfers > $1,000 |
| MiCA (Markets in Crypto-Assets) | EU | VASP registration, capital requirements, consumer protection |
| UK FCA (Financial Conduct Authority) | UK | Crypto-asset registration, AML/KYC obligations |
| FinCEN (BSA/AML) | US | MSB registration, suspicious activity reporting |
| AMLD6 (Anti-Money Laundering Directive) | EU | Enhanced due diligence, beneficial ownership |
| GDPR | EU | Data protection, right to erasure (balanced against retention requirements) |
| Local VASP regulations | Per-country | Vary by corridor (Nigeria, Ghana, UK, EU) |

---

## Audit Trail (Immutable Ledger)

### Requirements

Regulators require a complete, immutable audit trail of all financial transactions. Specific requirements:

- **7-year retention** for all transaction records
- **Immutable records** -- no modification or deletion of completed transaction data
- **Chronological ordering** -- transactions must be sequenceable by time
- **Balanced entries** -- every debit has a corresponding credit (double-entry bookkeeping)

### How Settla Satisfies This

**TigerBeetle (write authority):**
- Append-only ledger engine; entries cannot be modified or deleted after creation
- Every entry is balanced at the engine level (sum of debits = sum of credits)
- Entries are timestamped and sequenced with monotonically increasing IDs
- 3-node cluster with synchronous replication (no silent data loss)

**PostgreSQL (read model, CQRS):**
- `journal_entries` and `entry_lines` tables are insert-only (no UPDATE or DELETE permissions granted to application user)
- All tables use monthly partitions (created 6 months ahead), enabling lifecycle management
- Partitions are never dropped; old partitions are detached and archived

**Corrections are additive:**
- Errors are corrected by issuing reversing entries, never by modifying or deleting existing entries
- Reversing entries reference the original entry via `reference_id`
- This preserves the complete history for auditors

---

## Data Retention Lifecycle

### Active Data (0--90 days)

- Stored in PostgreSQL (hot storage, SSD-backed)
- Indexed for fast query access
- Full PII available (encrypted at rest)

### Warm Archive (90 days -- 2 years)

- PostgreSQL partitions detached from active tables
- Moved to cheaper storage (gp2 EBS or S3-backed PostgreSQL)
- PII fields remain encrypted; access requires elevated permissions
- Queryable on demand (reattach partition or query archive database)

### Cold Archive (2 years -- 7 years)

- Monthly partition data exported to Parquet format
- Stored in S3 Glacier Deep Archive
- PII encrypted with separate archive encryption key (KMS)
- Retrieval time: 12--48 hours (Glacier restore)
- Retained for full 7 years per VASP requirements

### Deletion (After 7 years)

- S3 lifecycle policy automatically deletes objects older than 7 years
- Deletion is logged and auditable
- GDPR erasure requests during the retention period are handled by encrypting PII with a per-tenant key; deleting the key effectively erases the PII while preserving the financial record

### Partition Lifecycle Implementation

```sql
-- Monthly partitions created 6 months ahead (automated job)
-- Example: ledger.journal_entries partitioned by created_at

-- Active: current month + 2 previous months (hot queries)
-- Warm: 3-24 months old (detach, move to archive tablespace)
ALTER TABLE ledger.journal_entries DETACH PARTITION journal_entries_2025_01;
ALTER TABLE ledger.journal_entries_archive ATTACH PARTITION journal_entries_2025_01
  FOR VALUES FROM ('2025-01-01') TO ('2025-02-01');

-- Cold: export to Parquet, upload to S3 Glacier
-- Managed by a scheduled job (CronJob in Kubernetes)
```

---

## Travel Rule Compliance

### Requirements (FATF Recommendation 16)

For transfers exceeding the de minimis threshold ($1,000 USD equivalent):
- **Originator information:** Name, account number, address or national ID, institution
- **Beneficiary information:** Name, account number, institution

### How Settla Supports This

- Transfer creation requires `sender` and `recipient` objects with required fields
- PII stored in encrypted JSONB columns in Transfer DB
- Travel Rule data attached to on-chain transactions via provider-specific mechanisms:
  - **TRON/USDT:** Memo field or Shyft/Notabene integration
  - **Ethereum/USDC:** Compliant with TRUST (Travel Rule Universal Solution Technology)
- Settla stores the Travel Rule data; the tenant's compliance team verifies it
- API response includes `travel_rule_status` field: `pending`, `verified`, `flagged`

---

## Suspicious Activity Monitoring

### Requirements

VASPs must monitor for suspicious activity and file Suspicious Activity Reports (SARs) with relevant authorities.

### Settla's Monitoring Capabilities

**Real-time detection (application-level):**

| Pattern | Detection | Action |
|---------|-----------|--------|
| Rapid sequential transfers | Rate limiting (per-tenant, per-sender) | Auto-throttle + alert |
| Unusually large transfers | Configurable per-tenant threshold | Hold for review + alert |
| Structuring (just-below-threshold amounts) | Pattern detection on rolling 24h window | Flag + alert |
| Sanctioned address interaction | Pre-transfer screening against OFAC/EU lists | Block + alert |
| Unusual corridor activity | Deviation from tenant's historical corridor mix | Flag + alert |

**Audit queries (for compliance teams):**

```sql
-- All transfers for a sender in the last 30 days
SELECT * FROM transfers
WHERE tenant_id = $1
  AND sender->>'account' = $2
  AND created_at > NOW() - INTERVAL '30 days'
ORDER BY created_at;

-- Aggregate volumes by sender (structuring detection)
SELECT sender->>'account' AS sender_account,
       COUNT(*) AS transfer_count,
       SUM(amount) AS total_amount
FROM transfers
WHERE tenant_id = $1
  AND created_at > NOW() - INTERVAL '24 hours'
GROUP BY sender->>'account'
HAVING COUNT(*) > 10 OR SUM(amount) > 50000;
```

**Reporting:**
- Per-tenant transaction reports exportable via API (CSV, JSON)
- SAR filing is the tenant's responsibility; Settla provides the data
- Settla retains all data for 7 years to support retrospective investigations

---

## Tenant KYC/KYB Obligations

Settla enforces tenant-level controls but does not perform end-user KYC (that is the tenant's responsibility).

| Control | Enforcement |
|---------|-------------|
| Tenant onboarding | Manual review, KYB verification before API key issuance |
| Per-tenant limits | Configurable daily/monthly volume limits per tenant |
| Per-tenant fee schedules | Negotiated per-tenant, enforced in router (see ADR-011) |
| Tenant suspension | Immediate API key revocation, PgBouncer connection drain |
| Tenant audit | Per-tenant dashboards (Grafana `tenant-health.json`), full transaction export |

---

## GDPR Considerations

### Data Subjects

Settla processes PII on behalf of tenant fintechs. Under GDPR, Settla is a **data processor**; the tenant is the **data controller**.

### Data Processing Agreement (DPA)

Each tenant agreement includes a DPA covering:
- Purpose limitation (settlement processing only)
- Data minimization (only PII required for settlement + Travel Rule)
- Security measures (encryption at rest and in transit, access controls)
- Sub-processor disclosure (AWS, blockchain networks)
- Breach notification (72-hour obligation)

### Right to Erasure vs. Retention

GDPR's right to erasure conflicts with the 7-year financial record retention requirement. Resolution:

1. Financial transaction records are retained for 7 years (legal obligation overrides erasure right, per GDPR Article 17(3)(b))
2. PII within those records is **crypto-shredded**: the per-tenant encryption key for PII fields is deleted, rendering the PII unrecoverable
3. The financial record (amounts, timestamps, account codes, status) remains intact for audit purposes
4. Non-financial PII (contact emails, IP addresses) is deleted upon request

---

## Compliance Reporting

### Available Reports

| Report | Frequency | Contents |
|--------|-----------|----------|
| Transaction volume | Daily/monthly | Per-tenant, per-corridor volumes and counts |
| Settlement report | Daily | Completed settlements with counterparty details |
| Failed transaction report | Daily | Failed transfers with failure reasons |
| Suspicious activity flags | Real-time | Flagged transactions requiring review |
| Balance reconciliation | Daily | TigerBeetle vs PostgreSQL balance comparison |
| Audit trail export | On-demand | Full chronological transaction history for a tenant |

### Regulator Access

- Regulators can request transaction data via the tenant (data controller)
- Settla provides a read-only API endpoint for authorized compliance queries
- All compliance queries are logged with the requesting user and purpose
- Data exports are encrypted and transmitted via secure channel

---

## Architecture Decisions Supporting Compliance

| ADR | Compliance Benefit |
|-----|-------------------|
| ADR-002 (TigerBeetle) | Immutable append-only ledger, engine-level balance enforcement |
| ADR-003 (CQRS dual backend) | Audit-queryable read model in PostgreSQL, separate from write authority |
| ADR-008 (Multi-database) | Bounded context isolation, targeted data retention policies |
| ADR-010 (Decimal math) | No floating-point rounding errors in financial calculations |
| ADR-013 (Monthly partitioning) | Partition lifecycle enables 7-year retention with archive tiering |
| ADR-011 (Per-tenant fees) | Transparent, auditable fee calculation per tenant |
| ADR-012 (HMAC webhooks) | Verifiable event delivery for tenant audit trails |
