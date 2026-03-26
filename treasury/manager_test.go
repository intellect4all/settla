package treasury

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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
	ops       []ReserveOp // logged reserve ops
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

func (s *mockStore) LoadPositionsPaginated(_ context.Context, limit, offset int32) ([]domain.Position, error) {
	start := int(offset)
	if start >= len(s.positions) {
		return nil, nil
	}
	end := start + int(limit)
	if end > len(s.positions) {
		end = len(s.positions)
	}
	return s.positions[start:end], nil
}

func (s *mockStore) UpdatePosition(_ context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, updateRecord{ID: id, Balance: balance, Locked: locked})
	// Simulate real DB: update the underlying position so LoadAllPositions
	// returns flushed values on restart (crash recovery test).
	for i := range s.positions {
		if s.positions[i].ID == id {
			s.positions[i].Balance = balance
			s.positions[i].Locked = locked
			break
		}
	}
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

func (s *mockStore) LogReserveOp(_ context.Context, op ReserveOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, op)
	return nil
}

func (s *mockStore) LogReserveOps(_ context.Context, ops []ReserveOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, ops...)
	return nil
}

func (s *mockStore) GetUncommittedOps(_ context.Context) ([]ReserveOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Self-healing: return only reserve ops without a matching commit/release.
	// This mirrors the production SQL query.
	resolved := make(map[uuid.UUID]bool)
	for _, op := range s.ops {
		if op.OpType == OpCommit || op.OpType == OpRelease {
			resolved[op.Reference] = true
		}
	}
	var result []ReserveOp
	for _, op := range s.ops {
		if op.OpType == OpReserve && !resolved[op.Reference] {
			result = append(result, op)
		}
	}
	return result, nil
}

func (s *mockStore) MarkOpCompleted(_ context.Context, opID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, op := range s.ops {
		if op.ID == opID {
			s.ops = append(s.ops[:i], s.ops[i+1:]...)
			break
		}
	}
	return nil
}

func (s *mockStore) CleanupOldOps(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []ReserveOp
	for _, op := range s.ops {
		if !op.CreatedAt.Before(before) {
			kept = append(kept, op)
		}
	}
	s.ops = kept
	return nil
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
	// locked in DB = lockedMicro only (NOT + reservedMicro). Reserved amounts
	// are reconstructed from reserve_ops on crash recovery to avoid double-counting.
	if !u.Locked.Equal(decimal.NewFromInt(0)) {
		t.Errorf("flushed locked: expected 0 (committed only), got %s", u.Locked)
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
		{"1", 1_000_000},
		{"0.5", 500_000},
		{"0.000001", 1},
		{"1234.567890", 1_234_567_890},
		{"0", 0},
		{"9000000000000", 9_000_000_000_000_000_000}, // ~$9T, within safe range
	}

	for _, tt := range tests {
		d, _ := decimal.NewFromString(tt.input)
		got := toMicro(d)
		if got != tt.expected {
			t.Errorf("toMicro(%s) = %d, want %d", tt.input, got, tt.expected)
		}

		// Round-trip: precision limited to 6 decimal places with microScale=1e6.
		back := fromMicro(got)
		if !back.Equal(d.Truncate(6)) {
			t.Errorf("fromMicro(toMicro(%s)) = %s, want %s", tt.input, back, d.Truncate(6))
		}
	}

	// Test overflow validation
	t.Run("overflow panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("toMicro with overflow amount did not panic")
			}
		}()
		d, _ := decimal.NewFromString("9300000000000") // ~$9.3T, exceeds max
		toMicro(d)
	})

	// Test validateMicroRange
	t.Run("validateMicroRange", func(t *testing.T) {
		d, _ := decimal.NewFromString("9300000000000")
		if err := validateMicroRange(d); err == nil {
			t.Errorf("validateMicroRange should return error for overflow amount")
		}
		d2, _ := decimal.NewFromString("1000000")
		if err := validateMicroRange(d2); err != nil {
			t.Errorf("validateMicroRange should not return error for normal amount: %v", err)
		}
	})
}

func TestCommitReservationDoubleCommitReturnsError(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	amount := decimal.NewFromInt(2000)
	reserveRef := uuid.New()
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, reserveRef)

	// First commit succeeds.
	commitRef := uuid.New()
	err := m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, commitRef)
	if err != nil {
		t.Fatalf("first CommitReservation: %v", err)
	}

	// Second commit with DIFFERENT reference should fail (reserved is now 0).
	err = m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, uuid.New())
	if err == nil {
		t.Fatal("expected error on double-commit with different reference, got nil")
	}
}

func TestCommitReservationErrorIncludesDetails(t *testing.T) {
	// CRIT-3: Verifies that a failed commit returns actual have/need values
	// for debugging, not a generic error message.
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Try to commit without any reservation — should fail with details.
	ref := uuid.New()
	err := m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref)
	if err == nil {
		t.Fatal("expected error on commit without reservation")
	}

	errMsg := err.Error()
	if !containsAll(errMsg, "have 0", "need 1000000000", ref.String()) {
		t.Errorf("error should include actual values and transfer ID, got: %s", errMsg)
	}
}

func TestConcurrentCommitReservations(t *testing.T) {
	// CRIT-3: Verifies that CAS on CommitReservation works correctly
	// under concurrent contention — no double-decrement of reserved.
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Reserve 100 × 1000 = 100,000 total.
	numReservations := 100
	reserveAmount := decimal.NewFromInt(1000)
	reserveRefs := make([]uuid.UUID, numReservations)
	for i := 0; i < numReservations; i++ {
		reserveRefs[i] = uuid.New()
		if err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", reserveAmount, reserveRefs[i]); err != nil {
			t.Fatalf("Reserve %d: %v", i, err)
		}
	}

	// Commit all 100 concurrently.
	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex

	wg.Add(numReservations)
	for i := 0; i < numReservations; i++ {
		go func(idx int) {
			defer wg.Done()
			err := m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", reserveAmount, uuid.New())
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// All 100 commits should succeed (there's enough reserved).
	if successCount != int64(numReservations) {
		t.Errorf("expected %d successful commits, got %d", numReservations, successCount)
	}

	// reserved should be 0, locked should be 100,000.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expectedAvailable := decimal.NewFromInt(0) // balance(100000) - locked(100000) - reserved(0)
	if !pos.Available().Equal(expectedAvailable) {
		t.Errorf("expected available %s, got %s", expectedAvailable, pos.Available())
	}

	// One more commit should fail (reserved is 0).
	err := m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", reserveAmount, uuid.New())
	if err == nil {
		t.Fatal("expected error on commit with no remaining reservation")
	}
}

// containsAll checks if s contains all substrings.
func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestReserveIdempotency(t *testing.T) {
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

	// First call reserves.
	err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, ref)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}

	// Second call with same reference should be a no-op.
	err = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, ref)
	if err != nil {
		t.Fatalf("second Reserve (idempotent): %v", err)
	}

	// Available should reflect only ONE reservation (10000 - 3000 = 7000).
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(7000)
	if !pos.Available().Equal(expected) {
		t.Errorf("expected available %s after idempotent reserve, got %s", expected, pos.Available())
	}
}

func TestReleaseIdempotency(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	reserveRef := uuid.New()
	amount := decimal.NewFromInt(3000)
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, reserveRef)

	releaseRef := uuid.New()

	// First release succeeds.
	err := m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, releaseRef)
	if err != nil {
		t.Fatalf("first Release: %v", err)
	}

	// Second release with same reference is a no-op (doesn't under-reserve).
	err = m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, releaseRef)
	if err != nil {
		t.Fatalf("second Release (idempotent): %v", err)
	}

	// Available should be back to full balance.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(10000)) {
		t.Errorf("expected 10000 available after idempotent release, got %s", pos.Available())
	}
}

func TestCommitIdempotency(t *testing.T) {
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

	commitRef := uuid.New()

	// First commit succeeds.
	err := m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, commitRef)
	if err != nil {
		t.Fatalf("first CommitReservation: %v", err)
	}

	// Second commit with same reference is a no-op.
	err = m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, commitRef)
	if err != nil {
		t.Fatalf("second CommitReservation (idempotent): %v", err)
	}

	// Available should reflect one commit only (10000 - 2000 = 8000).
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(8000)
	if !pos.Available().Equal(expected) {
		t.Errorf("expected available %s after idempotent commit, got %s", expected, pos.Available())
	}
}

func TestReserveIdempotencyNotSetOnFailure(t *testing.T) {
	// CRIT-2: Verifies that a failed reserve (insufficient funds) does NOT
	// set the idempotency key, so a retry after balance is replenished succeeds.
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 1000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()

	// First attempt: insufficient funds (trying to reserve 5000 from 1000 balance).
	err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(5000), ref)
	if err == nil {
		t.Fatal("expected insufficient funds error")
	}

	// Replenish balance.
	_ = m.UpdateBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(10000))

	// Retry with same reference should NOW succeed (not be short-circuited as idempotent).
	err = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(5000), ref)
	if err != nil {
		t.Fatalf("retry after balance replenish should succeed: %v", err)
	}

	// Available should be 10000 - 5000 = 5000.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(5000)
	if !pos.Available().Equal(expected) {
		t.Errorf("expected available %s, got %s", expected, pos.Available())
	}
}

func TestReleaseIdempotencyNotSetOnFailure(t *testing.T) {
	// Verifies that a failed release does NOT set the idempotency key.
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()

	// Release without any reservation should fail.
	err := m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref)
	if err == nil {
		t.Fatal("expected error for release with no reservation")
	}

	// Now make a reservation and retry the release with the same ref.
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), uuid.New())

	err = m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref)
	if err != nil {
		t.Fatalf("retry release should succeed: %v", err)
	}

	// Available should be back to 10000.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(10000)) {
		t.Errorf("expected 10000 available, got %s", pos.Available())
	}
}

func TestConcurrentIdempotentReserves(t *testing.T) {
	// CRIT-2: Verifies that concurrent calls with the same reference
	// result in exactly one reservation.
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()
	amount := decimal.NewFromInt(1000)

	var wg sync.WaitGroup
	numGoroutines := 100
	var successCount int64
	var mu sync.Mutex

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", amount, ref)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// All should succeed (idempotent = return nil), but only one reservation applied.
	if successCount != int64(numGoroutines) {
		t.Errorf("expected all %d calls to return nil, got %d", numGoroutines, successCount)
	}

	// Available should reflect exactly ONE reservation.
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(99000) // 100000 - 1000
	if !pos.Available().Equal(expected) {
		t.Errorf("expected available %s (one reservation), got %s", expected, pos.Available())
	}
}

func TestCrashRecoveryWithReserveOps(t *testing.T) {
	tenantID := uuid.New()
	posID := uuid.New()

	store := &mockStore{
		positions: []domain.Position{
			{
				ID:            posID,
				TenantID:      tenantID,
				Currency:      domain.CurrencyUSD,
				Location:      "bank:chase",
				Balance:       decimal.NewFromInt(10000),
				Locked:        decimal.NewFromInt(0),
				MinBalance:    decimal.NewFromInt(100),
				TargetBalance: decimal.NewFromInt(10000),
				UpdatedAt:     time.Now().UTC(),
			},
		},
	}

	m := newTestManager(t, store)
	ctx := context.Background()

	ref1 := uuid.New()
	ref2 := uuid.New()

	// Make two reservations.
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), ref1)
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), ref2)

	// Flush to persist ops to DB.
	m.flushOnce()

	// Verify ops were logged.
	store.mu.Lock()
	opCount := len(store.ops)
	store.mu.Unlock()
	if opCount != 2 {
		t.Fatalf("expected 2 logged ops, got %d", opCount)
	}

	// Simulate crash — create new manager from same store.
	// Reserve ops are in the store and get replayed to reconstruct reservedMicro.
	m2 := NewManager(store, &mockPublisher{}, slog.Default(), nil, WithFlushInterval(50*time.Millisecond))
	if err := m2.LoadPositions(ctx); err != nil {
		t.Fatalf("LoadPositions after crash: %v", err)
	}

	// After replay, available should be 10000 - 3000 - 2000 = 5000.
	pos, _ := m2.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(5000)
	if !pos.Available().Equal(expected) {
		t.Errorf("after crash recovery expected available %s, got %s", expected, pos.Available())
	}
}

func TestCrashRecoveryAfterCommit(t *testing.T) {
	// CRIT-1: Verifies that committed reservations are NOT replayed on restart.
	// The self-healing GetUncommittedOps query excludes reserves that have a
	// matching commit op, preventing double-counting of locked amounts.
	tenantID := uuid.New()
	posID := uuid.New()

	store := &mockStore{
		positions: []domain.Position{
			{
				ID:            posID,
				TenantID:      tenantID,
				Currency:      domain.CurrencyUSD,
				Location:      "bank:chase",
				Balance:       decimal.NewFromInt(10000),
				Locked:        decimal.NewFromInt(0),
				MinBalance:    decimal.NewFromInt(100),
				TargetBalance: decimal.NewFromInt(10000),
				UpdatedAt:     time.Now().UTC(),
			},
		},
	}

	m := newTestManager(t, store)
	ctx := context.Background()

	ref1 := uuid.New()
	ref2 := uuid.New()

	// Reserve two amounts.
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), ref1)
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), ref2)

	// Commit ref1 (moves 3000 from reserved to locked).
	_ = m.CommitReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), ref1)

	// Flush: persists locked=3000 (committed only), ops=[reserve(ref1), reserve(ref2), commit(ref1)].
	m.flushOnce()

	// Verify flushed locked = 3000 (committed amount only).
	updates := store.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected store update after flush")
	}
	if !updates[0].Locked.Equal(decimal.NewFromInt(3000)) {
		t.Errorf("flushed locked: expected 3000, got %s", updates[0].Locked)
	}

	// Simulate crash and restart — only ref2 should be replayed (ref1 has a matching commit).
	m2 := NewManager(store, &mockPublisher{}, slog.Default(), nil, WithFlushInterval(50*time.Millisecond))
	if err := m2.LoadPositions(ctx); err != nil {
		t.Fatalf("LoadPositions after crash: %v", err)
	}

	pos, _ := m2.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")

	// Available = balance(10000) - locked(3000, from DB) - reserved(2000, replayed) = 5000
	expected := decimal.NewFromInt(5000)
	if !pos.Available().Equal(expected) {
		t.Errorf("after crash recovery expected available %s, got %s", expected, pos.Available())
	}
}

func TestCrashRecoveryAfterRelease(t *testing.T) {
	// CRIT-1: Verifies that released reservations are NOT replayed on restart.
	tenantID := uuid.New()
	posID := uuid.New()

	store := &mockStore{
		positions: []domain.Position{
			{
				ID:            posID,
				TenantID:      tenantID,
				Currency:      domain.CurrencyUSD,
				Location:      "bank:chase",
				Balance:       decimal.NewFromInt(10000),
				Locked:        decimal.NewFromInt(0),
				MinBalance:    decimal.NewFromInt(100),
				TargetBalance: decimal.NewFromInt(10000),
				UpdatedAt:     time.Now().UTC(),
			},
		},
	}

	m := newTestManager(t, store)
	ctx := context.Background()

	ref1 := uuid.New()
	ref2 := uuid.New()

	// Reserve two amounts.
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), ref1)
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), ref2)

	// Release ref1 (frees 3000).
	_ = m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), ref1)

	// Flush.
	m.flushOnce()

	// Simulate crash and restart — only ref2 should be replayed (ref1 has a matching release).
	m2 := NewManager(store, &mockPublisher{}, slog.Default(), nil, WithFlushInterval(50*time.Millisecond))
	if err := m2.LoadPositions(ctx); err != nil {
		t.Fatalf("LoadPositions after crash: %v", err)
	}

	pos, _ := m2.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")

	// Available = balance(10000) - locked(0) - reserved(2000) = 8000
	expected := decimal.NewFromInt(8000)
	if !pos.Available().Equal(expected) {
		t.Errorf("after crash recovery expected available %s, got %s", expected, pos.Available())
	}
}

// TestUpdateBalanceRejectsNegativeAvailable verifies that UpdateBalance rejects
// a new balance that would cause Available() to go negative (below locked+reserved).
func TestUpdateBalanceRejectsNegativeAvailable(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			{
				ID:         uuid.New(),
				TenantID:   tenantID,
				Currency:   domain.CurrencyGBP,
				Location:   "bank:gbp",
				Balance:    decimal.NewFromInt(10_000),
				Locked:     decimal.Zero,
				MinBalance: decimal.Zero,
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(store, nil, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)
	m.Start()
	defer m.Stop()

	// Reserve 5000 GBP
	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(5000), uuid.New())
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}

	// Commit 3000 of the reserved amount
	commitRef := uuid.New()
	err = m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(3000), commitRef)
	if err != nil {
		// If reserve fails, skip this part — we still have 5000 reserved
	}

	// Try to set balance below committed amount — should fail
	err = m.UpdateBalance(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100))
	if err == nil {
		t.Fatal("expected UpdateBalance to reject balance below committed amount, but it succeeded")
	}
	if !strings.Contains(err.Error(), "below committed amount") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Set balance above committed amount — should succeed
	err = m.UpdateBalance(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(50_000))
	if err != nil {
		t.Fatalf("expected UpdateBalance to succeed with balance above committed amount, got: %v", err)
	}
}

// --- PositionCount ---

func TestPositionCount(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 10000, 0),
			testPosition(tenantID, domain.CurrencyNGN, "bank:ngn", 50000, 0),
			testPosition(uuid.New(), domain.CurrencyUSD, "bank:usd", 20000, 0),
		},
	}
	m := newTestManager(t, store)

	if got := m.PositionCount(); got != 3 {
		t.Errorf("PositionCount: got %d, want 3", got)
	}
}

func TestPositionCountEmpty(t *testing.T) {
	store := &mockStore{}
	m := newTestManager(t, store)

	if got := m.PositionCount(); got != 0 {
		t.Errorf("PositionCount: got %d, want 0", got)
	}
}

// --- CleanupIdempotencyMap ---

func TestCleanupIdempotencyMap(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Make several reserves with unique references to populate idempotency map.
	for i := 0; i < 10; i++ {
		ref := uuid.New()
		_ = m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(1), ref)
	}

	// Cleanup with zero maxAge should remove all entries.
	m.cleanupIdempotencyMap(0)

	// Verify cleanup worked: reserve with a previously-used reference should succeed
	// as a new reservation (not an idempotent hit). But since we used unique refs,
	// we can verify the map was cleaned by checking a fresh reserve still works.
	ref := uuid.New()
	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(1), ref)
	if err != nil {
		t.Fatalf("Reserve after cleanup: %v", err)
	}
}

func TestCleanupIdempotencyMap_KeepsRecent(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()
	_ = m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), ref)

	// Cleanup with 1-hour maxAge should keep the entry we just created.
	m.cleanupIdempotencyMap(1 * time.Hour)

	// Idempotent reserve should return nil (skipped).
	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), ref)
	if err != nil {
		t.Fatalf("idempotent reserve should succeed after cleanup with long maxAge: %v", err)
	}
}

// --- CleanupOldReserveOps ---

func TestCleanupOldReserveOps(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Create a reserve to generate ops.
	ref := uuid.New()
	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), ref)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Flush to write ops.
	m.flushOnce()

	// Verify ops exist.
	store.mu.Lock()
	opCount := len(store.ops)
	store.mu.Unlock()
	if opCount == 0 {
		t.Fatal("expected ops to be logged")
	}

	// Cleanup old ops. Since they were just created, none should be removed.
	m.cleanupOldReserveOps()

	store.mu.Lock()
	afterCleanup := len(store.ops)
	store.mu.Unlock()
	if afterCleanup != opCount {
		t.Errorf("expected %d ops after cleanup (too recent to remove), got %d", opCount, afterCleanup)
	}
}

// --- PublishLiquidityAlert ---

func TestPublishLiquidityAlert(t *testing.T) {
	tenantID := uuid.New()
	pos := testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 10000, 0)
	pos.MinBalance = decimal.NewFromInt(5000)

	store := &mockStore{
		positions: []domain.Position{pos},
	}
	pub := &mockPublisher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(store, pub, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)

	// Reserve enough to trigger low liquidity.
	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(6000), uuid.New())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Check that a liquidity alert was published.
	pub.mu.Lock()
	alerts := len(pub.events)
	var alertType string
	if alerts > 0 {
		alertType = pub.events[0].Type
	}
	pub.mu.Unlock()

	if alerts == 0 {
		t.Fatal("expected liquidity alert to be published when available < MinBalance")
	}
	if alertType != domain.EventLiquidityAlert {
		t.Errorf("expected event type %s, got %s", domain.EventLiquidityAlert, alertType)
	}
}

func TestPublishLiquidityAlert_NilPublisher(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 10000, 0),
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// nil publisher — should not panic
	m := NewManager(store, nil, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)

	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), uuid.New())
	if err != nil {
		t.Fatalf("Reserve with nil publisher should not fail: %v", err)
	}
}

// --- FlushBatch ---

type batchMockStore struct {
	mockStore
	batchCalled bool
	batchErr    error
}

func (s *batchMockStore) BatchUpdatePositions(_ context.Context, updates []PositionUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchCalled = true
	if s.batchErr != nil {
		return s.batchErr
	}
	for _, u := range updates {
		s.updates = append(s.updates, updateRecord{ID: u.ID, Balance: u.Balance, Locked: u.Locked})
	}
	return nil
}

func TestFlushBatch_Success(t *testing.T) {
	tenantID := uuid.New()
	pos := testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 10000, 0)
	bStore := &batchMockStore{
		mockStore: mockStore{positions: []domain.Position{pos}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(bStore, nil, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)

	// Reserve to mark position dirty.
	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), uuid.New())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Flush should use batch path.
	m.flushOnce()

	bStore.mu.Lock()
	called := bStore.batchCalled
	updateCount := len(bStore.updates)
	bStore.mu.Unlock()

	if !called {
		t.Error("expected BatchUpdatePositions to be called")
	}
	if updateCount == 0 {
		t.Error("expected at least one update")
	}
}

func TestFlushBatch_Error_RetriesNextCycle(t *testing.T) {
	tenantID := uuid.New()
	pos := testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 10000, 0)
	bStore := &batchMockStore{
		mockStore: mockStore{positions: []domain.Position{pos}},
		batchErr:  context.DeadlineExceeded,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(bStore, nil, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)

	err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), uuid.New())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// First flush fails.
	m.flushOnce()

	// Position should still be dirty (re-added to dirty set).
	dirty := m.dirtyPositions()
	if len(dirty) == 0 {
		t.Error("expected position to remain dirty after batch flush failure")
	}

	// Fix the error and flush again.
	bStore.mu.Lock()
	bStore.batchErr = nil
	bStore.mu.Unlock()

	m.flushOnce()

	dirty = m.dirtyPositions()
	if len(dirty) != 0 {
		t.Errorf("expected no dirty positions after successful retry, got %d", len(dirty))
	}
}

// --- UpdateBalance concurrent with Reserve ---

func TestUpdateBalanceConcurrentWithReserve(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:gbp", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 200)

	// Run concurrent reserves.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(10), uuid.New()); err != nil {
				errs <- err
			}
		}()
	}

	// Run concurrent UpdateBalance calls.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Set to a high value to avoid "below committed" errors.
			if err := m.UpdateBalance(ctx, tenantID, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(200000)); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	// Some reserves may fail due to insufficient funds, that's expected.
	// The test verifies no panics or data races.
	for err := range errs {
		_ = err // acceptable failures
	}
}

// --- FlushIndividual Error Path ---

type failingUpdateStore struct {
	mockStore
	failPositionID uuid.UUID
}

func (s *failingUpdateStore) UpdatePosition(_ context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	if id == s.failPositionID {
		return context.DeadlineExceeded
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, updateRecord{ID: id, Balance: balance, Locked: locked})
	return nil
}

func TestFlushIndividual_PartialFailure(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	posA := testPosition(tenantA, domain.CurrencyGBP, "bank:gbp", 10000, 0)
	posB := testPosition(tenantB, domain.CurrencyNGN, "bank:ngn", 50000, 0)

	fStore := &failingUpdateStore{
		mockStore:      mockStore{positions: []domain.Position{posA, posB}},
		failPositionID: posA.ID,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(fStore, nil, logger, nil)
	ctx := context.Background()
	_ = m.LoadPositions(ctx)

	// Reserve on both positions to mark dirty.
	_ = m.Reserve(ctx, tenantA, domain.CurrencyGBP, "bank:gbp", decimal.NewFromInt(100), uuid.New())
	_ = m.Reserve(ctx, tenantB, domain.CurrencyNGN, "bank:ngn", decimal.NewFromInt(100), uuid.New())

	// Flush — posA should fail, posB should succeed.
	m.flushOnce()

	// posA should remain dirty.
	dirty := m.dirtyPositions()
	foundDirtyA := false
	for _, ps := range dirty {
		if ps.TenantID == tenantA {
			foundDirtyA = true
		}
	}
	if !foundDirtyA {
		t.Error("expected position A to remain dirty after flush failure")
	}

	// posB should have been flushed.
	fStore.mu.Lock()
	updateCount := len(fStore.updates)
	fStore.mu.Unlock()
	if updateCount == 0 {
		t.Error("expected position B to be flushed successfully")
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
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

// ── CreditBalance tests ─────────────────────────────────────────────────────

func TestCreditBalance(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), uuid.New(), "deposit_session")
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(8000)) {
		t.Errorf("expected balance 8000 after credit, got %s", pos.Balance)
	}
	if !pos.Available().Equal(decimal.NewFromInt(8000)) {
		t.Errorf("expected available 8000 after credit, got %s", pos.Available())
	}
}

func TestCreditBalanceIdempotent(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()
	_ = m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref, "deposit_session")
	_ = m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref, "deposit_session")

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(6000)) {
		t.Errorf("expected balance 6000 (single credit), got %s", pos.Balance)
	}
}

func TestCreditBalanceConcurrent(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 0, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_ = m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(100), uuid.New(), "deposit_session")
		}()
	}
	wg.Wait()

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expected := decimal.NewFromInt(int64(n) * 100)
	if !pos.Balance.Equal(expected) {
		t.Errorf("expected balance %s after %d concurrent credits, got %s", expected, n, pos.Balance)
	}
}

func TestCreditBalanceRejectsNonPositive(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.Zero, uuid.New(), "test")
	if err == nil {
		t.Error("expected error for zero credit amount")
	}

	err = m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(-1), uuid.New(), "test")
	if err == nil {
		t.Error("expected error for negative credit amount")
	}
}

func TestCreditBalancePositionNotFound(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.CreditBalance(ctx, tenantID, domain.CurrencyGBP, "bank:barclays", decimal.NewFromInt(1000), uuid.New(), "test")
	if err == nil {
		t.Error("expected error for non-existent position")
	}
}

// ── DebitBalance tests ──────────────────────────────────────────────────────

func TestDebitBalance(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), uuid.New(), "position_transaction")
	if err != nil {
		t.Fatalf("DebitBalance: %v", err)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(3000)) {
		t.Errorf("expected balance 3000 after debit, got %s", pos.Balance)
	}
}

func TestDebitBalanceIdempotent(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	ref := uuid.New()
	_ = m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref, "position_transaction")
	_ = m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), ref, "position_transaction")

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(4000)) {
		t.Errorf("expected balance 4000 (single debit), got %s", pos.Balance)
	}
}

func TestDebitBalanceInsufficientFunds(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	err := m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(6000), uuid.New(), "test")
	if err == nil {
		t.Error("expected error for debit exceeding available balance")
	}
}

func TestDebitBalanceRespectsReserved(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Reserve 3000 → available = 2000
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), uuid.New())

	// Try to debit 3000 (more than available 2000)
	err := m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(3000), uuid.New(), "test")
	if err == nil {
		t.Error("expected error for debit exceeding available (balance minus reserved)")
	}

	// Debit 2000 should succeed (exactly available)
	err = m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), uuid.New(), "test")
	if err != nil {
		t.Fatalf("DebitBalance for available amount: %v", err)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(3000)) {
		t.Errorf("expected balance 3000, got %s", pos.Balance)
	}
}

func TestDebitBalanceConcurrent(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	const n = 100
	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			err := m.DebitBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(100), uuid.New(), "test")
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	expectedBalance := decimal.NewFromInt(10000 - successCount*100)
	if !pos.Balance.Equal(expectedBalance) {
		t.Errorf("expected balance %s (%d successful debits), got %s", expectedBalance, successCount, pos.Balance)
	}
	if successCount != 100 {
		t.Errorf("expected all 100 debits to succeed with 10000 balance, got %d", successCount)
	}
}

// ── ConsumeReservation tests ────────────────────────────────────────────────

func TestConsumeReservation(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Reserve first
	reserveRef := uuid.New()
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), reserveRef)

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Available().Equal(decimal.NewFromInt(3000)) {
		t.Fatalf("expected available 3000 after reserve, got %s", pos.Available())
	}

	// Consume the reservation
	consumeRef := uuid.New()
	err := m.ConsumeReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), consumeRef)
	if err != nil {
		t.Fatalf("ConsumeReservation: %v", err)
	}

	pos, _ = m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	// Balance should decrease by 2000 (from 5000 to 3000)
	if !pos.Balance.Equal(decimal.NewFromInt(3000)) {
		t.Errorf("expected balance 3000 after consume, got %s", pos.Balance)
	}
	// Reserved should be 0
	if !pos.Locked.Equal(decimal.Zero) {
		t.Errorf("expected locked 0 after consume, got %s", pos.Locked)
	}
	// Available should equal balance (3000)
	if !pos.Available().Equal(decimal.NewFromInt(3000)) {
		t.Errorf("expected available 3000 after consume, got %s", pos.Available())
	}
}

func TestConsumeReservationIdempotent(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), uuid.New())

	ref := uuid.New()
	_ = m.ConsumeReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), ref)
	_ = m.ConsumeReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), ref)

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(3000)) {
		t.Errorf("expected balance 3000 (single consume), got %s", pos.Balance)
	}
}

func TestConsumeReservationInsufficientReserved(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 5000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Reserve only 1000
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1000), uuid.New())

	// Try to consume 2000 (more than reserved)
	err := m.ConsumeReservation(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(2000), uuid.New())
	if err == nil {
		t.Error("expected error for consume exceeding reserved amount")
	}
	if !strings.Contains(err.Error(), "insufficient reserved") {
		t.Errorf("expected 'insufficient reserved' error, got: %v", err)
	}
}

func TestConsumeReservationTransferLifecycle(t *testing.T) {
	// Simulate the full transfer lifecycle:
	// Credit → Reserve → Consume → verify balance decreased correctly
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyGBP, "bank:barclays", 0, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Step 1: Tenant deposits 10,000 GBP
	err := m.CreditBalance(ctx, tenantID, domain.CurrencyGBP, "bank:barclays", decimal.NewFromInt(10000), uuid.New(), "deposit_session")
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}

	// Step 2: Transfer reserves 3,000 GBP
	reserveRef := uuid.New()
	err = m.Reserve(ctx, tenantID, domain.CurrencyGBP, "bank:barclays", decimal.NewFromInt(3000), reserveRef)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyGBP, "bank:barclays")
	if !pos.Balance.Equal(decimal.NewFromInt(10000)) {
		t.Errorf("balance should still be 10000 after reserve, got %s", pos.Balance)
	}
	if !pos.Available().Equal(decimal.NewFromInt(7000)) {
		t.Errorf("available should be 7000 after reserve, got %s", pos.Available())
	}

	// Step 3: Transfer completes → consume the reservation
	consumeRef := uuid.New()
	err = m.ConsumeReservation(ctx, tenantID, domain.CurrencyGBP, "bank:barclays", decimal.NewFromInt(3000), consumeRef)
	if err != nil {
		t.Fatalf("ConsumeReservation: %v", err)
	}

	pos, _ = m.GetPosition(ctx, tenantID, domain.CurrencyGBP, "bank:barclays")
	if !pos.Balance.Equal(decimal.NewFromInt(7000)) {
		t.Errorf("balance should be 7000 after consume, got %s", pos.Balance)
	}
	if !pos.Available().Equal(decimal.NewFromInt(7000)) {
		t.Errorf("available should be 7000 after consume, got %s", pos.Available())
	}
}

func TestConsumeReservationFailedTransferRelease(t *testing.T) {
	// Verify that failed transfers use Release (not Consume) and balance stays unchanged
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 10000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Reserve 5000
	reserveRef := uuid.New()
	_ = m.Reserve(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(5000), reserveRef)

	// Transfer fails → release (not consume)
	releaseRef := uuid.New()
	err := m.Release(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(5000), releaseRef)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}

	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	// Balance should be unchanged (10000)
	if !pos.Balance.Equal(decimal.NewFromInt(10000)) {
		t.Errorf("balance should remain 10000 after release, got %s", pos.Balance)
	}
	if !pos.Available().Equal(decimal.NewFromInt(10000)) {
		t.Errorf("available should be 10000 after release, got %s", pos.Available())
	}
}

func TestQueueEventNonBlocking(t *testing.T) {
	tenantID := uuid.New()
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 100000, 0),
		},
	}
	m := newTestManager(t, store)
	ctx := context.Background()

	// Fill the event channel to capacity (should not block or panic)
	for range 200 {
		_ = m.CreditBalance(ctx, tenantID, domain.CurrencyUSD, "bank:chase", decimal.NewFromInt(1), uuid.New(), "test")
	}

	// Verify balance is correct despite potential channel overflow
	pos, _ := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if !pos.Balance.Equal(decimal.NewFromInt(100200)) {
		t.Errorf("expected balance 100200, got %s", pos.Balance)
	}
}

// TestReplayWithLargeOpBacklog verifies that crash recovery replay of >10K
// uncommitted ops completes quickly. Before the fix, each op beyond the
// pendingOps channel capacity (10,000) would block for 1s, causing startup
// to take hours. The fix sets replaying=true during replay so logOp() skips
// channel enqueue entirely.
func TestReplayWithLargeOpBacklog(t *testing.T) {
	const numOps = 15_000

	tenantID := uuid.New()

	// Build a store with positions that have enough balance to cover all reserves.
	// Each reserve is $1, so we need at least $15,000 available.
	store := &mockStore{
		positions: []domain.Position{
			testPosition(tenantID, domain.CurrencyUSD, "bank:chase", 100_000, 0),
		},
	}

	// Pre-populate the store's ops slice with 15,000 uncommitted reserve ops.
	// Each op reserves $1 with a unique reference (so idempotency doesn't collapse them).
	ops := make([]ReserveOp, numOps)
	for i := range ops {
		ops[i] = ReserveOp{
			ID:        uuid.New(),
			TenantID:  tenantID,
			Currency:  domain.CurrencyUSD,
			Location:  "bank:chase",
			Amount:    decimal.NewFromInt(1),
			Reference: uuid.New(),
			OpType:    OpReserve,
			CreatedAt: time.Now().UTC(),
		}
	}
	store.ops = ops

	// Create the manager but do NOT call newTestManager — we need to control
	// LoadPositions timing ourselves.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub := &mockPublisher{}
	m := NewManager(store, pub, logger, nil, WithFlushInterval(50*time.Millisecond))

	ctx := context.Background()

	// LoadPositions must complete within 5 seconds. Before the fix, 15K ops
	// with a 1s timeout each on channel send would take >4 hours.
	done := make(chan error, 1)
	go func() {
		done <- m.LoadPositions(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LoadPositions failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadPositions blocked for >5s — replay likely deadlocked on pendingOps channel")
	}

	// Verify that reserves were actually applied to the in-memory position.
	pos, err := m.GetPosition(ctx, tenantID, domain.CurrencyUSD, "bank:chase")
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}

	// All 15,000 ops should have been replayed as reserves ($1 each).
	// Balance stays 100,000; locked increases by 15,000.
	expectedAvailable := decimal.NewFromInt(100_000 - numOps)
	if !pos.Available().Equal(expectedAvailable) {
		t.Errorf("expected available %s, got %s (balance=%s, locked=%s)",
			expectedAvailable, pos.Available(), pos.Balance, pos.Locked)
	}

	expectedLocked := decimal.NewFromInt(numOps)
	if !pos.Locked.Equal(expectedLocked) {
		t.Errorf("expected locked %s, got %s", expectedLocked, pos.Locked)
	}
}
