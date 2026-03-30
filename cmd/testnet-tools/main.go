// cmd/testnet-tools is a CLI utility for testnet wallet setup and verification.
//
// Commands:
//
//	setup    — Create system + tenant wallets, fund from faucets
//	verify   — Check RPC connectivity, wallet balances, explorer links
//	status   — Show wallet addresses and balances
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/wallet"
)

// Seed tenants from db/seed/transfer_seed.sql
var seedTenants = []struct {
	ID   string
	Slug string
}{
	{ID: "a0000000-0000-0000-0000-000000000001", Slug: "lemfi"},
	{ID: "b0000000-0000-0000-0000-000000000002", Slug: "fincra"},
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cmd := os.Args[1]
	switch cmd {
	case "setup":
		if err := runSetup(logger); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(logger); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := runStatus(logger); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: testnet-tools <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  setup    Create system + tenant wallets, fund from faucets")
	fmt.Fprintln(os.Stderr, "  verify   Check RPC connectivity and wallet balances")
	fmt.Fprintln(os.Stderr, "  status   Show wallet addresses and explorer links")
}

// newWalletManager creates a wallet.Manager from environment variables.
func newWalletManager(logger *slog.Logger) (*wallet.Manager, error) {
	encKey := os.Getenv("SETTLA_WALLET_ENCRYPTION_KEY")
	if encKey == "" {
		return nil, fmt.Errorf("SETTLA_WALLET_ENCRYPTION_KEY is required (64 hex chars = 32 bytes)")
	}

	storagePath := os.Getenv("SETTLA_WALLET_STORAGE_PATH")
	if storagePath == "" {
		storagePath = ".settla/wallets"
	}

	// Decode master seed if provided as hex
	var masterSeed []byte
	if seedHex := os.Getenv("SETTLA_MASTER_SEED"); seedHex != "" {
		var err error
		masterSeed, err = hex.DecodeString(seedHex)
		if err != nil {
			return nil, fmt.Errorf("SETTLA_MASTER_SEED must be hex-encoded: %w", err)
		}
	}

	faucetCfg := wallet.FaucetConfig{
		TronNileFaucetURL:    envOrDefault("SETTLA_TRON_FAUCET_URL", "https://nileex.io"),
		TronNileAPIKey:       os.Getenv("SETTLA_TRON_API_KEY"),
		SolanaRPCURL:         envOrDefault("SETTLA_SOLANA_RPC_URL", "https://api.devnet.solana.com"),
		SolanaAirdropSOL:     1.0,
		EthereumFaucetURL:    "https://sepoliafaucet.com",
		BaseSepoliaFaucetURL: "https://www.coinbase.com/faucets/base-ethereum-goerli-faucet",
		MaxRetries:           3,
		RetryDelay:           2 * time.Second,
	}

	return wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    masterSeed,
		EncryptionKey: encKey,
		StoragePath:   storagePath,
		Logger:        logger,
		FaucetConfig:  faucetCfg,
	})
}

func runSetup(logger *slog.Logger) error {
	fmt.Println("=== Settla Testnet Setup ===")
	fmt.Println()

	mgr, err := newWalletManager(logger)
	if err != nil {
		return fmt.Errorf("wallet manager init: %w", err)
	}
	defer mgr.Close()

	chains := []wallet.Chain{wallet.ChainTron, wallet.ChainSolana, wallet.ChainEthereum, wallet.ChainBase}
	ctx := context.Background()

	fmt.Println("--- Creating system wallets ---")
	var systemWallets []*wallet.Wallet
	for _, chain := range chains {
		w, err := mgr.GetSystemWallet(chain)
		if err != nil {
			return fmt.Errorf("creating system wallet for %s: %w", chain, err)
		}
		systemWallets = append(systemWallets, w)
		fmt.Printf("  [%s] %s\n", chain, w.Address)
	}
	fmt.Println()

	fmt.Println("--- Creating tenant wallets ---")
	for _, tenant := range seedTenants {
		fmt.Printf("  Tenant: %s\n", tenant.Slug)
		for _, chain := range chains {
			w, err := mgr.GetOrCreateWallet(ctx,
				wallet.TenantWalletPath(tenant.Slug, chain),
				chain, nil)
			if err != nil {
				return fmt.Errorf("creating tenant wallet %s/%s: %w", tenant.Slug, chain, err)
			}
			fmt.Printf("    [%s] %s\n", chain, w.Address)
		}
	}
	fmt.Println()

	fmt.Println("--- Funding system wallets from faucets ---")
	var manualFunding []string
	for _, w := range systemWallets {
		chain := w.Chain
		fmt.Printf("  [%s] Requesting tokens for %s ...\n", chain, w.Address)
		err := mgr.FundFromFaucet(ctx, chain, w.Address)
		if err != nil {
			if manualErr, ok := err.(*wallet.ErrManualRequired); ok {
				manualFunding = append(manualFunding,
					fmt.Sprintf("  %s: %s", chain, manualErr.URL))
				fmt.Printf("    MANUAL: requires browser interaction\n")
			} else {
				fmt.Printf("    WARNING: %v\n", err)
			}
		} else {
			fmt.Printf("    OK\n")
		}
	}
	fmt.Println()

	if len(manualFunding) > 0 {
		fmt.Println("--- Manual Faucet Steps Required ---")
		fmt.Println("The following chains require manual browser-based faucet requests:")
		for _, msg := range manualFunding {
			fmt.Println(msg)
		}
		fmt.Println()
		fmt.Println("Fund each system wallet address shown above for the respective chain.")
		fmt.Println()
	}

	fmt.Println("--- Explorer Address Links ---")
	for _, w := range systemWallets {
		url := addressExplorerURL(string(w.Chain), w.Address)
		if url != "" {
			fmt.Printf("  [%s] %s\n", w.Chain, url)
		}
	}
	fmt.Println()

	fmt.Println("Setup complete. Run 'make testnet-verify' to check balances.")
	return nil
}

func runVerify(logger *slog.Logger) error {
	fmt.Println("=== Settla Testnet Verification ===")
	fmt.Println()

	fmt.Println("--- Checking RPC connectivity ---")
	cfg := blockchain.LoadConfigFromEnv()
	rpcEndpoints := map[string]string{
		"tron":     cfg.TronRPCURL,
		"ethereum": cfg.EthereumRPCURL,
		"base":     cfg.BaseRPCURL,
		"solana":   cfg.SolanaRPCURL,
	}

	allOK := true
	for chain, url := range rpcEndpoints {
		fmt.Printf("  [%s] %s ... ", chain, url)
		if err := checkRPC(chain, url); err != nil {
			fmt.Printf("FAIL (%v)\n", err)
			allOK = false
		} else {
			fmt.Printf("OK\n")
		}
	}
	fmt.Println()

	fmt.Println("--- Checking wallets ---")
	mgr, err := newWalletManager(logger)
	if err != nil {
		fmt.Printf("  WARNING: Could not initialize wallet manager: %v\n", err)
		fmt.Println("  Run 'make testnet-setup' first.")
		return nil
	}
	defer mgr.Close()

	wallets, err := mgr.ListWallets("")
	if err != nil {
		return fmt.Errorf("listing wallets: %w", err)
	}

	if len(wallets) == 0 {
		fmt.Println("  No wallets found. Run 'make testnet-setup' first.")
		return nil
	}

	systemCount := 0
	tenantCount := 0
	for _, w := range wallets {
		if w.IsSystemWallet() {
			systemCount++
		} else {
			tenantCount++
		}
	}
	fmt.Printf("  Found %d system wallets, %d tenant wallets\n", systemCount, tenantCount)
	fmt.Println()

	fmt.Println("--- Wallet Details ---")
	for _, w := range wallets {
		typeStr := "SYSTEM"
		if w.IsTenantWallet() {
			typeStr = fmt.Sprintf("TENANT(%s)", w.TenantSlug)
		}
		fmt.Printf("  [%s] %-10s %s  %s\n", w.Chain, typeStr, w.Address, w.Path)
	}
	fmt.Println()

	if !allOK {
		return fmt.Errorf("some RPC connectivity checks failed")
	}

	fmt.Println("Verification complete.")
	return nil
}

func runStatus(logger *slog.Logger) error {
	fmt.Println("=== Settla Testnet Wallet Status ===")
	fmt.Println()

	mgr, err := newWalletManager(logger)
	if err != nil {
		return fmt.Errorf("wallet manager init: %w", err)
	}
	defer mgr.Close()

	wallets, err := mgr.ListWallets("")
	if err != nil {
		return fmt.Errorf("listing wallets: %w", err)
	}

	if len(wallets) == 0 {
		fmt.Println("No wallets found. Run 'make testnet-setup' first.")
		return nil
	}

	// Group by type
	fmt.Println("--- System Wallets ---")
	for _, w := range wallets {
		if !w.IsSystemWallet() {
			continue
		}
		url := addressExplorerURL(string(w.Chain), w.Address)
		fmt.Printf("  %-10s %s\n", w.Chain, w.Address)
		if url != "" {
			fmt.Printf("  %-10s %s\n", "", url)
		}
	}
	fmt.Println()

	fmt.Println("--- Tenant Wallets ---")
	for _, w := range wallets {
		if !w.IsTenantWallet() {
			continue
		}
		fmt.Printf("  %-10s %-8s %s\n", w.Chain, w.TenantSlug, w.Address)
	}
	fmt.Println()

	return nil
}

// checkRPC performs an actual connectivity check against an RPC endpoint
// by sending a lightweight JSON-RPC or REST request appropriate for the chain.
func checkRPC(chain, url string) error {
	if url == "" {
		return fmt.Errorf("empty RPC URL")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("invalid URL scheme")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	switch chain {
	case "tron":
		// Tron uses REST-style API — GET /wallet/getnowblock
		resp, err := client.Get(strings.TrimRight(url, "/") + "/wallet/getnowblock")
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil

	case "solana":
		// Solana uses JSON-RPC — getHealth
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "getHealth",
		})
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil

	default:
		// Ethereum/Base use JSON-RPC — eth_blockNumber
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber", "params": []any{},
		})
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

// addressExplorerURL returns the block explorer URL for an address (not tx).
func addressExplorerURL(chain, address string) string {
	switch chain {
	case "tron":
		return "https://nile.tronscan.org/#/address/" + address
	case "ethereum":
		return "https://sepolia.etherscan.io/address/" + address
	case "base":
		return "https://sepolia.basescan.org/address/" + address
	case "solana":
		return fmt.Sprintf("https://explorer.solana.com/address/%s?cluster=devnet", address)
	default:
		return ""
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
