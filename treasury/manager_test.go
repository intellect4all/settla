package treasury

import (
	"context"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// --- Mock Store ---

type mockStore struct {
	mu        sync.Mutex
	positions []domain.Position
	updates   []updateRecord
	history   []historyRecord
}

type updateRecord struct {
	ID      uuid.UUID
	Balance decimal.Decimal
	Locked  decimal.Decimal
}

type historyRecord struct {
	PositionID  uuid.UUID
	TenantID    uuid.UUID
	Balance     decimal.Decimal
	Locked      decimal.Decimal
	TriggerType string
}

func (s *mockStore) LoadAllPositions(_ context.Context) ([]domain.Position, error) {
	return s.positions, nil
}

func (s *mockStore) UpdatePosition(_ context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, updateRecord{ID: id, Balance: balance, Locked: locked})
	return nil
}

func (s *mockStore) RecordHistory(_ context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, historyRecord{
		PositionID:  positionID,
		TenantID:    tenantID,
		Balance:     balance,
		Locked:      locked,
		TriggerType: triggerType,
	})
	return nil
}

func (s *mockStore) getUpdates() []updateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]updateRecord, len(s.updates))
	copy(out, s.updates)
	return out
}

// --- Mock EventPublisher ---

type mockPublisher struct {
	mu     sync.Mutex
	events []domain.Event
}

func (p *mockPublisher) Publish(_ context.Context, event domain.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

// --- Helpers ---

func newTestManager(t *testing.T, store *mockStore) *Manager {
	t.Helper()
	logger := slog.Default()
	pub := &mockPublisher{}
	m := NewManager(store, pub, logger, nil, WithFlushInterval(50*time.Millisecond))
	ctx := context.Background()
	if err := m.LoadPositions(ctx); err != nil {
		t.Fatalf("LoadPositions: %v", err)
	}
	return m
}

func testPosition(tenantID uuid.UUID, currency domain.Currency, location string, balance, locked int64) domain.Position {
	return domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      currency,
		Location:      location,
		Balance:       decimal.NewFromInt(balance),
		Locked:        decimal.NewFromInt(locked),
		MinBalance:    decimal.NewFromInt(100),
		TargetBalance: decimal.NewFromInt(10000),
		UpdatedAt:     time.Now().UTC(),
	}
}

// --- Tests ---

func TestReserveSufficient(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(5000), uuid.New())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	pos, err := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}

	// Available should be 10000 - 0 - 5000 = 5000
	expected := decimal.NewFromInt(5000)
	if !pos.Available().Equal(expected) {
		t.Errorf("expected available %s, got %s", expected, pos.Available())
	}
}

func TestReserveInsufficient(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 1000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1001), uuid.New())
	if err == nil {
		t.Fatal("expected error for insufficient funds")
	}

	// Position should be unchanged.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(1000)
	if !pos.Available().Equal(expected) {
		t.Errorf("expected available %s after failed reserve, got %s", expected, pos.Available())
	}
}

func TestReserveNonPositiveAmount(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.Zero, uuid.New())
	if err == nil {
		t.Fatal("expected error for zero amount")
	}

	err = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(-100), uuid.New())
	if err == nil {
		t.Fatal("expected error for negative amount")
	}
}

func TestReserveUnknownPosition(t *testing.T) {
	store := &mockStore{}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.Reserve(ctx, uuid.New(), domain.CurrencyUSD, "bank:unknown", decimal.NewFromInt(100), uuid.New())
	if err == nil {
		t.Fatal("expected error for unknown position")
	}
}

func TestRelease(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()
	amount := decimal.NewFromInt(3000)

	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, ref)

	// Available should be 7000.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(7000)) {
		t.Fatalf("expected 7000 available after reserve, got %s", pos.Available())
	}

	// Release.
	err := m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, ref)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Available should be back to 10000.
	pos, _ = m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(10000)) {
		t.Errorf("expected 10000 available after release, got %s", pos.Available())
	}
}

func TestReleaseExceedsReserved(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(100), uuid.New())

	err := m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(200), uuid.New())
	if err == nil {
		t.Fatal("expected error when release exceeds reserved")
	}
}

func TestCommitReservation(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	amount := decimal.NewFromInt(2000)
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())

	// Commit moves reserved → locked.
	err := m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
	if err != nil {
		t.Fatalf("CommitReservation: %v", err)
	}

	// Available should still be 8000 (balance=10000, locked=2000, reserved=0).
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(8000)) {
		t.Errorf("expected 8000 available after commit, got %s", pos.Available())
	}
}

func TestConcurrentReserves(t *testing.T) {
	tenantID := uuid.New()
	balance := int64(100000)
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", balance, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// 10,000 concurrent reserves of 10 each = 100,000 total.
	// Exactly all should succeed (total = balance).
	numGoroutines := 10000
	reserveAmount := decimal.NewFromInt(10)

	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", reserveAmount, uuid.New())
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	available := pos.Available()

	// Total reserved should never exceed balance.
	if available.IsNegative() {
		t.Fatalf("available went negative: %s — atomicity violated", available)
	}

	// With exact balance match, all 10,000 should succeed and available should be 0.
	if successCount != int64(numGoroutines) {
		t.Logf("success=%d (expected %d), available=%s", successCount, numGoroutines, available)
	}

	// Key invariant: successful reserves × amount + available = balance.
	totalReserved := reserveAmount.Mul(decimal.NewFromInt(successCount))
	if !totalReserved.Add(available).Equal(decimal.NewFromInt(balance)) {
		t.Errorf("invariant violated: reserved(%s) + available(%s) != balance(%d)",
			totalReserved, available, balance)
	}
}

func TestConcurrentReservesExceedBalance(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 1000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// 10,000 goroutines trying to reserve 1 each, but only 1000 available.
	numGoroutines := 10000
	reserveAmount := decimal.NewFromInt(1)

	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", reserveAmount, uuid.New())
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successCount > 1000 {
		t.Fatalf("over-reserved: %d successes with only 1000 available", successCount)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if pos.Available().IsNegative() {
		t.Fatalf("available went negative: %s", pos.Available())
	}
}

func TestTenantIsolation(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantA, domain.CurrencyUSD, "bank:chase", 5000, 0),
			testPosition(tenantB, domain.CurrencyUSD, "bank:chase", 3000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Reserve from tenant A.
	err := m.Reserve(ctx, tenantA, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(4000), uuid.New())
	if err != nil {
		t.Fatalf("Reserve tenant A: %v", err)
	}

	// Tenant B should be unaffected.
	posB, _ := m.GetPosition(ctx, tenantB, domain.CurrencyUSD, "bank:chase")
	if !posB.Available().Equal(decimal.NewFromInt(3000)) {
		t.Errorf("tenant B available changed: expected 3000, got %s", posB.Available())
	}

	// Tenant A should show 1000 available.
	posA, _ := m.GetPosition(ctx, tenantA, domain.CurrencyUSD, "bank:chase")
	if !posA.Available().Equal(decimal.NewFromInt(1000)) {
		t.Errorf("tenant A available: expected 1000, got %s", posA.Available())
	}
}

func TestFlushWritesToStore(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), uuid.New())

	// Manual flush.
	m.flushOnce()

	updates := store.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one store update after flush")
	}

	// Verify the flushed values.
	u := updates[0]
	if !u.Balance.Equal(decimal.NewFromInt(10000)) {
		t.Errorf("flushed balance: expected 10000, got %s", u.Balance)
	}
	// locked in DB = lockedMicro + reservedMicro = 0 + 3000 = 3000
	if !u.Locked.Equal(decimal.NewFromInt(3000)) {
		t.Errorf("flushed locked: expected 3000, got %s", u.Locked)
	}
}

func TestFlushClearsDirtyFlag(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), uuid.New())

	// First flush writes.
	m.flushOnce()
	firstCount := len(store.getUpdates())

	// Second flush should be a no-op (dirty cleared).
	m.flushOnce()
	secondCount := len(store.getUpdates())

	if secondCount != firstCount {
		t.Errorf("expected no additional writes after dirty cleared, got %d → %d", firstCount, secondCount)
	}
}

func TestCrashRecovery(t *testing.T) {
	tenantID := uuid.New()
	posID := uuid.New()

	// Simulate a crash: DB has balance=10000, locked=2000 (from previous committed reservations).
	// On reload, reserved resets to 0.
	store := &mockStore{
		positions: []domain.Position{
			{
				ID:            posID,
				TenantID:      tenantID,
				Currency:      domain.CurrencyUSD,
				Location:      "bank:chase",
				Balance:       decimal.NewFromInt(10000),
				Locked:        decimal.NewFromInt(2000),
				MinBalance:    decimal.NewFromInt(100),
				TargetBalance: decimal.NewFromInt(10000),
				UpdatedAt:     time.Now().UTC(),
			},
		},
	}

	// Create a fresh manager (simulates restart).
	m := newTestManager(t, store)
	ctx := context.Background()

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")

	// Available = balance - locked - reserved = 10000 - 2000 - 0 = 8000.
	if !pos.Available().Equal(decimal.NewFromInt(8000)) {
		t.Errorf("after crash recovery expected 8000 available, got %s", pos.Available())
	}

	// Can still reserve.
	err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(5000), uuid.New())
	if err != nil {
		t.Fatalf("Reserve after crash recovery: %v", err)
	}
}

func TestGetPositions(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
			testPosition(tenantID, domain.CurrencyGBP, "bank:hsbc", 3000, 0),
			testPosition(uuid.New(), domain.CurrencyEUR, "bank:bnp", 9000, 0), // different tenant
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	positions, err := m.GetPositions(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(positions) != 2 {
		t.Errorf("expected 2 positions for tenant, got %d", len(positions))
	}
}

func TestGetLiquidityReport(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			{
				ID:         uuid.New(),
				TenantID:   tenantID,
				Currency:   domain.CurrencyUSD,
				Location:   "bank:chase",
				Balance:    decimal.NewFromInt(5000),
				Locked:     decimal.NewFromInt(0),
				MinBalance: decimal.NewFromInt(1000),
				UpdatedAt:  time.Now().UTC(),
			},
			{
				ID:         uuid.New(),
				TenantID:   tenantID,
				Currency:   domain.CurrencyUSD,
				Location:   "bank:wells",
				Balance:    decimal.NewFromInt(500), // Below min
				Locked:     decimal.NewFromInt(0),
				MinBalance: decimal.NewFromInt(1000),
				UpdatedAt:  time.Now().UTC(),
			},
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	report, err := m.GetLiquidityReport(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetLiquidityReport: %v", err)
	}

	if report.TenantID != tenantID {
		t.Error("wrong tenant ID in report")
	}
	if len(report.Positions) != 2 {
		t.Errorf("expected 2 positions, got %d", len(report.Positions))
	}
	if len(report.AlertPositions) != 1 {
		t.Errorf("expected 1 alert position (below min), got %d", len(report.AlertPositions))
	}

	totalUSD := report.TotalAvailable[domain.CurrencyUSD]
	if !totalUSD.Equal(decimal.NewFromInt(5500)) {
		t.Errorf("expected total available USD 5500, got %s", totalUSD)
	}
}

func TestUpdateBalance(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Simulate a deposit.
	err := m.UpdateBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(15000))
	if err != nil {
		t.Fatalf("UpdateBalance: %v", err)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(15000)) {
		t.Errorf("expected 15000 available after balance update, got %s", pos.Available())
	}
}

func TestStartStop(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), uuid.New())

	m.Start()
	// Give the flush goroutine time to run at least once.
	time.Sleep(100 * time.Millisecond)
	m.Stop()

	// Final flush should have written to store.
	updates := store.getUpdates()
	if len(updates) == 0 {
		t.Error("expected store updates after Start/Stop cycle")
	}
}

func TestMicroConversion(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"1", 100_000_000},
		{"0.5", 50_000_000},
		{"0.00000001", 1},
		{"1234.56789012", 123_456_789_012},
		{"0", 0},
	}

	for _, tt := range tests {
		d, _ := decimal.NewFromString(tt.input)
		got := toMicro(d)
		if got != tt.expected {
			t.Errorf("toMicro(%s) = %d, want %d", tt.input, got, tt.expected)
		}

		// Round-trip.
		back := fromMicro(got)
		if !back.Equal(d.Truncate(8)) {
			t.Errorf("fromMicro(toMicro(%s)) = %s, want %s", tt.input, back, d.Truncate(8))
		}
	}
}

// --- Benchmark ---

func BenchmarkReserve(b *testing.B) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			{
				ID:         uuid.New(),
				TenantID:   tenantID,
				Currency:   domain.CurrencyUSD,
				Location:   "bank:chase",
				Balance:    decimal.NewFromInt(1_000_000_000), // 1 billion
				Locked:     decimal.Zero,
				MinBalance: decimal.Zero,
				UpdatedAt:  time.Now().UTC(),
			},
		},
	}
	logger := slog.Default()
	m := NewManager(store, nil, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)

	amount := decimal.NewFromInt(1)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
		}
	})
}
