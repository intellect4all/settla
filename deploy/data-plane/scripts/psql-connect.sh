#!/usr/bin/env bash
# ============================================================================
# Quick psql connection helper
# ============================================================================
# Opens a psql session to one of the Settla databases.
#
# Usage:
#   ./psql-connect.sh <db>
#
#   <db> is one of: ledger, transfer, treasury
#
# Reads connection info from .env files in macbook-1/ and macbook-2/.
# ============================================================================

set -euo pipefail

DB="${1:-}"
if [ -z "$DB" ]; then
  echo "Usage: $0 {ledger|transfer|treasury}"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_PLANE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Look for .env.homelab in the kustomize overlay to get MacBook IPs
HOMELAB_ENV="$DATA_PLANE_DIR/../k8s/overlays/homelab/.env.homelab"
if [ ! -f "$HOMELAB_ENV" ]; then
  echo "ERROR: $HOMELAB_ENV not found."
  echo "Create it from .env.homelab.example and set MACBOOK_1_IP and MACBOOK_2_IP."
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$HOMELAB_ENV"
set +a

case "$DB" in
  transfer)
    HOST="$MACBOOK_1_IP"
    PORT=5434
    DBNAME=settla_transfer
    PW="${POSTGRES_TRANSFER_PASSWORD:-}"
    ;;
  ledger)
    HOST="$MACBOOK_2_IP"
    PORT=5433
    DBNAME=settla_ledger
    PW="${POSTGRES_LEDGER_PASSWORD:-}"
    ;;
  treasury)
    HOST="$MACBOOK_2_IP"
    PORT=5435
    DBNAME=settla_treasury
    PW="${POSTGRES_TREASURY_PASSWORD:-}"
    ;;
  *)
    echo "Unknown database: $DB"
    echo "Valid options: ledger, transfer, treasury"
    exit 1
    ;;
esac

echo "Connecting to $DBNAME at $HOST:$PORT ..."
PGPASSWORD="$PW" psql -h "$HOST" -p "$PORT" -U "${POSTGRES_USER:-settla}" -d "$DBNAME"
