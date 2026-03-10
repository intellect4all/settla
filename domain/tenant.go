package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TenantStatus represents the lifecycle state of a tenant.
type TenantStatus string

const (
	// TenantStatusActive indicates the tenant is fully operational.
	TenantStatusActive TenantStatus = "ACTIVE"
	// TenantStatusSuspended indicates the tenant has been suspended.
	TenantStatusSuspended TenantStatus = "SUSPENDED"
	// TenantStatusOnboarding indicates the tenant is still going through setup.
	TenantStatusOnboarding TenantStatus = "ONBOARDING"
)

// SettlementModel determines how a tenant funds settlements.
type SettlementModel string

const (
	// SettlementModelPrefunded requires the tenant to pre-fund a treasury position.
	SettlementModelPrefunded SettlementModel = "PREFUNDED"
	// SettlementModelNetSettlement settles on a net basis at end of period.
	SettlementModelNetSettlement SettlementModel = "NET_SETTLEMENT"
)

// KYBStatus represents the Know-Your-Business verification state.
type KYBStatus string

const (
	// KYBStatusPending indicates verification is not yet started.
	KYBStatusPending KYBStatus = "PENDING"
	// KYBStatusInReview indicates documents are being reviewed.
	KYBStatusInReview KYBStatus = "IN_REVIEW"
	// KYBStatusVerified indicates the tenant has passed KYB checks.
	KYBStatusVerified KYBStatus = "VERIFIED"
	// KYBStatusRejected indicates the tenant failed KYB checks.
	KYBStatusRejected KYBStatus = "REJECTED"
)

// FeeSchedule defines the per-tenant fee configuration in basis points.
// Basis points: 1 bp = 0.01%, so 40 bps = 0.40%.
type FeeSchedule struct {
	// OnRampBPS is the fee in basis points for converting fiat to stablecoin.
	OnRampBPS int `json:"onramp_bps"`
	// OffRampBPS is the fee in basis points for converting stablecoin to fiat.
	OffRampBPS int `json:"offramp_bps"`
	// MinFeeUSD is the minimum fee charged per transaction in USD.
	MinFeeUSD decimal.Decimal `json:"min_fee_usd"`
	// MaxFeeUSD is the maximum fee charged per transaction in USD.
	MaxFeeUSD decimal.Decimal `json:"max_fee_usd"`
}

// bpsDivisor converts basis points to a decimal fraction (1/10000).
var bpsDivisor = decimal.NewFromInt(10000)

// CalculateFee computes the fee for a given amount based on the fee type.
// feeType must be "onramp" or "offramp". The computed fee is clamped
// between MinFeeUSD and MaxFeeUSD.
// Returns an error for unknown fee types instead of silently returning zero.
func (f FeeSchedule) CalculateFee(amount decimal.Decimal, feeType string) (decimal.Decimal, error) {
	var bps int
	switch feeType {
	case "onramp":
		bps = f.OnRampBPS
	case "offramp":
		bps = f.OffRampBPS
	default:
		return decimal.Zero, fmt.Errorf("settla-domain: unknown fee type %q", feeType)
	}

	fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)

	if !f.MinFeeUSD.IsZero() && fee.LessThan(f.MinFeeUSD) {
		fee = f.MinFeeUSD
	}
	if !f.MaxFeeUSD.IsZero() && fee.GreaterThan(f.MaxFeeUSD) {
		fee = f.MaxFeeUSD
	}

	return fee, nil
}

// Tenant represents a fintech customer (e.g., Lemfi, Fincra, Paystack).
// Every piece of data in Settla is scoped to a tenant.
type Tenant struct {
	ID               uuid.UUID
	Name             string
	Slug             string
	Status           TenantStatus
	FeeSchedule      FeeSchedule
	SettlementModel  SettlementModel
	WebhookURL       string
	WebhookSecret    string
	DailyLimitUSD    decimal.Decimal
	PerTransferLimit decimal.Decimal
	KYBStatus        KYBStatus
	KYBVerifiedAt    *time.Time
	Metadata         map[string]string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// IsActive returns true if the tenant is ACTIVE and KYB VERIFIED.
// Both conditions must be met for a tenant to process transactions.
func (t *Tenant) IsActive() bool {
	return t.Status == TenantStatusActive && t.KYBStatus == KYBStatusVerified
}

// APIKey represents a tenant's API credential used for authentication.
// Keys are stored as SHA-256 hashes; the raw key is never persisted.
type APIKey struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	KeyHash     string // SHA-256 hash of the raw key
	KeyPrefix   string // First 8 chars for identification (e.g., "sk_live_")
	Environment string // "live" or "test"
	Name        string
	IsActive    bool
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}
