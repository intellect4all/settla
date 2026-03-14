package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
	"github.com/intellect4all/settla/rail/router"
	"io"
	"log/slog"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockTenantStore implements router.TenantStore for tests.
type mockTenantStore struct {
	tenants map[uuid.UUID]*domain.Tenant
}

func (m *mockTenantStore) GetTenant(_ context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	t, ok := m.tenants[tenantID]
	if !ok {
		return nil, domain.ErrTenantNotFound(tenantID.String())
	}
	return t, nil
}

// GBP corridor pairs.
var (
	gbpToUSDT = []domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}}
	usdtToNGN = []domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}}
	ngnToUSDT = []domain.CurrencyPair{{From: domain.CurrencyNGN, To: domain.CurrencyUSDT}}
	usdtToGBP = []domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyGBP}}
)

func setupTestRouter(t *testing.T) (*router.Router, *provider.Registry, *mockTenantStore) {
	t.Helper()

	reg := provider.NewRegistry()

	// On-ramp providers: fiat → USDT
	reg.RegisterOnRamp(mock.NewOnRampProvider("onramp-gbp",
		gbpToUSDT, decimal.NewFromFloat(1.25), decimal.NewFromFloat(1.0), 0))
	reg.RegisterOnRamp(mock.NewOnRampProvider("onramp-ngn",
		ngnToUSDT, decimal.NewFromFloat(0.0012), decimal.NewFromFloat(0.5), 0))

	// Off-ramp providers: USDT → fiat
	reg.RegisterOffRamp(mock.NewOffRampProvider("offramp-ngn",
		usdtToNGN, decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 0))
	reg.RegisterOffRamp(mock.NewOffRampProvider("offramp-gbp",
		usdtToGBP, decimal.NewFromFloat(0.80), decimal.NewFromFloat(1.0), 0))

	// Blockchain client
	bc := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.50))
	reg.RegisterBlockchainClient(bc)

	lemfiID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	fincraID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	tenants := &mockTenantStore{
		tenants: map[uuid.UUID]*domain.Tenant{
			lemfiID: {
				ID:   lemfiID,
				Slug: "lemfi",
				FeeSchedule: domain.FeeSchedule{
					OnRampBPS:  40,
					OffRampBPS: 35,
					MinFeeUSD:  decimal.NewFromFloat(0.50),
				},
			},
			fincraID: {
				ID:   fincraID,
				Slug: "fincra",
				FeeSchedule: domain.FeeSchedule{
					OnRampBPS:  25,
					OffRampBPS: 20,
					MinFeeUSD:  decimal.NewFromFloat(0.25),
				},
			},
		},
	}

	r := router.NewRouter(reg, tenants, testLogger())
	return r, reg, tenants
}

func TestRouteGBPtoNGN(t *testing.T) {
	r, _, _ := setupTestRouter(t)

	result, err := r.Route(context.Background(), domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.ProviderID != "onramp-gbp" {
		t.Errorf("expected onramp-gbp, got %s", result.ProviderID)
	}
	if result.Rate.IsZero() {
		t.Error("expected non-zero rate")
	}
	if !result.Fee.Amount.IsPositive() {
		t.Error("expected positive fee")
	}
	// Corridor should contain USDT
	if !findSubstring(result.Corridor, "USDT") {
		t.Errorf("expected USDT in corridor, got %s", result.Corridor)
	}
}

func TestRouteNGNtoGBP(t *testing.T) {
	r, _, _ := setupTestRouter(t)

	result, err := r.Route(context.Background(), domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyNGN,
		TargetCurrency: domain.CurrencyGBP,
		Amount:         decimal.NewFromInt(500000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.ProviderID != "onramp-ngn" {
		t.Errorf("expected onramp-ngn, got %s", result.ProviderID)
	}
}

func TestRouteNoProviders(t *testing.T) {
	reg := provider.NewRegistry()
	bc := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.50))
	reg.RegisterBlockchainClient(bc)

	tenants := &mockTenantStore{tenants: map[uuid.UUID]*domain.Tenant{}}

	r := router.NewRouter(reg, tenants, testLogger())

	_, err := r.Route(context.Background(), domain.RouteRequest{
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for no providers, got nil")
	}
}

func TestRoutesSortedByScore(t *testing.T) {
	reg := provider.NewRegistry()

	// Two on-ramp providers with different fees
	reg.RegisterOnRamp(mock.NewOnRampProvider("expensive-onramp",
		gbpToUSDT, decimal.NewFromFloat(1.25), decimal.NewFromFloat(10.0), 0))
	reg.RegisterOnRamp(mock.NewOnRampProvider("cheap-onramp",
		gbpToUSDT, decimal.NewFromFloat(1.25), decimal.NewFromFloat(0.5), 0))

	reg.RegisterOffRamp(mock.NewOffRampProvider("offramp-ngn",
		usdtToNGN, decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 0))

	bc := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.50))
	reg.RegisterBlockchainClient(bc)

	tenants := &mockTenantStore{tenants: map[uuid.UUID]*domain.Tenant{}}

	r := router.NewRouter(reg, tenants, testLogger())

	result, err := r.Route(context.Background(), domain.RouteRequest{
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Cheap provider should win (lower fee → higher cost score)
	if result.ProviderID != "cheap-onramp" {
		t.Errorf("expected cheap-onramp to win, got %s", result.ProviderID)
	}
}

func TestDifferentTenantsGetDifferentFees(t *testing.T) {
	r, _, tenants := setupTestRouter(t)

	lemfiID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	fincraID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	adapter := router.NewCoreRouterAdapter(r, tenants, testLogger())
	amount := decimal.NewFromInt(1000)
	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   amount,
		DestCurrency:   domain.CurrencyNGN,
	}

	lemfiQuote, err := adapter.GetQuote(context.Background(), lemfiID, req)
	if err != nil {
		t.Fatalf("lemfi quote error: %v", err)
	}

	fincraQuote, err := adapter.GetQuote(context.Background(), fincraID, req)
	if err != nil {
		t.Fatalf("fincra quote error: %v", err)
	}

	// Lemfi has higher fees (40/35 bps) vs Fincra (25/20 bps)
	if !lemfiQuote.Fees.OnRampFee.GreaterThan(fincraQuote.Fees.OnRampFee) {
		t.Errorf("expected lemfi on-ramp fee (%s) > fincra (%s)",
			lemfiQuote.Fees.OnRampFee, fincraQuote.Fees.OnRampFee)
	}
	if !lemfiQuote.Fees.OffRampFee.GreaterThan(fincraQuote.Fees.OffRampFee) {
		t.Errorf("expected lemfi off-ramp fee (%s) > fincra (%s)",
			lemfiQuote.Fees.OffRampFee, fincraQuote.Fees.OffRampFee)
	}
	if !lemfiQuote.Fees.TotalFeeUSD.GreaterThan(fincraQuote.Fees.TotalFeeUSD) {
		t.Errorf("expected lemfi total fee (%s) > fincra (%s)",
			lemfiQuote.Fees.TotalFeeUSD, fincraQuote.Fees.TotalFeeUSD)
	}

	// Both quotes should have the same network fee (provider-level, not tenant-specific)
	if !lemfiQuote.Fees.NetworkFee.Equal(fincraQuote.Fees.NetworkFee) {
		t.Errorf("expected same network fee, got lemfi=%s fincra=%s",
			lemfiQuote.Fees.NetworkFee, fincraQuote.Fees.NetworkFee)
	}
}

func TestCoreRouterAdapterQuoteHasRouteInfo(t *testing.T) {
	r, _, tenants := setupTestRouter(t)
	adapter := router.NewCoreRouterAdapter(r, tenants, testLogger())

	lemfiID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	quote, err := adapter.GetQuote(context.Background(), lemfiID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if quote.Route.StableCoin == "" {
		t.Error("expected stablecoin in route")
	}
	if quote.Route.OnRampProvider == "" {
		t.Error("expected on-ramp provider in route")
	}
	if quote.ExpiresAt.Before(time.Now().UTC()) {
		t.Error("expected future expiry")
	}
	if quote.TenantID != lemfiID {
		t.Errorf("expected tenant ID %s, got %s", lemfiID, quote.TenantID)
	}
}

func TestMockOnRampSupportedPairs(t *testing.T) {
	p := mock.NewOnRampProvider("test", gbpToUSDT, decimal.NewFromInt(1), decimal.Zero, 0)

	if p.ID() != "test" {
		t.Errorf("expected ID 'test', got %s", p.ID())
	}
	pairs := p.SupportedPairs()
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
	if pairs[0].From != domain.CurrencyGBP || pairs[0].To != domain.CurrencyUSDT {
		t.Errorf("unexpected pair: %v", pairs[0])
	}
}

func TestMockOnRampUnsupportedPair(t *testing.T) {
	p := mock.NewOnRampProvider("test", gbpToUSDT, decimal.NewFromInt(1), decimal.Zero, 0)

	_, err := p.GetQuote(context.Background(), domain.QuoteRequest{
		SourceCurrency: domain.CurrencyNGN,
		DestCurrency:   domain.CurrencyUSDT,
	})
	if err == nil {
		t.Error("expected error for unsupported pair")
	}
}

func TestMockOffRampExecute(t *testing.T) {
	p := mock.NewOffRampProvider("test", usdtToNGN,
		decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 0)

	tx, err := p.Execute(context.Background(), domain.OffRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyUSDT,
		ToCurrency:   domain.CurrencyNGN,
		Reference:    "ref-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", tx.Status)
	}
	if tx.Currency != domain.CurrencyNGN {
		t.Errorf("expected NGN, got %s", tx.Currency)
	}
	// 100 * 830 - 0.5 = 82999.5
	expected := decimal.NewFromFloat(82999.5)
	if !tx.Amount.Equal(expected) {
		t.Errorf("expected amount %s, got %s", expected, tx.Amount)
	}
}

func TestMockBlockchainClient(t *testing.T) {
	bc := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.50))

	if bc.Chain() != "tron" {
		t.Errorf("expected tron, got %s", bc.Chain())
	}

	// Set and get balance
	bc.SetBalance("addr1", "USDT", decimal.NewFromInt(1000))
	bal, err := bc.GetBalance(context.Background(), "addr1", "USDT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bal.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("expected 1000, got %s", bal)
	}

	// Zero balance for unknown
	bal, err = bc.GetBalance(context.Background(), "unknown", "USDT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bal.IsZero() {
		t.Errorf("expected zero, got %s", bal)
	}

	// EstimateGas
	gas, err := bc.EstimateGas(context.Background(), domain.TxRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gas.Equal(decimal.NewFromFloat(0.50)) {
		t.Errorf("expected 0.50, got %s", gas)
	}

	// SendTransaction
	tx, err := bc.SendTransaction(context.Background(), domain.TxRequest{
		Amount: decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Status != "CONFIRMED" {
		t.Errorf("expected CONFIRMED, got %s", tx.Status)
	}
	if tx.Fee.IsZero() {
		t.Error("expected non-zero fee")
	}
}

func TestRegistryBlockchainClient(t *testing.T) {
	reg := provider.NewRegistry()

	// Not found
	_, err := reg.GetBlockchainClient("tron")
	if err == nil {
		t.Error("expected error for unregistered chain")
	}

	// Register and find
	bc := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.50))
	reg.RegisterBlockchainClient(bc)

	found, err := reg.GetBlockchainClient("tron")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Chain() != "tron" {
		t.Errorf("expected tron, got %s", found.Chain())
	}

	// List chains
	chains := reg.ListBlockchainChains()
	if len(chains) != 1 || chains[0] != "tron" {
		t.Errorf("expected [tron], got %v", chains)
	}
}

func TestMockOnRampContextCancellation(t *testing.T) {
	p := mock.NewOnRampProvider("slow", gbpToUSDT,
		decimal.NewFromInt(1), decimal.Zero, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := p.Execute(ctx, domain.OnRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
	})
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
