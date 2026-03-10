package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// RouteInfo describes the settlement route selected for a quote.
type RouteInfo struct {
	Chain             string             `json:"chain"`
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
