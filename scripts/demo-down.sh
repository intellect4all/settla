#!/usr/bin/env bash
set -euo pipefail

# demo-down.sh — Clean shutdown of the demo environment.
# Removes all containers and volumes.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

COMPOSE_CMD="docker compose -f $PROJECT_ROOT/deploy/docker-compose.yml -f $PROJECT_ROOT/deploy/docker-compose.demo.yml"

if [ -f "$PROJECT_ROOT/.env" ]; then
  COMPOSE_CMD="$COMPOSE_CMD --env-file $PROJECT_ROOT/.env"
fi

echo "--- Shutting down demo environment..."
$COMPOSE_CMD down -v --remove-orphans 2>&1 | tail -5
echo ""
echo "Demo environment stopped. All volumes removed."
echo "Run 'bash scripts/demo-up.sh' to start fresh."
