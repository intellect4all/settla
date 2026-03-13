package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// CleanupDB is the interface the cleanup goroutine needs for executing DDL.
type CleanupDB interface {
	Exec(ctx context.Context, sql string, arguments ...interface{}) error
	QueryRow(ctx context.Context, sql string, args ...interface{}) Row
}

// Row is a minimal row scanner for single-value queries.
type Row interface {
	Scan(dest ...interface{}) error
}

// Cleanup manages outbox partition lifecycle.
//   - Drops daily partitions older than 48 hours (instant DDL, no vacuum needed).
//   - Creates new daily partitions 3 days ahead.
//   - Warns if the default partition contains rows (indicates a partition gap).
type Cleanup struct {
	db       CleanupDB
	logger   *slog.Logger
	interval time.Duration // how often to run (default: 1 hour)
	// retentionHours is the number of hours to keep partitions (default: 48).
	retentionHours int
	// lookAheadDays is how many days ahead to create partitions (default: 3).
	lookAheadDays int
}

// CleanupOption configures the Cleanup goroutine.
type CleanupOption func(*Cleanup)

// WithCleanupInterval sets the cleanup check interval (default 1 hour).
func WithCleanupInterval(d time.Duration) CleanupOption {
	return func(c *Cleanup) {
		c.interval = d
	}
}

// WithRetentionHours sets the partition retention period in hours (default 48).
func WithRetentionHours(h int) CleanupOption {
	return func(c *Cleanup) {
		c.retentionHours = h
	}
}

// WithLookAheadDays sets how many days ahead to create partitions (default 3).
func WithLookAheadDays(d int) CleanupOption {
	return func(c *Cleanup) {
		c.lookAheadDays = d
	}
}

// NewCleanup creates an outbox partition cleanup manager.
func NewCleanup(db CleanupDB, logger *slog.Logger, opts ...CleanupOption) *Cleanup {
	c := &Cleanup{
		db:             db,
		logger:         logger.With("component", "outbox-cleanup"),
		interval:       1 * time.Hour,
		retentionHours: 48,
		lookAheadDays:  3,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Run starts the cleanup loop. It blocks until ctx is cancelled.
// It runs an initial cleanup immediately, then repeats at the configured interval.
func (c *Cleanup) Run(ctx context.Context) error {
	c.logger.Info("settla-outbox: cleanup started",
		"interval", c.interval,
		"retention_hours", c.retentionHours,
		"look_ahead_days", c.lookAheadDays,
	)

	// Run once immediately on startup.
	c.runCycle(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("settla-outbox: cleanup stopped")
			return ctx.Err()
		case <-ticker.C:
			c.runCycle(ctx)
		}
	}
}

// runCycle performs one cleanup cycle: drop old partitions, create future ones, check default.
func (c *Cleanup) runCycle(ctx context.Context) {
	now := time.Now().UTC()

	c.dropOldPartitions(ctx, now)
	c.createFuturePartitions(ctx, now)
	c.checkDefaultPartition(ctx)
}

// dropOldPartitions drops outbox daily partitions older than the retention period.
func (c *Cleanup) dropOldPartitions(ctx context.Context, now time.Time) {
	cutoff := now.Add(-time.Duration(c.retentionHours) * time.Hour)

	// Drop partitions for the 7 days before the cutoff to catch any stragglers.
	for i := 0; i < 7; i++ {
		day := cutoff.AddDate(0, 0, -i)
		partitionName := fmt.Sprintf("outbox_y%dm%02dd%02d", day.Year(), day.Month(), day.Day())

		sql := fmt.Sprintf("DROP TABLE IF EXISTS %s", partitionName)
		if err := c.db.Exec(ctx, sql); err != nil {
			c.logger.Error("settla-outbox: failed to drop partition",
				"partition", partitionName,
				"error", err,
			)
			continue
		}

		c.logger.Debug("settla-outbox: dropped partition (if existed)",
			"partition", partitionName,
		)
	}
}

// createFuturePartitions creates daily outbox partitions for the next N days.
func (c *Cleanup) createFuturePartitions(ctx context.Context, now time.Time) {
	for i := 0; i <= c.lookAheadDays; i++ {
		day := now.AddDate(0, 0, i)
		nextDay := day.AddDate(0, 0, 1)

		partitionName := fmt.Sprintf("outbox_y%dm%02dd%02d", day.Year(), day.Month(), day.Day())
		rangeStart := day.Format("2006-01-02")
		rangeEnd := nextDay.Format("2006-01-02")

		sql := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF outbox FOR VALUES FROM ('%s') TO ('%s')`,
			partitionName, rangeStart, rangeEnd,
		)
		if err := c.db.Exec(ctx, sql); err != nil {
			c.logger.Error("settla-outbox: failed to create partition",
				"partition", partitionName,
				"error", err,
			)
			continue
		}

		c.logger.Debug("settla-outbox: ensured partition exists",
			"partition", partitionName,
			"range_start", rangeStart,
			"range_end", rangeEnd,
		)
	}
}

// checkDefaultPartition warns if the default partition has any rows,
// which indicates a partition gap (rows falling outside defined partitions).
func (c *Cleanup) checkDefaultPartition(ctx context.Context) {
	var count int64
	row := c.db.QueryRow(ctx, "SELECT count(*) FROM outbox_default")
	if err := row.Scan(&count); err != nil {
		c.logger.Error("settla-outbox: failed to check default partition", "error", err)
		return
	}
	if count > 0 {
		c.logger.Warn("settla-outbox: default partition has rows — partition gap detected",
			"row_count", count,
		)
	}
}
