package treasury

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// setupBenchmarkManager creates a manager with test positions for benchmarking.
func setupBenchmarkManager(b *testing.B, positions []domain.Position) (*Manager, *mockStore) {
	b.Helper()

	store := &mockStore{
		positions: positions,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub := &mockPublisher{}
	m := NewManager(store, pub, logger, nil, WithFlushInterval(100*time.Millisecond))

	ctx := context.Background()
	if err := m.LoadPositions(ctx); err != nil {
		b.Fatalf("LoadPositions: %v", err)
	}

	return m, store
}

// BenchmarkReserve_Single measures a single Reserve call.
// This is the hot path - must be extremely fast with no DB hit.
//
// Target: <1μs (1000ns) per reserve
func BenchmarkReserve_Single(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000000, 0),
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(100)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Reset position state every 1000 iterations to avoid running out of balance
		if i%1000 == 0 && i > 0 {
			b.StopTimer()
			ps, _ := m.getPosition(tenantID, domain.CurrencyUSD, "bank:chase")
			ps.reservedMicro.Store(0)
			b.StartTimer()
		}
		_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
	}
}

// BenchmarkReserve_Concurrent measures Reserve throughput with concurrent access.
// 1000 goroutines reserving from the same position.
//
// Target: >100,000 reserves/sec (should be trivial for in-memory CAS)
func BenchmarkReserve_Concurrent(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 1000000000, 0), // Large balance
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(1) // Reserve 1 unit at a time

	// Reset position before benchmark
	ps, _ := m.getPosition(tenantID, domain.CurrencyUSD, "bank:chase")

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Reset if we're running low on balance (but this shouldn't happen in b.N iterations)
			if ps.reservedMicro.Load() > ps.balanceMicro.Load()-int64(1000000) {
				ps.reservedMicro.Store(0)
			}
			_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
		}
	})

	// Verify no over-reservation after benchmark
	b.StopTimer()
	reserved := fromMicro(ps.reservedMicro.Load())
	balance := fromMicro(ps.balanceMicro.Load())
	if reserved.GreaterThan(balance) {
		b.Fatalf("OVER-RESERVATION DETECTED: reserved %s > balance %s", reserved, balance)
	}
}

// BenchmarkReserve_Concurrent_MultiTenant measures cross-tenant throughput.
// 10 tenants × 100 goroutines each = 1000 concurrent reservers.
//
// Target: Should scale linearly with tenant count (no cross-tenant interference)
func BenchmarkReserve_Concurrent_MultiTenant(b *testing.B) {
	const numTenants = 10
	const goroutinesPerTenant = 100

	positions := make([]domain.Position, numTenants)
	tenantIDs := make([]uuid.UUID, numTenants)

	for i := 0; i < numTenants; i++ {
		tenantIDs[i] = uuid.MustParse(fmt.Sprintf("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a%02d", i+1))
		positions[i] = testPosition(tenantIDs[i], domain.CurrencyUSD, "bank:chase", 1000000000, 0)
	}

	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(1)

	b.ReportAllocs()
	b.ResetTimer()

	var wg sync.WaitGroup
	// Use atomic counter to distribute work
	var counter atomic.Int64

	for i := 0; i < numTenants; i++ {
		for j := 0; j < goroutinesPerTenant; j++ {
			wg.Add(1)
			go func(tenantIdx int) {
				defer wg.Done()
				tenantID := tenantIDs[tenantIdx]
				ps, _ := m.getPosition(tenantID, domain.CurrencyUSD, "bank:chase")

				for {
					c := counter.Add(1)
					if c > int64(b.N) {
						break
					}
					// Reset if running low
					if ps.reservedMicro.Load() > ps.balanceMicro.Load()-int64(1000000) {
						ps.reservedMicro.Store(0)
					}
					_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
				}
			}(i)
		}
	}
	wg.Wait()

	// Verify no cross-tenant interference
	b.StopTimer()
	for i := 0; i < numTenants; i++ {
		ps, _ := m.getPosition(tenantIDs[i], domain.CurrencyUSD, "bank:chase")
		reserved := fromMicro(ps.reservedMicro.Load())
		balance := fromMicro(ps.balanceMicro.Load())
		if reserved.GreaterThan(balance) {
			b.Fatalf("Tenant %d OVER-RESERVATION: reserved %s > balance %s", i, reserved, balance)
		}
	}
}

// BenchmarkRelease measures Release performance.
//
// Target: <1μs per release (same as Reserve)
func BenchmarkRelease(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000000, 0),
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(100)

	// Pre-reserve amounts to release
	for i := 0; i < 10000; i++ {
		_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
	}

	b.ReportAllocs()
	b.ResetTimer()

	released := 0
	for i := 0; i < b.N; i++ {
		if released >= 10000 {
			b.StopTimer()
			// Re-reserve more
			for j := 0; j < 10000; j++ {
				_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
			}
			released = 0
			b.StartTimer()
		}
		_ = m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
		released++
	}
}

// BenchmarkCommitReservation measures CommitReservation performance.
// Moves reserved → locked.
//
// Target: <1μs per commit
func BenchmarkCommitReservation(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000000, 0),
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(100)

	// Pre-reserve amounts to commit
	refs := make([]uuid.UUID, 10000)
	for i := 0; i < 10000; i++ {
		refs[i] = uuid.New()
		_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, refs[i])
	}

	b.ReportAllocs()
	b.ResetTimer()

	committed := 0
	for i := 0; i < b.N; i++ {
		if committed >= 10000 {
			b.StopTimer()
			// Re-reserve more
			for j := 0; j < 10000; j++ {
				refs[j] = uuid.New()
				_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, refs[j])
			}
			committed = 0
			b.StartTimer()
		}
		_ = m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, refs[committed])
		committed++
	}
}

// BenchmarkGetPosition measures position lookup performance.
//
// Target: <500ns per lookup
func BenchmarkGetPosition(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000000, 0),
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	}
}

// BenchmarkFlush measures the flush goroutine performance.
// How long does it take to write 1000 position updates to Postgres in a single batch.
//
// Target: <50ms to flush 1000 positions
func BenchmarkFlush(b *testing.B) {
	// Create 1000 positions
	const numPositions = 1000
	positions := make([]domain.Position, numPositions)
	for i := 0; i < numPositions; i++ {
		tenantID := uuid.MustParse(fmt.Sprintf("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a%02x", i%256))
		positions[i] = testPosition(tenantID, domain.CurrencyUSD, fmt.Sprintf("bank:%d", i), 1000000, 0)
	}

	store := &mockStore{
		positions: positions,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub := &mockPublisher{}
	m := NewManager(store, pub, logger, nil, WithFlushInterval(1*time.Hour)) // Long interval - manual flush

	ctx := context.Background()
	if err := m.LoadPositions(ctx); err != nil {
		b.Fatalf("LoadPositions: %v", err)
	}

	// Mark all positions as dirty
	for _, ps := range m.allPositionStates() {
		ps.dirty.Store(true)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Reset dirty flags
		for _, ps := range m.allPositionStates() {
			ps.dirty.Store(true)
		}
		store.updates = nil
		b.StartTimer()

		m.flushOnce()
	}

	// Verify all positions were flushed
	if len(store.updates) != numPositions {
		b.Fatalf("expected %d updates, got %d", numPositions, len(store.updates))
	}
}

// BenchmarkReserveConcurrentContention measures worst-case contention.
// All goroutines hammering the same position with CAS failures and retries.
//
// Target: Still >50,000 reserves/sec even under extreme contention
func BenchmarkReserveConcurrentContention(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 100000000, 0),
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(1)

	ps, _ := m.getPosition(tenantID, domain.CurrencyUSD, "bank:chase")

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if ps.reservedMicro.Load() > ps.balanceMicro.Load()-int64(1000000) {
				// Try to reset (may have races, that's OK for this benchmark)
				ps.reservedMicro.Store(0)
			}
			_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
		}
	})
}

// BenchmarkGetLiquidityReport measures liquidity report generation.
//
// Target: <10ms for 1000 positions
func BenchmarkGetLiquidityReport(b *testing.B) {
	// Create positions for a single tenant
	const numPositions = 100
	positions := make([]domain.Position, numPositions)
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")

	currencies := []domain.Currency{domain.CurrencyUSD, domain.CurrencyGBP, domain.CurrencyEUR, domain.CurrencyNGN}
	locations := []string{"bank:chase", "bank:barclays", "crypto:tron", "crypto:ethereum"}

	for i := 0; i < numPositions; i++ {
		positions[i] = testPosition(
			tenantID,
			currencies[i%len(currencies)],
			locations[i%len(locations)],
			1000000,
			0,
		)
	}

	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.GetLiquidityReport(ctx, tenantID)
	}
}

// BenchmarkUpdateBalance measures balance update performance.
// Called by ledger sync.
//
// Target: <500ns per update
func BenchmarkUpdateBalance(b *testing.B) {
	tenantID := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01")
	positions := []domain.Position{
		testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000000, 0),
	}
	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()

	newBalance := decimal.NewFromInt(5000000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = m.UpdateBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", newBalance)
	}
}

// BenchmarkReserve_100KTenants measures Reserve throughput with 100K tenants
// (~500K positions). Validates that CAS performance and map lookups scale.
//
// Target: Reserve latency unchanged vs single-tenant (<1μs per reserve)
func BenchmarkReserve_100KTenants(b *testing.B) {
	const numTenants = 100_000
	const positionsPerTenant = 5

	positions := make([]domain.Position, 0, numTenants*positionsPerTenant)
	tenantIDs := make([]uuid.UUID, numTenants)

	currencies := []domain.Currency{domain.CurrencyUSD, domain.CurrencyGBP, domain.CurrencyEUR, domain.CurrencyNGN, domain.Currency("USDT")}
	locations := []string{"bank:a", "bank:b", "crypto:tron", "crypto:eth", "crypto:sol"}

	for i := 0; i < numTenants; i++ {
		tenantIDs[i] = uuid.New()
		for j := 0; j < positionsPerTenant; j++ {
			positions = append(positions, testPosition(tenantIDs[i], currencies[j], locations[j], 1000000, 0))
		}
	}

	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()
	amount := decimal.NewFromInt(1)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tid := tenantIDs[i%numTenants]
			ci := i % positionsPerTenant
			ps, _ := m.getPosition(tid, currencies[ci], locations[ci])
			if ps != nil && ps.reservedMicro.Load() > ps.balanceMicro.Load()-int64(1000000) {
				ps.reservedMicro.Store(0)
			}
			_ = m.Reserve(ctx, tid, currencies[ci], locations[ci], amount, uuid.New())
			i++
		}
	})
}

// BenchmarkGetPositions_100KTenants measures tenant position lookup with 100K tenants.
// Validates the per-tenant index is O(tenant positions) not O(all positions).
//
// Target: <1μs per lookup (same as single-tenant)
func BenchmarkGetPositions_100KTenants(b *testing.B) {
	const numTenants = 100_000
	const positionsPerTenant = 5

	positions := make([]domain.Position, 0, numTenants*positionsPerTenant)
	tenantIDs := make([]uuid.UUID, numTenants)

	currencies := []domain.Currency{domain.CurrencyUSD, domain.CurrencyGBP, domain.CurrencyEUR, domain.CurrencyNGN, domain.Currency("USDT")}
	locations := []string{"bank:a", "bank:b", "crypto:tron", "crypto:eth", "crypto:sol"}

	for i := 0; i < numTenants; i++ {
		tenantIDs[i] = uuid.New()
		for j := 0; j < positionsPerTenant; j++ {
			positions = append(positions, testPosition(tenantIDs[i], currencies[j], locations[j], 1000000, 0))
		}
	}

	m, _ := setupBenchmarkManager(b, positions)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.GetPositions(ctx, tenantIDs[i%numTenants])
	}
}

// BenchmarkFlush_DirtySet measures flush performance when only a small fraction
// of 500K positions are dirty. Validates O(dirty) not O(all).
//
// Target: <5ms to flush 1000 dirty out of 500K total
func BenchmarkFlush_DirtySet(b *testing.B) {
	const numTenants = 100_000
	const positionsPerTenant = 5
	const dirtyCount = 1000

	positions := make([]domain.Position, 0, numTenants*positionsPerTenant)
	tenantIDs := make([]uuid.UUID, numTenants)

	currencies := []domain.Currency{domain.CurrencyUSD, domain.CurrencyGBP, domain.CurrencyEUR, domain.CurrencyNGN, domain.Currency("USDT")}
	locations := []string{"bank:a", "bank:b", "crypto:tron", "crypto:eth", "crypto:sol"}

	for i := 0; i < numTenants; i++ {
		tenantIDs[i] = uuid.New()
		for j := 0; j < positionsPerTenant; j++ {
			positions = append(positions, testPosition(tenantIDs[i], currencies[j], locations[j], 1000000, 0))
		}
	}

	store := &mockStore{positions: positions}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub := &mockPublisher{}
	m := NewManager(store, pub, logger, nil, WithFlushInterval(1*time.Hour))

	ctx := context.Background()
	if err := m.LoadPositions(ctx); err != nil {
		b.Fatalf("LoadPositions: %v", err)
	}

	// Pre-select positions to mark dirty each iteration.
	dirtyTenants := tenantIDs[:dirtyCount]
	amount := decimal.NewFromInt(1)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store.mu.Lock()
		store.updates = nil
		store.mu.Unlock()
		// Mark dirtyCount positions as dirty via Reserve.
		for _, tid := range dirtyTenants {
			_ = m.Reserve(ctx, tid, domain.CurrencyUSD, "bank:a", amount, uuid.New())
		}
		b.StartTimer()

		m.flushOnce()
	}
}
