package settla_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	settla "github.com/intellect4all/settla/rail/provider/settla"
)

// newTestOffRampProvider creates an OffRampProvider with fast simulator delays
// and no real blockchain or wallet manager dependencies.
func newTestOffRampProvider(t *testing.T) *settla.OffRampProvider {
	t.Helper()
	cfg := settla.SimulatorConfig{
		FailureRate: 0,
		CurrencyDelays: map[string][2]time.Duration{
			"NGN": {10 * time.Millisecond, 20 * time.Millisecond},
			"GBP": {10 * time.Millisecond, 20 * time.Millisecond},
			"USD": {10 * time.Millisecond, 20 * time.Millisecond},
			"EUR": {10 * time.Millisecond, 20 * time.Millisecond},
			"GHS": {10 * time.Millisecond, 20 * time.Millisecond},
		},
	}
	fiatSim := settla.NewFiatSimulator(cfg)
	fxOracle := settla.NewFXOracle(nil)
	// nil registry and walletMgr — falls back to simulation mode.
	return settla.NewOffRampProvider(fxOracle, fiatSim, nil, nil, nil)
}

func TestOffRampProvider_ID(t *testing.T) {
	p := newTestOffRampProvider(t)
	if got := p.ID(); got != "settla-offramp" {
		t.Errorf("ID() = %q, want %q", got, "settla-offramp")
	}
}

func TestOffRampProvider_SupportedPairs(t *testing.T) {
	p := newTestOffRampProvider(t)
	pairs := p.SupportedPairs()

	if len(pairs) == 0 {
		t.Fatal("SupportedPairs() returned empty slice")
	}

	// Verify all sources are stablecoins and all destinations are fiat.
	validStables := map[string]bool{"USDT": true, "USDC": true}
	validFiats := map[string]bool{"GBP": true, "NGN": true, "USD": true, "EUR": true, "GHS": true}

	for _, pair := range pairs {
		if !validStables[string(pair.From)] {
			t.Errorf("unexpected source currency %q (want stablecoin)", pair.From)
		}
		if !validFiats[string(pair.To)] {
			t.Errorf("unexpected destination currency %q (want fiat)", pair.To)
		}
	}

	// 2 stablecoins × 5 fiats = 10 pairs
	if len(pairs) != 10 {
		t.Errorf("SupportedPairs() len = %d, want 10", len(pairs))
	}
}

func TestOffRampProvider_GetQuote(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	tests := []struct {
		name         string
		from, to     string
		amount       float64
		wantErr      bool
	}{
		{name: "USDT→NGN", from: "USDT", to: "NGN", amount: 100},
		{name: "USDC→GBP", from: "USDC", to: "GBP", amount: 50},
		{name: "USDT→USD", from: "USDT", to: "USD", amount: 200},
		{name: "USDC→EUR", from: "USDC", to: "EUR", amount: 100},
		{name: "USDT→GHS", from: "USDT", to: "GHS", amount: 100},
		{name: "unsupported source", from: "BTC", to: "NGN", amount: 1, wantErr: true},
		{name: "unsupported dest", from: "USDT", to: "BTC", amount: 100, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.QuoteRequest{
				SourceCurrency: domain.Currency(tc.from),
				SourceAmount:   decimal.NewFromFloat(tc.amount),
				DestCurrency:   domain.Currency(tc.to),
			}
			quote, err := p.GetQuote(ctx, req)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetQuote() error = %v", err)
			}
			if quote.ProviderID != "settla-offramp" {
				t.Errorf("ProviderID = %q, want %q", quote.ProviderID, "settla-offramp")
			}
			if quote.Rate.IsZero() || quote.Rate.IsNegative() {
				t.Errorf("Rate should be positive, got %s", quote.Rate)
			}
			if quote.Fee.LessThan(decimal.NewFromFloat(0.50)) {
				t.Errorf("Fee should be ≥ min fee $0.50, got %s", quote.Fee)
			}
			if quote.EstimatedSeconds <= 0 {
				t.Errorf("EstimatedSeconds should be positive, got %d", quote.EstimatedSeconds)
			}
		})
	}
}

func TestOffRampProvider_GetQuote_SpreadApplied(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	// USDT→USD: 1 USDT ≈ 1 USD, but spread should give rate < 1.
	req := domain.QuoteRequest{
		SourceCurrency: "USDT",
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   "USD",
	}
	quote, err := p.GetQuote(ctx, req)
	if err != nil {
		t.Fatalf("GetQuote() error = %v", err)
	}

	// With 30bps spread, rate should be < 1.0 (provider keeps spread).
	one := decimal.NewFromInt(1)
	if quote.Rate.GreaterThanOrEqual(one) {
		t.Errorf("expected rate < 1 after spread, got %s", quote.Rate)
	}
}

func TestOffRampProvider_Execute_InvalidInput(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		from    string
		to      string
		amount  float64
	}{
		{name: "unsupported source", from: "BTC", to: "NGN", amount: 100},
		{name: "unsupported dest", from: "USDT", to: "GBP_INVALID", amount: 100},
		{name: "zero amount", from: "USDT", to: "NGN", amount: 0},
		{name: "negative amount", from: "USDT", to: "NGN", amount: -10},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := domain.OffRampRequest{
				Amount:       decimal.NewFromFloat(tc.amount),
				FromCurrency: domain.Currency(tc.from),
				ToCurrency:   domain.Currency(tc.to),
				Reference:    "test-ref",
			}
			_, err := p.Execute(ctx, req)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestOffRampProvider_Execute_ReturnsDepositAddress(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	req := domain.OffRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: "USDT",
		ToCurrency:   "NGN",
		Reference:    "offramp-test-001",
	}
	tx, err := p.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if tx.ID == "" {
		t.Error("ProviderTx.ID should not be empty")
	}

	addr, ok := tx.Metadata["deposit_address"]
	if !ok || addr == "" {
		t.Error("Metadata should contain non-empty deposit_address")
	}
	chain, ok := tx.Metadata["chain"]
	if !ok || chain == "" {
		t.Error("Metadata should contain chain")
	}
}

func TestOffRampProvider_Execute_CompletesAsync(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	req := domain.OffRampRequest{
		Amount:       decimal.NewFromInt(50),
		FromCurrency: "USDT",
		ToCurrency:   "GBP",
		Reference:    "offramp-gbp-001",
	}
	tx, err := p.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Poll until completed (or timeout).
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("off-ramp did not complete within timeout; last status = %s",
				func() string {
					s, _ := p.GetStatus(ctx, tx.ID)
					if s != nil {
						return s.Status
					}
					return "unknown"
				}())
		case <-ticker.C:
			status, err := p.GetStatus(ctx, tx.ID)
			if err != nil {
				t.Fatalf("GetStatus() error = %v", err)
			}
			if status.Status == "COMPLETED" {
				// Verify metadata includes tx hash and explorer URL.
				if status.Metadata["tx_hash"] == "" {
					t.Error("completed tx should have tx_hash in metadata")
				}
				return
			}
		}
	}
}

func TestOffRampProvider_GetStatus_NotFound(t *testing.T) {
	p := newTestOffRampProvider(t)
	_, err := p.GetStatus(context.Background(), "non-existent-id")
	if err == nil {
		t.Error("expected error for unknown txID, got nil")
	}
}

func TestOffRampProvider_ConcurrentExecute(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	const n = 10
	errs := make(chan error, n)
	ids := make(chan string, n)

	for i := 0; i < n; i++ {
		go func() {
			req := domain.OffRampRequest{
				Amount:       decimal.NewFromInt(25),
				FromCurrency: "USDC",
				ToCurrency:   "USD",
				Reference:    "concurrent-" + uuid.randomString(),
			}
			tx, err := p.Execute(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			ids <- tx.ID
		}()
	}

	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			t.Errorf("concurrent Execute error: %v", err)
		case id := <-ids:
			if id == "" {
				t.Error("got empty transaction ID")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent Execute timed out")
		}
	}
}

func TestOffRampProvider_PreferredChain(t *testing.T) {
	p := newTestOffRampProvider(t)
	ctx := context.Background()

	// USDT should use Tron.
	req := domain.OffRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: "USDT",
		ToCurrency:   "NGN",
	}
	tx, err := p.Execute(ctx, req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if chain := tx.Metadata["chain"]; chain != "tron" {
		t.Errorf("USDT should prefer tron chain, got %q", chain)
	}

	// USDC should use Base.
	req2 := domain.OffRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: "USDC",
		ToCurrency:   "GBP",
	}
	tx2, err := p.Execute(ctx, req2)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if chain := tx2.Metadata["chain"]; chain != "base" {
		t.Errorf("USDC should prefer base chain, got %q", chain)
	}
}

// uuid is a minimal helper so tests don't import the full uuid package.
var uuid = &uuidHelper{}

type uuidHelper struct{}

func (u *uuidHelper) randomString() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
