package wallet

import (
	"context"
	"fmt"
	"time"
)

// Faucet provides testnet token funding for a specific blockchain.
type Faucet interface {
	// Fund requests testnet tokens for the given address.
	// Returns ErrManualRequired if the faucet requires manual interaction.
	Fund(ctx context.Context, address string) error

	// IsAutomated returns true if the faucet supports programmatic requests.
	// Returns false for faucets that require manual steps (captcha, web form).
	IsAutomated() bool

	// FaucetURL returns the URL for manual faucet requests.
	FaucetURL() string

	// Chain returns the blockchain network this faucet operates on.
	Chain() Chain
}

// FaucetConfig holds configuration for all chain testnet faucets.
// All fields are optional — sensible defaults are used when not set.
type FaucetConfig struct {
	// Tron Nile faucet
	TronNileFaucetURL string // Default endpoint base URL
	TronNileAPIKey    string // Optional TronGrid API key

	// Solana Devnet
	SolanaRPCURL     string  // Default: https://api.devnet.solana.com
	SolanaAirdropSOL float64 // Amount to request in SOL (default: 1.0)

	// Ethereum Sepolia (manual only — most require captcha)
	EthereumFaucetURL string // Documentation URL shown in error

	// Base Sepolia (manual — requires Coinbase account)
	BaseSepoliaFaucetURL string

	// Retry settings
	MaxRetries int           // Default: 3
	RetryDelay time.Duration // Default: 2s
}

// ErrManualRequired is returned when a faucet requires manual browser interaction.
type ErrManualRequired struct {
	FaucetChain Chain
	URL         string
	Message     string
}

func (e *ErrManualRequired) Error() string {
	return fmt.Sprintf("settla-wallet: %s faucet requires manual interaction — visit %s — %s",
		e.FaucetChain, e.URL, e.Message)
}

// NewFaucet returns the Faucet implementation for the given chain.
func NewFaucet(chain Chain, cfg FaucetConfig) (Faucet, error) {
	// Apply defaults
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 2 * time.Second
	}

	switch chain {
	case ChainTron:
		return newTronFaucet(cfg), nil
	case ChainSolana:
		return newSolanaFaucet(cfg), nil
	case ChainEthereum:
		return newEthereumFaucet(cfg), nil
	case ChainBase:
		return newBaseFaucet(cfg), nil
	default:
		return nil, fmt.Errorf("settla-wallet: no faucet available for chain %s", chain)
	}
}

// FundFromFaucet requests testnet tokens for the given chain and address.
// Uses the FaucetConfig set during Manager creation.
func (m *Manager) FundFromFaucet(ctx context.Context, chain Chain, address string) error {
	faucet, err := NewFaucet(chain, m.faucetCfg)
	if err != nil {
		return err
	}

	m.logger.Info("settla-wallet: requesting testnet tokens from faucet",
		"chain", chain,
		"address", address,
		"automated", faucet.IsAutomated(),
	)

	return faucet.Fund(ctx, address)
}

// withRetry executes fn up to maxRetries times with exponential backoff on failure.
// Returns the last error if all retries are exhausted.
func withRetry(ctx context.Context, maxRetries int, delay time.Duration, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if attempt < maxRetries-1 {
				select {
				case <-time.After(delay):
					delay *= 2 // Exponential backoff
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			continue
		}
		return nil
	}
	return lastErr
}
