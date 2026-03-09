// Package rpc provides resilience primitives for blockchain RPC calls:
// circuit breaking, token-bucket rate limiting, and multi-endpoint failover.
package rpc

import (
	"sync"
	"time"
)

// CircuitState represents the current state of a circuit breaker.
type CircuitState int

const (
	StateClosed   CircuitState = iota // requests flow normally
	StateOpen                         // requests are blocked
	StateHalfOpen                     // one probe request allowed
)

// CircuitBreaker implements the circuit breaker pattern for RPC endpoints.
//
// The breaker opens after maxFailures consecutive failures. After resetTimeout
// it transitions to HalfOpen, allowing a single probe. A successful probe
// closes the circuit; a failed probe reopens it.
type CircuitBreaker struct {
	mu sync.RWMutex

	maxFailures  int
	resetTimeout time.Duration

	failures      int
	state         CircuitState
	nextRetryTime time.Time
}

// NewCircuitBreaker creates a circuit breaker with the given failure threshold
// and reset timeout.
func NewCircuitBreaker(maxFailures int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		maxFailures:  maxFailures,
		resetTimeout: resetTimeout,
		state:        StateClosed,
	}
}

// CanAttempt returns true if a request can be attempted.
//
// When the reset timeout has elapsed on an Open breaker, it atomically
// transitions to HalfOpen to prevent multiple goroutines from stampeding
// a recovering endpoint.
func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Now().After(cb.nextRetryTime) {
			cb.state = StateHalfOpen
			return true
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return false
	}
}

// RecordSuccess resets the failure counter and closes the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	cb.state = StateClosed
}

// RecordFailure increments the failure counter.
// If the threshold is reached (or the breaker was HalfOpen), the circuit opens.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++

	if cb.state == StateHalfOpen {
		cb.state = StateOpen
		cb.nextRetryTime = time.Now().Add(cb.resetTimeout)
		return
	}

	if cb.failures >= cb.maxFailures {
		cb.state = StateOpen
		cb.nextRetryTime = time.Now().Add(cb.resetTimeout)
	}
}

// GetState returns the current state (thread-safe).
func (cb *CircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetFailures returns the current failure count (thread-safe).
func (cb *CircuitBreaker) GetFailures() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.failures
}

// Reset returns the breaker to the Closed state (for testing / manual recovery).
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	cb.state = StateClosed
}

// TryTransition attempts to move an Open breaker to HalfOpen if the reset
// timeout has elapsed. Called by background health-check loops.
func (cb *CircuitBreaker) TryTransition() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen && time.Now().After(cb.nextRetryTime) {
		cb.state = StateHalfOpen
	}
}