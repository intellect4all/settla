package mockhttp

import (
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider/factory"
)

func init() {
	modes := []factory.ProviderMode{factory.ModeMockHTTP}

	// On-ramp factories.
	factory.RegisterOnRampFactory("mock-onramp-gbp", modes, newOnRampGBP)
	factory.RegisterOnRampFactory("mock-onramp-ngn", modes, newOnRampNGN)

	// Off-ramp factories.
	factory.RegisterOffRampFactory("mock-offramp-ngn", modes, newOffRampNGN)
	factory.RegisterOffRampFactory("mock-offramp-gbp", modes, newOffRampGBP)

	// Blockchain factory.
	factory.RegisterBlockchainFactory("mock-tron", modes, newTron)

	// Normalizers (required for every on-ramp and off-ramp).
	factory.RegisterNormalizerFactory("mock-onramp-gbp", modes, newNormalizer)
	factory.RegisterNormalizerFactory("mock-onramp-ngn", modes, newNormalizer)
	factory.RegisterNormalizerFactory("mock-offramp-ngn", modes, newNormalizer)
	factory.RegisterNormalizerFactory("mock-offramp-gbp", modes, newNormalizer)
}

func clientFromConfig(cfg factory.ProviderConfig) *Client {
	url := cfg.Extra["MOCKPROVIDER_URL"]
	return NewClient(url)
}

func newOnRampGBP(_ factory.Deps, cfg factory.ProviderConfig) (domain.OnRampProvider, error) {
	return NewOnRampProvider("mock-onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		clientFromConfig(cfg),
	), nil
}

func newOnRampNGN(_ factory.Deps, cfg factory.ProviderConfig) (domain.OnRampProvider, error) {
	return NewOnRampProvider("mock-onramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyNGN, To: domain.CurrencyUSDT}},
		clientFromConfig(cfg),
	), nil
}

func newOffRampNGN(_ factory.Deps, cfg factory.ProviderConfig) (domain.OffRampProvider, error) {
	return NewOffRampProvider("mock-offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		clientFromConfig(cfg),
	), nil
}

func newOffRampGBP(_ factory.Deps, cfg factory.ProviderConfig) (domain.OffRampProvider, error) {
	return NewOffRampProvider("mock-offramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyGBP}},
		clientFromConfig(cfg),
	), nil
}

func newTron(_ factory.Deps, cfg factory.ProviderConfig) (domain.BlockchainClient, error) {
	return NewBlockchainClient(domain.ChainTron, clientFromConfig(cfg)), nil
}

func newNormalizer(_ factory.Deps, _ factory.ProviderConfig) (domain.WebhookNormalizer, error) {
	return &Normalizer{}, nil
}
