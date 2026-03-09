package blockchain

import "fmt"

// ExplorerURL returns the testnet block explorer URL for a transaction hash on
// the given chain. Chain names match domain.BlockchainClient.Chain() values.
//
// Supported chains and their explorers:
//   - "tron"     → Nile Tronscan
//   - "ethereum" → Sepolia Etherscan
//   - "base"     → Base Sepolia Basescan
//   - "solana"   → Solana Explorer (devnet cluster)
//
// Returns an empty string for unrecognised chains.
func ExplorerURL(chain, txHash string) string {
	switch chain {
	case "tron":
		return "https://nile.tronscan.org/#/transaction/" + txHash
	case "ethereum":
		return "https://sepolia.etherscan.io/tx/" + txHash
	case "base":
		return "https://sepolia.basescan.org/tx/" + txHash
	case "solana":
		return fmt.Sprintf("https://explorer.solana.com/tx/%s?cluster=devnet", txHash)
	default:
		return ""
	}
}
