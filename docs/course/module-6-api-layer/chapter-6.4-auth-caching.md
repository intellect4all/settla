# Chapter 6.4: Authentication and Three-Level Caching

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Trace the full auth flow from Bearer token to tenant context
2. Explain how the three-level cache (local, Redis, DB) works and why each level exists
3. Calculate the performance impact of removing the local cache at 5,000 TPS
4. Describe cross-instance cache invalidation via Redis pub/sub

---

## The Authentication Flow

Every authenticated request to Settla follows this path:

```
  Client: POST /v1/transfers
  Authorization: Bearer sk_live_abc123...
         |
         v
  +------------------+
  | Extract token    |  authPlugin.ts (onRequest hook)
  | from header      |
  +--------+---------+
           |
  +--------v---------+
  | HMAC-SHA256 hash |  hashApiKey(token)  (keyed with SETTLA_API_KEY_HMAC_SECRET)
  | the raw key      |  "sk_live_abc123..." -> "a1b2c3d4e5..."
  +--------+---------+
           |
  +--------v-----------------------------------+
  |                                            |
  |  L1: Local Map    hit (~100ns)             |
  |  +----------+                              |
  |  | keyHash  |---> TenantAuth               |
  |  +----------+                              |
  |       | miss                               |
  |       v                                    |
  |  L2: Redis        hit (~0.5ms)             |
  |  +----------+                              |
  |  | tenant:  |---> TenantAuth (JSON)        |
  |  | auth:    |     promote to L1            |
  |  | keyHash  |                              |
  |  +----------+                              |
  |       | miss                               |
  |       v                                    |
  |  L3: gRPC -> DB   hit (~2-5ms)             |
  |  +----------+                              |
  |  | ValidateAPIKey RPC                      |
  |  | queries tenants table                   |
  |  +----------+                              |
  |       | found                              |
  |       v                                    |
  |  Store in L1 + L2                          |
  |                                            |
  +--------+-----------------------------------+
           |
           v
  request.tenantAuth = { tenantId, slug, status, feeSchedule, ... }
```

The raw API key never leaves the gateway process. It is hashed immediately, and only the hash crosses any network boundary.

---

## The Auth Plugin: plugin.ts

The auth plugin is registered with `fastify-plugin` (the `fp` wrapper), which makes it run in global scope rather than being encapsulated to a route prefix:

```typescript
import { createHash } from "node:crypto";
import fp from "fastify-plugin";

export const authPlugin = fp(async function authPluginInner(
  app: FastifyInstance,
  opts: AuthPluginOpts,
): Promise<void> {
  const { cache, resolveTenant, redis } = opts;

  app.decorateRequest("tenantAuth", undefined as unknown as TenantAuth);
```

The `decorateRequest` call adds `tenantAuth` to every Fastify request object. This is how downstream route handlers access the authenticated tenant context via `request.tenantAuth`.

### The onRequest Hook

The core auth logic runs as an `onRequest` hook, which fires before any route handler:

```typescript
  app.addHook("onRequest", async (request: FastifyRequest, reply: FastifyReply) => {
    // Skip auth for public endpoints
    if (
      request.url === "/health" ||
      request.url.startsWith("/docs") ||
      request.url.startsWith("/documentation") ||
      request.url === "/metrics" ||
      request.url.startsWith("/webhooks/") ||
      request.url.startsWith("/v1/auth/") ||
      request.url.startsWith("/v1/payment-links/resolve/") ||
      request.url.startsWith("/v1/payment-links/redeem/")
    ) {
      return;
    }

    const authHeader = request.headers.authorization;

    if (!authHeader ||
        (!authHeader.startsWith("Bearer ") && !authHeader.startsWith("bearer "))) {
      reply.code(401).send({
        error: "UNAUTHORIZED",
        message: "Missing or invalid Authorization header",
      });
      return;
    }

    const token = authHeader.slice(7);
```

The URL-based bypass list is checked first. Health endpoints, docs, metrics, inbound provider webhooks, and public auth routes do not require authentication. This check uses `startsWith` for prefix matching rather than regex, which is faster on the hot path.

### Dual Auth: API Keys and JWTs

The gateway supports two authentication methods: API keys for programmatic access and JWTs for the portal UI:

```typescript
    // Dual auth: API keys start with sk_live_ or sk_test_
    const isApiKey = token.startsWith("sk_live_") || token.startsWith("sk_test_");

    if (isApiKey) {
      // API key flow
      const keyHash = hashApiKey(token);

      let auth = cache.getLocal(keyHash);
      if (!auth) {
        auth = await cache.getRedis(keyHash);
      }
      if (!auth) {
        const resolved = await resolveTenant(keyHash);
        if (!resolved) {
          reply.code(401).send({
            error: "UNAUTHORIZED",
            message: "Invalid API key",
          });
          return;
        }
        auth = resolved;
        await cache.set(keyHash, auth);
      }

      if (auth.status !== "ACTIVE") {
        reply.code(403).send({
          error: "FORBIDDEN",
          message: "Tenant is suspended",
        });
        return;
      }

      request.tenantAuth = auth;
    } else {
      // JWT flow (portal users)
      // ... JWT verification and tenant resolution ...
    }
  });
```

The API key flow is the hot path. Look at how it cascades through cache levels:

1. **Try L1 (local Map)** -- synchronous, ~100ns
2. **Try L2 (Redis)** -- async, ~0.5ms
3. **Try L3 (gRPC to DB)** -- async, ~2-5ms
4. **If found at L3, populate L1 + L2** for future requests

After the `ACTIVE` status check, the tenant context is attached to the request.

### The SHA-256 Hash Function

```typescript
export function hashApiKey(key: string): string {
  return createHash("sha256").update(key).digest("hex");
}
```

This is deliberately simple. SHA-256 is not a password hash (bcrypt/scrypt would be too slow for the hot path), but API keys are long random strings (high entropy), so rainbow tables are not a practical attack. The hash serves two purposes: (1) the raw key never hits Redis or the database, and (2) the key is never logged, even accidentally.

---

## The Cache Implementation: cache.ts

The `TenantAuthCache` class implements the two-level cache with explicit TTLs:

```typescript
export interface TenantAuth {
  tenantId: string;
  slug: string;
  status: string;
  feeSchedule: {
    onRampBps: number;
    offRampBps: number;
    minFeeUsd: string;
    maxFeeUsd: string;
  };
  dailyLimitUsd: string;
  perTransferLimit: string;
  userId?: string;     // Set when authenticated via JWT
  userRole?: string;   // Portal user role
}

interface CacheEntry {
  value: TenantAuth;
  expiresAt: number;
}

export class TenantAuthCache {
  private local: Map<string, CacheEntry> = new Map();
  private redis: Redis | null;
  private localTtlMs: number;
  private redisTtlSeconds: number;
  private maxLocalSize = 10_000;
```

The `TenantAuth` interface carries everything the gateway needs for a request: tenant ID, fee schedule, limits, and status. This avoids additional lookups during request processing.

### L1: The Local Map with LRU Eviction

```typescript
  getLocal(keyHash: string): TenantAuth | undefined {
    const entry = this.local.get(keyHash);
    if (!entry) return undefined;
    if (Date.now() > entry.expiresAt) {
      this.local.delete(keyHash);
      return undefined;
    }
    // LRU promotion: delete + re-insert moves entry to end of
    // Map iteration order, so Map.keys().next() always returns
    // the least-recently-used key.
    this.local.delete(keyHash);
    this.local.set(keyHash, entry);
    return entry.value;
  }
```

This is a clever use of JavaScript's `Map` insertion order guarantee. By deleting and re-inserting on every access, the most-recently-used entries move to the end. The least-recently-used entry is always at the front. When the map reaches capacity, eviction is `O(1)`:

```typescript
  private setLocal(keyHash: string, auth: TenantAuth): void {
    if (this.local.size >= this.maxLocalSize) {
      const firstKey = this.local.keys().next().value;
      if (firstKey) this.local.delete(firstKey);
    }
    this.local.set(keyHash, {
      value: auth,
      expiresAt: Date.now() + this.localTtlMs,
    });
  }
```

The `maxLocalSize` of 10,000 entries prevents unbounded memory growth. At roughly 200 bytes per `TenantAuth` object plus overhead, this uses about 4 MB of memory -- negligible for a server process.

### L2: Redis with TTL

```typescript
  async getRedis(keyHash: string): Promise<TenantAuth | undefined> {
    if (!this.redis) return undefined;
    const raw = await this.redis.get(`tenant:auth:${keyHash}`);
    if (!raw) return undefined;
    try {
      const auth = JSON.parse(raw) as TenantAuth;
      // Promote to L1
      this.setLocal(keyHash, auth);
      return auth;
    } catch {
      return undefined;
    }
  }

  async set(keyHash: string, auth: TenantAuth): Promise<void> {
    this.setLocal(keyHash, auth);
    if (this.redis) {
      await this.redis.setex(
        `tenant:auth:${keyHash}`,
        this.redisTtlSeconds,
        JSON.stringify(auth),
      );
    }
  }
```

When a Redis hit occurs, the value is promoted to L1 (local cache). This means the next request for the same key avoids Redis entirely. The Redis key format `tenant:auth:{keyHash}` uses a namespace prefix to avoid collisions with other Redis data.

### TTL Strategy

```
  L1 (Local): 30 seconds
  L2 (Redis): 5 minutes (300 seconds)
  L3 (DB):    Source of truth (no TTL)
```

The TTLs are deliberately asymmetric:

- **L1 = 30 seconds** because local cache is per-process. If a tenant's API key is revoked, the worst case is 30 seconds of continued access on a single gateway instance.
- **L2 = 5 minutes** because Redis is shared across all gateway instances. A longer TTL is acceptable because invalidation can explicitly delete the key.

> **Key Insight: The Math at 5,000 TPS**
>
> Without the local cache, every request requires a Redis round-trip (~0.5ms). At 5,000 TPS:
> `5,000 requests/sec * 0.5ms = 2.5 seconds of aggregate Redis wait time per second`
> This means 2.5 of every second is spent waiting for Redis, creating back-pressure.
>
> With the local cache (30s TTL, ~100ns lookup): a tenant making 100 req/sec hits L3 once, L2 once (for the L1 promotion), then L1 for the next 29.98 seconds. Hit rate > 99.9%. The aggregate cache lookup time drops to:
> `5,000 * 100ns = 0.5ms per second`
> That is a 5,000x reduction.

---

## The Go-Side Local Cache: cache/local.go

The Go backend has its own local cache (`cache.LocalCache`) used by other services. Understanding it illuminates how the TypeScript implementation differs:

```go
type LocalCache struct {
    mu       sync.RWMutex
    entries  map[string]localEntry
    maxSize  int
    nowFunc  func() time.Time // for testing
}

type localEntry struct {
    value      any
    expiresAt  time.Time
    lastAccess time.Time
}
```

### Get with Lazy Expiry and LRU Tracking

```go
func (c *LocalCache) Get(key string) (any, bool) {
    c.mu.RLock()
    entry, ok := c.entries[key]
    c.mu.RUnlock()

    if !ok {
        return nil, false
    }
    now := c.nowFunc()
    if now.After(entry.expiresAt) {
        // Expired -- lazy delete.
        c.mu.Lock()
        // Double-check under write lock in case it was refreshed.
        if e, still := c.entries[key]; still && c.nowFunc().After(e.expiresAt) {
            delete(c.entries, key)
        }
        c.mu.Unlock()
        return nil, false
    }

    // Update last-access time for LRU tracking.
    c.mu.Lock()
    if e, still := c.entries[key]; still {
        e.lastAccess = now
        c.entries[key] = e
    }
    c.mu.Unlock()

    return entry.value, true
}
```

The Go cache uses a `sync.RWMutex` for thread safety (Go is multi-threaded, unlike Node.js). The double-check pattern (check under read lock, then re-check under write lock) prevents a race where another goroutine refreshes the entry between the expiry check and the delete.

### Eviction: Expired First, Then LRU

```go
func (c *LocalCache) Set(key string, value any, ttl time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()

    now := c.nowFunc()

    if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxSize {
        c.evictExpired()
        if len(c.entries) >= c.maxSize {
            c.evictLRU()
        }
    }

    c.entries[key] = localEntry{
        value:      value,
        expiresAt:  now.Add(ttl),
        lastAccess: now,
    }
}

func (c *LocalCache) evictLRU() {
    var oldestKey string
    var oldestTime time.Time
    first := true

    for k, e := range c.entries {
        if first || e.lastAccess.Before(oldestTime) {
            oldestKey = k
            oldestTime = e.lastAccess
            first = false
        }
    }

    if !first {
        delete(c.entries, oldestKey)
    }
}
```

The eviction strategy is: first sweep expired entries (they are free to remove), then if still at capacity, find and evict the least-recently-accessed entry. The LRU scan is `O(n)` but runs rarely -- only when the cache is full and has no expired entries. With a 30-second TTL and steady traffic, most evictions happen through the expired sweep, not the LRU scan.

The `nowFunc` field is a dependency injection point for testing. In tests, you can inject a fake clock to verify TTL behavior without `time.Sleep`:

```go
cache := NewLocalCache(100)
cache.nowFunc = func() time.Time { return fixedTime }
```

---

## Cross-Instance Cache Invalidation (SEC-2)

When an API key is revoked, the local caches on all gateway instances must be cleared immediately. Settla uses Redis pub/sub for this:

```
  Gateway-1          Redis Pub/Sub           Gateway-2
  +--------+     settla:auth:invalidate     +--------+
  | Revoke |                                |        |
  | key    |                                |        |
  +---+----+                                +--------+
      |
      | 1. Delete from L1 + L2
      | 2. Publish keyHash to channel
      |
      +-------> Redis ---------> subscriber on Gateway-2
                                     |
                                     | 3. Delete from L1
                                     |
                                     v
                                  Evicted!
```

The setup in `plugin.ts`:

```typescript
  // Dedicated subscriber connection -- Redis in subscribe mode cannot
  // issue regular commands
  let subscriber: Redis | null = null;
  if (redis) {
    try {
      subscriber = redis.duplicate();
      await subscriber.subscribe(AUTH_INVALIDATE_CHANNEL);
      subscriber.on("message", (_channel: string, keyHash: string) => {
        // Evict from L1 only -- the publisher already deleted from L2
        cache.deleteLocal(keyHash);
        app.log.debug(
          { keyHash: keyHash.slice(0, 8) + "..." },
          "auth: evicted key via pub/sub"
        );
      });
    } catch (err) {
      app.log.warn({ err },
        "auth: failed to subscribe -- cross-instance revocation disabled");
      subscriber = null;
    }
  }
```

The `redis.duplicate()` is necessary because a Redis client in subscribe mode cannot run other commands (GET, SET, etc.). The gateway needs two connections: one for regular operations, one for pub/sub.

The invalidation function that triggers the broadcast:

```typescript
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
          "revoked key until TTL expires"
        );
      }
    }
  });
```

The `cache.invalidate` method deletes from both L1 and L2:

```typescript
  async invalidate(keyHash: string): Promise<void> {
    this.local.delete(keyHash);
    if (this.redis) {
      await this.redis.del(`tenant:auth:${keyHash}`);
    }
  }
```

If Redis pub/sub fails (network partition, Redis down), the system degrades gracefully: the local TTL of 30 seconds becomes the maximum stale window. This is documented in the warning log message.

---

## The Tenant Resolution Function

When L1 and L2 both miss, the gateway calls the Go backend via gRPC:

```typescript
  const resolveTenant = async (keyHash: string) => {
    try {
      const res = await grpcClient.validateApiKey({ keyHash });
      if (!res.valid) return null;

      let feeSchedule = {
        onRampBps: 0, offRampBps: 0,
        minFeeUsd: "0", maxFeeUsd: "0",
      };
      try {
        const raw = JSON.parse(res.feeScheduleJson);
        feeSchedule = {
          onRampBps: raw.onramp_bps ?? raw.onRampBps ?? 0,
          offRampBps: raw.offramp_bps ?? raw.offRampBps ?? 0,
          minFeeUsd: raw.min_fee_usd ?? raw.minFeeUsd ?? "0",
          maxFeeUsd: raw.max_fee_usd ?? raw.maxFeeUsd ?? "0",
        };
      } catch {
        // use defaults
      }

      return {
        tenantId: res.tenantId,
        slug: res.slug,
        status: res.status,
        feeSchedule,
        dailyLimitUsd: res.dailyLimitUsd,
        perTransferLimit: res.perTransferLimit,
      };
    } catch (err) {
      server.log.error(
        { err, keyHash: keyHash.slice(0, 8) + "..." },
        "gRPC auth validation failed"
      );
      return null;
    }
  };
```

The fee schedule JSON parsing handles both `snake_case` and `camelCase` keys (via `??` fallbacks) because the JSON originates from the Go backend, which may use either convention depending on the serialization path. The `catch` around JSON.parse ensures that a malformed fee schedule does not crash the auth flow -- it falls back to zero fees, which is safe (the domain layer will apply the correct fees from its own tenant configuration).

---

## Beyond Auth: Specialized Caches

The three-level auth cache is the highest-frequency cache in the system, but it is not the only one. Two other caches solve problems that cannot be addressed by the auth cache or the general-purpose `LocalCache`: atomic volume tracking and efficient tenant enumeration.

### Daily Volume Counter (`cache/daily_volume.go`)

Every transfer creation must check whether the tenant has exceeded their daily volume limit. At 580 TPS sustained across multiple gateway replicas, this check must be atomic and fast. Querying Postgres on every transfer would add 2-5ms of latency and create a hot row under `SELECT ... FOR UPDATE`. Instead, Settla uses Redis `INCRBYFLOAT` for race-free atomic increments:

```go
// RedisDailyVolumeCounter implements core.DailyVolumeCounter using Redis
// INCRBYFLOAT for atomic, race-free daily volume tracking.
type RedisDailyVolumeCounter struct {
	rc *RedisCache
}

func NewRedisDailyVolumeCounter(rc *RedisCache) *RedisDailyVolumeCounter {
	return &RedisDailyVolumeCounter{rc: rc}
}

func dailyVolumeKey(tenantID uuid.UUID, date time.Time) string {
	return fmt.Sprintf("dailyvol:%s:%s", tenantID, date.Format("2006-01-02"))
}
```

The key format `dailyvol:{tenantID}:{YYYY-MM-DD}` scopes each counter to a single tenant and day. This means each tenant gets a fresh counter at midnight UTC.

**TTL management** is critical. Without a TTL, volume keys would accumulate forever -- one key per tenant per day, with no automatic cleanup. The `ttlUntilEndOfDay` function computes the exact duration until midnight UTC:

```go
// ttlUntilEndOfDay returns the duration until midnight UTC of the given day.
func ttlUntilEndOfDay(date time.Time) time.Duration {
	endOfDay := date.Truncate(24 * time.Hour).Add(24 * time.Hour)
	ttl := time.Until(endOfDay)
	if ttl < time.Minute {
		ttl = time.Minute // minimum 1 minute to avoid instant expiry
	}
	return ttl
}
```

The 1-minute minimum prevents a race condition at midnight: if a transfer arrives at 23:59:59.999, the computed TTL would be near-zero, causing the key to expire before the next `GET`. The minimum ensures the counter survives long enough to be useful.

The three methods cover the full lifecycle:

```go
func (r *RedisDailyVolumeCounter) GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error) {
	val, err := r.rc.GetFloat(ctx, dailyVolumeKey(tenantID, date))
	if err == redis.Nil {
		return decimal.Zero, nil
	}
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-cache: get daily volume: %w", err)
	}
	return decimal.NewFromFloat(val), nil
}

func (r *RedisDailyVolumeCounter) IncrDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time, amount decimal.Decimal) (decimal.Decimal, error) {
	key := dailyVolumeKey(tenantID, date)
	f, _ := amount.Float64()
	newVal, err := r.rc.IncrByFloat(ctx, key, f)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-cache: incr daily volume: %w", err)
	}
	// Ensure the key has a TTL (set on first increment, idempotent).
	r.rc.client.ExpireNX(ctx, key, ttlUntilEndOfDay(date))
	return decimal.NewFromFloat(newVal), nil
}

func (r *RedisDailyVolumeCounter) SeedDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time, amount decimal.Decimal) (bool, error) {
	key := dailyVolumeKey(tenantID, date)
	f, _ := amount.Float64()
	set, err := r.rc.client.SetNX(ctx, key, f, ttlUntilEndOfDay(date)).Result()
	if err != nil {
		return false, fmt.Errorf("settla-cache: seed daily volume: %w", err)
	}
	return set, nil
}
```

- **`GetDailyVolume`** reads the current counter. A `redis.Nil` response (key does not exist) returns zero, meaning the tenant has not transferred anything today.
- **`IncrDailyVolume`** atomically adds the transfer amount and returns the new total. Redis `INCRBYFLOAT` is a single atomic operation -- no read-modify-write race, no distributed locks, no transactions. The `ExpireNX` call sets a TTL only if one is not already set, making it idempotent across concurrent increments.
- **`SeedDailyVolume`** uses `SetNX` (set-if-not-exists) to initialize the counter on startup. This is called during server boot to pre-populate from the database, so the first transfer of the day does not see a zero counter. The `SetNX` ensures that if two replicas boot simultaneously, only one seed wins.

> **Why atomic Redis increment matters at scale:** At 580 TPS sustained, multiple gateway replicas process transfers concurrently. If the volume check used a read-then-write pattern (`GET` + compare + `SET`), two replicas could both read "4,999,000 USD", both allow a 2,000 USD transfer, and the tenant would exceed their 5,000,000 USD daily limit. `INCRBYFLOAT` eliminates this race entirely -- the increment and the new total are returned in a single atomic operation.

### Tenant Index (`cache/tenant_index.go`)

Several background workers need to iterate over all active tenants. The `PositionRebalanceWorker`, for example, scans all active tenants every 30 seconds to check treasury positions. Querying Postgres on every scan would add unnecessary load to the Transfer DB. The `TenantIndex` maintains a Redis SET of active tenant IDs for efficient enumeration:

```go
const tenantIndexKey = "settla:active_tenants"

// TenantIndex maintains a Redis SET of active tenant IDs for efficient iteration.
// Workers use ForEach (SSCAN-based) instead of querying Postgres for all tenant IDs.
// The index is synced on tenant lifecycle events (SADD/SREM) and reconciled
// periodically from Postgres as a safety net.
type TenantIndex struct {
	client   *redis.Client
	fallback domain.TenantPageFetcher
	logger   *slog.Logger
}

func NewTenantIndex(client *redis.Client, fallback domain.TenantPageFetcher, logger *slog.Logger) *TenantIndex {
	return &TenantIndex{
		client:   client,
		fallback: fallback,
		logger:   logger,
	}
}
```

The `fallback` field is a `domain.TenantPageFetcher` -- a paginated Postgres query used when Redis is unavailable. This follows the same graceful degradation pattern as the auth cache.

**Mutation methods** are called on tenant lifecycle events:

```go
// Add registers a tenant as active (SADD). Called when a tenant status transitions to ACTIVE.
func (t *TenantIndex) Add(ctx context.Context, tenantID uuid.UUID) error {
	if err := t.client.SAdd(ctx, tenantIndexKey, tenantID.String()).Err(); err != nil {
		return fmt.Errorf("settla-tenant-index: adding tenant %s: %w", tenantID, err)
	}
	return nil
}

// Remove deregisters a tenant (SREM). Called when a tenant is suspended or deactivated.
func (t *TenantIndex) Remove(ctx context.Context, tenantID uuid.UUID) error {
	if err := t.client.SRem(ctx, tenantIndexKey, tenantID.String()).Err(); err != nil {
		return fmt.Errorf("settla-tenant-index: removing tenant %s: %w", tenantID, err)
	}
	return nil
}
```

**The iteration method** uses `SSCAN` for non-blocking cursor-based traversal:

```go
// ForEach iterates over all active tenant IDs using Redis SSCAN.
// Each batch of up to batchSize IDs is passed to fn.
// If Redis is unavailable, falls back to paginated Postgres queries.
func (t *TenantIndex) ForEach(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
	err := t.forEachRedis(ctx, batchSize, fn)
	if err == nil {
		return nil
	}

	// Redis unavailable — fall back to paginated Postgres
	t.logger.Warn("settla-tenant-index: Redis unavailable, falling back to Postgres", "error", err)
	if t.fallback == nil {
		return fmt.Errorf("settla-tenant-index: Redis unavailable and no fallback configured: %w", err)
	}
	return domain.ForEachTenantBatch(ctx, t.fallback, batchSize, fn)
}

func (t *TenantIndex) forEachRedis(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
	var cursor uint64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		keys, nextCursor, err := t.client.SScan(ctx, tenantIndexKey, cursor, "", int64(batchSize)).Result()
		if err != nil {
			return fmt.Errorf("settla-tenant-index: scanning tenants: %w", err)
		}

		if len(keys) > 0 {
			ids := make([]uuid.UUID, 0, len(keys))
			for _, k := range keys {
				id, err := uuid.Parse(k)
				if err != nil {
					t.logger.Warn("settla-tenant-index: skipping invalid UUID in tenant set", "value", k)
					continue
				}
				ids = append(ids, id)
			}

			if len(ids) > 0 {
				if err := fn(ids); err != nil {
					return err
				}
			}
		}

		if nextCursor == 0 {
			return nil
		}
		cursor = nextCursor
	}
}
```

The `forEachRedis` method uses the standard SSCAN cursor pattern: start with cursor 0, process each batch, advance to the next cursor, stop when the server returns cursor 0. The `select` on `ctx.Done()` ensures the iteration can be cancelled mid-scan if the worker is shutting down.

> **Why SSCAN over SMEMBERS:** With 20,000+ active tenants, `SMEMBERS` would return all members in a single response, blocking the Redis event loop for the entire O(n) operation. During that time, no other client can execute commands. `SSCAN` returns small batches (controlled by `batchSize`), interleaving with other Redis commands between batches. This is the same reason you use `SCAN` instead of `KEYS` in production.

**The rebuild method** handles startup reconciliation:

```go
// Rebuild replaces the Redis SET with a fresh set of active tenant IDs from Postgres.
// Uses paginated queries so it never loads all IDs into memory at once.
// Called at startup and periodically as a reconciliation safety net.
func (t *TenantIndex) Rebuild(ctx context.Context) error {
	if t.fallback == nil {
		return fmt.Errorf("settla-tenant-index: no fallback fetcher configured for rebuild")
	}

	// Use a temporary key + RENAME for atomic swap (no window where the set is empty).
	tmpKey := tenantIndexKey + ":rebuild"
	pipe := t.client.Pipeline()
	pipe.Del(ctx, tmpKey)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("settla-tenant-index: clearing temp key: %w", err)
	}

	var total int
	err = domain.ForEachTenantBatch(ctx, t.fallback, domain.DefaultTenantBatchSize, func(ids []uuid.UUID) error {
		members := make([]any, len(ids))
		for i, id := range ids {
			members[i] = id.String()
		}
		if err := t.client.SAdd(ctx, tmpKey, members...).Err(); err != nil {
			return fmt.Errorf("settla-tenant-index: adding batch to temp key: %w", err)
		}
		total += len(ids)
		return nil
	})
	if err != nil {
		// Clean up temp key on failure
		t.client.Del(ctx, tmpKey)
		return fmt.Errorf("settla-tenant-index: rebuild failed during Postgres iteration: %w", err)
	}

	// Atomic swap
	if err := t.client.Rename(ctx, tmpKey, tenantIndexKey).Err(); err != nil {
		return fmt.Errorf("settla-tenant-index: renaming temp key: %w", err)
	}

	t.logger.Info("settla-tenant-index: rebuild complete", "tenants", total)
	return nil
}
```

The rebuild uses a temporary key + `RENAME` pattern for atomic swap. This is important: if Rebuild wrote directly to `settla:active_tenants` (DEL then re-populate), there would be a window where the set is empty. Any worker calling `ForEach` during that window would see zero tenants. By building the new set in a temporary key and then atomically renaming it, the active set is always complete. If the rebuild fails partway through, the temporary key is cleaned up and the old set remains untouched.

---

## Common Mistakes

1. **Storing raw API keys in the cache.** The cache stores by key hash, not raw key. If raw keys were cached, a Redis dump or memory inspection would reveal all active API keys.

2. **Using the same Redis connection for pub/sub and regular commands.** Redis clients in subscribe mode cannot run GET/SET. You need `redis.duplicate()` for the subscriber.

3. **Setting L1 TTL too high.** A 5-minute local TTL means revoked keys remain valid for 5 minutes on each gateway instance. At 30 seconds, the maximum window is acceptable for most security policies.

4. **Not handling Redis unavailability.** The cache must degrade gracefully. If Redis is down, the system should fall through to L3 (gRPC) for every request rather than throwing errors. The `if (!this.redis) return undefined` checks throughout `TenantAuthCache` handle this.

5. **Trusting the tenant ID from JWT claims without verification.** The auth plugin constructs a minimal `TenantAuth` from JWT claims but marks it with the user ID and role. Portal routes should verify the tenant exists and is active before allowing sensitive operations.

6. **Using SMEMBERS instead of SSCAN for tenant iteration.** `SMEMBERS` returns the entire set in one call, blocking the Redis event loop for O(n) where n could be 20,000+ active tenants. During that time, all other Redis clients are stalled. `SSCAN` returns small batches with cursor-based pagination, interleaving with other commands between batches.

7. **Not setting TTL on daily volume keys.** Without a TTL, volume keys accumulate indefinitely -- one key per tenant per day, never cleaned up. Over a year with 500 tenants, that is 182,500 orphaned keys wasting Redis memory. The `ttlUntilEndOfDay` function ensures keys expire automatically at midnight UTC.

---

## Exercises

1. **Calculate hit rates.** A tenant makes 500 requests per second. L1 TTL is 30 seconds. How many L3 (database) lookups occur per hour for this tenant? How many Redis lookups?

2. **Simulate revocation.** Walk through the invalidation flow step by step when a key is revoked on Gateway-1 while Gateway-2 has the key cached in L1. What is the maximum delay before Gateway-2 stops accepting the revoked key?

3. **Benchmark the local cache.** The TypeScript cache uses `Map.delete()` + `Map.set()` for LRU promotion. The Go cache uses `sync.RWMutex` with `lastAccess` tracking. Which approach is better for single-threaded (Node.js) vs multi-threaded (Go) runtimes? Why?

4. **Design a metric.** Propose a Prometheus metric that tracks L1/L2/L3 hit ratios for the auth cache. What labels would you use? How would you use this metric to tune the TTL values?

---

## What's Next

In Chapter 6.5, we will examine per-tenant rate limiting: how the sliding window algorithm works with local counters synced to Redis, how adaptive load shedding prevents cascade failures, and how graceful SIGTERM drain ensures zero dropped requests during deployments.
