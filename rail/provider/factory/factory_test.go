package factory

import (
	"context"
	"testing"

	"github.com/intellect4all/settla/domain"
	"github.com/shopspring/decimal"
)

// --- Fake providers for testing ---

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
func (f *fakeBlockchain) GetBalance(_ context.Context, _ string, _ string) (decimal.Decimal, error) {
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

// --- Tests ---

func TestRegisterAndRetrieveFactories(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	RegisterOnRampFactory("test-onramp", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "test-onramp"}, nil
	})
	RegisterOffRampFactory("test-offramp", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OffRampProvider, error) {
		return &fakeOffRamp{id: "test-offramp"}, nil
	})
	RegisterBlockchainFactory("test-chain", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.BlockchainClient, error) {
		return &fakeBlockchain{chain: "test-chain"}, nil
	})

	onRamps := OnRampFactories(ModeMock)
	if len(onRamps) != 1 {
		t.Fatalf("expected 1 on-ramp factory for mock, got %d", len(onRamps))
	}
	if _, ok := onRamps["test-onramp"]; !ok {
		t.Fatal("expected test-onramp in mock factories")
	}

	offRamps := OffRampFactories(ModeMock)
	if len(offRamps) != 1 {
		t.Fatalf("expected 1 off-ramp factory for mock, got %d", len(offRamps))
	}

	blockchains := BlockchainFactories(ModeMock)
	if len(blockchains) != 1 {
		t.Fatalf("expected 1 blockchain factory for mock, got %d", len(blockchains))
	}
}

func TestModeFiltering(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	RegisterOnRampFactory("mock-only", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "mock-only"}, nil
	})
	RegisterOnRampFactory("testnet-only", []ProviderMode{ModeTestnet}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "testnet-only"}, nil
	})
	RegisterOnRampFactory("multi-mode", []ProviderMode{ModeMock, ModeTestnet, ModeLive}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "multi-mode"}, nil
	})

	// Mock mode: mock-only + multi-mode
	mockFactories := OnRampFactories(ModeMock)
	if len(mockFactories) != 2 {
		t.Fatalf("expected 2 mock factories, got %d", len(mockFactories))
	}
	if _, ok := mockFactories["mock-only"]; !ok {
		t.Fatal("expected mock-only in mock factories")
	}
	if _, ok := mockFactories["multi-mode"]; !ok {
		t.Fatal("expected multi-mode in mock factories")
	}

	// Testnet mode: testnet-only + multi-mode
	testnetFactories := OnRampFactories(ModeTestnet)
	if len(testnetFactories) != 2 {
		t.Fatalf("expected 2 testnet factories, got %d", len(testnetFactories))
	}

	// Live mode: multi-mode only
	liveFactories := OnRampFactories(ModeLive)
	if len(liveFactories) != 1 {
		t.Fatalf("expected 1 live factory, got %d", len(liveFactories))
	}
}

func TestResetForTesting(t *testing.T) {
	ResetForTesting()

	RegisterOnRampFactory("temp", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "temp"}, nil
	})

	if len(OnRampFactories(ModeMock)) != 1 {
		t.Fatal("expected 1 factory before reset")
	}

	ResetForTesting()

	if len(OnRampFactories(ModeMock)) != 0 {
		t.Fatal("expected 0 factories after reset")
	}
}
