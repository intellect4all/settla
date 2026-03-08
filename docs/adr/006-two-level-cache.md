# ADR-006: Two-Level Cache (Local LRU + Redis)

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla's API gateway must authenticate every inbound request by resolving an API key (`sk_live_xxx`) to a tenant record. At 5,000 TPS peak, this means 5,000 auth lookups/second. Each lookup must resolve the tenant_id, fee schedule, rate limits, and active status.

We evaluated caching strategies:

| Approach | Lookup latency | Throughput capacity | Staleness risk |
|----------|---------------|-------------------|----------------|
| No cache (DB every request) | ~2ms | ~500 TPS (connection pool bound) | None |
| Redis only | ~0.5ms | ~5,000 TPS | 5 min max |
| Local LRU only | ~107ns | ~50,000+ TPS | 30s max, per-process |
| Two-level (Local → Redis → DB) | ~107ns (99%+ hits) | ~50,000+ TPS | 30s max |

**The threshold: at 5,000 auth lookups/sec, Redis-only caching adds 2,500ms of cumulative network latency per second** (5,000 × 0.5ms). While individual requests see only 0.5ms overhead, the aggregate network I/O saturates the Redis connection pool and competes with other Redis operations (rate limiting, idempotency checks, quote caching).

**The second threshold: at 5,000 TPS with DB-only lookups, the Transfer DB connection pool is exhausted.** Each auth query holds a connection for ~2ms. At 5,000 concurrent requests, that is 10 connection-seconds/second — more than a 100-connection PgBouncer pool can sustain.

A local in-process LRU cache eliminates both problems: lookups complete in ~107ns (measured) with zero network I/O. The 30-second TTL means a tenant's auth data is fetched at most once per 30 seconds per gateway instance, reducing DB load from 5,000 queries/sec to ~1 query/30sec per tenant per instance.

## Decision

We implement a **two-level cache** with three tiers: L1 local in-process LRU, L2 Redis, L3 database (source of truth).

### Cache Tiers

| Tier | Technology | TTL | Latency | Purpose |
|------|-----------|-----|---------|---------|
| L1 | In-process LRU (Go `sync.Map` + TTL wrapper) | 30 seconds | ~107ns | Hot path — serves 99%+ of lookups |
| L2 | Redis | 5 minutes | ~0.5ms | Cross-instance shared cache, survives process restart |
| L3 | Postgres (Transfer DB) | Source of truth | ~2ms | Authoritative tenant data |

### Lookup Flow
```
Request → SHA-256(API key) → L1 check
    → HIT: return tenant (107ns)
    → MISS: L2 check (Redis)
        → HIT: populate L1, return tenant (0.5ms)
        → MISS: L3 query (Postgres)
            → HIT: populate L2 + L1, return tenant (2ms)
            → MISS: return 401 Unauthorized
```

### Cache Scope
The two-level cache is used for:
- **Auth resolution**: API key → tenant record (primary use case, highest throughput)
- **Rate limit counters**: per-tenant request counts (local counters synced to Redis every 5 seconds)
- **Idempotency keys**: request dedup (L1 30s + L2 24h)
- **Quote caching**: corridor quotes (L1 30s + L2 5min)

### Implementation
- `cache.TwoLevel` struct wraps L1 (`cache.Local`) and L2 (`cache.Redis`)
- `cache.Local` is a sharded LRU with per-entry TTL, safe for concurrent access
- `cache.Redis` uses `ioredis` in the gateway (TS) and `go-redis` in Go services
- Cache keys are namespaced: `settla:auth:{hash}`, `settla:rate:{tenant}:{window}`, `settla:idempotency:{tenant}:{key}`

## Consequences

### Benefits
- **107ns auth lookups**: measured in benchmarks. At 5,000 TPS, auth adds ~0.5ms total CPU time per second across all requests, compared to 2,500ms with Redis-only or 10,000ms with DB-only.
- **Zero network I/O on hot path**: 99%+ of auth lookups never leave the process. No Redis round-trip, no DB connection, no serialization.
- **Graceful degradation**: if Redis is down, L1 still serves cached data for up to 30 seconds. If both Redis and DB are down, L1 continues serving until TTL expires. The system degrades gracefully rather than failing immediately.
- **Reduced DB load**: with 4 gateway replicas and 30s L1 TTL, each tenant generates at most 4 DB queries per 30 seconds (one per replica), regardless of request rate. At 100 tenants, that is ~13 queries/sec to the DB — trivial.

### Trade-offs
- **Stale data (30 seconds max)**: if a tenant's API key is revoked, the revocation takes up to 30 seconds to propagate to all gateway instances. During this window, revoked keys continue to authenticate successfully.
- **Per-process cache (no cross-instance invalidation)**: each gateway instance maintains its own L1 cache. There is no broadcast invalidation mechanism. A tenant updated in the DB will see different cache states across instances until all L1 entries expire.
- **Memory overhead**: each gateway instance holds all recently-accessed tenant records in memory. At ~2KB per tenant record × 1,000 active tenants = ~2MB — negligible, but grows linearly with active tenant count.
- **Cache stampede risk**: when an L1 entry expires, multiple concurrent requests for the same tenant could simultaneously hit L2/L3. Under high concurrency for a single tenant, this creates a brief thundering herd.

### Mitigations
- **30-second staleness is acceptable for auth**: tenant configuration changes (fee schedule updates, key rotations) are infrequent operations. A 30-second propagation delay is acceptable. For emergency key revocation, operators can restart gateway instances to clear L1.
- **Singleflight for cache fills**: the `cache.TwoLevel` implementation uses `sync.Once`-style deduplication for concurrent cache fills. Only one goroutine fetches from L2/L3; others wait for the result.
- **TTL jitter**: L1 TTL includes ±5 seconds of random jitter to prevent synchronized expiration across entries, reducing cache stampede risk.
- **Redis as L2 buffer**: even if L1 misses, L2 (Redis) absorbs the load before it reaches the database. The DB only sees cache fills that miss both L1 and L2.

## References

- [Cache Stampede Prevention](https://en.wikipedia.org/wiki/Cache_stampede)
- [Multi-tier Caching at Scale](https://engineering.fb.com/2013/06/25/core-infra/scaling-memcache-at-facebook/) — Facebook's multi-level caching architecture
- ADR-007 (PgBouncer Connection Pooling) — L3 database access goes through PgBouncer
