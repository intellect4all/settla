# Chapter 10.4: Secrets Management -- The 15 Secrets That Run Settla

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Enumerate every secret required by a production Settla deployment and classify each by category, rotation cadence, and blast radius
2. Explain the KMS envelope encryption hierarchy and why a single AWS KMS master key protects dozens of data encryption keys
3. Trace the path a secret takes from AWS Secrets Manager through External Secrets Operator into a running Kubernetes pod
4. Describe the BIP-44 HD wallet derivation model and why the master seed is the single most critical secret in the system
5. Design an emergency rotation procedure for each secret category, including the "sweep all funds" response to a compromised master seed

---

## The Secret Inventory

A production Settla deployment depends on at least 15 distinct secrets. Forgetting even one -- or leaving it at its development default -- creates a hole that bypasses every other security control. Let us catalog them all.

### Authentication Secrets

These secrets verify identity. They answer the question "who is making this request?"

| Secret | Purpose | Entropy | Rotation |
|--------|---------|---------|----------|
| `SETTLA_API_KEY_HMAC_SECRET` | Keyed hash for API key verification (Chapter 10.1) | 256-bit | Quarterly |
| `SETTLA_JWT_SECRET` | Portal JWT signing for tenant dashboard sessions | 256-bit | Quarterly |
| `SETTLA_NATS_TOKEN` | NATS JetStream broker authentication | 256-bit | Quarterly |
| `SETTLA_OPS_API_KEY` | Ops dashboard `/v1/ops/*` endpoint auth | 256-bit | Quarterly |
| `TYK_SECRET` | Tyk API gateway shared secret | 256-bit | Quarterly |

The API key HMAC secret deserves special attention. As we saw in Chapter 10.1, this secret is the difference between HMAC-SHA256 (secure against offline brute-force even after database compromise) and plain SHA-256 (vulnerable). It must be identical across all gateway and settla-server instances. A mismatch means some instances cannot validate keys that others issued.

### Encryption Secrets

These secrets protect data at rest. They answer the question "can the data be read if the database is stolen?"

| Secret | Purpose | Entropy | Rotation |
|--------|---------|---------|----------|
| `SETTLA_PII_ENCRYPTION_KEY` | AES-256-GCM for sender/recipient PII (Chapter 10.2) | 256-bit | Annual |
| `SETTLA_WALLET_ENCRYPTION_KEY` | Encrypts HD wallet private keys at rest | 256-bit | On compromise |
| `SETTLA_MASTER_SEED` | BIP-44 HD wallet root -- derives all blockchain private keys | 512-bit | Never |

The master seed stands apart from every other secret. We will return to it in detail.

### Infrastructure Secrets

These secrets grant access to internal systems. They answer "can this process connect to the database?"

| Secret | Purpose | Entropy | Rotation |
|--------|---------|---------|----------|
| `POSTGRES_PASSWORD` | Database superuser password (3 databases) | 256-bit | Quarterly |
| `SETTLA_APP_DB_PASSWORD` | RLS application role password | 256-bit | Quarterly |
| `SETTLA_REDIS_URL` | Redis connection (contains password) | 256-bit (password portion) | Quarterly |
| `SETTLA_TIGERBEETLE_ADDRESSES` | TigerBeetle cluster endpoints | N/A (address, not secret) | On change |

Database connection strings embed passwords. The `.env.example` makes this explicit:

```
SETTLA_LEDGER_DB_URL=postgres://settla:settla@localhost:6433/settla_ledger?sslmode=prefer
SETTLA_TRANSFER_DB_URL=postgres://settla:settla@localhost:6434/settla_transfer?sslmode=prefer
SETTLA_TREASURY_DB_URL=postgres://settla:settla@localhost:6435/settla_treasury?sslmode=prefer
```

Those `settla:settla` credentials are development placeholders. In production, each URL contains a 256-bit random password injected from AWS Secrets Manager.

### External Secrets

These secrets authenticate Settla to third parties or third parties to Settla.

| Secret | Purpose | Entropy | Rotation |
|--------|---------|---------|----------|
| Per-provider API keys (`SETTLA_PROVIDER_{ID}_API_KEY`) | Authenticate to payment providers | Provider-defined | Per-provider policy |
| Per-tenant webhook secrets (`LEMFI_WEBHOOK_SECRET`, etc.) | HMAC-SHA256 for outbound webhook signatures (Chapter 10.3) | 256-bit | Per tenant request |
| Per-provider webhook secrets (`PROVIDER_{SLUG}_WEBHOOK_SECRET`) | Verify inbound provider callbacks | Provider-defined | On provider rotation |

### Operational Secrets

These support monitoring and incident response.

| Secret | Purpose | Rotation |
|--------|---------|----------|
| `GRAFANA_ADMIN_PASSWORD` | Grafana ops console access | Quarterly |
| `SLACK_WEBHOOK_URL` | AlertManager Slack notifications | On revocation |
| `PAGERDUTY_SERVICE_KEY` | PagerDuty critical alert integration | On revocation |
| `REMEDIATION_WEBHOOK_TOKEN` | Auto-remediation sidecar authentication | Quarterly |

> **Key Insight:** Count your secrets. If you cannot enumerate every secret your production system requires -- and state the rotation cadence and blast radius of each -- you do not understand your security posture. Settla's `.env.example` serves as a living inventory: every secret is documented with its generation command, production source, and a `CHANGE IN PRODUCTION` marker.

---

## Secret Categories and Blast Radius

Not all secrets are equal. The impact of compromise varies by orders of magnitude. This table ranks them from most to least critical:

```
+----------------------------+---------------+------------+----------------------------------+
| Secret                     | Category      | Rotation   | Impact if Compromised            |
+----------------------------+---------------+------------+----------------------------------+
| MASTER_SEED                | Encryption    | Never      | All wallet keys derivable        |
|                            |               |            | -> total fund loss               |
+----------------------------+---------------+------------+----------------------------------+
| WALLET_ENCRYPTION_KEY      | Encryption    | On         | Wallet private keys decryptable  |
|                            |               | compromise | (requires encrypted key files)   |
+----------------------------+---------------+------------+----------------------------------+
| PII_ENCRYPTION_KEY         | Encryption    | Annual     | PII readable in stolen DB dumps  |
+----------------------------+---------------+------------+----------------------------------+
| API_KEY_HMAC_SECRET        | Auth          | Quarterly  | All API key hashes verifiable    |
|                            |               |            | offline -> tenant impersonation  |
+----------------------------+---------------+------------+----------------------------------+
| DB passwords               | Infra         | Quarterly  | Direct database access           |
+----------------------------+---------------+------------+----------------------------------+
| JWT_SECRET                 | Auth          | Quarterly  | Portal sessions forgeable        |
+----------------------------+---------------+------------+----------------------------------+
| NATS_TOKEN                 | Infra         | Quarterly  | Message broker access ->         |
|                            |               |            | inject/read all events           |
+----------------------------+---------------+------------+----------------------------------+
| Provider API keys          | External      | Per-policy | Provider account compromise      |
+----------------------------+---------------+------------+----------------------------------+
| Webhook secrets            | External      | Per-tenant | Forge webhooks to tenants        |
+----------------------------+---------------+------------+----------------------------------+
| Operational secrets        | Ops           | Quarterly  | Monitoring access, alert spam    |
+----------------------------+---------------+------------+----------------------------------+
```

The master seed is in a category of its own. Every other compromised secret can be rotated with some operational pain but no fund loss. A compromised master seed means every blockchain wallet derived from it is controlled by the attacker. Funds can be drained before you finish reading the incident report.

---

## The KMS Envelope Encryption Hierarchy

Settla does not store encryption keys in plaintext anywhere -- not in environment variables, not in Kubernetes secrets, not in config files. Instead, it uses a two-layer envelope encryption scheme anchored to AWS KMS.

```
AWS KMS Master Key (CMK)
|   (never leaves the HSM -- all operations are API calls to KMS)
|
+-- Decrypt request --> returns plaintext service key
|
+-- Per-Service Encryption Keys (encrypted at rest by KMS)
|   |
|   +-- PII Encryption Key
|   |   (AES-256-GCM, encrypts sender/recipient data in Transfer DB)
|   |
|   +-- Wallet Encryption Key
|   |   (AES-256-GCM, encrypts HD wallet private keys on disk)
|   |
|   +-- Backup Encryption Key
|       (encrypts database backups in S3)
|
+-- Per-Tenant Data Encryption Keys (encrypted by service key)
    |
    +-- Tenant A DEK --> encrypts Tenant A PII
    +-- Tenant B DEK --> encrypts Tenant B PII
    +-- Tenant C DEK --> encrypts Tenant C PII
    +-- ...
```

### Why Two Layers?

The naive approach is to encrypt everything directly with the KMS master key. But KMS has a hard limit: you can call it perhaps 10,000 times per second per region. At 50M transactions per day (~580 TPS sustained), every transaction touching PII would need at least two KMS calls (encrypt + decrypt). That is 1,160 calls per second just for PII, without counting wallet operations, backup encryption, or burst traffic.

Envelope encryption solves this. The KMS master key encrypts a small number of service-level keys. Those service keys are cached in memory after decryption. All data encryption uses the in-memory service keys, never calling KMS directly. KMS is only called at startup (to decrypt the service keys) and during key rotation.

### The Crypto-Shred Advantage

Per-tenant DEKs enable crypto-shredding. When a tenant exercises their right to erasure (GDPR Article 17), Settla deletes the tenant's DEK. The encrypted PII remains in the database but is now mathematically unrecoverable. This satisfies both the erasure requirement and the financial record retention requirement -- the records exist for audit, but the PII within them is destroyed.

> **Key Insight:** Envelope encryption is not just about security. It is about operational boundaries. The KMS master key is controlled by infrastructure. Service keys are controlled by the application. Per-tenant DEKs are controlled by the compliance team. Each layer has its own access control, audit trail, and rotation schedule.

---

## External Secrets Operator: From Vault to Pod

Secrets must travel from their source of truth (AWS Secrets Manager) to the running container. Settla uses the External Secrets Operator (ESO) to bridge this gap without ever storing secrets in version control, CI pipelines, or Kubernetes manifests.

### The Flow

```
+---------------------+        +---------------------+        +-----------------+
| AWS Secrets Manager |  sync  | External Secrets    |  mount | Kubernetes Pod  |
|                     | -----> | Operator (ESO)      | -----> |                 |
| settla/production/  |        | ExternalSecret CR   |        | env vars from   |
|   db/               |        | -> K8s Secret       |        | secretKeyRef    |
|   wallets/          |        |                     |        |                 |
|   app/              |        |                     |        |                 |
|   webhooks/         |        |                     |        |                 |
|   providers/        |        |                     |        |                 |
|   alertmanager/     |        |                     |        |                 |
+---------------------+        +---------------------+        +-----------------+
```

### AWS Secrets Manager Path Layout

Settla organizes secrets by bounded context, mirroring the database separation:

```
settla/
+-- production/
|   +-- db                 # POSTGRES_PASSWORD, app-role passwords
|   +-- wallets            # SETTLA_WALLET_ENCRYPTION_KEY, SETTLA_MASTER_SEED
|   +-- app                # SETTLA_API_KEY_HMAC_SECRET, SETTLA_NATS_TOKEN,
|   |                      # SETTLA_JWT_SECRET, SETTLA_OPS_API_KEY, TYK_SECRET
|   +-- webhooks           # Per-tenant outbound webhook HMAC secrets
|   +-- providers          # Per-provider inbound webhook HMAC secrets
|   +-- alertmanager       # SLACK_WEBHOOK_URL, PAGERDUTY_SERVICE_KEY,
|                          # REMEDIATION_WEBHOOK_TOKEN
+-- staging/
    +-- ...                # Same layout, completely separate values
```

This layout enforces the principle of least privilege. The `settla-server` pod's IAM role can read `settla/production/app` and `settla/production/db`, but not `settla/production/alertmanager`. The monitoring stack can read `settla/production/alertmanager` but not `settla/production/wallets`.

### ExternalSecret Resources

Each ESO ExternalSecret maps a Secrets Manager path to a Kubernetes Secret:

```yaml
# deploy/k8s/base/secrets/settla-app-secrets.yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: settla-app-secrets
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: aws-secrets-manager
    kind: ClusterSecretStore
  target:
    name: settla-app-secrets
    creationPolicy: Owner
  data:
    - secretKey: api-key-hmac-secret
      remoteRef:
        key: settla/production/app
        property: SETTLA_API_KEY_HMAC_SECRET
    - secretKey: jwt-secret
      remoteRef:
        key: settla/production/app
        property: SETTLA_JWT_SECRET
    - secretKey: nats-token
      remoteRef:
        key: settla/production/app
        property: SETTLA_NATS_TOKEN
```

The pod spec references these:

```yaml
env:
  - name: SETTLA_API_KEY_HMAC_SECRET
    valueFrom:
      secretKeyRef:
        name: settla-app-secrets
        key: api-key-hmac-secret
```

### Why Not Kubernetes Secrets Directly?

Kubernetes Secrets are base64-encoded, not encrypted. Anyone with `kubectl get secret` access can read them. They are often backed up to etcd in plaintext (unless etcd encryption is configured). They appear in `kubectl describe pod` output. They are stored in the cluster's etcd database, which may be backed up to S3 without encryption.

ESO solves all of these problems:

| Problem | K8s Secrets | ESO + Secrets Manager |
|---------|-------------|----------------------|
| Encryption at rest | Only if etcd encryption configured | Always (KMS-backed) |
| Audit trail | K8s audit logs (often noisy) | AWS CloudTrail (dedicated) |
| Access control | RBAC (coarse) | IAM policies (fine-grained) |
| Rotation | Manual kubectl apply | Automated sync + rollout |
| Git history | Often committed by accident | Never in git |
| Cross-region | Not built-in | Secrets Manager replication |

> **Key Insight:** The External Secrets Operator is not just a convenience tool. It is a security boundary. It means that compromising the Kubernetes cluster does not automatically compromise the secrets -- the attacker also needs the IAM role credentials to reach Secrets Manager, which requires a second, independent breach.

---

## Wallet Key Management: The Most Critical Secret

The wallet subsystem manages blockchain private keys for on-chain stablecoin transfers. A single compromised key means stolen funds. A compromised master seed means all funds across all chains are stolen.

### BIP-44 HD Derivation

Settla uses hierarchical deterministic (HD) wallets following the BIP-44 standard. A single master seed deterministically generates every wallet the system will ever need:

```
SETTLA_MASTER_SEED (512-bit, 64 bytes hex)
|
+-- BIP-32 derivation
    |
    +-- m/44'/195'/0'/0/0   --> Tron wallet #0      (coin type 195)
    +-- m/44'/195'/0'/0/1   --> Tron wallet #1
    +-- m/44'/60'/0'/0/0    --> Ethereum wallet #0   (coin type 60)
    +-- m/44'/60'/0'/0/1    --> Base wallet #0       (coin type 60, same as ETH)
    +-- m/44'/501'/0'/0/0   --> Solana wallet #0     (coin type 501)
    +-- ...
```

The derivation code from `rail/wallet/derivation.go` shows the path construction:

```go
// DeriveWallet derives a wallet for the specified chain at the given index.
// Uses BIP-44 paths:
// - Tron: m/44'/195'/0'/0/{index}
// - Solana: m/44'/501'/0'/0/{index}
// - Ethereum/Base: m/44'/60'/0'/0/{index}
//
// The returned wallet contains the private key in memory.
// Call wallet.ZeroPrivateKey() when done.
func DeriveWallet(km keymgmt.KeyManager, keyID string, chain Chain, index uint32) (*Wallet, error) {
    coinType := CoinType(chain)
    if coinType == 0 && chain != ChainEthereum && chain != ChainBase {
        return nil, fmt.Errorf("settla-wallet: unsupported chain: %s", chain)
    }

    // Derive BIP-32 key at path m/44'/coinType'/0'/0/index
    derivedKey, err := km.DerivePath(keyID, coinType, 0, 0, index)
    if err != nil {
        return nil, fmt.Errorf("settla-wallet: failed to derive key: %w", err)
    }
    defer SecureZeroBIP32Key(derivedKey)
    // ... chain-specific wallet creation
}
```

The coin type mapping is defined in `rail/wallet/types.go`:

```go
// CoinType returns the BIP-44 coin type for the chain.
func CoinType(c Chain) uint32 {
    switch c {
    case ChainTron:
        return 195
    case ChainSolana:
        return 501
    case ChainEthereum, ChainBase:
        return 60 // Both EVM chains use Ethereum's coin type
    default:
        return 0
    }
}
```

### The Encryption Layer

Private keys are never stored in plaintext. The `Manager` requires a 32-byte encryption key at construction:

```go
// NewManager creates a new wallet manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
    // ...

    // Decode encryption key
    encryptionKeyBytes, err := hex.DecodeString(cfg.EncryptionKey)
    if err != nil {
        return nil, fmt.Errorf("settla-wallet: invalid encryption key hex: %w", err)
    }

    if len(encryptionKeyBytes) != 32 {
        return nil, fmt.Errorf("settla-wallet: encryption key must be 32 bytes (64 hex chars)")
    }

    // Create wallet store
    store, err := NewFileWalletStore(cfg.StoragePath+"/wallets", encryptionKeyBytes)
    // ...
}
```

The persisted form of a wallet is the `EncryptedWallet` struct -- the private key is AES-256 encrypted before it touches disk:

```go
// EncryptedWallet is the persisted form of a wallet with encrypted private key.
type EncryptedWallet struct {
    Path            string  `json:"path"`
    Chain           string  `json:"chain"`
    Address         string  `json:"address"`
    PublicKey       string  `json:"public_key"`
    EncryptedKey    string  `json:"encrypted_key"`    // base64-encoded AES ciphertext
    TenantID        *string `json:"tenant_id,omitempty"`
    TenantSlug      string  `json:"tenant_slug,omitempty"`
    Type            string  `json:"type"`
    DerivationIndex uint32  `json:"derivation_index"`
    CreatedAt       string  `json:"created_at"`
}
```

Notice what is stored: the address (public, needed for deposit monitoring), the public key (needed for verification), and the encrypted private key. The raw private key exists only in memory, and the code is explicit about clearing it:

```go
// Wallet represents a blockchain wallet with its keypair.
// Private keys are kept in memory and never exported or logged.
type Wallet struct {
    // ...
    // privateKey holds the private key in memory. Never exported.
    // Type depends on chain: *ecdsa.PrivateKey for EVM/Tron, ed25519.PrivateKey for Solana.
    privateKey interface{}
    // ...
}

// ZeroPrivateKey securely zeros the private key in memory.
// Should be called when the wallet is no longer needed.
func (w *Wallet) ZeroPrivateKey() {
    if w.privateKey == nil {
        return
    }

    switch key := w.privateKey.(type) {
    case *ecdsa.PrivateKey:
        SecureZeroECDSA(key)
    case ed25519.PrivateKey:
        SecureZeroEd25519(key)
    }

    w.privateKey = nil
}
```

The `defer SecureZeroBIP32Key(derivedKey)` in the derivation code ensures intermediate key material is cleared even if the function returns an error.

### The Master Seed Lifecycle

The master seed follows a unique lifecycle compared to every other secret:

```
Generation                  Storage                     Usage
+-----------+              +------------------+         +------------------+
| openssl   |  once  -->   | AWS Secrets Mgr  |  boot   | In-memory only   |
| rand -hex |              | settla/prod/     |  -->    | (cleared from    |
| 64        |              |   wallets        |         |  config after    |
|           |              |                  |         |  storage)        |
+-----------+              +------------------+         +------------------+
  (airgapped                 (encrypted by               SecureClearBytes
   machine)                   KMS CMK)                   called on seed
```

The code explicitly clears the seed from the configuration struct after storing it in the key manager:

```go
// Store master seed if provided
if len(cfg.MasterSeed) > 0 {
    if !km.HasSeed(cfg.KeyID) {
        if err := km.StoreMasterSeed(cfg.KeyID, cfg.MasterSeed); err != nil {
            m.Close()
            return nil, fmt.Errorf("settla-wallet: failed to store master seed: %w", err)
        }
        cfg.Logger.Info("settla-wallet: stored master seed", "key_id", cfg.KeyID)
    }
    // Clear the seed from config
    SecureClearBytes(cfg.MasterSeed)
}
```

This is defense in depth. Even if an attacker can dump the process's memory, the seed only exists in the key manager's encrypted storage -- not in the original config struct, not in a log, not in an environment variable dump.

> **Key Insight:** The master seed is quasi-permanent because it is deterministic. Rotating it changes every derived wallet address, which means every deposit address Settla has ever issued becomes orphaned. Funds sent to old addresses would be lost. This is why the seed is the one secret that is never rotated under normal operations -- and why its protection must be absolute.

---

## Generating Secrets Correctly

Every secret must come from a cryptographically secure random number generator. Settla standardizes on `openssl rand`:

```bash
# 256-bit hex (32 bytes = 64 hex chars) -- for keys, passwords, tokens
openssl rand -hex 32

# 512-bit hex (64 bytes = 128 hex chars) -- for master seed ONLY
openssl rand -hex 64

# 256-bit base64 (32 bytes) -- for JWT secret
openssl rand -base64 32
```

Why hex encoding? A 256-bit key in hex is exactly 64 characters. It is unambiguous -- no padding issues, no encoding confusion, no null bytes that truncate C strings. The wallet manager validates this precisely:

```go
if len(encryptionKeyBytes) != 32 {
    return nil, fmt.Errorf("settla-wallet: encryption key must be 32 bytes (64 hex chars)")
}
```

### Secret Scanning

Mistakes happen. Someone will eventually type a real secret into a commit message, a test file, or a docker-compose override. Settla's CI pipeline runs TruffleHog on every push:

```bash
# In CI
trufflehog git file://. --since-commit HEAD --only-verified

# Locally (recommended pre-commit)
brew install trufflehog
trufflehog git file://. --since-commit HEAD --only-verified
```

If a secret is detected:

1. Rotate the exposed secret immediately (follow the relevant procedure below)
2. Force-push to remove the secret from git history, or use `git filter-repo`
3. Notify the security team

The key word is "immediately." A secret committed to a public repository is compromised within minutes. Even a private repository should be treated as compromised -- the git history is permanent and may be cloned by any developer with access.

---

## Emergency Rotation Procedures

Each secret category has a different rotation procedure. Some are routine. Some are emergencies that wake people up at 3 AM.

### Routine Quarterly Rotation (DB passwords, API keys, tokens)

```
1. Generate:   openssl rand -hex 32
2. Stage:      Update AWS Secrets Manager (keep old value as staged version)
3. Sync:       kubectl annotate externalsecret settla-app-secrets \
                 force-sync=$(date +%s)
4. Rollout:    kubectl rollout restart deployment/settla-server \
                 deployment/settla-gateway
5. Verify:     Check /health on all services
6. Cleanup:    Delete old staged version after 15 minutes
```

The critical detail is step 2: keeping the old value as a staged version. During the rolling restart, some pods will have the old secret and some will have the new one. For most secrets (DB passwords, NATS token), the infrastructure accepts both during the rollover window. For `SETTLA_API_KEY_HMAC_SECRET`, this is not the case -- the HMAC secret must be identical across all instances. Coordinate the rollout to complete before any API key validation occurs with mixed secrets.

### API_KEY_HMAC_SECRET Compromised

**Severity: HIGH.** An attacker with the HMAC secret and a copy of the `api_keys` table can verify candidate API keys offline.

```
Response timeline:
+---------+----------------------------------------------------------+
| T+0     | Rotate SETTLA_API_KEY_HMAC_SECRET in Secrets Manager     |
| T+1min  | Force ESO sync + rolling restart all gateways + servers  |
| T+5min  | All instances running with new secret                    |
|         | ALL existing API key hashes are now invalid               |
| T+5min  | Notify all tenants: regenerate API keys                  |
| T+24h   | Deadline for tenant key regeneration                     |
+---------+----------------------------------------------------------+
```

This is the most disruptive auth rotation. Every tenant's API keys become invalid simultaneously. There is no graceful rollover -- the old HMAC secret must be discarded completely, and every key hash in the database was computed with the old secret.

### PII_ENCRYPTION_KEY Compromised

**Severity: HIGH.** An attacker with the encryption key and a database dump can decrypt all PII.

```
Response timeline:
+---------+----------------------------------------------------------+
| T+0     | Generate new PII encryption key                          |
| T+1min  | Deploy new key to all instances                          |
| T+5min  | Start background re-encryption job:                      |
|         |   FOR EACH transfer with encrypted PII:                  |
|         |     1. Decrypt with old key                              |
|         |     2. Re-encrypt with new key                           |
|         |     3. UPDATE row                                        |
| T+hours | Re-encryption completes (50M+ rows)                      |
| T+done  | Destroy old key from Secrets Manager                     |
+---------+----------------------------------------------------------+
```

The re-encryption is a background job that must be idempotent. Each row can carry a `key_version` indicator so the job knows which key to use for decryption. The application reads with both keys during the transition: try new key first, fall back to old key.

### MASTER_SEED Compromised

**Severity: CRITICAL / EMERGENCY.** All funds are at immediate risk.

```
Response timeline:
+---------+----------------------------------------------------------+
| T+0     | ALERT: All hands on deck                                 |
| T+0     | BEGIN FUND SWEEP: Transfer all funds from every derived   |
|         |   wallet to a pre-staged emergency cold wallet            |
| T+mins  | Sweep transactions broadcast to all chains                |
| T+conf  | Confirm all sweeps settled on-chain                       |
| T+done  | Generate new master seed (airgapped machine)              |
|         | Deploy new seed, regenerate all wallet addresses           |
|         | Update all deposit addresses in tenant configurations      |
|         | Notify all tenants of new deposit addresses                |
+---------+----------------------------------------------------------+
```

This is the only rotation that involves moving real money. The race is between Settla's sweep transactions and the attacker's drain transactions. Pre-staging emergency cold wallets -- generating them in advance, storing them offline, never derived from the production master seed -- buys critical minutes during the sweep.

### DB Password Compromised

**Severity: MEDIUM.** Direct database access, but data is encrypted at the application layer.

```
Response:
1. Rotate password in Secrets Manager immediately
2. Kill all existing database connections:
   SELECT pg_terminate_backend(pid) FROM pg_stat_activity
     WHERE usename = 'settla';
3. ESO sync + rolling restart
4. Verify no unauthorized connections in pg_stat_activity
```

The blast radius is limited because PII is encrypted at the application layer (Chapter 10.2). An attacker with database access sees ciphertext in PII columns. However, they can read transfer amounts, statuses, tenant configurations, and other operational data. They can also modify data, which is why the response must be immediate.

---

## The Secret Dependency Graph

Understanding which services need which secrets helps enforce least privilege. Not every service needs every secret:

```
                              Secrets
                    +---------------------------+
                    | HMAC | JWT | NATS | DB | Wallet | PII |
+-------------------+------+-----+------+----+--------+-----+
| settla-server     |  X   |  X  |      | X  |        |  X  |
| settla-node       |      |     |  X   | X  |   X    |     |
| gateway (TS)      |  X   |     |      |    |        |     |
| webhook (TS)      |      |     |  X   |    |        |     |
| ops dashboard     |      |     |      |    |        |     |
+-------------------+------+-----+------+----+--------+-----+
```

The gateway only needs the HMAC secret (for API key verification) and the gRPC address. It does not need database passwords, wallet keys, or PII encryption keys. If the gateway is compromised, the attacker cannot decrypt PII or sign blockchain transactions. This is a direct consequence of the modular architecture -- each service has a minimal attack surface defined by its secret access.

---

## Local Development vs Production

The `.env.example` file contains intentionally weak placeholder values:

```bash
POSTGRES_PASSWORD=settla                    # CHANGE IN PRODUCTION
SETTLA_OPS_API_KEY=settla-ops-secret-change-me  # CHANGE IN PRODUCTION
GRAFANA_ADMIN_PASSWORD=settla-dev-local     # CHANGE IN PRODUCTION
```

This is safe for local development because:

1. The local environment is not internet-accessible
2. All blockchain operations use testnets or mocks (`SETTLA_PROVIDER_MODE=mock`)
3. No real customer funds or PII are involved

But it creates a trap. The transition from development to production must replace every `CHANGE IN PRODUCTION` marker with a cryptographically random value. Missing even one creates a hole. Settla's production startup checks enforce this -- the server refuses to start if critical secrets are missing or match known development defaults.

> **Key Insight:** The most dangerous moment in a secret's lifecycle is the transition from development to production. Development defaults that "just work" become production vulnerabilities that "just exist." Every secret should have a startup-time validation that rejects known weak values in non-development environments.

---

## Compliance Alignment

Settla's secrets management aligns with multiple regulatory frameworks:

| Requirement | Framework | How Settla Satisfies It |
|------------|-----------|------------------------|
| Secrets encrypted at rest | PCI DSS 3.4, SOC 2 CC6.1 | AWS Secrets Manager + KMS encryption |
| Access logging | PCI DSS 10.2, SOC 2 CC7.2 | CloudTrail logs all Secrets Manager access |
| Quarterly rotation | PCI DSS 8.6.3 | Automated quarterly cadence for service credentials |
| Separation of duties | SOC 2 CC6.3 | Infrastructure team manages KMS, app team manages service keys |
| Key management procedures | PCI DSS 3.5, 3.6 | Documented generation, rotation, and destruction procedures |
| Secrets not in code | OWASP ASVS 2.10.4 | ESO pattern + TruffleHog CI scanning |

---

## Common Mistakes

**1. Storing secrets in docker-compose.yml committed to git.**
The `deploy/docker-compose.yml` uses `${VARIABLE}` references that read from `.env`. The `.env` file is in `.gitignore`. But developers sometimes hardcode values directly into the compose file for "quick testing" and commit it. TruffleHog catches this, but the damage is done -- git history is permanent.

**2. Using the same secret for multiple purposes.**
It is tempting to use one 256-bit key for PII encryption, wallet encryption, and HMAC signing. After all, they are all "just keys." But if the shared key is compromised, every protection fails simultaneously. Separate secrets create separate blast radii. Settla uses distinct secrets for each purpose precisely because a breach of one should not cascade to all.

**3. Not rotating secrets on a schedule.**
"We will rotate when we need to" means "we will rotate never." Quarterly rotation is not about the theoretical risk of a key being brute-forced -- 256-bit keys are computationally unbreakable. It is about limiting the blast radius of an undetected compromise. If a key was stolen six months ago, quarterly rotation means the attacker has had at most three months of access, not six months and counting.

**4. Logging secrets accidentally (even partial values).**
A structured log like `{"event": "auth_failed", "hmac_secret_prefix": "a1b2c3"}` leaks six hex characters of the HMAC secret. That is 24 bits of information. Repeat this in enough log lines and the secret is reconstructable. Settla's logging convention is absolute: never log any portion of a secret, not even a hash of it, not even its length.

**5. Sharing the master seed with multiple team members.**
The master seed should be known to zero humans during normal operations. It is generated on an airgapped machine, encrypted, and stored in AWS Secrets Manager. The IAM policy restricts read access to the EKS node role. No engineer should have a copy, a printout, or a screenshot. If an engineer leaves the company, you should not need to ask "did they have the master seed?"

**6. Passing secrets as command-line arguments.**
Command-line arguments are visible in `/proc/{pid}/cmdline` on Linux. Running `./settla-server --hmac-secret=abc123` exposes the secret to anyone who can list processes. Settla passes all secrets via environment variables (in Kubernetes, injected from secrets) or mounted files, never as CLI arguments.

**7. Forgetting to clear secrets from memory.**
Settla's wallet code calls `SecureClearBytes()` and `ZeroPrivateKey()` to overwrite key material in memory when it is no longer needed. Without this, the Go garbage collector may hold the old memory contents indefinitely. A heap dump or core dump would reveal the secret. This is defense in depth -- it does not prevent all attacks, but it shrinks the window.

---

## Exercises

### Exercise 1: Secret Inventory Audit

Your team is preparing for a SOC 2 audit. The auditor asks: "For each secret in your system, show me: (a) where it is stored, (b) who can access it, (c) when it was last rotated, and (d) what happens if it is compromised."

Create a table with all 15+ Settla secrets answering these four questions. For the "who can access it" column, specify the AWS IAM role or Kubernetes service account, not individual humans.

### Exercise 2: Rotation Runbook

Write a step-by-step runbook for rotating `SETTLA_API_KEY_HMAC_SECRET`. Your runbook must address:

- How to ensure all gateway and settla-server instances receive the new secret before any begin validating with it (hint: you cannot do a rolling restart -- why not?)
- How to handle the fact that all existing API key hashes become invalid
- How to communicate the change to tenants with minimal disruption
- How to verify that the rotation completed successfully

### Exercise 3: Blast Radius Analysis

An attacker has obtained a copy of the Transfer DB `api_keys` table (the `key_hash` column) and the `SETTLA_API_KEY_HMAC_SECRET`. Describe:

1. What can the attacker do with this combination?
2. What can the attacker NOT do? (What other secrets would they need?)
3. What is the correct incident response sequence?
4. How would the response differ if the attacker had only the `key_hash` column without the HMAC secret?

### Exercise 4: Design a Key Rotation Pipeline

Settla currently relies on manual quarterly rotation triggered by a calendar reminder. Design an automated rotation pipeline that:

- Generates new secrets using `openssl rand`
- Stores them in AWS Secrets Manager with staging labels
- Triggers ESO sync
- Performs a rolling restart of affected services
- Runs health checks to verify the rotation succeeded
- Rolls back automatically if health checks fail
- Notifies the team via Slack on success or failure

Sketch the pipeline as a sequence of steps. For each step, identify what can go wrong and how to recover.

### Exercise 5: Master Seed Emergency Drill

Design a tabletop exercise for a master seed compromise. The scenario: at 2:47 AM, your secret scanning tool detects the master seed in a public GitHub gist posted by a former contractor. The gist was posted 3 hours ago. Write:

1. The first five actions your on-call engineer should take, in order
2. The communication template sent to affected tenants
3. The post-incident checklist for ensuring it cannot happen again
4. An estimate of how long the fund sweep should take, given that Settla operates wallets on Tron, Ethereum, Base, and Solana (consider block confirmation times for each chain)

---

## Summary

Settla's production deployment depends on 15+ secrets spanning five categories: authentication, encryption, infrastructure, external, and operational. Each has a defined rotation cadence, a blast radius analysis, and an emergency response procedure.

The architecture follows three principles:

1. **Envelope encryption.** A single AWS KMS master key protects service-level keys, which protect per-tenant data encryption keys. KMS is called only at startup, never on the hot path.

2. **External Secrets Operator.** Secrets flow from AWS Secrets Manager through ESO into Kubernetes pods. No secret is ever committed to git, baked into a Docker image, or passed as a CLI argument.

3. **Minimal exposure.** Each service receives only the secrets it needs. Private keys are zeroed from memory after use. The master seed is cleared from configuration immediately after storage. Secrets are never logged, not even partially.

The master seed is in a category of its own. It is the single point of failure for all blockchain funds. Its compromise triggers an emergency fund sweep, not a rotation. Protecting it is not a security best practice -- it is the difference between a security incident and a total loss.

---

## Further Reading

- `docs/secrets-management.md` -- Settla's production secret management policy with full rotation procedures
- `docs/security.md` -- Complete security architecture including encryption, PII handling, and key management
- `rail/wallet/manager.go` -- Wallet Manager implementation showing seed storage and key derivation
- `rail/wallet/types.go` -- Wallet types including `EncryptedWallet` and secure zeroing functions
- `rail/wallet/derivation.go` -- BIP-44 HD derivation for Tron, Ethereum, Base, and Solana
- [AWS Secrets Manager documentation](https://docs.aws.amazon.com/secretsmanager/) -- External secrets storage
- [External Secrets Operator](https://external-secrets.io/) -- Kubernetes secrets synchronization
- [BIP-44 specification](https://github.com/bitcoin/bips/blob/master/bip-0044.mediawiki) -- Multi-account hierarchy for deterministic wallets
