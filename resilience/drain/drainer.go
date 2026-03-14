// Package drain provides graceful connection draining for zero-downtime deployments.
//
// During shutdown, the drainer transitions through three phases:
//  1. Normal: Accept() returns true, all requests proceed.
//  2. Draining: Accept() returns false (new requests get 503),
//     but in-flight requests continue until they complete or timeout.
//  3. Complete: all in-flight requests done, or timeout reached.
//
// Usage in HTTP middleware:
//
//	if !drainer.Accept() {
//	    w.Header().Set("Connection", "close")
//	    w.WriteHeader(http.StatusServiceUnavailable)
//	    return
//	}
//	done := drainer.Track()
//	defer done()
//	// ... handle request ...
package drain

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// Drainer manages graceful connection draining during shutdown.
type Drainer struct {
	draining atomic.Bool
	inFlight atomic.Int64
	doneCh   chan struct{}
	timeout  time.Duration
	logger   *slog.Logger

	// Metrics counters.
	rejected atomic.Int64
}

// NewDrainer creates a new drainer with the given drain timeout.
// The timeout is the maximum time to wait for in-flight requests to complete.
func NewDrainer(timeout time.Duration, logger *slog.Logger) *Drainer {
	return &Drainer{
		doneCh:  make(chan struct{}),
		timeout: timeout,
		logger:  logger,
	}
}

// Accept returns true if the server is accepting new requests.
// Returns false when draining — callers should reject the request with 503.
func (d *Drainer) Accept() bool {
	if d.draining.Load() {
		d.rejected.Add(1)
		return false
	}
	return true
}

// Track marks a request as in-flight. The returned function must be called
// when the request completes (typically via defer).
func (d *Drainer) Track() func() {
	d.inFlight.Add(1)
	return func() {
		if d.inFlight.Add(-1) == 0 && d.draining.Load() {
			// Last in-flight request completed during drain.
			select {
			case d.doneCh <- struct{}{}:
			default:
			}
		}
	}
}

// Drain initiates graceful drain. It stops accepting new requests and blocks
// until all in-flight requests complete or the timeout is reached.
//
// Returns nil if all requests completed, or an error if the timeout was reached
// with requests still in-flight.
func (d *Drainer) Drain() error {
	d.draining.Store(true)

	current := d.inFlight.Load()
	d.logger.Info("settla-drain: starting graceful drain",
		"in_flight", current,
		"timeout", d.timeout,
	)

	if current == 0 {
		d.logger.Info("settla-drain: no in-flight requests, drain complete")
		return nil
	}

	timer := time.NewTimer(d.timeout)
	defer timer.Stop()

	select {
	case <-d.doneCh:
		d.logger.Info("settla-drain: all in-flight requests completed")
		return nil
	case <-timer.C:
		remaining := d.inFlight.Load()
		d.logger.Warn("settla-drain: timeout reached with requests still in-flight",
			"remaining", remaining,
		)
		return fmt.Errorf("settla-drain: timeout after %s with %d requests in-flight", d.timeout, remaining)
	}
}

// InFlight returns the current number of in-flight requests.
func (d *Drainer) InFlight() int64 {
	return d.inFlight.Load()
}

// Rejected returns the total number of rejected requests since drain started.
func (d *Drainer) Rejected() int64 {
	return d.rejected.Load()
}

// IsDraining returns true if the drainer is in drain mode.
func (d *Drainer) IsDraining() bool {
	return d.draining.Load()
}
