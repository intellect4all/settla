package factory

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/intellect4all/settla/domain"
)

// registerWithNormalizer is a test helper that registers a provider factory
// along with the required normalizer.
func registerOnRampWithNormalizer(name string, modes []ProviderMode) {
	RegisterOnRampFactory(name, modes, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: name}, nil
	})
	RegisterNormalizerFactory(name, modes, func(_ Deps, _ ProviderConfig) (domain.WebhookNormalizer, error) {
		return &fakeNormalizer{}, nil
	})
}

func registerOffRampWithNormalizer(name string, modes []ProviderMode) {
	RegisterOffRampFactory(name, modes, func(_ Deps, _ ProviderConfig) (domain.OffRampProvider, error) {
		return &fakeOffRamp{id: name}, nil
	})
	RegisterNormalizerFactory(name, modes, func(_ Deps, _ ProviderConfig) (domain.WebhookNormalizer, error) {
		return &fakeNormalizer{}, nil
	})
}

func TestBootstrap_MockMode(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	registerOnRampWithNormalizer("test-onramp", []ProviderMode{ModeMock})
	registerOffRampWithNormalizer("test-offramp", []ProviderMode{ModeMock})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result, err := Bootstrap(ModeMock, Deps{Logger: logger})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(result.OnRamps) != 1 {
		t.Fatalf("expected 1 on-ramp, got %d", len(result.OnRamps))
	}
	if _, ok := result.OnRamps["test-onramp"]; !ok {
		t.Fatal("expected test-onramp in result")
	}

	if len(result.OffRamps) != 1 {
		t.Fatalf("expected 1 off-ramp, got %d", len(result.OffRamps))
	}

	if len(result.Configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(result.Configs))
	}
}

func TestBootstrap_SkipsDisabledProviders(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	registerOnRampWithNormalizer("enabled-provider", []ProviderMode{ModeMock})
	// Register disabled provider — its factory should never be called.
	RegisterOnRampFactory("disabled-provider", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		t.Fatal("disabled provider factory should not be called")
		return nil, nil
	})
	RegisterNormalizerFactory("disabled-provider", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.WebhookNormalizer, error) {
		return &fakeNormalizer{}, nil
	})

	// Disable via env.
	t.Setenv("SETTLA_PROVIDER_DISABLED_PROVIDER_ENABLED", "false")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result, err := Bootstrap(ModeMock, Deps{Logger: logger})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(result.OnRamps) != 1 {
		t.Fatalf("expected 1 on-ramp (disabled should be skipped), got %d", len(result.OnRamps))
	}
	if _, ok := result.OnRamps["enabled-provider"]; !ok {
		t.Fatal("expected enabled-provider in result")
	}
}

func TestBootstrap_ModeFilteringExcludesWrongMode(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	RegisterOnRampFactory("testnet-only", []ProviderMode{ModeTestnet}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		t.Fatal("testnet-only factory should not be called in mock mode")
		return nil, nil
	})
	RegisterNormalizerFactory("testnet-only", []ProviderMode{ModeTestnet}, func(_ Deps, _ ProviderConfig) (domain.WebhookNormalizer, error) {
		return &fakeNormalizer{}, nil
	})
	registerOnRampWithNormalizer("mock-provider", []ProviderMode{ModeMock})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result, err := Bootstrap(ModeMock, Deps{Logger: logger})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(result.OnRamps) != 1 {
		t.Fatalf("expected 1 on-ramp, got %d", len(result.OnRamps))
	}
}

func TestBootstrap_RequiresLogger(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	_, err := Bootstrap(ModeMock, Deps{})
	if err == nil {
		t.Fatal("expected error when Logger is nil")
	}
}

func TestBootstrap_FailsWithoutNormalizer(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	// Register an on-ramp factory WITHOUT a matching normalizer.
	RegisterOnRampFactory("orphan-onramp", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "orphan-onramp"}, nil
	})
	// No RegisterNormalizerFactory for "orphan-onramp"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := Bootstrap(ModeMock, Deps{Logger: logger})
	if err == nil {
		t.Fatal("expected error when on-ramp has no normalizer")
	}
	if !strings.Contains(err.Error(), "no registered WebhookNormalizer") {
		t.Errorf("error should mention missing normalizer, got: %v", err)
	}
}

func TestBootstrap_FailsWithoutNormalizerForOffRamp(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	RegisterOffRampFactory("orphan-offramp", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OffRampProvider, error) {
		return &fakeOffRamp{id: "orphan-offramp"}, nil
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := Bootstrap(ModeMock, Deps{Logger: logger})
	if err == nil {
		t.Fatal("expected error when off-ramp has no normalizer")
	}
}

func TestBootstrap_WithNormalizerSucceeds(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	RegisterOnRampFactory("good-provider", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.OnRampProvider, error) {
		return &fakeOnRamp{id: "good-provider"}, nil
	})
	RegisterNormalizerFactory("good-provider", []ProviderMode{ModeMock}, func(_ Deps, _ ProviderConfig) (domain.WebhookNormalizer, error) {
		return &fakeNormalizer{}, nil
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result, err := Bootstrap(ModeMock, Deps{Logger: logger})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if len(result.OnRamps) != 1 {
		t.Fatalf("expected 1 on-ramp, got %d", len(result.OnRamps))
	}
	if len(result.Normalizers) != 1 {
		t.Fatalf("expected 1 normalizer, got %d", len(result.Normalizers))
	}
}

// fakeNormalizer implements domain.WebhookNormalizer for testing.
type fakeNormalizer struct{}

func (f *fakeNormalizer) NormalizeWebhook(_ string, _ []byte) (*domain.ProviderWebhookPayload, error) {
	return nil, nil
}
