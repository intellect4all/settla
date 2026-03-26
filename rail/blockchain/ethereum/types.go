// Package ethereum implements the domain.BlockchainClient interface for
// EVM-compatible chains. It supports Ethereum Sepolia and Base Sepolia testnets
// using the same client with different configurations.
package ethereum

import (
	"fmt"
	"strings"
	"time"

	"github.com/intellect4all/settla/domain"
)

// Config holds the configuration for an EVM chain client.
type Config struct {
	// ChainName is the chain identifier (e.g., domain.ChainEthereum, domain.ChainBase).
	ChainName domain.CryptoChain

	// ChainID is the EVM chain ID (Sepolia: 11155111, Base Sepolia: 84532).
	ChainID int64

	// RPCURL is the JSON-RPC endpoint URL.
	RPCURL string

	// ExplorerURL is the block explorer base URL (no trailing slash).
	ExplorerURL string

	// Contracts maps token symbol (uppercase) → ERC20 contract address.
	Contracts map[string]string

	// BlockTime is the expected block production interval.
	BlockTime time.Duration

	// Confirmations is the number of blocks required to consider a tx confirmed.
	Confirmations uint64

	// GasLimit is the default gas limit for ERC20 transfers.
	GasLimit uint64

	// PollInterval controls how often SubscribeTransactions polls for new events.
	PollInterval time.Duration
}

// ContractAddress returns the ERC20 contract address for the given token symbol.
// Returns an error if the token is not configured.
func (c Config) ContractAddress(token string) (string, error) {
	addr, ok := c.Contracts[strings.ToUpper(token)]
	if !ok || addr == "" {
		return "", fmt.Errorf("settla-ethereum: no contract address for token %s on %s", token, c.ChainName)
	}
	return addr, nil
}

// ExplorerTxURL returns the block explorer URL for a transaction hash.
func (c Config) ExplorerTxURL(txHash string) string {
	return c.ExplorerURL + "/tx/" + txHash
}

var (
	// SepoliaConfig is the default configuration for Ethereum Sepolia testnet.
	SepoliaConfig = Config{
		ChainName:   "ethereum",
		ChainID:     11155111,
		RPCURL:      "https://rpc.sepolia.org",
		ExplorerURL: "https://sepolia.etherscan.io",
		Contracts: map[string]string{
			// Circle USDC on Sepolia
			"USDC": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
		BlockTime:     12 * time.Second,
		Confirmations: 12,
		GasLimit:      100_000,
		PollInterval:  15 * time.Second,
	}

	// BaseSepoliaConfig is the default configuration for Base Sepolia testnet.
	BaseSepoliaConfig = Config{
		ChainName:   "base",
		ChainID:     84532,
		RPCURL:      "https://sepolia.base.org",
		ExplorerURL: "https://sepolia.basescan.org",
		Contracts: map[string]string{
			// USDC on Base Sepolia
			"USDC": "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		},
		BlockTime:     2 * time.Second,
		Confirmations: 12,
		GasLimit:      100_000,
		PollInterval:  5 * time.Second,
	}
)

// TxStatus values used in domain.ChainTx.Status.
const (
	TxStatusPending   = "PENDING"
	TxStatusConfirmed = "CONFIRMED"
	TxStatusFailed    = "FAILED"
	TxStatusUnknown   = "UNKNOWN"
)
