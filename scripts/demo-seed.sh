#!/usr/bin/env bash
set -euo pipefail

# demo-seed.sh — Seed demo data with profile support.
#
# Usage:
#   bash scripts/demo-seed.sh                          # quick profile (10 tenants)
#   bash scripts/demo-seed.sh --profile=scale          # scale profile (20,000 tenants)
#   bash scripts/demo-seed.sh --tenant-count=50        # custom tenant count
#   bash scripts/demo-seed.sh --cleanup                # remove demo-seeded tenants
#   bash scripts/demo-seed.sh --skip-migrations        # skip migration step
#
# Requires: migrate CLI (optional), psql, Go toolchain

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Defaults
PROFILE="quick"
TENANT_COUNT=""
CLEANUP=false
SKIP_MIGRATIONS=false

# Parse arguments
for arg in "$@"; do
  case "$arg" in
    --profile=*)   PROFILE="${arg#*=}" ;;
    --tenant-count=*) TENANT_COUNT="${arg#*=}" ;;
    --cleanup)     CLEANUP=true ;;
    --skip-migrations) SKIP_MIGRATIONS=true ;;
    -h|--help)
      echo "Usage: demo-seed.sh [--profile=quick|scale] [--tenant-count=N] [--cleanup] [--skip-migrations]"
      echo ""
      echo "Profiles:"
      echo "  quick  — 10 tenants, minimal data (default, ~30s)"
      echo "  scale  — 20,000 tenants with Zipf-weighted traffic profiles (~2min)"
      echo "  stress — 100,000 tenants for stress testing (~10min)"
      exit 0
      ;;
    *) echo "Unknown argument: $arg"; exit 1 ;;
  esac
done

# Set tenant count from profile if not overridden
if [ -z "$TENANT_COUNT" ]; then
  case "$PROFILE" in
    quick)  TENANT_COUNT=10 ;;
    scale)  TENANT_COUNT=20000 ;;
    stress) TENANT_COUNT=100000 ;;
    *)      echo "Unknown profile: $PROFILE (use quick, scale, or stress)"; exit 1 ;;
  esac
fi

# Database URLs (raw Postgres for migrations, PgBouncer URLs for seed)
LEDGER_URL="${SETTLA_LEDGER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5433/settla_ledger?sslmode=disable}"
TRANSFER_URL="${SETTLA_TRANSFER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable}"
TREASURY_URL="${SETTLA_TREASURY_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5435/settla_treasury?sslmode=disable}"

START_TIME=$(date +%s)

echo "=== Settla Demo Seed ==="
echo "Profile:     $PROFILE"
echo "Tenants:     $TENANT_COUNT"
echo "Ledger DB:   ${LEDGER_URL%%@*}@***"
echo "Transfer DB: ${TRANSFER_URL%%@*}@***"
echo "Treasury DB: ${TREASURY_URL%%@*}@***"
echo ""

# Handle cleanup mode
if [ "$CLEANUP" = true ]; then
  echo "--- Cleaning up demo-seeded tenants..."
  cd "$PROJECT_ROOT"
  go run scripts/demo-seed.go \
    --cleanup \
    --transfer-db-url="$TRANSFER_URL" \
    --ledger-db-url="$LEDGER_URL" \
    --treasury-db-url="$TREASURY_URL" \
    --verbose
  echo ""
  echo "=== Cleanup complete ==="
  exit 0
fi

# Step 1: Run migrations (idempotent — only applies pending migrations)
if [ "$SKIP_MIGRATIONS" = false ]; then
  echo "--- Running migrations..."
  if command -v goose &>/dev/null; then
    goose -dir "$PROJECT_ROOT/db/migrations/ledger"   postgres "$LEDGER_URL"   up 2>&1 || true
    goose -dir "$PROJECT_ROOT/db/migrations/transfer" postgres "$TRANSFER_URL" up 2>&1 || true
    goose -dir "$PROJECT_ROOT/db/migrations/treasury" postgres "$TREASURY_URL" up 2>&1 || true
    echo "    Migrations applied."
  else
    echo "    WARNING: goose CLI not found. Skipping migrations."
    echo "    Install: go install github.com/pressly/goose/v3/cmd/goose@latest"
  fi
  echo ""
fi

# Step 2: Seed base data (Lemfi + Fincra, idempotent)
echo "--- Seeding base tenants (Lemfi, Fincra)..."
SEED_DIR="$PROJECT_ROOT/db/seed"
if [ -d "$SEED_DIR" ]; then
  for f in transfer_seed.sql ledger_seed.sql treasury_seed.sql; do
    if [ -f "$SEED_DIR/$f" ]; then
      psql "$TRANSFER_URL" -f "$SEED_DIR/$f" -q 2>/dev/null || \
      psql "$LEDGER_URL" -f "$SEED_DIR/$f" -q 2>/dev/null || \
      psql "$TREASURY_URL" -f "$SEED_DIR/$f" -q 2>/dev/null || true
    fi
  done
  # Seed each file against its correct DB
  [ -f "$SEED_DIR/transfer_seed.sql" ] && psql "$TRANSFER_URL" -f "$SEED_DIR/transfer_seed.sql" -q 2>/dev/null || true
  [ -f "$SEED_DIR/ledger_seed.sql" ]   && psql "$LEDGER_URL"   -f "$SEED_DIR/ledger_seed.sql"   -q 2>/dev/null || true
  [ -f "$SEED_DIR/treasury_seed.sql" ] && psql "$TREASURY_URL" -f "$SEED_DIR/treasury_seed.sql" -q 2>/dev/null || true
  echo "    Base tenants seeded."
else
  echo "    WARNING: $SEED_DIR not found. Skipping base seed."
fi
echo ""

# Step 3: Seed additional demo tenants
if [ "$TENANT_COUNT" -gt 0 ]; then
  echo "--- Seeding $TENANT_COUNT demo tenants..."
  cd "$PROJECT_ROOT"
  go run scripts/demo-seed.go \
    --tenant-count="$TENANT_COUNT" \
    --transfer-db-url="$TRANSFER_URL" \
    --ledger-db-url="$LEDGER_URL" \
    --treasury-db-url="$TREASURY_URL" \
    --verbose
  echo ""
fi

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo "=== Demo seed complete (${ELAPSED}s) ==="
echo "Profile: $PROFILE | Tenants: $TENANT_COUNT (+ Lemfi + Fincra)"
echo ""
echo "Base tenants:"
echo "  - Lemfi  (a0000000-0000-0000-0000-000000000001) — GBP→NGN corridor"
echo "  - Fincra (b0000000-0000-0000-0000-000000000002) — NGN→GBP corridor"
echo "  + $TENANT_COUNT demo tenants (Enterprise/Growth/Starter tiers)"
