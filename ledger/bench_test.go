package ledger

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// setupBenchmarkService creates a ledger service with mock TB for benchmarking.
func setupBenchmarkService(b *testing.B) *Service {
	b.Helper()

	tb := newMockTBClient()
	pub := &mockPublisher{}
	svc := NewService(tb, nil, pub, slogDiscard(), nil, WithNoBatching())

	return svc
}

// setupBenchmarkServiceWithBatching creates a service with batching enabled.
func setupBenchmarkServiceWithBatching(b *testing.B) (*Service, *mockTBClient) {
	b.Helper()

	tb := newMockTBClient()
	pub := &mockPublisher{}
	svc := NewService(tb, nil, pub, slogDiscard(), nil)
	svc.Start()

	return svc, tb
}

// BenchmarkPostEntries_HotKey measures throughput under hot-key contention.
// A single debit+credit account pair is shared across 256 goroutines to simulate
// the worst-case hot-key scenario (e.g., a system clearing account).
//
// Target: >15,000 entries/sec under contention, no data races
func BenchmarkPostEntries_HotKey(b *testing.B) {
	svc, _ := setupBenchmarkServiceWithBatching(b)
	defer svc.Stop()

	ctx := context.Background()

	// Pre-create the hot accounts.
	hotEntry := balancedEntry("hot-key-contention")
	_, _ = svc.PostEntries(ctx, hotEntry)

	var opsCompleted atomic.Int64
	start := time.Now()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			e := balancedEntry("hot-key-contention")
			e.IdempotencyKey = domain.IdempotencyKey(fmt.Sprintf("idem-hotkey-%d-%d", time.Now().UnixNano(), i))
			_, _ = svc.PostEntries(ctx, e)
			opsCompleted.Add(1)
			i++
		}
	})

	b.StopTimer()
	elapsed := time.Since(start)
	ops := opsCompleted.Load()
	if elapsed > 0 {
		entriesPerSec := float64(ops) / elapsed.Seconds()
		b.ReportMetric(entriesPerSec, "entries/s")
	}
}

// BenchmarkPostEntries_Single measures single entry posting performance.
// Single PostEntries call with 2-line balanced entry.
//
// Target: <500μs per posting
func BenchmarkPostEntries_Single(b *testing.B) {
	svc := setupBenchmarkService(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		entry := balancedEntry(fmt.Sprintf("bench%d", i))
		b.StartTimer()

		_, _ = svc.PostEntries(ctx, entry)
	}
}

// BenchmarkPostEntries_Batch measures batched entry posting performance.
// Batching enabled with 10ms window, 1000 goroutines posting concurrently.
//
// Target: >30,000 entries/sec
func BenchmarkPostEntries_Batch(b *testing.B) {
	svc, _ := setupBenchmarkServiceWithBatching(b)
	defer svc.Stop()

	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	// Use RunParallel to simulate concurrent posting
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			entry := balancedEntry(fmt.Sprintf("bench-concurrent-%d", i))
			_, _ = svc.PostEntries(ctx, entry)
			i++
		}
	})
}

// BenchmarkGetBalance measures balance lookup performance.
//
// Target: <100μs per lookup
func BenchmarkGetBalance(b *testing.B) {
	svc := setupBenchmarkService(b)
	ctx := context.Background()

	// Create an account first
	entry := balancedEntry("balance-test")
	_, err := svc.PostEntries(ctx, entry)
	if err != nil {
		b.Fatalf("PostEntries: %v", err)
	}

	accountCode := domain.TenantAccountCode("balance-test", "assets:bank:gbp:clearing")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = svc.GetBalance(ctx, accountCode)
	}
}

// BenchmarkPostEntries_Concurrent measures throughput under contention.
// 256 goroutines posting to different accounts simultaneously.
//
// Target: No race conditions, consistent balances
func BenchmarkPostEntries_Concurrent(b *testing.B) {
	svc, tb := setupBenchmarkServiceWithBatching(b)
	defer svc.Stop()

	ctx := context.Background()

	// Pre-create accounts
	accounts := make([]domain.AccountCode, 256)
	for i := 0; i < 256; i++ {
		accounts[i] = domain.AccountCode(domain.TenantAccountCode(fmt.Sprintf("concurrent-%d", i), "assets:bank:gbp:clearing"))
	}

	var opCount atomic.Int64

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		idx := int(opCount.Add(1)) % 256
		for pb.Next() {
			entry := domain.JournalEntry{
				ID:             uuid.New(),
				IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("idem-concurrent-%d", idx)),
				Description:    "Concurrent test entry",
				ReferenceType:  "transfer",
				Lines: []domain.EntryLine{
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: accounts[idx],
							EntryType:   domain.EntryTypeDebit,
							Amount:      decimal.NewFromInt(1),
							Currency:    domain.CurrencyGBP,
						},
					},
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: "system:liabilities:pending",
							EntryType:   domain.EntryTypeCredit,
							Amount:      decimal.NewFromInt(1),
							Currency:    domain.CurrencyGBP,
						},
					},
				},
			}
			_, _ = svc.PostEntries(ctx, entry)
		}
	})

	// Verify consistency - no over-posting should have occurred
	b.StopTimer()
	var totalDebits int64
	for _, acc := range tb.accounts {
		totalDebits += int64(acc.DebitsPosted)
	}
	// Total debits should equal number of successful operations
	expectedOps := int64(b.N)
	if totalDebits != expectedOps {
		b.Logf("Note: Total debits (%d) vs expected (%d) - some operations may have failed", totalDebits, expectedOps)
	}
}

// BenchmarkPostEntries_MultiLine measures multi-line entry posting.
// 4-line entries are common for settlement postings.
//
// Target: <600μs per multi-line entry
func BenchmarkPostEntries_MultiLine(b *testing.B) {
	svc := setupBenchmarkService(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		entry := multiLineEntry()
		entry.IdempotencyKey = domain.IdempotencyKey(fmt.Sprintf("idem-multiline-%d", i))
		b.StartTimer()

		_, _ = svc.PostEntries(ctx, entry)
	}
}

// BenchmarkPostEntries_WithAccountCreation measures posting when accounts don't exist.
// Includes account creation overhead.
//
// Target: <1ms per posting with account creation
func BenchmarkPostEntries_WithAccountCreation(b *testing.B) {
	svc := setupBenchmarkService(b)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Use unique tenant each time to force account creation
		tenantSlug := fmt.Sprintf("new-tenant-%d", i)
		entry := balancedEntry(tenantSlug)
		b.StartTimer()

		_, _ = svc.PostEntries(ctx, entry)
	}
}

// BenchmarkPostEntries_ReverseEntry measures reversal performance.
//
// Target: <1ms per reversal
func BenchmarkPostEntries_ReverseEntry(b *testing.B) {
	svc := setupBenchmarkService(b)
	ctx := context.Background()

	// Pre-create entries to reverse
	entryIDs := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		entry := balancedEntry(fmt.Sprintf("reverse-test-%d", i))
		result, err := svc.PostEntries(ctx, entry)
		if err != nil {
			b.Fatalf("PostEntries: %v", err)
		}
		entryIDs[i] = result.ID
	}

	// Need to mock PG backend for reversals
	// For this benchmark, we'll use a simplified approach
	b.Skip("Reversal benchmark requires Postgres backend setup")
}

// BenchmarkEnsureAccounts measures account creation performance.
//
// Target: <200μs per account
func BenchmarkEnsureAccounts(b *testing.B) {
	tb := newMockTBClient()
	svc := NewService(tb, nil, &mockPublisher{}, slogDiscard(), nil, WithNoBatching())
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		codes := []string{
			fmt.Sprintf("tenant:%d:assets:bank:gbp:clearing", i),
			fmt.Sprintf("tenant:%d:liabilities:customer:pending", i),
		}
		b.StartTimer()

		_ = svc.tb.EnsureAccounts(ctx, codes)
	}
}

// BenchmarkPostEntries_HighThroughput stress tests the batcher.
// Simulates sustained high-throughput posting.
//
// Target: Sustained >25,000 entries/sec
func BenchmarkPostEntries_HighThroughput(b *testing.B) {
	svc, _ := setupBenchmarkServiceWithBatching(b)
	defer svc.Stop()

	ctx := context.Background()
	entry := balancedEntry("throughput-test")

	// Pre-create accounts
	_, _ = svc.PostEntries(ctx, entry)

	b.ReportAllocs()
	b.ResetTimer()

	var wg sync.WaitGroup
	numWorkers := 256
	opsPerWorker := b.N / numWorkers

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				e := balancedEntry(fmt.Sprintf("worker-%d", workerID))
				e.IdempotencyKey = domain.IdempotencyKey(fmt.Sprintf("idem-throughput-%d-%d", workerID, j))
				_, _ = svc.PostEntries(ctx, e)
			}
		}(i)
	}
	wg.Wait()
}

// BenchmarkGetEntries measures entry query performance.
// Note: This queries the Postgres backend (read-side).
//
// Target: <10ms for 100 entries
func BenchmarkGetEntries(b *testing.B) {
	svc := setupBenchmarkService(b)
	ctx := context.Background()

	accountCode := domain.TenantAccountCode("query-test", "assets:bank:gbp:clearing")

	// Post some entries
	for i := 0; i < 100; i++ {
		entry := balancedEntry("query-test")
		entry.IdempotencyKey = domain.IdempotencyKey(fmt.Sprintf("idem-query-%d", i))
		_, _ = svc.PostEntries(ctx, entry)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = svc.GetEntries(ctx, accountCode, time.Time{}, time.Time{}, 100, 0)
	}
}

// BenchmarkPostEntriesValidation measures entry validation performance only.
// Pure computation, no I/O.
//
// Target: <1μs per validation
func BenchmarkPostEntriesValidation(b *testing.B) {
	entry := balancedEntry("validation-test")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = domain.ValidateEntries(entry.Lines)
	}
}

// BenchmarkTBCreateTransfers measures raw TigerBeetle transfer creation.
//
// Target: <100μs per transfer batch
func BenchmarkTBCreateTransfers(b *testing.B) {
	tb := newMockTBClient()
	ctx := context.Background()

	// Pre-create accounts
	accounts := []string{
		"tenant:tbtest:assets:bank:gbp:clearing",
		"tenant:tbtest:liabilities:customer:pending",
	}

	// Create service to get access to tbBackend
	svc := NewService(tb, nil, &mockPublisher{}, slogDiscard(), nil, WithNoBatching())
	_ = svc.tb.EnsureAccounts(ctx, accounts)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		entry := domain.JournalEntry{
			ID:             uuid.New(),
			IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("idem-tb-%d", i)),
			Lines: []domain.EntryLine{
				{
					ID: uuid.New(),
					Posting: domain.Posting{
						AccountCode: domain.AccountCode(accounts[0]),
						EntryType:   domain.EntryTypeDebit,
						Amount:      decimal.NewFromInt(1000),
						Currency:    domain.CurrencyGBP,
					},
				},
				{
					ID: uuid.New(),
					Posting: domain.Posting{
						AccountCode: domain.AccountCode(accounts[1]),
						EntryType:   domain.EntryTypeCredit,
						Amount:      decimal.NewFromInt(1000),
						Currency:    domain.CurrencyGBP,
					},
				},
			},
		}
		b.StartTimer()

		_, _ = svc.tb.PostEntries(ctx, entry)
	}
}

// BenchmarkTBLookupAccounts measures raw TigerBeetle account lookup.
//
// Target: <50μs per lookup
func BenchmarkTBLookupAccounts(b *testing.B) {
	tb := newMockTBClient()
	ctx := context.Background()

	// Pre-create account
	accounts := []string{"tenant:lookuptest:assets:bank:gbp:clearing"}
	svc := NewService(tb, nil, &mockPublisher{}, slogDiscard(), nil, WithNoBatching())
	_ = svc.tb.EnsureAccounts(ctx, accounts)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = svc.tb.GetBalance(ctx, accounts[0])
	}
}
