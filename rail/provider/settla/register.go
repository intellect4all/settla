package settla

import (
	"fmt"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider/factory"
)

func init() {
	testnet := []factory.ProviderMode{factory.ModeTestnet}

	factory.RegisterOnRampFactory("settla-onramp", testnet, newSettlaOnRamp)
	factory.RegisterOffRampFactory("settla-offramp", testnet, newSettlaOffRamp)
	factory.RegisterNormalizerFactory("settla-onramp", testnet, newSettlaNormalizer)
	factory.RegisterNormalizerFactory("settla-offramp", testnet, newSettlaNormalizer)
}

func newSettlaOnRamp(deps factory.Deps, _ factory.ProviderConfig) (domain.OnRampProvider, error) {
	if deps.BlockchainReg == nil {
		return nil, fmt.Errorf("settla-onramp: blockchain registry is required in testnet mode")
	}

	fxOracle := NewFXOracle()
	fiatSim := NewFiatSimulator(DefaultSimulatorConfig())
	cfg := DefaultOnRampConfig()
	cfg.Logger = deps.Logger

	return NewOnRampProvider(fxOracle, fiatSim, deps.BlockchainReg, nil, cfg), nil
}

func newSettlaOffRamp(deps factory.Deps, _ factory.ProviderConfig) (domain.OffRampProvider, error) {
	if deps.BlockchainReg == nil {
		return nil, fmt.Errorf("settla-offramp: blockchain registry is required in testnet mode")
	}

	fxOracle := NewFXOracle()
	fiatSim := NewFiatSimulator(DefaultSimulatorConfig())

	return NewOffRampProvider(fxOracle, fiatSim, deps.BlockchainReg, nil, deps.Logger), nil
}

func newSettlaNormalizer(_ factory.Deps, _ factory.ProviderConfig) (domain.WebhookNormalizer, error) {
	return &Normalizer{}, nil
}
