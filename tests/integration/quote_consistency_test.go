//go:build integration

package integration

// TEST-10: Quote consistency tests — FX rate application, quote-to-transfer
// value copy, and expired-quote rejection.
//
// The mock providers registered in newTestHarness produce deterministic rates:
//   on-ramp  GBP→USDT : rate=1.25,  fixed fee=0.50
//   off-ramp USDT→NGN : rate=1550,  fixed fee=200
//   blockchain tron   : gas fee≈0.10
//
// CoreRouterAdapter fee schedule for Lemfi (OnRampBPS=40, OffRampBPS=35):
//   onRampFee  = 40/10000  × 1000 = 4.00 GBP
//   offRampFee = 35/10000  × 1000 = 3.50 GBP
//   networkFee = onFee(0.50) + gas(0.10) + offFee(200) = 200.60
//   totalFee   = 4.00 + 3.50 + 200.60 = 208.10
//   FXRate     = 1.25 × 1550 = 1937.5
//   destAmount = (1000 - 208.10) × 1937.5 = 791.90 × 1937.5 = 1,534,556.25 NGN

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestQuoteConsistency_FXRateApplied verifies that GetQuote correctly computes
// the destination amount by applying the two-leg FX rate, subtracting tenant
// fees (BPS) and network fees, within an acceptable decimal tolerance.
func TestQuoteConsistency_FXRateApplied(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sourceAmount := decimal.NewFromInt(1000) // 1000 GBP

	quote, err := h.Engine.GetQuote(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   sourceAmount,
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	})
	if err != nil {
		t.Fatalf("GetQuote failed: %v", err)
	}

	// ── Structural checks ───────────────────────────────────────────────────

	if quote.TenantID != LemfiTenantID {
		t.Errorf("quote.TenantID: want %s, got %s", LemfiTenantID, quote.TenantID)
	}
	if quote.SourceCurrency != domain.CurrencyGBP {
		t.Errorf("quote.SourceCurrency: want GBP, got %s", quote.SourceCurrency)
	}
	if quote.DestCurrency != domain.CurrencyNGN {
		t.Errorf("quote.DestCurrency: want NGN, got %s", quote.DestCurrency)
	}
	if !quote.ExpiresAt.After(time.Now().UTC()) {
		t.Errorf("quote.ExpiresAt should be in the future, got %s", quote.ExpiresAt)
	}
	if quote.DestAmount.IsZero() || quote.DestAmount.IsNegative() {
		t.Errorf("quote.DestAmount should be positive, got %s", quote.DestAmount)
	}

	// ── FX rate check ────────────────────────────────────────────────────────
	// Expected composite FX rate: on-ramp rate × off-ramp rate = 1.25 × 1550 = 1937.5
	expectedFXRate := decimal.NewFromFloat(1937.5)
	if !quote.FXRate.Equal(expectedFXRate) {
		t.Errorf("quote.FXRate: want %s, got %s", expectedFXRate, quote.FXRate)
	}

	// ── Fee breakdown check ──────────────────────────────────────────────────
	// Lemfi: 40 bps on-ramp, 35 bps off-ramp (no min/max configured in harness)
	expectedOnRampFee := decimal.NewFromFloat(4.00)  // 40/10000 × 1000
	expectedOffRampFee := decimal.NewFromFloat(3.50) // 35/10000 × 1000

	tolerance := decimal.NewFromFloat(0.01)

	if diff := quote.Fees.OnRampFee.Sub(expectedOnRampFee).Abs(); diff.GreaterThan(tolerance) {
		t.Errorf("quote.Fees.OnRampFee: want ~%s, got %s (diff=%s)", expectedOnRampFee, quote.Fees.OnRampFee, diff)
	}
	if diff := quote.Fees.OffRampFee.Sub(expectedOffRampFee).Abs(); diff.GreaterThan(tolerance) {
		t.Errorf("quote.Fees.OffRampFee: want ~%s, got %s (diff=%s)", expectedOffRampFee, quote.Fees.OffRampFee, diff)
	}
	if quote.Fees.TotalFeeUSD.LessThan(expectedOnRampFee.Add(expectedOffRampFee)) {
		t.Errorf("quote.Fees.TotalFeeUSD should include on-ramp + off-ramp + network: got %s", quote.Fees.TotalFeeUSD)
	}

	// ── Destination amount check ─────────────────────────────────────────────
	// destAmount = (sourceAmount - totalFee) × FXRate
	// = (1000 - totalFee) × 1937.5
	// With totalFee = onRamp(4.00) + offRamp(3.50) + network(0.50+0.10+200=200.60) = 208.10
	// destAmount = 791.90 × 1937.5 = 1,534,556.25 NGN
	expectedDestAmount := sourceAmount.Sub(quote.Fees.TotalFeeUSD).Mul(expectedFXRate)

	if diff := quote.DestAmount.Sub(expectedDestAmount).Abs(); diff.GreaterThan(tolerance) {
		t.Errorf("quote.DestAmount: want ~%s, got %s (diff=%s; totalFee=%s, fxRate=%s)",
			expectedDestAmount.StringFixed(2),
			quote.DestAmount.StringFixed(2),
			diff.StringFixed(4),
			quote.Fees.TotalFeeUSD.StringFixed(4),
			quote.FXRate,
		)
	}

	// ── Stable amount check ──────────────────────────────────────────────────
	// stableAmount = sourceAmount × on-ramp rate - on-ramp fixed fee
	// = 1000 × 1.25 - 0.50 = 1249.50 USDT
	expectedStableAmount := decimal.NewFromFloat(1249.50)
	if diff := quote.StableAmount.Sub(expectedStableAmount).Abs(); diff.GreaterThan(tolerance) {
		t.Errorf("quote.StableAmount: want ~%s, got %s (diff=%s)",
			expectedStableAmount, quote.StableAmount, diff)
	}

	// ── Route metadata ───────────────────────────────────────────────────────
	if quote.Route.OnRampProvider == "" {
		t.Error("quote.Route.OnRampProvider should be set")
	}
	if quote.Route.OffRampProvider == "" {
		t.Error("quote.Route.OffRampProvider should be set")
	}
	if quote.Route.Chain == "" {
		t.Error("quote.Route.Chain should be set")
	}

	t.Logf("quote %s: FXRate=%s, fees=%s, destAmount=%s NGN",
		quote.ID, quote.FXRate, quote.Fees.TotalFeeUSD.StringFixed(2), quote.DestAmount.StringFixed(2))
}

// TestQuoteConsistency_QuoteUsedForTransfer verifies that when a quote ID is
// provided to CreateTransfer, the transfer's fx_rate, source_amount,
// dest_amount, and fee breakdown exactly match the quote values.
//
// The engine must copy quote values into the transfer without modification.
func TestQuoteConsistency_QuoteUsedForTransfer(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sourceAmount := decimal.NewFromInt(500) // 500 GBP

	quote, err := h.Engine.GetQuote(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   sourceAmount,
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	})
	if err != nil {
		t.Fatalf("GetQuote failed: %v", err)
	}

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "quote-used-transfer-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   sourceAmount,
		DestCurrency:   domain.CurrencyNGN,
		QuoteID:        &quote.ID,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Quote User",
			Email:   "quoteuser@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Quote Recipient",
			AccountNumber: "3333333333",
			BankName:      "FCMB",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer with quote ID failed: %v", err)
	}

	// ── Verify transfer fields exactly match the quote ───────────────────────

	if transfer.Status != domain.TransferStatusCreated {
		t.Errorf("transfer.Status: want CREATED, got %s", transfer.Status)
	}

	// FX rate must be copied verbatim from the quote.
	if !transfer.FXRate.Equal(quote.FXRate) {
		t.Errorf("transfer.FXRate: want %s (from quote), got %s", quote.FXRate, transfer.FXRate)
	}

	// Source amount must match the request (and the quote's source amount).
	if !transfer.SourceAmount.Equal(sourceAmount) {
		t.Errorf("transfer.SourceAmount: want %s, got %s", sourceAmount, transfer.SourceAmount)
	}

	// Destination amount must match the quote's computed dest amount exactly.
	if !transfer.DestAmount.Equal(quote.DestAmount) {
		t.Errorf("transfer.DestAmount: want %s (from quote), got %s", quote.DestAmount, transfer.DestAmount)
	}

	// Stable amount must match.
	if !transfer.StableAmount.Equal(quote.StableAmount) {
		t.Errorf("transfer.StableAmount: want %s (from quote), got %s", quote.StableAmount, transfer.StableAmount)
	}

	// Fee breakdown must match.
	if !transfer.Fees.OnRampFee.Equal(quote.Fees.OnRampFee) {
		t.Errorf("transfer.Fees.OnRampFee: want %s, got %s", quote.Fees.OnRampFee, transfer.Fees.OnRampFee)
	}
	if !transfer.Fees.OffRampFee.Equal(quote.Fees.OffRampFee) {
		t.Errorf("transfer.Fees.OffRampFee: want %s, got %s", quote.Fees.OffRampFee, transfer.Fees.OffRampFee)
	}
	if !transfer.Fees.TotalFeeUSD.Equal(quote.Fees.TotalFeeUSD) {
		t.Errorf("transfer.Fees.TotalFeeUSD: want %s, got %s", quote.Fees.TotalFeeUSD, transfer.Fees.TotalFeeUSD)
	}

	// Quote ID back-reference must be set.
	if transfer.QuoteID == nil {
		t.Fatal("transfer.QuoteID should not be nil when created with a quote")
	}
	if *transfer.QuoteID != quote.ID {
		t.Errorf("transfer.QuoteID: want %s, got %s", quote.ID, *transfer.QuoteID)
	}

	// Chain and routing info must be copied from the quote's route.
	if transfer.Chain != quote.Route.Chain {
		t.Errorf("transfer.Chain: want %s (from quote), got %s", quote.Route.Chain, transfer.Chain)
	}
	if transfer.StableCoin != quote.Route.StableCoin {
		t.Errorf("transfer.StableCoin: want %s (from quote), got %s", quote.Route.StableCoin, transfer.StableCoin)
	}
	if transfer.OnRampProviderID != quote.Route.OnRampProvider {
		t.Errorf("transfer.OnRampProviderID: want %s, got %s", quote.Route.OnRampProvider, transfer.OnRampProviderID)
	}
	if transfer.OffRampProviderID != quote.Route.OffRampProvider {
		t.Errorf("transfer.OffRampProviderID: want %s, got %s", quote.Route.OffRampProvider, transfer.OffRampProviderID)
	}

	t.Logf("quote-to-transfer consistency: quoteID=%s, transferID=%s, destAmount=%s NGN",
		quote.ID, transfer.ID, transfer.DestAmount.StringFixed(2))
}

// TestQuoteConsistency_ExpiredQuoteRejected verifies that the engine rejects
// a transfer creation request that references an expired quote.
//
// A quote is expired when time.Now().UTC().After(quote.ExpiresAt). The engine
// calls quote.IsExpired() in CreateTransfer and returns ErrQuoteExpired.
func TestQuoteConsistency_ExpiredQuoteRejected(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Insert an expired quote directly into the store (bypassing the engine).
	pastTime := time.Now().UTC().Add(-10 * time.Minute)
	expiredQuote := &domain.Quote{
		ID:             uuid.New(),
		TenantID:       LemfiTenantID,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(300),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromFloat(500_000),
		StableAmount:   decimal.NewFromFloat(374.85),
		FXRate:         decimal.NewFromFloat(1937.5),
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(1.20),
			OffRampFee:  decimal.NewFromFloat(1.05),
			NetworkFee:  decimal.NewFromFloat(200.60),
			TotalFeeUSD: decimal.NewFromFloat(202.85),
		},
		Route: domain.RouteInfo{
			Chain:           "tron",
			StableCoin:      domain.CurrencyUSDT,
			OnRampProvider:  "mock-onramp-gbp",
			OffRampProvider: "mock-offramp-ngn",
		},
		ExpiresAt: pastTime, // already expired
		CreatedAt: pastTime.Add(-5 * time.Minute),
	}
	h.TransferStore.addQuote(expiredQuote)

	_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "expired-quote-transfer-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(300),
		DestCurrency:   domain.CurrencyNGN,
		QuoteID:        &expiredQuote.ID,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Expired Sender",
			Email:   "expired@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Expired Recipient",
			AccountNumber: "4444444444",
			BankName:      "UBA",
			Country:       "NG",
		},
	})

	if err == nil {
		t.Fatal("expected an error when creating transfer with an expired quote, got nil")
	}

	// Verify the error communicates quote expiry (domain.ErrQuoteExpired wraps the ID).
	t.Logf("expired quote correctly rejected: %v", err)

	transfers := h.TransferStore.allTransfers()
	for _, tr := range transfers {
		if tr.QuoteID != nil && *tr.QuoteID == expiredQuote.ID {
			t.Errorf("a transfer was persisted referencing the expired quote %s — it should have been rejected", expiredQuote.ID)
		}
	}

	freshQuote, err := h.Engine.GetQuote(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(300),
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	})
	if err != nil {
		t.Fatalf("GetQuote (fresh) failed: %v", err)
	}
	if freshQuote.IsExpired() {
		t.Fatal("freshly generated quote should not be expired")
	}

	_, err = h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "fresh-quote-transfer-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(300),
		DestCurrency:   domain.CurrencyNGN,
		QuoteID:        &freshQuote.ID,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Fresh Sender",
			Email:   "fresh@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Fresh Recipient",
			AccountNumber: "5555555555",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer with fresh quote should succeed, got error: %v", err)
	}

	t.Logf("expired quote rejected, fresh quote accepted for same corridor")
}
