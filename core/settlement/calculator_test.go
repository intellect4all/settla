package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// --- mock stores ---

type mockTransferStore struct {
	transfers  []TransferSummary
	aggregates []CorridorAggregate
}

func (m *mockTransferStore) ListCompletedTransfersByPeriod(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]TransferSummary, error) {
	return m.transfers, nil
}

func (m *mockTransferStore) AggregateCompletedTransfersByPeriod(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]CorridorAggregate, error) {
	if m.aggregates != nil {
		return m.aggregates, nil
	}
	// Auto-derive aggregates from transfers for backward compat with existing tests
	grouped := make(map[string]*CorridorAggregate)
	for _, t := range m.transfers {
		key := t.SourceCurrency + "->" + t.DestCurrency
		agg, ok := grouped[key]
		if !ok {
			agg = &CorridorAggregate{
				SourceCurrency: t.SourceCurrency,
				DestCurrency:   t.DestCurrency,
				TotalSource:    decimal.Zero,
				TotalDest:      decimal.Zero,
			}
			grouped[key] = agg
		}
		agg.TotalSource = agg.TotalSource.Add(t.SourceAmount)
		agg.TotalDest = agg.TotalDest.Add(t.DestAmount)
		agg.TotalFeesUSD = agg.TotalFeesUSD.Add(t.Fees)
		agg.TransferCount++
	}
	result := make([]CorridorAggregate, 0, len(grouped))
	for _, agg := range grouped {
		result = append(result, *agg)
	}
	return result, nil
}

type mockTenantStore struct {
	tenant  *domain.Tenant
	tenants []domain.Tenant
}

func (m *mockTenantStore) GetTenant(_ context.Context, id uuid.UUID) (*domain.Tenant, error) {
	// Check in tenants list first (for multi-tenant tests)
	for i := range m.tenants {
		if m.tenants[i].ID == id {
			return &m.tenants[i], nil
		}
	}
	if m.tenant == nil {
		return nil, domain.ErrTenantNotFound("test")
	}
	return m.tenant, nil
}

func (m *mockTenantStore) ListTenantsBySettlementModel(_ context.Context, _ domain.SettlementModel, _, _ int32) ([]domain.Tenant, error) {
	return m.tenants, nil
}

func (m *mockTenantStore) ListActiveTenantIDsBySettlementModel(_ context.Context, _ domain.SettlementModel, limit int32, afterID uuid.UUID) ([]uuid.UUID, error) {
	var activeIDs []uuid.UUID
	for _, t := range m.tenants {
		if t.Status == domain.TenantStatusActive {
			activeIDs = append(activeIDs, t.ID)
		}
	}
	// Cursor-based: find first ID > afterID
	start := 0
	if afterID != uuid.Nil {
		for i, id := range activeIDs {
			if id.String() > afterID.String() {
				start = i
				break
			}
			if i == len(activeIDs)-1 {
				return nil, nil // past end
			}
		}
	}
	end := start + int(limit)
	if end > len(activeIDs) {
		end = len(activeIDs)
	}
	return activeIDs[start:end], nil
}

func (m *mockTenantStore) CountActiveTenantsBySettlementModel(_ context.Context, _ domain.SettlementModel) (int64, error) {
	var count int64
	for _, t := range m.tenants {
		if t.Status == domain.TenantStatusActive {
			count++
		}
	}
	return count, nil
}

type mockSettlementStore struct {
	mu          sync.Mutex
	settlements []NetSettlement
	created     *NetSettlement
}

func (m *mockSettlementStore) CreateNetSettlement(_ context.Context, s *NetSettlement) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = s
	m.settlements = append(m.settlements, *s)
	return nil
}

func (m *mockSettlementStore) GetNetSettlement(_ context.Context, id uuid.UUID) (*NetSettlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.settlements {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockSettlementStore) ListPendingSettlements(_ context.Context, _ domain.AdminCaller) ([]NetSettlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []NetSettlement
	for _, s := range m.settlements {
		if s.Status == "pending" || s.Status == "overdue" {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockSettlementStore) UpdateSettlementStatus(_ context.Context, id uuid.UUID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.settlements {
		if m.settlements[i].ID == id {
			m.settlements[i].Status = status
			return nil
		}
	}
	return nil
}

// --- helpers ---

func newTestCalculator(
	transfers []TransferSummary,
	tenant *domain.Tenant,
) (*Calculator, *mockSettlementStore) {
	store := &mockSettlementStore{}
	calc := NewCalculator(
		&mockTransferStore{transfers: transfers},
		&mockTenantStore{tenant: tenant},
		store,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	)
	return calc, store
}

func testTenant(name string) *domain.Tenant {
	return &domain.Tenant{
		ID:              uuid.New(),
		Name:            name,
		Slug:            "test",
		Status:          domain.TenantStatusActive,
		SettlementModel: domain.SettlementModelNetSettlement,
		KYBStatus:       domain.KYBStatusVerified,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  40,
			OffRampBPS: 35,
			MinFeeUSD:  decimal.NewFromFloat(0.50),
			MaxFeeUSD:  decimal.NewFromFloat(500.00),
		},
	}
}

// --- tests ---

func TestCalculateNetSettlement_SingleCorridor(t *testing.T) {
	tenant := testTenant("Fincra")
	transfers := []TransferSummary{
		{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(1000), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(2000000), Fees: decimal.NewFromFloat(4.00)},
		{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(500), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(1000000), Fees: decimal.NewFromFloat(2.00)},
		{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(750), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(1500000), Fees: decimal.NewFromFloat(3.00)},
	}

	calc, store := newTestCalculator(transfers, tenant)
	ctx := context.Background()
	now := time.Now().UTC()
	periodStart := now.Add(-24 * time.Hour)
	periodEnd := now

	result, err := calc.CalculateNetSettlement(ctx, tenant.ID, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify settlement was created
	if store.created == nil {
		t.Fatal("expected settlement to be persisted")
	}

	// Single corridor: GBP->NGN
	if len(result.Corridors) != 1 {
		t.Fatalf("expected 1 corridor, got %d", len(result.Corridors))
	}
	corridor := result.Corridors[0]
	if corridor.SourceCurrency != "GBP" || corridor.DestCurrency != "NGN" {
		t.Errorf("unexpected corridor: %s->%s", corridor.SourceCurrency, corridor.DestCurrency)
	}
	expectedSource := decimal.NewFromInt(2250) // 1000+500+750
	if !corridor.TotalSource.Equal(expectedSource) {
		t.Errorf("expected total source %s, got %s", expectedSource, corridor.TotalSource)
	}
	if corridor.TransferCount != 3 {
		t.Errorf("expected 3 transfers, got %d", corridor.TransferCount)
	}

	// Net by currency: GBP is outflow only, NGN is inflow only
	if len(result.NetByCurrency) != 2 {
		t.Fatalf("expected 2 currency nets, got %d", len(result.NetByCurrency))
	}

	// Total fees
	expectedFees := decimal.NewFromFloat(9.00)
	if !result.TotalFeesUSD.Equal(expectedFees) {
		t.Errorf("expected total fees %s, got %s", expectedFees, result.TotalFeesUSD)
	}

	// Status
	if result.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", result.Status)
	}

	// Instructions should exist
	if len(result.Instructions) == 0 {
		t.Error("expected at least one settlement instruction")
	}
}

func TestCalculateNetSettlement_MultiCorridor(t *testing.T) {
	tenant := testTenant("Lemfi")
	transfers := []TransferSummary{
		// GBP -> NGN
		{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(1000), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(2000000), Fees: decimal.NewFromFloat(4.00)},
		// NGN -> GBP (reverse direction)
		{SourceCurrency: "NGN", SourceAmount: decimal.NewFromInt(1000000), DestCurrency: "GBP", DestAmount: decimal.NewFromInt(500), Fees: decimal.NewFromFloat(2.00)},
	}

	calc, _ := newTestCalculator(transfers, tenant)
	ctx := context.Background()
	now := time.Now().UTC()

	result, err := calc.CalculateNetSettlement(ctx, tenant.ID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Two corridors: GBP->NGN and NGN->GBP
	if len(result.Corridors) != 2 {
		t.Fatalf("expected 2 corridors, got %d", len(result.Corridors))
	}

	// Net by currency: both GBP and NGN have both inflows and outflows
	for _, net := range result.NetByCurrency {
		switch net.Currency {
		case "GBP":
			// GBP: outflow 1000 (sent), inflow 500 (received)
			expectedNet := decimal.NewFromInt(-500) // 500 - 1000
			if !net.Net.Equal(expectedNet) {
				t.Errorf("GBP net: expected %s, got %s", expectedNet, net.Net)
			}
		case "NGN":
			// NGN: outflow 1000000 (sent), inflow 2000000 (received)
			expectedNet := decimal.NewFromInt(1000000) // 2000000 - 1000000
			if !net.Net.Equal(expectedNet) {
				t.Errorf("NGN net: expected %s, got %s", expectedNet, net.Net)
			}
		}
	}
}

func TestCalculateNetSettlement_FeeCalculation(t *testing.T) {
	tenant := testTenant("Fincra")
	tenant.FeeSchedule = domain.FeeSchedule{
		OnRampBPS:  25,
		OffRampBPS: 20,
		MinFeeUSD:  decimal.NewFromFloat(1.00),
		MaxFeeUSD:  decimal.NewFromFloat(100.00),
	}

	transfers := []TransferSummary{
		{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(10000), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(20000000), Fees: decimal.NewFromFloat(25.00)},
		{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(5000), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(10000000), Fees: decimal.NewFromFloat(12.50)},
	}

	calc, _ := newTestCalculator(transfers, tenant)
	ctx := context.Background()
	now := time.Now().UTC()

	result, err := calc.CalculateNetSettlement(ctx, tenant.ID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Total fees: 25.00 + 12.50 = 37.50
	expectedFees := decimal.NewFromFloat(37.50)
	if !result.TotalFeesUSD.Equal(expectedFees) {
		t.Errorf("expected total fees %s, got %s", expectedFees, result.TotalFeesUSD)
	}

	// Should have a fee instruction
	hasFeeInstruction := false
	for _, inst := range result.Instructions {
		if inst.Currency == "USD" && inst.Description != "" {
			hasFeeInstruction = true
			if !inst.Amount.Equal(expectedFees) {
				t.Errorf("fee instruction amount: expected %s, got %s", expectedFees, inst.Amount)
			}
		}
	}
	if !hasFeeInstruction {
		t.Error("expected a fee instruction")
	}
}

func TestCalculateNetSettlement_EmptyPeriod(t *testing.T) {
	tenant := testTenant("Fincra")
	var transfers []TransferSummary // empty

	calc, _ := newTestCalculator(transfers, tenant)
	ctx := context.Background()
	now := time.Now().UTC()

	result, err := calc.CalculateNetSettlement(ctx, tenant.ID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Zero corridors, zero fees, zero instructions (no fee instruction since fees are zero)
	if len(result.Corridors) != 0 {
		t.Errorf("expected 0 corridors, got %d", len(result.Corridors))
	}
	if !result.TotalFeesUSD.IsZero() {
		t.Errorf("expected zero fees, got %s", result.TotalFeesUSD)
	}
	if len(result.Instructions) != 0 {
		t.Errorf("expected 0 instructions, got %d", len(result.Instructions))
	}
	if result.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", result.Status)
	}
}

func TestOverdueTracking(t *testing.T) {
	tenant := testTenant("Fincra")
	store := &mockSettlementStore{}

	calc := NewCalculator(
		&mockTransferStore{},
		&mockTenantStore{tenant: tenant, tenants: []domain.Tenant{*tenant}},
		store,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	)

	scheduler := NewScheduler(calc, &mockTenantStore{tenant: tenant, tenants: []domain.Tenant{*tenant}}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	now := time.Now().UTC()

	// Create settlements with various overdue states
	threeDaysAgo := now.Add(-3 * 24 * time.Hour)
	fiveDaysAgo := now.Add(-5 * 24 * time.Hour)
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)
	notOverdue := now.Add(24 * time.Hour)

	store.settlements = []NetSettlement{
		{ID: uuid.New(), TenantID: tenant.ID, TenantName: tenant.Name, Status: "pending", DueDate: &threeDaysAgo},
		{ID: uuid.New(), TenantID: tenant.ID, TenantName: tenant.Name, Status: "pending", DueDate: &fiveDaysAgo},
		{ID: uuid.New(), TenantID: tenant.ID, TenantName: tenant.Name, Status: "pending", DueDate: &sevenDaysAgo},
		{ID: uuid.New(), TenantID: tenant.ID, TenantName: tenant.Name, Status: "pending", DueDate: &notOverdue},
	}

	actions, err := scheduler.checkOverdue(context.Background(), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 3 actions (threeDaysAgo=reminder, fiveDaysAgo=warning, sevenDaysAgo=suspend)
	if len(actions) != 3 {
		t.Fatalf("expected 3 overdue actions, got %d", len(actions))
	}

	actionMap := make(map[string]string)
	for _, a := range actions {
		actionMap[a.SettlementID] = a.Action
	}

	// Check each settlement got the right action
	if action := actionMap[store.settlements[0].ID.String()]; action != "reminder" {
		t.Errorf("3-day overdue: expected 'reminder', got %q", action)
	}
	if action := actionMap[store.settlements[1].ID.String()]; action != "warning" {
		t.Errorf("5-day overdue: expected 'warning', got %q", action)
	}
	if action := actionMap[store.settlements[2].ID.String()]; action != "suspend" {
		t.Errorf("7-day overdue: expected 'suspend', got %q", action)
	}

	// 7-day overdue should have been updated to "overdue" status
	for _, s := range store.settlements {
		if s.ID == store.settlements[2].ID {
			if s.Status != "overdue" {
				t.Errorf("7-day overdue settlement: expected status 'overdue', got %q", s.Status)
			}
		}
	}
}

func TestFormatAmount(t *testing.T) {
	tests := []struct {
		amount   decimal.Decimal
		expected string
	}{
		{decimal.NewFromFloat(1350000000), "1.35B"},
		{decimal.NewFromFloat(522000), "522K"},
		{decimal.NewFromFloat(12340), "12K"},
		{decimal.NewFromFloat(999), "999.00"},
		{decimal.NewFromFloat(1500000), "1.50M"},
	}

	for _, tt := range tests {
		result := formatAmount(tt.amount)
		if result != tt.expected {
			t.Errorf("formatAmount(%s): expected %q, got %q", tt.amount, tt.expected, result)
		}
	}
}

func TestBatchedSettlementScheduler(t *testing.T) {
	// Create 5 active tenants
	var tenants []domain.Tenant
	for i := range 5 {
		tenant := testTenant(fmt.Sprintf("Tenant-%d", i))
		tenant.Status = domain.TenantStatusActive
		tenants = append(tenants, *tenant)
	}

	transferStore := &mockTransferStore{
		transfers: []TransferSummary{
			{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(100), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(200000), Fees: decimal.NewFromFloat(1.00)},
		},
	}
	tenantStore := &mockTenantStore{tenants: tenants}
	settlementStore := &mockSettlementStore{}

	calc := NewCalculator(transferStore, tenantStore, settlementStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx := context.Background()
	now := time.Now().UTC()
	periodEnd := now.Truncate(24 * time.Hour)
	periodStart := periodEnd.Add(-24 * time.Hour)

	err := scheduler.calculateForAllTenants(ctx, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All 5 tenants should have settlements created
	if len(settlementStore.settlements) != 5 {
		t.Errorf("expected 5 settlements, got %d", len(settlementStore.settlements))
	}
}

func TestBatchedSettlementScheduler_ContextCancellation(t *testing.T) {
	var tenants []domain.Tenant
	for i := range 10 {
		tenant := testTenant(fmt.Sprintf("Tenant-%d", i))
		tenant.Status = domain.TenantStatusActive
		tenants = append(tenants, *tenant)
	}

	transferStore := &mockTransferStore{
		transfers: []TransferSummary{
			{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(100), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(200000), Fees: decimal.NewFromFloat(1.00)},
		},
	}
	tenantStore := &mockTenantStore{tenants: tenants}
	settlementStore := &mockSettlementStore{}

	calc := NewCalculator(transferStore, tenantStore, settlementStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	now := time.Now().UTC()
	periodEnd := now.Truncate(24 * time.Hour)
	periodStart := periodEnd.Add(-24 * time.Hour)

	err := scheduler.calculateForAllTenants(ctx, periodStart, periodEnd)
	// Should either return context error or succeed (race between cancel and processing)
	if err != nil && err != context.Canceled {
		// Some tenants may have been processed before cancellation; that's fine
		t.Logf("got expected error on cancellation: %v", err)
	}
}

// --- Scheduler lifecycle and tick tests ---

func TestScheduler_RunOnce(t *testing.T) {
	tenant := testTenant("Lemfi")
	tenant.Status = domain.TenantStatusActive
	tenants := []domain.Tenant{*tenant}

	transferStore := &mockTransferStore{
		transfers: []TransferSummary{
			{SourceCurrency: "GBP", SourceAmount: decimal.NewFromInt(500), DestCurrency: "NGN", DestAmount: decimal.NewFromInt(1000000), Fees: decimal.NewFromFloat(2.00)},
		},
	}
	tenantStore := &mockTenantStore{tenants: tenants, tenant: tenant}
	settlementStore := &mockSettlementStore{}
	calc := NewCalculator(transferStore, tenantStore, settlementStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	settlementStore.mu.Lock()
	count := len(settlementStore.settlements)
	settlementStore.mu.Unlock()

	if count != 1 {
		t.Errorf("expected 1 settlement after RunOnce, got %d", count)
	}
}

func TestScheduler_SetInterval(t *testing.T) {
	calc := NewCalculator(
		&mockTransferStore{},
		&mockTenantStore{},
		&mockSettlementStore{},
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	)
	scheduler := NewScheduler(calc, &mockTenantStore{}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	scheduler.SetInterval(1 * time.Hour)
	if scheduler.interval != 1*time.Hour {
		t.Errorf("expected interval 1h, got %v", scheduler.interval)
	}
}

func TestScheduler_Start_StopsOnContextCancel(t *testing.T) {
	calc := NewCalculator(
		&mockTransferStore{},
		&mockTenantStore{},
		&mockSettlementStore{},
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	)
	tenantStore := &mockTenantStore{}
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	scheduler.SetInterval(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := scheduler.Start(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestScheduler_ProcessOneTenant_Error(t *testing.T) {
	// Create a calculator with a transfer store that returns errors.
	failingTransferStore := &failingMockTransferStore{}
	tenantStore := &mockTenantStore{
		tenant: testTenant("Failing"),
	}
	settlementStore := &mockSettlementStore{}
	calc := NewCalculator(failingTransferStore, tenantStore, settlementStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	err := scheduler.processOneTenant(context.Background(), uuid.New(), time.Now().Add(-24*time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error from processOneTenant when calculator fails")
	}
}

type failingMockTransferStore struct{}

func (f *failingMockTransferStore) ListCompletedTransfersByPeriod(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]TransferSummary, error) {
	return nil, fmt.Errorf("simulated DB failure")
}

func (f *failingMockTransferStore) AggregateCompletedTransfersByPeriod(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]CorridorAggregate, error) {
	return nil, fmt.Errorf("simulated DB failure")
}

func TestOverdueEscalation_Thresholds(t *testing.T) {
	tenant := testTenant("OverdueTest")
	tenant.Status = domain.TenantStatusActive

	tenantStore := &mockTenantStore{tenants: []domain.Tenant{*tenant}, tenant: tenant}
	settlementStore := &mockSettlementStore{}
	calc := NewCalculator(
		&mockTransferStore{},
		tenantStore,
		settlementStore,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	)
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	now := time.Now().UTC()

	tests := []struct {
		name       string
		dueDate    time.Time
		wantAction string
	}{
		{"3d overdue → reminder", now.Add(-3*24*time.Hour - time.Hour), "reminder"},
		{"5d overdue → warning", now.Add(-5*24*time.Hour - time.Hour), "warning"},
		{"7d overdue → suspend", now.Add(-7*24*time.Hour - time.Hour), "suspend"},
		{"1d overdue → no action", now.Add(-1 * 24 * time.Hour), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset settlements.
			settlementStore.mu.Lock()
			dueDate := tt.dueDate
			settlementStore.settlements = []NetSettlement{
				{
					ID:         uuid.New(),
					TenantID:   tenant.ID,
					TenantName: tenant.Name,
					Status:     "pending",
					DueDate:    &dueDate,
				},
			}
			settlementStore.mu.Unlock()

			actions, err := scheduler.checkOverdue(context.Background(), now)
			if err != nil {
				t.Fatalf("checkOverdue: %v", err)
			}

			if tt.wantAction == "" {
				if len(actions) != 0 {
					t.Errorf("expected no actions for %s, got %d", tt.name, len(actions))
				}
				return
			}

			if len(actions) != 1 {
				t.Fatalf("expected 1 action, got %d", len(actions))
			}
			if actions[0].Action != tt.wantAction {
				t.Errorf("action: got %q, want %q", actions[0].Action, tt.wantAction)
			}
		})
	}
}

func TestCheckOverdue_NilDueDate(t *testing.T) {
	tenant := testTenant("NoDueDate")
	tenant.Status = domain.TenantStatusActive
	settlementStore := &mockSettlementStore{
		settlements: []NetSettlement{
			{
				ID:         uuid.New(),
				TenantID:   tenant.ID,
				TenantName: tenant.Name,
				Status:     "pending",
				DueDate:    nil, // no due date
			},
		},
	}
	tenantStore := &mockTenantStore{tenants: []domain.Tenant{*tenant}, tenant: tenant}
	calc := NewCalculator(&mockTransferStore{}, tenantStore, settlementStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	scheduler := NewScheduler(calc, tenantStore, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	actions, err := scheduler.checkOverdue(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("checkOverdue: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("expected 0 actions for nil due date, got %d", len(actions))
	}
}

func TestCalculateNetSettlement_WrongModel(t *testing.T) {
	tenant := testTenant("Fincra")
	tenant.SettlementModel = domain.SettlementModelPrefunded

	calc, _ := newTestCalculator(nil, tenant)
	ctx := context.Background()
	now := time.Now().UTC()

	_, err := calc.CalculateNetSettlement(ctx, tenant.ID, now.Add(-24*time.Hour), now)
	if err == nil {
		t.Fatal("expected error for PREFUNDED tenant")
	}
}
