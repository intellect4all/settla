package cache

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
)

func TestTenantCache_L1Hit(t *testing.T) {
	local := NewLocalCache(100)
	rc := newTestRedis(t)
	ctx := context.Background()

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant := &domain.Tenant{
		ID:     tenantID,
		Name:   "Lemfi",
		Slug:   "lemfi",
		Status: domain.TenantStatusActive,
	}

	var dbCalls int32
	loader := func(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
		atomic.AddInt32(&dbCalls, 1)
		return tenant, nil
	}

	tc := NewTenantCache(local, rc, loader)

	// First call: goes to DB (L3).
	got, err := tc.Get(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lemfi" {
		t.Fatalf("expected Lemfi, got %s", got.Name)
	}
	if atomic.LoadInt32(&dbCalls) != 1 {
		t.Fatalf("expected 1 DB call, got %d", dbCalls)
	}

	// Second call: should hit L1 (no additional DB call).
	got, err = tc.Get(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lemfi" {
		t.Fatalf("expected Lemfi, got %s", got.Name)
	}
	if atomic.LoadInt32(&dbCalls) != 1 {
		t.Fatalf("expected still 1 DB call, got %d", dbCalls)
	}
}

func TestTenantCache_L2Hit(t *testing.T) {
	local := NewLocalCache(100)
	rc := newTestRedis(t)
	ctx := context.Background()

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant := &domain.Tenant{
		ID:     tenantID,
		Name:   "Lemfi",
		Slug:   "lemfi",
		Status: domain.TenantStatusActive,
	}

	var dbCalls int32
	loader := func(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
		atomic.AddInt32(&dbCalls, 1)
		return tenant, nil
	}

	tc := NewTenantCache(local, rc, loader)

	// Prime the cache (L1 + L2).
	tc.Get(ctx, tenantID)

	// Clear L1 only.
	local.Delete(tenantKey(tenantID))

	// Should hit L2 (Redis), not L3 (DB).
	got, err := tc.Get(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lemfi" {
		t.Fatalf("expected Lemfi from L2, got %s", got.Name)
	}
	if atomic.LoadInt32(&dbCalls) != 1 {
		t.Fatalf("expected still 1 DB call after L2 hit, got %d", dbCalls)
	}
}

func TestTenantCache_Invalidation(t *testing.T) {
	local := NewLocalCache(100)
	rc := newTestRedis(t)
	ctx := context.Background()

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	var dbCalls int32
	loader := func(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
		atomic.AddInt32(&dbCalls, 1)
		return &domain.Tenant{
			ID:     tenantID,
			Name:   fmt.Sprintf("Lemfi-v%d", atomic.LoadInt32(&dbCalls)),
			Slug:   "lemfi",
			Status: domain.TenantStatusActive,
		}, nil
	}

	tc := NewTenantCache(local, rc, loader)

	// Prime cache.
	tc.Get(ctx, tenantID)

	// Invalidate.
	tc.Invalidate(ctx, tenantID)

	// Next call should go to DB again.
	got, err := tc.Get(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&dbCalls) != 2 {
		t.Fatalf("expected 2 DB calls after invalidation, got %d", dbCalls)
	}
	if got.Name != "Lemfi-v2" {
		t.Fatalf("expected Lemfi-v2, got %s", got.Name)
	}
}

func TestTenantCache_TenantIsolation(t *testing.T) {
	local := NewLocalCache(100)
	rc := newTestRedis(t)
	ctx := context.Background()

	tenant1ID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant2ID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	loader := func(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
		return &domain.Tenant{
			ID:   id,
			Name: fmt.Sprintf("Tenant-%s", id.String()[:8]),
			Slug: id.String()[:8],
		}, nil
	}

	tc := NewTenantCache(local, rc, loader)

	t1, _ := tc.Get(ctx, tenant1ID)
	t2, _ := tc.Get(ctx, tenant2ID)

	if t1.ID == t2.ID {
		t.Fatal("tenant isolation violated: same ID returned for different tenants")
	}
	if t1.Name == t2.Name {
		t.Fatal("tenant isolation violated: same name returned for different tenants")
	}
}

func BenchmarkTenantCache_L1Hit(b *testing.B) {
	local := NewLocalCache(10000)
	// Use a nil redis — L1 should always hit so we never reach Redis.
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant := &domain.Tenant{
		ID:   tenantID,
		Name: "Lemfi",
		Slug: "lemfi",
	}

	key := tenantKey(tenantID)
	local.Set(key, tenant, 1*time.Minute)

	tc := &TenantCache{local: local}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tc.local.Get(key)
		}
	})
}
