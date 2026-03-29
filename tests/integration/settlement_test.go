//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/settlement"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// ─── Settlement Adapters ──────────────────────────────────────────────────────

// settlementTransferAdapter wraps *memTransferStore to implement settlement.TransferStore.
type settlementTransferAdapter struct {
	inner *memTransferStore
}

var _ settlement.TransferStore = (*settlementTransferAdapter)(nil)

func (a *settlementTransferAdapter) ListCompletedTransfersByPeriod(
	ctx context.Context,
	tenantID uuid.UUID,
	start, end time.Time,
) ([]settlement.TransferSummary, error) {
	a.inner.mu.RLock()
	defer a.inner.mu.RUnlock()

	var result []settlement.TransferSummary
	for _, t := range a.inner.transfers {
		if t.TenantID != tenantID {
			continue
		}
		if t.Status != domain.TransferStatusCompleted {
			continue
		}
		if t.CreatedAt.Before(start) || !t.CreatedAt.Before(end) {
			continue
		}
		result = append(result, settlement.TransferSummary{
			SourceCurrency: string(t.SourceCurrency),
			SourceAmount:   t.SourceAmount,
			DestCurrency:   string(t.DestCurrency),
			DestAmount:     t.DestAmount,
			Fees:           t.Fees.TotalFeeUSD,
		})
	}
	return result, nil
}

// settlementTenantAdapter wraps *memTenantStore to implement settlement.TenantStore.
type settlementTenantAdapter struct {
	inner *memTenantStore
}

var _ settlement.TenantStore = (*settlementTenantAdapter)(nil)

func (a *settlementTenantAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	return a.inner.GetTenant(ctx, tenantID)
}

func (a *settlementTenantAdapter) ListTenantsBySettlementModel(
	ctx context.Context,
	model domain.SettlementModel,
	limit, offset int32,
) ([]domain.Tenant, error) {
	a.inner.mu.RLock()
	defer a.inner.mu.RUnlock()

	var result []domain.Tenant
	for _, t := range a.inner.tenants {
		if t.SettlementModel == model {
			result = append(result, *t)
		}
	}
	// Apply pagination
	start := int(offset)
	if start >= len(result) {
		return nil, nil
	}
	end := start + int(limit)
	if end > len(result) {
		end = len(result)
	}
	return result[start:end], nil
}

// settlementStoreInMem is an in-memory implementation of settlement.SettlementStore.
type settlementStoreInMem struct {
	mu          sync.RWMutex
	settlements map[uuid.UUID]*domain.NetSettlement
}

var _ settlement.SettlementStore = (*settlementStoreInMem)(nil)

func newSettlementStoreInMem() *settlementStoreInMem {
	return &settlementStoreInMem{
		settlements: make(map[uuid.UUID]*domain.NetSettlement),
	}
}

func (s *settlementStoreInMem) CreateNetSettlement(ctx context.Context, ns *domain.NetSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlements[ns.ID] = ns
	return nil
}

func (s *settlementStoreInMem) GetNetSettlement(ctx context.Context, id uuid.UUID) (*domain.NetSettlement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ns, ok := s.settlements[id]
	if !ok {
		return nil, fmt.Errorf("settlement not found: %s", id)
	}
	return ns, nil
}

func (s *settlementStoreInMem) ListPendingSettlements(ctx context.Context, _ domain.AdminCaller) ([]domain.NetSettlement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []domain.NetSettlement
	for _, ns := range s.settlements {
		if ns.Status == "pending" || ns.Status == "overdue" {
			result = append(result, *ns)
		}
	}
	return result, nil
}

func (s *settlementStoreInMem) UpdateSettlementStatus(ctx context.Context, id uuid.UUID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.settlements[id]
	if !ok {
		return fmt.Errorf("settlement not found: %s", id)
	}
	ns.Status = status
	return nil
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestSettlement_NetSettlementE2E(t *testing.T) {
	ctx := context.Background()
	logger := observability.NewLogger("test", "test")

	// Set up tenant and transfer stores.
	tenantStore := newMemTenantStore()
	transferStore := newMemTransferStore()

	tenantStore.addTenant(&domain.Tenant{
		ID:   NetSettlementTenantID,
		Name: "NetSettler",
		Slug: "netsettler",
		Status: domain.TenantStatusActive,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  30,
			OffRampBPS: 25,
		},
		SettlementModel: domain.SettlementModelNetSettlement,
		KYBStatus:       domain.KYBStatusVerified,
		DailyLimitUSD:   decimal.NewFromInt(10_000_000),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	// Seed 3 completed GBP->NGN transfers with different amounts.
	now := time.Now().UTC()
	transfers := []struct {
		sourceAmount decimal.Decimal
		destAmount   decimal.Decimal
		feeUSD       decimal.Decimal
	}{
		{decimal.NewFromInt(1000), decimal.NewFromInt(500_000), decimal.NewFromFloat(3.00)},
		{decimal.NewFromInt(2000), decimal.NewFromInt(1_000_000), decimal.NewFromFloat(6.00)},
		{decimal.NewFromInt(3000), decimal.NewFromInt(1_500_000), decimal.NewFromFloat(9.00)},
	}

	for i, tr := range transfers {
		transfer := &domain.Transfer{
			ID:             uuid.New(),
			TenantID:       NetSettlementTenantID,
			ExternalRef:    uuid.New().String(),
			IdempotencyKey: uuid.New().String(),
			Status:         domain.TransferStatusCompleted,
			Version:        1,
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   tr.sourceAmount,
			DestCurrency:   domain.CurrencyNGN,
			DestAmount:     tr.destAmount,
			FXRate:         decimal.NewFromInt(500),
			Fees: domain.FeeBreakdown{
				TotalFeeUSD: tr.feeUSD,
			},
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt: now,
		}
		if err := transferStore.CreateTransfer(ctx, transfer); err != nil {
			t.Fatalf("failed to create transfer %d: %v", i, err)
		}
	}

	// Build settlement adapters and calculator.
	settlTransferAdapter := &settlementTransferAdapter{inner: transferStore}
	settlTenantAdapter := &settlementTenantAdapter{inner: tenantStore}
	settlStore := newSettlementStoreInMem()

	calculator := settlement.NewCalculator(settlTransferAdapter, settlTenantAdapter, settlStore, logger)

	// Calculate net settlement for today's window.
	periodStart := now.Truncate(24 * time.Hour)
	periodEnd := periodStart.Add(24 * time.Hour)

	ns, err := calculator.CalculateNetSettlement(ctx, NetSettlementTenantID, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("CalculateNetSettlement failed: %v", err)
	}

	// Verify corridors.
	if len(ns.Corridors) != 1 {
		t.Fatalf("expected 1 corridor, got %d", len(ns.Corridors))
	}
	corridor := ns.Corridors[0]
	if corridor.SourceCurrency != "GBP" || corridor.DestCurrency != "NGN" {
		t.Errorf("expected GBP->NGN corridor, got %s->%s", corridor.SourceCurrency, corridor.DestCurrency)
	}
	if corridor.TransferCount != 3 {
		t.Errorf("expected 3 transfers in corridor, got %d", corridor.TransferCount)
	}
	expectedSourceTotal := decimal.NewFromInt(6000) // 1000 + 2000 + 3000
	if !corridor.TotalSource.Equal(expectedSourceTotal) {
		t.Errorf("expected total source %s, got %s", expectedSourceTotal, corridor.TotalSource)
	}
	expectedDestTotal := decimal.NewFromInt(3_000_000) // 500K + 1M + 1.5M
	if !corridor.TotalDest.Equal(expectedDestTotal) {
		t.Errorf("expected total dest %s, got %s", expectedDestTotal, corridor.TotalDest)
	}

	// Verify net by currency.
	if len(ns.NetByCurrency) != 2 {
		t.Fatalf("expected 2 currency nets (GBP, NGN), got %d", len(ns.NetByCurrency))
	}
	netMap := make(map[string]domain.CurrencyNet)
	for _, cn := range ns.NetByCurrency {
		netMap[cn.Currency] = cn
	}
	gbpNet, ok := netMap["GBP"]
	if !ok {
		t.Fatal("missing GBP in net_by_currency")
	}
	// GBP: 0 inflows, 6000 outflows => net = -6000 (tenant owes Settla)
	if !gbpNet.Outflows.Equal(decimal.NewFromInt(6000)) {
		t.Errorf("expected GBP outflows 6000, got %s", gbpNet.Outflows)
	}
	if !gbpNet.Net.Equal(decimal.NewFromInt(-6000)) {
		t.Errorf("expected GBP net -6000, got %s", gbpNet.Net)
	}

	ngnNet, ok := netMap["NGN"]
	if !ok {
		t.Fatal("missing NGN in net_by_currency")
	}
	// NGN: 3M inflows, 0 outflows => net = +3M (Settla owes tenant)
	if !ngnNet.Inflows.Equal(decimal.NewFromInt(3_000_000)) {
		t.Errorf("expected NGN inflows 3000000, got %s", ngnNet.Inflows)
	}
	if !ngnNet.Net.Equal(decimal.NewFromInt(3_000_000)) {
		t.Errorf("expected NGN net 3000000, got %s", ngnNet.Net)
	}

	// Verify total fees.
	expectedFees := decimal.NewFromFloat(18.00) // 3 + 6 + 9
	if !ns.TotalFeesUSD.Equal(expectedFees) {
		t.Errorf("expected total fees %s, got %s", expectedFees, ns.TotalFeesUSD)
	}

	// Verify instructions exist and have correct directions.
	if len(ns.Instructions) == 0 {
		t.Fatal("expected settlement instructions, got none")
	}
	foundTenantOwes := false
	foundSettlaOwes := false
	foundFeeInstr := false
	for _, inst := range ns.Instructions {
		switch {
		case inst.Currency == "GBP" && inst.Direction == "tenant_owes_settla":
			foundTenantOwes = true
			if !inst.Amount.Equal(decimal.NewFromInt(6000)) {
				t.Errorf("expected GBP tenant_owes_settla amount 6000, got %s", inst.Amount)
			}
		case inst.Currency == "NGN" && inst.Direction == "settla_owes_tenant":
			foundSettlaOwes = true
			if !inst.Amount.Equal(decimal.NewFromInt(3_000_000)) {
				t.Errorf("expected NGN settla_owes_tenant amount 3000000, got %s", inst.Amount)
			}
		case inst.Currency == "USD" && inst.Direction == "tenant_owes_settla":
			foundFeeInstr = true
			if !inst.Amount.Equal(expectedFees) {
				t.Errorf("expected fee instruction amount %s, got %s", expectedFees, inst.Amount)
			}
		}
	}
	if !foundTenantOwes {
		t.Error("missing GBP tenant_owes_settla instruction")
	}
	if !foundSettlaOwes {
		t.Error("missing NGN settla_owes_tenant instruction")
	}
	if !foundFeeInstr {
		t.Error("missing USD fee instruction")
	}

	// Verify due date is T+3.
	if ns.DueDate == nil {
		t.Fatal("expected due date to be set")
	}
	expectedDueDate := periodEnd.AddDate(0, 0, 3)
	if !ns.DueDate.Equal(expectedDueDate) {
		t.Errorf("expected due date %v, got %v", expectedDueDate, *ns.DueDate)
	}

	// Verify status is pending.
	if ns.Status != "pending" {
		t.Errorf("expected status 'pending', got '%s'", ns.Status)
	}

	// Verify the settlement was persisted in the store.
	stored, err := settlStore.GetNetSettlement(ctx, ns.ID)
	if err != nil {
		t.Fatalf("failed to retrieve stored settlement: %v", err)
	}
	if stored.ID != ns.ID {
		t.Errorf("stored settlement ID mismatch: got %s, want %s", stored.ID, ns.ID)
	}
}

func TestSettlement_SchedulerRunOnce(t *testing.T) {
	ctx := context.Background()
	logger := observability.NewLogger("test", "test")

	tenantStore := newMemTenantStore()
	transferStore := newMemTransferStore()

	// Add an active net-settlement tenant.
	tenantStore.addTenant(&domain.Tenant{
		ID:   NetSettlementTenantID,
		Name: "NetSettler",
		Slug: "netsettler",
		Status: domain.TenantStatusActive,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  30,
			OffRampBPS: 25,
		},
		SettlementModel: domain.SettlementModelNetSettlement,
		KYBStatus:       domain.KYBStatusVerified,
		DailyLimitUSD:   decimal.NewFromInt(10_000_000),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	// Add an inactive net-settlement tenant (should be skipped).
	inactiveTenantID := uuid.MustParse("d0000000-0000-0000-0000-000000000004")
	tenantStore.addTenant(&domain.Tenant{
		ID:              inactiveTenantID,
		Name:            "InactiveSettler",
		Slug:            "inactivesettler",
		Status:          domain.TenantStatusSuspended,
		SettlementModel: domain.SettlementModelNetSettlement,
		KYBStatus:       domain.KYBStatusVerified,
		DailyLimitUSD:   decimal.NewFromInt(1_000_000),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	// Seed completed transfers with CreatedAt = yesterday so they fall in the
	// scheduler's window (yesterday 00:00 UTC to today 00:00 UTC).
	yesterday := time.Now().UTC().Truncate(24*time.Hour).Add(-12 * time.Hour) // middle of yesterday

	for i := 0; i < 2; i++ {
		transfer := &domain.Transfer{
			ID:             uuid.New(),
			TenantID:       NetSettlementTenantID,
			ExternalRef:    uuid.New().String(),
			IdempotencyKey: uuid.New().String(),
			Status:         domain.TransferStatusCompleted,
			Version:        1,
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(5000),
			DestCurrency:   domain.CurrencyNGN,
			DestAmount:     decimal.NewFromInt(2_500_000),
			FXRate:         decimal.NewFromInt(500),
			Fees: domain.FeeBreakdown{
				TotalFeeUSD: decimal.NewFromFloat(15.00),
			},
			CreatedAt: yesterday.Add(time.Duration(i) * time.Minute),
			UpdatedAt: yesterday,
		}
		if err := transferStore.CreateTransfer(ctx, transfer); err != nil {
			t.Fatalf("failed to create transfer %d: %v", i, err)
		}
	}

	// Build adapters.
	settlTransferAdapter := &settlementTransferAdapter{inner: transferStore}
	settlTenantAdapter := &settlementTenantAdapter{inner: tenantStore}
	settlStore := newSettlementStoreInMem()

	calculator := settlement.NewCalculator(settlTransferAdapter, settlTenantAdapter, settlStore, logger)
	scheduler := settlement.NewScheduler(calculator, settlTenantAdapter, logger)

	// Run one tick.
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Verify a settlement was created for the active tenant.
	pending, err := settlStore.ListPendingSettlements(ctx, domain.AdminCaller{
		Service: "integration_test",
		Reason:  "verify_settlement_created",
	})
	if err != nil {
		t.Fatalf("ListPendingSettlements failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending settlement, got %d", len(pending))
	}

	ns := pending[0]
	if ns.TenantID != NetSettlementTenantID {
		t.Errorf("expected tenant ID %s, got %s", NetSettlementTenantID, ns.TenantID)
	}
	if ns.TenantName != "NetSettler" {
		t.Errorf("expected tenant name 'NetSettler', got '%s'", ns.TenantName)
	}
	if len(ns.Corridors) != 1 {
		t.Errorf("expected 1 corridor, got %d", len(ns.Corridors))
	}
	if ns.Corridors[0].TransferCount != 2 {
		t.Errorf("expected 2 transfers in corridor, got %d", ns.Corridors[0].TransferCount)
	}

	// Verify the inactive tenant produced no settlement.
	settlStore.mu.RLock()
	for _, s := range settlStore.settlements {
		if s.TenantID == inactiveTenantID {
			t.Error("inactive tenant should not have a settlement record")
		}
	}
	settlStore.mu.RUnlock()
}

// ─── TEST-36: Settlement Batch Timing Boundary ──────────────────────────────

// TestSettlement_BatchTimingBoundary verifies that transfers created just before
// and just after the settlement batch boundary are correctly included/excluded.
// The scheduler runs at 00:30 UTC covering [yesterday 00:00, today 00:00).
func TestSettlement_BatchTimingBoundary(t *testing.T) {
	ctx := context.Background()
	logger := observability.NewLogger("test", "test")

	tenantStore := newMemTenantStore()
	transferStore := newMemTransferStore()

	tenantStore.addTenant(&domain.Tenant{
		ID:              NetSettlementTenantID,
		Name:            "NetSettler",
		Slug:            "netsettler",
		Status:          domain.TenantStatusActive,
		FeeSchedule:     domain.FeeSchedule{OnRampBPS: 30, OffRampBPS: 25},
		SettlementModel: domain.SettlementModelNetSettlement,
		KYBStatus:       domain.KYBStatusVerified,
		DailyLimitUSD:   decimal.NewFromInt(10_000_000),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})

	// Batch window: [yesterday 00:00 UTC, today 00:00 UTC)
	todayMidnight := time.Now().UTC().Truncate(24 * time.Hour)
	yesterdayMidnight := todayMidnight.Add(-24 * time.Hour)

	// Transfer A: completed 1 second BEFORE batch end (should be INCLUDED)
	transferIncluded := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       NetSettlementTenantID,
		ExternalRef:    uuid.New().String(),
		IdempotencyKey: uuid.New().String(),
		Status:         domain.TransferStatusCompleted,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromInt(500_000),
		Fees:           domain.FeeBreakdown{OnRampFee: decimal.NewFromFloat(3), OffRampFee: decimal.NewFromFloat(2.5), NetworkFee: decimal.NewFromFloat(0.5), TotalFeeUSD: decimal.NewFromInt(6)},
		CompletedAt:    timePtr(todayMidnight.Add(-1 * time.Second)), // 23:59:59 yesterday
		CreatedAt:      yesterdayMidnight.Add(12 * time.Hour),
		UpdatedAt:      todayMidnight.Add(-1 * time.Second),
	}

	// Transfer B: completed 1 second AFTER batch end (should be EXCLUDED)
	transferExcluded := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       NetSettlementTenantID,
		ExternalRef:    uuid.New().String(),
		IdempotencyKey: uuid.New().String(),
		Status:         domain.TransferStatusCompleted,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(2000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromInt(1_000_000),
		Fees:           domain.FeeBreakdown{OnRampFee: decimal.NewFromFloat(6), OffRampFee: decimal.NewFromFloat(5), NetworkFee: decimal.NewFromFloat(1), TotalFeeUSD: decimal.NewFromInt(12)},
		CompletedAt:    timePtr(todayMidnight.Add(1 * time.Second)), // 00:00:01 today
		CreatedAt:      todayMidnight.Add(1 * time.Second),
		UpdatedAt:      todayMidnight.Add(1 * time.Second),
	}

	transferStore.mu.Lock()
	transferStore.transfers[transferIncluded.ID] = transferIncluded
	transferStore.transfers[transferExcluded.ID] = transferExcluded
	transferStore.mu.Unlock()

	settlStore := newMemSettlementStore()
	calc := settlement.NewCalculator(logger)
	scheduler := settlement.NewScheduler(
		&memSettlementTransferStore{ts: transferStore},
		settlStore,
		tenantStore,
		calc,
		logger,
	)

	results, err := scheduler.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least one settlement result")
	}

	// Find the settlement for our tenant.
	var found *domain.NetSettlement
	settlStore.mu.RLock()
	for _, s := range settlStore.settlements {
		if s.TenantID == NetSettlementTenantID {
			found = s
			break
		}
	}
	settlStore.mu.RUnlock()

	if found == nil {
		t.Fatal("no settlement created for NetSettlement tenant")
	}

	// The settlement should include ONLY transferIncluded (1000 GBP), not transferExcluded (2000 GBP).
	if len(found.Corridors) != 1 {
		t.Fatalf("expected 1 corridor, got %d", len(found.Corridors))
	}
	if found.Corridors[0].TransferCount != 1 {
		t.Errorf("expected 1 transfer in settlement batch, got %d (boundary transfer may have leaked)", found.Corridors[0].TransferCount)
	}

	t.Logf("Settlement batch: %d corridors, %d transfers, period [%s, %s)",
		len(found.Corridors), found.Corridors[0].TransferCount,
		found.PeriodStart.Format(time.RFC3339), found.PeriodEnd.Format(time.RFC3339))
	_ = results
}

func timePtr(t time.Time) *time.Time { return &t }
