package router

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/intellect4all/settla/domain"
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
	OnRamp         domain.OnRampProvider
	OffRamp        domain.OffRampProvider
	Chain          domain.CryptoChain
	Stable         domain.Currency
	Score          decimal.Decimal
	ScoreBreakdown domain.ScoreBreakdown
	OnQuote        *domain.ProviderQuote
	OffQuote       *domain.ProviderQuote
	GasFee         decimal.Decimal
}

// TenantStore resolves tenant fee schedules. The router only needs this
// narrow interface — it doesn't depend on the full core.TenantStore.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}

// LiquidityScorer optionally provides a liquidity score (0–1) for a given
// provider corridor. A score of 1.0 means ample liquidity; 0.0 means dry.
// When nil, the router falls back to 1.0 (fully liquid assumed).
type LiquidityScorer interface {
	LiquidityScore(ctx context.Context, providerID string, currency domain.Currency, amount decimal.Decimal) decimal.Decimal
}

// ReliabilityScorer optionally provides a reliability score (0–1) for a
// provider based on recent success/failure metrics. 1.0 = perfect track
// record; 0.0 = always failing. When nil, it falls back to 1.0.
type ReliabilityScorer interface {
	ReliabilityScore(ctx context.Context, providerID string) decimal.Decimal
}

type ExplorerURLProvider interface {
	ExplorerURL(chain domain.CryptoChain, txHash string) string
}

// RouterOption configures optional Router behaviour.
type RouterOption func(*Router)

// WithLiquidityScorer attaches a real liquidity scorer to the router.
func WithLiquidityScorer(s LiquidityScorer) RouterOption {
	return func(r *Router) { r.liquidityScorer = s }
}

// WithReliabilityScorer attaches a real reliability scorer to the router.
func WithReliabilityScorer(s ReliabilityScorer) RouterOption {
	return func(r *Router) { r.reliabilityScorer = s }
}

func WithExplorerUrl(s ExplorerURLProvider) RouterOption {
	return func(r *Router) { r.explorerUrlProvider = s }
}

// WithProviderRateLimits sets per-provider rate limit overrides. Providers not
// in the map use the default (100 req/s, burst 200).
func WithProviderRateLimits(limits map[string]RateLimitConfig) RouterOption {
	return func(r *Router) { r.providerRateLimits = limits }
}

// Router selects the optimal provider corridor for each settlement.
// It implements domain.Router (Route method).
// RateLimitConfig holds per-provider rate limit settings.
type RateLimitConfig struct {
	PerSec int
	Burst  int
}

type Router struct {
	registry            domain.ProviderRegistry
	tenants             TenantStore
	logger              *slog.Logger
	liquidityScorer     LiquidityScorer   // optional — nil = assume 1.0
	reliabilityScorer   ReliabilityScorer // optional — nil = assume 1.0
	explorerUrlProvider ExplorerURLProvider
	providerRateLimits  map[string]RateLimitConfig // optional per-provider overrides
	providerLimiters    map[string]*rate.Limiter
	providerLimitersMu  sync.Mutex
}

// Compile-time check: Router implements domain.Router.
var _ domain.Router = (*Router)(nil)

// NewRouter creates a smart router.
func NewRouter(
	registry domain.ProviderRegistry,
	tenants TenantStore,
	logger *slog.Logger,
	opts ...RouterOption,
) *Router {
	r := &Router{
		registry: registry,
		tenants:  tenants,
		logger:   logger.With("module", "rail.router"),
	}
	r.providerLimiters = make(map[string]*rate.Limiter)
	for _, opt := range opts {
		opt(r)
	}
	return r
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
	var explorerURL string
	if r.explorerUrlProvider != nil {
		explorerURL = r.explorerUrlProvider.ExplorerURL(best.Chain, "")
	}

	// Build up to 2 fallback alternatives from remaining candidates.
	var alternatives []domain.RouteAlternative
	for i := 1; i < len(candidates) && len(alternatives) < 2; i++ {
		alt := candidates[i]
		altFee := alt.OnQuote.Fee.Add(alt.GasFee).Add(alt.OffQuote.Fee)
		altStable := req.Amount.Mul(alt.OnQuote.Rate).Sub(alt.OnQuote.Fee).Round(8)
		if !altStable.IsPositive() {
			continue
		}
		alternatives = append(alternatives, domain.RouteAlternative{
			OnRampProvider:  alt.OnRamp.ID(),
			OffRampProvider: alt.OffRamp.ID(),
			Chain:           alt.Chain,
			StableCoin:      alt.Stable,
			Fee:             domain.Money{Amount: altFee, Currency: domain.CurrencyUSD},
			Rate:            alt.OnQuote.Rate.Mul(alt.OffQuote.Rate),
			StableAmount:    altStable,
			Score:           alt.Score,
			ScoreBreakdown:  alt.ScoreBreakdown,
		})
	}

	return &domain.RouteResult{
		ProviderID:       best.OnRamp.ID(),
		OffRampProvider:  best.OffRamp.ID(),
		BlockchainChain:  best.Chain,
		Corridor:         domain.NewCorridor(req.SourceCurrency, best.Stable, req.TargetCurrency).String(),
		Fee:              domain.Money{Amount: totalFee, Currency: domain.CurrencyUSD},
		Rate:             best.OnQuote.Rate.Mul(best.OffQuote.Rate),
		StableAmount:     stableAmount,
		ExplorerURL:      explorerURL,
		EstimatedSeconds: best.OnQuote.EstimatedSeconds + best.OffQuote.EstimatedSeconds,
		Score:            best.Score,
		ScoreBreakdown:   best.ScoreBreakdown,
		Alternatives:     alternatives,
	}, nil
}

// buildCandidates enumerates all on-ramp × chain × off-ramp combinations that
// can serve the request and scores each one.
func (r *Router) buildCandidates(ctx context.Context, req domain.RouteRequest) ([]Route, error) {
	onRampIDs := r.registry.ListOnRampIDs(ctx)
	offRampIDs := r.registry.ListOffRampIDs(ctx)
	chains := r.registry.ListBlockchainChains()

	// Discover stablecoins dynamically from what providers actually support,
	// rather than maintaining a hardcoded list.
	stables := r.registry.StablecoinsFromProviders(ctx)
	if len(stables) == 0 {
		return nil, fmt.Errorf("settla-rail: no stablecoins available from registered providers")
	}

	onRampsByStable := make(map[domain.Currency][]domain.OnRampProvider, len(stables))
	for _, onID := range onRampIDs {
		onRamp, err := r.registry.GetOnRamp(onID)
		if err != nil {
			r.logger.Debug("settla-rail: skipping candidate",
				"provider_id", onID, "stage", "on_ramp_lookup", "error", err)
			continue
		}
		if !r.getProviderLimiter(onID).Allow() {
			continue
		}
		for _, pair := range onRamp.SupportedPairs() {
			if pair.From == req.SourceCurrency {
				onRampsByStable[pair.To] = append(onRampsByStable[pair.To], onRamp)
			}
		}
	}

	// offRampsByStable[stable] = off-ramps that support stable → req.TargetCurrency
	offRampsByStable := make(map[domain.Currency][]domain.OffRampProvider, len(stables))
	for _, offID := range offRampIDs {
		offRamp, err := r.registry.GetOffRamp(offID)
		if err != nil {
			r.logger.Debug("settla-rail: skipping candidate",
				"provider_id", offID, "stage", "off_ramp_lookup", "error", err)
			continue
		}
		if !r.getProviderLimiter(offID).Allow() {
			continue
		}
		for _, pair := range offRamp.SupportedPairs() {
			if pair.To == req.TargetCurrency {
				offRampsByStable[pair.From] = append(offRampsByStable[pair.From], offRamp)
			}
		}
	}

	type gasKey struct {
		chain  domain.CryptoChain
		stable domain.Currency
	}
	gasCache := make(map[gasKey]decimal.Decimal, len(chains)*len(stables))

	for _, chain := range chains {
		bc, err := r.registry.GetBlockchain(chain)
		if err != nil {
			r.logger.Debug("settla-rail: skipping candidate",
				"provider_id", chain, "stage", "blockchain_lookup", "error", err)
			continue
		}
		for _, stable := range stables {
			fee, err := bc.EstimateGas(ctx, domain.TxRequest{
				Token:  string(stable),
				Amount: req.Amount,
			})
			if err != nil {
				r.logger.Debug("settla-rail: skipping candidate",
					"provider_id", chain, "stage", "gas_estimate", "error", err)
				continue
			}
			gasCache[gasKey{chain, stable}] = fee
		}
	}

	onQuotes, err := r.fetchOnRampQuotes(ctx, req, stables, onRampsByStable)
	if err != nil {
		return nil, err
	}

	pairs, err := r.fetchOffRampQuotes(ctx, req, onQuotes, offRampsByStable)
	if err != nil {
		return nil, err
	}

	// Assemble candidates using cached gas (no network calls).
	candidates := make([]Route, 0, len(pairs)*len(chains))
	for i := range pairs {
		p := &pairs[i]
		for _, chain := range chains {
			gasFee, ok := gasCache[gasKey{chain, p.stable}]
			if !ok {
				continue
			}
			route := Route{
				OnRamp:   p.onRamp,
				OffRamp:  p.offRamp,
				Chain:    chain,
				Stable:   p.stable,
				OnQuote:  p.onQuote,
				OffQuote: p.offQuote,
				GasFee:   gasFee,
			}
			route.Score, route.ScoreBreakdown = r.scoreRoute(ctx, route, req.Amount)
			candidates = append(candidates, route)
		}
	}

	return candidates, nil
}

// onRampQuote holds a successful on-ramp quote for a specific stablecoin.
type onRampQuote struct {
	onRamp domain.OnRampProvider
	stable domain.Currency
	quote  *domain.ProviderQuote
}

// quotedPair is a fully-quoted on-ramp + off-ramp combination, ready for
// candidate assembly (only the chain and gas fee are missing).
type quotedPair struct {
	onRamp   domain.OnRampProvider
	offRamp  domain.OffRampProvider
	stable   domain.Currency
	onQuote  *domain.ProviderQuote
	offQuote *domain.ProviderQuote
}

const quoteParallelism = 20

// fetchOnRampQuotes fetches quotes from all eligible on-ramp providers concurrently.
func (r *Router) fetchOnRampQuotes(
	ctx context.Context,
	req domain.RouteRequest,
	stables []domain.Currency,
	onRampsByStable map[domain.Currency][]domain.OnRampProvider,
) ([]onRampQuote, error) {
	var (
		mu      sync.Mutex
		results []onRampQuote
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(quoteParallelism)

	for _, stable := range stables {
		for _, onRamp := range onRampsByStable[stable] {
			g.Go(func() error {
				quote, err := onRamp.GetQuote(gctx, domain.QuoteRequest{
					SourceCurrency: req.SourceCurrency,
					SourceAmount:   req.Amount,
					DestCurrency:   stable,
				})
				if err != nil {
					r.logger.Debug("settla-rail: skipping candidate",
						"provider_id", onRamp.ID(), "stage", "on_ramp_quote", "error", err)
					return nil
				}
				mu.Lock()
				results = append(results, onRampQuote{onRamp: onRamp, stable: stable, quote: quote})
				mu.Unlock()
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("settla-rail: fetching on-ramp quotes: %w", err)
	}
	return results, nil
}

// fetchOffRampQuotes pairs each on-ramp quote with every eligible off-ramp,
// fetching off-ramp quotes concurrently.
func (r *Router) fetchOffRampQuotes(
	ctx context.Context,
	req domain.RouteRequest,
	onQuotes []onRampQuote,
	offRampsByStable map[domain.Currency][]domain.OffRampProvider,
) ([]quotedPair, error) {
	var (
		mu      sync.Mutex
		results []quotedPair
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(quoteParallelism)

	for _, oq := range onQuotes {
		stableAmount := req.Amount.Mul(oq.quote.Rate).Sub(oq.quote.Fee)
		for _, offRamp := range offRampsByStable[oq.stable] {
			g.Go(func() error {
				quote, err := offRamp.GetQuote(gctx, domain.QuoteRequest{
					SourceCurrency: oq.stable,
					SourceAmount:   stableAmount,
					DestCurrency:   req.TargetCurrency,
				})
				if err != nil {
					r.logger.Debug("settla-rail: skipping candidate",
						"provider_id", offRamp.ID(), "stage", "off_ramp_quote", "error", err)
					return nil
				}
				mu.Lock()
				results = append(results, quotedPair{
					onRamp:   oq.onRamp,
					offRamp:  offRamp,
					stable:   oq.stable,
					onQuote:  oq.quote,
					offQuote: quote,
				})
				mu.Unlock()
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("settla-rail: fetching off-ramp quotes: %w", err)
	}
	return results, nil
}

// getProviderLimiter returns a per-provider rate limiter, creating one if needed.
// Uses per-provider config if set via WithProviderRateLimits, otherwise defaults
// to 100 req/s with burst 200.
func (r *Router) getProviderLimiter(providerID string) *rate.Limiter {
	r.providerLimitersMu.Lock()
	defer r.providerLimitersMu.Unlock()
	if lim, ok := r.providerLimiters[providerID]; ok {
		return lim
	}
	perSec, burst := 100, 200
	if cfg, ok := r.providerRateLimits[providerID]; ok {
		if cfg.PerSec > 0 {
			perSec = cfg.PerSec
		}
		if cfg.Burst > 0 {
			burst = cfg.Burst
		}
	}
	lim := rate.NewLimiter(rate.Limit(perSec), burst)
	r.providerLimiters[providerID] = lim
	return lim
}

// scoreRoute computes a weighted score for a candidate route.
// Higher is better. Score components are normalized to [0, 1].
//
// Cost (40%): Lower total fee → higher score
// Speed (30%): Lower estimated time → higher score
// Liquidity (20%): From LiquidityScorer if wired, else 1.0
// Reliability (10%): From ReliabilityScorer if wired, else 1.0
func (r *Router) scoreRoute(ctx context.Context, route Route, amount decimal.Decimal) (decimal.Decimal, domain.ScoreBreakdown) {
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

	// Liquidity score: use real scorer when available, else assume 1.0
	liquidityScore := decimal.NewFromInt(1)
	if r.liquidityScorer != nil {
		liquidityScore = r.liquidityScorer.LiquidityScore(ctx, route.OnRamp.ID(), route.Stable, amount)
		if liquidityScore.IsNegative() {
			liquidityScore = decimal.Zero
		} else if liquidityScore.GreaterThan(decimal.NewFromInt(1)) {
			liquidityScore = decimal.NewFromInt(1)
		}
	}

	// Reliability score: use real scorer when available, else assume 1.0
	reliabilityScore := decimal.NewFromInt(1)
	if r.reliabilityScorer != nil {
		reliabilityScore = r.reliabilityScorer.ReliabilityScore(ctx, route.OnRamp.ID())
		if reliabilityScore.IsNegative() {
			reliabilityScore = decimal.Zero
		} else if reliabilityScore.GreaterThan(decimal.NewFromInt(1)) {
			reliabilityScore = decimal.NewFromInt(1)
		}
	}

	breakdown := domain.ScoreBreakdown{
		Cost:        costScore,
		Speed:       speedScore,
		Liquidity:   liquidityScore,
		Reliability: reliabilityScore,
	}

	composite := costScore.Mul(weightCost).
		Add(speedScore.Mul(weightSpeed)).
		Add(liquidityScore.Mul(weightLiquidity)).
		Add(reliabilityScore.Mul(weightReliability))

	return composite, breakdown
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

	// Apply tenant fee schedule
	// On-ramp fee is calculated on the source (fiat) amount.
	onRampFee, err := tenant.FeeSchedule.CalculateFee(req.SourceAmount, "onramp")
	if err != nil {
		return nil, fmt.Errorf("settla-rail: fee calculation for quote: %w", err)
	}
	// Off-ramp fee must be calculated on the intermediate stablecoin amount
	// (after on-ramp conversion), not on the original source amount.
	offRampFee, err := tenant.FeeSchedule.CalculateFee(result.StableAmount, "offramp")
	if err != nil {
		return nil, fmt.Errorf("settla-rail: fee calculation for quote: %w", err)
	}
	networkFee := result.Fee.Amount
	totalFee := onRampFee.Add(offRampFee).Add(networkFee)

	// destAmount = stableAmount × offRampRate
	// The stable amount already accounts for on-ramp fees and conversion. We apply
	// the off-ramp rate to convert stablecoin to destination currency, then subtract
	// the off-ramp fee (in dest currency terms). This avoids mixing currency units
	// (previously subtracted USD fees from fiat source amount).
	destAmount := result.StableAmount.Sub(offRampFee).Mul(result.Rate)
	if !destAmount.IsPositive() {
		// Fallback: use the original formula if the new one produces non-positive
		destAmount = req.SourceAmount.Sub(totalFee).Mul(result.Rate)
	}

	if !destAmount.IsPositive() {
		return nil, fmt.Errorf("settla-rail: amount too small: dest amount %s after fees (onramp=%s, offramp=%s, network=%s) is not positive",
			destAmount.String(), onRampFee.String(), offRampFee.String(), networkFee.String())
	}

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
			Chain:             result.BlockchainChain,
			StableCoin:        stable,
			OnRampProvider:    result.ProviderID,
			OffRampProvider:   result.OffRampProvider,
			ExplorerURL:       result.ExplorerURL,
			AlternativeRoutes: result.Alternatives,
		},
		ExpiresAt: time.Now().UTC().Add(quoteExpiry(result.EstimatedSeconds)),
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

// GetRoutingOptions returns raw route scoring results without creating a quote.
// This is a read-only operation — no state is persisted.
func (a *CoreRouterAdapter) GetRoutingOptions(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.RouteResult, error) {
	result, err := a.router.Route(ctx, domain.RouteRequest{
		TenantID:       tenantID,
		SourceCurrency: req.SourceCurrency,
		TargetCurrency: req.DestCurrency,
		Amount:         req.SourceAmount,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-rail: routing options: %w", err)
	}
	return result, nil
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

// quoteExpiry computes a dynamic quote expiry based on the estimated settlement
// time. Formula: clamp(2×estimate, 2min, 30min). Falls back to 5min when the
// estimate is zero or negative (unknown).
func quoteExpiry(estimatedSeconds int) time.Duration {
	if estimatedSeconds <= 0 {
		return 5 * time.Minute
	}
	d := time.Duration(estimatedSeconds*2) * time.Second
	const minExpiry = 2 * time.Minute
	const maxExpiry = 30 * time.Minute
	if d < minExpiry {
		return minExpiry
	}
	if d > maxExpiry {
		return maxExpiry
	}
	return d
}
