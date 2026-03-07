package cache

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestIdempotencyCache_FirstRequest(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	ic := NewIdempotencyCache(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	key := "req-abc-123"

	// Check — should be empty.
	val, err := ic.Check(ctx, tenantID, key)
	if err != nil {
		t.Fatal(err)
	}
	if val != "" {
		t.Fatalf("expected empty, got %s", val)
	}

	// Set.
	ok, err := ic.Set(ctx, tenantID, key, "transfer-001")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected first Set to succeed")
	}

	// Check again — should return the transfer ID.
	val, err = ic.Check(ctx, tenantID, key)
	if err != nil {
		t.Fatal(err)
	}
	if val != "transfer-001" {
		t.Fatalf("expected transfer-001, got %s", val)
	}
}

func TestIdempotencyCache_DuplicateRejected(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	ic := NewIdempotencyCache(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	key := "dup-key"

	ic.Set(ctx, tenantID, key, "transfer-001")

	// Second set with different value — should fail (NX).
	ok, err := ic.Set(ctx, tenantID, key, "transfer-002")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected duplicate Set to fail")
	}

	// Original value preserved.
	val, _ := ic.Check(ctx, tenantID, key)
	if val != "transfer-001" {
		t.Fatalf("expected transfer-001, got %s", val)
	}
}

func TestIdempotencyCache_TenantIsolation(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	ic := NewIdempotencyCache(rc)

	tenant1 := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant2 := uuid.MustParse("b0000000-0000-0000-0000-000000000002")
	key := "same-key"

	ic.Set(ctx, tenant1, key, "t1-transfer")
	ic.Set(ctx, tenant2, key, "t2-transfer")

	v1, _ := ic.Check(ctx, tenant1, key)
	v2, _ := ic.Check(ctx, tenant2, key)

	if v1 != "t1-transfer" {
		t.Fatalf("tenant1 expected t1-transfer, got %s", v1)
	}
	if v2 != "t2-transfer" {
		t.Fatalf("tenant2 expected t2-transfer, got %s", v2)
	}
}
