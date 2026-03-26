package factory

import (
	"fmt"

	"github.com/intellect4all/settla/domain"
)

// BootstrapResult holds the providers and configs created by Bootstrap.
// The caller registers these into a provider.Registry.
type BootstrapResult struct {
	OnRamps     map[string]domain.OnRampProvider
	OffRamps    map[string]domain.OffRampProvider
	Blockchains []domain.BlockchainClient
	Normalizers map[string]domain.WebhookNormalizer // keyed by provider slug
	Listeners   map[string]domain.ProviderListener  // keyed by provider slug (optional)
	Configs     map[string]ProviderConfig           // keyed by provider ID
}

// Bootstrap constructs all providers for the given mode by calling registered
// factories with shared dependencies and per-provider config loaded from env.
//
// Disabled providers (SETTLA_PROVIDER_{ID}_ENABLED=false) are skipped.
// Returns an error if any enabled on-ramp or off-ramp provider does not have
// a matching normalizer registered — normalizers are required.
func Bootstrap(mode ProviderMode, deps Deps) (*BootstrapResult, error) {
	if deps.Logger == nil {
		return nil, fmt.Errorf("factory: deps.Logger is required")
	}
	logger := deps.Logger.With("module", "factory.bootstrap")

	result := &BootstrapResult{
		OnRamps:     make(map[string]domain.OnRampProvider),
		OffRamps:    make(map[string]domain.OffRampProvider),
		Normalizers: make(map[string]domain.WebhookNormalizer),
		Listeners:   make(map[string]domain.ProviderListener),
		Configs:     make(map[string]ProviderConfig),
	}

	// Build normalizers first — we need them to validate provider registrations.
	normFactories := NormalizerFactories(mode)
	for name, f := range normFactories {
		cfg := LoadProviderConfig(name)
		if !cfg.Enabled {
			continue
		}
		n, err := f(deps, cfg)
		if err != nil {
			return nil, fmt.Errorf("settla-factory: normalizer %q: %w", name, err)
		}
		result.Normalizers[name] = n
		logger.Info("settla-factory: registered normalizer", "provider", name)
	}

	// On-ramps
	for name, f := range OnRampFactories(mode) {
		cfg := LoadProviderConfig(name)
		if !cfg.Enabled {
			logger.Info("settla-factory: provider disabled, skipping", "provider", name, "type", "onramp")
			continue
		}
		// Enforce: every on-ramp provider must have a normalizer.
		if _, ok := result.Normalizers[name]; !ok {
			return nil, fmt.Errorf("settla-factory: on-ramp %q has no registered WebhookNormalizer — "+
				"call factory.RegisterNormalizerFactory(%q, ...) in your provider's register.go", name, name)
		}
		p, err := f(deps, cfg)
		if err != nil {
			return nil, fmt.Errorf("settla-factory: on-ramp %q: %w", name, err)
		}
		result.OnRamps[p.ID()] = p
		result.Configs[p.ID()] = cfg
		logger.Info("settla-factory: registered on-ramp", "provider", p.ID())
	}

	// Off-ramps
	for name, f := range OffRampFactories(mode) {
		cfg := LoadProviderConfig(name)
		if !cfg.Enabled {
			logger.Info("settla-factory: provider disabled, skipping", "provider", name, "type", "offramp")
			continue
		}
		// Enforce: every off-ramp provider must have a normalizer.
		if _, ok := result.Normalizers[name]; !ok {
			return nil, fmt.Errorf("settla-factory: off-ramp %q has no registered WebhookNormalizer — "+
				"call factory.RegisterNormalizerFactory(%q, ...) in your provider's register.go", name, name)
		}
		p, err := f(deps, cfg)
		if err != nil {
			return nil, fmt.Errorf("settla-factory: off-ramp %q: %w", name, err)
		}
		result.OffRamps[p.ID()] = p
		result.Configs[p.ID()] = cfg
		logger.Info("settla-factory: registered off-ramp", "provider", p.ID())
	}

	// Blockchains (no normalizer required — blockchains don't send webhooks)
	for name, f := range BlockchainFactories(mode) {
		cfg := LoadProviderConfig(name)
		if !cfg.Enabled {
			logger.Info("settla-factory: provider disabled, skipping", "provider", name, "type", "blockchain")
			continue
		}
		c, err := f(deps, cfg)
		if err != nil {
			return nil, fmt.Errorf("settla-factory: blockchain %q: %w", name, err)
		}
		result.Blockchains = append(result.Blockchains, c)
		result.Configs[name] = cfg
		logger.Info("settla-factory: registered blockchain", "chain", c.Chain())
	}

	// Listeners (optional — for WebSocket/polling/gRPC-streaming providers)
	for name, f := range ListenerFactories(mode) {
		cfg := LoadProviderConfig(name)
		if !cfg.Enabled {
			continue
		}
		l, err := f(deps, cfg)
		if err != nil {
			return nil, fmt.Errorf("settla-factory: listener %q: %w", name, err)
		}
		result.Listeners[name] = l
		logger.Info("settla-factory: registered listener", "provider", name)
	}

	logger.Info("settla-factory: bootstrap complete",
		"mode", string(mode),
		"on_ramps", len(result.OnRamps),
		"off_ramps", len(result.OffRamps),
		"blockchains", len(result.Blockchains),
		"normalizers", len(result.Normalizers),
		"listeners", len(result.Listeners),
	)

	return result, nil
}
