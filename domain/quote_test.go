package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestQuoteIsExpiredPast(t *testing.T) {
	q := &Quote{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		ExpiresAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	if !q.IsExpired() {
		t.Error("expected quote with past expiry to be expired")
	}
}

func TestQuoteIsExpiredFuture(t *testing.T) {
	q := &Quote{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	if q.IsExpired() {
		t.Error("expected quote with future expiry to not be expired")
	}
}

func TestQuoteHasTenantID(t *testing.T) {
	tenantID := uuid.New()
	q := &Quote{
		TenantID:       tenantID,
		SourceCurrency: CurrencyUSD,
		DestCurrency:   CurrencyNGN,
		FXRate:         decimal.NewFromInt(1500),
	}
	if q.TenantID == uuid.Nil {
		t.Error("quote must have a tenant ID")
	}
}
