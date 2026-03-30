# Chapter 10.1: API Key Security -- Authentication Without Storing Secrets

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why HMAC-SHA256 is the right choice for API key hashing (and why bcrypt is wrong)
2. Trace the full lifecycle of an API key from generation to revocation
3. Describe the threat model that HMAC-SHA256 defends against (database breach + offline brute-force)
4. Implement a safe key rotation flow with zero downtime
5. Distinguish between test and live key environments and their security implications

---

## The Problem: 5,000 Auth Lookups Per Second

Settla issues API keys to every tenant fintech -- Lemfi, Fincra, Paystack, and hundreds more. Every API request carries a key:

```
Authorization: Bearer sk_live_a1b2c3d4e5f6...
```

At peak, the gateway handles 5,000 of these per second. Each one must be validated before the request touches any business logic. This creates two competing requirements:

1. **Speed.** Authentication is on the hot path. Adding 100ms per request means 100ms added to every transfer, quote, and balance check in the system.

2. **Security.** If an attacker compromises the database, they must not be able to reconstruct valid API keys. A leaked key_hash column should be useless without additional secrets.

The naive approach -- store keys in plaintext -- fails requirement 2. One SQL injection or backup leak exposes every tenant's credentials. The password-hashing approach -- bcrypt or scrypt -- fails requirement 1. Let us see why.

---

## HMAC-SHA256 vs Bcrypt: Choosing the Right Hash

### Why Bcrypt Exists (and Why It Does Not Belong Here)

Bcrypt was designed for passwords. Passwords are low-entropy: humans pick "password123", "Qwerty1!", and their dog's name followed by a year. An attacker with a leaked hash can try billions of candidates per second with a plain hash like SHA-256. Bcrypt solves this by being intentionally slow -- roughly 100ms per hash with a typical work factor. That slowness is the security property:

```
  Bcrypt cost analysis for password hashing:
  -------------------------------------------
  Work factor 12: ~100ms per hash
  Dictionary with 10 million common passwords:
    10,000,000 * 100ms = 11.5 days to try all candidates

  SHA-256: ~1 microsecond per hash
  Same dictionary:
    10,000,000 * 1us = 10 seconds
```

For login flows (a few per second), 100ms is acceptable. For API key validation at 5,000 TPS, it is not:

```
  Bcrypt at 5,000 TPS:
  5,000 req/sec * 100ms = 500 seconds of CPU time per second
  --> You need 500 CPU cores just for hashing
  --> Plus you block every request for 100ms minimum

  HMAC-SHA256 at 5,000 TPS:
  5,000 req/sec * 1us = 5ms of CPU time per second
  --> Negligible overhead
```

### Why HMAC-SHA256 Works for API Keys

API keys are fundamentally different from passwords:

| Property          | Passwords               | API Keys                      |
|-------------------|-------------------------|-------------------------------|
| Entropy           | Low (8-20 chars, human) | High (32 random bytes, 256 bits) |
| Brute-force risk  | High (dictionary attack) | None (2^256 keyspace)         |
| Rainbow tables    | Effective               | Useless (key is unique random) |
| Lookup frequency  | Rare (login)            | Every request (5,000/sec)     |
| Needs slow hash?  | Yes (compensates for low entropy) | No (entropy is already high) |

Settla generates API keys with 32 bytes of cryptographic randomness (256 bits of entropy). Even with a fast hash, an attacker trying to brute-force the keyspace faces:

```
  2^256 possible keys
  At 10 billion hashes/second (optimistic GPU cluster):
  2^256 / 10^10 = 1.16 * 10^67 seconds
                = 3.67 * 10^59 years

  The universe is ~1.4 * 10^10 years old.
```

Rainbow tables are equally useless. A rainbow table is a precomputed mapping from hash to input. It only works when inputs are predictable (common passwords). With 256-bit random keys, the input space is too large to precompute.

### The HMAC Secret: Defense in Depth

Plain SHA-256 would technically suffice given the key entropy, but Settla uses HMAC-SHA256 -- a keyed hash -- for defense in depth. The difference:

```
  Plain SHA-256:    hash = SHA256(api_key)
  HMAC-SHA256:      hash = HMAC-SHA256(server_secret, api_key)
```

With plain SHA-256, an attacker who leaks the key_hash column can verify candidate keys offline (hash the candidate and compare). With HMAC-SHA256, they also need the server-side secret (`SETTLA_API_KEY_HMAC_SECRET`) to compute the hash. This secret is stored separately from the database -- in environment variables, a secrets manager (Vault, AWS Secrets Manager), or a hardware security module.

```
  Threat model:

  +----------------+     +------------------+     +-----------------+
  | Attacker gets  |     | Attacker also    |     | Attacker can    |
  | key_hash       |     | needs HMAC       |     | verify keys     |
  | column from DB | --> | secret from env  | --> | offline         |
  +----------------+     +------------------+     +-----------------+

  With HMAC: attacker must breach TWO systems (DB + secrets store)
  Without:   attacker needs only ONE system (DB)
```

> **Key Insight: The HMAC secret (`SETTLA_API_KEY_HMAC_SECRET`) is the crown jewel of the authentication system. If this leaks alongside the database, all API key hashes can be verified offline. This secret must be rotated independently of API keys, stored in a dedicated secrets manager, and never committed to version control or logged.**

---

## The Authentication Flow

Let us trace what happens when a tenant sends `Authorization: Bearer sk_live_a1b2c3...` to the gateway. The auth plugin (`api/gateway/src/auth/plugin.ts`) runs as an `onRequest` hook -- before any route handler executes.

### Step 1: Extract the Token

```typescript
// From api/gateway/src/auth/plugin.ts

app.addHook(
  "onRequest",
  async (request: FastifyRequest, reply: FastifyReply) => {
    // Skip auth for health/docs/metrics/webhook endpoints
    if (
      request.url === "/health" ||
      request.url.startsWith("/docs") ||
      request.url.startsWith("/documentation") ||
      request.url === "/openapi.json" ||
      request.url === "/metrics" ||
      request.url.startsWith("/webhooks/") ||
      request.url.startsWith("/v1/auth/") ||
      request.url.startsWith("/v1/ops/")
    ) {
      return;
    }

    // Check per-IP auth failure rate limit before processing credentials.
    if (checkAuthFailureRateLimit(request.ip)) {
      return reply
        .status(429)
        .header("Retry-After", "60")
        .send({
          error: "TOO_MANY_AUTH_FAILURES",
          message: "Too many failed authentication attempts. Try again later.",
        });
    }

    const authHeader = request.headers.authorization;

    if (!authHeader ||
        (!authHeader.startsWith("Bearer ") && !authHeader.startsWith("bearer "))) {
      recordAuthFailure(request.ip);
      reply.code(401).send({
        error: "UNAUTHORIZED",
        message: "Missing or invalid Authorization header",
      });
      return;
    }

    const token = authHeader.slice(7);
```

Notice the order: public routes are skipped first, then the auth failure rate limit is checked (preventing brute-force attempts), then the header is extracted. The `authHeader.slice(7)` strips the "Bearer " prefix to get the raw token.

### Step 2: Determine Auth Method

The gateway supports two auth methods -- API keys and JWTs -- distinguished by prefix:

```typescript
    // Dual auth: API keys start with sk_live_ or sk_test_
    const isApiKey = token.startsWith("sk_live_") || token.startsWith("sk_test_");
```

API keys always start with `sk_live_` or `sk_test_`. Anything else is treated as a JWT (for the tenant portal). This prefix convention makes routing unambiguous.

### Step 3: Hash the Key with HMAC-SHA256

```typescript
// From api/gateway/src/auth/plugin.ts

const API_KEY_HMAC_SECRET = process.env.SETTLA_API_KEY_HMAC_SECRET || "";

export function hashApiKey(key: string): string {
  if (API_KEY_HMAC_SECRET) {
    return createHmac("sha256", API_KEY_HMAC_SECRET).update(key).digest("hex");
  }
  return createHash("sha256").update(key).digest("hex");
}
```

The function checks for the HMAC secret at module load time. In production, the secret must be present -- the gateway refuses to start without it:

```typescript
if (!API_KEY_HMAC_SECRET) {
  const env = process.env.SETTLA_ENV || process.env.NODE_ENV || "development";
  if (env === "production") {
    console.error(
      "FATAL: SETTLA_API_KEY_HMAC_SECRET is required in production " +
      "-- refusing to start with plain SHA-256 API key hashing",
    );
    process.exit(1);
  } else {
    console.warn(
      "WARNING: SETTLA_API_KEY_HMAC_SECRET is not set " +
      "-- API keys hashed with plain SHA-256 (not safe for production)",
    );
  }
}
```

This is a hard fail in production, not a warning. Running without the HMAC secret would silently downgrade security for every tenant.

### Step 4: Cache Lookup (L1 -> L2 -> L3)

The hash is used to look up tenant context through three cache levels:

```typescript
    if (isApiKey) {
      const keyHash = hashApiKey(token);

      let cacheSource: "l1" | "l2" | "l3" = "l3";

      let auth = cache.getLocal(keyHash);
      if (auth) {
        cacheSource = "l1";
      }
      if (!auth) {
        auth = await cache.getRedis(keyHash);
        if (auth) cacheSource = "l2";
      }
      if (!auth) {
        const resolved = await resolveTenant(keyHash);
        if (!resolved) {
          recordAuthFailure(request.ip);
          reply.code(401).send({
            error: "UNAUTHORIZED",
            message: "Invalid API key",
          });
          return;
        }
        auth = resolved;
        cacheSource = "l3";
        await cache.set(keyHash, auth);
      }
```

The full flow as an ASCII diagram:

```
  sk_live_a1b2c3d4e5f6...
       |
       v
  HMAC-SHA256(secret, key)  -->  "7f3a9c1e..."
       |
       v
  +----+------------------------------------------+
  |  L1: Local in-process Map    (~100ns)         |
  |  +------------+                               |
  |  | 7f3a9c1e...|---> TenantAuth (hit? done)   |
  |  +-----+------+                               |
  |        | miss                                  |
  |        v                                       |
  |  L2: Redis                   (~0.5ms)         |
  |  +------------+                               |
  |  | tenant:    |---> TenantAuth (JSON)         |
  |  | auth:      |     + promote to L1           |
  |  | 7f3a9c1e...|                               |
  |  +-----+------+                               |
  |        | miss                                  |
  |        v                                       |
  |  L3: gRPC -> Postgres        (~2-5ms)         |
  |  +------------+                               |
  |  | SELECT FROM api_keys                       |
  |  | JOIN tenants                               |
  |  | WHERE key_hash = '7f3a9c1e...'             |
  |  |   AND is_active = true                     |
  |  +-----+------+                               |
  |        | found                                 |
  |        v                                       |
  |  Store in L1 + L2 for next request            |
  +-----------------------------------------------+
       |
       v
  request.tenantAuth = { tenantId, slug, feeSchedule, ... }
```

The raw key never crosses a network boundary. It is hashed in-process, and only the hash is sent to Redis or Postgres. Even if Redis traffic is sniffed, the attacker sees hashes, not keys.

### Step 5: Periodic DB Revalidation

When a key is served from L2 (Redis cache), the plugin periodically revalidates against the database to narrow the revocation window:

```typescript
      if (cacheSource === "l2") {
        const lastDbCheckKey = `dbcheck:${keyHash}`;
        const lastCheck = dbRevalidationTimestamps.get(lastDbCheckKey) ?? 0;
        const now = Date.now();
        if (now - lastCheck > DB_REVALIDATION_INTERVAL_MS) {
          dbRevalidationTimestamps.set(lastDbCheckKey, now);
          try {
            const freshAuth = await resolveTenant(keyHash);
            if (!freshAuth) {
              // Key was revoked -- evict and reject
              await cache.invalidate(keyHash);
              reply.code(401).send({
                error: "UNAUTHORIZED",
                message: "API key has been revoked",
              });
              return;
            }
            auth = freshAuth;
            await cache.set(keyHash, auth);
          } catch (err) {
            // DB check failed -- allow request with cached data
            app.log.warn(
              { err, keyHash: keyHash.slice(0, 8) + "..." },
              "auth: DB revalidation failed, using cached auth",
            );
          }
        }
      }
```

This is a trade-off: the L2 cache has a 5-minute TTL, but the DB revalidation check fires every 10 seconds (configurable via `SETTLA_DB_REVALIDATION_INTERVAL_MS`). If a key is revoked and the Redis pub/sub invalidation misses a gateway instance (network partition, subscriber error), the worst-case revocation window shrinks from 5 minutes to ~10 seconds.

Notice the error handling: if the DB check fails (database momentarily unreachable), the request proceeds with cached data. This is a deliberate availability-over-consistency choice -- better to serve a potentially-stale auth than to reject all traffic during a transient outage.

### Step 6: Auth Failure Rate Limiting

Failed auth attempts are tracked per source IP:

```typescript
const AUTH_FAILURE_RATE_LIMIT = 10;
const AUTH_FAILURE_WINDOW_MS = 60_000;

function checkAuthFailureRateLimit(ip: string): boolean {
  const now = Date.now();
  let entry = authFailureRateLimitMap.get(ip);
  if (!entry || now - entry.windowStart >= AUTH_FAILURE_WINDOW_MS) {
    return false; // no failures in current window
  }
  return entry.count >= AUTH_FAILURE_RATE_LIMIT;
}

function recordAuthFailure(ip: string): void {
  const now = Date.now();
  let entry = authFailureRateLimitMap.get(ip);
  if (!entry || now - entry.windowStart >= AUTH_FAILURE_WINDOW_MS) {
    entry = { count: 1, windowStart: now };
    authFailureRateLimitMap.set(ip, entry);
  } else {
    entry.count++;
  }
}
```

An IP address that sends 10 invalid keys within 60 seconds is blocked for the remainder of the window. This prevents brute-force key enumeration: even if an attacker could generate candidate keys fast enough, the gateway stops responding after 10 failures.

The rate limit map is cleaned up every 60 seconds to prevent unbounded memory growth from scanning attacks:

```typescript
const authFailureCleanupTimer = setInterval(() => {
  const cutoff = Date.now() - 120_000;
  for (const [key, entry] of authFailureRateLimitMap) {
    if (entry.windowStart < cutoff) {
      authFailureRateLimitMap.delete(key);
    }
  }
}, 60_000);
authFailureCleanupTimer.unref();
```

The `unref()` call ensures the timer does not prevent Node.js from shutting down cleanly.

---

## Key Generation and Storage

API keys are generated on the Go backend when a tenant requests a new key through the portal. The generation function lives in `api/grpc/tenant_portal_service.go`:

```go
// From api/grpc/tenant_portal_service.go

func generateAPIKey(environment string, hmacSecret []byte) (rawKey, keyHash, keyPrefix string, err error) {
    buf := make([]byte, 32)
    if _, err = rand.Read(buf); err != nil {
        return "", "", "", fmt.Errorf("generating random bytes: %w", err)
    }

    prefix := "sk_live_"
    if environment == "TEST" {
        prefix = "sk_test_"
    }

    rawKey = prefix + hex.EncodeToString(buf)
    keyHash = hashAPIKey(rawKey, hmacSecret)
    keyPrefix = rawKey[:12] // e.g. "sk_live_ab3c"

    return rawKey, keyHash, keyPrefix, nil
}
```

Let us trace what this produces:

```
  1. crypto/rand generates 32 random bytes (256 bits)
     buf = [0xa1, 0xb2, 0xc3, ...]  (cryptographically random)

  2. Hex-encode and prepend prefix:
     rawKey = "sk_live_EXAMPLE_KEY_DO_NOT_USE_0000000000000000000000000000000000"
     (8-char prefix + 64-char hex = 72 characters total)

  3. HMAC-SHA256 hash for storage:
     keyHash = HMAC-SHA256(hmacSecret, rawKey)
             = "7f3a9c1e4b..."  (64-char hex)

  4. Save first 12 chars as prefix for display:
     keyPrefix = "sk_live_a1b2"
```

The raw key is returned to the tenant exactly once and never stored:

```go
func (s *Server) CreateAPIKey(ctx context.Context, req *pb.CreateAPIKeyRequest) (*pb.CreateAPIKeyResponse, error) {
    // ... validation ...

    rawKey, keyHash, keyPrefix, err := generateAPIKey(env, s.apiKeyHMACSecret)
    if err != nil {
        return nil, status.Error(codes.Internal, "failed to generate API key")
    }

    key := &domain.APIKey{
        TenantID:    tenantID,
        KeyHash:     keyHash,      // Only the hash is persisted
        KeyPrefix:   keyPrefix,    // For display: "sk_live_a1b2"
        Environment: env,
        Name:        req.GetName(),
        IsActive:    true,
    }

    if err := s.portalStore.CreateAPIKey(ctx, key); err != nil {
        return nil, mapDomainError(err)
    }

    return &pb.CreateAPIKeyResponse{
        Key:    apiKeyToProto(key),
        RawKey: rawKey,              // Shown once, never stored
    }, nil
}
```

The HMAC hash function on the Go side mirrors the TypeScript implementation:

```go
// From api/grpc/tenant_portal_service.go

func hashAPIKey(rawKey string, hmacSecret []byte) string {
    if len(hmacSecret) > 0 {
        mac := hmac.New(sha256.New, hmacSecret)
        mac.Write([]byte(rawKey))
        return hex.EncodeToString(mac.Sum(nil))
    }
    hash := sha256.Sum256([]byte(rawKey))
    return hex.EncodeToString(hash[:])
}
```

Both the Go backend and the TypeScript gateway must use the same HMAC secret. If they diverge, hashes will not match and all API keys will appear invalid.

### The Database Schema

The `api_keys` table stores only hashes, never raw keys:

```sql
-- From db/migrations/transfer/000001_create_tenants.up.sql

CREATE TABLE api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    key_hash        TEXT NOT NULL UNIQUE,       -- HMAC-SHA256 hash, never the raw key
    key_prefix      TEXT NOT NULL,              -- "sk_live_a1b2" for display
    environment     TEXT NOT NULL CHECK (environment IN ('LIVE', 'TEST')),
    name            TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    last_used_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_id ON api_keys(tenant_id);
```

A partial index accelerates the hot-path lookup by excluding deactivated keys:

```sql
-- From db/migrations/transfer/000020_add_api_keys_active_hash_index.up.sql

CREATE INDEX IF NOT EXISTS idx_api_keys_active_hash
  ON api_keys(key_hash)
  WHERE is_active = true;
```

The `ValidateAPIKey` query uses this partial index:

```sql
-- From db/queries/transfer/tenants.sql

-- name: ValidateAPIKey :one
SELECT ak.id, ak.tenant_id, ak.environment, ak.is_active, ak.expires_at,
       t.id AS tenant_uuid, t.slug, t.status AS tenant_status,
       t.fee_schedule, t.settlement_model,
       t.daily_limit_usd, t.per_transfer_limit,
       t.webhook_url, t.webhook_secret
FROM api_keys ak
JOIN tenants t ON t.id = ak.tenant_id
WHERE ak.key_hash = $1
  AND ak.is_active = true
  AND (ak.expires_at IS NULL OR ak.expires_at > now());
```

This query returns the full tenant context in a single round-trip: tenant ID, slug, status, fee schedule, limits. The partial index on `(key_hash) WHERE is_active = true` means deactivated keys do not even appear in the index, making lookups slightly faster and preventing accidental authentication with revoked keys at the database level.

### What Gets Stored vs What Gets Discarded

```
  +------------------+--------------------+------------------+
  |  Generated       |  Stored in DB      |  Returned to     |
  |                  |                    |  tenant          |
  +------------------+--------------------+------------------+
  | rawKey           |  NO (discarded     |  YES (once, at   |
  | "sk_live_a1b...")|  after hashing)    |  creation only)  |
  +------------------+--------------------+------------------+
  | keyHash          |  YES (api_keys.    |  NO (internal)   |
  | "7f3a9c..."      |  key_hash column)  |                  |
  +------------------+--------------------+------------------+
  | keyPrefix        |  YES (api_keys.    |  YES (for key    |
  | "sk_live_a1b2"   |  key_prefix column)|  management UI)  |
  +------------------+--------------------+------------------+
  | HMAC secret      |  NO (env var /     |  NO (server-     |
  | "mysecret..."    |  secrets manager)  |  side only)      |
  +------------------+--------------------+------------------+
```

If a tenant loses their API key, they cannot recover it. They must generate a new one. This is by design -- the same principle as password reset flows. If a system can show you your existing password, it is storing it insecurely.

---

## Key Rotation

Key rotation is the process of replacing an active API key with a new one. Settla supports this as a first-class operation.

### Why Rotate?

- **Key compromise.** If a key is accidentally committed to a public repository, logged, or exposed in a support ticket, it must be replaced immediately.
- **Employee departure.** When an engineer who had access to keys leaves the organization, rotating keys limits the blast radius.
- **Compliance.** Many compliance frameworks (PCI-DSS, SOC 2) require periodic credential rotation.
- **Principle of least privilege over time.** The longer a key exists, the more likely it has been copied to places it should not be (CI/CD config, developer laptops, Slack messages).

### Zero-Downtime Rotation Flow

Settla supports multiple active keys per tenant. This enables zero-downtime rotation:

```
  Time    Tenant's Active Keys     Action
  ----    --------------------     ------
  T+0     [key_A]                  Generate new key_B
  T+1     [key_A, key_B]          Deploy key_B to production systems
  T+2     [key_A, key_B]          Verify key_B works (test transfer)
  T+3     [key_B]                  Revoke key_A
```

The `RotateAPIKey` RPC handles this atomically -- it deactivates the old key and creates a new one in a single call:

```go
// From api/grpc/tenant_portal_service.go

func (s *Server) RotateAPIKey(ctx context.Context, req *pb.RotateAPIKeyRequest) (*pb.RotateAPIKeyResponse, error) {
    tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
    if err != nil {
        return nil, err
    }

    oldKeyID, err := parseUUID(req.GetOldKeyId(), "old_key_id")
    if err != nil {
        return nil, err
    }

    // Verify old key belongs to this tenant
    oldKey, err := s.portalStore.GetAPIKeyByIDAndTenant(ctx, tenantID, oldKeyID)
    if err != nil {
        return nil, mapDomainError(err)
    }

    // Deactivate old key
    if err := s.portalStore.DeactivateAPIKeyByTenant(ctx, tenantID, oldKeyID); err != nil {
        return nil, mapDomainError(err)
    }

    // Create new key with same environment
    rawKey, keyHash, keyPrefix, err := generateAPIKey(oldKey.Environment, s.apiKeyHMACSecret)
    if err != nil {
        return nil, status.Error(codes.Internal, "failed to generate replacement API key")
    }

    newKey := &domain.APIKey{
        TenantID:    tenantID,
        KeyHash:     keyHash,
        KeyPrefix:   keyPrefix,
        Environment: oldKey.Environment,
        Name:        req.GetName(),
        IsActive:    true,
    }

    if err := s.portalStore.CreateAPIKey(ctx, newKey); err != nil {
        return nil, mapDomainError(err)
    }

    return &pb.RotateAPIKeyResponse{
        Key:    apiKeyToProto(newKey),
        RawKey: rawKey,
    }, nil
}
```

For tenants that need zero-downtime, the recommended flow is: create a second key, deploy it, verify it, then revoke the old one. The `RotateAPIKey` RPC is a convenience shortcut for cases where a brief window is acceptable.

### Revocation and Cache Invalidation

When a key is revoked, the hash is returned so the gateway can immediately evict it from all caches:

```go
// From api/grpc/tenant_portal_service.go

func (s *Server) RevokeAPIKey(ctx context.Context, req *pb.RevokeAPIKeyRequest) (*pb.RevokeAPIKeyResponse, error) {
    // ... validation ...

    // Fetch the key hash BEFORE deactivating so we can return it
    // for immediate auth cache invalidation.
    existingKey, err := s.portalStore.GetAPIKeyByIDAndTenant(ctx, tenantID, keyID)
    if err != nil {
        return nil, mapDomainError(err)
    }

    if err := s.portalStore.DeactivateAPIKeyByTenant(ctx, tenantID, keyID); err != nil {
        return nil, mapDomainError(err)
    }

    return &pb.RevokeAPIKeyResponse{KeyHash: existingKey.KeyHash}, nil
}
```

The gateway receives the hash and calls `invalidateAuthCache`, which evicts from L1 (local), L2 (Redis), and broadcasts to all peer gateway instances via Redis pub/sub:

```typescript
// From api/gateway/src/auth/plugin.ts

app.decorate("invalidateAuthCache", async (keyHash: string): Promise<void> => {
  // L1 + L2 eviction
  await cache.invalidate(keyHash);

  // Broadcast to peers
  if (redis) {
    try {
      await redis.publish(AUTH_INVALIDATE_CHANNEL, keyHash);
    } catch (err) {
      app.log.warn(
        { err, keyHash: keyHash.slice(0, 8) + "..." },
        "auth: failed to publish invalidation -- peer gateways may serve " +
        "revoked key until TTL expires",
      );
    }
  }
});
```

The invalidation is best-effort. If Redis pub/sub fails, the local cache TTL (30 seconds) becomes the revocation window. The DB revalidation check (every 10 seconds) further narrows this window for keys served from L2.

### Audit Trail

Both creation and revocation are audit-logged with the key prefix (never the full key or hash):

```go
auditLog(ctx, s.auditLogger, s.logger, domain.AuditEntry{
    TenantID:   tenantID,
    ActorType:  "api_key",
    ActorID:    tenantID.String(),
    Action:     "api_key.rotated",
    EntityType: "api_key",
    EntityID:   &newKey.ID,
    OldValue:   mustJSON(map[string]string{
        "old_key_id":     oldKeyID.String(),
        "old_key_prefix": oldKey.KeyPrefix,
    }),
    NewValue:   mustJSON(map[string]string{
        "new_key_prefix": keyPrefix,
        "environment":    oldKey.Environment,
        "name":           name,
    }),
})
```

The prefix ("sk_live_a1b2") gives enough context to identify which key was rotated without revealing the full key or its hash.

---

## Test vs Live Keys

Settla uses key prefixes to distinguish environments:

```
  sk_test_  -->  Sandbox environment (no real money moves)
  sk_live_  -->  Production environment (real settlements)
```

### How It Works

The environment is determined at generation time and encoded in the prefix:

```go
prefix := "sk_live_"
if environment == "TEST" {
    prefix = "sk_test_"
}
```

The gateway determines auth type by prefix:

```typescript
const isApiKey = token.startsWith("sk_live_") || token.startsWith("sk_test_");
```

Both key types go through the exact same authentication flow -- same HMAC hash, same cache lookup, same tenant resolution. The `environment` field on the resolved tenant context tells downstream services whether to use real providers or sandbox mocks.

### Security Implications

| Property              | sk_test_                    | sk_live_                     |
|-----------------------|-----------------------------|------------------------------|
| Requires KYB          | No                          | Yes (tenant.kyb_status = VERIFIED) |
| Moves real money      | No (sandbox providers)      | Yes                          |
| Rate limits           | Same                        | Same                         |
| Auth flow             | Identical                   | Identical                    |
| Can create transfers  | Yes (sandbox only)          | Yes (real settlements)       |
| Stored in same table  | Yes (api_keys)              | Yes (api_keys)               |

The environment distinction happens at the business logic layer, not the auth layer. This is intentional: it means the security properties (HMAC hashing, cache invalidation, rate limiting) are identical for both environments. A test key has the same cryptographic strength as a live key.

### Environment Validation

The `CreateAPIKey` endpoint validates that the environment is either "LIVE" or "TEST":

```go
env := req.GetEnvironment()
if env != "LIVE" && env != "TEST" {
    return nil, status.Error(codes.InvalidArgument, "environment must be LIVE or TEST")
}
```

Live keys should only be issued to tenants that have completed KYB (Know Your Business) verification. This check is enforced at the portal layer, not the key generation layer, because the same `CreateAPIKey` RPC is used by both the tenant portal and internal admin tools.

---

## The Complete Security Model

Putting it all together, here is what an attacker needs to compromise API key authentication:

```
  Attack Vector             What They Get          Can They Auth?
  -------------------------+-----------------------+---------------
  Steal raw key from       | The actual key        | YES -- this is
  tenant's system          | "sk_live_a1b2..."     | the real threat
  -------------------------+-----------------------+---------------
  Breach Transfer DB       | key_hash column       | NO -- need HMAC
  (SQL injection, backup)  | "7f3a9c1e..."         | secret too
  -------------------------+-----------------------+---------------
  Breach Redis cache       | Serialized TenantAuth | NO -- this is
                           | (no key or hash)      | context, not cred
  -------------------------+-----------------------+---------------
  Breach DB + HMAC secret  | hash + ability to     | YES -- can verify
  (two-system compromise)  | compute hashes        | candidate keys
  -------------------------+-----------------------+---------------
  Sniff gateway traffic    | HMAC hash (sent to    | NO -- cannot
  (network interception)   | Redis/gRPC)           | reverse the hash
  -------------------------+-----------------------+---------------
  Brute-force API endpoint | 10 attempts per       | NO -- rate limited
  (try random keys)        | minute per IP         | at 10 failures/min
```

The primary threat is key theft from the tenant's own systems (committed to git, logged, sent in Slack). Settla mitigates this through rotation support, per-key revocation, and audit logging. But the fundamental responsibility for key custody lies with the tenant.

---

## Common Mistakes

1. **Using bcrypt for API key hashing.** Bcrypt is the right choice for passwords, but its intentional slowness (100ms) makes it unusable on the hot path at 5,000 TPS. API keys have enough entropy (256 bits) that a fast keyed hash (HMAC-SHA256) provides equivalent security.

2. **Storing raw API keys in the database.** Even encrypted-at-rest (EBS, TDE), raw keys in the database mean a SQL injection or backup leak exposes all tenant credentials. Always store only the HMAC hash.

3. **Using the same secret for HMAC hashing and other purposes.** The `SETTLA_API_KEY_HMAC_SECRET` should be dedicated to API key hashing. Reusing it for webhook signatures, JWT signing, or other purposes means a compromise of any system exposes the key authentication secret.

4. **Logging API keys.** Access logs, error messages, and debugging output must never contain raw keys. Settla logs only the key prefix (`keyHash.slice(0, 8) + "..."`) and only after hashing. Even structured logging with request bodies must redact the Authorization header.

5. **Allowing unlimited auth failures.** Without per-IP rate limiting, an attacker can try millions of keys per second. The 10-failure-per-minute limit makes enumeration attacks impractical, even with a botnet (each IP gets only 10 attempts).

6. **Not failing hard when the HMAC secret is missing in production.** The gateway calls `process.exit(1)` if `SETTLA_API_KEY_HMAC_SECRET` is unset in production. A softer approach (log a warning, fall back to plain SHA-256) would silently downgrade security for every tenant. Fail loud, fail early.

7. **Returning the raw key after creation more than once.** The `CreateAPIKey` and `RotateAPIKey` endpoints return the raw key exactly once in the response. There is no "show key again" endpoint. If the tenant does not save it, they must generate a new one. Any endpoint that retrieves an existing key should return only the prefix.

8. **Not matching HMAC secrets between Go and TypeScript.** The Go backend generates the hash with `hmac.New(sha256.New, hmacSecret)` and the TypeScript gateway verifies with `createHmac("sha256", API_KEY_HMAC_SECRET)`. Both must use the same secret value, or keys generated by Go will not validate in the gateway.

---

## Exercises

1. **Calculate the brute-force cost.** Settla API keys are 32 random bytes (256 bits of entropy). An attacker has a GPU cluster that computes 10 billion HMAC-SHA256 hashes per second. How long would it take to find a valid key by brute force? How does this compare to the age of the universe?

2. **Trace a revocation.** A tenant revokes key `sk_live_a1b2...` via the portal. Gateway-1 processes the revocation. Gateway-2 has the key cached in L1 (local) with 25 seconds remaining on its TTL. Walk through every step from the revocation request to the moment Gateway-2 stops accepting the revoked key. What is the maximum delay? What if Redis pub/sub is temporarily down?

3. **Design a key rotation policy.** A fintech tenant has 3 microservices using the same API key. Design a zero-downtime rotation procedure that: (a) generates a new key, (b) deploys it to all services, (c) verifies it works, and (d) revokes the old key. What happens if step (c) fails? How do you roll back?

4. **Compare hash strategies.** An engineer proposes switching from HMAC-SHA256 to Argon2id for API key hashing, arguing it is "more secure." Write a technical analysis explaining: (a) the performance impact at 5,000 TPS, (b) whether the additional security is meaningful given 256-bit key entropy, and (c) under what circumstances (if any) Argon2id would be the better choice.

5. **Audit the schema.** Review the `api_keys` table schema. The `key_hash` column has a `UNIQUE` constraint. What happens if two different raw keys produce the same HMAC-SHA256 hash (a collision)? Is this a realistic concern? How would you detect it if it happened? (Hint: consider the birthday paradox and the output size of SHA-256.)

6. **Extend the model.** Settla currently has two environments: LIVE and TEST. Design a third environment -- STAGING -- that connects to real provider sandboxes but uses a separate fee schedule. What changes are needed in: (a) the `generateAPIKey` function, (b) the `api_keys` table schema, (c) the gateway auth flow, and (d) the `ValidateAPIKey` SQL query?

---

## What's Next

In Chapter 10.2, we will examine tenant isolation -- how Settla ensures that one tenant can never access another tenant's data, even when they share the same database, cache, and message bus. We will trace the tenant_id filter through every layer from the gateway to the database, and examine the Row-Level Security policies that provide a defense-in-depth safety net.
