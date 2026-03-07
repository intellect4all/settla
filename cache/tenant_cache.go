package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/intellect4all/settla/domain"
)

const (
	// LocalTenantTTL is the L1 in-process cache TTL for tenant data (~100ns lookup).
	LocalTenantTTL = 30 * time.Second
	// RedisTenantTTL is the L2 Redis cache TTL for tenant data (~0.5ms lookup).
	RedisTenantTTL = 5 * time.Minute
)

// TenantLoader is the L3 source-of-truth function that loads a tenant from the database.
type TenantLoader func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)

// TenantCache provides two-level caching for tenant lookups:
//
//	L1: Local in-process cache (30s TTL, ~100ns)
//	L2: Redis (5min TTL, ~0.5ms)
//	L3: Database via TenantLoader (source of truth)
//
// On tenant update, invalidate Redis key; local caches expire naturally.
type TenantCache struct {
	local  *LocalCache
	redis  *RedisCache
	loader TenantLoader
}

// NewTenantCache creates a new two-level tenant cache.
func NewTenantCache(local *LocalCache, redis *RedisCache, loader TenantLoader) *TenantCache {
	return &TenantCache{
		local:  local,
		redis:  redis,
		loader: loader,
	}
}

// tenantKey returns the Redis key for a tenant.
func tenantKey(tenantID uuid.UUID) string {
	return fmt.Sprintf("settla:tenant:%s", tenantID.String())
}

// Get retrieves a tenant, checking L1 → L2 → L3 in order.
func (tc *TenantCache) Get(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	key := tenantKey(tenantID)

	// L1: Local cache.
	if val, ok := tc.local.Get(key); ok {
		if t, ok := val.(*domain.Tenant); ok {
			return t, nil
		}
	}

	// L2: Redis cache.
	var tenant domain.Tenant
	err := tc.redis.GetJSON(ctx, key, &tenant)
	if err == nil {
		// Populate L1 from L2 hit.
		tc.local.Set(key, &tenant, LocalTenantTTL)
		return &tenant, nil
	}
	if err != redis.Nil {
		// Redis error — fall through to L3 but log would happen upstream.
	}

	// L3: Database (source of truth).
	t, err := tc.loader(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Populate L2 and L1.
	_ = tc.redis.SetJSON(ctx, key, t, RedisTenantTTL)
	tc.local.Set(key, t, LocalTenantTTL)

	return t, nil
}

// Invalidate removes a tenant from L2 Redis cache.
// L1 local caches expire naturally (30s TTL).
func (tc *TenantCache) Invalidate(ctx context.Context, tenantID uuid.UUID) error {
	key := tenantKey(tenantID)
	tc.local.Delete(key)
	return tc.redis.Delete(ctx, key)
}
