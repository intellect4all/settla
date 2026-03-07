package cache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newTestRedis creates a Redis client pointing at localhost:6379.
// Tests using this are skipped if Redis is not available.
func newTestRedis(t *testing.T) *RedisCache {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // Use DB 15 for tests to avoid polluting other DBs.
	})
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	// Flush test DB.
	client.FlushDB(ctx)
	t.Cleanup(func() {
		client.FlushDB(ctx)
		client.Close()
	})
	return NewRedisCacheFromClient(client)
}

func TestRedisCache_SetGet(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	err := rc.Set(ctx, "test:key", "hello", 10*time.Second)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	val, err := rc.Get(ctx, "test:key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "hello" {
		t.Fatalf("expected hello, got %s", val)
	}
}

func TestRedisCache_Miss(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	_, err := rc.Get(ctx, "nonexistent")
	if err != redis.Nil {
		t.Fatalf("expected redis.Nil, got %v", err)
	}
}

func TestRedisCache_SetNX(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	ok, err := rc.SetNX(ctx, "idem:1", "transfer-123", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected first SetNX to succeed")
	}

	ok, err = rc.SetNX(ctx, "idem:1", "transfer-456", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected second SetNX to fail (key exists)")
	}

	// Value should still be from first set.
	val, _ := rc.Get(ctx, "idem:1")
	if val != "transfer-123" {
		t.Fatalf("expected transfer-123, got %s", val)
	}
}

func TestRedisCache_JSON(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	type testData struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	err := rc.SetJSON(ctx, "json:test", testData{Name: "foo", Count: 42}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var result testData
	err = rc.GetJSON(ctx, "json:test", &result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "foo" || result.Count != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRedisCache_Delete(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	rc.Set(ctx, "del:key", "value", 10*time.Second)
	rc.Delete(ctx, "del:key")

	_, err := rc.Get(ctx, "del:key")
	if err != redis.Nil {
		t.Fatalf("expected redis.Nil after delete, got %v", err)
	}
}
