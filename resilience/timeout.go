package resilience

import (
	"context"
	"time"
)

// TimeoutBudget tracks the remaining time budget for a request that spans
// multiple downstream calls. As each call completes, the budget shrinks.
// Subsequent calls receive a context with at most the remaining time, ensuring
// that a slow first call doesn't leave unbounded time for later calls.
//
// Usage:
//
//	budget := NewTimeoutBudget(ctx, 200*time.Millisecond)
//	ctx1, cancel1 := budget.Allocate(100*time.Millisecond)
//	defer cancel1()
//	// ... first call with ctx1 ...
//	ctx2, cancel2 := budget.Allocate(100*time.Millisecond)
//	defer cancel2()
//	// ... second call gets min(100ms, whatever remains) ...
type TimeoutBudget struct {
	parent  context.Context
	start   time.Time
	total   time.Duration
	nowFunc func() time.Time // for testing
}

// NewTimeoutBudget creates a timeout budget rooted at ctx with the given total duration.
// The parent context's deadline is respected: if it is shorter than total, the
// parent's deadline wins.
func NewTimeoutBudget(ctx context.Context, total time.Duration) *TimeoutBudget {
	return &TimeoutBudget{
		parent:  ctx,
		start:   time.Now(),
		total:   total,
		nowFunc: time.Now,
	}
}

// Allocate returns a child context with a deadline that is the minimum of max
// and the remaining budget. If the budget is already expired, the returned
// context is immediately cancelled.
func (tb *TimeoutBudget) Allocate(max time.Duration) (context.Context, context.CancelFunc) {
	remaining := tb.Remaining()

	// Use the smaller of the requested max and the remaining budget.
	timeout := min(max, remaining)
	if timeout <= 0 {
		// Budget exhausted: return an already-cancelled context.
		ctx, cancel := context.WithCancel(tb.parent)
		cancel()
		return ctx, cancel
	}

	return context.WithTimeout(tb.parent, timeout)
}

// Remaining returns how much time is left in the budget.
func (tb *TimeoutBudget) Remaining() time.Duration {
	elapsed := tb.nowFunc().Sub(tb.start)
	remaining := tb.total - elapsed
	if remaining < 0 {
		return 0
	}

	// Also respect the parent context's deadline if it is sooner.
	if deadline, ok := tb.parent.Deadline(); ok {
		parentRemaining := time.Until(deadline)
		if parentRemaining < remaining {
			return parentRemaining
		}
	}

	return remaining
}

// Expired returns true if the entire budget has been consumed.
func (tb *TimeoutBudget) Expired() bool {
	return tb.Remaining() <= 0
}
