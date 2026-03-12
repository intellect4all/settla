package reconciliation

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockReportStore stores reports in memory.
type mockReportStore struct {
	reports []*Report
}

func (m *mockReportStore) CreateReconciliationReport(_ context.Context, report *Report) error {
	m.reports = append(m.reports, report)
	return nil
}

func (m *mockReportStore) GetLatestReport(_ context.Context) (*Report, error) {
	if len(m.reports) == 0 {
		return nil, errors.New("no reports")
	}
	return m.reports[len(m.reports)-1], nil
}

// passingCheck always returns "pass".
type passingCheck struct{ name string }

func (c *passingCheck) Name() string { return c.name }
func (c *passingCheck) Run(_ context.Context) (*CheckResult, error) {
	return &CheckResult{
		Name:      c.name,
		Status:    "pass",
		CheckedAt: time.Now().UTC(),
	}, nil
}

// failingCheck always returns "fail".
type failingCheck struct{ name string }

func (c *failingCheck) Name() string { return c.name }
func (c *failingCheck) Run(_ context.Context) (*CheckResult, error) {
	return &CheckResult{
		Name:       c.name,
		Status:     "fail",
		Details:    "something is wrong",
		Mismatches: 3,
		CheckedAt:  time.Now().UTC(),
	}, nil
}

// errorCheck always returns an error.
type errorCheck struct{ name string }

func (c *errorCheck) Name() string { return c.name }
func (c *errorCheck) Run(_ context.Context) (*CheckResult, error) {
	return nil, errors.New("database connection failed")
}

// mockTreasuryManager returns canned positions.
type mockTreasuryManager struct {
	positions map[uuid.UUID][]domain.Position
}

func (m *mockTreasuryManager) Reserve(_ context.Context, _ uuid.UUID, _ domain.Currency, _ string, _ decimal.Decimal, _ uuid.UUID) error {
	return nil
}

func (m *mockTreasuryManager) Release(_ context.Context, _ uuid.UUID, _ domain.Currency, _ string, _ decimal.Decimal, _ uuid.UUID) error {
	return nil
}

func (m *mockTreasuryManager) GetPositions(_ context.Context, tenantID uuid.UUID) ([]domain.Position, error) {
	return m.positions[tenantID], nil
}

func (m *mockTreasuryManager) GetPosition(_ context.Context, _ uuid.UUID, _ domain.Currency, _ string) (*domain.Position, error) {
	return nil, nil
}

func (m *mockTreasuryManager) GetLiquidityReport(_ context.Context, _ uuid.UUID) (*domain.LiquidityReport, error) {
	return nil, nil
}

// mockLedgerQuerier returns balances by account code.
type mockLedgerQuerier struct {
	balances map[string]decimal.Decimal
}

func (m *mockLedgerQuerier) GetAccountBalance(_ context.Context, accountCode string) (decimal.Decimal, error) {
	bal, ok := m.balances[accountCode]
	if !ok {
		return decimal.Zero, errors.New("account not found")
	}
	return bal, nil
}

// mockTenantLister returns a fixed list of tenant IDs.
type mockTenantLister struct {
	tenantIDs []uuid.UUID
}

func (m *mockTenantLister) ListActiveTenantIDs(_ context.Context) ([]uuid.UUID, error) {
	return m.tenantIDs, nil
}

// mockSlugResolver maps tenant UUIDs to slugs for testing.
type mockSlugResolver struct {
	slugs map[uuid.UUID]string
}

func (m *mockSlugResolver) GetTenantSlug(_ context.Context, tenantID uuid.UUID) (string, error) {
	if slug, ok := m.slugs[tenantID]; ok {
		return slug, nil
	}
	return tenantID.String(), nil
}

// mockTransferQuerier returns canned counts.
type mockTransferQuerier struct {
	counts map[domain.TransferStatus]int
}

func (m *mockTransferQuerier) CountTransfersInStatus(_ context.Context, status domain.TransferStatus, _ time.Time) (int, error) {
	return m.counts[status], nil
}

// mockOutboxQuerier returns canned counts.
type mockOutboxQuerier struct {
	unpublished  int
	defaultRows  int
}

func (m *mockOutboxQuerier) CountUnpublishedOlderThan(_ context.Context, _ time.Time) (int, error) {
	return m.unpublished, nil
}

func (m *mockOutboxQuerier) CountDefaultPartitionRows(_ context.Context) (int, error) {
	return m.defaultRows, nil
}

// mockProviderTxQuerier returns canned counts.
type mockProviderTxQuerier struct {
	pending int
}

func (m *mockProviderTxQuerier) CountPendingProviderTxOlderThan(_ context.Context, _ time.Time) (int, error) {
	return m.pending, nil
}

// mockVolumeQuerier returns canned volume data.
type mockVolumeQuerier struct {
	todayCount int
	average    float64
}

func (m *mockVolumeQuerier) GetDailyTransferCount(_ context.Context, _ time.Time) (int, error) {
	return m.todayCount, nil
}

func (m *mockVolumeQuerier) GetAverageDailyTransferCount(_ context.Context, _, _ time.Time) (float64, error) {
	return m.average, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// ---------------------------------------------------------------------------
// Reconciler Tests
// ---------------------------------------------------------------------------

func TestReconciler_AllPass(t *testing.T) {
	store := &mockReportStore{}
	checks := []Check{
		&passingCheck{name: "check_a"},
		&passingCheck{name: "check_b"},
		&passingCheck{name: "check_c"},
	}

	r := NewReconciler(checks, store, testLogger())
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !report.OverallPass {
		t.Error("expected OverallPass=true when all checks pass")
	}
	if len(report.Results) != 3 {
		t.Errorf("expected 3 results, got %d", len(report.Results))
	}
	for _, result := range report.Results {
		if result.Status != "pass" {
			t.Errorf("expected status=pass for %s, got %s", result.Name, result.Status)
		}
	}

	// Verify report was stored.
	if len(store.reports) != 1 {
		t.Errorf("expected 1 stored report, got %d", len(store.reports))
	}
}

func TestReconciler_OneCheckFails(t *testing.T) {
	store := &mockReportStore{}
	checks := []Check{
		&passingCheck{name: "check_a"},
		&failingCheck{name: "check_b"},
		&passingCheck{name: "check_c"},
	}

	r := NewReconciler(checks, store, testLogger())
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.OverallPass {
		t.Error("expected OverallPass=false when a check fails")
	}

	// check_b should be the failing one
	if report.Results[1].Status != "fail" {
		t.Errorf("expected check_b status=fail, got %s", report.Results[1].Status)
	}
	if report.Results[1].Mismatches != 3 {
		t.Errorf("expected 3 mismatches, got %d", report.Results[1].Mismatches)
	}
}

func TestReconciler_CheckError_RecordedAsFail(t *testing.T) {
	store := &mockReportStore{}
	checks := []Check{
		&passingCheck{name: "check_a"},
		&errorCheck{name: "check_err"},
	}

	r := NewReconciler(checks, store, testLogger())
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.OverallPass {
		t.Error("expected OverallPass=false when a check errors")
	}
	if report.Results[1].Status != "fail" {
		t.Errorf("expected status=fail for errored check, got %s", report.Results[1].Status)
	}
	if report.Results[1].Details == "" {
		t.Error("expected error details to be recorded")
	}
}

// ---------------------------------------------------------------------------
// Treasury-Ledger Check Tests
// ---------------------------------------------------------------------------

func TestTreasuryLedgerCheck_BalancesMatch(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	treasury := &mockTreasuryManager{
		positions: map[uuid.UUID][]domain.Position{
			tenantID: {
				{
					TenantID: tenantID,
					Currency: domain.CurrencyGBP,
					Location: "clearing",
					Balance:  decimal.NewFromFloat(10000.50),
				},
			},
		},
	}

	accountCode := buildAccountCode("test-tenant", domain.Position{
		TenantID: tenantID,
		Currency: domain.CurrencyGBP,
		Location: "clearing",
	})

	ledger := &mockLedgerQuerier{
		balances: map[string]decimal.Decimal{
			accountCode: decimal.NewFromFloat(10000.50),
		},
	}

	tenants := &mockTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	slugs := &mockSlugResolver{slugs: map[uuid.UUID]string{tenantID: "test-tenant"}}
	check := NewTreasuryLedgerCheck(treasury, ledger, tenants, slugs, testLogger(), decimal.NewFromFloat(0.01))

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", result.Status, result.Details)
	}
	if result.Mismatches != 0 {
		t.Errorf("expected 0 mismatches, got %d", result.Mismatches)
	}
}

func TestTreasuryLedgerCheck_BalanceMismatch(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	treasury := &mockTreasuryManager{
		positions: map[uuid.UUID][]domain.Position{
			tenantID: {
				{
					TenantID: tenantID,
					Currency: domain.CurrencyGBP,
					Location: "clearing",
					Balance:  decimal.NewFromFloat(10000.50),
				},
			},
		},
	}

	accountCode := buildAccountCode("test-tenant", domain.Position{
		TenantID: tenantID,
		Currency: domain.CurrencyGBP,
		Location: "clearing",
	})

	// Ledger has a different balance (off by 5.00).
	ledger := &mockLedgerQuerier{
		balances: map[string]decimal.Decimal{
			accountCode: decimal.NewFromFloat(10005.50),
		},
	}

	tenants := &mockTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	slugs := &mockSlugResolver{slugs: map[uuid.UUID]string{tenantID: "test-tenant"}}
	check := NewTreasuryLedgerCheck(treasury, ledger, tenants, slugs, testLogger(), decimal.NewFromFloat(0.01))

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 1 {
		t.Errorf("expected 1 mismatch, got %d", result.Mismatches)
	}
}

func TestTreasuryLedgerCheck_WithinTolerance(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	treasury := &mockTreasuryManager{
		positions: map[uuid.UUID][]domain.Position{
			tenantID: {
				{
					TenantID: tenantID,
					Currency: domain.CurrencyUSD,
					Location: "clearing",
					Balance:  decimal.NewFromFloat(1000.005),
				},
			},
		},
	}

	accountCode := buildAccountCode("test-tenant", domain.Position{
		TenantID: tenantID,
		Currency: domain.CurrencyUSD,
		Location: "clearing",
	})

	// Difference of 0.005, within 0.01 tolerance.
	ledger := &mockLedgerQuerier{
		balances: map[string]decimal.Decimal{
			accountCode: decimal.NewFromFloat(1000.00),
		},
	}

	tenants := &mockTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	slugs := &mockSlugResolver{slugs: map[uuid.UUID]string{tenantID: "test-tenant"}}
	check := NewTreasuryLedgerCheck(treasury, ledger, tenants, slugs, testLogger(), decimal.NewFromFloat(0.01))

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass (within tolerance), got %s: %s", result.Status, result.Details)
	}
}

// ---------------------------------------------------------------------------
// Transfer State Check Tests
// ---------------------------------------------------------------------------

func TestTransferStateCheck_NoStuckTransfers(t *testing.T) {
	store := &mockTransferQuerier{
		counts: map[domain.TransferStatus]int{
			domain.TransferStatusFunded:     0,
			domain.TransferStatusOnRamping:  0,
			domain.TransferStatusSettling:   0,
			domain.TransferStatusOffRamping: 0,
		},
	}

	check := NewTransferStateCheck(store, testLogger(), nil)
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", result.Status, result.Details)
	}
}

func TestTransferStateCheck_StuckTransfers(t *testing.T) {
	store := &mockTransferQuerier{
		counts: map[domain.TransferStatus]int{
			domain.TransferStatusFunded:     0,
			domain.TransferStatusOnRamping:  5,
			domain.TransferStatusSettling:   0,
			domain.TransferStatusOffRamping: 2,
		},
	}

	check := NewTransferStateCheck(store, testLogger(), nil)
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 7 {
		t.Errorf("expected 7 stuck transfers, got %d", result.Mismatches)
	}
}

// ---------------------------------------------------------------------------
// Outbox Check Tests
// ---------------------------------------------------------------------------

func TestOutboxCheck_Healthy(t *testing.T) {
	store := &mockOutboxQuerier{unpublished: 0, defaultRows: 0}
	check := NewOutboxCheck(store, testLogger(), 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", result.Status, result.Details)
	}
}

func TestOutboxCheck_StaleEntries(t *testing.T) {
	store := &mockOutboxQuerier{unpublished: 12, defaultRows: 0}
	check := NewOutboxCheck(store, testLogger(), 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 12 {
		t.Errorf("expected 12 mismatches, got %d", result.Mismatches)
	}
}

func TestOutboxCheck_DefaultPartitionLeak(t *testing.T) {
	store := &mockOutboxQuerier{unpublished: 0, defaultRows: 3}
	check := NewOutboxCheck(store, testLogger(), 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 3 {
		t.Errorf("expected 3 mismatches, got %d", result.Mismatches)
	}
}

// ---------------------------------------------------------------------------
// Provider Tx Check Tests
// ---------------------------------------------------------------------------

func TestProviderTxCheck_NoPending(t *testing.T) {
	store := &mockProviderTxQuerier{pending: 0}
	check := NewProviderTxCheck(store, testLogger(), 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass, got %s", result.Status)
	}
}

func TestProviderTxCheck_StalePending(t *testing.T) {
	store := &mockProviderTxQuerier{pending: 8}
	check := NewProviderTxCheck(store, testLogger(), 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "warn" {
		t.Errorf("expected warn, got %s", result.Status)
	}
	if result.Mismatches != 8 {
		t.Errorf("expected 8 mismatches, got %d", result.Mismatches)
	}
}

// ---------------------------------------------------------------------------
// Daily Volume Check Tests
// ---------------------------------------------------------------------------

func TestDailyVolumeCheck_Normal(t *testing.T) {
	store := &mockVolumeQuerier{todayCount: 100, average: 90}
	check := NewDailyVolumeCheck(store, testLogger())

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", result.Status, result.Details)
	}
}

func TestDailyVolumeCheck_Spike_Warn(t *testing.T) {
	// 250% of average -> warn
	store := &mockVolumeQuerier{todayCount: 500, average: 200}
	check := NewDailyVolumeCheck(store, testLogger())

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "warn" {
		t.Errorf("expected warn, got %s: %s", result.Status, result.Details)
	}
}

func TestDailyVolumeCheck_ExtremeSpike_Fail(t *testing.T) {
	// 600% of average -> fail
	store := &mockVolumeQuerier{todayCount: 6000, average: 1000}
	check := NewDailyVolumeCheck(store, testLogger())

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "fail" {
		t.Errorf("expected fail, got %s: %s", result.Status, result.Details)
	}
}

func TestDailyVolumeCheck_NoHistoricalData(t *testing.T) {
	store := &mockVolumeQuerier{todayCount: 50, average: 0}
	check := NewDailyVolumeCheck(store, testLogger())

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "pass" {
		t.Errorf("expected pass when no historical data, got %s", result.Status)
	}
}

// ---------------------------------------------------------------------------
// Integration-style: all 6 checks wired into Reconciler
// ---------------------------------------------------------------------------

func TestReconciler_All6Checks_AllPass(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	accountCode := buildAccountCode("test-tenant", domain.Position{
		TenantID: tenantID,
		Currency: domain.CurrencyGBP,
		Location: "clearing",
	})

	checks := []Check{
		NewTreasuryLedgerCheck(
			&mockTreasuryManager{
				positions: map[uuid.UUID][]domain.Position{
					tenantID: {{TenantID: tenantID, Currency: domain.CurrencyGBP, Location: "clearing", Balance: decimal.NewFromInt(1000)}},
				},
			},
			&mockLedgerQuerier{balances: map[string]decimal.Decimal{accountCode: decimal.NewFromInt(1000)}},
			&mockTenantLister{tenantIDs: []uuid.UUID{tenantID}},
			&mockSlugResolver{slugs: map[uuid.UUID]string{tenantID: "test-tenant"}},
			testLogger(),
			decimal.NewFromFloat(0.01),
		),
		NewTransferStateCheck(
			&mockTransferQuerier{counts: map[domain.TransferStatus]int{}},
			testLogger(),
			nil,
		),
		NewOutboxCheck(
			&mockOutboxQuerier{unpublished: 0, defaultRows: 0},
			testLogger(),
			0,
		),
		NewProviderTxCheck(
			&mockProviderTxQuerier{pending: 0},
			testLogger(),
			0,
		),
		NewDailyVolumeCheck(
			&mockVolumeQuerier{todayCount: 100, average: 90},
			testLogger(),
		),
		NewSettlementFeeCheck(
			&mockSettlementFeeStore{
				settlement: &SettlementRecord{
					ID:           uuid.MustParse("c0000000-0000-0000-0000-000000000001"),
					TenantID:     tenantID,
					PeriodStart:  time.Now().UTC().Add(-24 * time.Hour),
					PeriodEnd:    time.Now().UTC(),
					TotalFeesUSD: decimal.NewFromFloat(500.00),
				},
				transferFees: decimal.NewFromFloat(500.00),
			},
			testLogger(),
			decimal.Zero,
		),
	}

	store := &mockReportStore{}
	r := NewReconciler(checks, store, testLogger())
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !report.OverallPass {
		t.Error("expected OverallPass=true")
		for _, res := range report.Results {
			t.Logf("  %s: %s (%s)", res.Name, res.Status, res.Details)
		}
	}
	if len(report.Results) != 6 {
		t.Errorf("expected 6 results, got %d", len(report.Results))
	}
}

func TestReconciler_All6Checks_OneFails(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	accountCode := buildAccountCode("test-tenant", domain.Position{
		TenantID: tenantID,
		Currency: domain.CurrencyGBP,
		Location: "clearing",
	})

	checks := []Check{
		NewTreasuryLedgerCheck(
			&mockTreasuryManager{
				positions: map[uuid.UUID][]domain.Position{
					tenantID: {{TenantID: tenantID, Currency: domain.CurrencyGBP, Location: "clearing", Balance: decimal.NewFromInt(1000)}},
				},
			},
			&mockLedgerQuerier{balances: map[string]decimal.Decimal{accountCode: decimal.NewFromInt(1000)}},
			&mockTenantLister{tenantIDs: []uuid.UUID{tenantID}},
			&mockSlugResolver{slugs: map[uuid.UUID]string{tenantID: "test-tenant"}},
			testLogger(),
			decimal.NewFromFloat(0.01),
		),
		// This one has stuck transfers -> fail
		NewTransferStateCheck(
			&mockTransferQuerier{counts: map[domain.TransferStatus]int{
				domain.TransferStatusOnRamping: 3,
			}},
			testLogger(),
			nil,
		),
		NewOutboxCheck(
			&mockOutboxQuerier{unpublished: 0, defaultRows: 0},
			testLogger(),
			0,
		),
		NewProviderTxCheck(
			&mockProviderTxQuerier{pending: 0},
			testLogger(),
			0,
		),
		NewDailyVolumeCheck(
			&mockVolumeQuerier{todayCount: 100, average: 90},
			testLogger(),
		),
		NewSettlementFeeCheck(
			&mockSettlementFeeStore{settlement: nil}, // no settlements — passes
			testLogger(),
			decimal.Zero,
		),
	}

	store := &mockReportStore{}
	r := NewReconciler(checks, store, testLogger())
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.OverallPass {
		t.Error("expected OverallPass=false when transfer state check fails")
	}
}

// TestReconciler_WarnCountsAsNotPass verifies that "warn" status makes OverallPass false.
func TestReconciler_WarnCountsAsNotPass(t *testing.T) {
	store := &mockReportStore{}
	checks := []Check{
		&passingCheck{name: "check_a"},
		// Provider tx check returns "warn" when stale pending exist.
		NewProviderTxCheck(
			&mockProviderTxQuerier{pending: 5},
			testLogger(),
			0,
		),
	}

	r := NewReconciler(checks, store, testLogger())
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.OverallPass {
		t.Error("expected OverallPass=false when a check warns")
	}
}

// ---------------------------------------------------------------------------
// SettlementFeeCheck tests
// ---------------------------------------------------------------------------

// mockSettlementFeeStore is an in-memory implementation of SettlementFeeStore.
type mockSettlementFeeStore struct {
	settlement   *SettlementRecord
	transferFees decimal.Decimal
	err          error
}

func (m *mockSettlementFeeStore) GetLatestNetSettlement(_ context.Context) (*SettlementRecord, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.settlement, nil
}

func (m *mockSettlementFeeStore) SumCompletedTransferFeesUSD(_ context.Context, _ uuid.UUID, _, _ time.Time) (decimal.Decimal, error) {
	if m.err != nil {
		return decimal.Zero, m.err
	}
	return m.transferFees, nil
}

func TestSettlementFeeCheck_Name(t *testing.T) {
	check := NewSettlementFeeCheck(&mockSettlementFeeStore{}, testLogger(), decimal.Zero)
	if check.Name() != "settlement_fee_reconciliation" {
		t.Errorf("unexpected name: %s", check.Name())
	}
}

func TestSettlementFeeCheck_NoSettlements_Pass(t *testing.T) {
	store := &mockSettlementFeeStore{settlement: nil}
	check := NewSettlementFeeCheck(store, testLogger(), decimal.Zero)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected pass when no settlements, got %s: %s", result.Status, result.Details)
	}
}

func TestSettlementFeeCheck_FeesMatch_Pass(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	now := time.Now().UTC()
	fees := decimal.NewFromFloat(120.50)

	store := &mockSettlementFeeStore{
		settlement: &SettlementRecord{
			ID:           uuid.New(),
			TenantID:     tenantID,
			PeriodStart:  now.Add(-24 * time.Hour),
			PeriodEnd:    now,
			TotalFeesUSD: fees,
		},
		transferFees: fees, // exact match
	}
	check := NewSettlementFeeCheck(store, testLogger(), decimal.Zero)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected pass when fees match exactly, got %s: %s", result.Status, result.Details)
	}
	if result.Mismatches != 0 {
		t.Errorf("expected 0 mismatches, got %d", result.Mismatches)
	}
}

func TestSettlementFeeCheck_SmallDiff_Pass(t *testing.T) {
	// Difference of 0.005 USD is within the default 0.01 tolerance.
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	now := time.Now().UTC()

	store := &mockSettlementFeeStore{
		settlement: &SettlementRecord{
			ID:           uuid.New(),
			TenantID:     tenantID,
			PeriodStart:  now.Add(-24 * time.Hour),
			PeriodEnd:    now,
			TotalFeesUSD: decimal.NewFromFloat(100.00),
		},
		transferFees: decimal.NewFromFloat(100.005),
	}
	check := NewSettlementFeeCheck(store, testLogger(), decimal.Zero)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected pass for diff within tolerance, got %s: %s", result.Status, result.Details)
	}
}

func TestSettlementFeeCheck_LargeDiff_Fail(t *testing.T) {
	// Difference of 5.00 USD exceeds the default 0.01 tolerance.
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	now := time.Now().UTC()

	store := &mockSettlementFeeStore{
		settlement: &SettlementRecord{
			ID:           uuid.New(),
			TenantID:     tenantID,
			PeriodStart:  now.Add(-24 * time.Hour),
			PeriodEnd:    now,
			TotalFeesUSD: decimal.NewFromFloat(100.00),
		},
		transferFees: decimal.NewFromFloat(105.00),
	}
	check := NewSettlementFeeCheck(store, testLogger(), decimal.Zero)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail for diff exceeding tolerance, got %s: %s", result.Status, result.Details)
	}
	if result.Mismatches != 1 {
		t.Errorf("expected 1 mismatch, got %d", result.Mismatches)
	}
}

func TestSettlementFeeCheck_CustomTolerance(t *testing.T) {
	// With tolerance=10.00, a diff of 5.00 should still pass.
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	now := time.Now().UTC()

	store := &mockSettlementFeeStore{
		settlement: &SettlementRecord{
			ID:           uuid.New(),
			TenantID:     tenantID,
			PeriodStart:  now.Add(-24 * time.Hour),
			PeriodEnd:    now,
			TotalFeesUSD: decimal.NewFromFloat(100.00),
		},
		transferFees: decimal.NewFromFloat(105.00),
	}
	check := NewSettlementFeeCheck(store, testLogger(), decimal.NewFromFloat(10.00))

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected pass with wide tolerance, got %s: %s", result.Status, result.Details)
	}
}

func TestSettlementFeeCheck_StoreError_PropagatesErr(t *testing.T) {
	store := &mockSettlementFeeStore{err: errors.New("db connection failed")}
	check := NewSettlementFeeCheck(store, testLogger(), decimal.Zero)

	_, err := check.Run(context.Background())
	if err == nil {
		t.Fatal("expected error to be propagated, got nil")
	}
}
