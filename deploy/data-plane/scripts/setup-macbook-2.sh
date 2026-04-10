#!/usr/bin/env bash
# ============================================================================
# MacBook 2 Setup Script — ledger DB + treasury DB
# ============================================================================
# Run this ON MacBook 2 after cloning the settla repo.
#
#   cd deploy/data-plane/macbook-2
#   ../scripts/setup-macbook-2.sh
# ============================================================================

set -euo pipefail

COMPOSE_DIR="$(cd "$(dirname "$0")/../macbook-2" && pwd)"
cd "$COMPOSE_DIR"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

echo "=== Settla Data Plane: MacBook 2 Setup ==="
echo "Services: postgres-ledger (5433), postgres-treasury (5435)"
echo ""

info "Checking prerequisites..."
command -v docker >/dev/null 2>&1 || error "Docker not installed. Install Docker Desktop for Mac."
docker info >/dev/null 2>&1 || error "Docker daemon not running. Start Docker Desktop."

if [ ! -f .env ]; then
  warn ".env not found. Copying from .env.example..."
  cp .env.example .env
  error "Edit .env and set POSTGRES_LEDGER_PASSWORD and POSTGRES_TREASURY_PASSWORD, then re-run this script."
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

if [ -z "${POSTGRES_LEDGER_PASSWORD:-}" ] || [ "$POSTGRES_LEDGER_PASSWORD" = "CHANGE_ME_STRONG_PASSWORD" ]; then
  error "POSTGRES_LEDGER_PASSWORD is not set in .env. Edit .env first."
fi

if [ -z "${POSTGRES_TREASURY_PASSWORD:-}" ] || [ "$POSTGRES_TREASURY_PASSWORD" = "CHANGE_ME_STRONG_PASSWORD" ]; then
  error "POSTGRES_TREASURY_PASSWORD is not set in .env. Edit .env first."
fi

LAN_IP=$(ifconfig | grep -E 'inet (192\.168\.|10\.)' | awk '{print $2}' | head -1)

info "Starting docker compose stack..."
docker compose up -d

info "Waiting for services to become healthy..."
for i in $(seq 1 60); do
  LEDGER_STATUS=$(docker inspect --format='{{.State.Health.Status}}' settla-postgres-ledger 2>/dev/null || echo "starting")
  TREASURY_STATUS=$(docker inspect --format='{{.State.Health.Status}}' settla-postgres-treasury 2>/dev/null || echo "starting")

  if [ "$LEDGER_STATUS" = "healthy" ] && [ "$TREASURY_STATUS" = "healthy" ]; then
    info "All services healthy."
    break
  fi

  printf "."
  sleep 2
done
echo ""

docker compose ps

echo ""
echo "=== Connection Info ==="
echo "  Ledger DB:    postgres://settla:<password>@${LAN_IP:-<mac2-ip>}:5433/settla_ledger"
echo "  Treasury DB:  postgres://settla:<password>@${LAN_IP:-<mac2-ip>}:5435/settla_treasury"
echo ""
echo "Add this to deploy/k8s/overlays/homelab/.env.homelab on the k8s controller:"
echo "  MACBOOK_2_IP=${LAN_IP:-<replace-me>}"
