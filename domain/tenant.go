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
	// CryptoCollectionBPS is the fee in basis points for crypto deposit collection.
	CryptoCollectionBPS int `json:"crypto_collection_bps"`
	// CryptoCollectionMaxFeeUSD is the maximum crypto collection fee per deposit in USD.
	CryptoCollectionMaxFeeUSD decimal.Decimal `json:"crypto_collection_max_fee_usd"`
	// BankCollectionBPS is the fee in basis points for bank deposit collection.
	BankCollectionBPS int `json:"bank_collection_bps"`
	// BankCollectionMinFeeUSD is the minimum bank collection fee per deposit in USD.
	BankCollectionMinFeeUSD decimal.Decimal `json:"bank_collection_min_fee_usd"`
	// BankCollectionMaxFeeUSD is the maximum bank collection fee per deposit in USD.
	BankCollectionMaxFeeUSD decimal.Decimal `json:"bank_collection_max_fee_usd"`
	// FeeScheduleVersion tracks the version of the fee schedule for audit trail.
	FeeScheduleVersion int `json:"fee_schedule_version"`
	// FeeScheduleUpdatedAt records when the fee schedule was last modified.
	FeeScheduleUpdatedAt time.Time `json:"fee_schedule_updated_at,omitempty"`
}

// Validate ValidateFeeSchedule checks that the fee schedule has sensible values:
//   - All BPS fields must be in [0, 10000] (0% to 100%).
//   - MinFeeUSD must not exceed MaxFeeUSD (when both are set).
//   - BankCollectionMinFeeUSD must not exceed BankCollectionMaxFeeUSD (when both are set).
func (f FeeSchedule) Validate() error {
	type bpsField struct {
		name string
		val  int
	}
	for _, bf := range []bpsField{
		{"OnRampBPS", f.OnRampBPS},
		{"OffRampBPS", f.OffRampBPS},
		{"CryptoCollectionBPS", f.CryptoCollectionBPS},
		{"BankCollectionBPS", f.BankCollectionBPS},
	} {
		if bf.val < 0 || bf.val > 10000 {
			return fmt.Errorf("settla-domain: %s must be between 0 and 10000, got %d", bf.name, bf.val)
		}
	}

	if !f.MinFeeUSD.IsZero() && !f.MaxFeeUSD.IsZero() && f.MinFeeUSD.GreaterThan(f.MaxFeeUSD) {
		return fmt.Errorf("settla-domain: MinFeeUSD (%s) must not exceed MaxFeeUSD (%s)", f.MinFeeUSD, f.MaxFeeUSD)
	}
	if !f.BankCollectionMinFeeUSD.IsZero() && !f.BankCollectionMaxFeeUSD.IsZero() && f.BankCollectionMinFeeUSD.GreaterThan(f.BankCollectionMaxFeeUSD) {
		return fmt.Errorf("settla-domain: BankCollectionMinFeeUSD (%s) must not exceed BankCollectionMaxFeeUSD (%s)", f.BankCollectionMinFeeUSD, f.BankCollectionMaxFeeUSD)
	}

	return nil
}

// bpsDivisor converts basis points to a decimal fraction (1/10000).
var bpsDivisor = decimal.NewFromInt(10000)

// CalculateFee computes the fee for a given amount based on the fee type.
// feeType must be "onramp", "offramp", or "crypto_collection". The computed fee
// is clamped between MinFeeUSD and the applicable max fee.
// Returns an error for unknown fee types instead of silently returning zero.
func (f FeeSchedule) CalculateFee(amount decimal.Decimal, feeType string) (decimal.Decimal, error) {
	var bps int
	var maxFee decimal.Decimal
	switch feeType {
	case "onramp":
		bps = f.OnRampBPS
		maxFee = f.MaxFeeUSD
	case "offramp":
		bps = f.OffRampBPS
		maxFee = f.MaxFeeUSD
	case "crypto_collection":
		bps = f.CryptoCollectionBPS
		maxFee = f.CryptoCollectionMaxFeeUSD
	case "bank_collection":
		bps = f.BankCollectionBPS
		maxFee = f.BankCollectionMaxFeeUSD
	default:
		return decimal.Zero, fmt.Errorf("settla-domain: unknown fee type %q", feeType)
	}

	if bps < 0 {
		return decimal.Zero, fmt.Errorf("settla-domain: basis points must be non-negative, got %d for fee type %q", bps, feeType)
	}

	fee := amount.Mul(decimal.NewFromInt(int64(bps))).Div(bpsDivisor)

	// Round to 8 decimal places to avoid floating-point dust in fee calculations
	fee = fee.Round(8)

	minFee := f.MinFeeUSD
	if feeType == "bank_collection" && !f.BankCollectionMinFeeUSD.IsZero() {
		minFee = f.BankCollectionMinFeeUSD
	}

	if !minFee.IsZero() && fee.LessThan(minFee) {
		fee = minFee.Round(8)
	}
	if !maxFee.IsZero() && fee.GreaterThan(maxFee) {
		fee = maxFee.Round(8)
	}

	return fee, nil
}

// Tenant represents a fintech customer (e.g., Lemfi, Fincra, Paystack).
// Every piece of data in Settla is scoped to a tenant.
type Tenant struct {
	ID                 uuid.UUID
	Name               string
	Slug               string
	Status             TenantStatus
	FeeSchedule        FeeSchedule
	SettlementModel    SettlementModel
	WebhookURL         string
	WebhookSecret      string
	DailyLimitUSD       decimal.Decimal
	PerTransferLimit    decimal.Decimal
	MaxPendingTransfers int
	KYBStatus           KYBStatus
	KYBVerifiedAt      *time.Time
	Metadata           map[string]string
	CryptoConfig       TenantCryptoConfig
	BankConfig         TenantBankConfig
	NotificationConfig TenantNotificationConfig
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TenantNotificationConfig holds per-tenant email notification settings.
type TenantNotificationConfig struct {
	// EmailEnabled controls whether email notifications are sent for this tenant.
	EmailEnabled bool `json:"email_enabled"`
	// NotificationEmails is the list of email addresses to notify.
	NotificationEmails []string `json:"notification_emails"`
	// NotifyOnSuccess sends emails for successful deposit/transfer completions.
	NotifyOnSuccess bool `json:"notify_on_success"`
	// NotifyOnFailure sends emails for failed deposits/transfers.
	NotifyOnFailure bool `json:"notify_on_failure"`
	// NotifyOnDetection sends emails when on-chain payments are detected.
	NotifyOnDetection bool `json:"notify_on_detection"`
}

// IsActive returns true if the tenant is ACTIVE and KYB VERIFIED.
// Both conditions must be met for a tenant to process transactions.
func (t *Tenant) IsActive() bool {
	return t.Status == TenantStatusActive && t.KYBStatus == KYBStatusVerified
}

// Validate runs all tenant-level validations: fee schedule and metadata.
func (t *Tenant) Validate() error {
	if err := t.FeeSchedule.Validate(); err != nil {
		return err
	}
	if err := t.ValidateMetadata(); err != nil {
		return err
	}
	return nil
}

// ValidateMetadata checks that tenant metadata values are well-formed.
// Currently validates:
//   - auto_refund_currency: must be "source", "stablecoin", or a supported currency code.
func (t *Tenant) ValidateMetadata() error {
	if t.Metadata == nil {
		return nil
	}
	if v, ok := t.Metadata["auto_refund_currency"]; ok {
		switch v {
		case "source", "stablecoin":
			// valid symbolic values
		default:
			// Must be a supported currency code
			if err := ValidateCurrency(Currency(v)); err != nil {
				return fmt.Errorf("settla-domain: invalid auto_refund_currency %q: must be \"source\", \"stablecoin\", or a supported currency code", v)
			}
		}
	}
	return nil
}

func (t *Tenant) ChainSupported(chain CryptoChain) bool {
	chainSupported := false
	for _, c := range t.CryptoConfig.SupportedChains {
		if c == chain {
			chainSupported = true
			break
		}
	}

	return chainSupported

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
