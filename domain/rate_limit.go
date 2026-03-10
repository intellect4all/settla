package domain

import "time"

// RateLimitPolicy defines per-tenant rate limiting parameters.
type RateLimitPolicy struct {
	RequestsPerMinute int64
	BurstSize         int64
	Window            time.Duration
}

// DefaultRateLimitPolicy returns a rate limit policy suitable for most tenants.
func DefaultRateLimitPolicy() RateLimitPolicy {
	return RateLimitPolicy{
		RequestsPerMinute: 600,
		BurstSize:         100,
		Window:            time.Minute,
	}
}
