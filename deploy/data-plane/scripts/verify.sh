#!/usr/bin/env bash
# ============================================================================
# Data Plane Verification
# ============================================================================
# Verifies all data plane services are reachable from the current machine.
# Run from any OptiPlex node or your workstation.
#
# Usage:
#   ./verify.sh <mac1-ip> <mac2-ip>
#
# Example:
#   ./verify.sh 192.168.1.100 192.168.1.101
# ============================================================================

set -uo pipefail

MAC1_IP="${1:-}"
MAC2_IP="${2:-}"

if [ -z "$MAC1_IP" ] || [ -z "$MAC2_IP" ]; then
  echo "Usage: $0 <mac1-ip> <mac2-ip>"
  echo "Example: $0 192.168.1.100 192.168.1.101"
  exit 1
fi

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

FAILURES=0

check() {
  local label="$1"
  local host="$2"
  local port="$3"
  if nc -z -w3 "$host" "$port" 2>/dev/null; then
    echo -e "  ${GREEN}[OK]${NC}   ${label} ${host}:${port}"
  else
    echo -e "  ${RED}[FAIL]${NC} ${label} ${host}:${port}"
    FAILURES=$((FAILURES + 1))
  fi
}

echo "=== Data Plane Connectivity Check ==="
echo ""
echo "MacBook 1 (${MAC1_IP}):"
check "Transfer DB " "$MAC1_IP" 5434
check "TigerBeetle " "$MAC1_IP" 3001

echo ""
echo "MacBook 2 (${MAC2_IP}):"
check "Ledger DB   " "$MAC2_IP" 5433
check "Treasury DB " "$MAC2_IP" 5435

echo ""
if [ "$FAILURES" -eq 0 ]; then
  echo -e "${GREEN}All data plane services reachable.${NC}"
  exit 0
else
  echo -e "${RED}${FAILURES} service(s) unreachable.${NC}"
  echo ""
  echo "Troubleshooting:"
  echo "  1. Ensure Docker Desktop is running on both MacBooks"
  echo "  2. Ensure docker compose stacks are up on both MacBooks"
  echo "  3. Check macOS firewall: System Settings → Network → Firewall"
  echo "  4. Verify IPs are correct: ifconfig | grep inet"
  echo "  5. Ensure both machines are on the same subnet"
  exit 1
fi
