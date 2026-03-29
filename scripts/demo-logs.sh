#!/usr/bin/env bash
set -euo pipefail

# demo-logs.sh — Tail application logs from demo services.
#
# Usage:
#   bash scripts/demo-logs.sh                    # tail all app services
#   bash scripts/demo-logs.sh --service=gateway  # tail specific service
#   bash scripts/demo-logs.sh --all              # include infrastructure

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

SERVICE=""
ALL=false

for arg in "$@"; do
  case "$arg" in
    --service=*) SERVICE="${arg#*=}" ;;
    --all)       ALL=true ;;
    -h|--help)
      echo "Usage: demo-logs.sh [--service=NAME] [--all]"
      echo ""
      echo "Services: settla-server, settla-node, gateway, mockprovider, webhook"
      echo "Flags:"
      echo "  --all       Include infrastructure services (postgres, nats, redis, etc.)"
      echo "  --service=X Tail only a specific service"
      exit 0
      ;;
  esac
done

COMPOSE_CMD="docker compose -f $PROJECT_ROOT/deploy/docker-compose.yml -f $PROJECT_ROOT/deploy/docker-compose.demo.yml"
if [ -f "$PROJECT_ROOT/.env" ]; then
  COMPOSE_CMD="$COMPOSE_CMD --env-file $PROJECT_ROOT/.env"
fi

if [ -n "$SERVICE" ]; then
  exec $COMPOSE_CMD logs -f "$SERVICE"
elif [ "$ALL" = true ]; then
  exec $COMPOSE_CMD logs -f
else
  # Default: only app services (skip noisy infrastructure)
  exec $COMPOSE_CMD logs -f settla-server settla-node gateway webhook mockprovider 2>/dev/null || \
  exec $COMPOSE_CMD logs -f settla-server settla-node gateway
fi
