#!/usr/bin/env bash
set -euo pipefail

# demo-status.sh — Check health of all demo services and print dashboard URLs.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "============================================"
echo "  Settla Demo Status"
echo "============================================"
echo ""

# Check each service
check_service() {
  local name="$1"
  local url="$2"
  local display_url="$3"

  if curl -sf --max-time 3 "$url" >/dev/null 2>&1; then
    printf "  %-20s \033[32mUP\033[0m    %s\n" "$name" "$display_url"
  else
    printf "  %-20s \033[31mDOWN\033[0m  %s\n" "$name" "$display_url"
  fi
}

echo "  Service             Status  URL"
echo "  ─────────────────── ──────  ─────────────────────────────"

# Application services
check_service "Gateway"         "http://localhost:8080/health"      "http://localhost:8080"
check_service "settla-server"   "http://localhost:8081/health"      "http://localhost:8081"
check_service "Webhook"         "http://localhost:3001/health"      "http://localhost:3001"
check_service "Mock Provider"   "http://localhost:9095/health"      "http://localhost:9095/admin/config"

# Monitoring
check_service "Grafana"         "http://localhost:3000/api/health"  "http://localhost:3000"
check_service "Prometheus"      "http://localhost:9090/-/healthy"   "http://localhost:9090"
check_service "NATS"            "http://localhost:8222/healthz"     "http://localhost:8222"

# Infrastructure (basic port check)
check_service "Metabase"        "http://localhost:3003"             "http://localhost:3003"

echo ""

# Tenant count
TRANSFER_URL="${SETTLA_TRANSFER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable}"
TENANT_COUNT=$(psql "$TRANSFER_URL" -t -c "SELECT count(*) FROM tenants" 2>/dev/null | tr -d ' ' || echo "N/A")
echo "  Tenants in database: $TENANT_COUNT"

# Mock provider config
if curl -sf --max-time 3 "http://localhost:9095/health" >/dev/null 2>&1; then
  echo ""
  echo "  Mock Provider Config:"
  curl -s http://localhost:9095/admin/config 2>/dev/null | python3 -m json.tool 2>/dev/null | sed 's/^/    /' || \
  curl -s http://localhost:9095/admin/config 2>/dev/null | sed 's/^/    /'
fi

echo ""

# Docker resource usage
echo "  Container Resources:"
echo "  ─────────────────────────────────────────────────────"
docker stats --no-stream --format "  {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}" 2>/dev/null | head -15 || \
  echo "  (docker stats unavailable)"
echo ""
