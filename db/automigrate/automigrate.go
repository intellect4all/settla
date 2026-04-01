package automigrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

// DB identifies which bounded-context database to migrate.
type DB int

const (
	Ledger DB = iota
	Transfer
	Treasury
)

func (d DB) String() string {
	switch d {
	case Ledger:
		return "ledger"
	case Transfer:
		return "transfer"
	case Treasury:
		return "treasury"
	default:
		return "unknown"
	}
}

// Run applies all pending up-migrations for the given database.
// migrationFS should contain the .sql files at its root (already sub-dir'd).
// dbURL must be a raw Postgres connection string (not PgBouncer) because
// goose uses pg_advisory_lock which requires session-level state.
func Run(db DB, dbURL string, migrationFS fs.FS, logger *slog.Logger) error {
	sqlDB, err := sql.Open("postgres", dbURL)
	if err != nil {
		return fmt.Errorf("automigrate: open %s: %w", db, err)
	}
	defer sqlDB.Close()

	if err := sqlDB.PingContext(context.Background()); err != nil {
		return fmt.Errorf("automigrate: ping %s: %w", db, err)
	}

	// Configure goose to read migrations from the embedded FS.
	goose.SetBaseFS(migrationFS)
	defer goose.SetBaseFS(nil)

	// Suppress goose's default logger — we log ourselves.
	goose.SetLogger(goose.NopLogger())

	currentVersion, err := goose.GetDBVersion(sqlDB)
	if err != nil {
		currentVersion = 0
	}
	logger.Info("automigrate: current state", "db", db.String(), "version", currentVersion)

	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("automigrate: %s up: %w", db, err)
	}

	newVersion, _ := goose.GetDBVersion(sqlDB)
	if newVersion != currentVersion {
		logger.Info("automigrate: applied migrations", "db", db.String(), "from", currentVersion, "to", newVersion)
	} else {
		logger.Info("automigrate: already up to date", "db", db.String(), "version", newVersion)
	}

	return nil
}
