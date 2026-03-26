// Package factory provides a self-registration mechanism for payment providers.
//
// Each provider package registers factory functions at init() time. At startup,
// Bootstrap() reads config, builds shared dependencies, and calls each factory
// to populate a provider Registry. This is the standard Go pattern used by
// database/sql, image/png, etc.
//
// Adding a new provider requires:
//  1. Create a package (e.g., rail/provider/flutterwave/)
//  2. Implement domain.OnRampProvider / domain.OffRampProvider
//  3. Add a register.go with init() calling RegisterOnRampFactory / RegisterOffRampFactory
//  4. Add a blank import in rail/provider/all/all.go
//  5. Set env config (SETTLA_PROVIDER_FLUTTERWAVE_ENABLED=true, etc.)
package factory

import (
	"sync"

	"github.com/intellect4all/settla/domain"
)

// ProviderMode controls which factories are activated at bootstrap.
type ProviderMode string

const (
	ModeMock     ProviderMode = "mock"
	ModeMockHTTP ProviderMode = "mock-http"
	ModeTestnet  ProviderMode = "testnet"
	ModeLive     ProviderMode = "live"
)

// OnRampFactory creates an OnRampProvider from shared dependencies and config.
type OnRampFactory func(deps Deps, cfg ProviderConfig) (domain.OnRampProvider, error)

// OffRampFactory creates an OffRampProvider from shared dependencies and config.
type OffRampFactory func(deps Deps, cfg ProviderConfig) (domain.OffRampProvider, error)

// BlockchainFactory creates a BlockchainClient from shared dependencies and config.
type BlockchainFactory func(deps Deps, cfg ProviderConfig) (domain.BlockchainClient, error)

// NormalizerFactory creates a WebhookNormalizer for a provider.
// Every on-ramp and off-ramp provider MUST register a normalizer — without it,
// Settla cannot process the provider's inbound webhooks.
type NormalizerFactory func(deps Deps, cfg ProviderConfig) (domain.WebhookNormalizer, error)

// ListenerFactory creates an optional ProviderListener for providers that
// communicate via non-HTTP channels (WebSocket, gRPC streaming, polling).
// Providers using HTTP webhooks do not need this.
type ListenerFactory func(deps Deps, cfg ProviderConfig) (domain.ProviderListener, error)

type registration[F any] struct {
	Modes   []ProviderMode
	Factory F
}

var (
	mu                  sync.Mutex
	onRampFactories     = map[string]registration[OnRampFactory]{}
	offRampFactories    = map[string]registration[OffRampFactory]{}
	blockchainFactories = map[string]registration[BlockchainFactory]{}
	normalizerFactories = map[string]registration[NormalizerFactory]{}
	listenerFactories   = map[string]registration[ListenerFactory]{}
)

// RegisterOnRampFactory registers a factory that creates an OnRampProvider.
// modes specifies which provider modes (mock, testnet, live) this factory applies to.
// Called from init() in provider packages.
func RegisterOnRampFactory(name string, modes []ProviderMode, f OnRampFactory) {
	mu.Lock()
	defer mu.Unlock()
	onRampFactories[name] = registration[OnRampFactory]{Modes: modes, Factory: f}
}

// RegisterOffRampFactory registers a factory that creates an OffRampProvider.
func RegisterOffRampFactory(name string, modes []ProviderMode, f OffRampFactory) {
	mu.Lock()
	defer mu.Unlock()
	offRampFactories[name] = registration[OffRampFactory]{Modes: modes, Factory: f}
}

// RegisterBlockchainFactory registers a factory that creates a BlockchainClient.
func RegisterBlockchainFactory(name string, modes []ProviderMode, f BlockchainFactory) {
	mu.Lock()
	defer mu.Unlock()
	blockchainFactories[name] = registration[BlockchainFactory]{Modes: modes, Factory: f}
}

// RegisterNormalizerFactory registers a webhook normalizer factory for a provider.
// REQUIRED for every on-ramp and off-ramp provider. Bootstrap will fail if a
// provider registers an on-ramp or off-ramp factory without a matching normalizer.
func RegisterNormalizerFactory(name string, modes []ProviderMode, f NormalizerFactory) {
	mu.Lock()
	defer mu.Unlock()
	normalizerFactories[name] = registration[NormalizerFactory]{Modes: modes, Factory: f}
}

// RegisterListenerFactory registers an optional listener factory for providers
// that communicate via non-HTTP channels (WebSocket, gRPC streaming, polling).
func RegisterListenerFactory(name string, modes []ProviderMode, f ListenerFactory) {
	mu.Lock()
	defer mu.Unlock()
	listenerFactories[name] = registration[ListenerFactory]{Modes: modes, Factory: f}
}

// matchesMode returns true if the registration applies to the given mode.
func matchesMode(modes []ProviderMode, target ProviderMode) bool {
	for _, m := range modes {
		if m == target {
			return true
		}
	}
	return false
}

// OnRampFactories returns all registered on-ramp factories matching the mode.
func OnRampFactories(mode ProviderMode) map[string]OnRampFactory {
	mu.Lock()
	defer mu.Unlock()
	result := make(map[string]OnRampFactory)
	for name, reg := range onRampFactories {
		if matchesMode(reg.Modes, mode) {
			result[name] = reg.Factory
		}
	}
	return result
}

// OffRampFactories returns all registered off-ramp factories matching the mode.
func OffRampFactories(mode ProviderMode) map[string]OffRampFactory {
	mu.Lock()
	defer mu.Unlock()
	result := make(map[string]OffRampFactory)
	for name, reg := range offRampFactories {
		if matchesMode(reg.Modes, mode) {
			result[name] = reg.Factory
		}
	}
	return result
}

// BlockchainFactories returns all registered blockchain factories matching the mode.
func BlockchainFactories(mode ProviderMode) map[string]BlockchainFactory {
	mu.Lock()
	defer mu.Unlock()
	result := make(map[string]BlockchainFactory)
	for name, reg := range blockchainFactories {
		if matchesMode(reg.Modes, mode) {
			result[name] = reg.Factory
		}
	}
	return result
}

// NormalizerFactories returns all registered normalizer factories matching the mode.
func NormalizerFactories(mode ProviderMode) map[string]NormalizerFactory {
	mu.Lock()
	defer mu.Unlock()
	result := make(map[string]NormalizerFactory)
	for name, reg := range normalizerFactories {
		if matchesMode(reg.Modes, mode) {
			result[name] = reg.Factory
		}
	}
	return result
}

// ListenerFactories returns all registered listener factories matching the mode.
func ListenerFactories(mode ProviderMode) map[string]ListenerFactory {
	mu.Lock()
	defer mu.Unlock()
	result := make(map[string]ListenerFactory)
	for name, reg := range listenerFactories {
		if matchesMode(reg.Modes, mode) {
			result[name] = reg.Factory
		}
	}
	return result
}

// ResetForTesting clears all registered factories. Only for use in tests.
func ResetForTesting() {
	mu.Lock()
	defer mu.Unlock()
	onRampFactories = map[string]registration[OnRampFactory]{}
	offRampFactories = map[string]registration[OffRampFactory]{}
	blockchainFactories = map[string]registration[BlockchainFactory]{}
	normalizerFactories = map[string]registration[NormalizerFactory]{}
	listenerFactories = map[string]registration[ListenerFactory]{}
}
