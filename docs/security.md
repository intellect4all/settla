# Settla Security Architecture

## Overview

Settla processes B2B stablecoin settlements for fintechs at scale (50M transactions/day). This document covers the security architecture, threat model, and operational security controls.

---

## API Security

### Authentication

All API access requires a bearer token issued per-tenant.

```
Authorization: Bearer sk_live_<random-32-bytes-base64>
```

**Token handling:**
1. API key is SHA-256 hashed before storage in the Transfer DB (`api_keys` table)
2. Raw key is shown once at creation, never stored or logged
3. Hash lookup resolves to `tenant_id`, which scopes all subsequent operations
4. Key rotation: tenants can have multiple active keys; old keys are revoked, not deleted (audit trail)

**Auth cache (three-level, for 5,000 lookups/sec at peak):**

| Level | Store | TTL | Latency | Purpose |
|-------|-------|-----|---------|---------|
| L1 | Local in-process LRU | 30 seconds | ~100ns | Eliminates network round-trips |
| L2 | Redis | 5 minutes | ~0.5ms | Shared across gateway replicas |
| L3 | PostgreSQL (Transfer DB) | Source of truth | ~2ms | Authoritative lookup |

Cache invalidation on key revocation propagates via Redis pub/sub to all gateway instances.

### Rate Limiting

Rate limiting is enforced at two layers:

1. **Tyk API Gateway** (external edge): Global rate limits per API key, configured in `deploy/tyk/apps/` and `deploy/tyk/policies/policies.json`
2. **Application-level** (per-tenant): Sliding window counters in the `cache` module, local counters synced to Redis every 5 seconds

Default limits per tenant:
- Quotes: 1,000 requests/minute
- Transfers: 500 requests/minute
- Treasury reads: 2,000 requests/minute

### Idempotency

Every mutation endpoint requires an `Idempotency-Key` header.

- Keys are scoped per-tenant: `UNIQUE(tenant_id, idempotency_key)` in the Transfer DB
- TTL: 24 hours (after which the key can be reused)
- Duplicate requests within TTL return the cached response, no re-execution
- Idempotency state cached in Redis (L2) with DB as authoritative store

---

## Encryption

### At Rest

| Data | Encryption | Details |
|------|-----------|---------|
| EBS volumes (all databases) | AES-256 | AWS-managed keys (SSE-EBS) |
| TigerBeetle data files | AES-256 | EBS volume encryption; TB does not support native encryption |
| PostgreSQL data | AES-256 | EBS volume encryption + column-level encryption for PII |
| S3 backups | AES-256 | SSE-S3 with bucket policy enforcing encryption |
| Redis | N/A | Cache-only, no persistent sensitive data; ElastiCache encryption at rest enabled |

**Column-level encryption for PII:**
- Sender/recipient details stored in JSONB columns are encrypted using AES-256-GCM with a per-tenant data encryption key (DEK)
- DEKs are envelope-encrypted with a master key stored in AWS KMS
- Encrypted fields: `sender_name`, `sender_account`, `recipient_name`, `recipient_account`, `recipient_bank`

### In Transit

| Path | Protocol | Details |
|------|----------|---------|
| Client to Tyk Gateway | TLS 1.3 | AWS ALB terminates TLS, minimum TLS 1.2 enforced |
| Tyk to Fastify Gateway | TLS 1.2+ | Internal ALB or service mesh |
| Gateway to settla-server (gRPC) | Plain TCP | NetworkPolicy isolation is the current control; no mTLS |
| settla-server to PostgreSQL | TLS (production) / Plain (dev) | PgBouncer `sslmode=verify-full` in production, `sslmode=disable` in dev |
| settla-server to TigerBeetle | Plain TCP | TigerBeetle does not support TLS; isolated in private subnet, network policy restricts access |
| settla-server to Redis | TLS (production) / Plain (dev) | ElastiCache in-transit encryption in production; password-authenticated in dev |
| settla-server to NATS | TLS (production) / Plain (dev) | NATS TLS with client certificates in production |
| Inter-pod (Kubernetes) | Plain TCP | NetworkPolicy isolation is the current control; no service mesh mTLS deployed |

---

## PII Handling

### What Constitutes PII

- Sender name, account number, bank details
- Recipient name, account number, bank details
- IP addresses of API callers
- Tenant admin email addresses

### Storage

- PII fields stored in encrypted JSONB columns in Transfer DB
- PII is never stored in TigerBeetle (ledger entries reference transfer IDs, not personal data)
- PII is never stored in Redis or NATS messages

### Logging

- PII is masked in all application logs
- Account numbers: show last 4 digits only (`****4567`)
- Names: first character + asterisks (`J***`)
- slog (Go) and pino (TS) structured loggers enforce masking through custom formatters
- Log aggregation (CloudWatch/ELK) access is restricted to SRE team

### Retention

- Active PII retained for transaction lifecycle + 90 days
- Archived PII encrypted and moved to S3 Glacier after 90 days
- Full deletion after 7 years (regulatory requirement)
- See `docs/compliance.md` for data retention lifecycle

---

## Key Management

### Blockchain Private Keys

- Private keys for on-chain settlement are AES-256-GCM encrypted before storage
- Encryption keys are stored in AWS KMS (HSM-backed)
- Key hierarchy: KMS master key > DEK (data encryption key) > encrypted private key
- Private keys are never:
  - Logged (any log level)
  - Transmitted in API responses
  - Stored in environment variables
  - Included in error messages or stack traces
- Key usage is audited: every signing operation logs `tenant_id`, `transfer_id`, `chain`, `timestamp` (but never the key material)

### API Key Management

- Raw API keys generated using cryptographically secure random bytes (32 bytes, base64-encoded)
- Only the SHA-256 hash is stored; raw key shown once at creation
- Key prefixes (`sk_live_`, `sk_test_`) indicate environment
- Rotation: create new key, migrate traffic, revoke old key (no hard cut-over)

### TLS Certificate Management

- External certificates managed via AWS Certificate Manager (ACM)
- Internal mTLS certificates issued by a private CA (AWS Private CA or cert-manager in Kubernetes)
- Certificate rotation automated, 90-day validity for internal certs
- Certificate pinning not used (managed rotation incompatible)

---

## Access Control

### Tenant Isolation

Tenant isolation is a critical invariant enforced at every layer:

| Layer | Enforcement |
|-------|-------------|
| API Gateway | `tenant_id` extracted from authenticated API key, never from request body |
| gRPC service | `tenant_id` passed in gRPC metadata, validated against auth context |
| Database queries | All SQLC-generated queries include `WHERE tenant_id = $1` |
| TigerBeetle | Account IDs encode tenant ownership; cross-tenant transfers rejected by domain logic |
| NATS partitioning | Partition key is hash of `tenant_id`; no cross-tenant event leakage |
| Treasury | In-memory positions keyed by `(tenant_id, currency)`; isolated atomic operations |

### Administrative Access

| Role | Access | Authentication |
|------|--------|---------------|
| SRE on-call | Kubernetes, databases (read), Grafana, PagerDuty | SSO + MFA + kubectl RBAC |
| SRE lead | Kubernetes (admin), databases (read/write), AWS console | SSO + MFA + assume-role |
| DBA | Database direct access (via bastion) | SSO + MFA + SSH key + IP allowlist |
| Developer | Read-only Kubernetes, Grafana, staging databases | SSO + MFA + kubectl RBAC |
| No one | Production database write access without change ticket | N/A |

### Network Security

- All databases in private subnets (no public IP)
- TigerBeetle accessible only from settla-server pods (Kubernetes NetworkPolicy)
- PgBouncer accessible only from settla-server and settla-node pods
- Redis accessible only from settla-server, gateway, and webhook pods
- NATS accessible only from settla-server and settla-node pods
- Bastion host required for any direct database access (audit-logged)

---

## Vulnerability Management

### Dependency Scanning

- Go: `govulncheck` in CI pipeline
- Node.js: `npm audit` / `pnpm audit` in CI pipeline
- Container images: Trivy scan on every build
- Critical/High CVEs: patch within 48 hours
- Medium CVEs: patch within 2 weeks

### Penetration Testing

- Annual third-party penetration test
- Quarterly internal security review
- Bug bounty program (scope: API endpoints, webhook verification)

---

## Incident Response

### Security Incident Classification

| Severity | Example | Response Time |
|----------|---------|---------------|
| P1 (Critical) | Data breach, unauthorized fund movement | 15 minutes |
| P2 (High) | API key compromise, privilege escalation | 1 hour |
| P3 (Medium) | Suspicious activity pattern, failed auth spikes | 4 hours |
| P4 (Low) | Vulnerability disclosure, config drift | 24 hours |

### Response Procedures

1. **Contain:** Revoke compromised credentials, isolate affected tenant
2. **Assess:** Determine scope of exposure using audit logs
3. **Remediate:** Patch vulnerability, rotate affected keys
4. **Notify:** Inform affected tenants within 72 hours (GDPR requirement)
5. **Post-mortem:** Document root cause, preventive measures, timeline
