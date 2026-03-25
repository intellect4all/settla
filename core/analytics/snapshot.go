package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// AnalyticsQuerier provides the query methods needed by the snapshot scheduler.
type AnalyticsQuerier interface {
	GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error)
	GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error)
	GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error)
	GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error)
	GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error)
}

// SnapshotWriter persists daily snapshot rows.
type SnapshotWriter interface {
	UpsertDailySnapshot(ctx context.Context, snap domain.DailySnapshot) error
	ForEachActiveTenant(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error
}

// SnapshotScheduler runs at 01:00 UTC daily, aggregating yesterday's data
// into analytics_daily_snapshots.
type SnapshotScheduler struct {
	querier  AnalyticsQuerier
	writer   SnapshotWriter
	logger   *slog.Logger
	interval time.Duration
}

// NewSnapshotScheduler creates a snapshot scheduler.
func NewSnapshotScheduler(
	querier AnalyticsQuerier,
	writer SnapshotWriter,
	logger *slog.Logger,
) *SnapshotScheduler {
	return &SnapshotScheduler{
		querier:  querier,
		writer:   writer,
		logger:   logger.With("module", "core.analytics.snapshot"),
		interval: 24 * time.Hour,
	}
}

// SetInterval overrides the default 24h interval (for testing).
func (s *SnapshotScheduler) SetInterval(d time.Duration) {
	s.interval = d
}

// Start begins the scheduler loop. Runs immediately then at each interval.
func (s *SnapshotScheduler) Start(ctx context.Context) error {
	s.logger.Info("settla-analytics: snapshot scheduler starting", "interval", s.interval.String())

	if err := s.tick(ctx); err != nil {
		s.logger.Error("settla-analytics: initial snapshot tick failed", "error", err)
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("settla-analytics: snapshot scheduler stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.logger.Error("settla-analytics: snapshot tick failed", "error", err)
			}
		}
	}
}

// RunOnce executes a single snapshot cycle. Useful for testing.
func (s *SnapshotScheduler) RunOnce(ctx context.Context) error {
	return s.tick(ctx)
}

func (s *SnapshotScheduler) tick(ctx context.Context) error {
	s.logger.Info("settla-analytics: snapshot tick starting")

	now := time.Now().UTC()
	dayEnd := now.Truncate(24 * time.Hour)
	dayStart := dayEnd.Add(-24 * time.Hour)
	snapshotDate := dayStart

	var errs atomic.Int64
	var total atomic.Int64

	err := s.writer.ForEachActiveTenant(ctx, domain.DefaultTenantBatchSize, func(ids []uuid.UUID) error {
		for _, tenantID := range ids {
			total.Add(1)
			if err := s.snapshotTenant(ctx, tenantID, dayStart, dayEnd, snapshotDate); err != nil {
				s.logger.Error("settla-analytics: snapshot failed for tenant",
					"tenant_id", tenantID,
					"error", err,
				)
				errs.Add(1)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("settla-analytics: tenant iteration failed: %w", err)
	}

	errCount := errs.Load()
	totalCount := total.Load()

	if errCount > 0 {
		return fmt.Errorf("settla-analytics: %d/%d tenants failed", errCount, totalCount)
	}

	s.logger.Info("settla-analytics: snapshot tick completed", "tenants", totalCount)
	return nil
}

func (s *SnapshotScheduler) snapshotTenant(ctx context.Context, tenantID uuid.UUID, from, to, snapshotDate time.Time) error {
	// Transfer corridor snapshots
	corridors, err := s.querier.GetCorridorMetrics(ctx, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("corridor metrics: %w", err)
	}

	fees, err := s.querier.GetFeeBreakdown(ctx, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("fee breakdown: %w", err)
	}

	latency, err := s.querier.GetTransferLatencyPercentiles(ctx, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("latency percentiles: %w", err)
	}

	// Build fee lookup by corridor
	feeMap := make(map[string]domain.FeeBreakdownEntry)
	for _, f := range fees {
		key := f.SourceCurrency + ":" + f.DestCurrency
		feeMap[key] = f
	}

	for _, c := range corridors {
		snap := domain.DailySnapshot{
			TenantID:       tenantID,
			SnapshotDate:   snapshotDate,
			MetricType:     "transfer",
			SourceCurrency: c.SourceCurrency,
			DestCurrency:   c.DestCurrency,
			TransferCount:  c.TransferCount,
			CompletedCount: c.Completed,
			FailedCount:    c.Failed,
			VolumeUSD:      c.VolumeUSD,
			FeesUSD:        c.FeesUSD,
			AvgLatencyMs:   c.AvgLatencyMs,
			P50LatencyMs:   latency.P50Ms,
			P90LatencyMs:   latency.P90Ms,
			P95LatencyMs:   latency.P95Ms,
			SuccessRate:    c.SuccessRate,
		}

		// Attach fee breakdown if available
		key := c.SourceCurrency + ":" + c.DestCurrency
		if f, ok := feeMap[string(key)]; ok {
			snap.OnRampFeesUSD = f.OnRampFeesUSD
			snap.OffRampFeesUSD = f.OffRampFeesUSD
			snap.NetworkFeesUSD = f.NetworkFeesUSD
		}

		if err := s.writer.UpsertDailySnapshot(ctx, snap); err != nil {
			return fmt.Errorf("upsert transfer snapshot: %w", err)
		}
	}

	// Crypto deposit snapshot
	crypto, err := s.querier.GetCryptoDepositAnalytics(ctx, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("crypto deposit analytics: %w", err)
	}
	if crypto.TotalSessions > 0 {
		if err := s.writer.UpsertDailySnapshot(ctx, domain.DailySnapshot{
			TenantID:       tenantID,
			SnapshotDate:   snapshotDate,
			MetricType:     "crypto_deposit",
			TransferCount:  crypto.TotalSessions,
			CompletedCount: crypto.CompletedSessions,
			FailedCount:    crypto.FailedSessions,
			VolumeUSD:      crypto.TotalReceived,
			FeesUSD:        crypto.TotalFees,
			SuccessRate:    crypto.ConversionRate,
		}); err != nil {
			return fmt.Errorf("upsert crypto deposit snapshot: %w", err)
		}
	}

	// Bank deposit snapshot
	bank, err := s.querier.GetBankDepositAnalytics(ctx, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("bank deposit analytics: %w", err)
	}
	if bank.TotalSessions > 0 {
		if err := s.writer.UpsertDailySnapshot(ctx, domain.DailySnapshot{
			TenantID:       tenantID,
			SnapshotDate:   snapshotDate,
			MetricType:     "bank_deposit",
			TransferCount:  bank.TotalSessions,
			CompletedCount: bank.CompletedSessions,
			FailedCount:    bank.FailedSessions,
			VolumeUSD:      bank.TotalReceived,
			FeesUSD:        bank.TotalFees,
			SuccessRate:    bank.ConversionRate,
		}); err != nil {
			return fmt.Errorf("upsert bank deposit snapshot: %w", err)
		}
	}

	return nil
}
