package domain

import (
	"fmt"
	"os"
	"strconv"

	"github.com/shopspring/decimal"
)

// SlippagePolicy defines the maximum allowed FX rate deviation between quote
// time and execution time.
type SlippagePolicy struct {
	MaxSlippage decimal.Decimal
}

// DefaultSlippagePolicy allows up to 2% rate deviation.
// Override at startup via SETTLA_MAX_SLIPPAGE_PCT env var (e.g., "3" → 0.03).
var DefaultSlippagePolicy = SlippagePolicy{MaxSlippage: decimal.NewFromFloat(0.02)}

func init() {
	if v := os.Getenv("SETTLA_MAX_SLIPPAGE_PCT"); v != "" {
		pct, err := strconv.ParseFloat(v, 64)
		if err == nil && pct > 0 && pct <= 100 {
			DefaultSlippagePolicy.MaxSlippage = decimal.NewFromFloat(pct / 100)
		}
	}
}

// Check returns an error if the live rate has moved beyond the allowed
// slippage from the quoted rate. It is a no-op when quotedRate is zero
// (no quote was captured).
func (p SlippagePolicy) Check(quotedRate, liveRate decimal.Decimal) error {
	if !quotedRate.IsPositive() {
		return nil
	}
	slippage := liveRate.Sub(quotedRate).Abs().Div(quotedRate)
	if slippage.GreaterThan(p.MaxSlippage) {
		pctDec := slippage.Mul(decimal.NewFromInt(100)).Round(2)
		maxPctDec := p.MaxSlippage.Mul(decimal.NewFromInt(100)).Round(0)
		return fmt.Errorf("settla-domain: FX rate moved %s%%, exceeds %s%% slippage limit", pctDec.String(), maxPctDec.String())
	}
	return nil
}
