package compensation

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// Strategy is an alias for domain.CompensationStrategy.
type Strategy = domain.CompensationStrategy

// CompensationPlan is an alias for domain.CompensationPlan.
type CompensationPlan = domain.CompensationPlan

// CompensationStep is an alias for domain.CompensationStep.
type CompensationStep = domain.CompensationStep

const (
	StrategySimpleRefund    = domain.CompensationSimpleRefund
	StrategyReverseOnRamp   = domain.CompensationReverseOnRamp
	StrategyCreditStablecoin = domain.CompensationCreditStablecoin
	StrategyManualReview    = domain.CompensationManualReview
)

// Completed step identifiers used to determine what has already happened.
const (
	StepFunded          = "funded"
	StepOnRampCompleted = "onramp_completed"
	StepSettling        = "settling"
	StepOffRamping      = "off_ramping"
)

// autoRefundCurrency reads the tenant's preferred refund currency from metadata.
// Returns "source" if the tenant has no preference set.
func autoRefundCurrency(tenant *domain.Tenant) string {
	if tenant.Metadata != nil {
		if v, ok := tenant.Metadata["auto_refund_currency"]; ok {
			return v
		}
	}
	return "source"
}

// ExternalStatus carries optional provider/blockchain status information that
// callers can populate (e.g. via recovery.ProviderStatusChecker) before calling
// DetermineCompensation. This allows the compensation system to resolve
// ambiguous states (ON_RAMPING, SETTLING) without always falling back to
// manual review.
type ExternalStatus struct {
	// OnRampStatus is the provider's reported status: "completed", "failed",
	// "pending", "unknown", or "" (not checked).
	OnRampStatus string
	// BlockchainConfirmed indicates whether the on-chain tx is confirmed.
	// nil means not checked / unknown.
	BlockchainConfirmed *bool
	// BlockchainError is set when the blockchain check returned an error.
	BlockchainError string
	// CurrentRate is the live FX rate for the corridor, used to estimate FX loss
	// when reversing an on-ramp. Zero or unset means the rate was not fetched.
	CurrentRate decimal.Decimal
}

// EstimateFXLoss computes the estimated FX loss when reversing a stablecoin position
// back to fiat at the current rate vs. the original rate. The result is in source
// currency units: positive means the tenant receives less fiat than originally sent.
func EstimateFXLoss(stableAmount, originalRate, currentRate decimal.Decimal) decimal.Decimal {
	if currentRate.IsZero() || originalRate.IsZero() {
		return decimal.Zero // Cannot estimate — return zero loss
	}
	return stableAmount.Div(currentRate).Sub(stableAmount.Div(originalRate)).Round(8)
}

// DetermineCompensation analyzes what completed vs what failed in a transfer
// and determines the correct recovery strategy.
//
// completedSteps should contain identifiers for each step that completed
// successfully (e.g., "funded", "onramp_completed").
//
// extStatus carries optional external provider/blockchain status to resolve
// ambiguous states. Pass ExternalStatus{} if external status is unavailable.
func DetermineCompensation(
	transfer *domain.Transfer,
	tenant *domain.Tenant,
	completedSteps []string,
	extStatus ExternalStatus,
) CompensationPlan {
	plan := CompensationPlan{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		FXLoss:         decimal.Zero,
		TransferStatus: transfer.Status,
	}

	hasOnRamp := containsStep(completedSteps, StepOnRampCompleted)

	// Check for ambiguous states: if the transfer is in SETTLING or ON_RAMPING
	// and we cannot confirm whether the step completed, flag for manual review.
	if isAmbiguousState(transfer, completedSteps, extStatus) {
		plan.Strategy = StrategyManualReview
		plan.RefundAmount = decimal.Zero
		plan.RefundCurrency = transfer.SourceCurrency
		return plan
	}

	// If ON_RAMPING with known provider status, override hasOnRamp accordingly.
	if transfer.Status == domain.TransferStatusOnRamping {
		switch extStatus.OnRampStatus {
		case "completed":
			hasOnRamp = true
		case "failed":
			// Provider confirmed failure — simple refund.
			plan.Strategy = StrategySimpleRefund
			plan.RefundAmount = transfer.SourceAmount
			plan.RefundCurrency = transfer.SourceCurrency
			plan.Steps = buildSimpleRefundSteps(transfer, tenant.Slug)
			return plan
		}
	}

	if !hasOnRamp {
		// Nothing beyond funding completed — simple refund.
		plan.Strategy = StrategySimpleRefund
		plan.RefundAmount = transfer.SourceAmount
		plan.RefundCurrency = transfer.SourceCurrency
		plan.Steps = buildSimpleRefundSteps(transfer, tenant.Slug)
		return plan
	}

	// On-ramp completed. Determine refund method based on tenant preference.
	refundPref := autoRefundCurrency(tenant)

	if refundPref == "stablecoin" {
		plan.Strategy = StrategyCreditStablecoin
		plan.RefundAmount = transfer.StableAmount
		plan.RefundCurrency = transfer.StableCoin
		plan.FXLoss = decimal.Zero
		plan.Steps = buildCreditStablecoinSteps(transfer)
		return plan
	}

	// Default: reverse on-ramp (sell stablecoins back to source currency).
	plan.Strategy = StrategyReverseOnRamp

	// We need the current reversal rate to compute FX loss.
	// At plan creation time we use the original rate as a placeholder;
	// the executor will fetch a live rate. For the plan, estimate using
	// the original rate (conservative: assume some spread).
	plan.RefundAmount = transfer.SourceAmount // will be adjusted by executor with real rate
	plan.RefundCurrency = transfer.SourceCurrency
	plan.Steps = buildReverseOnRampSteps(transfer, tenant.Slug)

	// If a live rate was provided, estimate the FX loss from reversing.
	if extStatus.CurrentRate.IsPositive() {
		plan.FXLoss = EstimateFXLoss(transfer.StableAmount, transfer.FXRate, extStatus.CurrentRate)
	}

	return plan
}

// isAmbiguousState returns true if the transfer is in a state where we cannot
// confidently determine what has completed and what hasn't. When external
// status is available (via ExternalStatus), it resolves the ambiguity.
func isAmbiguousState(transfer *domain.Transfer, completedSteps []string, ext ExternalStatus) bool {
	switch transfer.Status {
	case domain.TransferStatusOnRamping:
		// If caller provided provider status, use it to resolve ambiguity.
		switch ext.OnRampStatus {
		case "completed", "failed":
			return false // status is known — caller can determine strategy
		default:
			// "pending", "unknown", or "" — still ambiguous.
			return true
		}
	case domain.TransferStatusSettling:
		// If settling but on-ramp completed is not in steps, something is off.
		if !containsStep(completedSteps, StepOnRampCompleted) {
			return true
		}
		// If caller provided blockchain status, use it to resolve ambiguity.
		if ext.BlockchainConfirmed != nil {
			return false // status is known (confirmed or failed)
		}
		if ext.BlockchainError != "" {
			return false // blockchain check returned error — treat as known failure
		}
	}
	return false
}

func containsStep(steps []string, step string) bool {
	for _, s := range steps {
		if s == step {
			return true
		}
	}
	return false
}

func buildSimpleRefundSteps(transfer *domain.Transfer, tenantSlug string) []CompensationStep {
	var steps []CompensationStep

	releasePayload, _ := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   "bank:" + lower(string(transfer.SourceCurrency)),
		Reason:     "compensation_simple_refund",
	})
	steps = append(steps, CompensationStep{
		Type:    domain.IntentTreasuryRelease,
		Payload: releasePayload,
	})

	// Build reversal lines to undo any on-ramp posting
	var reversalLines []domain.LedgerLineEntry
	if transfer.StableAmount.IsPositive() {
		reversalLines = []domain.LedgerLineEntry{
			{
				AccountCode: "assets:crypto:" + lower(string(transfer.StableCoin)) + ":" + lower(transfer.Chain),
				EntryType:   "CREDIT",
				Amount:      transfer.StableAmount,
				Currency:    string(transfer.StableCoin),
				Description: "Compensation: reverse crypto asset",
			},
			{
				AccountCode: "expenses:provider:onramp",
				EntryType:   "CREDIT",
				Amount:      transfer.Fees.OnRampFee,
				Currency:    string(transfer.SourceCurrency),
				Description: "Compensation: reverse on-ramp fee",
			},
			{
				AccountCode: domain.TenantAccountCode(tenantSlug, "assets:bank:"+lower(string(transfer.SourceCurrency))+":clearing"),
				EntryType:   "DEBIT",
				Amount:      transfer.SourceAmount,
				Currency:    string(transfer.SourceCurrency),
				Description: "Compensation: debit clearing account",
			},
		}
	}

	reversePayload, _ := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: "compensation-reverse:" + transfer.ID.String(),
		Description:    "Compensation reversal for transfer " + transfer.ID.String(),
		ReferenceType:  "reversal",
		Lines:          reversalLines,
	})
	steps = append(steps, CompensationStep{
		Type:    domain.IntentLedgerReverse,
		Payload: reversePayload,
	})

	return steps
}

func buildReverseOnRampSteps(transfer *domain.Transfer, tenantSlug string) []CompensationStep {
	var steps []CompensationStep

	reverseOnRampPayload, _ := json.Marshal(ProviderReverseOnRampPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		ProviderID:     transfer.OnRampProviderID,
		StableAmount:   transfer.StableAmount,
		StableCoin:     transfer.StableCoin,
		SourceCurrency: transfer.SourceCurrency,
		OriginalRate:   transfer.FXRate,
	})
	steps = append(steps, CompensationStep{
		Type:    domain.IntentProviderReverseOnRamp,
		Payload: reverseOnRampPayload,
	})

	releasePayload, _ := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   "bank:" + lower(string(transfer.SourceCurrency)),
		Reason:     "compensation_reverse_onramp",
	})
	steps = append(steps, CompensationStep{
		Type:    domain.IntentTreasuryRelease,
		Payload: releasePayload,
	})

	// Build reversal lines for reverse-onramp compensation
	var reverseLines []domain.LedgerLineEntry
	if transfer.StableAmount.IsPositive() {
		reverseLines = []domain.LedgerLineEntry{
			{
				AccountCode: "assets:crypto:" + lower(string(transfer.StableCoin)) + ":" + lower(transfer.Chain),
				EntryType:   "CREDIT",
				Amount:      transfer.StableAmount,
				Currency:    string(transfer.StableCoin),
				Description: "Reverse on-ramp: credit crypto asset",
			},
			{
				AccountCode: "expenses:provider:onramp",
				EntryType:   "CREDIT",
				Amount:      transfer.Fees.OnRampFee,
				Currency:    string(transfer.SourceCurrency),
				Description: "Reverse on-ramp: credit on-ramp fee",
			},
			{
				AccountCode: domain.TenantAccountCode(tenantSlug, "assets:bank:"+lower(string(transfer.SourceCurrency))+":clearing"),
				EntryType:   "DEBIT",
				Amount:      transfer.SourceAmount,
				Currency:    string(transfer.SourceCurrency),
				Description: "Reverse on-ramp: debit clearing account",
			},
		}
	}

	reversePayload, _ := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: "compensation-reverse-onramp:" + transfer.ID.String(),
		Description:    "Reverse on-ramp compensation for transfer " + transfer.ID.String(),
		ReferenceType:  "reversal",
		Lines:          reverseLines,
	})
	steps = append(steps, CompensationStep{
		Type:    domain.IntentLedgerReverse,
		Payload: reversePayload,
	})

	return steps
}

func buildCreditStablecoinSteps(transfer *domain.Transfer) []CompensationStep {
	var steps []CompensationStep

	creditPayload, _ := json.Marshal(PositionCreditPayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.StableCoin,
		Amount:     transfer.StableAmount,
	})
	steps = append(steps, CompensationStep{
		Type:    "position.credit",
		Payload: creditPayload,
	})

	releasePayload, _ := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   "bank:" + lower(string(transfer.SourceCurrency)),
		Reason:     "compensation_credit_stablecoin",
	})
	steps = append(steps, CompensationStep{
		Type:    domain.IntentTreasuryRelease,
		Payload: releasePayload,
	})

	return steps
}

// ProviderReverseOnRampPayload is the intent payload for reversing a completed
// on-ramp (selling stablecoins back to source currency).
type ProviderReverseOnRampPayload struct {
	TransferID     uuid.UUID       `json:"transfer_id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	ProviderID     string          `json:"provider_id"`
	StableAmount   decimal.Decimal `json:"stable_amount"`
	StableCoin     domain.Currency `json:"stable_coin"`
	SourceCurrency domain.Currency `json:"source_currency"`
	OriginalRate   decimal.Decimal `json:"original_rate"`
}

// PositionCreditPayload is the intent payload for crediting a tenant's
// stablecoin position as compensation.
type PositionCreditPayload struct {
	TransferID uuid.UUID       `json:"transfer_id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	Currency   domain.Currency `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
}

func lower(s string) string {
	// Simple ASCII lowercase — currencies are always ASCII.
	b := make([]byte, len(s))
	for i := range s {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b[i] = s[i] + 32
		} else {
			b[i] = s[i]
		}
	}
	return string(b)
}
