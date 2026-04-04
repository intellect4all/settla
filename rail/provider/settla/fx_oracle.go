package settla

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const (
	jitterPct = 0.0015 // ±0.15%

	// rateCacheTTL is how long a jittered rate is reused before recomputing.
	rateCacheTTL = 10 * time.Second

	// staleDuration is the threshold after which base rates are considered stale.
	// A warning is logged if rates haven't been refreshed within this window.
	staleDuration = 1 * time.Hour
)

// cachedRate holds a pre-jittered rate with an expiry timestamp.
type cachedRate struct {
	rate      decimal.Decimal
	expiresAt time.Time
}

// FXOracle provides FX rates with realistic market jitter and TTL-based caching.
// All rates are expressed as units of the quote currency per 1 unit of base currency.
// Base quote currency is USD (e.g. GBP/USD = 1.2645 means 1 GBP = 1.2645 USD).
type FXOracle struct {
	// baseRates maps "CURRENCY" → rate in USD (i.e. how many USD per 1 unit of currency).
	baseRates   map[string]decimal.Decimal
	mu          sync.RWMutex
	lastUpdated time.Time

	// rateCache stores pre-jittered rates with TTL to avoid recomputing on every call.
	rateCache sync.Map // map[string]cachedRate

	logger *slog.Logger
}

// NewFXOracle returns an FXOracle initialised with production-like base rates.
func NewFXOracle(logger *slog.Logger) *FXOracle {
	return &FXOracle{
		baseRates: map[string]decimal.Decimal{
			"USD": decimal.NewFromFloat(1.0),
			"GBP": decimal.NewFromFloat(1.2645),
			"EUR": decimal.NewFromFloat(1.0835),
			"NGN": decimal.NewFromFloat(1.0).Div(decimal.NewFromFloat(1755.20)),
			"GHS": decimal.NewFromFloat(1.0).Div(decimal.NewFromFloat(15.80)),
		},
		lastUpdated: time.Now(),
		logger:      logger,
	}
}

// UpdateRate sets a base rate for a currency. This resets the staleness timer
// and invalidates the cache for all pairs involving this currency.
func (o *FXOracle) UpdateRate(currency string, rateInUSD decimal.Decimal) {
	o.mu.Lock()
	o.baseRates[currency] = rateInUSD
	o.lastUpdated = time.Now()
	o.mu.Unlock()

	// Invalidate cached pairs involving this currency.
	o.rateCache.Range(func(key, _ any) bool {
		k := key.(string)
		if len(k) >= 7 && (k[:3] == currency || k[4:] == currency) {
			o.rateCache.Delete(key)
		}
		return true
	})
}

// GetRate returns the exchange rate from one currency to another with ±0.15% jitter.
// The jittered rate is cached for rateCacheTTL to avoid recomputation at high QPS.
// The returned value is: how many units of `to` you get per 1 unit of `from`.
func (o *FXOracle) GetRate(from, to string) (decimal.Decimal, error) {
	key := from + "/" + to

	// Check cache first.
	if v, ok := o.rateCache.Load(key); ok {
		cached := v.(cachedRate)
		if time.Now().Before(cached.expiresAt) {
			return cached.rate, nil
		}
	}

	o.mu.RLock()
	fromUSD, fromOK := o.baseRates[from]
	toUSD, toOK := o.baseRates[to]
	lastUpdated := o.lastUpdated
	o.mu.RUnlock()

	if !fromOK {
		return decimal.Zero, fmt.Errorf("settla-fx-oracle: unknown currency %q", from)
	}
	if !toOK {
		return decimal.Zero, fmt.Errorf("settla-fx-oracle: unknown currency %q", to)
	}

	// Staleness warning.
	if time.Since(lastUpdated) > staleDuration && o.logger != nil {
		o.logger.Warn("settla-fx-oracle: base rates are stale",
			"last_updated", lastUpdated,
			"stale_for", time.Since(lastUpdated).Round(time.Second),
		)
	}

	// Cross rate: from→USD→to
	rate := fromUSD.Div(toUSD)

	// Apply ±0.15% jitter
	jitter := (rand.Float64()*2 - 1) * jitterPct
	multiplier := decimal.NewFromFloat(1.0 + jitter)
	jitteredRate := rate.Mul(multiplier)

	// Cache the jittered rate.
	o.rateCache.Store(key, cachedRate{
		rate:      jitteredRate,
		expiresAt: time.Now().Add(rateCacheTTL),
	})

	return jitteredRate, nil
}
