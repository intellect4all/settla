package resilience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ErrBulkheadFull is returned when the bulkhead has no available slots.
var ErrBulkheadFull = errors.New("settla-resilience: bulkhead is full")

var (
	bulkheadConcurrent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "settla",
		Subsystem: "bulkhead",
		Name:      "concurrent",
		Help:      "Current number of in-flight requests in the bulkhead.",
	}, []string{"name"})

	bulkheadRejectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "bulkhead",
		Name:      "rejections_total",
		Help:      "Total requests rejected because the bulkhead was full.",
	}, []string{"name"})
)

// Bulkhead limits concurrent access to a resource using a semaphore pattern.
// If the bulkhead is full, calls fail fast with ErrBulkheadFull rather than
// queuing indefinitely, preventing one slow dependency from exhausting all
// goroutines and cascading into unrelated call paths.
type Bulkhead struct {
	name          string
	maxConcurrent int
	sem           chan struct{}
	inFlight      atomic.Int64

	concurrent prometheus.Gauge
	rejections prometheus.Counter
}

// NewBulkhead creates a bulkhead that allows at most maxConcurrent simultaneous
// executions for the named resource.
func NewBulkhead(name string, maxConcurrent int) *Bulkhead {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	b := &Bulkhead{
		name:          name,
		maxConcurrent: maxConcurrent,
		sem:           make(chan struct{}, maxConcurrent),
		concurrent:    bulkheadConcurrent.WithLabelValues(name),
		rejections:    bulkheadRejectionsTotal.WithLabelValues(name),
	}
	return b
}

// Execute runs fn if a slot is available, otherwise returns ErrBulkheadFull immediately.
func (b *Bulkhead) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("settla-resilience: bulkhead %s: %w", b.name, err)
	}

	// Try to acquire a slot without blocking.
	select {
	case b.sem <- struct{}{}:
		// Acquired.
	default:
		b.rejections.Inc()
		slog.Debug("settla-resilience: bulkhead full",
			"name", b.name,
			"max_concurrent", b.maxConcurrent,
		)
		return ErrBulkheadFull
	}

	b.inFlight.Add(1)
	b.concurrent.Inc()

	defer func() {
		<-b.sem
		b.inFlight.Add(-1)
		b.concurrent.Dec()
	}()

	return fn(ctx)
}

// TryExecute is like Execute but waits up to timeout for a slot to become available.
// If the timeout expires before a slot opens, it returns ErrBulkheadFull.
func (b *Bulkhead) TryExecute(ctx context.Context, timeout time.Duration, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("settla-resilience: bulkhead %s: %w", b.name, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case b.sem <- struct{}{}:
		// Acquired.
	case <-timer.C:
		b.rejections.Inc()
		return ErrBulkheadFull
	case <-ctx.Done():
		return fmt.Errorf("settla-resilience: bulkhead %s: %w", b.name, ctx.Err())
	}

	b.inFlight.Add(1)
	b.concurrent.Inc()

	defer func() {
		<-b.sem
		b.inFlight.Add(-1)
		b.concurrent.Dec()
	}()

	return fn(ctx)
}

// InFlight returns the current number of in-flight requests.
func (b *Bulkhead) InFlight() int64 {
	return b.inFlight.Load()
}

// Name returns the bulkhead name.
func (b *Bulkhead) Name() string {
	return b.name
}
