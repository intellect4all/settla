package cache

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

func TestQuoteCache_SetGet(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	qc := NewQuoteCache(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	quoteID := uuid.New()

	quote := &domain.Quote{
		ID:             quoteID,
		TenantID:       tenantID,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromInt(500000),
		FXRate:         decimal.NewFromInt(500),
		ExpiresAt:      time.Now().Add(10 * time.Minute),
		CreatedAt:      time.Now(),
	}

	err := qc.Set(ctx, quote)
	if err != nil {
		t.Fatal(err)
	}

	got, err := qc.Get(ctx, tenantID, quoteID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected quote, got nil")
	}
	if !got.SourceAmount.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("expected 1000, got %s", got.SourceAmount)
	}
}

func TestQuoteCache_Miss(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	qc := NewQuoteCache(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	got, err := qc.Get(ctx, tenantID, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected nil on miss")
	}
}

func TestQuoteCache_ExpiredQuoteNotCached(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	qc := NewQuoteCache(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	quoteID := uuid.New()

	quote := &domain.Quote{
		ID:        quoteID,
		TenantID:  tenantID,
		ExpiresAt: time.Now().Add(-1 * time.Minute), // Already expired.
	}

	err := qc.Set(ctx, quote)
	if err != nil {
		t.Fatal(err)
	}

	got, err := qc.Get(ctx, tenantID, quoteID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expired quote should not be cached")
	}
}

func TestQuoteCache_TenantIsolation(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	qc := NewQuoteCache(rc)

	tenant1 := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenant2 := uuid.MustParse("b0000000-0000-0000-0000-000000000002")
	quoteID := uuid.New()

	quote := &domain.Quote{
		ID:           quoteID,
		TenantID:     tenant1,
		SourceAmount: decimal.NewFromInt(999),
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}
	qc.Set(ctx, quote)

	// Tenant 2 should NOT see tenant 1's quote.
	got, err := qc.Get(ctx, tenant2, quoteID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("tenant isolation violated: tenant2 saw tenant1's quote")
	}
}

func TestQuoteCache_Delete(t *testing.T) {
	rc := newTestRedis(t)
	ctx := context.Background()
	qc := NewQuoteCache(rc)

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	quoteID := uuid.New()

	quote := &domain.Quote{
		ID:        quoteID,
		TenantID:  tenantID,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	qc.Set(ctx, quote)
	qc.Delete(ctx, tenantID, quoteID)

	got, _ := qc.Get(ctx, tenantID, quoteID)
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}
