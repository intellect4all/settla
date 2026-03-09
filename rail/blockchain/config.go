package blockchain

import "os"

// BlockchainConfig holds the RPC endpoint configuration for all supported chains.
// Values are loaded from environment variables, falling back to public testnet defaults.
//
// Environment variables:
//
//	SETTLA_TRON_RPC_URL       — Tron RPC URL (default: https://nile.trongrid.io)
//	SETTLA_TRON_API_KEY       — TronGrid API key (optional, increases rate limits)
//	SETTLA_ETHEREUM_RPC_URL   — Ethereum Sepolia RPC URL
//	SETTLA_BASE_RPC_URL       — Base Sepolia RPC URL
//	SETTLA_SOLANA_RPC_URL     — Solana Devnet RPC URL
type BlockchainConfig struct {
	TronRPCURL     string
	TronAPIKey     string
	EthereumRPCURL string
	BaseRPCURL     string
	SolanaRPCURL   string
}

// Testnet default RPC URLs (public, no auth required).
const (
	DefaultTronRPCURL     = "https://nile.trongrid.io"
	DefaultEthereumRPCURL = "https://rpc.sepolia.org"
	DefaultBaseRPCURL     = "https://sepolia.base.org"
	DefaultSolanaRPCURL   = "https://api.devnet.solana.com"
)

// LoadConfigFromEnv returns a BlockchainConfig populated from environment
// variables, using public testnet defaults for any missing values.
func LoadConfigFromEnv() BlockchainConfig {
	return BlockchainConfig{
		TronRPCURL:     envOrDefault("SETTLA_TRON_RPC_URL", DefaultTronRPCURL),
		TronAPIKey:     os.Getenv("SETTLA_TRON_API_KEY"),
		EthereumRPCURL: envOrDefault("SETTLA_ETHEREUM_RPC_URL", DefaultEthereumRPCURL),
		BaseRPCURL:     envOrDefault("SETTLA_BASE_RPC_URL", DefaultBaseRPCURL),
		SolanaRPCURL:   envOrDefault("SETTLA_SOLANA_RPC_URL", DefaultSolanaRPCURL),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
