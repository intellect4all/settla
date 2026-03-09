package settla

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/wallet"
)

// --- fakes ---

// fakeBlockchainClient is a minimal mock of domain.BlockchainClient.
type fakeBlockchainClient struct {
	chain   string
	sendErr error

	mu      sync.Mutex
	sentTxs []domain.TxRequest
}

func (f *fakeBlockchainClient) Chain() string { return f.chain }
func (f *fakeBlockchainClient) GetBalance(_ context.Context, _, _ string) (decimal.Decimal, error) {
	return decimal.NewFromInt(1000), nil
}
func (f *fakeBlockchainClient) EstimateGas(_ context.Context, _ domain.TxRequest) (decimal.Decimal, error) {
	return decimal.NewFromFloat(0.1), nil
}
func (f *fakeBlockchainClient) SendTransaction(_ context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.mu.Lock()
	f.sentTxs = append(f.sentTxs, req)
	f.mu.Unlock()
	return &domain.ChainTx{
		Hash:   "fake-tx-hash-" + f.chain,
		Status: "PENDING",
	}, nil
}
func (f *fakeBlockchainClient) GetTransaction(_ context.Context, hash string) (*domain.ChainTx, error) {
	return &domain.ChainTx{Hash: hash, Status: "CONFIRMED"}, nil
}
func (f *fakeBlockchainClient) SubscribeTransactions(_ context.Context, _ string, _ chan<- domain.ChainTx) error {
	return nil
}

// fakeChainRegistry implements chainRegistryIface using a map of fakeBlockchainClients.
type fakeChainRegistry struct {
	clients map[string]*fakeBlockchainClient
}

func newFakeChainRegistry(chains ...string) *fakeChainRegistry {
	r := &fakeChainRegistry{clients: make(map[string]*fakeBlockchainClient)}
	for _, c := range chains {
		r.clients[c] = &fakeBlockchainClient{chain: c}
	}
	return r
}

func (r *fakeChainRegistry) GetClient(chain string) (domain.BlockchainClient, error) {
	c, ok := r.clients[chain]
	if !ok {
		return nil, fmt.Errorf("fakeRegistry: unknown chain %s", chain)
	}
	return c, nil
}

// fakeWalletManager implements walletManagerIface.
type fakeWalletManager struct {
	addresses map[wallet.Chain]string
}

func newFakeWalletManager() *fakeWalletManager {
	return &fakeWalletManager{
		addresses: map[wallet.Chain]string{
			wallet.ChainTron:     "TFakeSystemAddress000000000000000001",
			wallet.ChainEthereum: "0xFakeSystemAddress0000000000000000001",
			wallet.ChainBase:     "0xFakeSystemAddress0000000000000000002",
			wallet.ChainSolana:   "FakeSolanaAddress0000000000000000001",
		},
	}
}

func (m *fakeWalletManager) GetSystemWallet(chain wallet.Chain) (*wallet.Wallet, error) {
	addr, ok := m.addresses[chain]
	if !ok {
		return nil, fmt.Errorf("fakeWalletManager: no wallet for chain %s", chain)
	}
	return &wallet.Wallet{Address: addr, Chain: chain}, nil
}

// --- helpers ---

// newTestOnRampProvider builds a provider wired to fast, deterministic fakes.
func newTestOnRampProvider(failureRate float64) (*OnRampProvider, *fakeChainRegistry) {
	fxOracle := NewFXOracle()
	delays := map[string][2]time.Duration{
		"TST": {10 * time.Millisecond, 20 * time.Millisecond},
		"GBP": {10 * time.Millisecond, 20 * time.Millisecond},
		"NGN": {10 * time.Millisecond, 20 * time.Millisecond},
		"USD": {10 * time.Millisecond, 20 * time.Millisecond},
		"EUR": {10 * time.Millisecond, 20 * time.Millisecond},
		"GHS": {10 * time.Millisecond, 20 * time.Millisecond},
	}
	fiatSim := NewFiatSimulator(SimulatorConfig{FailureRate: failureRate, CurrencyDelays: delays})
	chainReg := newFakeChainRegistry("tron", "ethereum", "base", "solana")
	walletMgr := newFakeWalletManager()

	p := NewOnRampProvider(fxOracle, fiatSim, chainReg, walletMgr, DefaultOnRampConfig())
	return p, chainReg
}

// pollOnRamp waits until the on-ramp tx reaches a terminal status.
func pollOnRamp(t *testing.T, p *OnRampProvider, txID string, timeout time.Duration) *domain.ProviderTx {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tx, err := p.GetStatus(context.Background(), txID)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		switch tx.Status {
		case string(onRampStatusCompleted), string(onRampStatusFailed):
			return tx
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("on-ramp tx %s did not complete within %s", txID, timeout)
	return nil
}

// --- ID / SupportedPairs ---

func TestOnRampProvider_ID(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	if p.ID() != "settla-onramp" {
		t.Errorf("ID: got %q, want %q", p.ID(), "settla-onramp")
	}
}

func TestOnRampProvider_SupportedPairs(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	pairs := p.SupportedPairs()
	want := len(supportedFiatCurrencies) * len(supportedStablecoins)
	if len(pairs) != want {
		t.Errorf("SupportedPairs: got %d, want %d", len(pairs), want)
	}
	// Every pair must have a supported fiat source and stablecoin dest.
	for _, pair := range pairs {
		foundFiat := false
		for _, c := range supportedFiatCurrencies {
			if c == pair.From {
				foundFiat = true
				break
			}
		}
		if !foundFiat {
			t.Errorf("pair %v has unexpected From currency", pair)
		}
	}
}

// --- GetQuote ---

func TestGetQuote_ValidPair(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	quote, err := p.GetQuote(context.Background(), domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(100),
		DestCurrency:   domain.CurrencyUSDT,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote.ProviderID != "settla-onramp" {
		t.Errorf("ProviderID: got %q", quote.ProviderID)
	}
	// Rate for GBP/USD ≈ 1.2645; spread reduces it slightly.
	if quote.Rate.LessThanOrEqual(decimal.Zero) {
		t.Error("rate must be positive")
	}
	if quote.Fee.LessThanOrEqual(decimal.Zero) {
		t.Error("fee must be positive")
	}
	if quote.EstimatedSeconds <= 0 {
		t.Error("estimated seconds must be positive")
	}
}

func TestGetQuote_SpreadApplied(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyUSD,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyUSDT,
	}
	quote, err := p.GetQuote(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// USD→USDT base rate = 1.0; after 50 bps spread → 0.995
	maxRate := decimal.NewFromFloat(1.002) // allow for jitter
	minRate := decimal.NewFromFloat(0.993) // allow for jitter
	if quote.Rate.GreaterThan(maxRate) || quote.Rate.LessThan(minRate) {
		t.Errorf("USD→USDT rate with 50bps spread: got %s, want ≈0.995", quote.Rate)
	}
}

func TestGetQuote_UnsupportedSource(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	_, err := p.GetQuote(context.Background(), domain.QuoteRequest{
		SourceCurrency: domain.CurrencyUSDT,
		SourceAmount:   decimal.NewFromInt(100),
		DestCurrency:   domain.CurrencyUSDC,
	})
	if err == nil {
		t.Error("expected error for stablecoin→stablecoin pair")
	}
}

func TestGetQuote_ZeroAmount(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	_, err := p.GetQuote(context.Background(), domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.Zero,
		DestCurrency:   domain.CurrencyUSDT,
	})
	if err == nil {
		t.Error("expected error for zero amount")
	}
}

// --- Execute ---

func TestExecute_ReturnsPending(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	tx, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "test-ref-001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if tx.Status != string(onRampStatusPending) {
		t.Errorf("initial status: got %q, want %q", tx.Status, onRampStatusPending)
	}
	if tx.ID == "" {
		t.Error("ID must not be empty")
	}
	if tx.Amount.IsZero() {
		t.Error("crypto amount must be set")
	}
	if tx.Currency != domain.CurrencyUSDT {
		t.Errorf("currency: got %q, want USDT", tx.Currency)
	}
}

func TestExecute_InvalidPair(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	_, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyUSDT, // stablecoin, not fiat
		ToCurrency:   domain.CurrencyUSDC,
		Reference:    "bad-ref",
	})
	if err == nil {
		t.Error("expected error for invalid pair")
	}
}

func TestExecute_ZeroAmount(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	_, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.Zero,
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "ref",
	})
	if err == nil {
		t.Error("expected error for zero amount")
	}
}

// --- Full lifecycle ---

func TestExecute_FullLifecycle_Success(t *testing.T) {
	p, chainReg := newTestOnRampProvider(0)
	tx, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(500),
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "lifecycle-ref-001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	final := pollOnRamp(t, p, tx.ID, 3*time.Second)

	if final.Status != string(onRampStatusCompleted) {
		t.Errorf("status: got %q, want COMPLETED", final.Status)
	}
	if final.TxHash == "" {
		t.Error("TxHash must be set on completion")
	}
	if final.Metadata["explorer_url"] == "" {
		t.Error("explorer_url must be set on completion")
	}
	if final.Metadata["chain"] != "tron" {
		t.Errorf("chain: got %q, want tron", final.Metadata["chain"])
	}

	// Verify the blockchain client received a SendTransaction call.
	tronClient := chainReg.clients["tron"]
	if len(tronClient.sentTxs) == 0 {
		t.Error("expected at least one SendTransaction call on tron client")
	}
	sentReq := tronClient.sentTxs[0]
	if sentReq.Amount.IsZero() {
		t.Error("sent amount must be non-zero")
	}
}

func TestExecute_FullLifecycle_USDCOnEthereum(t *testing.T) {
	p, chainReg := newTestOnRampProvider(0)
	tx, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyNGN,
		ToCurrency:   domain.CurrencyUSDC,
		Reference:    "ngn-usdc-ref-001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	final := pollOnRamp(t, p, tx.ID, 3*time.Second)

	if final.Status != string(onRampStatusCompleted) {
		t.Errorf("status: got %q, want COMPLETED", final.Status)
	}
	if final.Metadata["chain"] != "ethereum" {
		t.Errorf("chain: got %q, want ethereum", final.Metadata["chain"])
	}
	ethClient := chainReg.clients["ethereum"]
	if len(ethClient.sentTxs) == 0 {
		t.Error("expected at least one SendTransaction call on ethereum client")
	}
}

func TestExecute_FullLifecycle_FiatFails(t *testing.T) {
	p, _ := newTestOnRampProvider(1) // 100% fiat failure rate
	tx, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "fail-ref-001",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	final := pollOnRamp(t, p, tx.ID, 3*time.Second)

	if final.Status != string(onRampStatusFailed) {
		t.Errorf("status: got %q, want FAILED", final.Status)
	}
}

func TestExecute_FullLifecycle_BlockchainFails(t *testing.T) {
	p, chainReg := newTestOnRampProvider(0)
	// Inject a blockchain send error.
	chainReg.clients["tron"].sendErr = fmt.Errorf("rpc timeout")

	tx, err := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(100),
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "blockchain-fail-ref",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	final := pollOnRamp(t, p, tx.ID, 3*time.Second)

	if final.Status != string(onRampStatusFailed) {
		t.Errorf("status: got %q, want FAILED", final.Status)
	}
	if final.Metadata["error"] == "" {
		t.Error("error message must be set in metadata on failure")
	}
}

// --- GetStatus ---

func TestOnRampGetStatus_UnknownID(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	_, err := p.GetStatus(context.Background(), "nonexistent-id")
	if err == nil {
		t.Error("expected error for unknown transaction ID")
	}
}

func TestGetStatus_MetadataComplete(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	tx, _ := p.Execute(context.Background(), domain.OnRampRequest{
		Amount:       decimal.NewFromInt(200),
		FromCurrency: domain.CurrencyUSD,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "meta-ref-001",
	})

	status, err := p.GetStatus(context.Background(), tx.ID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.Metadata["fiat_tx_id"] == "" {
		t.Error("metadata must contain fiat_tx_id")
	}
	if status.Metadata["chain"] != "tron" {
		t.Errorf("metadata chain: got %q, want tron", status.Metadata["chain"])
	}
}

// --- Concurrency ---

func TestConcurrentOnRamps(t *testing.T) {
	p, _ := newTestOnRampProvider(0)
	ctx := context.Background()
	const n = 10

	ids := make([]string, n)
	currencies := []domain.Currency{domain.CurrencyGBP, domain.CurrencyNGN, domain.CurrencyUSD, domain.CurrencyEUR, domain.CurrencyGHS}
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}

	for i := range n {
		tx, err := p.Execute(ctx, domain.OnRampRequest{
			Amount:       decimal.NewFromInt(int64(100 + i)),
			FromCurrency: currencies[i%len(currencies)],
			ToCurrency:   stables[i%len(stables)],
			Reference:    fmt.Sprintf("concurrent-ref-%d", i),
		})
		if err != nil {
			t.Fatalf("Execute[%d]: %v", i, err)
		}
		ids[i] = tx.ID
	}

	for _, id := range ids {
		final := pollOnRamp(t, p, id, 5*time.Second)
		if final.Status != string(onRampStatusCompleted) {
			t.Errorf("tx %s: got status %q, want COMPLETED", id, final.Status)
		}
	}
}
