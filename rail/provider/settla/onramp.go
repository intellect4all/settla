
package settla

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/wallet"
)

// onRampStatus is the internal lifecycle state of an on-ramp transaction.
type onRampStatus string

const (
	onRampStatusPending       onRampStatus = "PENDING"         // fiat collection initiated
	onRampStatusFiatCollected onRampStatus = "FIAT_COLLECTED"  // fiat cleared
	onRampStatusCryptoSent    onRampStatus = "CRYPTO_SENT"     // blockchain tx broadcast
	onRampStatusCompleted     onRampStatus = "COMPLETED"       // confirmed
	onRampStatusFailed        onRampStatus = "FAILED"          // terminal failure
)

// stablecoinChain maps a stablecoin symbol to the chain and contract address used
// for testnet delivery.
type stablecoinChain struct {
	Chain    domain.CryptoChain // e.g. domain.ChainTron, domain.ChainEthereum
	Token    string             // contract address; empty = native
}

// defaultStablecoinChains is the testnet mapping of stablecoin → chain info.
var defaultStablecoinChains = map[string]stablecoinChain{
	"USDT": {
		Chain: domain.ChainTron,
		Token: "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj", // Nile TRC20 USDT
	},
	"USDC": {
		Chain: domain.ChainEthereum,
		Token: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // Sepolia USDC
	},
}

// supportedFiatCurrencies is the set of fiat currencies the on-ramp accepts.
var supportedFiatCurrencies = []domain.Currency{
	domain.CurrencyGBP,
	domain.CurrencyNGN,
	domain.CurrencyUSD,
	domain.CurrencyEUR,
	domain.CurrencyGHS,
}

// supportedStablecoins is the set of stablecoins the on-ramp delivers.
var supportedStablecoins = []domain.Currency{
	domain.CurrencyUSDT,
	domain.CurrencyUSDC,
}

// onRampTx tracks the internal state of a single on-ramp transaction.
type onRampTx struct {
	id           string
	fiatTxID     string
	fiatRef      string
	amount       decimal.Decimal
	fromCurrency string
	toCurrency   string
	chain        domain.CryptoChain
	token        string
	cryptoAmount decimal.Decimal
	fromAddr     string // system wallet address (sender)
	toAddr       string // recipient address
	chainTxHash  string
	explorerURL  string
	status       onRampStatus
	errMsg       string
	createdAt    time.Time
	updatedAt    time.Time
}

// chainRegistryIface abstracts blockchain.Registry for testability.
type chainRegistryIface interface {
	GetClient(chain domain.CryptoChain) (domain.BlockchainClient, error)
}

// walletManagerIface abstracts wallet.Manager for testability.
type walletManagerIface interface {
	GetSystemWallet(chain wallet.Chain) (*wallet.Wallet, error)
}

// OnRampProvider implements domain.OnRampProvider using FiatSimulator for fiat
// collection and real blockchain clients for stablecoin delivery.
//
// All methods are safe for concurrent use.
type OnRampProvider struct {
	fxOracle   *FXOracle
	fiatSim    *FiatSimulator
	chainReg   chainRegistryIface
	walletMgr  walletManagerIface
	spreadBPS  decimal.Decimal // spread in basis points (e.g. 50 = 0.50%)
	logger     *slog.Logger
	ctx        context.Context    // parent context for background goroutines
	cancel     context.CancelFunc // cancels ctx on shutdown

	mu      sync.RWMutex
	txs     map[string]*onRampTx
	txByRef map[string]string // reference → txID for idempotent Execute
}

// Compile-time interface check.
var _ domain.OnRampProvider = (*OnRampProvider)(nil)

// OnRampConfig holds tuneable parameters for the on-ramp provider.
type OnRampConfig struct {
	// SpreadBPS is the provider spread in basis points (default: 50).
	SpreadBPS int

	// Logger is used for structured output. Defaults to slog.Default().
	Logger *slog.Logger
}

// DefaultOnRampConfig returns a config with production-like defaults.
func DefaultOnRampConfig() OnRampConfig {
	return OnRampConfig{SpreadBPS: 50}
}

// NewOnRampProvider creates a new on-ramp provider.
//
//   - fxOracle  — rates with jitter
//   - fiatSim   — simulates banking collection
//   - chainReg  — provides blockchain clients for each chain
//   - walletMgr — provides system hot wallets for signing
//   - cfg       — spread and logger settings
func NewOnRampProvider(
	fxOracle *FXOracle,
	fiatSim *FiatSimulator,
	chainReg chainRegistryIface,
	walletMgr walletManagerIface,
	cfg OnRampConfig,
) *OnRampProvider {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SpreadBPS <= 0 {
		cfg.SpreadBPS = 50
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &OnRampProvider{
		fxOracle:  fxOracle,
		fiatSim:   fiatSim,
		chainReg:  chainReg,
		walletMgr: walletMgr,
		spreadBPS: decimal.NewFromInt(int64(cfg.SpreadBPS)),
		logger:    cfg.Logger,
		ctx:       ctx,
		cancel:    cancel,
		txs:       make(map[string]*onRampTx),
		txByRef:   make(map[string]string),
	}
	go p.cleanupLoop()
	return p
}

// Close cancels the provider's background context and stops cleanup.
// It should be called when the provider is no longer needed.
func (p *OnRampProvider) Close() {
	p.cancel()
}

// cleanupLoop periodically removes completed/failed transactions older than 1 hour.
func (p *OnRampProvider) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.cleanupOldTransactions()
		}
	}
}

// cleanupOldTransactions removes transactions in terminal states (COMPLETED or
// FAILED) that are older than 1 hour.
func (p *OnRampProvider) cleanupOldTransactions() {
	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, tx := range p.txs {
		if (tx.status == onRampStatusCompleted || tx.status == onRampStatusFailed) && tx.updatedAt.Before(cutoff) {
			delete(p.txs, id)
			// Clean up reverse reference mapping.
			for ref, txID := range p.txByRef {
				if txID == id {
					delete(p.txByRef, ref)
					break
				}
			}
		}
	}
}

// ID returns the provider identifier.
func (p *OnRampProvider) ID() string { return "settla-onramp" }

// SupportedPairs returns all fiat→stablecoin pairs this provider supports.
func (p *OnRampProvider) SupportedPairs() []domain.CurrencyPair {
	pairs := make([]domain.CurrencyPair, 0, len(supportedFiatCurrencies)*len(supportedStablecoins))
	for _, fiat := range supportedFiatCurrencies {
		for _, stable := range supportedStablecoins {
			pairs = append(pairs, domain.CurrencyPair{From: fiat, To: stable})
		}
	}
	return pairs
}

// GetQuote returns a quote for converting fiat to stablecoin.
//
// The quote applies a spread on the FX rate and includes an estimated
// processing time based on the source currency's banking rail.
func (p *OnRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	if err := p.validatePair(req.SourceCurrency, req.DestCurrency); err != nil {
		return nil, err
	}
	if req.SourceAmount.IsNegative() || req.SourceAmount.IsZero() {
		return nil, fmt.Errorf("settla-onramp: source amount must be positive")
	}

	// USDT/USDC are pegged to USD; rate is fiat→USD.
	rate, err := p.fxOracle.GetRate(string(req.SourceCurrency), "USD")
	if err != nil {
		return nil, fmt.Errorf("settla-onramp: getting fx rate: %w", err)
	}

	// Apply spread: client-side rate = base rate * (1 - spread/10000)
	// i.e. we give slightly fewer stablecoins per fiat unit.
	hundred := decimal.NewFromInt(10000)
	spreadMultiplier := hundred.Sub(p.spreadBPS).Div(hundred)
	adjustedRate := rate.Mul(spreadMultiplier)

	// Fee in stablecoin = source_amount * spread_bps / 10000
	fee := req.SourceAmount.Mul(rate).Mul(p.spreadBPS).Div(hundred)

	return &domain.ProviderQuote{
		ProviderID:       p.ID(),
		Rate:             adjustedRate,
		Fee:              fee,
		EstimatedSeconds: p.estimatedSeconds(string(req.SourceCurrency)),
	}, nil
}

// Execute initiates an on-ramp: fiat collection (simulated) followed by
// stablecoin delivery (real blockchain) from the system hot wallet.
//
// The returned ProviderTx has status PENDING. Poll GetStatus for updates.
func (p *OnRampProvider) Execute(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	if err := p.validatePair(req.FromCurrency, req.ToCurrency); err != nil {
		return nil, err
	}
	if req.Amount.IsNegative() || req.Amount.IsZero() {
		return nil, fmt.Errorf("settla-onramp: amount must be positive")
	}

	stablecoin, ok := defaultStablecoinChains[string(req.ToCurrency)]
	if !ok {
		return nil, fmt.Errorf("settla-onramp: unsupported stablecoin %s", req.ToCurrency)
	}

	// Idempotency: if we've already processed this reference, return the existing tx.
	p.mu.RLock()
	if existingTxID, ok := p.txByRef[req.Reference]; ok {
		if existingTx, found := p.txs[existingTxID]; found {
			snap := *existingTx
			p.mu.RUnlock()
			return p.toProviderTx(&snap), nil
		}
	}
	p.mu.RUnlock()

	// Compute crypto amount using FX rate with spread applied.
	rate, err := p.fxOracle.GetRate(string(req.FromCurrency), "USD")
	if err != nil {
		return nil, fmt.Errorf("settla-onramp: getting fx rate: %w", err)
	}

	// PROV-8: Reject execution if the live rate has moved more than the allowed slippage.
	if err := domain.DefaultSlippagePolicy.Check(req.QuotedRate, rate); err != nil {
		return nil, fmt.Errorf("settla-onramp: %w", err)
	}

	hundred := decimal.NewFromInt(10000)
	spreadMultiplier := hundred.Sub(p.spreadBPS).Div(hundred)
	cryptoAmount := req.Amount.Mul(rate).Mul(spreadMultiplier).Round(6)

	// Resolve system hot wallet address (used as sender and, for testnet, recipient).
	sysWallet, err := p.walletMgr.GetSystemWallet(stablecoin.Chain)
	if err != nil {
		return nil, fmt.Errorf("settla-onramp: getting system wallet for %s: %w", stablecoin.Chain, err)
	}

	// Start fiat collection simulation.
	fiatTx, err := p.fiatSim.SimulateCollection(ctx, req.Amount, string(req.FromCurrency), req.Reference)
	if err != nil {
		return nil, fmt.Errorf("settla-onramp: starting fiat collection: %w", err)
	}

	txID := uuid.New().String()
	now := time.Now().UTC()
	tx := &onRampTx{
		id:           txID,
		fiatTxID:     fiatTx.ID,
		fiatRef:      fiatTx.BankRef,
		amount:       req.Amount,
		fromCurrency: string(req.FromCurrency),
		toCurrency:   string(req.ToCurrency),
		chain:        stablecoin.Chain,
		token:        stablecoin.Token,
		cryptoAmount: cryptoAmount,
		fromAddr:     wallet.SystemWalletPath("hot", stablecoin.Chain),
		toAddr:       sysWallet.Address,
		status:       onRampStatusPending,
		createdAt:    now,
		updatedAt:    now,
	}

	p.mu.Lock()
	p.txs[txID] = tx
	p.txByRef[req.Reference] = txID
	snap := *tx // snapshot under lock, before background goroutine can modify
	p.mu.Unlock()

	// Drive the on-ramp lifecycle asynchronously.
	go p.runOnRamp(txID)

	p.logger.Info("settla-onramp: execute initiated",
		"tx_id", txID,
		"fiat_tx_id", fiatTx.ID,
		"from", req.FromCurrency,
		"to", req.ToCurrency,
		"amount", req.Amount,
		"crypto_amount", cryptoAmount,
		"chain", stablecoin.Chain,
	)

	return p.toProviderTx(&snap), nil
}

// GetStatus returns the current status of an on-ramp transaction.
func (p *OnRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	p.mu.RLock()
	tx, ok := p.txs[txID]
	if !ok {
		p.mu.RUnlock()
		return nil, fmt.Errorf("settla-onramp: transaction %q not found", txID)
	}
	snap := *tx // snapshot under lock to avoid data race
	p.mu.RUnlock()
	return p.toProviderTx(&snap), nil
}

// --- internal helpers ---

// runOnRamp drives the on-ramp lifecycle in a background goroutine.
func (p *OnRampProvider) runOnRamp(txID string) {
	p.mu.RLock()
	tx := p.txs[txID]
	p.mu.RUnlock()

	fiatTx, err := p.waitForFiatCollection(tx.fiatTxID)
	if err != nil || fiatTx.Status == FiatStatusFailed {
		msg := "fiat collection failed"
		if err != nil {
			msg = err.Error()
		} else if len(fiatTx.History) > 0 {
			msg = "fiat collection failed: " + fiatTx.History[len(fiatTx.History)-1].Reason
		}
		p.setStatus(txID, onRampStatusFailed, msg)
		return
	}

	p.setStatus(txID, onRampStatusFiatCollected, "")

	p.mu.RLock()
	tx = p.txs[txID]
	p.mu.RUnlock()

	client, err := p.chainReg.GetClient(tx.chain)
	if err != nil {
		p.setStatus(txID, onRampStatusFailed, fmt.Sprintf("no blockchain client for %s: %v", tx.chain, err))
		return
	}

	ctx, cancel := context.WithTimeout(p.ctx, 2*time.Minute)
	defer cancel()
	chainTx, err := client.SendTransaction(ctx, domain.TxRequest{
		From:   tx.fromAddr,
		To:     tx.toAddr,
		Token:  tx.token,
		Amount: tx.cryptoAmount,
		Memo:   "settla-onramp:" + txID,
	})
	if err != nil {
		p.setStatus(txID, onRampStatusFailed, fmt.Sprintf("blockchain send failed: %v", err))
		return
	}

	explorerURL := blockchain.ExplorerURL(tx.chain, chainTx.Hash)

	p.mu.Lock()
	tx = p.txs[txID]
	tx.chainTxHash = chainTx.Hash
	tx.explorerURL = explorerURL
	tx.status = onRampStatusCryptoSent
	tx.updatedAt = time.Now().UTC()
	p.mu.Unlock()

	p.logger.Info("settla-onramp: crypto sent",
		"tx_id", txID,
		"chain", tx.chain,
		"tx_hash", chainTx.Hash,
		"explorer_url", explorerURL,
	)

	// Consider the on-ramp complete once the tx is broadcast (confirmations
	// happen asynchronously on-chain).
	p.setStatus(txID, onRampStatusCompleted, "")
}

// waitForFiatCollection polls the fiat simulator until the collection reaches
// a terminal status (COLLECTED or FAILED). Uses exponential backoff via waitWithBackoff.
func (p *OnRampProvider) waitForFiatCollection(fiatTxID string) (*FiatTransaction, error) {
	var result *FiatTransaction
	err := waitWithBackoff(p.ctx, func() (bool, error) {
		fiatTx, err := p.fiatSim.GetStatus(fiatTxID)
		if err != nil {
			return false, fmt.Errorf("settla-onramp: polling fiat status: %w", err)
		}
		switch fiatTx.Status {
		case FiatStatusCollected, FiatStatusFailed:
			result = fiatTx
			return true, nil
		}
		return false, nil
	}, defaultMaxWait)
	if err != nil {
		return nil, fmt.Errorf("settla-onramp: fiat collection timed out: %w", err)
	}
	return result, nil
}

// setStatus updates the tx status under the write lock.
func (p *OnRampProvider) setStatus(txID string, status onRampStatus, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tx, ok := p.txs[txID]
	if !ok {
		return
	}
	tx.status = status
	tx.errMsg = errMsg
	tx.updatedAt = time.Now().UTC()
}

// validatePair returns an error if the from/to currency combination is not
// a supported fiat→stablecoin pair.
func (p *OnRampProvider) validatePair(from, to domain.Currency) error {
	isFiat := false
	for _, c := range supportedFiatCurrencies {
		if c == from {
			isFiat = true
			break
		}
	}
	if !isFiat {
		return fmt.Errorf("settla-onramp: unsupported source currency %s", from)
	}

	isStable := false
	for _, c := range supportedStablecoins {
		if c == to {
			isStable = true
			break
		}
	}
	if !isStable {
		return fmt.Errorf("settla-onramp: unsupported destination currency %s", to)
	}
	return nil
}

// estimatedSeconds returns the approximate processing time (in seconds) for a
// fiat currency's banking rail.
func (p *OnRampProvider) estimatedSeconds(currency string) int {
	bounds, ok := defaultCurrencyDelays[currency]
	if !ok {
		return 30
	}
	avg := (bounds[0] + bounds[1]) / 2
	return int(avg.Seconds())
}

// toProviderTx converts an internal onRampTx to the domain ProviderTx.
// Caller must provide a snapshot (copy) of the tx — no lock is taken here.
func (p *OnRampProvider) toProviderTx(tx *onRampTx) *domain.ProviderTx {
	metadata := map[string]string{
		"fiat_tx_id":   tx.fiatTxID,
		"fiat_ref":     tx.fiatRef,
		"chain":        string(tx.chain),
		"from_address": tx.fromAddr, // system wallet address (sender)
		"to_address":   tx.toAddr,   // recipient address
		"token":        tx.token,
	}
	if tx.explorerURL != "" {
		metadata["explorer_url"] = tx.explorerURL
	}
	if tx.errMsg != "" {
		metadata["error"] = tx.errMsg
	}

	return &domain.ProviderTx{
		ID:         tx.id,
		ExternalID: tx.chainTxHash,
		Status:     string(tx.status),
		Amount:     tx.cryptoAmount,
		Currency:   domain.Currency(tx.toCurrency),
		TxHash:     tx.chainTxHash,
		Metadata:   metadata,
	}
}