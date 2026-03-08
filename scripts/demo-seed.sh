#!/usr/bin/env bash
set -euo pipefail

# demo-seed.sh — Reset databases, run migrations, and seed both tenants.
# Requires: migrate CLI, psql, and env vars (or .env file loaded).
#
# Usage:
#   source .env && bash scripts/demo-seed.sh
#   # or with docker-compose running:
#   make docker-up && bash scripts/demo-seed.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Default to raw Postgres URLs (not PgBouncer — migrations need direct connections)
LEDGER_URL="${SETTLA_LEDGER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5433/settla_ledger?sslmode=disable}"
TRANSFER_URL="${SETTLA_TRANSFER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable}"
TREASURY_URL="${SETTLA_TREASURY_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5435/settla_treasury?sslmode=disable}"

echo "=== Settla Demo Seed ==="
echo "Ledger DB:   ${LEDGER_URL%%@*}@***"
echo "Transfer DB: ${TRANSFER_URL%%@*}@***"
echo "Treasury DB: ${TREASURY_URL%%@*}@***"
echo ""

# Step 1: Roll back all migrations (ignore errors if tables don't exist)
echo "--- Rolling back migrations..."
migrate -path "$PROJECT_ROOT/db/migrations/treasury" -database "$TREASURY_URL" down -all 2>/dev/null || true
migrate -path "$PROJECT_ROOT/db/migrations/transfer" -database "$TRANSFER_URL" down -all 2>/dev/null || true
migrate -path "$PROJECT_ROOT/db/migrations/ledger"   -database "$LEDGER_URL"   down -all 2>/dev/null || true
echo "    Done."

# Step 2: Run all migrations up
echo "--- Running migrations..."
migrate -path "$PROJECT_ROOT/db/migrations/ledger"   -database "$LEDGER_URL"   up
migrate -path "$PROJECT_ROOT/db/migrations/transfer" -database "$TRANSFER_URL" up
migrate -path "$PROJECT_ROOT/db/migrations/treasury" -database "$TREASURY_URL" up
echo "    Done."

# Step 3: Seed data
echo "--- Seeding data..."
psql "$TRANSFER_URL" -f "$PROJECT_ROOT/db/seed/transfer_seed.sql"
psql "$LEDGER_URL"   -f "$PROJECT_ROOT/db/seed/ledger_seed.sql"
psql "$TREASURY_URL" -f "$PROJECT_ROOT/db/seed/treasury_seed.sql"
echo "    Done."

echo ""
echo "=== Demo seed complete ==="
echo "Tenants seeded:"
echo "  - Lemfi  (a0000000-0000-0000-0000-000000000001) — GBP→NGN corridor"
echo "  - Fincra (b0000000-0000-0000-0000-000000000002) — NGN→GBP corridor"
echo ""
echo "Start the server: make build && ./bin/settla-server"
