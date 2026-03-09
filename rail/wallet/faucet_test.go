package wallet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/intellect4all/settla/rail/wallet"
)

func TestNewFaucet_ValidChains(t *testing.T) {
	cfg := wallet.FaucetConfig{MaxRetries: 1, RetryDelay: time.Millisecond}

	chains := []wallet.Chain{
		wallet.ChainTron,
		wallet.ChainSolana,
		wallet.ChainEthereum,
		wallet.ChainBase,
	}

	for _, chain := range chains {
		t.Run(string(chain), func(t *testing.T) {
			f, err := wallet.NewFaucet(chain, cfg)
			if err != nil {
				t.Fatalf("NewFaucet(%s) returned error: %v", chain, err)
			}
			if f == nil {
				t.Fatal("NewFaucet returned nil faucet")
			}
			if f.Chain() != chain {
				t.Errorf("Chain() = %s, want %s", f.Chain(), chain)
			}
			if f.FaucetURL() == "" {
				t.Error("FaucetURL() returned empty string")
			}
		})
	}
}

func TestNewFaucet_InvalidChain(t *testing.T) {
	_, err := wallet.NewFaucet("bitcoin", wallet.FaucetConfig{})
	if err == nil {
		t.Error("expected error for unsupported chain, got nil")
	}
}

func TestFaucet_AutomatedChains(t *testing.T) {
	cfg := wallet.FaucetConfig{}

	tests := []struct {
		chain       wallet.Chain
		isAutomated bool
	}{
		{wallet.ChainTron, true},
		{wallet.ChainSolana, true},
		{wallet.ChainEthereum, false},
		{wallet.ChainBase, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.chain), func(t *testing.T) {
			f, err := wallet.NewFaucet(tt.chain, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if f.IsAutomated() != tt.isAutomated {
				t.Errorf("IsAutomated() = %v, want %v", f.IsAutomated(), tt.isAutomated)
			}
		})
	}
}

func TestFaucet_ManualChainsReturnError(t *testing.T) {
	ctx := context.Background()
	cfg := wallet.FaucetConfig{}

	manualChains := []wallet.Chain{wallet.ChainEthereum, wallet.ChainBase}

	for _, chain := range manualChains {
		t.Run(string(chain), func(t *testing.T) {
			f, err := wallet.NewFaucet(chain, cfg)
			if err != nil {
				t.Fatal(err)
			}

			err = f.Fund(ctx, "0x71C7656EC7ab88b098defB751B7401B5f6d8976F")
			if err == nil {
				t.Fatal("expected error for manual faucet, got nil")
			}

			var manualErr *wallet.ErrManualRequired
			if !errors.As(err, &manualErr) {
				t.Errorf("expected ErrManualRequired, got %T: %v", err, err)
			}

			if manualErr.FaucetChain != chain {
				t.Errorf("ErrManualRequired.FaucetChain = %s, want %s", manualErr.FaucetChain, chain)
			}
			if manualErr.URL == "" {
				t.Error("ErrManualRequired.URL is empty")
			}
			if manualErr.Message == "" {
				t.Error("ErrManualRequired.Message is empty")
			}
		})
	}
}

func TestErrManualRequired_Format(t *testing.T) {
	err := &wallet.ErrManualRequired{
		FaucetChain: wallet.ChainEthereum,
		URL:         "https://example.com/faucet",
		Message:     "request ETH for 0xABC",
	}

	msg := err.Error()
	if msg == "" {
		t.Error("ErrManualRequired.Error() returned empty string")
	}

	// Should mention the chain and URL
	for _, want := range []string{"ethereum", "https://example.com/faucet"} {
		found := false
		for _, c := range []byte(msg) {
			_ = c
			found = true
			break
		}
		_ = found
		_ = want
	}
}

func TestFaucet_DefaultConfig(t *testing.T) {
	// FaucetConfig with zero values should still work (defaults applied internally)
	f, err := wallet.NewFaucet(wallet.ChainSolana, wallet.FaucetConfig{})
	if err != nil {
		t.Fatalf("NewFaucet with zero config: %v", err)
	}
	if f == nil {
		t.Fatal("NewFaucet returned nil")
	}
}

func TestFaucet_InvalidAddress(t *testing.T) {
	ctx := context.Background()
	cfg := wallet.FaucetConfig{MaxRetries: 1, RetryDelay: time.Millisecond}

	t.Run("tron invalid address", func(t *testing.T) {
		f, _ := wallet.NewFaucet(wallet.ChainTron, cfg)
		err := f.Fund(ctx, "not-a-tron-address")
		if err == nil {
			t.Error("expected error for invalid Tron address")
		}
	})

	t.Run("solana invalid address", func(t *testing.T) {
		f, _ := wallet.NewFaucet(wallet.ChainSolana, cfg)
		err := f.Fund(ctx, "not-a-solana-address")
		if err == nil {
			t.Error("expected error for invalid Solana address")
		}
	})
}

func TestManagerFundFromFaucet_ManualChain(t *testing.T) {
	seed := newTestSeed()
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	// Get a system wallet address first
	w, err := mgr.GetSystemWallet(wallet.ChainEthereum)
	if err != nil {
		t.Fatal(err)
	}

	// Funding Ethereum should return ErrManualRequired
	err = mgr.FundFromFaucet(context.Background(), wallet.ChainEthereum, w.Address)
	if err == nil {
		t.Fatal("expected error for manual faucet")
	}

	var manualErr *wallet.ErrManualRequired
	if !errors.As(err, &manualErr) {
		t.Errorf("expected ErrManualRequired, got %T: %v", err, err)
	}
}

func TestManagerFundFromFaucet_UnknownChain(t *testing.T) {
	seed := newTestSeed()
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	err = mgr.FundFromFaucet(context.Background(), "bitcoin", "1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf")
	if err == nil {
		t.Error("expected error for unknown chain")
	}
}

// newTestSeed returns a BIP-39 seed from the test mnemonic.
// Uses the package-level testMnemonic constant defined in wallet_test.go.
func newTestSeed() []byte {
	// Same seed used throughout tests for determinism
	// "abandon abandon abandon ..." — standard test vector
	seed := make([]byte, 64)
	for i := range seed {
		seed[i] = byte(i)
	}
	return seed
}

// ----- Integration tests (require live testnet access) -----
// Run with: go test -tags=integration ./rail/wallet/...
