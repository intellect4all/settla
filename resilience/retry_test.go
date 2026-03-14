package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_SucceedsFirstAttempt(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), RetryConfig{
		Operation:    "test-first",
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
	}, func(error) bool { return true }, func(ctx context.Context) error {
		attempts++
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestRetry_SucceedsAfterRetries(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), RetryConfig{
		Operation:    "test-retry-ok",
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
	}, func(error) bool { return true }, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errFake
		}
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error after 3rd attempt, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetry_ExhaustsMaxAttempts(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), RetryConfig{
		Operation:    "test-exhaust",
		MaxAttempts:  3,
		InitialDelay: 5 * time.Millisecond,
		MaxDelay:     20 * time.Millisecond,
	}, func(error) bool { return true }, func(ctx context.Context) error {
		attempts++
		return errFake
	})

	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if !errors.Is(err, errFake) {
		t.Fatalf("expected wrapped errFake, got %v", err)
	}
}

func TestRetry_NonRetryableErrorStopsImmediately(t *testing.T) {
	permanent := errors.New("permanent")
	attempts := 0
	err := Retry(context.Background(), RetryConfig{
		Operation:    "test-nonretry",
		MaxAttempts:  5,
		InitialDelay: 10 * time.Millisecond,
	}, func(err error) bool {
		return !errors.Is(err, permanent)
	}, func(ctx context.Context) error {
		attempts++
		return permanent
	})

	if attempts != 1 {
		t.Fatalf("expected 1 attempt (non-retryable), got %d", attempts)
	}
	if !errors.Is(err, permanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
}

func TestRetry_ExponentialBackoffTiming(t *testing.T) {
	attempts := 0
	start := time.Now()

	_ = Retry(context.Background(), RetryConfig{
		Operation:    "test-timing",
		MaxAttempts:  3,
		InitialDelay: 20 * time.Millisecond,
		Multiplier:   2.0,
		JitterFactor: 0, // no jitter for predictable timing
	}, func(error) bool { return true }, func(ctx context.Context) error {
		attempts++
		return errFake
	})

	elapsed := time.Since(start)

	// Expected: 20ms (first backoff) + 40ms (second backoff) = ~60ms total wait.
	// Allow generous bounds for CI.
	if elapsed < 40*time.Millisecond {
		t.Fatalf("backoff too fast: %v (expected >= ~60ms)", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("backoff too slow: %v (expected ~60ms)", elapsed)
	}
}

func TestRetry_JitterBounds(t *testing.T) {
	// Run many retries to check that jitter stays within bounds.
	for i := range 20 {
		start := time.Now()
		_ = Retry(context.Background(), RetryConfig{
			Operation:    "test-jitter",
			MaxAttempts:  2,
			InitialDelay: 50 * time.Millisecond,
			JitterFactor: 0.1,
		}, func(error) bool { return true }, func(ctx context.Context) error {
			return errFake
		})
		elapsed := time.Since(start)

		// With 10% jitter on 50ms: delay in [45ms, 55ms].
		// Be generous for CI: [30ms, 100ms].
		if elapsed < 30*time.Millisecond || elapsed > 100*time.Millisecond {
			t.Fatalf("trial %d: jitter out of bounds, elapsed %v", i, elapsed)
		}
	}
}

func TestRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err := Retry(ctx, RetryConfig{
		Operation:    "test-cancel",
		MaxAttempts:  10,
		InitialDelay: 20 * time.Millisecond,
	}, func(error) bool { return true }, func(ctx context.Context) error {
		attempts++
		return errFake
	})

	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	// Should have stopped early, not exhausted all 10 attempts.
	if attempts >= 10 {
		t.Fatalf("expected early stop, but ran %d attempts", attempts)
	}
}

func TestRetry_DefaultConfig(t *testing.T) {
	cfg := RetryConfig{}.withDefaults()
	if cfg.MaxAttempts != 3 {
		t.Fatalf("default MaxAttempts should be 3, got %d", cfg.MaxAttempts)
	}
	if cfg.InitialDelay != 100*time.Millisecond {
		t.Fatalf("default InitialDelay should be 100ms, got %v", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 5*time.Second {
		t.Fatalf("default MaxDelay should be 5s, got %v", cfg.MaxDelay)
	}
	if cfg.Multiplier != 2.0 {
		t.Fatalf("default Multiplier should be 2.0, got %f", cfg.Multiplier)
	}
}
