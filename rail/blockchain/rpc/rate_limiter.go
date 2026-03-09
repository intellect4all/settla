package rpc

import (
	"sync"
	"time"
)

// RateLimiter implements a token-bucket rate limiter.
//
// Tokens refill at rate tokens/second up to capacity. Allow() consumes one
// token and returns false if none are available (non-blocking).
type RateLimiter struct {
	mu sync.Mutex

	rate       float64   // tokens per second
	capacity   int       // maximum tokens
	tokens     float64   // current token count
	lastRefill time.Time
}

// NewRateLimiter creates a rate limiter with the given rate and burst capacity.
// If capacity is 0 it defaults to ratePerSecond.
func NewRateLimiter(ratePerSecond, capacity int) *RateLimiter {
	if capacity <= 0 {
		capacity = ratePerSecond
	}
	return &RateLimiter{
		rate:       float64(ratePerSecond),
		capacity:   capacity,
		tokens:     float64(capacity),
		lastRefill: time.Now(),
	}
}

// Allow returns true if a token is available, consuming it. Non-blocking.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.refill()

	if rl.tokens >= 1.0 {
		rl.tokens--
		return true
	}
	return false
}

// Wait blocks until a token is available.
func (rl *RateLimiter) Wait() {
	for !rl.Allow() {
		time.Sleep(10 * time.Millisecond)
	}
}

// GetTokens returns the current token count (for observability).
func (rl *RateLimiter) GetTokens() float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refill()
	return rl.tokens
}

// refill adds tokens proportional to elapsed time since the last refill.
// Must be called with rl.mu held.
func (rl *RateLimiter) refill() {
	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	added := elapsed * rl.rate
	rl.tokens = min(rl.tokens+added, float64(rl.capacity))
	rl.lastRefill = now
}
