package domain

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// CurrencyPair represents a source→destination currency combination.
type CurrencyPair struct {
	From Currency
	To   Currency
}

// QuoteRequest is the input for requesting a quote from a provider.
type QuoteRequest struct {
	SourceCurrency Currency
	SourceAmount   decimal.Decimal
	DestCurrency   Currency
	DestCountry    string
}

// ProviderQuote is a quote returned by a provider.
type ProviderQuote struct {
	ProviderID       string
	Rate             decimal.Decimal
	Fee              decimal.Decimal
	EstimatedSeconds int
}

// ProviderTx represents a transaction executed by a provider.
type ProviderTx struct {
	ID         string
	ExternalID string
	Status     string
	Amount     decimal.Decimal
	Currency   Currency
	TxHash     string
	Metadata   map[string]string
}

// OnRampRequest is the input for executing an on-ramp (fiat→stablecoin) transaction.
type OnRampRequest struct {
	Amount       decimal.Decimal
	FromCurrency Currency
	ToCurrency   Currency
	Reference    string
}

// OffRampRequest is the input for executing an off-ramp (stablecoin→fiat) transaction.
type OffRampRequest struct {
	Amount       decimal.Decimal
	FromCurrency Currency
	ToCurrency   Currency
	Recipient    Recipient
	Reference    string
}

// OnRampProvider is a provider that converts fiat to stablecoin.
type OnRampProvider interface {
	// ID returns the unique provider identifier.
	ID() string
	// SupportedPairs returns the currency pairs this provider can handle.
	SupportedPairs() []CurrencyPair
	// GetQuote returns a quote for the given request.
	GetQuote(ctx context.Context, req QuoteRequest) (*ProviderQuote, error)
	// Execute initiates an on-ramp transaction.
	Execute(ctx context.Context, req OnRampRequest) (*ProviderTx, error)
	// GetStatus returns the current status of a transaction.
	GetStatus(ctx context.Context, txID string) (*ProviderTx, error)
}

// OffRampProvider is a provider that converts stablecoin to fiat.
type OffRampProvider interface {
	// ID returns the unique provider identifier.
	ID() string
	// SupportedPairs returns the currency pairs this provider can handle.
	SupportedPairs() []CurrencyPair
	// GetQuote returns a quote for the given request.
	GetQuote(ctx context.Context, req QuoteRequest) (*ProviderQuote, error)
	// Execute initiates an off-ramp transaction.
	Execute(ctx context.Context, req OffRampRequest) (*ProviderTx, error)
	// GetStatus returns the current status of a transaction.
	GetStatus(ctx context.Context, txID string) (*ProviderTx, error)
}

// TxRequest is the input for a blockchain transaction.
type TxRequest struct {
	From   string
	To     string
	Token  string
	Amount decimal.Decimal
	Memo   string
}

// ChainTx represents a blockchain transaction.
type ChainTx struct {
	Hash          string
	Status        string
	Confirmations int
	BlockNumber   uint64
	Fee           decimal.Decimal
}

// BlockchainClient interacts with a specific blockchain for on-chain settlements.
type BlockchainClient interface {
	// Chain returns the blockchain identifier (e.g., "tron", "ethereum").
	Chain() string
	// GetBalance returns the token balance for an address.
	GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error)
	// EstimateGas returns the estimated gas fee for a transaction.
	EstimateGas(ctx context.Context, req TxRequest) (decimal.Decimal, error)
	// SendTransaction submits a transaction to the blockchain.
	SendTransaction(ctx context.Context, req TxRequest) (*ChainTx, error)
	// GetTransaction retrieves a transaction by hash.
	GetTransaction(ctx context.Context, hash string) (*ChainTx, error)
	// SubscribeTransactions subscribes to transaction events for an address.
	SubscribeTransactions(ctx context.Context, address string, ch chan<- ChainTx) error
}

// Router selects the optimal provider and corridor for a settlement.
type Router interface {
	// Route evaluates available providers and selects the optimal route.
	Route(ctx context.Context, req RouteRequest) (*RouteResult, error)
}

// RouteRequest describes what needs to be routed.
type RouteRequest struct {
	TenantID       uuid.UUID
	SourceCurrency Currency
	TargetCurrency Currency
	Amount         decimal.Decimal
}

// RouteResult is the router's decision.
type RouteResult struct {
	ProviderID      string // on-ramp provider ID
	OffRampProvider string // off-ramp provider ID
	BlockchainChain string // blockchain chain (e.g., "tron")
	Corridor        string
	Fee             Money
	Rate            decimal.Decimal
	StableAmount    decimal.Decimal // intermediate stablecoin amount (e.g., USDT on-chain)
	ExplorerURL     string          // block explorer base URL for the chain (testnet)
}
