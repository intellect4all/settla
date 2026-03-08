package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// maxHistogramCap caps the number of samples stored per histogram.
// At 5,000 TPS for 10 minutes = 3M samples; sorting 3M entries on each
// printMetrics call would dominate CPU. 100K samples gives accurate
// percentiles while keeping sort time under 50ms.
const maxHistogramCap = 100_000

// LoadTestMetrics collects real-time metrics during load testing.
type LoadTestMetrics struct {
	// Throughput counters
	RequestsTotal      atomic.Int64
	TransfersCreated   atomic.Int64
	TransfersCompleted atomic.Int64
	TransfersFailed    atomic.Int64
	PeakInflight       atomic.Int64
	CurrentTPS         atomic.Int64
	PeakTPS            atomic.Int64

	// Latency histograms (in microseconds)
	quoteLatency    *LatencyHistogram
	createLatency   *LatencyHistogram
	pollLatency     *LatencyHistogram
	endToEndLatency *LatencyHistogram

	// Error tracking
	errors   map[string]*atomic.Int64
	errorsMu sync.RWMutex

	// Results for verification
	mu      sync.RWMutex
	results []TransferResult
}

// LatencyHistogram tracks latency percentiles.
type LatencyHistogram struct {
	buckets []int64 // Microsecond buckets
	mu      sync.RWMutex
}

// NewLoadTestMetrics creates a new metrics collector.
func NewLoadTestMetrics() *LoadTestMetrics {
	return &LoadTestMetrics{
		quoteLatency:    NewLatencyHistogram(),
		createLatency:   NewLatencyHistogram(),
		pollLatency:     NewLatencyHistogram(),
		endToEndLatency: NewLatencyHistogram(),
		errors:          make(map[string]*atomic.Int64),
		results:         make([]TransferResult, 0, 10000),
	}
}

// NewLatencyHistogram creates a new latency histogram.
func NewLatencyHistogram() *LatencyHistogram {
	return &LatencyHistogram{
		buckets: make([]int64, 0, maxHistogramCap),
	}
}

// Record adds a latency sample. Samples beyond maxHistogramCap are dropped;
// early samples are most representative during a sustained-peak test.
func (h *LatencyHistogram) Record(duration time.Duration) {
	micros := duration.Microseconds()
	h.mu.Lock()
	if len(h.buckets) < maxHistogramCap {
		h.buckets = append(h.buckets, micros)
	}
	h.mu.Unlock()
}

// Percentile returns the specified percentile (0-100).
func (h *LatencyHistogram) Percentile(p float64) time.Duration {
	h.mu.RLock()
	if len(h.buckets) == 0 {
		h.mu.RUnlock()
		return 0
	}
	sorted := make([]int64, len(h.buckets))
	copy(sorted, h.buckets)
	h.mu.RUnlock()

	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := int(float64(len(sorted)-1) * p / 100.0)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}

	return time.Duration(sorted[idx]) * time.Microsecond
}

// Stats returns p50, p95, p99 latencies.
func (h *LatencyHistogram) Stats() (p50, p95, p99 time.Duration) {
	return h.Percentile(50), h.Percentile(95), h.Percentile(99)
}

// Count returns the number of samples.
func (h *LatencyHistogram) Count() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return int64(len(h.buckets))
}

// RecordQuoteLatency records quote creation latency.
func (m *LoadTestMetrics) RecordQuoteLatency(duration time.Duration) {
	m.quoteLatency.Record(duration)
}

// RecordCreateLatency records transfer creation latency.
func (m *LoadTestMetrics) RecordCreateLatency(duration time.Duration) {
	m.createLatency.Record(duration)
}

// RecordPollLatency records transfer polling latency.
func (m *LoadTestMetrics) RecordPollLatency(duration time.Duration) {
	m.pollLatency.Record(duration)
}

// RecordEndToEndLatency records end-to-end transfer latency.
func (m *LoadTestMetrics) RecordEndToEndLatency(duration time.Duration) {
	m.endToEndLatency.Record(duration)
}

// RecordError increments the error counter for the given error code.
func (m *LoadTestMetrics) RecordError(code string) {
	m.errorsMu.Lock()
	counter, ok := m.errors[code]
	if !ok {
		counter = &atomic.Int64{}
		m.errors[code] = counter
	}
	m.errorsMu.Unlock()
	counter.Add(1)
}

// AddResult adds a transfer result for verification.
func (m *LoadTestMetrics) AddResult(result TransferResult) {
	m.mu.Lock()
	m.results = append(m.results, result)
	m.mu.Unlock()
}

// PrintLatencyStats prints latency statistics.
func (m *LoadTestMetrics) PrintLatencyStats() {
	fmt.Println("\nLatency Statistics:")

	if count := m.quoteLatency.Count(); count > 0 {
		p50, p95, p99 := m.quoteLatency.Stats()
		fmt.Printf("  Quote:     p50=%v, p95=%v, p99=%v (n=%d)\n", p50, p95, p99, count)
	}

	if count := m.createLatency.Count(); count > 0 {
		p50, p95, p99 := m.createLatency.Stats()
		fmt.Printf("  Create:    p50=%v, p95=%v, p99=%v (n=%d)\n", p50, p95, p99, count)
	}

	if count := m.pollLatency.Count(); count > 0 {
		p50, p95, p99 := m.pollLatency.Stats()
		fmt.Printf("  Poll:      p50=%v, p95=%v, p99=%v (n=%d)\n", p50, p95, p99, count)
	}

	if count := m.endToEndLatency.Count(); count > 0 {
		p50, p95, p99 := m.endToEndLatency.Stats()
		fmt.Printf("  End-to-End: p50=%v, p95=%v, p99=%v (n=%d)\n", p50, p95, p99, count)
	}

	// Print errors
	m.errorsMu.RLock()
	if len(m.errors) > 0 {
		fmt.Println("\nErrors:")
		for code, count := range m.errors {
			fmt.Printf("  %s: %d\n", code, count.Load())
		}
	}
	m.errorsMu.RUnlock()
}

// FinalReport generates a final test report.
func (m *LoadTestMetrics) FinalReport() string {
	created := m.TransfersCreated.Load()
	completed := m.TransfersCompleted.Load()
	failed := m.TransfersFailed.Load()

	report := fmt.Sprintf("\n=== Final Load Test Report ===\n")
	report += fmt.Sprintf("Total Transfers Created:   %d\n", created)
	report += fmt.Sprintf("Transfers Completed:       %d\n", completed)
	report += fmt.Sprintf("Transfers Failed:          %d\n", failed)

	if created > 0 {
		successRate := float64(completed) / float64(created) * 100
		report += fmt.Sprintf("Success Rate:              %.2f%%\n", successRate)
	}

	if count := m.endToEndLatency.Count(); count > 0 {
		p50, p95, p99 := m.endToEndLatency.Stats()
		report += fmt.Sprintf("\nEnd-to-End Latency:\n")
		report += fmt.Sprintf("  p50: %v\n", p50)
		report += fmt.Sprintf("  p95: %v\n", p95)
		report += fmt.Sprintf("  p99: %v\n", p99)
	}

	return report
}
