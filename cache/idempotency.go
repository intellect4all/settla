package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	// DefaultIdempotencyTTL is how long idempotency keys are retained.
	// 24 hours matches typical API retry windows.
	DefaultIdempotencyTTL = 24 * time.Hour
)

// IdempotencyCache stores idempotency keys in Redis to detect duplicate requests.
// Key format: settla:idem:{tenant_id}:{key}
// Value: the transfer ID (or result reference) from the original request.
type IdempotencyCache struct {
	redis *RedisCache
	ttl   time.Duration
}

// NewIdempotencyCache creates a new idempotency cache.
func NewIdempotencyCache(redis *RedisCache) *IdempotencyCache {
	return &IdempotencyCache{
		redis: redis,
		ttl:   DefaultIdempotencyTTL,
	}
}

// idemKey returns the Redis key for an idempotency entry.
func idemKey(tenantID uuid.UUID, key string) string {
	return fmt.Sprintf("settla:idem:%s:%s", tenantID.String(), key)
}

// Check returns the stored transfer ID if the idempotency key was already used.
// Returns ("", nil) if the key is fresh (not a duplicate).
func (ic *IdempotencyCache) Check(ctx context.Context, tenantID uuid.UUID, key string) (string, error) {
	rk := idemKey(tenantID, key)
	val, err := ic.redis.Get(ctx, rk)
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("settla-cache: check idempotency %s: %w", key, err)
	}
	return val, nil
}

// Set records an idempotency key with the resulting transfer ID.
// Uses SetNX so only the first request wins.
// Returns true if this was a new key, false if it already existed.
func (ic *IdempotencyCache) Set(ctx context.Context, tenantID uuid.UUID, key, transferID string) (bool, error) {
	rk := idemKey(tenantID, key)
	ok, err := ic.redis.SetNX(ctx, rk, transferID, ic.ttl)
	if err != nil {
		return false, fmt.Errorf("settla-cache: set idempotency %s: %w", key, err)
	}
	return ok, nil
}
