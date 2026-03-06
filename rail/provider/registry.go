package provider

import (
	"context"
	"fmt"
	"sync"

	"github.com/intellect4all/settla/domain"
)

// Registry manages the set of available on-ramp, off-ramp, and blockchain providers.
type Registry struct {
	mu          sync.RWMutex
	onRamps     map[string]domain.OnRampProvider
	offRamps    map[string]domain.OffRampProvider
	blockchains map[string]domain.BlockchainClient
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		onRamps:     make(map[string]domain.OnRampProvider),
		offRamps:    make(map[string]domain.OffRampProvider),
		blockchains: make(map[string]domain.BlockchainClient),
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
func (r *Registry) GetBlockchainClient(chain string) (domain.BlockchainClient, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.blockchains[chain]
	if !ok {
		return nil, fmt.Errorf("settla-rail: blockchain client not found: %s", chain)
	}
	return c, nil
}

// GetBlockchain is an alias for GetBlockchainClient that satisfies router.ProviderRegistry.
func (r *Registry) GetBlockchain(chain string) (domain.BlockchainClient, error) {
	return r.GetBlockchainClient(chain)
}

// ListBlockchainChains returns the chain IDs of all registered blockchain clients.
func (r *Registry) ListBlockchainChains() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	chains := make([]string, 0, len(r.blockchains))
	for chain := range r.blockchains {
		chains = append(chains, chain)
	}
	return chains
}
