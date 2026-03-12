package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DBExecutor abstracts database operations for DDL commands.
// Uses raw SQL because partition management requires DDL that ORMs and
// query builders typically cannot express.
type DBExecutor interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (Rows, error)
}

// CommandTag represents the result of an Exec command.
type CommandTag interface {
	RowsAffected() int64
}

// Rows represents the result set of a Query.
type Rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Close()
	Err() error
}

// PartitionConfig defines the partition strategy for a table.
type PartitionConfig struct {
	Table         string        // parent table name
	Database      string        // which database this table lives in
	Interval      string        // "daily", "weekly", or "monthly"
	CreateAhead   int           // number of future partitions to maintain
	DropOlderThan time.Duration // drop partitions older than this (0 = never drop)
}

// DefaultPartitionConfigs returns the standard partition configurations for Settla.
func DefaultPartitionConfigs() []PartitionConfig {
	return []PartitionConfig{
		{
			Table:         "outbox",
			Database:      "transfer",
			Interval:      "daily",
			CreateAhead:   3,
			DropOlderThan: 48 * time.Hour,
		},
		{
			Table:       "transfers",
			Database:    "transfer",
			Interval:    "monthly",
			CreateAhead: 2,
			// No auto-drop for transfers; archive is future work
		},
		{
			Table:       "transfer_events",
			Database:    "transfer",
			Interval:    "monthly",
			CreateAhead: 2,
		},
		{
			Table:       "entry_lines",
			Database:    "ledger",
			Interval:    "weekly",
			CreateAhead: 8,
		},
		{
			Table:       "position_history",
			Database:    "treasury",
			Interval:    "monthly",
			CreateAhead: 2,
		},
	}
}

// PartitionManager creates future partitions and archives old ones.
// At 500M outbox rows/day, partition management is critical.
// Uses DROP PARTITION (instant) instead of DELETE (vacuum nightmare).
type PartitionManager struct {
	db     DBExecutor
	logger *slog.Logger
	configs []PartitionConfig
}

// NewPartitionManager creates a partition manager with the given database executor.
func NewPartitionManager(db DBExecutor, logger *slog.Logger) *PartitionManager {
	return &PartitionManager{
		db:      db,
		logger:  logger.With("module", "core.maintenance.partition"),
		configs: DefaultPartitionConfigs(),
	}
}

// SetConfigs overrides the default partition configurations.
func (pm *PartitionManager) SetConfigs(configs []PartitionConfig) {
	pm.configs = configs
}

// ManagePartitions runs the full partition management cycle:
// 1. Create future partitions for all configured tables
// 2. Drop old partitions for tables with a DropOlderThan policy
// 3. Verify default partitions have no stale rows
func (pm *PartitionManager) ManagePartitions(ctx context.Context) error {
	pm.logger.Info("settla-maintenance: partition management starting")

	var errs []error

	for _, config := range pm.configs {
		if err := pm.createFuturePartitions(ctx, config); err != nil {
			pm.logger.Error("settla-maintenance: failed to create partitions",
				"table", config.Table,
				"error", err,
			)
			errs = append(errs, err)
		}

		if config.DropOlderThan > 0 {
			if err := pm.dropOldPartitions(ctx, config); err != nil {
				pm.logger.Error("settla-maintenance: failed to drop old partitions",
					"table", config.Table,
					"error", err,
				)
				errs = append(errs, err)
			}
		}
	}

	// Verify default partitions
	if err := pm.verifyDefaultPartitions(ctx); err != nil {
		pm.logger.Error("settla-maintenance: default partition verification failed",
			"error", err,
		)
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("settla-maintenance: %d partition errors: %v", len(errs), errs[0])
	}

	pm.logger.Info("settla-maintenance: partition management completed")
	return nil
}

// createFuturePartitions creates partitions ahead of the current date.
func (pm *PartitionManager) createFuturePartitions(ctx context.Context, config PartitionConfig) error {
	now := time.Now().UTC()

	for i := 0; i <= config.CreateAhead; i++ {
		var partStart, partEnd time.Time
		var partName string

		switch config.Interval {
		case "daily":
			partStart = now.AddDate(0, 0, i).Truncate(24 * time.Hour)
			partEnd = partStart.AddDate(0, 0, 1)
			partName = DailyPartitionName(config.Table, partStart)
		case "weekly":
			// Align to Monday
			weekday := int(now.Weekday())
			if weekday == 0 {
				weekday = 7
			}
			mondayOffset := 1 - weekday
			thisMonday := now.AddDate(0, 0, mondayOffset).Truncate(24 * time.Hour)
			partStart = thisMonday.AddDate(0, 0, 7*i)
			partEnd = partStart.AddDate(0, 0, 7)
			partName = WeeklyPartitionName(config.Table, partStart)
		case "monthly":
			partStart = time.Date(now.Year(), now.Month()+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
			partEnd = partStart.AddDate(0, 1, 0)
			partName = MonthlyPartitionName(config.Table, partStart)
		default:
			return fmt.Errorf("settla-maintenance: unknown interval %q for table %s", config.Interval, config.Table)
		}

		sql := CreatePartitionSQL(config.Table, partName, partStart, partEnd)
		if _, err := pm.db.Exec(ctx, sql); err != nil {
			return fmt.Errorf("settla-maintenance: creating partition %s: %w", partName, err)
		}

		pm.logger.Info("settla-maintenance: partition ensured",
			"table", config.Table,
			"partition", partName,
			"from", partStart.Format(time.DateOnly),
			"to", partEnd.Format(time.DateOnly),
		)
	}

	return nil
}

// dropOldPartitions drops partitions older than the configured threshold.
func (pm *PartitionManager) dropOldPartitions(ctx context.Context, config PartitionConfig) error {
	cutoff := time.Now().UTC().Add(-config.DropOlderThan)

	// Generate partition names for dates going back further than the cutoff.
	// We check up to 30 days back to catch any stragglers.
	maxLookback := 30
	if config.Interval == "monthly" {
		maxLookback = 12
	}

	for i := 0; i < maxLookback; i++ {
		var partDate time.Time
		var partName string

		switch config.Interval {
		case "daily":
			partDate = cutoff.AddDate(0, 0, -i).Truncate(24 * time.Hour)
			partName = DailyPartitionName(config.Table, partDate)
		case "weekly":
			partDate = cutoff.AddDate(0, 0, -7*i).Truncate(24 * time.Hour)
			partName = WeeklyPartitionName(config.Table, partDate)
		case "monthly":
			partDate = time.Date(cutoff.Year(), cutoff.Month()-time.Month(i), 1, 0, 0, 0, 0, time.UTC)
			partName = MonthlyPartitionName(config.Table, partDate)
		}

		sql := DropPartitionSQL(partName)
		if _, err := pm.db.Exec(ctx, sql); err != nil {
			pm.logger.Warn("settla-maintenance: failed to drop partition (may not exist)",
				"partition", partName,
				"error", err,
			)
			// Non-fatal: partition may not exist
		} else {
			pm.logger.Info("settla-maintenance: partition dropped",
				"table", config.Table,
				"partition", partName,
			)
		}
	}

	return nil
}

// verifyDefaultPartitions checks that default partitions contain no rows.
// If data lands in the default partition, it means a future partition is missing.
func (pm *PartitionManager) verifyDefaultPartitions(ctx context.Context) error {
	// Only check tables that we manage partitions for
	tablesToCheck := []string{"outbox"}

	for _, table := range tablesToCheck {
		defaultName := table + "_default"
		sql := DefaultPartitionCheckSQL(defaultName)

		rows, err := pm.db.Query(ctx, sql)
		if err != nil {
			pm.logger.Warn("settla-maintenance: failed to check default partition",
				"table", defaultName,
				"error", err,
			)
			continue
		}

		var count int64
		if rows.Next() {
			if err := rows.Scan(&count); err != nil {
				rows.Close()
				continue
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			continue
		}

		if count > 0 {
			pm.logger.Warn("settla-maintenance: data in default partition — future partitions may be missing",
				"table", defaultName,
				"row_count", count,
			)
		} else {
			pm.logger.Info("settla-maintenance: default partition is clean",
				"table", defaultName,
			)
		}
	}

	return nil
}

// --- SQL generation functions (exported for testing) ---

// DailyPartitionName returns a partition name like "outbox_y2026m03d15".
func DailyPartitionName(table string, date time.Time) string {
	return fmt.Sprintf("%s_y%dm%02dd%02d", table, date.Year(), date.Month(), date.Day())
}

// WeeklyPartitionName returns a partition name like "entry_lines_y2026w12".
func WeeklyPartitionName(table string, date time.Time) string {
	_, week := date.ISOWeek()
	return fmt.Sprintf("%s_y%dw%02d", table, date.Year(), week)
}

// MonthlyPartitionName returns a partition name like "transfers_y2026m03".
func MonthlyPartitionName(table string, date time.Time) string {
	return fmt.Sprintf("%s_y%dm%02d", table, date.Year(), date.Month())
}

// CreatePartitionSQL returns idempotent DDL for creating a partition.
func CreatePartitionSQL(parentTable, partitionName string, from, to time.Time) string {
	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		partitionName, parentTable,
		from.Format("2006-01-02"), to.Format("2006-01-02"),
	)
}

// DropPartitionSQL returns idempotent DDL for dropping a partition.
func DropPartitionSQL(partitionName string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s", partitionName)
}

// DefaultPartitionCheckSQL returns a query to count rows in a default partition.
func DefaultPartitionCheckSQL(defaultPartitionName string) string {
	return fmt.Sprintf("SELECT COUNT(*) FROM ONLY %s", defaultPartitionName)
}
