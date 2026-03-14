package settla

import (
	"context"
	"fmt"
	"time"
)

// defaultMaxWait is the default maximum duration waitWithBackoff will poll.
const defaultMaxWait = 5 * time.Minute

// waitWithBackoff polls checkFn with exponential backoff until it returns true,
// the maxWait duration is exceeded, or ctx is cancelled.
//
// Backoff starts at 500ms and doubles on each attempt, capped at 30s.
// If maxWait is zero, defaultMaxWait (5 minutes) is used.
func waitWithBackoff(ctx context.Context, checkFn func() (bool, error), maxWait time.Duration) error {
	if maxWait <= 0 {
		maxWait = defaultMaxWait
	}

	const (
		initialBackoff = 500 * time.Millisecond
		maxBackoff     = 30 * time.Second
	)

	backoff := initialBackoff
	deadline := time.Now().Add(maxWait)

	for {
		// Check context cancellation first.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while waiting: %w", err)
		}

		done, err := checkFn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		// Have we exceeded the deadline?
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out after %s", maxWait)
		}

		// Sleep for min(backoff, remaining), also watching ctx.
		sleep := backoff
		if sleep > remaining {
			sleep = remaining
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting: %w", ctx.Err())
		case <-time.After(sleep):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
