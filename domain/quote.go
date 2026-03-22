package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// RouteInfo describes the settlement route selected for a quote.
type RouteInfo struct {
	Chain             CryptoChain        `json:"chain"`
	StableCoin        Currency           `json:"stablecoin"`
	EstimatedTimeMin  int                `json:"estimated_time_min,omitempty"`
	OnRampProvider    string             `json:"on_ramp_provider"`
	OffRampProvider   string             `json:"off_ramp_provider"`
	ExplorerURL       string             `json:"explorer_url,omitempty"`
	AlternativeRoutes []RouteAlternative `json:"alternative_routes,omitempty"`
}

// Quote represents an FX rate quote with an expiry window.
// All quotes are tenant-scoped.
type Quote struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	SourceCurrency Currency
	SourceAmount   decimal.Decimal
	DestCurrency   Currency
	DestAmount     decimal.Decimal
	StableAmount   decimal.Decimal // intermediate stablecoin amount (e.g., USDT flowing on-chain)
	FXRate         decimal.Decimal
	Fees           FeeBreakdown
	Route          RouteInfo
	ExpiresAt      time.Time
	CreatedAt      time.Time
}

// IsExpired returns true if the quote has passed its expiry time.
func (q *Quote) IsExpired() bool {
	return time.Now().UTC().After(q.ExpiresAt)
}

func (q *Quote) Validate() error {
	if !q.DestAmount.IsPositive() {
		return fmt.Errorf("settla-core: create transfer: quote dest amount must be positive, got %s", q.DestAmount.String())
	}
	if !q.StableAmount.IsPositive() {
		return fmt.Errorf("settla-core: create transfer: quote stable amount must be positive, got %s", q.StableAmount.String())
	}

	if q.Route.OnRampProvider == "" {
		return fmt.Errorf("settla-core: create transfer: quote route on_ramp_provider is required")
	}
	if q.Route.OffRampProvider == "" {
		return fmt.Errorf("settla-core: create transfer: quote route off_ramp_provider is required")
	}
	if q.Route.Chain == "" {
		return fmt.Errorf("settla-core: create transfer: quote route chain is required")
	}
	if q.Route.StableCoin == "" {
		return fmt.Errorf("settla-core: create transfer: quote route stablecoin is required")
	}

	return nil
}
