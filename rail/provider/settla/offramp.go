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

// offRampSpreadBPS is the provider spread in basis points for off-ramp (0.30%).
const offRampSpreadBPS = 30

// offRampFeeMinUSD is the minimum off-ramp fee in USD.
var offRampFeeMinUSD = decimal.NewFromFloat(0.50)

// offRampTxStatus is the internal lifecycle state of an off-ramp transaction.
type offRampTxStatus string

const (
	offRampStatusPending   offRampTxStatus = "PENDING"
	offRampStatusReceiving offRampTxStatus = "CRYPTO_RECEIVING"
	offRampStatusReceived  offRampTxStatus = "CRYPTO_RECEIVED"
	offRampStatusPaying    offRampTxStatus = "PAYOUT_INITIATED"
	offRampStatusCompleted offRampTxStatus = "COMPLETED"
	offRampStatusFailed    offRampTxStatus = "FAILED"
)

// offRampRecord is the internal record of an off-ramp transaction.
type offRampRecord struct {
	ID             string
	Status         offRampTxStatus
	Chain          string
	DepositAddress string // system wallet address where user sends crypto
	TxHash         string // on-chain confirmation tx hash (if available)
	ExplorerURL    string
	FiatTxID       string // ID in the fiat simulator
	Amount         decimal.Decimal
	FromCurrency   string
	ToCurrency     string
	PayoutRef      string
	FailureReason  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OffRampProvider implements domain.OffRampProvider using simulated crypto
// receipt (reads system wallet address) and simulated fiat payout.
type OffRampProvider struct {
	fxOracle  *FXOracle
	fiatSim   *FiatSimulator
	registry  *blockchain.Registry
	walletMgr *wallet.Manager
	logger    *slog.Logger

	mu  sync.RWMutex
	txs map[string]*offRampRecord
}

// Compile-time interface check.
var _ domain.OffRampProvider = (*OffRampProvider)(nil)

// NewOffRampProvider creates a new Settla off-ramp provider.
func NewOffRampProvider(
	fxOracle *FXOracle,
	fiatSim *FiatSimulator,
	registry *blockchain.Registry,
	walletMgr *wallet.Manager,
	logger *slog.Logger,
) *OffRampProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &OffRampProvider{
		fxOracle:  fxOracle,
		fiatSim:   fiatSim,
		registry:  registry,
		walletMgr: walletMgr,
		logger:    logger,
		txs:       make(map[string]*offRampRecord),
	}
}

// ID returns the unique provider identifier.
func (p *OffRampProvider) ID() string { return "settla-offramp" }

// SupportedPairs returns the stablecoin→fiat pairs this provider handles.
func (p *OffRampProvider) SupportedPairs() []domain.CurrencyPair {
	stables := []domain.Currency{"USDT", "USDC"}
	fiats := []domain.Currency{"GBP", "NGN", "USD", "EUR", "GHS"}

	pairs := make([]domain.CurrencyPair, 0, len(stables)*len(fiats))
	for _, from := range stables {
		for _, to := range fiats {
			pairs = append(pairs, domain.CurrencyPair{From: from, To: to})
		}
	}
	return pairs
}

// GetQuote returns an FX quote for converting stablecoin to fiat.
// The stablecoin is treated as USD-pegged (1:1 with USD) for FX purposes.
func (p *OffRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	if !isSupportedStable(string(req.SourceCurrency)) {
		return nil, fmt.Errorf("settla-offramp: unsupported source currency %q", req.SourceCurrency)
	}
	if !isSupportedFiat(string(req.DestCurrency)) {
		return nil, fmt.Errorf("settla-offramp: unsupported destination currency %q", req.DestCurrency)
	}

	// Stablecoins are USD-pegged: 1 USDT ≈ 1 USD
	rate, err := p.fxOracle.GetRate("USD", string(req.DestCurrency))
	if err != nil {
		return nil, fmt.Errorf("settla-offramp: fx lookup: %w", err)
	}

	// Apply spread: rate *= (1 - spread/10000). Provider earns spread.
	spread := decimal.NewFromInt(offRampSpreadBPS).Div(decimal.NewFromInt(10000))
	adjustedRate := rate.Mul(decimal.NewFromInt(1).Sub(spread))

	// Fee: max(minFee, spread * sourceAmount in USD)
	feeUSD := req.SourceAmount.Mul(spread)
	if feeUSD.LessThan(offRampFeeMinUSD) {
		feeUSD = offRampFeeMinUSD
	}

	// Estimated seconds based on destination currency
	estimated := estimatedPayoutSeconds(string(req.DestCurrency))

	return &domain.ProviderQuote{
		ProviderID:       p.ID(),
		Rate:             adjustedRate,
		Fee:              feeUSD,
		EstimatedSeconds: estimated,
	}, nil
}

// Execute initiates an off-ramp: returns a deposit address for crypto receipt
// and asynchronously runs the simulated receipt + fiat payout flow.
func (p *OffRampProvider) Execute(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	if !isSupportedStable(string(req.FromCurrency)) {
		return nil, fmt.Errorf("settla-offramp: unsupported from currency %q", req.FromCurrency)
	}
	if !isSupportedFiat(string(req.ToCurrency)) {
		return nil, fmt.Errorf("settla-offramp: unsupported to currency %q", req.ToCurrency)
	}
	if req.Amount.IsZero() || req.Amount.IsNegative() {
		return nil, fmt.Errorf("settla-offramp: amount must be positive")
	}

	// Determine chain and get system hot wallet deposit address.
	chain := preferredChainForToken(string(req.FromCurrency))
	depositAddress, err := p.systemWalletAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("settla-offramp: getting deposit address for chain %s: %w", chain, err)
	}

	txID := uuid.New().String()
	now := time.Now().UTC()

	rec := &offRampRecord{
		ID:             txID,
		Status:         offRampStatusPending,
		Chain:          chain,
		DepositAddress: depositAddress,
		Amount:         req.Amount,
		FromCurrency:   string(req.FromCurrency),
		ToCurrency:     string(req.ToCurrency),
		PayoutRef:      req.Reference,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	p.store(rec)

	p.logger.Info("settla-offramp: transaction initiated",
		"tx_id", txID,
		"amount", req.Amount,
		"from", req.FromCurrency,
		"to", req.ToCurrency,
		"chain", chain,
		"deposit_address", depositAddress,
	)

	// Run the async flow: simulate crypto receipt → fiat payout.
	go p.runOffRamp(txID, req)

	// Snapshot under the read lock before returning to avoid a race with
	// the goroutine started above.
	p.mu.RLock()
	snap := p.snapshot(rec)
	p.mu.RUnlock()
	return p.toProviderTx(snap), nil
}

// GetStatus returns the current status of an off-ramp transaction.
func (p *OffRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	p.mu.RLock()
	rec, ok := p.txs[txID]
	if !ok {
		p.mu.RUnlock()
		return nil, fmt.Errorf("settla-offramp: transaction %q not found", txID)
	}
	snap := p.snapshot(rec)
	p.mu.RUnlock()
	return p.toProviderTx(snap), nil
}

// --- async flow ---

func (p *OffRampProvider) runOffRamp(txID string, req domain.OffRampRequest) {
	chain := preferredChainForToken(string(req.FromCurrency))

	// Step 1: Mark as receiving — waiting for on-chain deposit.
	p.updateStatus(txID, offRampStatusReceiving)

	// Step 2: Simulate crypto receipt. In testnet mode we verify the system
	// wallet has non-zero balance; if not accessible we simulate receipt directly.
	txHash, explorerURL := p.verifyCryptoReceipt(req.FromCurrency, chain, req.Amount)

	p.mu.Lock()
	if rec, ok := p.txs[txID]; ok {
		rec.TxHash = txHash
		rec.ExplorerURL = explorerURL
		rec.UpdatedAt = time.Now().UTC()
	}
	p.mu.Unlock()

	p.updateStatus(txID, offRampStatusReceived)

	p.logger.Info("settla-offramp: crypto receipt confirmed",
		"tx_id", txID,
		"tx_hash", txHash,
		"chain", chain,
	)

	// Step 3: Initiate fiat payout.
	p.updateStatus(txID, offRampStatusPaying)

	payoutRef := p.buildPayoutRef(req)
	fiatTx, err := p.fiatSim.SimulatePayout(context.Background(), req.Amount, string(req.ToCurrency), payoutRef)
	if err != nil {
		p.logger.Error("settla-offramp: fiat payout failed",
			"tx_id", txID,
			"error", err,
		)
		p.updateStatusWithReason(txID, offRampStatusFailed, err.Error())
		return
	}

	p.mu.Lock()
	if rec, ok := p.txs[txID]; ok {
		rec.FiatTxID = fiatTx.ID
		rec.UpdatedAt = time.Now().UTC()
	}
	p.mu.Unlock()

	// Step 4: Poll fiat simulator until terminal state.
	if err := p.waitForFiatPayout(fiatTx.ID); err != nil {
		p.updateStatusWithReason(txID, offRampStatusFailed, err.Error())
		return
	}

	p.updateStatus(txID, offRampStatusCompleted)

	p.logger.Info("settla-offramp: transaction completed",
		"tx_id", txID,
		"fiat_tx_id", fiatTx.ID,
		"payout_ref", payoutRef,
	)
}

// verifyCryptoReceipt checks the system wallet balance on-chain. If the check
// fails (testnet RPC unavailable, insufficient balance, etc.), we simulate
// receipt with a fixed hash so the flow can still complete end-to-end.
func (p *OffRampProvider) verifyCryptoReceipt(token domain.Currency, chain string, amount decimal.Decimal) (txHash, explorerURL string) {
	// Simulate receipt delay (1–3s to model on-chain confirmation wait).
	time.Sleep(1500 * time.Millisecond)

	var client domain.BlockchainClient
	var err error
	if p.registry != nil {
		client, err = p.registry.GetClient(chain)
	} else {
		err = fmt.Errorf("no registry configured")
	}
	if err == nil {
		depositAddr, addrErr := p.systemWalletAddress(chain)
		if addrErr == nil {
			tokenContract := tokenContractForChain(string(token), chain)
			balance, balErr := client.GetBalance(context.Background(), depositAddr, tokenContract)
			if balErr == nil && balance.GreaterThanOrEqual(amount) {
				// Real on-chain receipt confirmed — return a synthetic hash to
				// represent the receipt event (no on-chain tx initiated by us here).
				syntheticHash := "offramp-receipt-" + uuid.New().String()
				return syntheticHash, blockchain.ExplorerURL(chain, depositAddr)
			}
		}
	}

	// Fall back: simulate receipt with a deterministic hash.
	simHash := "sim-offramp-" + uuid.New().String()
	return simHash, blockchain.ExplorerURL(chain, simHash)
}

// waitForFiatPayout polls the fiat simulator until the payout reaches a
// terminal state (COMPLETED or FAILED). Returns nil on COMPLETED.
func (p *OffRampProvider) waitForFiatPayout(fiatTxID string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	deadline := time.After(120 * time.Second)

	for {
		select {
		case <-deadline:
			return fmt.Errorf("settla-offramp: fiat payout timed out after 120s")
		case <-ticker.C:
			fiatTx, err := p.fiatSim.GetStatus(fiatTxID)
			if err != nil {
				return fmt.Errorf("settla-offramp: polling fiat status: %w", err)
			}
			switch fiatTx.Status {
			case FiatStatusCompleted:
				return nil
			case FiatStatusFailed:
				return fmt.Errorf("settla-offramp: fiat payout failed: bank rejected")
			}
		}
	}
}

// --- helpers ---

func (p *OffRampProvider) store(rec *offRampRecord) {
	p.mu.Lock()
	p.txs[rec.ID] = rec
	p.mu.Unlock()
}

func (p *OffRampProvider) snapshot(rec *offRampRecord) *offRampRecord {
	cp := *rec
	return &cp
}

func (p *OffRampProvider) updateStatus(txID string, status offRampTxStatus) {
	p.mu.Lock()
	if rec, ok := p.txs[txID]; ok {
		rec.Status = status
		rec.UpdatedAt = time.Now().UTC()
	}
	p.mu.Unlock()
}

func (p *OffRampProvider) updateStatusWithReason(txID string, status offRampTxStatus, reason string) {
	p.mu.Lock()
	if rec, ok := p.txs[txID]; ok {
		rec.Status = status
		rec.FailureReason = reason
		rec.UpdatedAt = time.Now().UTC()
	}
	p.mu.Unlock()
}

func (p *OffRampProvider) systemWalletAddress(chain string) (string, error) {
	if p.walletMgr == nil {
		return "sim-deposit-addr-" + chain, nil
	}
	w, err := p.walletMgr.GetSystemWallet(wallet.Chain(chain))
	if err != nil {
		return "", err
	}
	return w.Address, nil
}

func (p *OffRampProvider) buildPayoutRef(req domain.OffRampRequest) string {
	if req.Reference != "" {
		return req.Reference
	}
	if req.Recipient.AccountNumber != "" {
		return req.Recipient.Name + "/" + req.Recipient.AccountNumber
	}
	return "offramp-" + uuid.New().String()
}

func (p *OffRampProvider) toProviderTx(rec *offRampRecord) *domain.ProviderTx {
	status := string(rec.Status)
	if rec.Status == offRampStatusCompleted {
		status = "COMPLETED"
	}

	metadata := map[string]string{
		"chain":           rec.Chain,
		"deposit_address": rec.DepositAddress,
		"from_currency":   rec.FromCurrency,
		"to_currency":     rec.ToCurrency,
	}
	if rec.TxHash != "" {
		metadata["tx_hash"] = rec.TxHash
		metadata["explorer_url"] = rec.ExplorerURL
	}
	if rec.FiatTxID != "" {
		metadata["fiat_tx_id"] = rec.FiatTxID
	}
	if rec.FailureReason != "" {
		metadata["failure_reason"] = rec.FailureReason
	}

	return &domain.ProviderTx{
		ID:         rec.ID,
		ExternalID: rec.TxHash,
		Status:     status,
		Amount:     rec.Amount,
		Currency:   domain.Currency(rec.FromCurrency),
		TxHash:     rec.TxHash,
		Metadata:   metadata,
	}
}

// --- shared helpers (also used by onramp) ---

// isSupportedStable returns true for USDT and USDC.
func isSupportedStable(currency string) bool {
	return currency == "USDT" || currency == "USDC"
}

// isSupportedFiat returns true for the fiat currencies supported by Settla.
func isSupportedFiat(currency string) bool {
	switch currency {
	case "GBP", "NGN", "USD", "EUR", "GHS":
		return true
	}
	return false
}

// preferredChainForToken returns the preferred chain for a stablecoin.
// USDT defaults to Tron (cheapest fees), USDC defaults to Base Sepolia.
func preferredChainForToken(token string) string {
	switch token {
	case "USDT":
		return "tron"
	case "USDC":
		return "base"
	default:
		return "tron"
	}
}

// tokenContractForChain returns the testnet token contract address for a
// given token on a specific chain.
func tokenContractForChain(token, chain string) string {
	switch chain + ":" + token {
	case "tron:USDT":
		return "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
	case "ethereum:USDC":
		return "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	case "base:USDC":
		return "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	case "solana:USDC":
		return "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	default:
		return ""
	}
}

// estimatedPayoutSeconds returns realistic payout duration estimates per currency.
func estimatedPayoutSeconds(currency string) int {
	switch currency {
	case "NGN":
		return 5  // NIP instant
	case "GBP":
		return 8  // Faster Payments
	case "USD":
		return 20 // ACH
	case "EUR":
		return 8  // SEPA Instant
	case "GHS":
		return 8  // GhIPSS
	default:
		return 15
	}
}
