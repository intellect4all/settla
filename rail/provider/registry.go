package provider

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/intellect4all/settla/domain"
)

// ProviderMode controls which set of providers the registry initialises.
type ProviderMode string

const (
	// ProviderModeMock uses mock providers (unit tests, CI).
	ProviderModeMock ProviderMode = "mock"
	// ProviderModeMockHTTP uses mock providers that delegate to an external HTTP service (demos).
	ProviderModeMockHTTP ProviderMode = "mock-http"
	// ProviderModeTestnet uses Settla on/off-ramp providers with real testnet blockchain.
	ProviderModeTestnet ProviderMode = "testnet"
	// ProviderModeLive is reserved for future production providers.
	ProviderModeLive ProviderMode = "live"
)

// ModeFromEnv reads SETTLA_PROVIDER_MODE from the environment.
// Returns ProviderModeTestnet if not set (development default).
// In test builds (SETTLA_ENV=test), defaults to ProviderModeMock.
func ModeFromEnv() ProviderMode {
	mode := os.Getenv("SETTLA_PROVIDER_MODE")
	switch ProviderMode(mode) {
	case ProviderModeMock:
		return ProviderModeMock
	case ProviderModeMockHTTP:
		return ProviderModeMockHTTP
	case ProviderModeTestnet:
		return ProviderModeTestnet
	case ProviderModeLive:
		return ProviderModeLive
	default:
		if os.Getenv("SETTLA_ENV") == "test" {
			return ProviderModeMock
		}
		return ProviderModeTestnet
	}
}

// SettlaProviderDeps holds the dependencies needed to construct Settla testnet providers.
type SettlaProviderDeps struct {
	OnRamp  domain.OnRampProvider
	OffRamp domain.OffRampProvider
	Chains  []domain.BlockchainClient
}

// NewRegistryFromMode creates a registry populated according to the given mode.
//
//   - mock: caller must register mock providers separately (returns empty registry)
//   - testnet: uses the supplied SettlaProviderDeps
//   - live: reserved (returns empty registry)
func NewRegistryFromMode(mode ProviderMode, deps *SettlaProviderDeps, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	reg := NewRegistry()

	switch mode {
	case ProviderModeTestnet:
		if deps == nil {
			logger.Warn("settla-rail: testnet mode requested but no provider deps supplied, registry empty")
			return reg
		}
		if deps.OnRamp != nil {
			reg.RegisterOnRamp(deps.OnRamp)
			logger.Info("settla-rail: registered testnet on-ramp", "provider", deps.OnRamp.ID())
		}
		if deps.OffRamp != nil {
			reg.RegisterOffRamp(deps.OffRamp)
			logger.Info("settla-rail: registered testnet off-ramp", "provider", deps.OffRamp.ID())
		}
		for _, c := range deps.Chains {
			reg.RegisterBlockchainClient(c)
			logger.Info("settla-rail: registered testnet blockchain", "chain", c.Chain())
		}

	case ProviderModeLive:
		logger.Warn("settla-rail: live provider mode not yet implemented, registry empty")

	default:
		// ProviderModeMock — caller registers mock providers after construction.
		logger.Info("settla-rail: mock provider mode, registry empty for caller to populate")
	}

	return reg
}

// Compile-time check: Registry implements domain.ProviderRegistry.
var _ domain.ProviderRegistry = (*Registry)(nil)

// Registry manages the set of available on-ramp, off-ramp, and blockchain providers.
type Registry struct {
	mu          sync.RWMutex
	onRamps     map[string]domain.OnRampProvider
	offRamps    map[string]domain.OffRampProvider
	blockchains map[domain.CryptoChain]domain.BlockchainClient
	normalizers map[string]domain.WebhookNormalizer // keyed by provider slug
	listeners   map[string]domain.ProviderListener  // keyed by provider slug
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		onRamps:     make(map[string]domain.OnRampProvider),
		offRamps:    make(map[string]domain.OffRampProvider),
		blockchains: make(map[domain.CryptoChain]domain.BlockchainClient),
		normalizers: make(map[string]domain.WebhookNormalizer),
		listeners:   make(map[string]domain.ProviderListener),
	}
}

// RegisterOnRamp adds an on-ramp provider to the registry.
func (r *Registry) RegisterOnRamp(p domain.OnRampProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRamps[p.ID()] = p
}

// RegisterOffRamp adds an off-ramp provider to the registry.
func (r *Registry) RegisterOffRamp(p domain.OffRampProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offRamps[p.ID()] = p
}

// GetOnRamp returns an on-ramp provider by ID or an error if not found.
func (r *Registry) GetOnRamp(id string) (domain.OnRampProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.onRamps[id]
	if !ok {
		return nil, fmt.Errorf("settla-rail: on-ramp provider not found: %s", id)
	}
	return p, nil
}

// GetOffRamp returns an off-ramp provider by ID or an error if not found.
func (r *Registry) GetOffRamp(id string) (domain.OffRampProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.offRamps[id]
	if !ok {
		return nil, fmt.Errorf("settla-rail: off-ramp provider not found: %s", id)
	}
	return p, nil
}

// ListOnRampIDs returns the IDs of all registered on-ramp providers.
func (r *Registry) ListOnRampIDs(ctx context.Context) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.onRamps))
	for id := range r.onRamps {
		ids = append(ids, id)
	}
	return ids
}

// ListOffRampIDs returns the IDs of all registered off-ramp providers.
func (r *Registry) ListOffRampIDs(ctx context.Context) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.offRamps))
	for id := range r.offRamps {
		ids = append(ids, id)
	}
	return ids
}

// RegisterBlockchainClient adds a blockchain client to the registry.
func (r *Registry) RegisterBlockchainClient(c domain.BlockchainClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blockchains[c.Chain()] = c
}

// GetBlockchainClient returns a blockchain client by chain ID or an error if not found.
func (r *Registry) GetBlockchainClient(chain domain.CryptoChain) (domain.BlockchainClient, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.blockchains[chain]
	if !ok {
		return nil, fmt.Errorf("settla-rail: blockchain client not found: %s", chain)
	}
	return c, nil
}

// GetBlockchain is an alias for GetBlockchainClient that satisfies domain.ProviderRegistry.
func (r *Registry) GetBlockchain(chain domain.CryptoChain) (domain.BlockchainClient, error) {
	return r.GetBlockchainClient(chain)
}

// RegisterNormalizer adds a webhook normalizer for a provider slug.
func (r *Registry) RegisterNormalizer(slug string, n domain.WebhookNormalizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.normalizers[slug] = n
}

// GetNormalizer returns the webhook normalizer for a provider slug, or nil if not found.
func (r *Registry) GetNormalizer(slug string) domain.WebhookNormalizer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.normalizers[slug]
}

// RegisterListener adds an optional provider listener for non-HTTP communication.
func (r *Registry) RegisterListener(slug string, l domain.ProviderListener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners[slug] = l
}

// GetListener returns the provider listener for a slug, or nil if not registered.
func (r *Registry) GetListener(slug string) domain.ProviderListener {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listeners[slug]
}

// Listeners returns all registered provider listeners.
func (r *Registry) Listeners() map[string]domain.ProviderListener {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]domain.ProviderListener, len(r.listeners))
	for k, v := range r.listeners {
		result[k] = v
	}
	return result
}

// ListBlockchainChains returns the chain IDs of all registered blockchain clients.
func (r *Registry) ListBlockchainChains() []domain.CryptoChain {
	r.mu.RLock()
	defer r.mu.RUnlock()
	chains := make([]domain.CryptoChain, 0, len(r.blockchains))
	for chain := range r.blockchains {
		chains = append(chains, chain)
	}
	return chains
}

// StablecoinsFromProviders returns the set of stablecoin currencies that
// registered providers can handle. Discovered from on-ramp output currencies
// (pair.To) and off-ramp input currencies (pair.From) that satisfy IsStablecoin.
func (r *Registry) StablecoinsFromProviders(ctx context.Context) []domain.Currency {
	seen := make(map[domain.Currency]bool)

	for _, id := range r.ListOnRampIDs(ctx) {
		p, err := r.GetOnRamp(id)
		if err != nil {
			continue
		}
		for _, pair := range p.SupportedPairs() {
			if domain.IsStablecoin(pair.To) {
				seen[pair.To] = true
			}
		}
	}

	for _, id := range r.ListOffRampIDs(ctx) {
		p, err := r.GetOffRamp(id)
		if err != nil {
			continue
		}
		for _, pair := range p.SupportedPairs() {
			if domain.IsStablecoin(pair.From) {
				seen[pair.From] = true
			}
		}
	}

	result := make([]domain.Currency, 0, len(seen))
	for c := range seen {
		result = append(result, c)
	}
	return result
}
