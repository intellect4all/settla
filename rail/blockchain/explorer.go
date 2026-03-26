package blockchain

import (
	"fmt"

	"github.com/intellect4all/settla/domain"
)

// ExplorerURL returns the testnet block explorer URL for a transaction hash on
// the given chain. Chain names match domain.BlockchainClient.Chain() values.
//
// Supported chains and their explorers:
//   - ChainTron     → Nile Tronscan
//   - ChainEthereum → Sepolia Etherscan
//   - ChainBase     → Base Sepolia Basescan
//   - ChainSolana   → Solana Explorer (devnet cluster)
//
// Returns an empty string for unrecognised chains.
//
// ExplorerURLProvider wraps ExplorerURL as an interface suitable for
// router.WithExplorerUrl(). Usage: router.WithExplorerUrl(blockchain.Explorer{})
type Explorer struct{}

// ExplorerURL implements router.ExplorerURLProvider.
func (Explorer) ExplorerURL(chain domain.CryptoChain, txHash string) string {
	return ExplorerURL(chain, txHash)
}

func ExplorerURL(chain domain.CryptoChain, txHash string) string {
	switch chain {
	case domain.ChainTron:
		return "https://nile.tronscan.org/#/transaction/" + txHash
	case domain.ChainEthereum:
		return "https://sepolia.etherscan.io/tx/" + txHash
	case domain.ChainBase:
		return "https://sepolia.basescan.org/tx/" + txHash
	case domain.ChainSolana:
		return fmt.Sprintf("https://explorer.solana.com/tx/%s?cluster=devnet", txHash)
	default:
		return ""
	}
}
