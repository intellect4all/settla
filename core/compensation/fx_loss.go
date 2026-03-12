package compensation

import (
	"github.com/shopspring/decimal"
)

// FXLossResult captures the details of an FX loss from reversing a completed
// on-ramp conversion. The loss is always reported as >= 0; even if the reversal
// rate is favorable (tenant would profit), we report zero loss and do not pass
// through the profit.
type FXLossResult struct {
	OriginalAmount decimal.Decimal // source currency amount in original conversion
	ReversedAmount decimal.Decimal // source currency amount after reversal
	FXLoss         decimal.Decimal // always >= 0
	LossPercent    decimal.Decimal // loss as percentage of original amount
	OriginalRate   decimal.Decimal // rate used for original conversion
	ReversalRate   decimal.Decimal // rate used for reversal
}

// CalculateFXLoss computes the FX loss from reversing a completed on-ramp.
//
// The original on-ramp converted source currency to stablecoins:
//
//	stableAmount = sourceAmount * originalRate
//
// The reversal sells stablecoins back to source currency:
//
//	reversedAmount = stableAmount / reversalRate
//
// The FX loss is the difference between what the tenant originally paid and
// what they get back:
//
//	loss = originalAmount - reversedAmount
//
// Example:
//
//	Original:  GBP 2,847 * 1.2581 = USDT 3,582.82
//	Reversal:  USDT 3,582.82 / 1.2656 = GBP 2,830.21
//	FX loss:   GBP 16.79
//
// If the reversal rate is more favorable (reversedAmount > originalAmount),
// we cap the loss at zero — the tenant does not receive a profit on reversal.
func CalculateFXLoss(
	originalAmount decimal.Decimal,
	originalRate decimal.Decimal,
	reversalRate decimal.Decimal,
) FXLossResult {
	result := FXLossResult{
		OriginalAmount: originalAmount,
		OriginalRate:   originalRate,
		ReversalRate:   reversalRate,
	}

	// Guard against zero rates.
	if originalRate.IsZero() || reversalRate.IsZero() {
		result.ReversedAmount = originalAmount
		result.FXLoss = decimal.Zero
		result.LossPercent = decimal.Zero
		return result
	}

	// stableAmount = originalAmount * originalRate
	stableAmount := originalAmount.Mul(originalRate)

	// reversedAmount = stableAmount / reversalRate
	result.ReversedAmount = stableAmount.Div(reversalRate)

	// loss = originalAmount - reversedAmount
	loss := originalAmount.Sub(result.ReversedAmount)

	// FX loss is always >= 0: tenant doesn't profit from a reversal.
	if loss.IsNegative() {
		loss = decimal.Zero
		// When the rate is favorable, the reversed amount is capped at original.
		result.ReversedAmount = originalAmount
	}

	result.FXLoss = loss

	// Compute loss as a percentage of the original amount.
	if originalAmount.IsPositive() {
		hundred := decimal.NewFromInt(100)
		result.LossPercent = result.FXLoss.Mul(hundred).Div(originalAmount)
	} else {
		result.LossPercent = decimal.Zero
	}

	return result
}
