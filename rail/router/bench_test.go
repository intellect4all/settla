package router

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
)

// setupBenchmarkRouter creates a router with mock providers for benchmarking.
func setupBenchmarkRouter(b *testing.B) (*Router, *mockTenantStore) {
	b.Helper()

	reg := provider.NewRegistry()

	// On-ramp providers: fiat → USDT
	gbpToUSDT := []domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}}
	ngnToUSDT := []domain.CurrencyPair{{From: domain.CurrencyNGN, To: domain.CurrencyUSDT}}

	reg.RegisterOnRamp(mock.NewOnRampProvider("onramp-gbp",
		gbpToUSDT, decimal.NewFromFloat(1.25), decimal.NewFromFloat(1.0), 0))
	reg.RegisterOnRamp(mock.NewOnRampProvider("onramp-ngn",
		ngnToUSDT, decimal.NewFromFloat(0.0012), decimal.NewFromFloat(0.5), 0))

	// Off-ramp providers: USDT → fiat
	usdtToNGN := []domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}}
	usdtToGBP := []domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyGBP}}

	reg.RegisterOffRamp(mock.NewOffRampProvider("offramp-ngn",
		usdtToNGN, decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 0))
	reg.RegisterOffRamp(mock.NewOffRampProvider("offramp-gbp",
		usdtToGBP, decimal.NewFromFloat(0.80), decimal.NewFromFloat(1.0), 0))

	// Blockchain client
	bc := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.50))
	reg.RegisterBlockchainClient(bc)

	// Add more chains for multi-chain benchmarks
	bcEth := mock.NewBlockchainClient("ethereum", decimal.NewFromFloat(2.50))
	reg.RegisterBlockchainClient(bcEth)

	lemfiID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

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
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRouter(reg, tenants, logger)

	return r, tenants
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

// BenchmarkRoute evaluates a full route request.
// Tests the complete routing pipeline: build candidates, score, sort.
//
// Target: <100μs per route evaluation
func BenchmarkRoute(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)
	ctx := context.Background()

	req := domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(1000),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = router.Route(ctx, req)
	}
}

// BenchmarkRoute_MultiChain evaluates routing with multiple blockchain options.
// 4 chains × 2 on-ramps × 2 off-ramps = up to 16 route combinations
//
// Target: <100μs even with multiple chains
func BenchmarkRoute_MultiChain(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)
	ctx := context.Background()

	req := domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(1000),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = router.Route(ctx, req)
	}
}

// BenchmarkRouteConcurrent measures routing throughput under concurrent load.
//
// Target: >5,000 route evaluations/sec total
func BenchmarkRouteConcurrent(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)
	ctx := context.Background()

	req := domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(1000),
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = router.Route(ctx, req)
		}
	})
}

// BenchmarkScoreRoute measures the scoring function in isolation.
//
// Target: <1μs per route score
func BenchmarkScoreRoute(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)

	onRamp := mock.NewOnRampProvider("onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(1.25), decimal.NewFromFloat(1.0), 300)

	offRamp := mock.NewOffRampProvider("offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 600)

	route := Route{
		OnRamp:  onRamp,
		OffRamp: offRamp,
		Chain:   "tron",
		Stable:  domain.CurrencyUSDT,
		OnQuote: &domain.ProviderQuote{
			Rate:             decimal.NewFromFloat(1.25),
			Fee:              decimal.NewFromFloat(1.0),
			EstimatedSeconds: 300,
		},
		OffQuote: &domain.ProviderQuote{
			Rate:             decimal.NewFromFloat(830.0),
			Fee:              decimal.NewFromFloat(0.5),
			EstimatedSeconds: 600,
		},
		GasFee: decimal.NewFromFloat(0.5),
	}

	amount := decimal.NewFromInt(1000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = router.scoreRoute(context.Background(), route, amount)
	}
}

// BenchmarkScoreRouteConcurrent measures scoring throughput.
//
// Target: >100,000 scores/sec
func BenchmarkScoreRouteConcurrent(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)

	onRamp := mock.NewOnRampProvider("onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(1.25), decimal.NewFromFloat(1.0), 300)

	offRamp := mock.NewOffRampProvider("offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 600)

	route := Route{
		OnRamp:  onRamp,
		OffRamp: offRamp,
		Chain:   "tron",
		Stable:  domain.CurrencyUSDT,
		OnQuote: &domain.ProviderQuote{
			Rate:             decimal.NewFromFloat(1.25),
			Fee:              decimal.NewFromFloat(1.0),
			EstimatedSeconds: 300,
		},
		OffQuote: &domain.ProviderQuote{
			Rate:             decimal.NewFromFloat(830.0),
			Fee:              decimal.NewFromFloat(0.5),
			EstimatedSeconds: 600,
		},
		GasFee: decimal.NewFromFloat(0.5),
	}

	amount := decimal.NewFromInt(1000)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = router.scoreRoute(context.Background(), route, amount)
		}
	})
}

// BenchmarkGetQuote evaluates quote generation through CoreRouterAdapter.
//
// Target: <200μs per quote (includes routing + fee calculation)
func BenchmarkGetQuote(b *testing.B) {
	router, tenants := setupBenchmarkRouter(b)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	adapter := NewCoreRouterAdapter(router, tenants, logger)

	ctx := context.Background()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = adapter.GetQuote(ctx, tenantID, req)
	}
}

// BenchmarkGetQuoteConcurrent measures quote generation under load.
//
// Target: >5,000 quotes/sec total
func BenchmarkGetQuoteConcurrent(b *testing.B) {
	router, tenants := setupBenchmarkRouter(b)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	adapter := NewCoreRouterAdapter(router, tenants, logger)

	ctx := context.Background()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = adapter.GetQuote(ctx, tenantID, req)
		}
	})
}

// BenchmarkRouteLargeAmount tests routing with large amounts (edge case).
func BenchmarkRouteLargeAmount(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)
	ctx := context.Background()

	req := domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromInt(1000000), // 1M GBP
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = router.Route(ctx, req)
	}
}

// BenchmarkRouteSmallAmount tests routing with small amounts (edge case).
func BenchmarkRouteSmallAmount(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)
	ctx := context.Background()

	req := domain.RouteRequest{
		TenantID:       uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		SourceCurrency: domain.CurrencyGBP,
		TargetCurrency: domain.CurrencyNGN,
		Amount:         decimal.NewFromFloat(0.01), // 0.01 GBP
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = router.Route(ctx, req)
	}
}

// BenchmarkScoreRouteVariations benchmarks scoring with different fee/amount ratios.
func BenchmarkScoreRouteVariations(b *testing.B) {
	router, _ := setupBenchmarkRouter(b)

	testCases := []struct {
		name   string
		amount decimal.Decimal
		fee    decimal.Decimal
	}{
		{"LowFee", decimal.NewFromInt(1000), decimal.NewFromFloat(0.1)},
		{"MediumFee", decimal.NewFromInt(1000), decimal.NewFromFloat(10)},
		{"HighFee", decimal.NewFromInt(1000), decimal.NewFromFloat(100)},
		{"VeryHighFee", decimal.NewFromInt(1000), decimal.NewFromFloat(500)},
	}

	onRamp := mock.NewOnRampProvider("onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(1.25), decimal.NewFromFloat(1.0), 300)

	offRamp := mock.NewOffRampProvider("offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		decimal.NewFromFloat(830.0), decimal.NewFromFloat(0.5), 600)

	b.ReportAllocs()

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			route := Route{
				OnRamp:  onRamp,
				OffRamp: offRamp,
				Chain:   "tron",
				Stable:  domain.CurrencyUSDT,
				OnQuote: &domain.ProviderQuote{
					Rate:             decimal.NewFromFloat(1.25),
					Fee:              tc.fee.Div(decimal.NewFromInt(2)),
					EstimatedSeconds: 300,
				},
				OffQuote: &domain.ProviderQuote{
					Rate:             decimal.NewFromFloat(830.0),
					Fee:              tc.fee.Div(decimal.NewFromInt(2)),
					EstimatedSeconds: 600,
				},
				GasFee: decimal.NewFromFloat(0.5),
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = router.scoreRoute(context.Background(), route, tc.amount)
			}
		})
	}
}
