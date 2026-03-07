package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	// DefaultRateWindow is the sliding window duration for rate limiting.
	DefaultRateWindow = 1 * time.Minute
	// DefaultSyncInterval is how often local counters sync from Redis.
	// Rate limits are approximate within this window but saves 5,000 Redis calls/sec.
	DefaultSyncInterval = 5 * time.Second
)

// RateLimiter implements per-tenant sliding window rate limiting with a two-tier
// approach:
//
//	Hot path: local in-memory counter (approximate, ~100ns)
//	Cold path: Redis sorted set (accurate, synced every 5 seconds)
//
// This saves ~5,000 Redis calls/sec at peak by checking locally first.
type RateLimiter struct {
	redis    *RedisCache
	window   time.Duration
	syncInt  time.Duration
	mu       sync.RWMutex
	counters map[string]*localCounter
	stopCh   chan struct{}
	stopped  chan struct{}
}

// localCounter tracks the approximate count and last sync time for a tenant.
type localCounter struct {
	count   int64
	limit   int64
	syncAt  time.Time
	nowFunc func() time.Time
}

// NewRateLimiter creates a new rate limiter with local pre-check and Redis backing.
func NewRateLimiter(redis *RedisCache) *RateLimiter {
	return &RateLimiter{
		redis:    redis,
		window:   DefaultRateWindow,
		syncInt:  DefaultSyncInterval,
		counters: make(map[string]*localCounter),
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// rateLimitKey returns the Redis sorted set key for a tenant's rate limit window.
func rateLimitKey(tenantID uuid.UUID) string {
	return fmt.Sprintf("settla:rate:%s", tenantID.String())
}

// localCounterKey returns the map key for the local counter.
func localCounterKey(tenantID uuid.UUID) string {
	return tenantID.String()
}

// Allow checks if a request is within the tenant's rate limit.
// Returns (allowed bool, remaining int64, err error).
//
// Hot path: checks local counter first (~100ns). If the local counter
// is fresh (within syncInterval) and below limit, allows immediately.
// Cold path: falls through to Redis for an accurate count.
func (rl *RateLimiter) Allow(ctx context.Context, tenantID uuid.UUID, limit int64) (bool, int64, error) {
	lk := localCounterKey(tenantID)

	// Hot path: check local counter.
	rl.mu.RLock()
	lc, exists := rl.counters[lk]
	rl.mu.RUnlock()

	now := time.Now()
	if exists && now.Before(lc.syncAt.Add(rl.syncInt)) && lc.count < lc.limit {
		// Local counter is fresh and below limit — allow without Redis call.
		rl.mu.Lock()
		rl.counters[lk].count++
		remaining := lc.limit - rl.counters[lk].count
		rl.mu.Unlock()
		return true, remaining, nil
	}

	// Cold path: accurate check via Redis sorted set.
	key := rateLimitKey(tenantID)
	nowUnix := float64(now.UnixNano())
	windowStart := float64(now.Add(-rl.window).UnixNano())

	pipe := rl.redis.Client().Pipeline()
	// Remove entries outside the window.
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%f", windowStart))
	// Add current request.
	pipe.ZAdd(ctx, key, redis.Z{Score: nowUnix, Member: fmt.Sprintf("%d", now.UnixNano())})
	// Count entries in the window.
	countCmd := pipe.ZCard(ctx, key)
	// Set expiry on the sorted set.
	pipe.Expire(ctx, key, rl.window+time.Second)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("settla-cache: rate limit check for tenant %s: %w", tenantID, err)
	}

	count := countCmd.Val()
	allowed := count <= limit
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}

	// Update local counter for subsequent hot-path checks.
	rl.mu.Lock()
	rl.counters[lk] = &localCounter{
		count:  count,
		limit:  limit,
		syncAt: now,
	}
	rl.mu.Unlock()

	return allowed, remaining, nil
}

// Reset clears the rate limit counter for a tenant (e.g., for testing).
func (rl *RateLimiter) Reset(ctx context.Context, tenantID uuid.UUID) error {
	key := rateLimitKey(tenantID)
	rl.mu.Lock()
	delete(rl.counters, localCounterKey(tenantID))
	rl.mu.Unlock()
	return rl.redis.Delete(ctx, key)
}
