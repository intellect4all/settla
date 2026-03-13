package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// TestDistributedRateLimit verifies that 3 RateLimiter instances sharing the
// same Redis enforce the rate limit globally. With limit=100, each instance
// fires 50 requests (150 total), and the total allowed should be <= limit +
// tolerance (where tolerance accounts for the local batch sync window).
func TestDistributedRateLimit(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	const (
		numInstances   = 3
		requestsPerInst = 50
		totalRequests  = numInstances * requestsPerInst
		limit          = int64(100)
		// Tolerance: each instance may overshoot by up to its local batch before
		// syncing to Redis. With 3 instances, the max overage is bounded by
		// 3 × localBatchSize. In practice the default sync interval is 5s and
		// local counters hold ~5 requests, so tolerance = 3 × 5 = 15.
		tolerance = int64(15)
	)

	tenantID := uuid.MustParse("d0000000-0000-0000-0000-000000000099")

	// Create 3 rate limiter instances sharing the same Redis.
	limiters := make([]*RateLimiter, numInstances)
	for i := range limiters {
		limiters[i] = NewRateLimiter(rc)
	}

	// Reset the tenant's counter.
	limiters[0].Reset(ctx, tenantID)

	var totalAllowed atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < numInstances; i++ {
		wg.Add(1)
		go func(rl *RateLimiter) {
			defer wg.Done()
			for j := 0; j < requestsPerInst; j++ {
				// Force cold path by clearing local counter to simulate
				// distributed instances (each doesn't share local state).
				rl.mu.Lock()
				delete(rl.counters, localCounterKey(tenantID))
				rl.mu.Unlock()

				allowed, _, err := rl.Allow(ctx, tenantID, limit)
				if err != nil {
					continue
				}
				if allowed {
					totalAllowed.Add(1)
				}
			}
		}(limiters[i])
	}

	wg.Wait()

	got := totalAllowed.Load()
	maxAllowed := limit + tolerance
	t.Logf("distributed rate limiter: %d/%d requests allowed (limit=%d, max_allowed=%d)",
		got, totalRequests, limit, maxAllowed)

	if got > maxAllowed {
		t.Errorf("total allowed %d exceeds limit %d + tolerance %d = %d",
			got, limit, tolerance, maxAllowed)
	}

	if got < limit-tolerance {
		t.Errorf("total allowed %d is suspiciously low (expected near %d)", got, limit)
	}
}

// TestDistributedRateLimit_TenantIsolation verifies that distributed rate
// limiting maintains tenant isolation — one tenant's limit exhaustion does not
// affect another tenant.
func TestDistributedRateLimit_TenantIsolation(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()

	tenant1 := uuid.MustParse("d0000000-0000-0000-0000-000000000001")
	tenant2 := uuid.MustParse("d0000000-0000-0000-0000-000000000002")

	rl1 := NewRateLimiter(rc)
	rl2 := NewRateLimiter(rc)

	rl1.Reset(ctx, tenant1)
	rl2.Reset(ctx, tenant2)

	limit := int64(5)

	// Exhaust tenant1's limit via rl1.
	for i := int64(0); i <= limit+2; i++ {
		rl1.mu.Lock()
		delete(rl1.counters, localCounterKey(tenant1))
		rl1.mu.Unlock()
		rl1.Allow(ctx, tenant1, limit)
	}

	// Tenant2 should still be allowed via rl2.
	rl2.mu.Lock()
	delete(rl2.counters, localCounterKey(tenant2))
	rl2.mu.Unlock()

	allowed, _, err := rl2.Allow(ctx, tenant2, limit)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("tenant2 should be allowed even though tenant1 is exhausted")
	}
}
