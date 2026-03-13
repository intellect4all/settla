package cache

import (
	"context"
	"sync"
	"sync/atomic"
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

func TestRateLimiter_ConcurrentOverage(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenantID := uuid.MustParse("c0000000-0000-0000-0000-000000000099")
	rl.Reset(ctx, tenantID)

	const (
		goroutines   = 100
		callsPerG    = 20
		limit        = int64(1000)
		tolerance    = 150 // 15% of limit — Redis pipeline is not atomic across goroutines
	)

	var allowed atomic.Int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerG; j++ {
				// Force cold path each time by clearing local counter.
				rl.mu.Lock()
				delete(rl.counters, localCounterKey(tenantID))
				rl.mu.Unlock()

				ok, _, err := rl.Allow(ctx, tenantID, limit)
				if err != nil {
					continue
				}
				if ok {
					allowed.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	got := allowed.Load()
	maxAllowed := limit + tolerance
	if got > maxAllowed {
		t.Errorf("allowed %d requests, expected at most %d (limit %d + %d tolerance)", got, maxAllowed, limit, tolerance)
	}
	t.Logf("concurrent rate limiter: %d/%d requests allowed (limit %d)", got, int64(goroutines)*int64(callsPerG), limit)
}

func TestRateLimiter_ExactLimit(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	rl := NewRateLimiter(rc)

	tenantID := uuid.MustParse("c0000000-0000-0000-0000-000000000098")
	rl.Reset(ctx, tenantID)

	limit := int64(5)

	// Issue exactly `limit` requests via cold path.
	for i := int64(0); i < limit; i++ {
		rl.mu.Lock()
		delete(rl.counters, localCounterKey(tenantID))
		rl.mu.Unlock()

		allowed, _, err := rl.Allow(ctx, tenantID, limit)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed (within limit)", i)
		}
	}

	// The (limit+1)th request should be denied.
	rl.mu.Lock()
	delete(rl.counters, localCounterKey(tenantID))
	rl.mu.Unlock()

	allowed, remaining, err := rl.Allow(ctx, tenantID, limit)
	if err != nil {
		t.Fatalf("over-limit request: %v", err)
	}
	if allowed {
		t.Error("request at limit+1 should be denied")
	}
	if remaining != 0 {
		t.Errorf("expected 0 remaining, got %d", remaining)
	}
}
