package settla

import (
	"fmt"
	"math/rand/v2"
	"sync"

	"github.com/shopspring/decimal"
)

const jitterPct = 0.0015 // ±0.15%

// FXOracle provides FX rates with realistic market jitter.
// All rates are expressed as units of the quote currency per 1 unit of base currency.
// Base quote currency is USD (e.g. GBP/USD = 1.2645 means 1 GBP = 1.2645 USD).
type FXOracle struct {
	// baseRates maps "CURRENCY" → rate in USD (i.e. how many USD per 1 unit of currency).
	baseRates map[string]decimal.Decimal
	mu        sync.RWMutex
}

// NewFXOracle returns an FXOracle initialised with production-like base rates.
func NewFXOracle() *FXOracle {
	return &FXOracle{
		baseRates: map[string]decimal.Decimal{
			"USD": decimal.NewFromFloat(1.0),
			"GBP": decimal.NewFromFloat(1.2645),
			"EUR": decimal.NewFromFloat(1.0835),
			"NGN": decimal.NewFromFloat(1.0).Div(decimal.NewFromFloat(1755.20)),
			"GHS": decimal.NewFromFloat(1.0).Div(decimal.NewFromFloat(15.80)),
		},
	}
}

// GetRate returns the exchange rate from one currency to another with ±0.15% jitter.
// The returned value is: how many units of `to` you get per 1 unit of `from`.
func (o *FXOracle) GetRate(from, to string) (decimal.Decimal, error) {
	o.mu.RLock()
	fromUSD, fromOK := o.baseRates[from]
	toUSD, toOK := o.baseRates[to]
	o.mu.RUnlock()

	if !fromOK {
		return decimal.Zero, fmt.Errorf("settla-fx-oracle: unknown currency %q", from)
	}
	if !toOK {
		return decimal.Zero, fmt.Errorf("settla-fx-oracle: unknown currency %q", to)
	}

	// Cross rate: from→USD→to
	// fromUSD = USD per 1 from; toUSD = USD per 1 to
	// rate = fromUSD / toUSD (units of `to` per 1 `from`)
	rate := fromUSD.Div(toUSD)

	// Apply ±0.15% jitter
	jitter := (rand.Float64()*2 - 1) * jitterPct // [-0.0015, +0.0015]
	multiplier := decimal.NewFromFloat(1.0 + jitter)
	return rate.Mul(multiplier), nil
}
