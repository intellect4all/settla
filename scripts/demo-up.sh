#!/usr/bin/env bash
set -euo pipefail

# demo-up.sh — Start the full Settla demo environment.
#
# Builds all containers, waits for health, seeds data, and prints URLs.
# Idempotent: safe to run multiple times.
#
# Usage:
#   bash scripts/demo-up.sh                   # quick profile (10 tenants)
#   bash scripts/demo-up.sh --profile=scale   # scale profile (20,000 tenants)
#   bash scripts/demo-up.sh --no-seed         # start services without seeding
#   bash scripts/demo-up.sh --no-mock         # use in-process mock (no mockprovider)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Defaults
PROFILE="quick"
SEED=true
USE_MOCK_HTTP=true

for arg in "$@"; do
  case "$arg" in
    --profile=*)  PROFILE="${arg#*=}" ;;
    --no-seed)    SEED=false ;;
    --no-mock)    USE_MOCK_HTTP=false ;;
    -h|--help)
      echo "Usage: demo-up.sh [--profile=quick|scale] [--no-seed] [--no-mock]"
      exit 0
      ;;
  esac
done

# Compose command
if [ "$USE_MOCK_HTTP" = true ]; then
  COMPOSE_CMD="docker compose -f $PROJECT_ROOT/deploy/docker-compose.yml -f $PROJECT_ROOT/deploy/docker-compose.demo.yml"
else
  COMPOSE_CMD="docker compose -f $PROJECT_ROOT/deploy/docker-compose.yml"
fi

# Load .env if present
if [ -f "$PROJECT_ROOT/.env" ]; then
  COMPOSE_CMD="$COMPOSE_CMD --env-file $PROJECT_ROOT/.env"
elif [ -f "$PROJECT_ROOT/.env.example" ]; then
  echo "--- No .env found, copying from .env.example..."
  cp "$PROJECT_ROOT/.env.example" "$PROJECT_ROOT/.env"
  COMPOSE_CMD="$COMPOSE_CMD --env-file $PROJECT_ROOT/.env"
fi

START_TIME=$(date +%s)

echo "============================================"
echo "  Settla Demo Environment"
echo "============================================"
echo "Profile:    $PROFILE"
echo "Mock HTTP:  $USE_MOCK_HTTP"
echo "Seed data:  $SEED"
echo ""

# Step 1: Build and start containers
echo "--- Building and starting containers..."
$COMPOSE_CMD up -d --build 2>&1 | tail -5
echo ""

# Step 2: Wait for services to become healthy
echo "--- Waiting for services to become healthy..."

wait_for_health() {
  local name="$1"
  local url="$2"
  local max_attempts="${3:-60}"
  local attempt=0

  while [ $attempt -lt $max_attempts ]; do
    if curl -sf --max-time 3 "$url" >/dev/null 2>&1; then
      printf "  %-20s UP\n" "$name"
      return 0
    fi
    attempt=$((attempt + 1))
    sleep 3
  done

  printf "  %-20s TIMEOUT (after %ds)\n" "$name" $((max_attempts * 3))
  return 1
}

HEALTH_FAILED=false

# Core infrastructure (wait longer)
wait_for_health "NATS"            "http://localhost:8222/healthz"  40 || HEALTH_FAILED=true
wait_for_health "Prometheus"      "http://localhost:9090/-/healthy" 30 || HEALTH_FAILED=true

# Application services
wait_for_health "settla-server"   "http://localhost:8081/health"  60 || HEALTH_FAILED=true
wait_for_health "settla-node"     "http://localhost:9094/health"  60 || true  # metrics-only, may not have /health
wait_for_health "Gateway"         "http://localhost:8080/health"  40 || HEALTH_FAILED=true
wait_for_health "Webhook"         "http://localhost:3001/health"  30 || true

# Monitoring
wait_for_health "Grafana"         "http://localhost:3000/api/health" 30 || true

# Mock provider (only if enabled)
if [ "$USE_MOCK_HTTP" = true ]; then
  wait_for_health "Mock Provider"   "http://localhost:9095/health"  20 || HEALTH_FAILED=true
fi

echo ""

if [ "$HEALTH_FAILED" = true ]; then
  echo "WARNING: Some services did not become healthy. Check logs with:"
  echo "  bash scripts/demo-logs.sh"
  echo ""
fi

# Step 3: Seed data
if [ "$SEED" = true ]; then
  echo "--- Seeding demo data (profile: $PROFILE)..."
  bash "$SCRIPT_DIR/demo-seed.sh" --profile="$PROFILE"
  echo ""
fi

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

# Print summary
echo "============================================"
echo "  Demo Environment Ready (${ELAPSED}s)"
echo "============================================"
echo ""
echo "  Service             URL"
echo "  ─────────────────── ──────────────────────────────────"
echo "  Gateway API         http://localhost:8080"
echo "  API Docs            http://localhost:8080/docs"
echo "  Grafana             http://localhost:3000  (admin / settla-dev-local)"
echo "  Prometheus          http://localhost:9090"
echo "  NATS Monitoring     http://localhost:8222"
if [ "$USE_MOCK_HTTP" = true ]; then
echo "  Mock Provider       http://localhost:9095/admin/config"
fi
echo "  Webhook Receiver    http://localhost:3001"
echo "  Metabase            http://localhost:3003"
echo "  settla-server       http://localhost:8081/health"
echo "  pprof               http://localhost:6060/debug/pprof/"
echo ""
echo "  Quick commands:"
echo "    bash scripts/demo-status.sh       # Check service health"
echo "    bash scripts/demo-logs.sh         # Tail application logs"
if [ "$USE_MOCK_HTTP" = true ]; then
echo "    curl -sX POST http://localhost:9095/admin/scenarios/provider-outage"
echo "    curl -sX POST http://localhost:9095/admin/reset"
fi
echo "    bash scripts/demo-down.sh         # Tear down everything"
echo ""
