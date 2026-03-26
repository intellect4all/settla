package mock

import (
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider/factory"
)

func init() {
	mock := []factory.ProviderMode{factory.ModeMock}

	factory.RegisterOnRampFactory("mock-onramp-gbp", mock, newMockOnRampGBP)
	factory.RegisterOnRampFactory("mock-onramp-ngn", mock, newMockOnRampNGN)
	factory.RegisterOffRampFactory("mock-offramp-ngn", mock, newMockOffRampNGN)
	factory.RegisterOffRampFactory("mock-offramp-gbp", mock, newMockOffRampGBP)
	factory.RegisterBlockchainFactory("mock-tron", mock, newMockTron)

	// All mock providers share the same webhook normalizer.
	factory.RegisterNormalizerFactory("mock-onramp-gbp", mock, newMockNormalizer)
	factory.RegisterNormalizerFactory("mock-onramp-ngn", mock, newMockNormalizer)
	factory.RegisterNormalizerFactory("mock-offramp-ngn", mock, newMockNormalizer)
	factory.RegisterNormalizerFactory("mock-offramp-gbp", mock, newMockNormalizer)
}

func mockDelay(cfg factory.ProviderConfig) time.Duration {
	if ms, ok := cfg.Extra["DELAY_MS"]; ok {
		if n, err := strconv.Atoi(ms); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 500 * time.Millisecond
}

func newMockOnRampGBP(_ factory.Deps, cfg factory.ProviderConfig) (domain.OnRampProvider, error) {
	return NewOnRampProvider("mock-onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(1.25), decimal.NewFromFloat(0.50), mockDelay(cfg),
	), nil
}

func newMockOnRampNGN(_ factory.Deps, cfg factory.ProviderConfig) (domain.OnRampProvider, error) {
	return NewOnRampProvider("mock-onramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyNGN, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(0.00065), decimal.NewFromFloat(0.50), mockDelay(cfg),
	), nil
}

func newMockOffRampNGN(_ factory.Deps, cfg factory.ProviderConfig) (domain.OffRampProvider, error) {
	return NewOffRampProvider("mock-offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		decimal.NewFromFloat(1550), decimal.NewFromFloat(0.50), mockDelay(cfg),
	), nil
}

func newMockOffRampGBP(_ factory.Deps, cfg factory.ProviderConfig) (domain.OffRampProvider, error) {
	return NewOffRampProvider("mock-offramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyGBP}},
		decimal.NewFromFloat(0.80), decimal.NewFromFloat(0.30), mockDelay(cfg),
	), nil
}

func newMockTron(_ factory.Deps, _ factory.ProviderConfig) (domain.BlockchainClient, error) {
	return NewBlockchainClient("tron", decimal.NewFromFloat(0.10)), nil
}

func newMockNormalizer(_ factory.Deps, _ factory.ProviderConfig) (domain.WebhookNormalizer, error) {
	return &Normalizer{}, nil
}
