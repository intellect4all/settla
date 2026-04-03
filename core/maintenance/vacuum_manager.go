package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

// VacuumConfig defines the vacuum schedule for a table.
type VacuumConfig struct {
	Table    string        // table name
	Database string        // which database
	Interval time.Duration // how often to vacuum
}

// DefaultVacuumConfigs returns the standard vacuum schedules for Settla hot tables.
func DefaultVacuumConfigs() []VacuumConfig {
	return []VacuumConfig{
		{Table: "transfers", Database: "transfer", Interval: 2 * time.Hour},
		{Table: "outbox", Database: "transfer", Interval: 1 * time.Hour},
		{Table: "positions", Database: "treasury", Interval: 30 * time.Minute},
	}
}

// VacuumManager runs VACUUM ANALYZE on hot tables at configured intervals.
// NEVER uses VACUUM FULL (blocks all operations).
type VacuumManager struct {
	db      DBExecutor
	logger  *slog.Logger
	configs []VacuumConfig
	lastRun map[string]time.Time // table -> last vacuum time
}

// NewVacuumManager creates a vacuum manager for the given database.
func NewVacuumManager(db DBExecutor, logger *slog.Logger) *VacuumManager {
	return &VacuumManager{
		db:      db,
		logger:  logger.With("module", "core.maintenance.vacuum"),
		configs: DefaultVacuumConfigs(),
		lastRun: make(map[string]time.Time),
	}
}

// SetConfigs overrides the default vacuum configurations.
func (vm *VacuumManager) SetConfigs(configs []VacuumConfig) {
	vm.configs = configs
}

// RunDueVacuums checks each table's last vacuum time and runs VACUUM ANALYZE
// on any tables that are overdue. This should be called periodically (e.g., every 5 minutes).
func (vm *VacuumManager) RunDueVacuums(ctx context.Context) error {
	now := time.Now().UTC()

	for _, config := range vm.configs {
		last, ok := vm.lastRun[config.Table]
		if ok && now.Sub(last) < config.Interval {
			continue // not yet due
		}

		if err := vm.vacuumAnalyze(ctx, config.Table); err != nil {
			vm.logger.Error("settla-maintenance: vacuum failed",
				"table", config.Table,
				"error", err,
			)
			// Continue to next table; don't fail the whole run
			continue
		}

		vm.lastRun[config.Table] = now
	}

	return nil
}

// vacuumAnalyze runs VACUUM ANALYZE on a single table.
// NEVER runs VACUUM FULL — it blocks all operations and rewrites the entire table.
func (vm *VacuumManager) vacuumAnalyze(ctx context.Context, table string) error {
	sql, err := VacuumAnalyzeSQL(table)
	if err != nil {
		return err
	}

	start := time.Now()
	if _, err := vm.db.Exec(ctx, sql); err != nil {
		return fmt.Errorf("settla-maintenance: VACUUM ANALYZE %s: %w", table, err)
	}
	duration := time.Since(start)

	vm.logger.Info("settla-maintenance: vacuum completed",
		"table", table,
		"duration_ms", duration.Milliseconds(),
	)

	return nil
}

// validIdentifier matches safe PostgreSQL identifiers (schema.table or table).
var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// VacuumAnalyzeSQL returns the SQL command for vacuuming a table with statistics update.
// Always uses VACUUM ANALYZE (lightweight), never VACUUM FULL (blocking).
// Returns an error if the table name contains unsafe characters.
func VacuumAnalyzeSQL(table string) (string, error) {
	if !validIdentifier.MatchString(table) {
		return "", fmt.Errorf("unsafe table identifier: %q", table)
	}
	return fmt.Sprintf("VACUUM ANALYZE %s", table), nil
}
