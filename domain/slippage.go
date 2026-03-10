package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// SlippagePolicy defines the maximum allowed FX rate deviation between quote
// time and execution time.
type SlippagePolicy struct {
	MaxSlippage decimal.Decimal
}

// DefaultSlippagePolicy allows up to 2% rate deviation.
var DefaultSlippagePolicy = SlippagePolicy{MaxSlippage: decimal.NewFromFloat(0.02)}

// Check returns an error if the live rate has moved beyond the allowed
// slippage from the quoted rate. It is a no-op when quotedRate is zero
// (no quote was captured).
func (p SlippagePolicy) Check(quotedRate, liveRate decimal.Decimal) error {
	if !quotedRate.IsPositive() {
		return nil
	}
	slippage := liveRate.Sub(quotedRate).Abs().Div(quotedRate)
	if slippage.GreaterThan(p.MaxSlippage) {
		pct := slippage.Mul(decimal.NewFromInt(100)).InexactFloat64()
		maxPct := p.MaxSlippage.Mul(decimal.NewFromInt(100)).InexactFloat64()
		return fmt.Errorf("settla-domain: FX rate moved %.2f%%, exceeds %.0f%% slippage limit", pct, maxPct)
	}
	return nil
}
