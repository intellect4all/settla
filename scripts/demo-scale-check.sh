#!/usr/bin/env bash
set -euo pipefail

# demo-scale-check.sh — Verify tenant-scale readiness after seeding.
#
# Checks tenant count, treasury positions, container memory, NATS streams,
# and Grafana dashboards. Prints a pass/fail checklist.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

TRANSFER_URL="${SETTLA_TRANSFER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable}"
TREASURY_URL="${SETTLA_TREASURY_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5435/settla_treasury?sslmode=disable}"
LEDGER_URL="${SETTLA_LEDGER_DB_MIGRATE_URL:-postgres://settla:settla@localhost:5433/settla_ledger?sslmode=disable}"
GRAFANA_PASSWORD="${GRAFANA_ADMIN_PASSWORD:-settla-dev-local}"

PASS_COUNT=0
FAIL_COUNT=0

check() {
  local name="$1"
  local result="$2"
  local expected="$3"
  local detail="$4"

  if [ "$result" = "pass" ]; then
    printf "  \033[32m✓\033[0m  %-35s %s\n" "$name" "$detail"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    printf "  \033[31m✗\033[0m  %-35s %s\n" "$name" "$detail"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

echo "============================================"
echo "  Settla Scale Readiness Check"
echo "============================================"
echo ""

# 1. Tenant count
TENANT_COUNT=$(psql "$TRANSFER_URL" -t -c "SELECT count(*) FROM tenants" 2>/dev/null | tr -d ' ' || echo "0")
if [ "$TENANT_COUNT" -gt 0 ] 2>/dev/null; then
  check "Tenants provisioned" "pass" ">0" "$TENANT_COUNT tenants"
else
  check "Tenants provisioned" "fail" ">0" "count: $TENANT_COUNT"
fi

# 2. Treasury positions
POSITION_COUNT=$(psql "$TREASURY_URL" -t -c "SELECT count(*) FROM positions" 2>/dev/null | tr -d ' ' || echo "0")
if [ "$POSITION_COUNT" -gt 0 ] 2>/dev/null; then
  check "Treasury positions" "pass" ">0" "$POSITION_COUNT positions"
else
  check "Treasury positions" "fail" ">0" "count: $POSITION_COUNT"
fi

# 3. Ledger accounts
ACCOUNT_COUNT=$(psql "$LEDGER_URL" -t -c "SELECT count(*) FROM accounts" 2>/dev/null | tr -d ' ' || echo "0")
if [ "$ACCOUNT_COUNT" -gt 0 ] 2>/dev/null; then
  check "Ledger accounts" "pass" ">0" "$ACCOUNT_COUNT accounts"
else
  check "Ledger accounts" "fail" ">0" "count: $ACCOUNT_COUNT"
fi

# 4. API keys
API_KEY_COUNT=$(psql "$TRANSFER_URL" -t -c "SELECT count(*) FROM api_keys" 2>/dev/null | tr -d ' ' || echo "0")
if [ "$API_KEY_COUNT" -gt 0 ] 2>/dev/null; then
  check "API keys" "pass" ">0" "$API_KEY_COUNT keys"
else
  check "API keys" "fail" ">0" "count: $API_KEY_COUNT"
fi

# 4b. Virtual accounts
VA_COUNT=$(psql "$TRANSFER_URL" -t -c "SELECT count(*) FROM virtual_account_pool" 2>/dev/null | tr -d ' ' || echo "0")
if [ "$VA_COUNT" -gt 0 ] 2>/dev/null; then
  check "Virtual accounts" "pass" ">0" "$VA_COUNT accounts"
else
  check "Virtual accounts" "fail" ">0" "count: $VA_COUNT"
fi

# 5. settla-server health
if curl -sf --max-time 3 "http://localhost:8081/health" >/dev/null 2>&1; then
  check "settla-server" "pass" "healthy" "http://localhost:8081"
else
  check "settla-server" "fail" "healthy" "not responding"
fi

# 6. Gateway health
if curl -sf --max-time 3 "http://localhost:8080/health" >/dev/null 2>&1; then
  check "Gateway" "pass" "healthy" "http://localhost:8080"
else
  check "Gateway" "fail" "healthy" "not responding"
fi

# 7. NATS streams
NATS_STREAMS=$(curl -sf --max-time 3 "http://localhost:8222/jsz" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('streams',0))" 2>/dev/null || echo "0")
if [ "$NATS_STREAMS" -gt 0 ] 2>/dev/null; then
  check "NATS JetStream streams" "pass" ">0" "$NATS_STREAMS streams"
else
  check "NATS JetStream streams" "fail" ">0" "count: $NATS_STREAMS"
fi

# 8. Grafana dashboards
DASHBOARD_COUNT=$(curl -sf --max-time 3 "http://admin:${GRAFANA_PASSWORD}@localhost:3000/api/search?type=dash-db" 2>/dev/null | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
if [ "$DASHBOARD_COUNT" -gt 0 ] 2>/dev/null; then
  check "Grafana dashboards" "pass" ">0" "$DASHBOARD_COUNT dashboards"
else
  check "Grafana dashboards" "fail" ">0" "count: $DASHBOARD_COUNT"
fi

# 9. Mock provider
if curl -sf --max-time 3 "http://localhost:9095/health" >/dev/null 2>&1; then
  check "Mock provider" "pass" "healthy" "http://localhost:9095"
else
  check "Mock provider" "fail" "healthy" "not responding (may not be needed)"
fi

echo ""

# 10. Container memory usage
echo "  Container Memory Usage:"
echo "  ──────────────────────────────────────────"
docker stats --no-stream --format "    {{.Name}}\t{{.MemUsage}}\t{{.MemPerc}}" 2>/dev/null | head -15 || \
  echo "    (docker stats unavailable)"
echo ""

# Summary
TOTAL=$((PASS_COUNT + FAIL_COUNT))
echo "  Result: $PASS_COUNT/$TOTAL checks passed"
if [ "$FAIL_COUNT" -gt 0 ]; then
  echo "  WARNING: $FAIL_COUNT check(s) failed. Review above for details."
  exit 1
else
  echo "  All checks passed. Ready for demo."
fi
echo ""
