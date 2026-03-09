package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"
)

const (
	defaultBaseBackoff  = 2 * time.Second
	defaultMaxAttempts  = 3
	defaultRPCTimeout   = 30 * time.Second
	defaultRateLimit    = 25  // requests per second per endpoint
	defaultBurstLimit   = 50  // token-bucket burst
	defaultMaxFailures  = 5   // failures before circuit opens
	defaultResetTimeout = 30 * time.Second
)

// FailoverManager manages multiple RPC endpoint URLs with automatic failover.
//
// Each endpoint has an independent CircuitBreaker and RateLimiter. Execute()
// routes calls to a healthy endpoint, advancing to the next on failure, with
// configurable retry count and exponential backoff.
type FailoverManager struct {
	mu              sync.Mutex
	endpoints       []string
	circuitBreakers map[string]*CircuitBreaker
	rateLimiters    map[string]*RateLimiter
	healthScores    map[string]float64
	currentIdx      int
	baseBackoff     time.Duration
	logger          *slog.Logger
}

// NewFailoverManager creates a FailoverManager with default settings.
// Each endpoint gets a circuit breaker (5 failures, 30s reset) and
// rate limiter (25 req/s, burst 50).
func NewFailoverManager(endpoints []string, logger *slog.Logger) *FailoverManager {
	return newFailoverManager(endpoints, logger, defaultBaseBackoff)
}

// NewFailoverManagerForTest creates a FailoverManager with a custom base backoff.
// Use in tests to avoid real sleep delays (pass 0 for no backoff).
func NewFailoverManagerForTest(endpoints []string, logger *slog.Logger, baseBackoff time.Duration) *FailoverManager {
	return newFailoverManager(endpoints, logger, baseBackoff)
}

// newFailoverManager is the internal constructor allowing a custom base backoff.
func newFailoverManager(endpoints []string, logger *slog.Logger, baseBackoff time.Duration) *FailoverManager {
	if logger == nil {
		logger = slog.Default()
	}

	fm := &FailoverManager{
		endpoints:       endpoints,
		circuitBreakers: make(map[string]*CircuitBreaker, len(endpoints)),
		rateLimiters:    make(map[string]*RateLimiter, len(endpoints)),
		healthScores:    make(map[string]float64, len(endpoints)),
		baseBackoff:     baseBackoff,
		logger:          logger,
	}

	for _, ep := range endpoints {
		fm.circuitBreakers[ep] = NewCircuitBreaker(defaultMaxFailures, defaultResetTimeout)
		fm.rateLimiters[ep] = NewRateLimiter(defaultRateLimit, defaultBurstLimit)
		fm.healthScores[ep] = 1.0
	}

	return fm
}

// Execute calls fn with a healthy endpoint URL, retrying on failure.
//
// Behaviour:
//   - Up to 3 total attempts.
//   - On each attempt, the healthiest available endpoint is selected.
//   - After a failed attempt, the circuit breaker for that endpoint records
//     the failure and the manager advances to the next endpoint.
//   - If no healthy endpoint is found, the manager sleeps for the current
//     backoff duration before the next attempt (2s, 4s).
//   - Each fn invocation receives a context with a 30-second deadline.
//
// fn must respect context cancellation for timeouts to take effect.
func (fm *FailoverManager) Execute(ctx context.Context, fn func(ctx context.Context, endpoint string) error) error {
	if len(fm.endpoints) == 0 {
		return fmt.Errorf("settla-rpc: no endpoints configured")
	}

	var lastErr error
	backoff := fm.baseBackoff

	for attempt := 0; attempt < defaultMaxAttempts; attempt++ {
		// Respect context cancellation between attempts.
		if err := ctx.Err(); err != nil {
			return err
		}

		ep, ok := fm.pickHealthyEndpoint()
		if !ok {
			lastErr = fmt.Errorf("no healthy endpoints available")
			fm.logger.Warn("settla-rpc: all endpoints unhealthy, backing off",
				"attempt", attempt, "backoff", backoff)
			if attempt < defaultMaxAttempts-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff *= 2
			}
			continue
		}

		callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
		start := time.Now()
		err := fn(callCtx, ep)
		latency := time.Since(start)
		cancel()

		if err == nil {
			fm.recordSuccess(ep, latency)
			return nil
		}

		lastErr = err
		fm.recordFailure(ep)
		fm.logger.Warn("settla-rpc: endpoint failed, trying next",
			"endpoint", ep, "attempt", attempt, "error", err)

		// Advance past the failing endpoint before the next attempt.
		fm.advance()
	}

	return fmt.Errorf("settla-rpc: all %d attempts failed: %w", defaultMaxAttempts, lastErr)
}

// HealthScores returns a snapshot of the current health scores (0.0–1.0).
func (fm *FailoverManager) HealthScores() map[string]float64 {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	out := make(map[string]float64, len(fm.healthScores))
	maps.Copy(out, fm.healthScores)
	return out
}

// pickHealthyEndpoint returns the URL of a healthy endpoint (circuit closed,
// rate limiter allows), cycling through all endpoints to find one.
// Returns ("", false) if no endpoint is currently available.
func (fm *FailoverManager) pickHealthyEndpoint() (string, bool) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	for i := 0; i < len(fm.endpoints); i++ {
		ep := fm.endpoints[fm.currentIdx]
		cb := fm.circuitBreakers[ep]
		rl := fm.rateLimiters[ep]
		if cb.CanAttempt() && rl.Allow() {
			return ep, true
		}
		fm.currentIdx = (fm.currentIdx + 1) % len(fm.endpoints)
	}
	return "", false
}

// advance moves the current index to the next endpoint.
func (fm *FailoverManager) advance() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.currentIdx = (fm.currentIdx + 1) % len(fm.endpoints)
}

// recordSuccess updates the circuit breaker and health score for ep.
func (fm *FailoverManager) recordSuccess(ep string, latency time.Duration) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.circuitBreakers[ep].RecordSuccess()
	if latency < 500*time.Millisecond {
		fm.healthScores[ep] = min(1.0, fm.healthScores[ep]+0.1)
	}
}

// recordFailure updates the circuit breaker and health score for ep.
func (fm *FailoverManager) recordFailure(ep string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.circuitBreakers[ep].RecordFailure()
	fm.healthScores[ep] = max(0.0, fm.healthScores[ep]-0.2)
}
