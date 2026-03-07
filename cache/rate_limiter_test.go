package cache

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRateLimiter_AllowWithinLimit(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	rl.Reset(ctx, tenantID)

	for i := 0; i < 5; i++ {
		allowed, remaining, err := rl.Allow(ctx, tenantID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed", i)
		}
		if remaining < 0 {
			t.Fatalf("remaining should not be negative, got %d", remaining)
		}
	}
}

func TestRateLimiter_DenyOverLimit(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	rl.Reset(ctx, tenantID)

	limit := int64(3)

	// Use up the limit (cold path — each call goes to Redis).
	for i := int64(0); i < limit; i++ {
		// Reset local counter to force cold path each time.
		rl.mu.Lock()
		delete(rl.counters, localCounterKey(tenantID))
		rl.mu.Unlock()

		allowed, _, err := rl.Allow(ctx, tenantID, limit)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed within limit", i)
		}
	}

	// Next request should be denied.
	rl.mu.Lock()
	delete(rl.counters, localCounterKey(tenantID))
	rl.mu.Unlock()

	allowed, remaining, err := rl.Allow(ctx, tenantID, limit)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("request should be denied over limit")
	}
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}
}

func TestRateLimiter_TenantIsolation(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenant1 := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant2 := uuid.MustParse("b0000000-0000-0000-0000-000000000002")
	rl.Reset(ctx, tenant1)
	rl.Reset(ctx, tenant2)

	limit := int64(2)

	// Exhaust tenant1's limit.
	for i := int64(0); i < limit+1; i++ {
		rl.mu.Lock()
		delete(rl.counters, localCounterKey(tenant1))
		rl.mu.Unlock()
		rl.Allow(ctx, tenant1, limit)
	}

	// Tenant2 should still be allowed.
	allowed, _, err := rl.Allow(ctx, tenant2, limit)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("tenant2 should not be affected by tenant1's rate limit")
	}
}

func TestRateLimiter_HotPath(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	rl.Reset(ctx, tenantID)

	// First call: cold path (Redis).
	rl.Allow(ctx, tenantID, 100)

	// Second call: should use hot path (local counter is fresh).
	allowed, _, err := rl.Allow(ctx, tenantID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("hot path should allow request under limit")
	}
}

func TestRateLimiter_Reset(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	// Use up some limit.
	rl.Allow(ctx, tenantID, 5)
	rl.Allow(ctx, tenantID, 5)

	// Reset.
	rl.Reset(ctx, tenantID)

	// Should be allowed again.
	allowed, _, err := rl.Allow(ctx, tenantID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("should be allowed after reset")
	}
}
