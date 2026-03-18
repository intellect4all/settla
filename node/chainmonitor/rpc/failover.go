package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Provider represents a single RPC endpoint with circuit breaker state.
type Provider struct {
	Name     string
	RPCURL   string
	APIKey   string
	failures atomic.Int64
	openedAt atomic.Int64 // unix millis when circuit opened; 0 = closed
}

const (
	// failureThreshold is the number of consecutive failures before opening the circuit.
	failureThreshold = 5
	// resetTimeout is how long a circuit stays open before entering half-open state.
	resetTimeout = 30 * time.Second
)

// IsAvailable returns true if the provider's circuit breaker allows requests.
func (p *Provider) IsAvailable() bool {
	opened := p.openedAt.Load()
	if opened == 0 {
		return true // circuit closed
	}
	// half-open: allow if reset timeout has elapsed
	return time.Since(time.UnixMilli(opened)) >= resetTimeout
}

// RecordSuccess resets the failure counter and closes the circuit.
func (p *Provider) RecordSuccess() {
	p.failures.Store(0)
	p.openedAt.Store(0)
}

// RecordFailure increments failures and opens the circuit if threshold is reached.
func (p *Provider) RecordFailure() {
	f := p.failures.Add(1)
	if f >= int64(failureThreshold) {
		p.openedAt.Store(time.Now().UnixMilli())
	}
}

// FailoverManager manages multiple RPC providers with circuit breaker failover.
// It tries providers in order, skipping those with open circuits.
type FailoverManager struct {
	providers []*Provider
	mu        sync.RWMutex
	logger    *slog.Logger
}

// NewFailoverManager creates a failover manager from the given providers.
func NewFailoverManager(providers []*Provider, logger *slog.Logger) *FailoverManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &FailoverManager{
		providers: providers,
		logger:    logger,
	}
}

// Execute tries the operation against each available provider in order.
// The operation receives the provider's RPC URL and API key.
// Returns the first successful result, or the last error if all providers fail.
func (fm *FailoverManager) Execute(ctx context.Context, op func(ctx context.Context, rpcURL, apiKey string) error) error {
	fm.mu.RLock()
	providers := fm.providers
	fm.mu.RUnlock()

	var lastErr error
	for _, p := range providers {
		if !p.IsAvailable() {
			fm.logger.Debug("settla-rpc: skipping unavailable provider",
				"provider", p.Name,
				"failures", p.failures.Load(),
			)
			continue
		}

		if err := op(ctx, p.RPCURL, p.APIKey); err != nil {
			p.RecordFailure()
			lastErr = err
			fm.logger.Warn("settla-rpc: provider failed, trying next",
				"provider", p.Name,
				"error", err,
			)
			continue
		}

		p.RecordSuccess()
		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("settla-rpc: all providers failed: %w", lastErr)
	}
	return fmt.Errorf("settla-rpc: no available providers")
}

// AvailableCount returns the number of providers with closed or half-open circuits.
func (fm *FailoverManager) AvailableCount() int {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	count := 0
	for _, p := range fm.providers {
		if p.IsAvailable() {
			count++
		}
	}
	return count
}
