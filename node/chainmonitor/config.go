package chainmonitor

import "time"

// ChainConfig holds the configuration for monitoring a specific blockchain.
type ChainConfig struct {
	// Chain is the identifier (e.g. "tron", "ethereum", "base").
	Chain string

	// PollInterval is how often to poll for new blocks.
	PollInterval time.Duration

	// Confirmations is the number of block confirmations required before
	// a deposit transaction is considered confirmed.
	Confirmations int

	// ReorgDepth is the maximum number of blocks to re-scan on each poll
	// cycle to detect chain reorganisations.
	ReorgDepth int

	// RPCURL is the primary RPC endpoint URL.
	RPCURL string

	// APIKey is the API key for the primary RPC endpoint.
	APIKey string

	// BackupRPCURL is the secondary/failover RPC endpoint URL.
	BackupRPCURL string

	// BackupAPIKey is the API key for the secondary RPC endpoint.
	BackupAPIKey string
}

// DefaultTronConfig returns sensible defaults for Tron mainnet monitoring.
func DefaultTronConfig() ChainConfig {
	return ChainConfig{
		Chain:         "tron",
		PollInterval:  3 * time.Second,
		Confirmations: 19,
		ReorgDepth:    20,
		RPCURL:        "https://api.trongrid.io",
		APIKey:        "",
	}
}

// DefaultEthereumConfig returns sensible defaults for Ethereum mainnet monitoring.
func DefaultEthereumConfig() ChainConfig {
	return ChainConfig{
		Chain:         "ethereum",
		PollInterval:  12 * time.Second, // ~1 block time
		Confirmations: 12,
		ReorgDepth:    20,
		RPCURL:        "https://eth-mainnet.g.alchemy.com/v2",
		APIKey:        "",
	}
}

// DefaultBaseConfig returns sensible defaults for Base mainnet monitoring.
func DefaultBaseConfig() ChainConfig {
	return ChainConfig{
		Chain:         "base",
		PollInterval:  2 * time.Second, // Base ~2s blocks
		Confirmations: 12,
		ReorgDepth:    20,
		RPCURL:        "https://mainnet.base.org",
		APIKey:        "",
	}
}

// MonitorConfig holds top-level configuration for the chain monitor.
type MonitorConfig struct {
	// Chains is the set of chains to monitor.
	Chains []ChainConfig

	// AddressSyncInterval is how often to do incremental address syncs.
	AddressSyncInterval time.Duration

	// FullReconcileInterval is how often to do a full address reconciliation.
	FullReconcileInterval time.Duration

	// TokenReloadInterval is how often to reload the token registry from DB.
	TokenReloadInterval time.Duration
}

// DefaultMonitorConfig returns sensible production defaults.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		AddressSyncInterval:   10 * time.Second,
		FullReconcileInterval: 5 * time.Minute,
		TokenReloadInterval:   5 * time.Minute,
	}
}
