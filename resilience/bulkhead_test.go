package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBulkhead_AllowsUpToMax(t *testing.T) {
	b := NewBulkhead("test-allows", 3)

	var running atomic.Int32
	var maxSeen atomic.Int32
	var wg sync.WaitGroup

	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Execute(context.Background(), func(ctx context.Context) error {
				cur := running.Add(1)
				// Track maximum concurrent.
				for {
					old := maxSeen.Load()
					if cur <= old || maxSeen.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				running.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()

	if maxSeen.Load() > 3 {
		t.Fatalf("expected max 3 concurrent, saw %d", maxSeen.Load())
	}
}

func TestBulkhead_RejectsWhenFull(t *testing.T) {
	b := NewBulkhead("test-full", 1)

	started := make(chan struct{})
	release := make(chan struct{})

	// Fill the bulkhead.
	go func() {
		_ = b.Execute(context.Background(), func(ctx context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started // Wait for the slot to be occupied.

	// This should fail immediately.
	err := b.Execute(context.Background(), func(ctx context.Context) error {
		t.Fatal("fn should not be called when bulkhead is full")
		return nil
	})
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("expected ErrBulkheadFull, got %v", err)
	}

	close(release)
}

func TestBulkhead_AllowsAfterSlotFreed(t *testing.T) {
	b := NewBulkhead("test-freed", 1)

	// Fill and release.
	err := b.Execute(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("first execute should succeed: %v", err)
	}

	// Should succeed again.
	err = b.Execute(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("second execute should succeed after slot freed: %v", err)
	}
}

func TestBulkhead_TryExecuteWaitsForSlot(t *testing.T) {
	b := NewBulkhead("test-try", 1)

	release := make(chan struct{})
	done := make(chan struct{})

	// Fill the slot.
	go func() {
		_ = b.Execute(context.Background(), func(ctx context.Context) error {
			<-release
			return nil
		})
		close(done)
	}()

	// Give goroutine time to acquire.
	time.Sleep(10 * time.Millisecond)

	// Release after 30ms.
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(release)
	}()

	// TryExecute with generous timeout should succeed.
	err := b.TryExecute(context.Background(), 200*time.Millisecond, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("TryExecute should have succeeded after waiting: %v", err)
	}

	<-done
}

func TestBulkhead_TryExecuteTimesOut(t *testing.T) {
	b := NewBulkhead("test-try-timeout", 1)

	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		_ = b.Execute(context.Background(), func(ctx context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started

	err := b.TryExecute(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		t.Fatal("fn should not be called")
		return nil
	})
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("expected ErrBulkheadFull on timeout, got %v", err)
	}

	close(release)
}

func TestBulkhead_ContextCancelled(t *testing.T) {
	b := NewBulkhead("test-ctx", 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.Execute(ctx, func(ctx context.Context) error {
		t.Fatal("should not be called")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestBulkhead_InFlightTracking(t *testing.T) {
	b := NewBulkhead("test-inflight", 10)

	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		_ = b.Execute(context.Background(), func(ctx context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started
	if b.InFlight() != 1 {
		t.Fatalf("expected 1 in-flight, got %d", b.InFlight())
	}

	close(release)
	time.Sleep(10 * time.Millisecond) // let goroutine finish
	if b.InFlight() != 0 {
		t.Fatalf("expected 0 in-flight after completion, got %d", b.InFlight())
	}
}
