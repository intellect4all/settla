#!/usr/bin/env bash
# ============================================================================
# MacBook 1 Setup Script — transfer DB + TigerBeetle
# ============================================================================
# Run this ON MacBook 1 after cloning the settla repo.
#
#   cd deploy/data-plane/macbook-1
#   ../scripts/setup-macbook-1.sh
# ============================================================================

set -euo pipefail

COMPOSE_DIR="$(cd "$(dirname "$0")/../macbook-1" && pwd)"
cd "$COMPOSE_DIR"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

echo "=== Settla Data Plane: MacBook 1 Setup ==="
echo "Services: postgres-transfer (5434), tigerbeetle (3001)"
echo ""

# --- Prerequisites ---
info "Checking prerequisites..."
command -v docker >/dev/null 2>&1 || error "Docker not installed. Install Docker Desktop for Mac."
docker info >/dev/null 2>&1 || error "Docker daemon not running. Start Docker Desktop."
command -v docker compose >/dev/null 2>&1 || error "Docker Compose not available."

# --- Prevent Mac from sleeping during operation ---
info "Disabling system sleep (requires sudo)..."
if command -v caffeinate >/dev/null 2>&1; then
  warn "Start a caffeinate session in a separate terminal: caffeinate -dimsu"
  warn "Or disable sleep permanently in System Settings → Energy Saver"
fi

# --- Check .env ---
if [ ! -f .env ]; then
  warn ".env not found. Copying from .env.example..."
  cp .env.example .env
  error "Edit .env and set POSTGRES_TRANSFER_PASSWORD, then re-run this script."
fi

# Ensure POSTGRES_TRANSFER_PASSWORD is set
set -a
# shellcheck disable=SC1091
. ./.env
set +a

if [ -z "${POSTGRES_TRANSFER_PASSWORD:-}" ] || [ "$POSTGRES_TRANSFER_PASSWORD" = "CHANGE_ME_STRONG_PASSWORD" ]; then
  error "POSTGRES_TRANSFER_PASSWORD is not set in .env. Edit .env first."
fi

# --- Get LAN IP for informational output ---
LAN_IP=$(ifconfig | grep -E 'inet (192\.168\.|10\.)' | awk '{print $2}' | head -1)
if [ -z "$LAN_IP" ]; then
  warn "Could not detect LAN IP. Check manually with: ifconfig | grep inet"
fi

# --- Start services ---
info "Starting docker compose stack..."
docker compose up -d

info "Waiting for services to become healthy..."
for i in $(seq 1 60); do
  TRANSFER_STATUS=$(docker inspect --format='{{.State.Health.Status}}' settla-postgres-transfer 2>/dev/null || echo "starting")
  TB_STATUS=$(docker inspect --format='{{.State.Health.Status}}' settla-tigerbeetle 2>/dev/null || echo "starting")

  if [ "$TRANSFER_STATUS" = "healthy" ] && [ "$TB_STATUS" = "healthy" ]; then
    info "All services healthy."
    break
  fi

  printf "."
  sleep 2
done
echo ""

docker compose ps

# --- Display connection info ---
echo ""
echo "=== Connection Info ==="
echo "  Transfer DB:  postgres://settla:<password>@${LAN_IP:-<mac1-ip>}:5434/settla_transfer"
echo "  TigerBeetle:  ${LAN_IP:-<mac1-ip>}:3001"
echo ""
echo "Add these to deploy/k8s/overlays/homelab/.env.homelab on the k8s controller:"
echo "  MACBOOK_1_IP=${LAN_IP:-<replace-me>}"
echo ""
echo "=== Next Steps ==="
echo "  1. On MacBook 2: run ../scripts/setup-macbook-2.sh"
echo "  2. Verify LAN connectivity: ../scripts/verify.sh ${LAN_IP:-<mac1-ip>} <mac2-ip>"
echo "  3. Run migrations from your workstation:"
echo "     export SETTLA_TRANSFER_DB_URL=postgres://settla:<pw>@${LAN_IP}:5434/settla_transfer?sslmode=disable"
echo "     make migrate-up-transfer"
