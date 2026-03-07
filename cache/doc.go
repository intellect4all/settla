// Package cache provides two-level caching (local in-process + Redis) and
// rate limiting for the Settla settlement engine.
//
// Architecture:
//   - L1: In-process LRU cache (~100ns lookups, 30s TTL for tenant auth)
//   - L2: Redis cache (~0.5ms lookups, 5min TTL, shared across replicas)
//   - L3: Database via store packages (source of truth)
//
// At 5,000 TPS peak, every gateway request needs tenant auth. Local cache
// eliminates Redis round-trips for the vast majority of requests.
//
// All cache keys are tenant-scoped to enforce strict multi-tenancy isolation.
// Key formats:
//   - Tenant auth:   settla:tenant:{tenant_id}
//   - Quote:         settla:quote:{tenant_id}:{quote_id}
//   - Idempotency:   settla:idem:{tenant_id}:{key}
//   - Rate limit:    settla:rate:{tenant_id}
package cache
