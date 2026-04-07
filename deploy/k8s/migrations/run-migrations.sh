#!/bin/sh
# ============================================================================
# Migration Runner (Goose)
# ============================================================================
# Runs goose against all 3 external PostgreSQL databases.
#
# Expects these environment variables:
#   SETTLA_LEDGER_DB_MIGRATE_URL    Direct Postgres URL for ledger
#   SETTLA_TRANSFER_DB_MIGRATE_URL  Direct Postgres URL for transfer
#   SETTLA_TREASURY_DB_MIGRATE_URL  Direct Postgres URL for treasury
#
# IMPORTANT: Must use DIRECT Postgres URLs, not PgBouncer. Goose uses
# session-level advisory locks which do not work with PgBouncer's
# transaction pooling mode.
# ============================================================================

set -e

echo "=== Settla Migration Runner (goose) ==="
echo "Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

# Verify all URLs are set
: "${SETTLA_LEDGER_DB_MIGRATE_URL:?SETTLA_LEDGER_DB_MIGRATE_URL is required}"
: "${SETTLA_TRANSFER_DB_MIGRATE_URL:?SETTLA_TRANSFER_DB_MIGRATE_URL is required}"
: "${SETTLA_TREASURY_DB_MIGRATE_URL:?SETTLA_TREASURY_DB_MIGRATE_URL is required}"

# Mask passwords in log output
mask_url() {
  echo "$1" | sed -E 's|://[^:]+:[^@]+@|://***:***@|'
}

echo "Target databases:"
echo "  Ledger:   $(mask_url "$SETTLA_LEDGER_DB_MIGRATE_URL")"
echo "  Transfer: $(mask_url "$SETTLA_TRANSFER_DB_MIGRATE_URL")"
echo "  Treasury: $(mask_url "$SETTLA_TREASURY_DB_MIGRATE_URL")"
echo ""

run_migrations() {
  context="$1"
  url="$2"
  dir="/migrations/${context}"

  echo "── Migrating ${context} ──"

  # Show current version
  version_before=$(goose -dir "$dir" postgres "$url" version 2>&1 || echo "none")
  echo "  Current version: ${version_before}"

  # Apply pending migrations
  if goose -dir "$dir" postgres "$url" up; then
    version_after=$(goose -dir "$dir" postgres "$url" version 2>&1 || echo "unknown")
    echo "  New version: ${version_after}"
    echo "  ${context} migrated successfully"
  else
    echo "  ${context} migration failed"
    echo "  Check logs above. For stuck state, use:"
    echo "    goose -dir $dir postgres <url> status"
    return 1
  fi
  echo ""
}

# Run in order: ledger, treasury, transfer
run_migrations ledger    "$SETTLA_LEDGER_DB_MIGRATE_URL"
run_migrations treasury  "$SETTLA_TREASURY_DB_MIGRATE_URL"
run_migrations transfer  "$SETTLA_TRANSFER_DB_MIGRATE_URL"

echo "=== All migrations applied successfully ==="
