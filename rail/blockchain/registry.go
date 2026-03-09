package blockchain

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/blockchain/ethereum"
	"github.com/intellect4all/settla/rail/blockchain/solana"
	"github.com/intellect4all/settla/rail/blockchain/tron"
)

// Registry holds blockchain clients indexed by chain name.
//
// It is the single source of truth for domain.BlockchainClient instances
// within the rail layer. Clients can be registered individually via Register()
// or bootstrapped from environment configuration via NewRegistryFromConfig().
type Registry struct {
	mu      sync.RWMutex
	clients map[string]domain.BlockchainClient
	logger  *slog.Logger
}

// NewRegistry creates an empty registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		clients: make(map[string]domain.BlockchainClient),
		logger:  logger,
	}
}

// NewRegistryFromConfig creates a registry pre-populated with read-only clients
// for all configured chains. Each client uses the RPC URL from cfg (falling
// back to the public testnet default). Clients are initialised without signers;
// call Register() to replace them with signing-capable instances.
//
// An error is returned only when a client constructor itself fails (e.g., an
// unparseable RPC URL). Network connectivity is NOT verified at construction time.
func NewRegistryFromConfig(cfg BlockchainConfig, logger *slog.Logger) (*Registry, error) {
	r := NewRegistry(logger)

	// ── Tron (Nile testnet) ────────────────────────────────────────────────
	tronCfg := tron.NileConfig
	if cfg.TronRPCURL != "" {
		tronCfg.RPCURL = cfg.TronRPCURL
	}
	if cfg.TronAPIKey != "" {
		tronCfg.APIKey = cfg.TronAPIKey
	}
	r.Register(tron.NewClient(tronCfg, nil /* read-only */, logger))

	// ── Ethereum (Sepolia testnet) ─────────────────────────────────────────
	ethCfg := ethereum.SepoliaConfig
	if cfg.EthereumRPCURL != "" {
		ethCfg.RPCURL = cfg.EthereumRPCURL
	}
	ethClient, err := ethereum.NewClient(ethCfg, nil /* read-only */, logger)
	if err != nil {
		return nil, fmt.Errorf("settla-blockchain: creating ethereum client: %w", err)
	}
	r.Register(ethClient)

	// ── Base (Sepolia testnet) ─────────────────────────────────────────────
	baseCfg := ethereum.BaseSepoliaConfig
	if cfg.BaseRPCURL != "" {
		baseCfg.RPCURL = cfg.BaseRPCURL
	}
	baseClient, err := ethereum.NewClient(baseCfg, nil /* read-only */, logger)
	if err != nil {
		return nil, fmt.Errorf("settla-blockchain: creating base client: %w", err)
	}
	r.Register(baseClient)

	// ── Solana (Devnet) ────────────────────────────────────────────────────
	solCfg := solana.DevnetConfig()
	if cfg.SolanaRPCURL != "" {
		solCfg.RPCURL = cfg.SolanaRPCURL
	}
	r.Register(solana.New(solCfg, nil /* read-only */))

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
func (r *Registry) GetClient(chain string) (domain.BlockchainClient, error) {
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
func (r *Registry) MustGetClient(chain string) domain.BlockchainClient {
	c, err := r.GetClient(chain)
	if err != nil {
		panic(err)
	}
	return c
}

// Chains returns the chain identifiers of all registered clients.
func (r *Registry) Chains() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	chains := make([]string, 0, len(r.clients))
	for chain := range r.clients {
		chains = append(chains, chain)
	}
	return chains
}
