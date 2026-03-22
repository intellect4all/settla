package domain

import (
	"context"
	"fmt"
	"strings"

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

// ProviderTxType identifies the direction of a provider transaction.
type ProviderTxType string

const (
	ProviderTxTypeOnRamp  ProviderTxType = "onramp"
	ProviderTxTypeOffRamp ProviderTxType = "offramp"
)

// ProviderTxStatus represents the lifecycle status of a provider transaction.
type ProviderTxStatus string

const (
	ProviderTxStatusPending   ProviderTxStatus = "pending"
	ProviderTxStatusCompleted ProviderTxStatus = "completed"
	ProviderTxStatusConfirmed ProviderTxStatus = "confirmed"
	ProviderTxStatusFailed    ProviderTxStatus = "failed"
)

// IsTerminal returns true if the status is a final state (completed, confirmed, or failed).
func (s ProviderTxStatus) IsTerminal() bool {
	return s == ProviderTxStatusCompleted || s == ProviderTxStatusConfirmed || s == ProviderTxStatusFailed
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
	Amount         decimal.Decimal
	FromCurrency   Currency
	ToCurrency     Currency
	Reference      string
	IdempotencyKey IdempotencyKey // prevents double-execution on retry; scoped per provider+tenant
	// QuotedRate is the FX rate presented to the user at quote time. When set,
	// the provider must reject execution if the live rate has moved more than
	// the configured slippage tolerance (default 2%).
	QuotedRate decimal.Decimal
}

// OffRampRequest is the input for executing an off-ramp (stablecoin→fiat) transaction.
type OffRampRequest struct {
	Amount         decimal.Decimal
	FromCurrency   Currency
	ToCurrency     Currency
	Recipient      Recipient
	Reference      string
	IdempotencyKey IdempotencyKey // prevents double-execution on retry; scoped per provider+tenant
	// QuotedRate is the FX rate presented to the user at quote time. When set,
	// the provider must reject execution if the live rate has moved more than
	// the configured slippage tolerance (default 2%).
	QuotedRate decimal.Decimal
	// SourceTxHash is the blockchain transaction hash from the upstream settlement
	// send. When provided, the off-ramp provider can verify on-chain receipt of
	// the specific transaction instead of relying on balance checks.
	SourceTxHash string
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

// WebhookNormalizer converts a provider-specific raw webhook payload into the
// canonical ProviderWebhookPayload. Each provider has its own payload format
// (e.g., Paystack sends { event: "charge.success", data: { reference: "..." } },
// Flutterwave sends { event: "charge.completed", data: { tx_ref: "..." } }).
//
// Implementing this interface is REQUIRED when registering a provider via the
// factory. Without a normalizer, Settla cannot process the provider's webhooks.
type WebhookNormalizer interface {
	// NormalizeWebhook converts a raw JSON webhook body from this provider into
	// the canonical ProviderWebhookPayload. Returns nil if the payload is not a
	// terminal status update (e.g., "pending" events are skipped).
	// Returns an error if the payload is malformed or missing required fields.
	NormalizeWebhook(providerSlug string, rawBody []byte) (*ProviderWebhookPayload, error)
}

// ProviderListener is an optional interface for providers that communicate
// status updates via non-HTTP channels (e.g., WebSocket, gRPC streaming,
// polling). When implemented, the listener is started alongside the workers
// and publishes normalized status updates to the NATS provider webhook stream.
//
// Providers that use HTTP webhooks do NOT need to implement this — the
// webhook HTTP receiver handles those.
type ProviderListener interface {
	// Listen connects to the provider's async notification channel and calls
	// publish for each status update. Blocks until ctx is cancelled.
	// The publish function handles NATS routing — the listener only needs to
	// produce ProviderWebhookPayload values.
	Listen(ctx context.Context, publish func(ProviderWebhookPayload) error) error
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
	// Chain returns the blockchain identifier (e.g., ChainTron, ChainEthereum).
	Chain() CryptoChain
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

// ProviderRegistry lists available providers and blockchain clients.
type ProviderRegistry interface {
	ListOnRampIDs(ctx context.Context) []string
	ListOffRampIDs(ctx context.Context) []string
	GetOnRamp(id string) (OnRampProvider, error)
	GetOffRamp(id string) (OffRampProvider, error)
	GetBlockchain(chain CryptoChain) (BlockchainClient, error)
	ListBlockchainChains() []CryptoChain
	// StablecoinsFromProviders returns the set of stablecoin currencies that
	// registered providers can handle. Discovered from on-ramp output currencies
	// and off-ramp input currencies that are stablecoins.
	StablecoinsFromProviders(ctx context.Context) []Currency
}

// Corridor represents a source → stablecoin → destination currency path.
type Corridor struct {
	SourceCurrency Currency
	StableCoin     Currency
	DestCurrency   Currency
}

// NewCorridor creates a Corridor value object.
func NewCorridor(source, stable, dest Currency) Corridor {
	return Corridor{SourceCurrency: source, StableCoin: stable, DestCurrency: dest}
}

// String returns the corridor in "GBP→USDT→NGN" format.
func (c Corridor) String() string {
	return fmt.Sprintf("%s→%s→%s", c.SourceCurrency, c.StableCoin, c.DestCurrency)
}

// ParseCorridor parses a corridor string in "SRC→STABLE→DEST" format.
func ParseCorridor(s string) (Corridor, error) {
	parts := strings.Split(s, "→")
	if len(parts) != 3 {
		return Corridor{}, fmt.Errorf("settla-domain: invalid corridor format %q, expected SRC→STABLE→DEST", s)
	}
	return Corridor{
		SourceCurrency: Currency(parts[0]),
		StableCoin:     Currency(parts[1]),
		DestCurrency:   Currency(parts[2]),
	}, nil
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

// ScoreBreakdown contains the individual component scores that make up the
// composite route score. Each component is normalized to [0, 1].
type ScoreBreakdown struct {
	Cost        decimal.Decimal `json:"cost"`
	Speed       decimal.Decimal `json:"speed"`
	Liquidity   decimal.Decimal `json:"liquidity"`
	Reliability decimal.Decimal `json:"reliability"`
}

// RouteAlternative represents a fallback route that can be tried if the primary
// route's provider fails. Alternatives travel in the outbox payload so the
// worker can retry without round-tripping back through the engine.
type RouteAlternative struct {
	OnRampProvider  string          `json:"on_ramp_provider"`
	OffRampProvider string          `json:"off_ramp_provider"`
	Chain           CryptoChain     `json:"chain"`
	StableCoin      Currency        `json:"stablecoin"`
	Fee             Money           `json:"fee"`
	Rate            decimal.Decimal `json:"rate"`
	StableAmount    decimal.Decimal `json:"stable_amount"`
	Score           decimal.Decimal `json:"score"`
	ScoreBreakdown  ScoreBreakdown  `json:"score_breakdown"`
}

// RouteResult is the router's decision.
type RouteResult struct {
	ProviderID       string // on-ramp provider ID
	OffRampProvider  string // off-ramp provider ID
	BlockchainChain  CryptoChain // blockchain chain (e.g., ChainTron)
	Corridor         string
	Fee              Money
	Rate             decimal.Decimal
	StableAmount     decimal.Decimal    // intermediate stablecoin amount (e.g., USDT on-chain)
	ExplorerURL      string             // block explorer base URL for the chain (testnet)
	EstimatedSeconds int                // total estimated settlement time (on-ramp + off-ramp)
	Score            decimal.Decimal    // composite score of the primary route
	ScoreBreakdown   ScoreBreakdown     // individual score components of the primary route
	Alternatives     []RouteAlternative // fallback routes, ordered by score descending
}
