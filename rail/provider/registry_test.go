package provider_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider"
)

// ── fakeProvider helpers ──────────────────────────────────────────────────────

type fakeOnRamp struct{ id string }

func (f *fakeOnRamp) ID() string                            { return f.id }
func (f *fakeOnRamp) SupportedPairs() []domain.CurrencyPair { return nil }
func (f *fakeOnRamp) GetQuote(_ context.Context, _ domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (f *fakeOnRamp) Execute(_ context.Context, _ domain.OnRampRequest) (*domain.ProviderTx, error) {
	return nil, nil
}
func (f *fakeOnRamp) GetStatus(_ context.Context, _ string) (*domain.ProviderTx, error) {
	return nil, nil
}

type fakeOffRamp struct{ id string }

func (f *fakeOffRamp) ID() string                            { return f.id }
func (f *fakeOffRamp) SupportedPairs() []domain.CurrencyPair { return nil }
func (f *fakeOffRamp) GetQuote(_ context.Context, _ domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (f *fakeOffRamp) Execute(_ context.Context, _ domain.OffRampRequest) (*domain.ProviderTx, error) {
	return nil, nil
}
func (f *fakeOffRamp) GetStatus(_ context.Context, _ string) (*domain.ProviderTx, error) {
	return nil, nil
}

type fakeBlockchain struct{ chain domain.CryptoChain }

func (f *fakeBlockchain) Chain() domain.CryptoChain { return f.chain }
func (f *fakeBlockchain) GetBalance(_ context.Context, _, _ string) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (f *fakeBlockchain) EstimateGas(_ context.Context, _ domain.TxRequest) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (f *fakeBlockchain) SendTransaction(_ context.Context, _ domain.TxRequest) (*domain.ChainTx, error) {
	return nil, nil
}
func (f *fakeBlockchain) GetTransaction(_ context.Context, _ string) (*domain.ChainTx, error) {
	return nil, nil
}
func (f *fakeBlockchain) SubscribeTransactions(_ context.Context, _ string, _ chan<- domain.ChainTx) error {
	return nil
}

// ── Registry basic tests ─────────────────────────────────────────────────────

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := provider.NewRegistry()

	on := &fakeOnRamp{id: "on-1"}
	off := &fakeOffRamp{id: "off-1"}
	bc := &fakeBlockchain{chain: "tron"}

	reg.RegisterOnRamp(on)
	reg.RegisterOffRamp(off)
	reg.RegisterBlockchainClient(bc)

	got, err := reg.GetOnRamp("on-1")
	if err != nil || got.ID() != "on-1" {
		t.Fatalf("GetOnRamp: got %v, err %v", got, err)
	}

	gotOff, err := reg.GetOffRamp("off-1")
	if err != nil || gotOff.ID() != "off-1" {
		t.Fatalf("GetOffRamp: got %v, err %v", gotOff, err)
	}

	gotBC, err := reg.GetBlockchainClient("tron")
	if err != nil || gotBC.Chain() != "tron" {
		t.Fatalf("GetBlockchainClient: got %v, err %v", gotBC, err)
	}
}

func TestRegistry_NotFound(t *testing.T) {
	reg := provider.NewRegistry()

	if _, err := reg.GetOnRamp("nope"); err == nil {
		t.Error("expected error for missing on-ramp")
	}
	if _, err := reg.GetOffRamp("nope"); err == nil {
		t.Error("expected error for missing off-ramp")
	}
	if _, err := reg.GetBlockchainClient("nope"); err == nil {
		t.Error("expected error for missing blockchain client")
	}
}

func TestRegistry_ListIDs(t *testing.T) {
	reg := provider.NewRegistry()
	reg.RegisterOnRamp(&fakeOnRamp{id: "a"})
	reg.RegisterOnRamp(&fakeOnRamp{id: "b"})

	ids := reg.ListOnRampIDs(context.Background())
	if len(ids) != 2 {
		t.Errorf("ListOnRampIDs: got %d, want 2", len(ids))
	}
}

// ── ProviderMode tests ──────────────────────────────────────────────────────

func TestProviderModeFromEnv_Defaults(t *testing.T) {
	// Clear env
	t.Setenv("SETTLA_PROVIDER_MODE", "")
	t.Setenv("SETTLA_ENV", "")

	mode := provider.ModeFromEnv()
	if mode != provider.ProviderModeTestnet {
		t.Errorf("default mode: got %q, want %q", mode, provider.ProviderModeTestnet)
	}
}

func TestProviderModeFromEnv_MockInTest(t *testing.T) {
	t.Setenv("SETTLA_PROVIDER_MODE", "")
	t.Setenv("SETTLA_ENV", "test")

	mode := provider.ModeFromEnv()
	if mode != provider.ProviderModeMock {
		t.Errorf("test env mode: got %q, want %q", mode, provider.ProviderModeMock)
	}
}

func TestProviderModeFromEnv_Explicit(t *testing.T) {
	tests := []struct {
		env  string
		want provider.ProviderMode
	}{
		{"mock", provider.ProviderModeMock},
		{"testnet", provider.ProviderModeTestnet},
		{"live", provider.ProviderModeLive},
	}
	for _, tc := range tests {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SETTLA_PROVIDER_MODE", tc.env)
			got := provider.ModeFromEnv()
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ── NewRegistryFromMode tests ────────────────────────────────────────────────

func TestNewRegistryFromMode_Mock(t *testing.T) {
	reg := provider.NewRegistryFromMode(provider.ProviderModeMock, nil, slog.Default())

	// Mock mode returns empty registry for caller to populate.
	if ids := reg.ListOnRampIDs(context.Background()); len(ids) != 0 {
		t.Errorf("mock mode should return empty registry, got %d on-ramps", len(ids))
	}
}

func TestNewRegistryFromMode_Testnet(t *testing.T) {
	on := &fakeOnRamp{id: "settla-onramp"}
	off := &fakeOffRamp{id: "settla-offramp"}
	bc := &fakeBlockchain{chain: "tron"}

	reg := provider.NewRegistryFromMode(provider.ProviderModeTestnet, &provider.SettlaProviderDeps{
		OnRamp:  on,
		OffRamp: off,
		Chains:  []domain.BlockchainClient{bc},
	}, slog.Default())

	// Verify on-ramp registered.
	got, err := reg.GetOnRamp("settla-onramp")
	if err != nil {
		t.Fatalf("GetOnRamp: %v", err)
	}
	if got.ID() != "settla-onramp" {
		t.Errorf("on-ramp ID: got %q, want %q", got.ID(), "settla-onramp")
	}

	// Verify off-ramp registered.
	gotOff, err := reg.GetOffRamp("settla-offramp")
	if err != nil {
		t.Fatalf("GetOffRamp: %v", err)
	}
	if gotOff.ID() != "settla-offramp" {
		t.Errorf("off-ramp ID: got %q, want %q", gotOff.ID(), "settla-offramp")
	}

	// Verify blockchain client registered.
	gotBC, err := reg.GetBlockchainClient("tron")
	if err != nil {
		t.Fatalf("GetBlockchainClient: %v", err)
	}
	if gotBC.Chain() != "tron" {
		t.Errorf("chain: got %q, want %q", gotBC.Chain(), "tron")
	}
}

func TestNewRegistryFromMode_TestnetNilDeps(t *testing.T) {
	// Should not panic; returns empty registry with a warning.
	reg := provider.NewRegistryFromMode(provider.ProviderModeTestnet, nil, slog.Default())
	if ids := reg.ListOnRampIDs(context.Background()); len(ids) != 0 {
		t.Errorf("nil deps should return empty registry, got %d on-ramps", len(ids))
	}
}

func TestNewRegistryFromMode_Live(t *testing.T) {
	reg := provider.NewRegistryFromMode(provider.ProviderModeLive, nil, slog.Default())
	if ids := reg.ListOnRampIDs(context.Background()); len(ids) != 0 {
		t.Errorf("live mode should return empty registry, got %d on-ramps", len(ids))
	}
}

func TestNewRegistryFromMode_ModeSwitchIsConfigOnly(t *testing.T) {
	on := &fakeOnRamp{id: "settla-onramp"}
	off := &fakeOffRamp{id: "settla-offramp"}
	deps := &provider.SettlaProviderDeps{OnRamp: on, OffRamp: off}
	logger := slog.Default()

	// Switch between mock and testnet — same deps, different behaviour.
	mockReg := provider.NewRegistryFromMode(provider.ProviderModeMock, deps, logger)
	testnetReg := provider.NewRegistryFromMode(provider.ProviderModeTestnet, deps, logger)

	mockOnRamps := mockReg.ListOnRampIDs(context.Background())
	testnetOnRamps := testnetReg.ListOnRampIDs(context.Background())

	if len(mockOnRamps) != 0 {
		t.Errorf("mock: expected 0 on-ramps, got %d", len(mockOnRamps))
	}
	if len(testnetOnRamps) != 1 {
		t.Errorf("testnet: expected 1 on-ramp, got %d", len(testnetOnRamps))
	}
}

// ── StablecoinsFromProviders tests ──────────────────────────────────────────

type fakeOnRampWithPairs struct {
	id    string
	pairs []domain.CurrencyPair
}

func (f *fakeOnRampWithPairs) ID() string                            { return f.id }
func (f *fakeOnRampWithPairs) SupportedPairs() []domain.CurrencyPair { return f.pairs }
func (f *fakeOnRampWithPairs) GetQuote(_ context.Context, _ domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (f *fakeOnRampWithPairs) Execute(_ context.Context, _ domain.OnRampRequest) (*domain.ProviderTx, error) {
	return nil, nil
}
func (f *fakeOnRampWithPairs) GetStatus(_ context.Context, _ string) (*domain.ProviderTx, error) {
	return nil, nil
}

type fakeOffRampWithPairs struct {
	id    string
	pairs []domain.CurrencyPair
}

func (f *fakeOffRampWithPairs) ID() string                            { return f.id }
func (f *fakeOffRampWithPairs) SupportedPairs() []domain.CurrencyPair { return f.pairs }
func (f *fakeOffRampWithPairs) GetQuote(_ context.Context, _ domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (f *fakeOffRampWithPairs) Execute(_ context.Context, _ domain.OffRampRequest) (*domain.ProviderTx, error) {
	return nil, nil
}
func (f *fakeOffRampWithPairs) GetStatus(_ context.Context, _ string) (*domain.ProviderTx, error) {
	return nil, nil
}

func TestStablecoinsFromProviders_DiscoverFromPairs(t *testing.T) {
	reg := provider.NewRegistry()
	ctx := context.Background()

	// On-ramp: GBP→USDT, NGN→USDC
	reg.RegisterOnRamp(&fakeOnRampWithPairs{
		id: "on-1",
		pairs: []domain.CurrencyPair{
			{From: domain.CurrencyGBP, To: domain.CurrencyUSDT},
			{From: domain.CurrencyNGN, To: domain.CurrencyUSDC},
		},
	})
	// Off-ramp: USDT→NGN
	reg.RegisterOffRamp(&fakeOffRampWithPairs{
		id: "off-1",
		pairs: []domain.CurrencyPair{
			{From: domain.CurrencyUSDT, To: domain.CurrencyNGN},
		},
	})

	stables := reg.StablecoinsFromProviders(ctx)

	stableSet := make(map[domain.Currency]bool)
	for _, s := range stables {
		stableSet[s] = true
	}

	if !stableSet[domain.CurrencyUSDT] {
		t.Error("expected USDT in stablecoins")
	}
	if !stableSet[domain.CurrencyUSDC] {
		t.Error("expected USDC in stablecoins")
	}
	if len(stables) != 2 {
		t.Errorf("expected 2 stablecoins, got %d", len(stables))
	}
}

func TestStablecoinsFromProviders_EmptyRegistry(t *testing.T) {
	reg := provider.NewRegistry()
	ctx := context.Background()

	stables := reg.StablecoinsFromProviders(ctx)
	if len(stables) != 0 {
		t.Errorf("expected 0 stablecoins from empty registry, got %d", len(stables))
	}
}

func TestStablecoinsFromProviders_NoFiatInResult(t *testing.T) {
	reg := provider.NewRegistry()
	ctx := context.Background()

	// On-ramp outputs GBP (fiat, not stablecoin) — should NOT appear.
	reg.RegisterOnRamp(&fakeOnRampWithPairs{
		id:    "weird-onramp",
		pairs: []domain.CurrencyPair{{From: domain.CurrencyUSD, To: domain.CurrencyGBP}},
	})

	stables := reg.StablecoinsFromProviders(ctx)
	if len(stables) != 0 {
		t.Errorf("expected 0 stablecoins (fiat should be excluded), got %d: %v", len(stables), stables)
	}
}
