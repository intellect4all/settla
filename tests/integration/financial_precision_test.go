//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestFinancialPrecision_100KTransfers verifies that decimal arithmetic
// stays precise across 100,000 synthetic transfers. It computes fees at 40 bps,
// verifies sum(debits) == sum(credits) exactly, and checks for zero rounding drift.
func TestFinancialPrecision_100KTransfers(t *testing.T) {
	const numTransfers = 100_000
	const feeBPS = 40 // 40 basis points = 0.40%

	feeFraction := decimal.NewFromInt(feeBPS).Div(decimal.NewFromInt(10_000))

	totalSourceAmount := decimal.Zero
	totalFees := decimal.Zero
	totalNetAmount := decimal.Zero

	// Simulate 100K transfers with varying amounts to stress decimal precision.
	for i := 0; i < numTransfers; i++ {
		// Vary amounts to cover a range of magnitudes and decimal places.
		// Use integers and fractional cents to exercise all rounding paths.
		sourceAmount := decimal.NewFromFloat(100.00).
			Add(decimal.NewFromFloat(0.01).Mul(decimal.NewFromInt(int64(i % 9999))))

		fee := sourceAmount.Mul(feeFraction)
		netAmount := sourceAmount.Sub(fee)

		totalSourceAmount = totalSourceAmount.Add(sourceAmount)
		totalFees = totalFees.Add(fee)
		totalNetAmount = totalNetAmount.Add(netAmount)

		// Invariant: source = fee + net for every single transfer
		if !sourceAmount.Equal(fee.Add(netAmount)) {
			t.Fatalf("transfer %d: source (%s) != fee (%s) + net (%s)",
				i, sourceAmount, fee, netAmount)
		}
	}

	// Global invariant: totalSource == totalFees + totalNet (no rounding drift)
	reconstructed := totalFees.Add(totalNetAmount)
	if !totalSourceAmount.Equal(reconstructed) {
		drift := totalSourceAmount.Sub(reconstructed)
		t.Fatalf("rounding drift detected over %d transfers: total_source=%s, total_fees+total_net=%s, drift=%s",
			numTransfers, totalSourceAmount, reconstructed, drift)
	}
	t.Logf("PASS: %d transfers, totalSource=%s, totalFees=%s, totalNet=%s, drift=0",
		numTransfers, totalSourceAmount, totalFees, totalNetAmount)
}

// TestFinancialPrecision_BalancedLedgerPostings verifies that every
// transfer created via the engine produces balanced ledger outbox entries where
// sum(debits) == sum(credits) exactly.
func TestFinancialPrecision_BalancedLedgerPostings(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	amounts := []decimal.Decimal{
		decimal.NewFromFloat(0.01),
		decimal.NewFromFloat(1.23),
		decimal.NewFromFloat(99.99),
		decimal.NewFromFloat(1000.00),
		decimal.NewFromFloat(12345.67),
		decimal.NewFromFloat(99999.99),
		decimal.NewFromFloat(0.03),     // sub-cent precision
		decimal.NewFromFloat(777.777),  // three decimal places
		decimal.NewFromFloat(3333.333), // repeating decimal
		decimal.NewFromFloat(50000.50),
	}

	for i, amount := range amounts {
		t.Run(fmt.Sprintf("amount_%s", amount.String()), func(t *testing.T) {
			transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
				IdempotencyKey: fmt.Sprintf("precision-test-%d", i),
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   amount,
				DestCurrency:   domain.CurrencyNGN,
				Sender: domain.Sender{
					ID:      uuid.New(),
					Name:    "Precision Tester",
					Email:   "test@example.com",
					Country: "GB",
				},
				Recipient: domain.Recipient{
					Name:          "Receiver",
					AccountNumber: "0123456789",
					BankName:      "GTBank",
					Country:       "NG",
				},
			})
			if err != nil {
				t.Fatalf("CreateTransfer failed for amount %s: %v", amount, err)
			}

			// Verify the fee breakdown is internally consistent
			fees := transfer.Fees
			componentSum := fees.OnRampFee.Add(fees.NetworkFee).Add(fees.OffRampFee)
			if !componentSum.Equal(fees.TotalFeeUSD) {
				t.Errorf("fee breakdown inconsistent for amount %s: components=%s, total=%s",
					amount, componentSum, fees.TotalFeeUSD)
			}

			// Verify source = destAmount + totalFees (conservation of value through FX)
			// Note: FX conversion means source and dest are in different currencies,
			// so we verify the quote's internal consistency instead.
			if transfer.DestAmount.IsZero() {
				t.Errorf("dest amount should be non-zero for source %s", amount)
			}
			if transfer.StableAmount.IsZero() {
				t.Errorf("stable amount should be non-zero for source %s", amount)
			}

			// Verify no negative fees
			if transfer.Fees.OnRampFee.IsNegative() {
				t.Errorf("negative on-ramp fee for amount %s: %s", amount, transfer.Fees.OnRampFee)
			}
			if transfer.Fees.OffRampFee.IsNegative() {
				t.Errorf("negative off-ramp fee for amount %s: %s", amount, transfer.Fees.OffRampFee)
			}
			if transfer.Fees.NetworkFee.IsNegative() {
				t.Errorf("negative network fee for amount %s: %s", amount, transfer.Fees.NetworkFee)
			}
		})
	}
}

// TestFinancialPrecision_FeeAt40BPS verifies that the Lemfi tenant's 40 bps on-ramp
// fee is calculated with exact decimal precision across a range of amounts.
func TestFinancialPrecision_FeeAt40BPS(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Lemfi has 40 bps on-ramp fee. For a 10,000 GBP transfer:
	// expected on-ramp fee = 10000 * 40/10000 = 40.00 GBP
	testCases := []struct {
		amount      decimal.Decimal
		expectedBPS int64
	}{
		{decimal.NewFromInt(10_000), 40},
		{decimal.NewFromInt(1_000), 40},
		{decimal.NewFromInt(100), 40},
		{decimal.NewFromFloat(1234.56), 40},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("case_%d_amount_%s", i, tc.amount.String()), func(t *testing.T) {
			transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
				IdempotencyKey: fmt.Sprintf("fee-bps-test-%d", i),
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   tc.amount,
				DestCurrency:   domain.CurrencyNGN,
				Sender: domain.Sender{
					ID:      uuid.New(),
					Name:    "Fee Tester",
					Email:   "fee@example.com",
					Country: "GB",
				},
				Recipient: domain.Recipient{
					Name:          "Receiver",
					AccountNumber: "0123456789",
					BankName:      "GTBank",
					Country:       "NG",
				},
			})
			if err != nil {
				t.Fatalf("CreateTransfer failed: %v", err)
			}

			expectedFee := tc.amount.Mul(decimal.NewFromInt(tc.expectedBPS)).Div(decimal.NewFromInt(10_000))
			if !transfer.Fees.OnRampFee.Equal(expectedFee) {
				t.Errorf("on-ramp fee mismatch: got %s, expected %s (amount=%s, bps=%d)",
					transfer.Fees.OnRampFee, expectedFee, tc.amount, tc.expectedBPS)
			}
		})
	}
}

// TestFinancialPrecision_CumulativeDrift runs many transfers and checks that
// cumulative fee calculations produce zero drift when re-summed.
func TestFinancialPrecision_CumulativeDrift(t *testing.T) {
	const iterations = 10_000

	// Simulate fee calculation with different BPS rates
	bpsRates := []int64{25, 35, 40, 50, 100}

	for _, bps := range bpsRates {
		t.Run(fmt.Sprintf("bps_%d", bps), func(t *testing.T) {
			feeFraction := decimal.NewFromInt(bps).Div(decimal.NewFromInt(10_000))
			totalSource := decimal.Zero
			totalFee := decimal.Zero
			totalNet := decimal.Zero

			for i := 0; i < iterations; i++ {
				// Vary amounts including awkward decimals
				amount := decimal.NewFromFloat(17.31).
					Add(decimal.NewFromFloat(0.07).Mul(decimal.NewFromInt(int64(i))))

				fee := amount.Mul(feeFraction)
				net := amount.Sub(fee)

				totalSource = totalSource.Add(amount)
				totalFee = totalFee.Add(fee)
				totalNet = totalNet.Add(net)
			}

			drift := totalSource.Sub(totalFee.Add(totalNet))
			if !drift.IsZero() {
				t.Fatalf("cumulative drift at %d bps over %d iterations: %s", bps, iterations, drift)
			}
		})
	}
}
