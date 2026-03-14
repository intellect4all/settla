package resilience

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	retryAttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "retry",
		Name:      "attempts_total",
		Help:      "Total retry attempts by operation.",
	}, []string{"operation"})

	retryExhaustedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "retry",
		Name:      "exhausted_total",
		Help:      "Total times retries were exhausted without success.",
	}, []string{"operation"})
)

// RetryConfig configures retry behavior with exponential backoff and jitter.
type RetryConfig struct {
	// Operation is used as the Prometheus label to distinguish metrics.
	Operation string

	// MaxAttempts is the total number of attempts (including the initial one).
	// Default: 3.
	MaxAttempts int

	// InitialDelay is the base delay before the first retry. Default: 100ms.
	InitialDelay time.Duration

	// MaxDelay caps the backoff delay. Default: 5s.
	MaxDelay time.Duration

	// Multiplier scales the delay between retries. Default: 2.0.
	Multiplier float64

	// JitterFactor adds randomness to avoid thundering herd.
	// 0.1 means +/-10% jitter. Default: 0.1.
	JitterFactor float64
}

// withDefaults returns a copy of cfg with zero-value fields replaced by defaults.
func (cfg RetryConfig) withDefaults() RetryConfig {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.InitialDelay <= 0 {
		cfg.InitialDelay = 100 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 5 * time.Second
	}
	if cfg.Multiplier <= 0 {
		cfg.Multiplier = 2.0
	}
	if cfg.JitterFactor < 0 {
		cfg.JitterFactor = 0.1
	}
	if cfg.JitterFactor == 0 && cfg.Multiplier == 2.0 {
		// Only set default jitter if both are at defaults (avoid overriding explicit 0).
		cfg.JitterFactor = 0.1
	}
	if cfg.Operation == "" {
		cfg.Operation = "unknown"
	}
	return cfg
}

// Retry executes fn with retries using exponential backoff and jitter.
// Only retries if shouldRetry returns true for the error.
// Respects context cancellation between attempts.
func Retry(ctx context.Context, cfg RetryConfig, shouldRetry func(error) bool, fn func(ctx context.Context) error) error {
	cfg = cfg.withDefaults()
	attempts := retryAttemptsTotal.WithLabelValues(cfg.Operation)
	exhausted := retryExhaustedTotal.WithLabelValues(cfg.Operation)

	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		attempts.Inc()

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}

		// Don't retry if caller says not to or if context is done.
		if !shouldRetry(lastErr) {
			return lastErr
		}
		if ctx.Err() != nil {
			return fmt.Errorf("settla-resilience: retry %s cancelled after attempt %d: %w", cfg.Operation, attempt+1, ctx.Err())
		}

		// Don't sleep after the last attempt.
		if attempt == cfg.MaxAttempts-1 {
			break
		}

		// Apply jitter: delay * (1 + random(-jitter, +jitter)).
		jittered := applyJitter(delay, cfg.JitterFactor)

		select {
		case <-time.After(jittered):
		case <-ctx.Done():
			return fmt.Errorf("settla-resilience: retry %s cancelled during backoff: %w", cfg.Operation, ctx.Err())
		}

		// Exponential backoff.
		delay = time.Duration(float64(delay) * cfg.Multiplier)
		delay = min(delay, cfg.MaxDelay)
	}

	exhausted.Inc()
	return fmt.Errorf("settla-resilience: retry %s exhausted after %d attempts: %w", cfg.Operation, cfg.MaxAttempts, lastErr)
}

// applyJitter adds uniform random jitter to a duration.
func applyJitter(d time.Duration, factor float64) time.Duration {
	if factor <= 0 {
		return d
	}
	jitter := float64(d) * factor
	// rand.Float64 returns [0.0, 1.0); scale to [-jitter, +jitter).
	offset := (rand.Float64()*2 - 1) * jitter
	result := float64(d) + offset
	if result < 0 {
		result = 0
	}
	return time.Duration(math.Round(result))
}
