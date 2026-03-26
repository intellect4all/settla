package factory

import (
	"log/slog"

	"github.com/intellect4all/settla/domain"
)

// ChainRegistry is a minimal interface for blockchain client lookup.
// Satisfied by *blockchain.Registry without importing it — breaks the
// factory → blockchain → {tron,ethereum,solana} → factory cycle.
type ChainRegistry interface {
	GetClient(chain domain.CryptoChain) (domain.BlockchainClient, error)
}

// Deps holds shared dependencies that provider factories may use.
// Fields may be nil depending on the provider mode (e.g., BlockchainReg is nil in mock mode).
type Deps struct {
	Logger        *slog.Logger
	BlockchainReg ChainRegistry // nil in mock mode; *blockchain.Registry in testnet/live
	WalletManager any           // nil when read-only / not configured
}
