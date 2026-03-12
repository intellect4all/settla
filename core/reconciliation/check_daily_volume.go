package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// VolumeQuerier retrieves daily transfer counts for volume analysis.
type VolumeQuerier interface {
	// GetDailyTransferCount returns the total number of transfers for a given date.
	GetDailyTransferCount(ctx context.Context, date time.Time) (int, error)
	// GetAverageDailyTransferCount returns the average daily transfer count
	// over the date range [startDate, endDate].
	GetAverageDailyTransferCount(ctx context.Context, startDate, endDate time.Time) (float64, error)
}

// DailyVolumeCheck compares today's transaction volume against the 7-day rolling
// average. It warns at 200% of average and fails at 500%.
type DailyVolumeCheck struct {
	store        VolumeQuerier
	logger       *slog.Logger
	warnPercent  float64 // default: 200
	failPercent  float64 // default: 500
}

// NewDailyVolumeCheck creates a DailyVolumeCheck with default thresholds
// (warn at 200%, fail at 500%).
func NewDailyVolumeCheck(store VolumeQuerier, logger *slog.Logger) *DailyVolumeCheck {
	return &DailyVolumeCheck{
		store:       store,
		logger:      logger,
		warnPercent: 200,
		failPercent: 500,
	}
}

// Name returns the check identifier.
func (c *DailyVolumeCheck) Name() string {
	return "daily_volume_sanity"
}

// Run compares today's transfer count against the 7-day average.
func (c *DailyVolumeCheck) Run(ctx context.Context) (*CheckResult, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	todayCount, err := c.store.GetDailyTransferCount(ctx, today)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: getting today's transfer count: %w", err)
	}

	// 7-day average: yesterday back to 7 days ago
	endDate := today.Add(-24 * time.Hour)
	startDate := today.Add(-7 * 24 * time.Hour)

	avg, err := c.store.GetAverageDailyTransferCount(ctx, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: getting average transfer count: %w", err)
	}

	// If there's no historical data, pass without comparison.
	if avg == 0 {
		return &CheckResult{
			Name:      c.Name(),
			Status:    "pass",
			Details:   fmt.Sprintf("today=%d, no historical average available", todayCount),
			CheckedAt: time.Now().UTC(),
		}, nil
	}

	pct := (float64(todayCount) / avg) * 100

	status := "pass"
	details := fmt.Sprintf("today=%d avg=%.0f ratio=%.1f%%", todayCount, avg, pct)

	if pct >= c.failPercent {
		status = "fail"
		details = fmt.Sprintf("EXTREME SPIKE: today=%d avg=%.0f ratio=%.1f%% (threshold=%.0f%%)",
			todayCount, avg, pct, c.failPercent)
		c.logger.Error("settla-reconciliation: extreme volume spike",
			slog.Int("today_count", todayCount),
			slog.Float64("average", avg),
			slog.Float64("ratio_pct", pct),
		)
	} else if pct >= c.warnPercent {
		status = "warn"
		details = fmt.Sprintf("VOLUME SPIKE: today=%d avg=%.0f ratio=%.1f%% (threshold=%.0f%%)",
			todayCount, avg, pct, c.warnPercent)
		c.logger.Warn("settla-reconciliation: volume spike detected",
			slog.Int("today_count", todayCount),
			slog.Float64("average", avg),
			slog.Float64("ratio_pct", pct),
		)
	}

	return &CheckResult{
		Name:      c.Name(),
		Status:    status,
		Details:   details,
		CheckedAt: time.Now().UTC(),
	}, nil
}
