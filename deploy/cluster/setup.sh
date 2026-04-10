#!/usr/bin/env bash
# ==========================================================================
# Multi-machine cluster setup helper
# Run from the repo root on each machine.
# ==========================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env.cluster"

if [ ! -f "$ENV_FILE" ]; then
    echo "ERROR: $ENV_FILE not found."
    echo "Run: cp deploy/cluster/.env.cluster.example deploy/cluster/.env.cluster"
    echo "Then edit with your actual machine IPs."
    exit 1
fi

# shellcheck source=/dev/null
source "$ENV_FILE"

usage() {
    cat <<EOF
Usage: $0 <command>

Commands:
  data-up         Start data tier (run on fedora-data)
  data-down       Stop data tier
  data-status     Show data tier status

  compute-up      Build and start compute tier (run on fedora-compute)
  compute-down    Stop compute tier
  compute-status  Show compute tier status
  compute-scale   Scale compute replicas (e.g., compute-scale 6 8)

  seed            Seed base data (50 tenants)
  seed-20k        Seed 20,000 tenants for scale tests
  seed-100k       Seed 100,000 tenants for scale tests
  cleanup         Remove scale-test tenant data

  check           Verify connectivity between machines
  status          Show status of all tiers
EOF
}

DATA_COMPOSE="docker compose -f ${SCRIPT_DIR}/docker-compose.data.yml --env-file ${ENV_FILE}"
COMPUTE_COMPOSE="docker compose -f ${SCRIPT_DIR}/docker-compose.compute.yml --env-file ${ENV_FILE}"

case "${1:-help}" in
    data-up)
        echo "Starting data tier..."
        $DATA_COMPOSE up -d
        echo "Waiting for health checks..."
        sleep 15
        $DATA_COMPOSE ps
        ;;
    data-down)
        $DATA_COMPOSE down
        ;;
    data-status)
        $DATA_COMPOSE ps
        ;;

    compute-up)
        echo "Building and starting compute tier..."
        $COMPUTE_COMPOSE up -d --build
        echo "Waiting for health checks..."
        sleep 20
        $COMPUTE_COMPOSE ps
        ;;
    compute-down)
        $COMPUTE_COMPOSE down
        ;;
    compute-status)
        $COMPUTE_COMPOSE ps
        ;;
    compute-scale)
        SERVERS="${2:-6}"
        NODES="${3:-8}"
        echo "Scaling to ${SERVERS} servers, ${NODES} nodes..."
        $COMPUTE_COMPOSE up -d --scale settla-server="${SERVERS}" --scale settla-node="${NODES}"
        ;;

    seed)
        echo "Seeding 50 base tenants..."
        PGPASSWORD="${POSTGRES_TRANSFER_PASSWORD}" psql -h "${DATA_HOST}" -p 5434 -U "${POSTGRES_USER}" -d settla_transfer < db/seed/transfer_seed.sql
        PGPASSWORD="${POSTGRES_TREASURY_PASSWORD}" psql -h "${DATA_HOST}" -p 5435 -U "${POSTGRES_USER}" -d settla_treasury < db/seed/treasury_seed.sql
        PGPASSWORD="${POSTGRES_LEDGER_PASSWORD}" psql -h "${DATA_HOST}" -p 5433 -U "${POSTGRES_USER}" -d settla_ledger < db/seed/ledger_seed.sql
        echo "Done. 50 tenants seeded."
        ;;
    seed-20k)
        echo "Seeding 20,000 tenants..."
        go run ./tests/loadtest/ seed -count=20000 \
            -transfer-db="postgres://${POSTGRES_USER}:${POSTGRES_TRANSFER_PASSWORD}@${DATA_HOST}:5434/settla_transfer?sslmode=disable" \
            -treasury-db="postgres://${POSTGRES_USER}:${POSTGRES_TREASURY_PASSWORD}@${DATA_HOST}:5435/settla_treasury?sslmode=disable"
        ;;
    seed-100k)
        echo "Seeding 100,000 tenants..."
        go run ./tests/loadtest/ seed -count=100000 \
            -transfer-db="postgres://${POSTGRES_USER}:${POSTGRES_TRANSFER_PASSWORD}@${DATA_HOST}:5434/settla_transfer?sslmode=disable" \
            -treasury-db="postgres://${POSTGRES_USER}:${POSTGRES_TREASURY_PASSWORD}@${DATA_HOST}:5435/settla_treasury?sslmode=disable"
        ;;
    cleanup)
        echo "Removing scale-test tenants..."
        go run ./tests/loadtest/ seed -cleanup \
            -transfer-db="postgres://${POSTGRES_USER}:${POSTGRES_TRANSFER_PASSWORD}@${DATA_HOST}:5434/settla_transfer?sslmode=disable" \
            -treasury-db="postgres://${POSTGRES_USER}:${POSTGRES_TREASURY_PASSWORD}@${DATA_HOST}:5435/settla_treasury?sslmode=disable"
        ;;

    check)
        echo "Checking connectivity..."
        echo -n "  Data tier (${DATA_HOST}): "
        if pg_isready -h "${DATA_HOST}" -p 5434 -U "${POSTGRES_USER}" -q 2>/dev/null; then
            echo "PostgreSQL OK"
        else
            echo "PostgreSQL UNREACHABLE"
        fi
        echo -n "  Redis (${DATA_HOST}:6380): "
        if redis-cli -h "${DATA_HOST}" -p 6380 -a "${REDIS_PASSWORD}" --no-auth-warning ping 2>/dev/null | grep -q PONG; then
            echo "OK"
        else
            echo "UNREACHABLE"
        fi
        echo -n "  Gateway (${COMPUTE_HOST}:3100): "
        if curl -sf "http://${COMPUTE_HOST}:3100/health" >/dev/null 2>&1; then
            echo "OK"
        else
            echo "UNREACHABLE"
        fi
        echo -n "  NATS (${COMPUTE_HOST}:8222): "
        if curl -sf "http://${COMPUTE_HOST}:8222/healthz" >/dev/null 2>&1; then
            echo "OK"
        else
            echo "UNREACHABLE"
        fi
        ;;

    status)
        echo "=== Data Tier (${DATA_HOST}) ==="
        echo -n "  PostgreSQL transfer: "; pg_isready -h "${DATA_HOST}" -p 5434 -U "${POSTGRES_USER}" -q 2>/dev/null && echo "UP" || echo "DOWN"
        echo -n "  PostgreSQL ledger:   "; pg_isready -h "${DATA_HOST}" -p 5433 -U "${POSTGRES_USER}" -q 2>/dev/null && echo "UP" || echo "DOWN"
        echo -n "  PostgreSQL treasury: "; pg_isready -h "${DATA_HOST}" -p 5435 -U "${POSTGRES_USER}" -q 2>/dev/null && echo "UP" || echo "DOWN"
        echo -n "  Redis:               "; redis-cli -h "${DATA_HOST}" -p 6380 -a "${REDIS_PASSWORD}" --no-auth-warning ping 2>/dev/null | grep -q PONG && echo "UP" || echo "DOWN"
        echo ""
        echo "=== Compute Tier (${COMPUTE_HOST}) ==="
        echo -n "  Gateway:  "; curl -sf "http://${COMPUTE_HOST}:3100/health" >/dev/null 2>&1 && echo "UP" || echo "DOWN"
        echo -n "  NATS:     "; curl -sf "http://${COMPUTE_HOST}:8222/healthz" >/dev/null 2>&1 && echo "UP" || echo "DOWN"
        ;;

    help|*)
        usage
        ;;
esac
