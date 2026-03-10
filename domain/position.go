package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Position represents a tenant's treasury position for a currency at a location.
// Positions track total balance, locked (reserved) amounts, and threshold levels.
type Position struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Currency      Currency
	Location      string // e.g., "bank:gtbank:ngn", "crypto:tron:usdt"
	Balance       decimal.Decimal
	Locked        decimal.Decimal
	MinBalance    decimal.Decimal
	TargetBalance decimal.Decimal
	UpdatedAt     time.Time
}

// Available returns the amount that can be reserved (Balance - Locked).
// Returns zero if Locked exceeds Balance (corruption guard).
func (p *Position) Available() decimal.Decimal {
	avail := p.Balance.Sub(p.Locked)
	if avail.IsNegative() {
		return decimal.Zero
	}
	return avail
}

// IsAboveMinimum returns true if the current balance is at or above MinBalance.
func (p *Position) IsAboveMinimum() bool {
	return p.Balance.GreaterThanOrEqual(p.MinBalance)
}

// CanLock returns true if the position has enough available balance to lock the given amount.
func (p *Position) CanLock(amount decimal.Decimal) bool {
	return p.Available().GreaterThanOrEqual(amount)
}
