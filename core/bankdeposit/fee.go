package bankdeposit

import (
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// CalculateBankCollectionFee computes the bank collection fee for a deposit.
// fee = amount * (bps / 10000), clamped between min and max.
// Uses the tenant's FeeSchedule with fee type "bank_collection".
func CalculateBankCollectionFee(amount decimal.Decimal, schedule domain.FeeSchedule) decimal.Decimal {
	fee, err := schedule.CalculateFee(amount, "bank_collection")
	if err != nil {
		// bank_collection is always a known type; this can only happen if bps is 0
		return decimal.Zero
	}
	return fee
}
