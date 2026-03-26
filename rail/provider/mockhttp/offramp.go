package mockhttp

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// OffRampProvider delegates off-ramp operations to the external mock provider HTTP service.
type OffRampProvider struct {
	id     string
	pairs  []domain.CurrencyPair
	client *Client
}

// NewOffRampProvider creates an OffRampProvider that delegates to the mock HTTP service.
func NewOffRampProvider(id string, pairs []domain.CurrencyPair, client *Client) *OffRampProvider {
	return &OffRampProvider{id: id, pairs: pairs, client: client}
}

func (p *OffRampProvider) ID() string                            { return p.id }
func (p *OffRampProvider) SupportedPairs() []domain.CurrencyPair { return p.pairs }

func (p *OffRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	if !p.supportsPair(req.SourceCurrency, req.DestCurrency) {
		return nil, fmt.Errorf("settla-rail: mockhttp off-ramp %s: unsupported pair %s→%s", p.id, req.SourceCurrency, req.DestCurrency)
	}

	body := map[string]string{
		"provider_id":     p.id,
		"source_currency": string(req.SourceCurrency),
		"source_amount":   req.SourceAmount.String(),
		"dest_currency":   string(req.DestCurrency),
	}

	var resp struct {
		ProviderID       string `json:"provider_id"`
		Rate             string `json:"rate"`
		Fee              string `json:"fee"`
		EstimatedSeconds int    `json:"estimated_seconds"`
	}
	if err := p.client.doPost(ctx, "/api/offramp/quote", body, &resp); err != nil {
		return nil, err
	}

	rate, _ := decimal.NewFromString(resp.Rate)
	fee, _ := decimal.NewFromString(resp.Fee)
	return &domain.ProviderQuote{
		ProviderID:       p.id,
		Rate:             rate,
		Fee:              fee,
		EstimatedSeconds: resp.EstimatedSeconds,
	}, nil
}

func (p *OffRampProvider) Execute(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	if !p.supportsPair(req.FromCurrency, req.ToCurrency) {
		return nil, fmt.Errorf("settla-rail: mockhttp off-ramp %s: unsupported pair %s→%s", p.id, req.FromCurrency, req.ToCurrency)
	}

	body := map[string]string{
		"provider_id":     p.id,
		"amount":          req.Amount.String(),
		"from_currency":   string(req.FromCurrency),
		"to_currency":     string(req.ToCurrency),
		"reference":       req.Reference,
		"idempotency_key": string(req.IdempotencyKey),
	}

	var resp struct {
		ID         string            `json:"id"`
		ExternalID string            `json:"external_id"`
		Status     string            `json:"status"`
		Amount     string            `json:"amount"`
		Currency   string            `json:"currency"`
		TxHash     string            `json:"tx_hash"`
		Metadata   map[string]string `json:"metadata"`
	}
	if err := p.client.doPost(ctx, "/api/offramp/execute", body, &resp); err != nil {
		return nil, err
	}

	amount, _ := decimal.NewFromString(resp.Amount)
	return &domain.ProviderTx{
		ID:         resp.ID,
		ExternalID: resp.ExternalID,
		Status:     resp.Status,
		Amount:     amount,
		Currency:   domain.Currency(resp.Currency),
		TxHash:     resp.TxHash,
		Metadata:   resp.Metadata,
	}, nil
}

func (p *OffRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	var resp struct {
		ID         string            `json:"id"`
		ExternalID string            `json:"external_id"`
		Status     string            `json:"status"`
		Amount     string            `json:"amount"`
		Currency   string            `json:"currency"`
		TxHash     string            `json:"tx_hash"`
		Metadata   map[string]string `json:"metadata"`
	}
	path := fmt.Sprintf("/api/status/%s?provider_id=%s", txID, p.id)
	if err := p.client.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}

	amount, _ := decimal.NewFromString(resp.Amount)
	return &domain.ProviderTx{
		ID:         resp.ID,
		ExternalID: resp.ExternalID,
		Status:     resp.Status,
		Amount:     amount,
		Currency:   domain.Currency(resp.Currency),
		TxHash:     resp.TxHash,
		Metadata:   resp.Metadata,
	}, nil
}

func (p *OffRampProvider) supportsPair(from, to domain.Currency) bool {
	for _, pair := range p.pairs {
		if pair.From == from && pair.To == to {
			return true
		}
	}
	return false
}
