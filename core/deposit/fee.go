package deposit

import (
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// CalculateCollectionFee computes the crypto collection fee for a deposit.
// fee = amount × (bps / 10000), capped at max. Uses the tenant's FeeSchedule.
func CalculateCollectionFee(amount decimal.Decimal, schedule domain.FeeSchedule) decimal.Decimal {
	fee, err := schedule.CalculateFee(amount, "crypto_collection")
	if err != nil {
		// crypto_collection is always a known type; this can only happen if bps is 0
		return decimal.Zero
	}
	return fee
}
