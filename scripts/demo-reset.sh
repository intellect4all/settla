#!/usr/bin/env bash
set -euo pipefail

# demo-reset.sh — Reset demo data without restarting containers.
#
# Usage:
#   bash scripts/demo-reset.sh              # reset with quick profile
#   bash scripts/demo-reset.sh --profile=scale  # reset with scale profile

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PROFILE="quick"
for arg in "$@"; do
  case "$arg" in
    --profile=*) PROFILE="${arg#*=}" ;;
    -h|--help)
      echo "Usage: demo-reset.sh [--profile=quick|scale]"
      exit 0
      ;;
  esac
done

echo "=== Resetting Demo Environment ==="
echo "Profile: $PROFILE"
echo ""

# Verify containers are running
COMPOSE_CMD="docker compose -f $PROJECT_ROOT/deploy/docker-compose.yml -f $PROJECT_ROOT/deploy/docker-compose.demo.yml"
if [ -f "$PROJECT_ROOT/.env" ]; then
  COMPOSE_CMD="$COMPOSE_CMD --env-file $PROJECT_ROOT/.env"
fi

RUNNING=$($COMPOSE_CMD ps --format '{{.Service}}' 2>/dev/null | wc -l | tr -d ' ')
if [ "$RUNNING" -lt 3 ]; then
  echo "ERROR: Demo environment is not running (only $RUNNING services found)."
  echo "Start it first with: bash scripts/demo-up.sh"
  exit 1
fi

# Step 1: Clean up existing demo-seeded data
echo "--- Cleaning up existing demo data..."
bash "$SCRIPT_DIR/demo-seed.sh" --cleanup --skip-migrations 2>/dev/null || true
echo ""

# Step 2: Re-seed with selected profile
echo "--- Re-seeding with profile: $PROFILE..."
bash "$SCRIPT_DIR/demo-seed.sh" --profile="$PROFILE" --skip-migrations
echo ""

# Step 3: Reset mock provider config
if curl -sf --max-time 3 "http://localhost:9095/health" >/dev/null 2>&1; then
  echo "--- Resetting mock provider config..."
  curl -sX POST http://localhost:9095/admin/reset | python3 -m json.tool 2>/dev/null || \
  curl -sX POST http://localhost:9095/admin/reset
  echo ""
fi

echo ""
echo "=== Reset complete ==="
