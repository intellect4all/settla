package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// BenchmarkLocalCacheGet measures local in-process cache lookup performance.
//
// Target: <200ns per lookup
func BenchmarkLocalCacheGet(b *testing.B) {
	c := NewLocalCache(10000)
	c.Set("bench-key", "bench-value", 1*time.Minute)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Get("bench-key")
		}
	})
}

// BenchmarkLocalCacheSet measures local cache write performance.
//
// Target: <500ns per write
func BenchmarkLocalCacheSet(b *testing.B) {
	c := NewLocalCache(10000)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Set(fmt.Sprintf("key-%d", i), i, 1*time.Minute)
			i++
		}
	})
}

// BenchmarkLocalCacheSetOverwrite measures overwrite performance (existing key).
func BenchmarkLocalCacheSetOverwrite(b *testing.B) {
	c := NewLocalCache(10000)
	c.Set("overwrite-key", "initial", 1*time.Minute)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set("overwrite-key", i, 1*time.Minute)
	}
}

// BenchmarkLocalCacheGetMiss measures cache miss performance.
func BenchmarkLocalCacheGetMiss(b *testing.B) {
	c := NewLocalCache(10000)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Get("nonexistent-key")
		}
	})
}

// BenchmarkLocalCacheDelete measures delete performance.
func BenchmarkLocalCacheDelete(b *testing.B) {
	c := NewLocalCache(10000)

	// Pre-populate
	for i := 0; i < 1000; i++ {
		c.Set(fmt.Sprintf("del-key-%d", i), i, 1*time.Minute)
	}

	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for i < b.N {
		c.Delete(fmt.Sprintf("del-key-%d", i%1000))
		i++
	}
}

// BenchmarkRedisGet measures Redis cache lookup performance.
// This benchmark requires Redis to be running (skipped if unavailable).
//
// Target: <1ms per lookup (network round-trip dominates)
func BenchmarkRedisGet(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	// Skip if Redis unavailable
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	// Setup
	client.Set(ctx, "bench:redis:key", "bench-value", 1*time.Minute)
	defer client.FlushDB(ctx)
	defer client.Close()

	cache := NewRedisCacheFromClient(client)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.Get(ctx, "bench:redis:key")
		}
	})
}

// BenchmarkRedisSet measures Redis cache write performance.
//
// Target: <1ms per write
func BenchmarkRedisSet(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	defer client.FlushDB(ctx)
	defer client.Close()

	cache := NewRedisCacheFromClient(client)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = cache.Set(ctx, fmt.Sprintf("bench:redis:key:%d", i), "value", 1*time.Minute)
			i++
		}
	})
}

// BenchmarkRedisSetJSON measures JSON serialization + Redis write.
func BenchmarkRedisSetJSON(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	defer client.FlushDB(ctx)
	defer client.Close()

	cache := NewRedisCacheFromClient(client)

	type testData struct {
		ID     string  `json:"id"`
		Amount float64 `json:"amount"`
		Count  int     `json:"count"`
	}

	data := testData{ID: "test-123", Amount: 999.99, Count: 42}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = cache.SetJSON(ctx, fmt.Sprintf("bench:json:%d", i), data, 1*time.Minute)
			i++
		}
	})
}

// BenchmarkRedisGetJSON measures Redis read + JSON deserialization.
func BenchmarkRedisGetJSON(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	cache := NewRedisCacheFromClient(client)

	type testData struct {
		ID     string  `json:"id"`
		Amount float64 `json:"amount"`
		Count  int     `json:"count"`
	}

	// Setup
	data := testData{ID: "test-123", Amount: 999.99, Count: 42}
	_ = cache.SetJSON(ctx, "bench:json:get", data, 1*time.Minute)

	defer client.FlushDB(ctx)
	defer client.Close()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var result testData
		for pb.Next() {
			_ = cache.GetJSON(ctx, "bench:json:get", &result)
		}
	})
}

// BenchmarkIdempotencyCheckSet measures idempotency key check-and-set performance.
// This is a critical hot path operation.
//
// Target: <2ms (Redis round-trip + check)
func BenchmarkIdempotencyCheckSet(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	defer client.FlushDB(ctx)
	defer client.Close()

	cache := NewRedisCacheFromClient(client)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Use unique keys to avoid conflicts in parallel benchmark
			_, _ = cache.SetNX(ctx, fmt.Sprintf("idem:bench:%d:%d", b.N, i), "transfer-id", 1*time.Hour)
			i++
		}
	})
}

// BenchmarkIdempotencyCheckDuplicate measures checking an existing key.
func BenchmarkIdempotencyCheckDuplicate(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	cache := NewRedisCacheFromClient(client)

	// Pre-set the key
	_, _ = cache.SetNX(ctx, "idem:duplicate", "transfer-id", 1*time.Hour)

	defer client.FlushDB(ctx)
	defer client.Close()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.SetNX(ctx, "idem:duplicate", "transfer-id", 1*time.Hour)
		}
	})
}

// BenchmarkTenantCacheGet measures tenant cache read performance.
//
// Target: <1μs for cached tenant (after initial load)
func BenchmarkTenantCacheGet(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	defer client.FlushDB(ctx)
	defer client.Close()

	// Note: TenantCache uses Redis, but we can test the local cache layer
	// This is a simplified benchmark

	localCache := NewLocalCache(1000)

	type tenantInfo struct {
		ID    string
		Name  string
		Limit int64
	}

	tenant := tenantInfo{ID: "tenant-123", Name: "Test Tenant", Limit: 1000000}
	localCache.Set("tenant:tenant-123", tenant, 1*time.Minute)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = localCache.Get("tenant:tenant-123")
		}
	})
}

// BenchmarkConcurrentLocalCache measures local cache under high concurrency.
//
// Target: >1M ops/sec combined read/write
func BenchmarkConcurrentLocalCache(b *testing.B) {
	c := NewLocalCache(10000)

	// Pre-populate some data
	for i := 0; i < 1000; i++ {
		c.Set(fmt.Sprintf("concurrent-key-%d", i), i, 10*time.Minute)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Mix of reads and writes (80% read, 20% write)
			if i%5 == 0 {
				c.Set(fmt.Sprintf("concurrent-key-%d", i%1000), i, 10*time.Minute)
			} else {
				_, _ = c.Get(fmt.Sprintf("concurrent-key-%d", i%1000))
			}
			i++
		}
	})
}

// BenchmarkConcurrentRedisCache measures Redis cache under high concurrency.
//
// Target: >10K ops/sec (limited by Redis single-threading and network)
func BenchmarkConcurrentRedisCache(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
		// Increase pool size for concurrent benchmark
		PoolSize:     100,
		MinIdleConns: 20,
	})
	ctx := context.Background()

	if err := client.Ping(ctx).Err(); err != nil {
		b.Skipf("Redis not available: %v", err)
	}

	defer client.FlushDB(ctx)
	defer client.Close()

	cache := NewRedisCacheFromClient(client)

	// Pre-populate
	for i := 0; i < 100; i++ {
		_ = cache.Set(ctx, fmt.Sprintf("redis-concurrent-%d", i), "value", 10*time.Minute)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%5 == 0 {
				_ = cache.Set(ctx, fmt.Sprintf("redis-concurrent-%d", i%100), fmt.Sprintf("%d", i), 10*time.Minute)
			} else {
				_, _ = cache.Get(ctx, fmt.Sprintf("redis-concurrent-%d", i%100))
			}
			i++
		}
	})
}
