package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PaymentLinkStatus represents the lifecycle state of a payment link.
type PaymentLinkStatus string

const (
	PaymentLinkStatusActive   PaymentLinkStatus = "ACTIVE"
	PaymentLinkStatusExpired  PaymentLinkStatus = "EXPIRED"
	PaymentLinkStatusDisabled PaymentLinkStatus = "DISABLED"
)

// PaymentLinkSessionConfig holds the template configuration for deposit sessions
// created when a payment link is redeemed. Stored as JSONB in the database.
type PaymentLinkSessionConfig struct {
	Amount         decimal.Decimal      `json:"amount"`
	Currency       Currency             `json:"currency"`
	Chain          CryptoChain          `json:"chain"`
	Token          string               `json:"token"`
	SettlementPref SettlementPreference `json:"settlement_pref,omitempty"`
	TTLSeconds     int32                `json:"ttl_seconds,omitempty"`
}

// PaymentLink is a shareable URL template that creates deposit sessions on redemption.
// It is a simple CRUD entity — no state machine, no outbox. The heavy lifting
// (address dispensing, chain monitoring, crediting, settlement) is delegated
// entirely to the existing deposit engine.
type PaymentLink struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	ShortCode   string
	Description string
	RedirectURL string
	Status      PaymentLinkStatus

	SessionConfig PaymentLinkSessionConfig

	UseLimit *int
	UseCount int

	ExpiresAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CanRedeem returns true if the payment link can be used to create a new deposit session.
func (pl *PaymentLink) CanRedeem() error {
	if pl.Status == PaymentLinkStatusDisabled {
		return ErrPaymentLinkDisabled(pl.ID.String())
	}
	if pl.Status == PaymentLinkStatusExpired {
		return ErrPaymentLinkExpired(pl.ID.String())
	}
	if pl.ExpiresAt != nil && time.Now().UTC().After(*pl.ExpiresAt) {
		return ErrPaymentLinkExpired(pl.ID.String())
	}
	if pl.UseLimit != nil && pl.UseCount >= *pl.UseLimit {
		return ErrPaymentLinkExhausted(pl.ID.String())
	}
	return nil
}
