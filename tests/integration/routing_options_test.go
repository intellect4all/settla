//go:build integration

package integration

// TEST: Routing options API — verifies that Engine.GetRoutingOptions returns
// ranked provider routes with score breakdowns for a corridor/amount without
// creating a transfer or persisting a quote.
//
// Uses the same mock providers as the test harness:
//   on-ramp  GBP→USDT : rate=1.25,  fixed fee=0.50
//   off-ramp USDT→NGN : rate=1550,  fixed fee=200
//   blockchain tron   : gas fee≈0.10

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

func TestRoutingOptions_ReturnsRankedRoutes(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sourceAmount := decimal.NewFromInt(1000)

	result, err := h.Engine.GetRoutingOptions(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   sourceAmount,
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("GetRoutingOptions failed: %v", err)
	}

	// ── Primary route must be populated ─────────────────────────────────────
	if result.ProviderID == "" {
		t.Error("result.ProviderID should be set")
	}
	if result.OffRampProvider == "" {
		t.Error("result.OffRampProvider should be set")
	}
	if result.BlockchainChain == "" {
		t.Error("result.BlockchainChain should be set")
	}
	if result.Corridor == "" {
		t.Error("result.Corridor should be set")
	}
	if !result.Fee.Amount.IsPositive() {
		t.Errorf("result.Fee.Amount should be positive, got %s", result.Fee.Amount)
	}
	if !result.Rate.IsPositive() {
		t.Errorf("result.Rate should be positive, got %s", result.Rate)
	}
	if !result.StableAmount.IsPositive() {
		t.Errorf("result.StableAmount should be positive, got %s", result.StableAmount)
	}
	if result.EstimatedSeconds <= 0 {
		t.Errorf("result.EstimatedSeconds should be positive, got %d", result.EstimatedSeconds)
	}

	// ── Composite score must be populated ───────────────────────────────────
	if result.Score.IsZero() {
		t.Error("result.Score should be non-zero")
	}
	if !result.Score.IsPositive() {
		t.Errorf("result.Score should be positive, got %s", result.Score)
	}
	if result.Score.GreaterThan(decimal.NewFromInt(1)) {
		t.Errorf("result.Score should be <= 1.0, got %s", result.Score)
	}

	t.Logf("primary route: provider=%s, chain=%s, score=%s, fee=%s",
		result.ProviderID, result.BlockchainChain,
		result.Score.StringFixed(4), result.Fee.Amount.StringFixed(2))

	// ── Score breakdown must have all components ────────────────────────────
	bd := result.ScoreBreakdown
	if bd.Cost.IsZero() && bd.Speed.IsZero() && bd.Liquidity.IsZero() && bd.Reliability.IsZero() {
		t.Error("ScoreBreakdown should have at least one non-zero component")
	}

	// Each component should be in [0, 1]
	for name, v := range map[string]decimal.Decimal{
		"Cost": bd.Cost, "Speed": bd.Speed,
		"Liquidity": bd.Liquidity, "Reliability": bd.Reliability,
	} {
		if v.IsNegative() {
			t.Errorf("ScoreBreakdown.%s should be >= 0, got %s", name, v)
		}
		if v.GreaterThan(decimal.NewFromInt(1)) {
			t.Errorf("ScoreBreakdown.%s should be <= 1, got %s", name, v)
		}
	}

	t.Logf("score breakdown: cost=%s speed=%s liquidity=%s reliability=%s",
		bd.Cost.StringFixed(4), bd.Speed.StringFixed(4),
		bd.Liquidity.StringFixed(4), bd.Reliability.StringFixed(4))

	// ── Verify weighted composite matches expected formula ──────────────────
	// score = cost*0.40 + speed*0.30 + liquidity*0.20 + reliability*0.10
	expectedScore := bd.Cost.Mul(decimal.NewFromFloat(0.40)).
		Add(bd.Speed.Mul(decimal.NewFromFloat(0.30))).
		Add(bd.Liquidity.Mul(decimal.NewFromFloat(0.20))).
		Add(bd.Reliability.Mul(decimal.NewFromFloat(0.10)))

	tolerance := decimal.NewFromFloat(0.0001)
	if diff := result.Score.Sub(expectedScore).Abs(); diff.GreaterThan(tolerance) {
		t.Errorf("composite score %s doesn't match weighted sum %s (diff=%s)",
			result.Score.StringFixed(6), expectedScore.StringFixed(6), diff.StringFixed(6))
	}
}

func TestRoutingOptions_AlternativesHaveBreakdowns(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	result, err := h.Engine.GetRoutingOptions(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(5000),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("GetRoutingOptions failed: %v", err)
	}

	// With mock providers there may be alternatives (USDT + USDC × tron).
	// If alternatives exist, verify they have score breakdowns.
	for i, alt := range result.Alternatives {
		if alt.Score.IsZero() {
			t.Errorf("alternative[%d].Score should be non-zero", i)
		}

		abd := alt.ScoreBreakdown
		if abd.Cost.IsZero() && abd.Speed.IsZero() && abd.Liquidity.IsZero() && abd.Reliability.IsZero() {
			t.Errorf("alternative[%d].ScoreBreakdown should have at least one non-zero component", i)
		}

		// Alternative scores should be <= primary score (sorted descending)
		if alt.Score.GreaterThan(result.Score) {
			t.Errorf("alternative[%d].Score (%s) > primary Score (%s) — routes should be sorted descending",
				i, alt.Score.StringFixed(4), result.Score.StringFixed(4))
		}

		t.Logf("alternative[%d]: provider=%s, chain=%s, score=%s, breakdown={cost=%s speed=%s liq=%s rel=%s}",
			i, alt.OnRampProvider, alt.Chain, alt.Score.StringFixed(4),
			abd.Cost.StringFixed(4), abd.Speed.StringFixed(4),
			abd.Liquidity.StringFixed(4), abd.Reliability.StringFixed(4))
	}
}

func TestRoutingOptions_ReadOnly_NoQuotePersisted(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Get routing options
	_, err := h.Engine.GetRoutingOptions(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("GetRoutingOptions failed: %v", err)
	}

	// Verify no outbox entries were created (read-only operation)
	entries := h.TransferStore.drainOutbox()
	if len(entries) != 0 {
		t.Errorf("GetRoutingOptions should not create outbox entries, got %d", len(entries))
	}
}

func TestRoutingOptions_SuspendedTenantRejected(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Use a non-existent tenant
	_, err := h.Engine.GetRoutingOptions(ctx, NetSettlementTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err == nil {
		t.Fatal("expected error for unknown tenant, got nil")
	}
	t.Logf("unknown tenant correctly rejected: %v", err)
}

func TestRoutingOptions_DifferentCorridors(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// GBP→NGN (Lemfi's primary corridor)
	gbpNgn, err := h.Engine.GetRoutingOptions(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("GBP→NGN routing failed: %v", err)
	}

	// NGN→GBP (Fincra's corridor)
	ngnGbp, err := h.Engine.GetRoutingOptions(ctx, FincraTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(500_000),
		DestCurrency:   domain.CurrencyGBP,
	})
	if err != nil {
		t.Fatalf("NGN→GBP routing failed: %v", err)
	}

	// Results should differ (different providers, rates, etc.)
	if gbpNgn.ProviderID == ngnGbp.ProviderID && gbpNgn.Corridor == ngnGbp.Corridor {
		t.Error("different corridors should produce different routing results")
	}

	t.Logf("GBP→NGN: score=%s fee=%s | NGN→GBP: score=%s fee=%s",
		gbpNgn.Score.StringFixed(4), gbpNgn.Fee.Amount.StringFixed(2),
		ngnGbp.Score.StringFixed(4), ngnGbp.Fee.Amount.StringFixed(2))
}
