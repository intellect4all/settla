package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig holds connection parameters for the Redis client.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// SentinelConfig holds connection parameters for a Redis Sentinel cluster.
// Sentinel-aware clients query sentinels to discover the current master before
// connecting, so failover is transparent to the application.
type SentinelConfig struct {
	// MasterName is the logical name of the master as configured in sentinel.conf
	// (e.g. "settla-redis").  All sentinels in the cluster must use this same name.
	MasterName string

	// SentinelAddrs is a comma-separated list of sentinel endpoints
	// (e.g. "sentinel-0:26379,sentinel-1:26379,sentinel-2:26379").
	// At least 2 of the 3 sentinels must be reachable for quorum-based failover.
	SentinelAddrs string

	Password string
	DB       int
}

// RedisCache wraps a Redis client for L2 caching.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache creates a Redis cache from the given config.
func NewRedisCache(cfg RedisConfig) *RedisCache {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     50,
		MinIdleConns: 10,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	return &RedisCache{client: client}
}

// NewRedisCacheFromSentinel creates a Redis cache that connects via Sentinel
// for automatic master discovery and failover.  The returned *RedisCache uses
// the same interface as NewRedisCache — the Sentinel topology is transparent
// to all callers.
//
// On failover, go-redis automatically re-queries the sentinels to discover the
// new master and re-establishes the connection pool, so the application
// continues to function without a restart.
func NewRedisCacheFromSentinel(cfg SentinelConfig) *RedisCache {
	addrs := strings.Split(cfg.SentinelAddrs, ",")
	for i, a := range addrs {
		addrs[i] = strings.TrimSpace(a)
	}
	client := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    cfg.MasterName,
		SentinelAddrs: addrs,
		Password:      cfg.Password,
		DB:            cfg.DB,
		PoolSize:      50,
		MinIdleConns:  10,
		ReadTimeout:   2 * time.Second,
		WriteTimeout:  2 * time.Second,
	})
	return &RedisCache{client: client}
}

// NewRedisCacheFromClient creates a RedisCache from an existing redis.Client.
// Useful for testing with miniredis.
func NewRedisCacheFromClient(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// Ping checks Redis connectivity.
func (r *RedisCache) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Get retrieves a raw string value from Redis.
func (r *RedisCache) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

// Set stores a string value in Redis with a TTL.
func (r *RedisCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

// SetNX stores a value only if the key does not exist (for idempotency).
// Returns true if the key was set, false if it already existed.
func (r *RedisCache) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return r.client.SetNX(ctx, key, value, ttl).Result()
}

// Delete removes a key from Redis.
func (r *RedisCache) Delete(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

// GetJSON retrieves and unmarshals a JSON value from Redis.
func (r *RedisCache) GetJSON(ctx context.Context, key string, dest any) error {
	data, err := r.client.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), dest)
}

// SetJSON marshals and stores a value as JSON in Redis with a TTL.
func (r *RedisCache) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("settla-cache: marshal for key %s: %w", key, err)
	}
	return r.client.Set(ctx, key, data, ttl).Err()
}

// Client returns the underlying redis.Client for advanced operations
// (e.g., sorted set commands in the rate limiter).
func (r *RedisCache) Client() *redis.Client {
	return r.client
}

// Close closes the Redis connection.
func (r *RedisCache) Close() error {
	return r.client.Close()
}
