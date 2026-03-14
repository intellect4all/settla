package drain

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestAcceptTrueWhenNotDraining(t *testing.T) {
	d := NewDrainer(5*time.Second, testLogger())

	for range 100 {
		if !d.Accept() {
			t.Fatal("expected Accept to return true when not draining")
		}
	}
}

func TestAcceptFalseWhenDraining(t *testing.T) {
	d := NewDrainer(5*time.Second, testLogger())

	// Start a tracked request so Drain blocks.
	done := d.Track()
	defer done()

	// Start drain in background.
	go func() {
		_ = d.Drain()
	}()

	// Wait briefly for drain to engage.
	time.Sleep(10 * time.Millisecond)

	if d.Accept() {
		t.Fatal("expected Accept to return false when draining")
	}

	if d.Rejected() != 1 {
		t.Errorf("expected 1 rejected, got %d", d.Rejected())
	}
}

func TestDrainBlocksUntilInFlightComplete(t *testing.T) {
	d := NewDrainer(5*time.Second, testLogger())

	done := d.Track()

	drainComplete := make(chan error, 1)
	go func() {
		drainComplete <- d.Drain()
	}()

	// Drain should not complete yet.
	select {
	case <-drainComplete:
		t.Fatal("expected Drain to block while request is in-flight")
	case <-time.After(50 * time.Millisecond):
		// Good, still blocking.
	}

	// Complete the request.
	done()

	select {
	case err := <-drainComplete:
		if err != nil {
			t.Fatalf("expected Drain to succeed, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected Drain to complete after in-flight request finished")
	}
}

func TestDrainRespectsTimeout(t *testing.T) {
	d := NewDrainer(50*time.Millisecond, testLogger())

	// Start a request that never completes.
	_ = d.Track()

	start := time.Now()
	err := d.Drain()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected Drain to return timeout error")
	}

	if elapsed < 40*time.Millisecond {
		t.Errorf("expected Drain to wait at least near timeout, elapsed: %v", elapsed)
	}
}

func TestDrainWithNoInFlight(t *testing.T) {
	d := NewDrainer(5*time.Second, testLogger())

	err := d.Drain()
	if err != nil {
		t.Fatalf("expected Drain with no in-flight to succeed, got: %v", err)
	}
}

func TestConcurrentAcceptTrackDrain(t *testing.T) {
	d := NewDrainer(2*time.Second, testLogger())

	var wg sync.WaitGroup

	// Simulate concurrent requests.
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.Accept() {
				done := d.Track()
				time.Sleep(10 * time.Millisecond)
				done()
			}
		}()
	}

	// Start drain after a small delay.
	time.Sleep(5 * time.Millisecond)
	err := d.Drain()

	wg.Wait()

	if err != nil {
		t.Fatalf("expected concurrent drain to succeed, got: %v", err)
	}

	if d.InFlight() != 0 {
		t.Errorf("expected 0 in-flight after drain, got %d", d.InFlight())
	}
}

func TestInFlightCounter(t *testing.T) {
	d := NewDrainer(5*time.Second, testLogger())

	if d.InFlight() != 0 {
		t.Fatalf("expected 0 in-flight initially")
	}

	done1 := d.Track()
	done2 := d.Track()

	if d.InFlight() != 2 {
		t.Fatalf("expected 2 in-flight, got %d", d.InFlight())
	}

	done1()
	if d.InFlight() != 1 {
		t.Fatalf("expected 1 in-flight, got %d", d.InFlight())
	}

	done2()
	if d.InFlight() != 0 {
		t.Fatalf("expected 0 in-flight, got %d", d.InFlight())
	}
}

func TestIsDraining(t *testing.T) {
	d := NewDrainer(5*time.Second, testLogger())

	if d.IsDraining() {
		t.Fatal("expected not draining initially")
	}

	_ = d.Drain()

	if !d.IsDraining() {
		t.Fatal("expected draining after Drain call")
	}
}
