package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var errFake = errors.New("fake failure")

func TestCircuitBreaker_ClosedAllowsRequests(t *testing.T) {
	cb := NewCircuitBreaker("test-closed")

	called := false
	err := cb.Execute(context.Background(), func(ctx context.Context) error {
		called = true
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !called {
		t.Fatal("expected fn to be called")
	}
	if cb.GetState() != StateClosed {
		t.Fatalf("expected state Closed, got %v", cb.GetState())
	}
}

func TestCircuitBreaker_TripsOpenAfterThresholdFailures(t *testing.T) {
	cb := NewCircuitBreaker("test-trip",
		WithFailureThreshold(3),
		WithResetTimeout(1*time.Second),
	)

	failFn := func(ctx context.Context) error { return errFake }

	for range 3 {
		_ = cb.Execute(context.Background(), failFn)
	}

	if cb.GetState() != StateOpen {
		t.Fatalf("expected state Open after 3 failures, got %v", cb.GetState())
	}

	// Subsequent calls should be rejected.
	err := cb.Execute(context.Background(), func(ctx context.Context) error {
		t.Fatal("fn should not be called when circuit is open")
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCircuitBreaker_DoesNotTripBelowThreshold(t *testing.T) {
	cb := NewCircuitBreaker("test-below",
		WithFailureThreshold(5),
	)

	failFn := func(ctx context.Context) error { return errFake }

	for range 4 {
		_ = cb.Execute(context.Background(), failFn)
	}

	if cb.GetState() != StateClosed {
		t.Fatalf("expected state Closed with only 4 failures (threshold 5), got %v", cb.GetState())
	}
}

func TestCircuitBreaker_SuccessResetsFailureCount(t *testing.T) {
	cb := NewCircuitBreaker("test-reset",
		WithFailureThreshold(3),
	)

	failFn := func(ctx context.Context) error { return errFake }
	successFn := func(ctx context.Context) error { return nil }

	// 2 failures, then a success, then 2 more failures.
	_ = cb.Execute(context.Background(), failFn)
	_ = cb.Execute(context.Background(), failFn)
	_ = cb.Execute(context.Background(), successFn) // resets counter
	_ = cb.Execute(context.Background(), failFn)
	_ = cb.Execute(context.Background(), failFn)

	if cb.GetState() != StateClosed {
		t.Fatalf("expected Closed (success reset the counter), got %v", cb.GetState())
	}
}

func TestCircuitBreaker_TransitionsToHalfOpenAfterResetTimeout(t *testing.T) {
	cb := NewCircuitBreaker("test-halfopen",
		WithFailureThreshold(2),
		WithResetTimeout(50*time.Millisecond),
	)

	failFn := func(ctx context.Context) error { return errFake }
	_ = cb.Execute(context.Background(), failFn)
	_ = cb.Execute(context.Background(), failFn)

	if cb.GetState() != StateOpen {
		t.Fatalf("expected Open, got %v", cb.GetState())
	}

	time.Sleep(60 * time.Millisecond)

	if cb.GetState() != StateHalfOpen {
		t.Fatalf("expected HalfOpen after reset timeout, got %v", cb.GetState())
	}
}

func TestCircuitBreaker_HalfOpenLimitsRequests(t *testing.T) {
	cb := NewCircuitBreaker("test-halfopen-limit",
		WithFailureThreshold(1),
		WithResetTimeout(10*time.Millisecond),
		WithHalfOpenMax(2),
		WithSuccessThreshold(3), // Need 3 successes, but only 2 slots in half-open.
	)

	// Trip the breaker.
	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return errFake })
	time.Sleep(15 * time.Millisecond)

	// In half-open, only halfOpenMax (2) requests should be allowed.
	// Since successThreshold is 3 and we only get 2 slots, the circuit
	// stays half-open and the 3rd request is rejected.
	allowed := 0
	rejected := 0
	for range 5 {
		err := cb.Execute(context.Background(), func(ctx context.Context) error { return nil })
		if err == nil {
			allowed++
		} else {
			rejected++
		}
	}

	if allowed != 2 {
		t.Fatalf("expected 2 allowed in half-open (halfOpenMax=2), got %d", allowed)
	}
	if rejected != 3 {
		t.Fatalf("expected 3 rejected, got %d", rejected)
	}
}

func TestCircuitBreaker_HalfOpenSuccessClosesCircuit(t *testing.T) {
	cb := NewCircuitBreaker("test-halfopen-close",
		WithFailureThreshold(1),
		WithResetTimeout(10*time.Millisecond),
		WithHalfOpenMax(3),
		WithSuccessThreshold(2),
	)

	// Trip the breaker.
	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return errFake })
	time.Sleep(15 * time.Millisecond)

	// Two successes in half-open should close the circuit.
	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return nil })
	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return nil })

	if cb.GetState() != StateClosed {
		t.Fatalf("expected Closed after 2 successes in half-open, got %v", cb.GetState())
	}

	// Now the circuit is closed, all requests should succeed.
	err := cb.Execute(context.Background(), func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatalf("expected success in closed state, got %v", err)
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker("test-halfopen-fail",
		WithFailureThreshold(1),
		WithResetTimeout(10*time.Millisecond),
		WithHalfOpenMax(3),
		WithSuccessThreshold(2),
	)

	// Trip.
	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return errFake })
	time.Sleep(15 * time.Millisecond)

	// Fail in half-open.
	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return errFake })

	if cb.GetState() != StateOpen {
		t.Fatalf("expected Open after half-open failure, got %v", cb.GetState())
	}
}

func TestCircuitBreaker_OnStateChangeCallback(t *testing.T) {
	var mu sync.Mutex
	var transitions []struct{ from, to State }

	cb := NewCircuitBreaker("test-callback",
		WithFailureThreshold(1),
		WithResetTimeout(10*time.Millisecond),
		WithOnStateChange(func(name string, from, to State) {
			mu.Lock()
			transitions = append(transitions, struct{ from, to State }{from, to})
			mu.Unlock()
		}),
	)

	_ = cb.Execute(context.Background(), func(ctx context.Context) error { return errFake })
	time.Sleep(15 * time.Millisecond)
	_ = cb.GetState() // triggers half-open transition

	mu.Lock()
	defer mu.Unlock()

	if len(transitions) < 2 {
		t.Fatalf("expected at least 2 transitions (closed->open, open->half_open), got %d", len(transitions))
	}
	if transitions[0].from != StateClosed || transitions[0].to != StateOpen {
		t.Fatalf("first transition should be closed->open, got %v->%v", transitions[0].from, transitions[0].to)
	}
	if transitions[1].from != StateOpen || transitions[1].to != StateHalfOpen {
		t.Fatalf("second transition should be open->half_open, got %v->%v", transitions[1].from, transitions[1].to)
	}
}

func TestCircuitBreaker_ContextCancelled(t *testing.T) {
	cb := NewCircuitBreaker("test-ctx")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := cb.Execute(ctx, func(ctx context.Context) error {
		t.Fatal("fn should not be called with cancelled context")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker("test-concurrent",
		WithFailureThreshold(100),
	)

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cb.Execute(context.Background(), func(ctx context.Context) error {
				if time.Now().UnixNano()%2 == 0 {
					return errFake
				}
				return nil
			})
		}()
	}
	wg.Wait()

	// Should not panic or deadlock. State should be valid.
	state := cb.GetState()
	if state != StateClosed && state != StateOpen {
		t.Fatalf("unexpected state %v", state)
	}
}
