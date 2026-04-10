# Settla Kubernetes Secrets Guide

Comprehensive reference for managing secrets across all Settla Kubernetes environments.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Secret Inventory](#2-secret-inventory)
3. [Production (AWS EKS)](#3-production-aws-eks)
4. [Homelab (k3s + SOPS)](#4-homelab-k3s--sops)
5. [Development (Plain K8s Secrets)](#5-development-plain-k8s-secrets)
6. [Secret Flow: Storage to Pod](#6-secret-flow-storage-to-pod)
7. [Generating Secrets](#7-generating-secrets)
8. [Rotation Procedures](#8-rotation-procedures)
9. [Adding a New Secret](#9-adding-a-new-secret)
10. [Troubleshooting](#10-troubleshooting)

---

## 1. Architecture Overview

Each environment uses a different secrets backend, but all produce the same six Kubernetes Secrets consumed by the same pod specs.

```
                    Production              Homelab              Development
                    ──────────              ───────              ───────────
Storage:            AWS Secrets Manager     SOPS + age           Plain YAML
Sync Mechanism:     External Secrets Op.    kubectl apply        kubectl apply
Refresh:            Automatic (1h)          Manual redeploy      Manual redeploy
Encryption at Rest: AWS KMS (CMK)          age (asymmetric)     K8s etcd only
Safe to Commit:     N/A (never in git)     Yes (encrypted)      No (dev only)
```

All three produce these six K8s Secrets:

| K8s Secret Name | Purpose |
|-----------------|---------|
| `settla-db-credentials` | Per-database PostgreSQL passwords + superuser/replication |
| `patroni-credentials` | Alias of above for Patroni StatefulSets |
| `settla-app-secrets` | Wallet keys, API gateway, Redis, Grafana, dashboard |
| `settla-webhook-secrets` | Per-tenant HMAC-SHA256 signing keys |
| `alertmanager-secrets` | Slack, PagerDuty, remediation webhook |
| `backup-secrets` | Backup job DB passwords, notification URLs |

---

## 2. Secret Inventory

### Database Credentials (`settla-db-credentials`)

| Key | Used By | Description |
|-----|---------|-------------|
| `superuser-password` | Patroni | Postgres superuser for cluster bootstrap |
| `replication-password` | Patroni | Streaming replication between replicas |
| `transfer-password` | PgBouncer, settla-server, settla-node, webhook, migration job | Transfer DB application password |
| `ledger-password` | PgBouncer, settla-server, settla-node, migration job | Ledger DB application password |
| `treasury-password` | PgBouncer, settla-server, settla-node, migration job | Treasury DB application password |

### Application Secrets (`settla-app-secrets`)

| Key | Used By | Description |
|-----|---------|-------------|
| `tyk-secret` | settla-server, Tyk gateway | API gateway admin secret |
| `wallet-encryption-key` | settla-server | AES-256 key for wallet private keys at rest |
| `master-seed` | settla-server | BIP-39 HD wallet root (512-bit, **never rotate**) |
| `dashboard-api-key` | ops dashboard | Valid tenant API key for dashboard auth |
| `tron-api-key` | settla-server | TronGrid RPC rate limit key (optional) |
| `redis-password` | settla-server, settla-node | Redis AUTH password (optional) |
| `grafana-admin-password` | Grafana | Monitoring console login |

### Webhook Secrets (`settla-webhook-secrets`)

| Key | Used By | Description |
|-----|---------|-------------|
| `lemfi-webhook-secret` | webhook | HMAC-SHA256 signing for Lemfi outbound webhooks |
| `fincra-webhook-secret` | webhook | HMAC-SHA256 signing for Fincra outbound webhooks |
| `paystack-webhook-secret` | webhook | HMAC-SHA256 signing for Paystack outbound webhooks |

### AlertManager Secrets (`alertmanager-secrets`)

| Key | Used By | Description |
|-----|---------|-------------|
| `slack-webhook-url` | AlertManager | Slack incoming webhook for alert notifications |
| `pagerduty-service-key` | AlertManager | PagerDuty Events API v2 routing key |
| `remediation-webhook-token` | AlertManager | Auth token for auto-remediation webhook |

### Backup Secrets (`backup-secrets`)

| Key | Used By | Description |
|-----|---------|-------------|
| `POSTGRES_TRANSFER_PASSWORD` | postgres-backup CronJob | Transfer DB password for pg_dump |
| `POSTGRES_LEDGER_PASSWORD` | postgres-backup CronJob | Ledger DB password for pg_dump |
| `POSTGRES_TREASURY_PASSWORD` | postgres-backup CronJob | Treasury DB password for pg_dump |
| `VERIFY_PG_PASSWORD` | backup-verify CronJob | Password for restore-test Postgres |
| `SLACK_WEBHOOK_URL` | All backup CronJobs | Backup success/failure notifications |
| `PAGERDUTY_ROUTING_KEY` | All backup CronJobs | Critical backup failure alerts |
| `NATS_CREDS_CONTENT` | nats-backup CronJob | JetStream auth credentials |

---

## 3. Production (AWS EKS)

Production uses the **External Secrets Operator (ESO)** to sync secrets from AWS Secrets Manager into Kubernetes.

### Prerequisites

1. **External Secrets Operator** installed:
   ```bash
   helm repo add external-secrets https://charts.external-secrets.io
   helm install external-secrets external-secrets/external-secrets \
     -n external-secrets-operator --create-namespace
   ```

2. **IAM policy** attached to the EKS node role or IRSA service account:
   ```json
   {
     "Effect": "Allow",
     "Action": ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"],
     "Resource": "arn:aws:secretsmanager:eu-west-1:*:secret:settla/*"
   }
   ```

### AWS Secrets Manager Layout

```
settla/production/
├── db              → settla-db-credentials, patroni-credentials
├── app             → settla-app-secrets
├── webhooks        → settla-webhook-secrets
├── alertmanager    → alertmanager-secrets
└── backup          → backup-secrets
```

### Creating Secrets in AWS

```bash
# Database credentials (per-DB passwords)
aws secretsmanager create-secret \
  --name settla/production/db \
  --region eu-west-1 \
  --secret-string '{
    "postgres-superuser-password": "'$(openssl rand -hex 32)'",
    "postgres-replication-password": "'$(openssl rand -hex 32)'",
    "postgres-transfer-password": "'$(openssl rand -hex 32)'",
    "postgres-ledger-password": "'$(openssl rand -hex 32)'",
    "postgres-treasury-password": "'$(openssl rand -hex 32)'"
  }'

# Application secrets
aws secretsmanager create-secret \
  --name settla/production/app \
  --region eu-west-1 \
  --secret-string '{
    "tyk-secret": "'$(openssl rand -hex 32)'",
    "wallet-encryption-key": "'$(openssl rand -hex 32)'",
    "master-seed": "'$(openssl rand -hex 64)'",
    "dashboard-api-key": "",
    "tron-api-key": "",
    "redis-password": "'$(openssl rand -hex 32)'",
    "grafana-admin-password": "'$(openssl rand -hex 16)'"
  }'

# Webhook HMAC secrets
aws secretsmanager create-secret \
  --name settla/production/webhooks \
  --region eu-west-1 \
  --secret-string '{
    "lemfi-webhook-secret": "'$(openssl rand -hex 32)'",
    "fincra-webhook-secret": "'$(openssl rand -hex 32)'",
    "paystack-webhook-secret": "'$(openssl rand -hex 32)'"
  }'

# AlertManager
aws secretsmanager create-secret \
  --name settla/production/alertmanager \
  --region eu-west-1 \
  --secret-string '{
    "slack-webhook-url": "https://hooks.slack.com/services/YOUR/SLACK/URL",
    "pagerduty-service-key": "YOUR_PAGERDUTY_KEY",
    "remediation-webhook-token": "'$(openssl rand -hex 32)'"
  }'

# Backup job credentials
aws secretsmanager create-secret \
  --name settla/production/backup \
  --region eu-west-1 \
  --secret-string '{
    "postgres-transfer-password": "<same-as-db-secret>",
    "postgres-ledger-password": "<same-as-db-secret>",
    "postgres-treasury-password": "<same-as-db-secret>",
    "verify-pg-password": "'$(openssl rand -hex 32)'",
    "slack-webhook-url": "https://hooks.slack.com/services/YOUR/SLACK/URL",
    "pagerduty-routing-key": "YOUR_PAGERDUTY_KEY",
    "nats-creds-content": ""
  }'
```

### How It Works

1. ESO controller watches `ExternalSecret` resources in `deploy/k8s/base/secrets/`
2. Every 1 hour, ESO fetches the latest values from AWS Secrets Manager
3. ESO creates/updates the corresponding K8s Secret objects
4. Pods read secrets via `secretKeyRef` in their env blocks
5. K8s resolves `$(POSTGRES_TRANSFER_PASSWORD)` in ConfigMap URLs using dependent env var substitution

### Force-Sync After AWS Update

```bash
kubectl annotate externalsecret settla-db-credentials \
  force-sync=$(date +%s) --overwrite -n settla

kubectl annotate externalsecret settla-app-secrets \
  force-sync=$(date +%s) --overwrite -n settla
```

### Verify Sync Status

```bash
kubectl get externalsecrets -n settla
# STATUS column should show "SecretSynced"

kubectl get secret settla-db-credentials -n settla -o jsonpath='{.data}' | jq
```

---

## 4. Homelab (k3s + SOPS)

The homelab uses **SOPS + age** to encrypt secrets at rest in git. No External Secrets Operator or AWS account required.

### Prerequisites

```bash
# Install SOPS
brew install sops       # macOS
# or: go install github.com/getsops/sops/v3/cmd/sops@latest

# Install age
brew install age        # macOS
# or: go install filippo.io/age/cmd/...@latest
```

### First-Time Setup

```bash
# 1. Generate an age key pair
age-keygen -o ~/.config/sops/age/keys.txt
# Output includes the public key: age1xxxx...

# 2. Export for CLI use
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
# Add this to your ~/.zshrc or ~/.bashrc

# 3. Update .sops.yaml with your public key
#    Replace the placeholder in the repo root .sops.yaml:
#      age: "age1xxxxxxxxx..."
#    with your actual public key from step 1.

# 4. Update .env.homelab
#    Set SOPS_AGE_RECIPIENTS to the same public key.
```

### Editing Secrets

```bash
# Option A: Decrypt → edit → re-encrypt (two-step)
make k8s-homelab-secrets-decrypt
vim deploy/k8s/overlays/homelab/secrets.yaml
make k8s-homelab-secrets-encrypt

# Option B: Edit in-place (SOPS opens your $EDITOR with decrypted content)
sops deploy/k8s/overlays/homelab/secrets.yaml
```

### What to Set

Edit `deploy/k8s/overlays/homelab/secrets.yaml` and replace all `CHANGE_ME` values:

```yaml
# settla-db-credentials
stringData:
  superuser-password: "<openssl rand -hex 32>"
  replication-password: "<openssl rand -hex 32>"
  transfer-password: "<must match POSTGRES_TRANSFER_PASSWORD in macbook-1/.env>"
  ledger-password: "<must match POSTGRES_LEDGER_PASSWORD in macbook-2/.env>"
  treasury-password: "<must match POSTGRES_TREASURY_PASSWORD in macbook-2/.env>"

# settla-app-secrets
stringData:
  tyk-secret: "<openssl rand -hex 32>"
  wallet-encryption-key: "<openssl rand -hex 32>"
  master-seed: "<openssl rand -hex 64>"
  # dashboard-api-key, tron-api-key, redis-password can be empty for homelab

# settla-webhook-secrets
stringData:
  lemfi-webhook-secret: "<openssl rand -hex 32>"
  fincra-webhook-secret: "<openssl rand -hex 32>"
  paystack-webhook-secret: "<openssl rand -hex 32>"
```

The DB passwords in `secrets.yaml` **must match** the passwords in:
- `deploy/data-plane/macbook-1/.env` (`POSTGRES_TRANSFER_PASSWORD`)
- `deploy/data-plane/macbook-2/.env` (`POSTGRES_LEDGER_PASSWORD`, `POSTGRES_TREASURY_PASSWORD`)
- `deploy/k8s/overlays/homelab/.env.homelab` (`POSTGRES_TRANSFER_PASSWORD`, `POSTGRES_LEDGER_PASSWORD`, `POSTGRES_TREASURY_PASSWORD`)

### GitOps Decryption (Flux/ArgoCD)

If using GitOps, the k3s cluster needs the private key to decrypt on apply:

```bash
kubectl create secret generic sops-age \
  --namespace=flux-system \
  --from-file=age.agekey=~/.config/sops/age/keys.txt
```

### Deploy

```bash
make k8s-homelab-deploy
# This runs: sops -d secrets.yaml | kubectl apply -f -
# (decrypts in-memory, never writes plaintext to disk during deploy)
```

---

## 5. Development (Plain K8s Secrets)

Development uses hardcoded placeholder values. **No encryption, no external secrets store.**

### Location

`deploy/k8s/overlays/development/secrets.yaml`

### Default Values

All database passwords default to `"settla"`. Wallet keys use 64-zero placeholders. Webhook secrets use `"dev-*"` prefixes.

These are intentionally weak because:
- The dev namespace (`settla-dev`) is not internet-accessible
- All blockchain operations use mocks
- No real funds or customer data

### Docker Compose (Local Dev)

The `deploy/docker-compose.yml` uses the same convention with `:-settla` defaults:

```yaml
POSTGRES_PASSWORD: ${POSTGRES_TRANSFER_PASSWORD:-settla}
```

No `.env` file required for local dev — all defaults are baked in.

### Applying

```bash
kubectl apply -f deploy/k8s/overlays/development/secrets.yaml
# or via kustomize:
kubectl apply -k deploy/k8s/overlays/development/
```

---

## 6. Secret Flow: Storage to Pod

### Production

```
AWS Secrets Manager                 K8s cluster
─────────────────                   ───────────
settla/production/db ──(ESO 1h)──▶ Secret: settla-db-credentials
                                      key: transfer-password = "abc123..."
                                         │
                                         ▼
                                    Pod spec (deployment.yaml):
                                      env:
                                        - name: POSTGRES_TRANSFER_PASSWORD
                                          valueFrom:
                                            secretKeyRef:
                                              name: settla-db-credentials
                                              key: transfer-password
                                         │
                                         ▼
                                    ConfigMap (settla-server-config):
                                      SETTLA_TRANSFER_DB_URL:
                                        "postgres://settla:$(POSTGRES_TRANSFER_PASSWORD)@..."
                                         │
                                         ▼
                                    K8s resolves $(POSTGRES_TRANSFER_PASSWORD) = "abc123..."
                                         │
                                         ▼
                                    Container env:
                                      SETTLA_TRANSFER_DB_URL=postgres://settla:abc123...@...
```

### Homelab

```
secrets.yaml (SOPS-encrypted in git)
         │
         ▼  (make k8s-homelab-deploy → sops -d | kubectl apply)
Secret: settla-db-credentials
  key: transfer-password = "xyz789..."
         │
         ▼  (same pod spec as production)
Container env:
  SETTLA_TRANSFER_DB_URL=postgres://settla:xyz789...@pgbouncer-transfer:6432/...
```

### Development

```
secrets.yaml (plain YAML, not committed to main)
         │
         ▼  (kubectl apply -k)
Secret: settla-db-credentials
  key: transfer-password = "settla"
         │
         ▼  (same pod spec as production)
Container env:
  SETTLA_TRANSFER_DB_URL=postgres://settla:settla@pgbouncer-transfer:6432/...
```

---

## 7. Generating Secrets

```bash
# 256-bit (standard for passwords, API keys, HMAC secrets)
openssl rand -hex 32

# 512-bit (master seed only)
openssl rand -hex 64

# 128-bit (Grafana admin password)
openssl rand -hex 16
```

For homelab, a helper script generates all secrets at once:

```bash
make init-testnet-wallets
# Outputs wallet-encryption-key and master-seed values to copy into secrets.yaml
```

---

## 8. Rotation Procedures

### Quarterly Rotation (DB passwords, API keys, tokens)

**Production:**

```bash
# 1. Generate new password
NEW_PW=$(openssl rand -hex 32)

# 2. Update in AWS Secrets Manager
aws secretsmanager put-secret-value \
  --secret-id settla/production/db \
  --secret-string "$(aws secretsmanager get-secret-value \
    --secret-id settla/production/db \
    --query SecretString --output text \
    | jq --arg pw "$NEW_PW" '.["postgres-transfer-password"] = $pw')"

# 3. Force ESO sync
kubectl annotate externalsecret settla-db-credentials \
  force-sync=$(date +%s) --overwrite -n settla

# 4. Rolling restart (zero-downtime)
kubectl rollout restart deployment/settla-server -n settla
kubectl rollout restart statefulset/settla-node -n settla
kubectl rollout restart deployment/webhook -n settla

# 5. Verify
kubectl rollout status deployment/settla-server -n settla
```

**Homelab:**

```bash
# 1. Generate new password
NEW_PW=$(openssl rand -hex 32)

# 2. Update MacBook .env
# Edit deploy/data-plane/macbook-1/.env: POSTGRES_TRANSFER_PASSWORD=$NEW_PW

# 3. Recreate the Postgres container (required for password change)
cd deploy/data-plane/macbook-1
docker compose down
docker compose up -d

# 4. Update secrets.yaml
make k8s-homelab-secrets-decrypt
# Edit secrets.yaml: set transfer-password to $NEW_PW
make k8s-homelab-secrets-encrypt

# 5. Update .env.homelab
# Edit .env.homelab: POSTGRES_TRANSFER_PASSWORD=$NEW_PW

# 6. Redeploy
make k8s-homelab-deploy
```

### Webhook Secret Rotation (per-tenant)

1. Generate new secret: `openssl rand -hex 32`
2. Update in AWS Secrets Manager (or `secrets.yaml` for homelab)
3. Rolling restart of webhook deployment
4. Coordinate with the receiving fintech to update their verification

### Wallet Encryption Key (emergency only)

1. Stop all settla-server and settla-node pods
2. Decrypt wallet files with the old key
3. Re-encrypt with the new key
4. Update the secret
5. Restart pods

### Master Seed (NEVER rotate)

The master seed is deterministic. Rotating it changes all derived wallet addresses and orphans funds. If compromised, migrate funds to wallets from a fresh seed.

---

## 9. Adding a New Secret

### Step 1: Add to AWS Secrets Manager (production)

```bash
aws secretsmanager put-secret-value \
  --secret-id settla/production/app \
  --secret-string "$(aws secretsmanager get-secret-value \
    --secret-id settla/production/app \
    --query SecretString --output text \
    | jq '. + {"new-secret-key": "value"}')"
```

### Step 2: Add to ExternalSecret (base)

Edit the relevant file in `deploy/k8s/base/secrets/`:

```yaml
# In settla-app-secrets.yaml, add under data:
- secretKey: new-secret-key
  remoteRef:
    key: settla/production/app
    property: new-secret-key
```

### Step 3: Add to overlay secrets

**Homelab** (`deploy/k8s/overlays/homelab/secrets.yaml`):
```yaml
# Under settla-app-secrets stringData:
new-secret-key: "value"
```

**Development** (`deploy/k8s/overlays/development/secrets.yaml`):
```yaml
# Under settla-app-secrets stringData:
new-secret-key: "dev-placeholder"
```

### Step 4: Reference in pod spec

Edit the deployment/statefulset that needs the secret:

```yaml
env:
  - name: MY_NEW_SECRET
    valueFrom:
      secretKeyRef:
        name: settla-app-secrets
        key: new-secret-key
```

### Step 5: Apply

```bash
# Production: ESO syncs automatically, or force:
kubectl annotate externalsecret settla-app-secrets force-sync=$(date +%s) -n settla

# Homelab:
make k8s-homelab-deploy

# Development:
kubectl apply -k deploy/k8s/overlays/development/
```

---

## 10. Troubleshooting

### Secret not syncing (production)

```bash
# Check ESO status
kubectl get externalsecrets -n settla
kubectl describe externalsecret settla-db-credentials -n settla

# Common errors:
# "SecretSyncedError" → IAM permissions issue (check IRSA role)
# "ProviderError"     → AWS Secrets Manager path doesn't exist
# "SecretNotFound"    → JSON property missing from the SM secret
```

### Password authentication failed

```bash
# Verify the K8s secret has the expected value
kubectl get secret settla-db-credentials -n settla \
  -o jsonpath='{.data.transfer-password}' | base64 -d

# Compare with what Postgres expects:
# Homelab: check deploy/data-plane/macbook-1/.env
# Production: check AWS Secrets Manager console
```

### SOPS decryption fails (homelab)

```bash
# Verify the age key is available
echo $SOPS_AGE_KEY_FILE
cat $SOPS_AGE_KEY_FILE | head -1
# Should show: # created: ...

# Verify the public key in .sops.yaml matches
grep age .sops.yaml

# Common fix: export the key file path
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
```

### Pod stuck in CrashLoopBackOff after secret change

```bash
# Check if the secret was actually updated
kubectl get secret settla-db-credentials -n settla -o yaml | grep -c transfer-password

# Restart pods to pick up new secret values
kubectl rollout restart deployment/settla-server -n settla

# If using PgBouncer, it also needs restart
kubectl rollout restart deployment/pgbouncer-transfer -n settla
```

### Homelab secrets out of sync with MacBook passwords

The DB passwords must match in three places:
1. MacBook `.env` files (what Postgres uses)
2. `secrets.yaml` → `settla-db-credentials` (what PgBouncer/apps use)
3. `.env.homelab` (what `envsubst` uses for migration job URLs)

```bash
# Quick check — all three should produce the same value:
grep POSTGRES_TRANSFER_PASSWORD deploy/data-plane/macbook-1/.env
grep POSTGRES_TRANSFER_PASSWORD deploy/k8s/overlays/homelab/.env.homelab
make k8s-homelab-secrets-decrypt && grep transfer-password deploy/k8s/overlays/homelab/secrets.yaml
make k8s-homelab-secrets-encrypt  # re-encrypt immediately
```

---

## Quick Reference

```bash
# === Production ===
# View sync status
kubectl get externalsecrets -n settla
# Force sync
kubectl annotate externalsecret <name> force-sync=$(date +%s) --overwrite -n settla
# Restart after rotation
kubectl rollout restart deployment/settla-server deployment/webhook -n settla
kubectl rollout restart statefulset/settla-node -n settla

# === Homelab ===
# Decrypt for editing
make k8s-homelab-secrets-decrypt
# Re-encrypt before commit
make k8s-homelab-secrets-encrypt
# Edit in-place
sops deploy/k8s/overlays/homelab/secrets.yaml
# Deploy (auto-decrypts)
make k8s-homelab-deploy

# === Generate secrets ===
openssl rand -hex 32    # 256-bit (passwords, keys, tokens)
openssl rand -hex 64    # 512-bit (master seed)
openssl rand -hex 16    # 128-bit (Grafana password)
```
