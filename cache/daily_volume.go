package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
)

// RedisDailyVolumeCounter implements core.DailyVolumeCounter using Redis
// INCRBYFLOAT for atomic, race-free daily volume tracking.
type RedisDailyVolumeCounter struct {
	rc *RedisCache
}

// NewRedisDailyVolumeCounter creates a counter backed by the given RedisCache.
func NewRedisDailyVolumeCounter(rc *RedisCache) *RedisDailyVolumeCounter {
	return &RedisDailyVolumeCounter{rc: rc}
}

func dailyVolumeKey(tenantID uuid.UUID, date time.Time) string {
	return fmt.Sprintf("dailyvol:%s:%s", tenantID, date.Format("2006-01-02"))
}

// ttlUntilEndOfDay returns the duration until midnight UTC of the given day.
func ttlUntilEndOfDay(date time.Time) time.Duration {
	endOfDay := date.Truncate(24 * time.Hour).Add(24 * time.Hour)
	ttl := time.Until(endOfDay)
	if ttl < time.Minute {
		ttl = time.Minute // minimum 1 minute to avoid instant expiry
	}
	return ttl
}

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
