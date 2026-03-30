# Settla Secret Management Policy

**Last updated**: 2026-03-12

This document covers every secret used by Settla, minimum security requirements, rotation procedures, and production injection patterns.

---

## Secret Inventory

| Secret | Service | Minimum Entropy | Rotation Cadence | Source (production) |
|--------|---------|----------------|-----------------|---------------------|
| `POSTGRES_PASSWORD` | All DB connections | 256-bit (32-char hex) | Quarterly | `settla/production/db` in AWS Secrets Manager |
| `SETTLA_WALLET_ENCRYPTION_KEY` | Hot wallet AES-256 | 256-bit (32-char hex) | On compromise only | `settla/production/wallets` in AWS Secrets Manager |
| `SETTLA_MASTER_SEED` | HD wallet derivation | 512-bit (64-char hex) | Never (deterministic) | `settla/production/wallets` in AWS Secrets Manager |
| `SETTLA_OPS_API_KEY` | Ops dashboard auth | 256-bit (32-char hex) | Quarterly | `settla/production/app` in AWS Secrets Manager |
| `TYK_SECRET` | Tyk API gateway | 256-bit (32-char hex) | Quarterly | `settla/production/app` in AWS Secrets Manager |
| `LEMFI_WEBHOOK_SECRET` | Outbound webhook HMAC | 256-bit (32-char hex) | Per tenant request | `settla/production/webhooks` in AWS Secrets Manager |
| `FINCRA_WEBHOOK_SECRET` | Outbound webhook HMAC | 256-bit (32-char hex) | Per tenant request | `settla/production/webhooks` in AWS Secrets Manager |
| `PROVIDER_*_WEBHOOK_SECRET` | Inbound provider HMAC | Provider-defined | On provider rotation | `settla/production/providers` in AWS Secrets Manager |
| `GRAFANA_ADMIN_PASSWORD` | Grafana ops console | 128-bit (16-char hex) | Quarterly | `settla/production/app` in AWS Secrets Manager |
| `SLACK_WEBHOOK_URL` | AlertManager Slack | N/A (URL) | On revocation | `settla/production/alertmanager` in AWS Secrets Manager |
| `PAGERDUTY_SERVICE_KEY` | AlertManager PagerDuty | N/A (UUID) | On revocation | `settla/production/alertmanager` in AWS Secrets Manager |
| `REMEDIATION_WEBHOOK_TOKEN` | Auto-remediation auth | 256-bit (32-char hex) | Quarterly | `settla/production/alertmanager` in AWS Secrets Manager |
| `SETTLA_API_KEY_HMAC_SECRET` | API key hashing (gateway + server) | 256-bit (32-char hex) | Quarterly | `settla/production/app` in AWS Secrets Manager |
| `SETTLA_NATS_TOKEN` | NATS JetStream auth | 256-bit (32-char hex) | Quarterly | `settla/production/app` in AWS Secrets Manager |
| `SETTLA_JWT_SECRET` | Portal JWT signing | 256-bit (32-char base64) | Quarterly | `settla/production/app` in AWS Secrets Manager |

---

## Generating Secrets

All secrets must be cryptographically random. Never use memorable passwords, dictionary words, or defaults.

```bash
# 256-bit hex (32 bytes = 64 hex chars) — use for keys, passwords, tokens
openssl rand -hex 32

# 512-bit hex (64 bytes = 128 hex chars) — use for master seed
openssl rand -hex 64

# 128-bit hex (16 bytes = 32 hex chars) — use for Grafana password
openssl rand -hex 16
```

---

## Production Injection

Settla uses the **External Secrets Operator (ESO)** to sync secrets from AWS Secrets Manager into Kubernetes Secrets at deploy time. No secret is ever written to version control, Dockerfiles, or CI environment variables.

### AWS Secrets Manager path layout

```
settla/
├── production/
│   ├── db                    # POSTGRES_PASSWORD, app-role passwords
│   ├── wallets               # SETTLA_WALLET_ENCRYPTION_KEY, SETTLA_MASTER_SEED
│   ├── app                   # SETTLA_OPS_API_KEY, TYK_SECRET, GRAFANA_ADMIN_PASSWORD, SETTLA_API_KEY_HMAC_SECRET, SETTLA_NATS_TOKEN, SETTLA_JWT_SECRET
│   ├── webhooks              # Per-tenant outbound webhook HMAC secrets
│   ├── providers             # Per-provider inbound webhook HMAC secrets
│   └── alertmanager          # SLACK_WEBHOOK_URL, PAGERDUTY_SERVICE_KEY, REMEDIATION_WEBHOOK_TOKEN
└── staging/
    └── ...                   # Same layout, separate values
```

### ExternalSecret resources

ESO ExternalSecrets are defined in `deploy/k8s/base/secrets/`. Each one maps a Secrets Manager path → a Kubernetes Secret → environment variables via `secretKeyRef` in pod specs.

Key ExternalSecrets:
- `settla-db-credentials` → DB passwords for all three databases
- `settla-app-secrets` → ops key, Tyk secret, Grafana password, API key HMAC secret, NATS token, JWT secret
- `settla-webhook-secrets` → per-tenant HMAC secrets
- `alertmanager-secrets` → Slack/PagerDuty/remediation tokens

---

## Rotation Procedures

### Quarterly rotation (database passwords, API keys, tokens)

1. Generate new value: `openssl rand -hex 32`
2. Update the secret in AWS Secrets Manager (keep old value as a staged version for zero-downtime rollover)
3. Trigger ESO sync: `kubectl annotate externalsecret settla-app-secrets force-sync=$(date +%s)`
4. Perform a rolling restart: `kubectl rollout restart deployment/settla-server deployment/settla-gateway`
   > **SETTLA_API_KEY_HMAC_SECRET note:** This secret must be identical across all gateway AND settla-server instances. Rolling restart must complete before any API key creation or validation occurs with the new secret. Coordinate the rollout to avoid a window where some instances use the old secret and others use the new one.
5. Verify health: check `/health` on all services
6. Delete the old staged version from Secrets Manager after 15 minutes

### Webhook secret rotation (per-tenant request)

1. Tenant contacts support to request webhook secret rotation
2. New secret generated and stored in Secrets Manager under `settla/production/webhooks/{tenant_slug}`
3. ESO sync + rolling restart of the webhook service
4. Tenant updates their endpoint verification to use the new secret
5. Old secret deprecated after tenant confirms receipt (max 24 hours)

### Wallet encryption key (emergency only)

The wallet encryption key protects hot wallet private keys on disk. Rotating it requires:

1. Stop all worker processes that access wallets
2. Decrypt all wallet files with the old key
3. Re-encrypt all wallet files with the new key
4. Update the secret in Secrets Manager
5. Restart worker processes

**This procedure should only be executed if the key is suspected to be compromised. Coordinate with the security team before proceeding.**

### Master seed (never rotate)

The HD wallet master seed is deterministic — rotating it changes all derived wallet addresses, which would orphan funds. If the master seed is compromised, treat all associated wallets as compromised and migrate funds to new wallets derived from a fresh seed in a separate emergency procedure.

---

## Local Development

In local development, `.env` (copied from `.env.example`) uses weak placeholder values. These are intentional and safe because:

- The local environment is not internet-accessible
- All blockchain operations use testnets or mocks
- No real customer funds are involved

**Never copy production secrets into `.env`.** If you need to test against production-like secrets, use a dedicated staging environment with its own Secrets Manager paths.

---

## Secret Scanning

The CI pipeline runs [trufflesecurity/trufflehog](https://github.com/trufflesecurity/trufflehog) on every push to detect accidentally committed secrets. If a secret is detected:

1. Immediately rotate the exposed secret (follow the relevant procedure above)
2. Force-push to remove the secret from git history, or use `git filter-repo`
3. Notify the security team

Pre-commit scanning is also recommended for local development:
```bash
brew install trufflehog
trufflehog git file://. --since-commit HEAD --only-verified
```

---

## Compliance Notes

- All secrets at rest in AWS Secrets Manager are encrypted with AWS KMS (customer-managed key)
- All secrets in transit use TLS 1.2+
- Access to Secrets Manager is restricted to the `settla-eks-node-role` IAM role via IRSA (IAM Roles for Service Accounts)
- Secret access is logged in AWS CloudTrail
- Quarterly rotation aligns with PCI-DSS requirement 8.6.3 for service account credentials
