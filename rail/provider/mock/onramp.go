package mock

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// OnRampProvider is a mock on-ramp provider for testing and demos.
// Simulates fiat → stablecoin conversion with configurable delay and rates.
type OnRampProvider struct {
	id    string
	pairs []domain.CurrencyPair
	rate  decimal.Decimal // FX rate multiplier (e.g., 1.0 for same-currency)
	fee   decimal.Decimal // Fixed fee per transaction
	delay time.Duration   // Simulated processing delay
}

// NewOnRampProvider creates a mock on-ramp provider.
func NewOnRampProvider(id string, pairs []domain.CurrencyPair, rate, fee decimal.Decimal, delay time.Duration) *OnRampProvider {
	return &OnRampProvider{
		id:    id,
		pairs: pairs,
		rate:  rate,
		fee:   fee,
		delay: delay,
	}
}

func (p *OnRampProvider) ID() string                       { return p.id }
func (p *OnRampProvider) SupportedPairs() []domain.CurrencyPair { return p.pairs }

func (p *OnRampProvider) GetQuote(_ context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	if !p.supportsPair(req.SourceCurrency, req.DestCurrency) {
		return nil, fmt.Errorf("settla-rail: mock on-ramp %s: unsupported pair %s→%s", p.id, req.SourceCurrency, req.DestCurrency)
	}
	return &domain.ProviderQuote{
		ProviderID:       p.id,
		Rate:             p.rate,
		Fee:              p.fee,
		EstimatedSeconds: int(p.delay.Seconds()) + 60,
	}, nil
}

func (p *OnRampProvider) Execute(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	if !p.supportsPair(req.FromCurrency, req.ToCurrency) {
		return nil, fmt.Errorf("settla-rail: mock on-ramp %s: unsupported pair %s→%s", p.id, req.FromCurrency, req.ToCurrency)
	}

	// Simulate processing delay.
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return &domain.ProviderTx{
		ID:         uuid.New().String(),
		ExternalID: fmt.Sprintf("mock-onramp-%s", uuid.New().String()[:8]),
		Status:     "COMPLETED",
		Amount:     req.Amount.Mul(p.rate).Sub(p.fee),
		Currency:   req.ToCurrency,
		Metadata:   map[string]string{"provider": p.id, "reference": req.Reference},
	}, nil
}

func (p *OnRampProvider) GetStatus(_ context.Context, txID string) (*domain.ProviderTx, error) {
	return &domain.ProviderTx{
		ID:     txID,
		Status: "COMPLETED",
	}, nil
}

func (p *OnRampProvider) supportsPair(from, to domain.Currency) bool {
	for _, pair := range p.pairs {
		if pair.From == from && pair.To == to {
			return true
		}
	}
	return false
}
