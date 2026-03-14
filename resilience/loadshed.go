package resilience

import (
	"errors"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ErrLoadShedded is returned when a request is rejected due to load shedding.
var ErrLoadShedded = errors.New("settla-resilience: load shedded — server overloaded")

var (
	loadshedRejectedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "loadshed",
		Name:      "rejected_total",
		Help:      "Total requests rejected by load shedding.",
	})

	loadshedInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "settla",
		Subsystem: "loadshed",
		Name:      "in_flight",
		Help:      "Current number of in-flight requests.",
	})

	loadshedLimit = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "settla",
		Subsystem: "loadshed",
		Name:      "limit",
		Help:      "Current adaptive concurrency limit.",
	})
)

// LoadShedOption configures a LoadShedder.
type LoadShedOption func(*LoadShedder)

// WithMinLimit sets the floor for the adaptive concurrency limit.
func WithMinLimit(n int) LoadShedOption {
	return func(ls *LoadShedder) {
		if n > 0 {
			ls.minLimit = n
		}
	}
}

// WithMaxLimit sets the ceiling for the adaptive concurrency limit.
func WithMaxLimit(n int) LoadShedOption {
	return func(ls *LoadShedder) {
		if n > 0 {
			ls.maxLimit = n
		}
	}
}

// WithInitialLimit sets the starting concurrency limit.
func WithInitialLimit(n int) LoadShedOption {
	return func(ls *LoadShedder) {
		if n > 0 {
			ls.limit = int64(n)
		}
	}
}

// WithTargetLatency sets the target latency for the adaptive algorithm.
// When observed latency exceeds this target, the limit decreases.
func WithTargetLatency(d time.Duration) LoadShedOption {
	return func(ls *LoadShedder) {
		if d > 0 {
			ls.targetLatency = d
		}
	}
}

// LoadShedder monitors system load and rejects excess traffic with 503.
//
// It uses an adaptive concurrency limiting algorithm inspired by Little's Law:
//
//	concurrency_limit = ceil(target_throughput * target_latency)
//
// When in-flight requests exceed the limit, new requests are rejected
// immediately rather than queuing and timing out. The limit adjusts based
// on observed latency: if latency is below target, the limit grows
// (additive increase); if latency exceeds target, the limit shrinks
// (multiplicative decrease).
type LoadShedder struct {
	inFlight      atomic.Int64
	limit         int64
	minLimit      int
	maxLimit      int
	targetLatency time.Duration

	// EWMA of observed latency for adaptive limiting.
	mu         sync.Mutex
	ewmaLatNs  float64
	ewmaAlpha  float64 // smoothing factor
	sampleCount int64
}

// NewLoadShedder creates a load shedder with the given options.
// Defaults: minLimit=10, maxLimit=5000, initialLimit=200, targetLatency=50ms.
func NewLoadShedder(opts ...LoadShedOption) *LoadShedder {
	ls := &LoadShedder{
		minLimit:      10,
		maxLimit:      5000,
		limit:         200,
		targetLatency: 50 * time.Millisecond,
		ewmaAlpha:     0.1, // smooth over ~10 samples
	}
	for _, opt := range opts {
		opt(ls)
	}

	loadshedLimit.Set(float64(ls.limit))
	return ls
}

// Allow checks if a new request should be admitted.
// If admitted, it returns a done function that MUST be called when the
// request completes, along with a success boolean indicating whether the
// request completed without error (used for adaptive limit tuning).
// If rejected, it returns ErrLoadShedded.
func (ls *LoadShedder) Allow() (done func(success bool), err error) {
	current := ls.inFlight.Add(1)
	limit := atomic.LoadInt64(&ls.limit)

	if current > limit {
		ls.inFlight.Add(-1)
		loadshedRejectedTotal.Inc()
		slog.Debug("settla-resilience: load shedding request",
			"in_flight", current-1,
			"limit", limit,
		)
		return nil, ErrLoadShedded
	}

	loadshedInFlight.Set(float64(current))
	start := time.Now()

	return func(success bool) {
		newInFlight := ls.inFlight.Add(-1)
		loadshedInFlight.Set(float64(newInFlight))

		latency := time.Since(start)
		ls.updateAdaptiveLimit(latency, success)
	}, nil
}

// updateAdaptiveLimit adjusts the concurrency limit based on observed latency.
func (ls *LoadShedder) updateAdaptiveLimit(latency time.Duration, success bool) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	ls.sampleCount++

	// Update EWMA of latency.
	latNs := float64(latency.Nanoseconds())
	if ls.sampleCount == 1 {
		ls.ewmaLatNs = latNs
	} else {
		ls.ewmaLatNs = ls.ewmaAlpha*latNs + (1-ls.ewmaAlpha)*ls.ewmaLatNs
	}

	// Only adjust every 20 samples to avoid oscillation.
	if ls.sampleCount%20 != 0 {
		return
	}

	currentLimit := atomic.LoadInt64(&ls.limit)
	targetNs := float64(ls.targetLatency.Nanoseconds())

	var newLimit int64
	if ls.ewmaLatNs <= targetNs && success {
		// Additive increase: latency is healthy, allow more concurrency.
		newLimit = currentLimit + 5
	} else {
		// Multiplicative decrease: latency too high, shed load.
		newLimit = int64(math.Ceil(float64(currentLimit) * 0.9))
	}

	// Clamp to bounds.
	if newLimit < int64(ls.minLimit) {
		newLimit = int64(ls.minLimit)
	}
	if newLimit > int64(ls.maxLimit) {
		newLimit = int64(ls.maxLimit)
	}

	if newLimit != currentLimit {
		atomic.StoreInt64(&ls.limit, newLimit)
		loadshedLimit.Set(float64(newLimit))
	}
}

// InFlight returns the current number of in-flight requests.
func (ls *LoadShedder) InFlight() int64 {
	return ls.inFlight.Load()
}

// Limit returns the current adaptive concurrency limit.
func (ls *LoadShedder) Limit() int64 {
	return atomic.LoadInt64(&ls.limit)
}
