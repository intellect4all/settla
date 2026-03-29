#!/usr/bin/env bash
# demo-cleanup.sh — Remove all demo-seeded data from Settla databases.
# Preserves the two original seed tenants (Lemfi and Fincra).
#
# Usage:
#   ./scripts/demo-cleanup.sh
#   SETTLA_TRANSFER_DB_URL=postgres://... ./scripts/demo-cleanup.sh
#   ./scripts/demo-cleanup.sh --dry-run
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
TRANSFER_URL="${SETTLA_TRANSFER_DB_URL:-postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable}"
LEDGER_URL="${SETTLA_LEDGER_DB_URL:-postgres://settla:settla@localhost:5433/settla_ledger?sslmode=disable}"
TREASURY_URL="${SETTLA_TREASURY_DB_URL:-postgres://settla:settla@localhost:5435/settla_treasury?sslmode=disable}"

# Preserved seed tenants (never deleted)
LEMFI_ID="a0000000-0000-0000-0000-000000000001"
FINCRA_ID="b0000000-0000-0000-0000-000000000002"

DRY_RUN=false
if [[ "${1:-}" == "--dry-run" ]]; then
    DRY_RUN=true
    echo "[DRY RUN] No data will be modified."
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log() { echo "[$(date +%H:%M:%S)] $*"; }

run_sql() {
    local db_url="$1"
    local query="$2"
    if $DRY_RUN; then
        echo "  [DRY RUN] Would execute: ${query:0:120}..."
        return
    fi
    psql -q -t -A "$db_url" -c "$query" 2>&1 || true
}

count_sql() {
    local db_url="$1"
    local query="$2"
    psql -q -t -A "$db_url" -c "$query" 2>/dev/null | head -1 || echo "0"
}

# ---------------------------------------------------------------------------
# Step 1: Count what will be removed
# ---------------------------------------------------------------------------
log "Connecting to databases..."

TENANT_COUNT=$(count_sql "$TRANSFER_URL" \
    "SELECT count(*) FROM tenants WHERE metadata->>'seeded_by' = 'demo-seed' AND id NOT IN ('$LEMFI_ID','$FINCRA_ID');")
API_KEY_COUNT=$(count_sql "$TRANSFER_URL" \
    "SELECT count(*) FROM api_keys WHERE tenant_id IN (SELECT id FROM tenants WHERE metadata->>'seeded_by' = 'demo-seed' AND id NOT IN ('$LEMFI_ID','$FINCRA_ID'));")
POSITION_COUNT=$(count_sql "$TREASURY_URL" \
    "SELECT count(*) FROM positions WHERE tenant_id IN (SELECT unnest(string_to_array(
        (SELECT string_agg(id::text, ',') FROM dblink('$TRANSFER_URL',
        'SELECT id FROM tenants WHERE metadata->>''seeded_by'' = ''demo-seed'' AND id NOT IN (''$LEMFI_ID'',''$FINCRA_ID'')') AS t(id uuid)),
        ','))::uuid);" 2>/dev/null || echo "?")
ACCOUNT_COUNT=$(count_sql "$LEDGER_URL" \
    "SELECT count(*) FROM accounts WHERE metadata->>'seeded_by' = 'demo-seed';" 2>/dev/null || echo "?")

log "Found demo-seeded data:"
log "  Tenants:            ${TENANT_COUNT:-0}"
log "  API keys:           ${API_KEY_COUNT:-0}"
log "  Treasury positions: ${POSITION_COUNT:-?} (cross-DB count may not be available)"
log "  Ledger accounts:    ${ACCOUNT_COUNT:-?}"

if [[ "${TENANT_COUNT:-0}" == "0" ]]; then
    log "Nothing to clean up. Exiting."
    exit 0
fi

if ! $DRY_RUN; then
    echo ""
    read -r -p "Proceed with cleanup? [y/N] " confirm
    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        log "Aborted."
        exit 0
    fi
fi

# ---------------------------------------------------------------------------
# Step 2: Get tenant IDs to remove
# ---------------------------------------------------------------------------
log "Fetching tenant IDs..."
TENANT_IDS=$(psql -q -t -A "$TRANSFER_URL" -c \
    "SELECT id FROM tenants WHERE metadata->>'seeded_by' = 'demo-seed' AND id NOT IN ('$LEMFI_ID','$FINCRA_ID');" 2>/dev/null)

if [[ -z "$TENANT_IDS" ]]; then
    log "No tenant IDs found. Exiting."
    exit 0
fi

# Build a comma-separated quoted list for IN clauses
IN_LIST=""
while IFS= read -r tid; do
    [[ -z "$tid" ]] && continue
    if [[ -n "$IN_LIST" ]]; then
        IN_LIST="$IN_LIST,"
    fi
    IN_LIST="$IN_LIST'$tid'"
done <<< "$TENANT_IDS"

# ---------------------------------------------------------------------------
# Step 3: Clean ledger DB (accounts + balance_snapshots)
# ---------------------------------------------------------------------------
log "Cleaning ledger DB..."
run_sql "$LEDGER_URL" \
    "DELETE FROM balance_snapshots WHERE account_id IN (SELECT id FROM accounts WHERE metadata->>'seeded_by' = 'demo-seed');"
run_sql "$LEDGER_URL" \
    "DELETE FROM accounts WHERE metadata->>'seeded_by' = 'demo-seed';"
log "  Ledger accounts removed."

# ---------------------------------------------------------------------------
# Step 4: Clean treasury DB (positions)
# ---------------------------------------------------------------------------
log "Cleaning treasury DB..."
run_sql "$TREASURY_URL" \
    "DELETE FROM positions WHERE tenant_id IN ($IN_LIST);"
log "  Treasury positions removed."

# ---------------------------------------------------------------------------
# Step 5: Clean transfer DB (api_keys, then tenants — FK order)
# ---------------------------------------------------------------------------
log "Cleaning transfer DB..."
run_sql "$TRANSFER_URL" \
    "DELETE FROM api_keys WHERE tenant_id IN ($IN_LIST);"
log "  API keys removed."

run_sql "$TRANSFER_URL" \
    "DELETE FROM tenants WHERE id IN ($IN_LIST);"
log "  Tenants removed."

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
log "Cleanup complete."
log "Preserved tenants: Lemfi ($LEMFI_ID), Fincra ($FINCRA_ID)"
