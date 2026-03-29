package main

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Component-level microbenchmarks
//
// Run with: go test -bench=Benchmark -benchmem -benchtime=5s ./tests/loadtest/
// =============================================================================

// --- Zipf Distribution Sampling ---

func BenchmarkZipfSample_1K(b *testing.B) {
	z := NewZipfDistribution(1_000, 1.2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		z.Sample()
	}
}

func BenchmarkZipfSample_10K(b *testing.B) {
	z := NewZipfDistribution(10_000, 1.2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		z.Sample()
	}
}

func BenchmarkZipfSample_100K(b *testing.B) {
	z := NewZipfDistribution(100_000, 1.2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		z.Sample()
	}
}

// --- Tenant Config Generation ---

func BenchmarkGenerateScaleTenants_50(b *testing.B) {
	mix := DefaultCurrencyMix()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateScaleTenants(50, mix)
	}
}

func BenchmarkGenerateScaleTenants_20K(b *testing.B) {
	mix := DefaultCurrencyMix()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateScaleTenants(20_000, mix)
	}
}

func BenchmarkGenerateScaleTenants_100K(b *testing.B) {
	mix := DefaultCurrencyMix()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateScaleTenants(100_000, mix)
	}
}

// --- Latency Histogram ---

func BenchmarkHistogramRecord(b *testing.B) {
	h := NewLatencyHistogram()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Record(time.Duration(rand.Intn(10000)) * time.Microsecond)
	}
}

func BenchmarkHistogramPercentile(b *testing.B) {
	h := NewLatencyHistogram()
	for i := 0; i < 100_000; i++ {
		h.Record(time.Duration(rand.Intn(10000)) * time.Microsecond)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Percentile(99)
	}
}

func BenchmarkHistogramRecordConcurrent(b *testing.B) {
	h := NewLatencyHistogram()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h.Record(time.Duration(rand.Intn(10000)) * time.Microsecond)
		}
	})
}

// --- Metrics Collection ---

func BenchmarkMetricsRecordError(b *testing.B) {
	m := NewLoadTestMetrics()
	categories := []string{"timeout", "rate_limited_429", "server_error_5xx", "client_error_4xx", "connection_error"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.RecordError(categories[i%len(categories)])
	}
}

func BenchmarkMetricsRecordErrorConcurrent(b *testing.B) {
	m := NewLoadTestMetrics()
	categories := []string{"timeout", "rate_limited_429", "server_error_5xx", "client_error_4xx", "connection_error"}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.RecordError(categories[i%len(categories)])
			i++
		}
	})
}

// --- HotSpot Selector ---

func BenchmarkHotspotSelect(b *testing.B) {
	tenants := multiTenantPool(50)
	hs, _ := HotspotTenantPool(tenants, 80.0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hs.Select()
	}
}

// --- sync.Map performance at scale (simulates per-tenant caches) ---

func BenchmarkSyncMapRead_1K(b *testing.B)   { benchSyncMapRead(b, 1_000) }
func BenchmarkSyncMapRead_10K(b *testing.B)  { benchSyncMapRead(b, 10_000) }
func BenchmarkSyncMapRead_50K(b *testing.B)  { benchSyncMapRead(b, 50_000) }
func BenchmarkSyncMapRead_100K(b *testing.B) { benchSyncMapRead(b, 100_000) }

func benchSyncMapRead(b *testing.B, n int) {
	var m sync.Map
	for i := 0; i < n; i++ {
		m.Store(fmt.Sprintf("tenant-%d", i), i)
	}
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("tenant-%d", i)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.Load(keys[i%n])
			i++
		}
	})
}

func BenchmarkSyncMapWrite_1K(b *testing.B)   { benchSyncMapWrite(b, 1_000) }
func BenchmarkSyncMapWrite_10K(b *testing.B)  { benchSyncMapWrite(b, 10_000) }
func BenchmarkSyncMapWrite_100K(b *testing.B) { benchSyncMapWrite(b, 100_000) }

func benchSyncMapWrite(b *testing.B, n int) {
	var m sync.Map
	// Pre-populate
	for i := 0; i < n; i++ {
		m.Store(fmt.Sprintf("tenant-%d", i), i)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.Store(fmt.Sprintf("tenant-%d", i%n), i)
			i++
		}
	})
}

// --- Per-tenant mutex creation/cleanup at scale ---

func BenchmarkMutexPool_1K(b *testing.B)   { benchMutexPool(b, 1_000) }
func BenchmarkMutexPool_10K(b *testing.B)  { benchMutexPool(b, 10_000) }
func BenchmarkMutexPool_100K(b *testing.B) { benchMutexPool(b, 100_000) }

func benchMutexPool(b *testing.B, n int) {
	// Simulate creating and locking per-tenant mutexes
	var pool sync.Map
	tenantIDs := make([]string, n)
	for i := range tenantIDs {
		tenantIDs[i] = fmt.Sprintf("tenant-%d", i)
		pool.Store(tenantIDs[i], &sync.Mutex{})
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := tenantIDs[i%n]
			if v, ok := pool.Load(key); ok {
				mu := v.(*sync.Mutex)
				mu.Lock()
				mu.Unlock()
			}
			i++
		}
	})
}

// --- Random amount generation ---

func BenchmarkRandomAmount_NGN(b *testing.B) {
	for i := 0; i < b.N; i++ {
		randomAmount("NGN")
	}
}

func BenchmarkRandomAmount_GBP(b *testing.B) {
	for i := 0; i < b.N; i++ {
		randomAmount("GBP")
	}
}

// --- Error categorization ---

func BenchmarkCategorizeError(b *testing.B) {
	err := fmt.Errorf("http 429: rate limited")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		categorizeError(err)
	}
}

func BenchmarkCategorizeHTTPError(b *testing.B) {
	codes := []int{200, 400, 401, 429, 500, 502, 503}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		categorizeHTTPError(codes[i%len(codes)])
	}
}
