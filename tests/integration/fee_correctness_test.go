//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// bpsDivisor is used throughout fee math to convert basis points to a fraction.
var bpsDivisor = decimal.NewFromInt(10000)

// TestPerTenantFeeSchedule verifies that the quote engine applies each tenant's
// basis-point fee schedule correctly, that Fincra's lower BPS yields a lower fee
// than Lemfi's for an equivalent amount, and that the returned dest_amount
// satisfies: dest_amount = (source_amount - total_fee) × fx_rate.
func TestPerTenantFeeSchedule(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const gbpAmount = 1000

	// ── Lemfi: 40 bps on-ramp, 35 bps off-ramp (GBP→NGN) ─────────────────

	lemfiQuote, err := h.Engine.GetQuote(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(gbpAmount),
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	})
	if err != nil {
		t.Fatalf("GetQuote for Lemfi failed: %v", err)
	}

	lemfiTenant, err := h.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant Lemfi: %v", err)
	}

	// Verify tenant fees are present and positive.
	if lemfiQuote.Fees.OnRampFee.IsZero() {
		t.Error("Lemfi on-ramp fee should not be zero for 1000 GBP")
	}
	if lemfiQuote.Fees.OffRampFee.IsZero() {
		t.Error("Lemfi off-ramp fee should not be zero for 1000 GBP")
	}

	// Verify on-ramp BPS calculation: on-ramp fee = source_amount * onramp_bps / 10000.
	expectedLemfiOnRamp := decimal.NewFromInt(gbpAmount).
		Mul(decimal.NewFromInt(int64(lemfiTenant.FeeSchedule.OnRampBPS))).
		Div(bpsDivisor)

	// Clamp to min/max if configured (the test tenant has no min/max set, but
	// we respect the same logic as CalculateFee for correctness).
	if !lemfiTenant.FeeSchedule.MinFeeUSD.IsZero() && expectedLemfiOnRamp.LessThan(lemfiTenant.FeeSchedule.MinFeeUSD) {
		expectedLemfiOnRamp = lemfiTenant.FeeSchedule.MinFeeUSD
	}
	if !lemfiTenant.FeeSchedule.MaxFeeUSD.IsZero() && expectedLemfiOnRamp.GreaterThan(lemfiTenant.FeeSchedule.MaxFeeUSD) {
		expectedLemfiOnRamp = lemfiTenant.FeeSchedule.MaxFeeUSD
	}

	if !lemfiQuote.Fees.OnRampFee.Equal(expectedLemfiOnRamp) {
		t.Errorf("Lemfi on-ramp fee: want %s (40 bps of %d GBP), got %s",
			expectedLemfiOnRamp, gbpAmount, lemfiQuote.Fees.OnRampFee)
	}

	// Off-ramp fee is BPS of the intermediate stableAmount (USDT), NOT the source amount.
	// The router calculates: offRampFee = stableAmount * offramp_bps / 10000.
	expectedLemfiOffRamp := lemfiQuote.StableAmount.
		Mul(decimal.NewFromInt(int64(lemfiTenant.FeeSchedule.OffRampBPS))).
		Div(bpsDivisor)
	if !lemfiQuote.Fees.OffRampFee.Equal(expectedLemfiOffRamp) {
		t.Errorf("Lemfi off-ramp fee: want %s (35 bps of %s USDT stable), got %s",
			expectedLemfiOffRamp, lemfiQuote.StableAmount, lemfiQuote.Fees.OffRampFee)
	}

	// Verify network fee is present (provider-level fees: on-ramp + off-ramp + blockchain).
	if lemfiQuote.Fees.NetworkFee.IsZero() {
		t.Error("Lemfi network fee should not be zero (provider fees exist)")
	}

	// Verify total fee = on-ramp + off-ramp + network.
	expectedLemfiTotal := lemfiQuote.Fees.OnRampFee.Add(lemfiQuote.Fees.OffRampFee).Add(lemfiQuote.Fees.NetworkFee)
	if !lemfiQuote.Fees.TotalFeeUSD.Equal(expectedLemfiTotal) {
		t.Errorf("Lemfi total fee: want %s (onramp %s + offramp %s + network %s), got %s",
			expectedLemfiTotal, lemfiQuote.Fees.OnRampFee, lemfiQuote.Fees.OffRampFee,
			lemfiQuote.Fees.NetworkFee, lemfiQuote.Fees.TotalFeeUSD)
	}

	// Verify dest_amount = (source_amount - total_fee) * fx_rate.
	lemfiNetAmount := decimal.NewFromInt(gbpAmount).Sub(lemfiQuote.Fees.TotalFeeUSD)
	lemfiExpectedDest := lemfiNetAmount.Mul(lemfiQuote.FXRate)
	if !lemfiQuote.DestAmount.Equal(lemfiExpectedDest) {
		t.Errorf("Lemfi dest_amount: want %s = (%d - %s) × %s, got %s",
			lemfiExpectedDest, gbpAmount, lemfiQuote.Fees.TotalFeeUSD, lemfiQuote.FXRate, lemfiQuote.DestAmount)
	}

	// ── Fincra: 25 bps on-ramp, 20 bps off-ramp (NGN→GBP) ────────────────

	// Use 300_000 NGN — well within Fincra's 500_000 NGN per-transfer limit.
	const ngnAmount = 300_000

	fincraTenant, err := h.TenantStore.GetTenant(ctx, FincraTenantID)
	if err != nil {
		t.Fatalf("GetTenant Fincra: %v", err)
	}

	fincraQuote, err := h.Engine.GetQuote(ctx, FincraTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(ngnAmount),
		DestCurrency:   domain.CurrencyGBP,
		DestCountry:    "GB",
	})
	if err != nil {
		t.Fatalf("GetQuote for Fincra failed: %v", err)
	}

	// Verify Fincra BPS calculation.
	expectedFincraOnRamp := decimal.NewFromInt(ngnAmount).
		Mul(decimal.NewFromInt(int64(fincraTenant.FeeSchedule.OnRampBPS))).
		Div(bpsDivisor)

	if !fincraQuote.Fees.OnRampFee.Equal(expectedFincraOnRamp) {
		t.Errorf("Fincra on-ramp fee: want %s (25 bps of %d NGN), got %s",
			expectedFincraOnRamp, ngnAmount, fincraQuote.Fees.OnRampFee)
	}

	// Off-ramp fee is BPS of stableAmount (USDT), not source amount.
	expectedFincraOffRamp := fincraQuote.StableAmount.
		Mul(decimal.NewFromInt(int64(fincraTenant.FeeSchedule.OffRampBPS))).
		Div(bpsDivisor)
	if !fincraQuote.Fees.OffRampFee.Equal(expectedFincraOffRamp) {
		t.Errorf("Fincra off-ramp fee: want %s (20 bps of %s USDT stable), got %s",
			expectedFincraOffRamp, fincraQuote.StableAmount, fincraQuote.Fees.OffRampFee)
	}

	// Verify Fincra's BPS rates (25/20) are lower than Lemfi's (40/35).
	if fincraTenant.FeeSchedule.OnRampBPS >= lemfiTenant.FeeSchedule.OnRampBPS {
		t.Errorf("Fincra on-ramp BPS (%d) should be lower than Lemfi (%d)",
			fincraTenant.FeeSchedule.OnRampBPS, lemfiTenant.FeeSchedule.OnRampBPS)
	}
	if fincraTenant.FeeSchedule.OffRampBPS >= lemfiTenant.FeeSchedule.OffRampBPS {
		t.Errorf("Fincra off-ramp BPS (%d) should be lower than Lemfi (%d)",
			fincraTenant.FeeSchedule.OffRampBPS, lemfiTenant.FeeSchedule.OffRampBPS)
	}

	// Verify BPS-only fees (on-ramp + off-ramp) are lower for Fincra on an
	// equal source amount. Uses a unit amount with no min/max for a clean comparison.
	equalAmount := decimal.NewFromInt(1000)
	lemfiBPSFee := equalAmount.
		Mul(decimal.NewFromInt(int64(lemfiTenant.FeeSchedule.OnRampBPS + lemfiTenant.FeeSchedule.OffRampBPS))).
		Div(bpsDivisor)
	fincraBPSFee := equalAmount.
		Mul(decimal.NewFromInt(int64(fincraTenant.FeeSchedule.OnRampBPS + fincraTenant.FeeSchedule.OffRampBPS))).
		Div(bpsDivisor)
	if !fincraBPSFee.LessThan(lemfiBPSFee) {
		t.Errorf("Fincra combined BPS fee (%s) should be less than Lemfi (%s) for same amount",
			fincraBPSFee, lemfiBPSFee)
	}

	// Verify Fincra dest_amount formula.
	fincraNetAmount := decimal.NewFromInt(ngnAmount).Sub(fincraQuote.Fees.TotalFeeUSD)
	fincraExpectedDest := fincraNetAmount.Mul(fincraQuote.FXRate)
	if !fincraQuote.DestAmount.Equal(fincraExpectedDest) {
		t.Errorf("Fincra dest_amount: want %s = (%d - %s) × %s, got %s",
			fincraExpectedDest, ngnAmount, fincraQuote.Fees.TotalFeeUSD, fincraQuote.FXRate, fincraQuote.DestAmount)
	}

	// Sanity: dest amounts are positive.
	if !lemfiQuote.DestAmount.IsPositive() {
		t.Errorf("Lemfi dest_amount should be positive, got %s", lemfiQuote.DestAmount)
	}
	if !fincraQuote.DestAmount.IsPositive() {
		t.Errorf("Fincra dest_amount should be positive, got %s", fincraQuote.DestAmount)
	}

	t.Logf("Lemfi 1000 GBP→NGN: on-ramp fee=%s, off-ramp fee=%s, total=%s, dest=%s NGN",
		lemfiQuote.Fees.OnRampFee, lemfiQuote.Fees.OffRampFee,
		lemfiQuote.Fees.TotalFeeUSD, lemfiQuote.DestAmount.StringFixed(2))
	t.Logf("Fincra 300000 NGN→GBP: on-ramp fee=%s, off-ramp fee=%s, total=%s, dest=%s GBP",
		fincraQuote.Fees.OnRampFee, fincraQuote.Fees.OffRampFee,
		fincraQuote.Fees.TotalFeeUSD, fincraQuote.DestAmount.StringFixed(6))
}

// TestFeeNotChargedOnFailedTransfer verifies that when an on-ramp failure triggers
// a REFUNDING state, the treasury-release intent carries the full source amount —
// i.e., no fee is deducted from the refund.
func TestFeeNotChargedOnFailedTransfer(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sourceAmount := decimal.NewFromInt(500)

	// 1. Create a GBP→NGN transfer for Lemfi.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "fee-no-charge-failed-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   sourceAmount,
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Carlos Mendes",
			Email:   "carlos@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Ngozi Eze",
			AccountNumber: "1122334455",
			BankName:      "UBA",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	// The fees are quoted and stored in the transfer at creation time.
	totalFeeQuoted := transfer.Fees.TotalFeeUSD

	if totalFeeQuoted.IsZero() {
		t.Fatal("expected non-zero fees in transfer (Lemfi has 40/35 bps), got zero")
	}

	t.Logf("transfer created: source=%s GBP, on-ramp fee=%s, off-ramp fee=%s, total fee=%s",
		sourceAmount, transfer.Fees.OnRampFee, transfer.Fees.OffRampFee, totalFeeQuoted)

	// 2. Fund (CREATED → FUNDED) and execute the outbox so treasury is reserved
	//    for the full source amount (not source minus fees).
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx) // processes IntentTreasuryReserve

	// 3. Initiate on-ramp (FUNDED → ON_RAMPING).
	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	h.TransferStore.drainOutbox()

	// 4. Fail the on-ramp immediately (ON_RAMPING → REFUNDING).
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:   false,
		Error:     "provider_rejected",
		ErrorCode: "REJECT",
	}); err != nil {
		t.Fatalf("HandleOnRampResult(failure) failed: %v", err)
	}

	// 5. Inspect the treasury-release intent written by the failure path.
	entries := h.TransferStore.drainOutbox()
	releaseEntry := findOutboxEntry(entries, domain.IntentTreasuryRelease)
	if releaseEntry == nil {
		t.Fatalf("expected IntentTreasuryRelease after on-ramp failure, got entries: %v",
			outboxEventTypes(entries))
	}

	var releasePayload domain.TreasuryReleasePayload
	if err := json.Unmarshal(releaseEntry.Payload, &releasePayload); err != nil {
		t.Fatalf("unmarshal TreasuryReleasePayload: %v", err)
	}

	// 6. The refund MUST equal the full source amount — no fee deduction.
	if !releasePayload.Amount.Equal(sourceAmount) {
		t.Errorf("refund amount should equal full source amount %s (no fee deduction on failure), got %s",
			sourceAmount, releasePayload.Amount)
	}

	// Explicitly assert the refund is NOT the fee-adjusted amount.
	feeDeductedAmount := sourceAmount.Sub(totalFeeQuoted)
	if releasePayload.Amount.Equal(feeDeductedAmount) {
		t.Errorf("refund amount incorrectly equals source minus fees (%s); "+
			"refund on failure must be the full source amount %s",
			feeDeductedAmount, sourceAmount)
	}

	// 7. Verify the transfer moved to REFUNDING (on-ramp failure path).
	updated, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer after failure: %v", err)
	}
	if updated.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING after on-ramp failure, got %s", updated.Status)
	}

	t.Logf("failed transfer: source=%s GBP, quoted fee=%s, refunded=%s GBP (full, no fee deduction)",
		sourceAmount, totalFeeQuoted, releasePayload.Amount)
}
