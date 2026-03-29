//go:build e2e

package e2e

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// TestMain runs before all tests. It resets the address and virtual account
// pools so that tests have resources available regardless of prior runs.
func TestMain(m *testing.M) {
	if err := resetPools(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: could not reset pools (tests may skip deposit tests): %v\n", err)
	}
	os.Exit(m.Run())
}

func resetPools() error {
	dsn := os.Getenv("SETTLA_TRANSFER_DB_MIGRATE_URL")
	if dsn == "" {
		dsn = "postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	// Clear crypto deposit address index (allows address reuse)
	if _, err := db.Exec(`DELETE FROM crypto_deposit_address_index`); err != nil {
		return fmt.Errorf("clear address index: %w", err)
	}

	// Reset dispensed crypto addresses
	if _, err := db.Exec(`
		UPDATE crypto_address_pool
		SET dispensed = false, dispensed_at = NULL, session_id = NULL
		WHERE dispensed = true
	`); err != nil {
		return fmt.Errorf("reset crypto pool: %w", err)
	}

	// Reset virtual accounts
	if _, err := db.Exec(`
		UPDATE virtual_account_pool
		SET available = true, session_id = NULL
		WHERE available = false
	`); err != nil {
		return fmt.Errorf("reset virtual accounts: %w", err)
	}

	fmt.Println("e2e: address and virtual account pools reset")
	return nil
}
