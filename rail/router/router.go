package router

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/blockchain"
)

// Scoring weights for route selection.
var (
	weightCost        = decimal.NewFromFloat(0.40)
	weightSpeed       = decimal.NewFromFloat(0.30)
	weightLiquidity   = decimal.NewFromFloat(0.20)
	weightReliability = decimal.NewFromFloat(0.10)
)

// Route represents a candidate settlement path through on-ramp → chain → off-ramp.
type Route struct {
	OnRamp   domain.OnRampProvider
	OffRamp  domain.OffRampProvider
	Chain    string
	Stable   domain.Currency
	Score    decimal.Decimal
	OnQuote  *domain.ProviderQuote
	OffQuote *domain.ProviderQuote
	GasFee   decimal.Decimal
}

// TenantStore resolves tenant fee schedules. The router only needs this
// narrow interface — it doesn't depend on the full core.TenantStore.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}

// ProviderRegistry lists available providers. Matches the methods the router
// actually needs from the provider registry.
type ProviderRegistry interface {
	ListOnRampIDs(ctx context.Context) []string
	ListOffRampIDs(ctx context.Context) []string
	GetOnRamp(id string) (domain.OnRampProvider, error)
	GetOffRamp(id string) (domain.OffRampProvider, error)
	GetBlockchain(chain string) (domain.BlockchainClient, error)
	ListBlockchainChains() []string
}

// Router selects the optimal provider corridor for each settlement.
// It implements domain.Router (Route method).
type Router struct {
	registry ProviderRegistry
	tenants  TenantStore
	logger   *slog.Logger
}

// Compile-time check: Router implements domain.Router.
var _ domain.Router = (*Router)(nil)

// NewRouter creates a smart router.
func NewRouter(
	registry ProviderRegistry,
	tenants TenantStore,
	logger *slog.Logger,
) *Router {
	return &Router{
		registry: registry,
		tenants:  tenants,
		logger:   logger.With("module", "rail.router"),
	}
}

// Route evaluates all possible on-ramp→chain→off-ramp corridors for the given
// request, scores them, and returns the best route.
func (r *Router) Route(ctx context.Context, req domain.RouteRequest) (*domain.RouteResult, error) {
	candidates, err := r.buildCandidates(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("settla-rail: building route candidates: %w", err)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("settla-rail: no routes available for %s→%s", req.SourceCurrency, req.TargetCurrency)
	}

	// Sort by score descending (highest is best).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score.GreaterThan(candidates[j].Score)
	})

	best := candidates[0]
	totalFee := best.OnQuote.Fee.Add(best.GasFee).Add(best.OffQuote.Fee)

	r.logger.Info("settla-rail: route selected",
		"on_ramp", best.OnRamp.ID(),
		"off_ramp", best.OffRamp.ID(),
		"chain", best.Chain,
		"score", best.Score.StringFixed(4),
		"candidates", len(candidates),
	)

	// StableAmount is the intermediate stablecoin amount flowing on-chain.
	// Calculated as: source_amount × on-ramp rate (before off-ramp conversion).
	stableAmount := req.Amount.Mul(best.OnQuote.Rate).Sub(best.OnQuote.Fee).Round(8)
	if !stableAmount.IsPositive() {
		return nil, fmt.Errorf("settla-rail: amount too small: stable amount would be %s after fees", stableAmount)
	}

	// Generate block explorer URL for the selected chain (empty for unknown chains).
	explorerURL := blockchain.ExplorerURL(best.Chain, "")

	return &domain.RouteResult{
		ProviderID:      best.OnRamp.ID(),
		OffRampProvider: best.OffRamp.ID(),
		BlockchainChain: best.Chain,
		Corridor:        fmt.Sprintf("%s→%s→%s", req.SourceCurrency, best.Stable, req.TargetCurrency),
		Fee:             domain.Money{Amount: totalFee, Currency: domain.CurrencyUSD},
		Rate:            best.OnQuote.Rate.Mul(best.OffQuote.Rate),
		StableAmount:    stableAmount,
		ExplorerURL:     explorerURL,
	}, nil
}

// buildCandidates enumerates all on-ramp × chain × off-ramp combinations that
// can serve the request and scores each one.
func (r *Router) buildCandidates(ctx context.Context, req domain.RouteRequest) ([]Route, error) {
	onRampIDs := r.registry.ListOnRampIDs(ctx)
	offRampIDs := r.registry.ListOffRampIDs(ctx)
	chains := r.registry.ListBlockchainChains()

	// Stablecoins we can route through.
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}

	var candidates []Route

	for _, stable := range stables {
		for _, onID := range onRampIDs {
			onRamp, err := r.registry.GetOnRamp(onID)
			if err != nil {
				continue
			}

			// Does the on-ramp support source→stable?
			onQuote, err := onRamp.GetQuote(ctx, domain.QuoteRequest{
				SourceCurrency: req.SourceCurrency,
				SourceAmount:   req.Amount,
				DestCurrency:   stable,
			})
			if err != nil {
				continue
			}

			for _, offID := range offRampIDs {
				offRamp, err := r.registry.GetOffRamp(offID)
				if err != nil {
					continue
				}

				// Does the off-ramp support stable→target?
				offQuote, err := offRamp.GetQuote(ctx, domain.QuoteRequest{
					SourceCurrency: stable,
					SourceAmount:   req.Amount.Mul(onQuote.Rate).Sub(onQuote.Fee),
					DestCurrency:   req.TargetCurrency,
				})
				if err != nil {
					continue
				}

				for _, chain := range chains {
					bc, err := r.registry.GetBlockchain(chain)
					if err != nil {
						continue
					}

					gasFee, err := bc.EstimateGas(ctx, domain.TxRequest{
						Token:  string(stable),
						Amount: req.Amount.Mul(onQuote.Rate),
					})
					if err != nil {
						continue
					}

					route := Route{
						OnRamp:   onRamp,
						OffRamp:  offRamp,
						Chain:    chain,
						Stable:   stable,
						OnQuote:  onQuote,
						OffQuote: offQuote,
						GasFee:   gasFee,
					}
					route.Score = r.scoreRoute(route, req.Amount)
					candidates = append(candidates, route)
				}
			}
		}
	}

	return candidates, nil
}

// scoreRoute computes a weighted score for a candidate route.
// Higher is better. Score components are normalized to [0, 1].
//
// Cost (40%):       Lower total fee → higher score
// Speed (30%):      Lower estimated time → higher score
// Liquidity (20%):  Always 1.0 for mock (real impl checks treasury positions)
// Reliability (10%): Always 1.0 for mock (real impl uses provider success rates)
func (r *Router) scoreRoute(route Route, amount decimal.Decimal) decimal.Decimal {
	totalFee := route.OnQuote.Fee.Add(route.GasFee).Add(route.OffQuote.Fee)

	// Cost score: 1 - (fee / amount), clamped to [0, 1]
	costScore := decimal.NewFromInt(1)
	if amount.IsPositive() {
		feeRatio := totalFee.Div(amount)
		costScore = decimal.NewFromInt(1).Sub(feeRatio)
		if costScore.IsNegative() {
			costScore = decimal.Zero
		}
	}

	// Speed score: 1 - (seconds / 3600), clamped to [0, 1]
	totalSeconds := route.OnQuote.EstimatedSeconds + route.OffQuote.EstimatedSeconds
	speedScore := decimal.NewFromInt(1).Sub(
		decimal.NewFromInt(int64(totalSeconds)).Div(decimal.NewFromInt(3600)),
	)
	if speedScore.IsNegative() {
		speedScore = decimal.Zero
	}

	// Liquidity: 1.0 (mock; real checks treasury)
	liquidityScore := decimal.NewFromInt(1)

	// Reliability: 1.0 (mock; real uses provider metrics)
	reliabilityScore := decimal.NewFromInt(1)

	return costScore.Mul(weightCost).
		Add(speedScore.Mul(weightSpeed)).
		Add(liquidityScore.Mul(weightLiquidity)).
		Add(reliabilityScore.Mul(weightReliability))
}

// CoreRouterAdapter adapts the domain.Router (Route method) to the core.Router
// interface (GetQuote method). It also applies tenant-specific fee schedules.
type CoreRouterAdapter struct {
	router  *Router
	tenants TenantStore
	logger  *slog.Logger
}

// NewCoreRouterAdapter creates an adapter that bridges domain.Router → core.Router.
func NewCoreRouterAdapter(router *Router, tenants TenantStore, logger *slog.Logger) *CoreRouterAdapter {
	return &CoreRouterAdapter{
		router:  router,
		tenants: tenants,
		logger:  logger.With("module", "rail.router.adapter"),
	}
}

// GetQuote implements core.Router. It routes the request, then applies the
// tenant's fee schedule to produce a domain.Quote with tenant-specific fees.
func (a *CoreRouterAdapter) GetQuote(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error) {
	// Load tenant for fee schedule
	tenant, err := a.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-rail: loading tenant %s for quote: %w", tenantID, err)
	}

	// Route
	result, err := a.router.Route(ctx, domain.RouteRequest{
		TenantID:       tenantID,
		SourceCurrency: req.SourceCurrency,
		TargetCurrency: req.DestCurrency,
		Amount:         req.SourceAmount,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-rail: routing for quote: %w", err)
	}

	// Apply tenant fee schedule (fees are in source currency)
	onRampFee := tenant.FeeSchedule.CalculateFee(req.SourceAmount, "onramp")
	offRampFee := tenant.FeeSchedule.CalculateFee(req.SourceAmount, "offramp")
	networkFee := result.Fee.Amount
	totalFee := onRampFee.Add(offRampFee).Add(networkFee)

	// Subtract fees from source, then convert to dest currency
	destAmount := req.SourceAmount.Sub(totalFee).Mul(result.Rate)

	// Parse corridor to extract stablecoin
	stable := domain.CurrencyUSDT // default
	// Corridor format: "GBP→USDT→NGN"
	// Extract middle segment if present
	if len(result.Corridor) > 0 {
		for _, s := range []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC} {
			if containsCurrency(result.Corridor, s) {
				stable = s
				break
			}
		}
	}

	quote := &domain.Quote{
		ID:             uuid.New(),
		TenantID:       tenantID,
		SourceCurrency: req.SourceCurrency,
		SourceAmount:   req.SourceAmount,
		DestCurrency:   req.DestCurrency,
		DestAmount:     destAmount,
		StableAmount:   result.StableAmount,
		FXRate:         result.Rate,
		Fees: domain.FeeBreakdown{
			OnRampFee:   onRampFee,
			NetworkFee:  networkFee,
			OffRampFee:  offRampFee,
			TotalFeeUSD: totalFee,
		},
		Route: domain.RouteInfo{
			Chain:           result.BlockchainChain,
			StableCoin:      stable,
			OnRampProvider:  result.ProviderID,
			OffRampProvider: result.OffRampProvider,
			ExplorerURL:     result.ExplorerURL,
		},
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		CreatedAt: time.Now().UTC(),
	}

	a.logger.Info("settla-rail: quote generated",
		"tenant_id", tenantID,
		"source", fmt.Sprintf("%s %s", req.SourceAmount, req.SourceCurrency),
		"dest", fmt.Sprintf("%s %s", destAmount.StringFixed(2), req.DestCurrency),
		"total_fee", totalFee.StringFixed(2),
	)

	return quote, nil
}

// containsCurrency checks if a corridor string contains the given currency.
func containsCurrency(corridor string, c domain.Currency) bool {
	return len(corridor) >= len(string(c)) && // basic length check
		findSubstring(corridor, string(c))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
