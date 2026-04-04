package blockchain

import (
	"fmt"
	"os"
	"strings"

	"github.com/intellect4all/settla/domain"
)

// network determines whether to use mainnet or testnet explorers.
// Set SETTLA_NETWORK=mainnet in production; defaults to testnet.
var network = strings.ToLower(os.Getenv("SETTLA_NETWORK"))

func isMainnet() bool {
	return network == "mainnet" || network == "production"
}

// ExplorerURLProvider wraps ExplorerURL as an interface suitable for
// router.WithExplorerUrl(). Usage: router.WithExplorerUrl(blockchain.Explorer{})
type Explorer struct{}

// ExplorerURL implements router.ExplorerURLProvider.
func (Explorer) ExplorerURL(chain domain.CryptoChain, txHash string) string {
	return ExplorerURL(chain, txHash)
}

// ExplorerURL returns the block explorer URL for a transaction hash on the given
// chain. Uses mainnet explorers when SETTLA_NETWORK=mainnet, testnet otherwise.
// Returns an empty string for unrecognised chains.
func ExplorerURL(chain domain.CryptoChain, txHash string) string {
	if isMainnet() {
		return mainnetExplorerURL(chain, txHash)
	}
	return testnetExplorerURL(chain, txHash)
}

func mainnetExplorerURL(chain domain.CryptoChain, txHash string) string {
	switch chain {
	case domain.ChainTron:
		return "https://tronscan.org/#/transaction/" + txHash
	case domain.ChainEthereum:
		return "https://etherscan.io/tx/" + txHash
	case domain.ChainBase:
		return "https://basescan.org/tx/" + txHash
	case domain.ChainSolana:
		return fmt.Sprintf("https://explorer.solana.com/tx/%s", txHash)
	default:
		return ""
	}
}

func testnetExplorerURL(chain domain.CryptoChain, txHash string) string {
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
