package analytics

import (
	"context"
	"testing"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

type mockQuerier struct {
	corridors []domain.CorridorMetrics
	fees      []domain.FeeBreakdownEntry
	latency   *domain.LatencyPercentiles
	crypto    *domain.DepositAnalytics
	bank      *domain.DepositAnalytics
}

func (m *mockQuerier) GetCorridorMetrics(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]domain.CorridorMetrics, error) {
	return m.corridors, nil
}

func (m *mockQuerier) GetFeeBreakdown(_ context.Context, _ uuid.UUID, _, _ time.Time) ([]domain.FeeBreakdownEntry, error) {
	return m.fees, nil
}

func (m *mockQuerier) GetTransferLatencyPercentiles(_ context.Context, _ uuid.UUID, _, _ time.Time) (*domain.LatencyPercentiles, error) {
	return m.latency, nil
}

func (m *mockQuerier) GetCryptoDepositAnalytics(_ context.Context, _ uuid.UUID, _, _ time.Time) (*domain.DepositAnalytics, error) {
	return m.crypto, nil
}

func (m *mockQuerier) GetBankDepositAnalytics(_ context.Context, _ uuid.UUID, _, _ time.Time) (*domain.DepositAnalytics, error) {
	return m.bank, nil
}

type mockWriter struct {
	snapshots []domain.DailySnapshot
	tenantIDs []uuid.UUID
}

func (m *mockWriter) UpsertDailySnapshot(_ context.Context, snap domain.DailySnapshot) error {
	m.snapshots = append(m.snapshots, snap)
	return nil
}

func (m *mockWriter) ListActiveTenantIDs(_ context.Context) ([]uuid.UUID, error) {
	return m.tenantIDs, nil
}

func TestSnapshotScheduler_RunOnce(t *testing.T) {
	tenantID := uuid.New()

	querier := &mockQuerier{
		corridors: []domain.CorridorMetrics{
			{
				SourceCurrency: "GBP",
				DestCurrency:   "NGN",
				TransferCount:  100,
				VolumeUSD:      decimal.NewFromInt(50000),
				FeesUSD:        decimal.NewFromInt(500),
				Completed:      90,
				Failed:         10,
				SuccessRate:    decimal.NewFromFloat(90.00),
				AvgLatencyMs:   1500,
			},
		},
		fees: []domain.FeeBreakdownEntry{
			{
				SourceCurrency: "GBP",
				DestCurrency:   "NGN",
				TransferCount:  100,
				VolumeUSD:      decimal.NewFromInt(50000),
				OnRampFeesUSD:  decimal.NewFromInt(200),
				OffRampFeesUSD: decimal.NewFromInt(200),
				NetworkFeesUSD: decimal.NewFromInt(100),
				TotalFeesUSD:   decimal.NewFromInt(500),
			},
		},
		latency: &domain.LatencyPercentiles{
			SampleCount: 90,
			P50Ms:       1000,
			P90Ms:       2000,
			P95Ms:       3000,
			P99Ms:       5000,
		},
		crypto: &domain.DepositAnalytics{
			TotalSessions:     50,
			CompletedSessions: 40,
			ExpiredSessions:   5,
			FailedSessions:    5,
			ConversionRate:    decimal.NewFromFloat(80.00),
			TotalReceived:     decimal.NewFromInt(10000),
			TotalFees:         decimal.NewFromInt(100),
			TotalNet:          decimal.NewFromInt(9900),
		},
		bank: &domain.DepositAnalytics{
			TotalSessions:     0,
		},
	}

	writer := &mockWriter{
		tenantIDs: []uuid.UUID{tenantID},
	}

	scheduler := NewSnapshotScheduler(querier, writer, slog.Default())

	err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Expect: 1 transfer corridor + 1 crypto deposit = 2 snapshots
	if len(writer.snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(writer.snapshots))
	}

	// Verify transfer snapshot
	ts := writer.snapshots[0]
	if ts.MetricType != "transfer" {
		t.Errorf("expected metric_type=transfer, got %s", ts.MetricType)
	}
	if ts.TransferCount != 100 {
		t.Errorf("expected transfer_count=100, got %d", ts.TransferCount)
	}
	if !ts.OnRampFeesUSD.Equal(decimal.NewFromInt(200)) {
		t.Errorf("expected on_ramp_fees_usd=200, got %s", ts.OnRampFeesUSD)
	}

	// Verify crypto deposit snapshot
	cs := writer.snapshots[1]
	if cs.MetricType != "crypto_deposit" {
		t.Errorf("expected metric_type=crypto_deposit, got %s", cs.MetricType)
	}
	if cs.TransferCount != 50 {
		t.Errorf("expected transfer_count=50, got %d", cs.TransferCount)
	}
}
