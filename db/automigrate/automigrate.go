package automigrate

import (
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
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
// golang-migrate uses pg_advisory_lock which requires session-level state.
func Run(db DB, dbURL string, migrationFS fs.FS, logger *slog.Logger) error {
	src, err := iofs.New(migrationFS, ".")
	if err != nil {
		return fmt.Errorf("automigrate: iofs source for %s: %w", db, err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return fmt.Errorf("automigrate: create migrator for %s: %w", db, err)
	}
	defer m.Close()

	ver, dirty, _ := m.Version()
	logger.Info("automigrate: current state", "db", db.String(), "version", ver, "dirty", dirty)

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("automigrate: %s up: %w", db, err)
	}

	newVer, _, _ := m.Version()
	if newVer != ver {
		logger.Info("automigrate: applied migrations", "db", db.String(), "from", ver, "to", newVer)
	} else {
		logger.Info("automigrate: already up to date", "db", db.String(), "version", newVer)
	}

	return nil
}
