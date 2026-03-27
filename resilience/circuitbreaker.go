// Package resilience provides general-purpose resilience primitives:
// circuit breakers, bulkheads, timeout budgets, retries, and load shedding.
//
// Unlike the blockchain-specific circuit breaker in rail/blockchain/rpc,
// these are designed to wrap any external dependency (Postgres, Redis, NATS, etc.)
// and expose Prometheus metrics for observability.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// State represents the circuit breaker state.
type State int

const (
	StateClosed   State = iota // Normal operation; requests flow through.
	StateOpen                  // Failures exceeded threshold; requests are rejected fast.
	StateHalfOpen              // Recovery probe; limited requests allowed to test the dependency.
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen Sentinel errors returned by CircuitBreaker.
var (
	ErrCircuitOpen = errors.New("settla-resilience: circuit breaker is open")
)

// CBOption configures a CircuitBreaker.
type CBOption func(*CircuitBreaker)

// WithFailureThreshold sets the number of consecutive failures before the circuit trips open.
func WithFailureThreshold(n int) CBOption {
	return func(cb *CircuitBreaker) {
		if n > 0 {
			cb.failureThreshold = n
		}
	}
}

// WithResetTimeout sets how long the circuit stays open before transitioning to half-open.
func WithResetTimeout(d time.Duration) CBOption {
	return func(cb *CircuitBreaker) {
		if d > 0 {
			cb.resetTimeout = d
		}
	}
}

// WithHalfOpenMax sets the number of probe requests allowed in the half-open state.
func WithHalfOpenMax(n int) CBOption {
	return func(cb *CircuitBreaker) {
		if n > 0 {
			cb.halfOpenMaxRequests = n
		}
	}
}

// WithSuccessThreshold sets consecutive successes in half-open needed to close the circuit.
func WithSuccessThreshold(n int) CBOption {
	return func(cb *CircuitBreaker) {
		if n > 0 {
			cb.successThreshold = n
		}
	}
}

// WithOnStateChange registers a callback invoked on every state transition.
func WithOnStateChange(fn func(name string, from, to State)) CBOption {
	return func(cb *CircuitBreaker) {
		cb.onStateChange = fn
	}
}

// circuitBreakerMetrics holds Prometheus metrics for a single circuit breaker.
type circuitBreakerMetrics struct {
	state      prometheus.Gauge
	failures   prometheus.Counter
	rejections prometheus.Counter
}

var (
	cbStateGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "settla",
		Subsystem: "circuit_breaker",
		Name:      "state",
		Help:      "Current circuit breaker state (0=closed, 1=open, 2=half_open).",
	}, []string{"name"})

	cbFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "circuit_breaker",
		Name:      "failures_total",
		Help:      "Total failures recorded by the circuit breaker.",
	}, []string{"name"})

	cbRejectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "circuit_breaker",
		Name:      "rejections_total",
		Help:      "Total requests rejected because the circuit was open.",
	}, []string{"name"})
)

// CircuitBreaker implements the circuit breaker pattern for any external dependency.
// It tracks consecutive failures and opens the circuit when a threshold is reached,
// rejecting subsequent calls immediately to avoid cascading failures.
//
// After a reset timeout the circuit transitions to half-open, admitting a limited
// number of probe requests. If enough probes succeed the circuit closes; if any
// probe fails the circuit reopens.
type CircuitBreaker struct {
	name string

	// Configuration (immutable after construction).
	failureThreshold    int
	resetTimeout        time.Duration
	halfOpenMaxRequests int
	successThreshold    int
	onStateChange       func(name string, from, to State)

	// Mutable state, protected by mu.
	mu                  sync.Mutex
	state               State
	consecutiveFailures int
	consecutiveSuccess  int
	halfOpenRequests    int
	lastFailureTime     time.Time

	metrics circuitBreakerMetrics
}

// NewCircuitBreaker creates a circuit breaker with the given name and options.
// Defaults: failureThreshold=5, resetTimeout=10s, halfOpenMaxRequests=3, successThreshold=2.
func NewCircuitBreaker(name string, opts ...CBOption) *CircuitBreaker {
	cb := &CircuitBreaker{
		name:                name,
		failureThreshold:    5,
		resetTimeout:        10 * time.Second,
		halfOpenMaxRequests: 3,
		successThreshold:    2,
		state:               StateClosed,
	}
	for _, opt := range opts {
		opt(cb)
	}

	cb.metrics = circuitBreakerMetrics{
		state:      cbStateGauge.WithLabelValues(name),
		failures:   cbFailuresTotal.WithLabelValues(name),
		rejections: cbRejectionsTotal.WithLabelValues(name),
	}
	cb.metrics.state.Set(0)

	return cb
}

// Execute wraps a function call with circuit breaker protection.
// If the circuit is open, it returns ErrCircuitOpen immediately.
// Context cancellation is respected.
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("settla-resilience: circuit breaker %s: %w", cb.name, err)
	}

	if !cb.allowRequest() {
		cb.metrics.rejections.Inc()
		return ErrCircuitOpen
	}

	err := fn(ctx)
	if err != nil {
		cb.recordFailure()
		return err
	}

	cb.recordSuccess()
	return nil
}

// GetState returns the current circuit breaker state.
func (cb *CircuitBreaker) GetState() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentStateLocked()
}

// currentStateLocked returns the effective state, handling the open->half-open
// transition when the reset timeout has elapsed. Caller must hold cb.mu.
func (cb *CircuitBreaker) currentStateLocked() State {
	if cb.state == StateOpen && time.Since(cb.lastFailureTime) >= cb.resetTimeout {
		cb.transitionLocked(StateHalfOpen)
	}
	return cb.state
}

// allowRequest decides whether a request should be allowed through.
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.currentStateLocked()
	switch state {
	case StateClosed:
		return true
	case StateOpen:
		return false
	case StateHalfOpen:
		if cb.halfOpenRequests < cb.halfOpenMaxRequests {
			cb.halfOpenRequests++
			return true
		}
		return false
	default:
		return false
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures = 0

	switch cb.state {
	case StateHalfOpen:
		cb.consecutiveSuccess++
		if cb.consecutiveSuccess >= cb.successThreshold {
			cb.transitionLocked(StateClosed)
		}
	case StateClosed:
		// Already healthy, nothing to do.
	}
}

func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures++
	cb.consecutiveSuccess = 0
	cb.metrics.failures.Inc()
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		if cb.consecutiveFailures >= cb.failureThreshold {
			cb.transitionLocked(StateOpen)
		}
	case StateHalfOpen:
		// Any failure in half-open immediately reopens.
		cb.transitionLocked(StateOpen)
	}
}

// transitionLocked changes the circuit state and fires the callback. Caller must hold cb.mu.
func (cb *CircuitBreaker) transitionLocked(to State) {
	from := cb.state
	if from == to {
		return
	}

	cb.state = to
	cb.halfOpenRequests = 0
	cb.consecutiveSuccess = 0

	// Update metric.
	cb.metrics.state.Set(float64(to))

	slog.Info("settla-resilience: circuit breaker state change",
		"name", cb.name,
		"from", from.String(),
		"to", to.String(),
	)

	if cb.onStateChange != nil {
		// Fire callback outside the critical path but still under lock to
		// guarantee ordering. Callbacks must be non-blocking.
		cb.onStateChange(cb.name, from, to)
	}
}

// Name returns the circuit breaker name for diagnostic purposes.
func (cb *CircuitBreaker) Name() string {
	return cb.name
}

// consecutiveFailureCount is exported for testing only via GetState + GetFailures pattern.
// We expose it here for testing convenience.
var _ atomic.Int64 // ensure sync/atomic is available (compile guard)
