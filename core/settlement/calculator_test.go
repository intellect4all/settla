package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// --- mock stores ---

type mockTransferStore struct {
	transfers []TransferSummary
}

func (m *mockTransferStore) ListCompletedTransfersByPeriod(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]TransferSummary, error) {
	return m.transfers, nil
}

type mockTenantStore struct {
	tenant  *domain.Tenant
	tenants []domain.Tenant
}

func (m *mockTenantStore) GetTenant(_ context.Context, _ uuid.UUID) (*domain.Tenant, error) {
	if m.tenant == nil {
		return nil, domain.ErrTenantNotFound("test")
	}
	return m.tenant, nil
}

func (m *mockTenantStore) ListTenantsBySettlementModel(_ context.Context, _ domain.SettlementModel) ([]domain.Tenant, error) {
	return m.tenants, nil
}

type mockSettlementStore struct {
	settlements []NetSettlement
	created     *NetSettlement
}

func (m *mockSettlementStore) CreateNetSettlement(_ context.Context, s *NetSettlement) error {
	m.created = s
	m.settlements = append(m.settlements, *s)
	return nil
}

func (m *mockSettlementStore) GetNetSettlement(_ context.Context, id uuid.UUID) (*NetSettlement, error) {
	for _, s := range m.settlements {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockSettlementStore) ListPendingSettlements(_ context.Context) ([]NetSettlement, error) {
	var result []NetSettlement
	for _, s := range m.settlements {
		if s.Status == "pending" || s.Status == "overdue" {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockSettlementStore) UpdateSettlementStatus(_ context.Context, id uuid.UUID, status string) error {
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
