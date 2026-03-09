//go:build integration

package wallet_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tyler-smith/go-bip39"

	"github.com/intellect4all/settla/rail/wallet"
)

// TestTronFaucetIntegration tests real TRX funding from the Tron Nile testnet faucet.
// Requires no env vars — Nile faucet is public.
// Set SETTLA_TRON_API_KEY for higher rate limits.
func TestTronFaucetIntegration(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "integration-tron",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
		FaucetConfig: wallet.FaucetConfig{
			TronNileAPIKey: os.Getenv("SETTLA_TRON_API_KEY"),
			MaxRetries:     3,
			RetryDelay:     5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("failed to create wallet manager: %v", err)
	}
	defer mgr.Close()

	w, err := mgr.GetSystemWallet(wallet.ChainTron)
	if err != nil {
		t.Fatalf("failed to get system wallet: %v", err)
	}

	t.Logf("Requesting TRX for Tron Nile address: %s", w.Address)
	t.Logf("Track on explorer: https://nile.tronscan.org/#/address/%s", w.Address)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := mgr.FundFromFaucet(ctx, wallet.ChainTron, w.Address); err != nil {
		t.Fatalf("Tron faucet funding failed: %v", err)
	}

	t.Log("TRX airdrop requested successfully — verify balance on Nile Tronscan")
}

// TestSolanaFaucetIntegration tests real SOL airdrops from Solana Devnet.
// Uses the native requestAirdrop RPC call — no API key required.
func TestSolanaFaucetIntegration(t *testing.T) {
	rpcURL := os.Getenv("SETTLA_SOLANA_RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://api.devnet.solana.com"
	}

	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "integration-solana",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
		FaucetConfig: wallet.FaucetConfig{
			SolanaRPCURL:     rpcURL,
			SolanaAirdropSOL: 1.0,
			MaxRetries:       3,
			RetryDelay:       5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("failed to create wallet manager: %v", err)
	}
	defer mgr.Close()

	w, err := mgr.GetSystemWallet(wallet.ChainSolana)
	if err != nil {
		t.Fatalf("failed to get system wallet: %v", err)
	}

	t.Logf("Requesting SOL airdrop for Solana Devnet address: %s", w.Address)
	t.Logf("Track on explorer: https://explorer.solana.com/address/%s?cluster=devnet", w.Address)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := mgr.FundFromFaucet(ctx, wallet.ChainSolana, w.Address); err != nil {
		t.Fatalf("Solana airdrop failed: %v", err)
	}

	t.Log("SOL airdrop requested successfully — verify balance on Solana Explorer (Devnet)")
}

// TestFaucetManagerRoundTrip tests that wallets can be created and funded in sequence.
func TestFaucetManagerRoundTrip(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "integration-roundtrip",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
		FaucetConfig: wallet.FaucetConfig{
			SolanaRPCURL:     "https://api.devnet.solana.com",
			SolanaAirdropSOL: 0.5,
			MaxRetries:       3,
			RetryDelay:       5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("failed to create wallet manager: %v", err)
	}
	defer mgr.Close()

	chains := []wallet.Chain{wallet.ChainTron, wallet.ChainSolana}

	for _, chain := range chains {
		t.Run(string(chain), func(t *testing.T) {
			w, err := mgr.GetSystemWallet(chain)
			if err != nil {
				t.Fatalf("GetSystemWallet(%s): %v", chain, err)
			}

			t.Logf("Chain: %s, Address: %s", chain, w.Address)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			if err := mgr.FundFromFaucet(ctx, chain, w.Address); err != nil {
				t.Logf("Faucet funding failed (may be rate limited): %v", err)
			} else {
				t.Logf("Successfully requested testnet tokens for %s on %s", w.Address, chain)
			}
		})
	}
}
