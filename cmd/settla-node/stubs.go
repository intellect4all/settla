package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/core/recovery"
)

// stubProviderStatusChecker is used until real provider status checks are wired.
// The recovery detector handles "unknown" status by skipping provider-driven recovery
// and relying on time-threshold-based escalation.
type stubProviderStatusChecker struct{}

var _ recovery.ProviderStatusChecker = (*stubProviderStatusChecker)(nil)

func (s *stubProviderStatusChecker) CheckOnRampStatus(_ context.Context, _ string, _ uuid.UUID) (*recovery.ProviderStatus, error) {
	return &recovery.ProviderStatus{Status: "unknown"}, nil
}

func (s *stubProviderStatusChecker) CheckOffRampStatus(_ context.Context, _ string, _ uuid.UUID) (*recovery.ProviderStatus, error) {
	return &recovery.ProviderStatus{Status: "unknown"}, nil
}

func (s *stubProviderStatusChecker) CheckBlockchainStatus(_ context.Context, _ string, _ string) (*recovery.ChainStatus, error) {
	return &recovery.ChainStatus{Confirmed: false}, nil
}
