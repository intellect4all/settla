package blockchain

import (
	"fmt"
	"log/slog"
	"math/big"
	"sync"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/blockchain/ethereum"
	"github.com/intellect4all/settla/rail/blockchain/solana"
	"github.com/intellect4all/settla/rail/blockchain/tron"
	"github.com/intellect4all/settla/rail/wallet"
)

// Registry holds blockchain clients indexed by chain name.
//
// It is the single source of truth for domain.BlockchainClient instances
// within the rail layer. Clients can be registered individually via Register()
// or bootstrapped from environment configuration via NewRegistryFromConfig().
type Registry struct {
	mu      sync.RWMutex
	clients map[domain.CryptoChain]domain.BlockchainClient
	logger  *slog.Logger
}

// NewRegistry creates an empty registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		clients: make(map[domain.CryptoChain]domain.BlockchainClient),
		logger:  logger,
	}
}

// NewRegistryFromConfig creates a registry pre-populated with blockchain clients
// for all configured chains. Each client uses the RPC URL from cfg (falling
// back to the public testnet default).
//
// If walletMgr is non-nil, clients are created with signing capability — they
// can build, sign, and broadcast real transactions. If walletMgr is nil, clients
// are read-only (balance queries, gas estimation, transaction status only).
//
// An error is returned only when a client constructor itself fails (e.g., an
// unparseable RPC URL). Network connectivity is NOT verified at construction time.
func NewRegistryFromConfig(cfg BlockchainConfig, walletMgr *wallet.Manager, logger *slog.Logger) (*Registry, error) {
	r := NewRegistry(logger)

	// ── Tron (Nile testnet) ────────────────────────────────────────────────
	tronCfg := tron.NileConfig
	if cfg.TronRPCURL != "" {
		tronCfg.RPCURL = cfg.TronRPCURL
	}
	if cfg.TronAPIKey != "" {
		tronCfg.APIKey = cfg.TronAPIKey
	}
	r.Register(tron.NewClient(tronCfg, walletMgr, logger))

	// ── Ethereum (Sepolia testnet) ─────────────────────────────────────────
	ethCfg := ethereum.SepoliaConfig
	if cfg.EthereumRPCURL != "" {
		ethCfg.RPCURL = cfg.EthereumRPCURL
	}
	var ethSigner ethereum.Signer
	if walletMgr != nil {
		ethSigner = ethereum.NewWalletSigner(walletMgr, big.NewInt(ethCfg.ChainID))
	}
	ethClient, err := ethereum.NewClient(ethCfg, ethSigner, logger)
	if err != nil {
		return nil, fmt.Errorf("settla-blockchain: creating ethereum client: %w", err)
	}
	r.Register(ethClient)

	// ── Base (Sepolia testnet) ─────────────────────────────────────────────
	baseCfg := ethereum.BaseSepoliaConfig
	if cfg.BaseRPCURL != "" {
		baseCfg.RPCURL = cfg.BaseRPCURL
	}
	var baseSigner ethereum.Signer
	if walletMgr != nil {
		baseSigner = ethereum.NewWalletSigner(walletMgr, big.NewInt(baseCfg.ChainID))
	}
	baseClient, err := ethereum.NewClient(baseCfg, baseSigner, logger)
	if err != nil {
		return nil, fmt.Errorf("settla-blockchain: creating base client: %w", err)
	}
	r.Register(baseClient)

	// ── Solana (Devnet) ────────────────────────────────────────────────────
	solCfg := solana.DevnetConfig()
	if cfg.SolanaRPCURL != "" {
		solCfg.RPCURL = cfg.SolanaRPCURL
	}
	r.Register(solana.New(solCfg, walletMgr))

	return r, nil
}

// Register adds or replaces a blockchain client in the registry.
// The client's Chain() return value is used as the key.
func (r *Registry) Register(client domain.BlockchainClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[client.Chain()] = client
	r.logger.Debug("settla-blockchain: registered client", "chain", client.Chain())
}

// GetClient returns the client for the given chain identifier.
// Returns an error if the chain is not registered.
func (r *Registry) GetClient(chain domain.CryptoChain) (domain.BlockchainClient, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[chain]
	if !ok {
		return nil, fmt.Errorf("settla-blockchain: no client registered for chain %q", chain)
	}
	return c, nil
}

// MustGetClient returns the client for chain, panicking if not registered.
// Intended for use in initialisation code where a missing client is a
// programming error.
func (r *Registry) MustGetClient(chain domain.CryptoChain) domain.BlockchainClient {
	c, err := r.GetClient(chain)
	if err != nil {
		panic(err)
	}
	return c
}

// RegisterSystemWallets derives and registers the system hot wallet for each
// configured chain. This is required for signing: EVM clients need a
// RegisterWallet(address, path) call to map the sender address to the wallet
// manager path, and Solana clients need the same. Tron resolves wallets
// internally via the wallet manager, but we still derive the wallet so it is
// ready for use.
//
// Errors are logged but not fatal — some chains may not be configured.
func (r *Registry) RegisterSystemWallets(walletMgr *wallet.Manager) error {
	if walletMgr == nil {
		return nil
	}

	chains := []struct {
		walletChain wallet.Chain
		cryptoChain domain.CryptoChain
	}{
		{wallet.ChainTron, domain.ChainTron},
		{wallet.ChainEthereum, domain.ChainEthereum},
		{wallet.ChainBase, domain.ChainBase},
		{wallet.ChainSolana, domain.ChainSolana},
	}

	for _, c := range chains {
		w, err := walletMgr.GetSystemWallet(c.walletChain)
		if err != nil {
			r.logger.Warn("settla-blockchain: failed to create system wallet",
				"chain", c.walletChain, "error", err)
			continue
		}

		client, err := r.GetClient(c.cryptoChain)
		if err != nil {
			continue // chain not configured in registry
		}

		// Register the wallet address with the client's signer so it can
		// resolve address → wallet path when signing transactions.
		type walletRegistrar interface {
			RegisterWallet(address, walletPath string)
		}
		if reg, ok := client.(walletRegistrar); ok {
			reg.RegisterWallet(w.Address, w.Path)
		}

		r.logger.Info("settla-blockchain: registered system wallet",
			"chain", c.walletChain, "address", w.Address)
	}

	return nil
}

// Chains returns the chain identifiers of all registered clients.
func (r *Registry) Chains() []domain.CryptoChain {
	r.mu.RLock()
	defer r.mu.RUnlock()
	chains := make([]domain.CryptoChain, 0, len(r.clients))
	for chain := range r.clients {
		chains = append(chains, chain)
	}
	return chains
}
