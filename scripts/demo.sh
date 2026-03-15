#!/usr/bin/env bash
set -uo pipefail

# ---------------------------------------------------------------------------
# Settla Demo — runs 5 integration-test scenarios with formatted output
# Usage: bash scripts/demo.sh   (or: make demo)
# ---------------------------------------------------------------------------

# --- Colour helpers (graceful fallback for non-TTY) -----------------------
if [ -t 1 ] && command -v tput &>/dev/null && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
  GREEN=$(tput setaf 2)
  RED=$(tput setaf 1)
  CYAN=$(tput setaf 6)
  BOLD=$(tput bold)
  DIM=$(tput dim)
  RESET=$(tput sgr0)
else
  GREEN=""
  RED=""
  CYAN=""
  BOLD=""
  DIM=""
  RESET=""
fi

# --- Banner ---------------------------------------------------------------
echo ""
echo "${BOLD}${CYAN}╔══════════════════════════════════════════════════════════════╗${RESET}"
echo "${BOLD}${CYAN}║                                                              ║${RESET}"
echo "${BOLD}${CYAN}║                    S E T T L A   D E M O                     ║${RESET}"
echo "${BOLD}${CYAN}║          B2B Stablecoin Settlement Infrastructure            ║${RESET}"
echo "${BOLD}${CYAN}║                                                              ║${RESET}"
echo "${BOLD}${CYAN}╚══════════════════════════════════════════════════════════════╝${RESET}"
echo ""
echo "${DIM}Running integration tests with in-memory stores (no infra needed)${RESET}"
echo ""

# --- Scenario definitions -------------------------------------------------
# Each entry is "TestFunctionName|Human-readable description"
SCENARIOS=(
  # Core corridor flows
  "TestLemfiGBPtoNGN|GBP -> NGN Corridor (Lemfi) — Primary corridor, full pipeline"
  "TestFincraNGNtoGBP|NGN -> GBP Corridor (Fincra) — Reverse corridor, different fees"
  "TestTenantIsolation|Tenant Isolation — Cross-tenant data isolation proof"
  "TestTreasuryReservationConsistency|Burst Concurrency — 100 concurrent transfers, no over-reservation"
  "TestPerTenantFees|Per-Tenant Fees — Different fee schedules per fintech"
  # Extended scenarios (outbox architecture)
  "TestTreasuryPositionTracking|Consumer USDC Payout — Full on-ramp → settlement → off-ramp pipeline"
  "TestLedgerTBWritePath|Enterprise Auto-Settlement — TigerBeetle write authority + ledger sync"
  "TestFailedTransferAndRefund|Failure Recovery — Provider failure → refund initiation + ledger reversal"
  "TestConcurrentMultiTenant|Operational Resilience — Multi-tenant concurrency, per-tenant ordering"
)

PASS_COUNT=0
FAIL_COUNT=0
TOTAL=${#SCENARIOS[@]}
RESULTS=()

# --- Run each scenario ----------------------------------------------------
for i in "${!SCENARIOS[@]}"; do
  IFS='|' read -r TEST_NAME DESCRIPTION <<< "${SCENARIOS[$i]}"
  NUM=$((i + 1))

  echo "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo "${BOLD}  Scenario ${NUM}/${TOTAL}: ${DESCRIPTION}${RESET}"
  echo "${DIM}  go test -tags=integration -v -run=${TEST_NAME} ./tests/integration/...${RESET}"
  echo "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo ""

  START_TIME=$(date +%s%N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1e9))')

  # Run test — do NOT let set -e abort on failure; capture exit code
  set +e
  go test -tags=integration -v -run="^${TEST_NAME}$" ./tests/integration/... 2>&1
  EXIT_CODE=$?
  set -e

  END_TIME=$(date +%s%N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1e9))')
  ELAPSED_MS=$(( (END_TIME - START_TIME) / 1000000 ))

  echo ""
  if [ "$EXIT_CODE" -eq 0 ]; then
    echo "  ${GREEN}${BOLD}PASS${RESET}  ${DESCRIPTION}  ${DIM}(${ELAPSED_MS}ms)${RESET}"
    PASS_COUNT=$((PASS_COUNT + 1))
    RESULTS+=("${GREEN}PASS${RESET}")
  else
    echo "  ${RED}${BOLD}FAIL${RESET}  ${DESCRIPTION}  ${DIM}(${ELAPSED_MS}ms)${RESET}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    RESULTS+=("${RED}FAIL${RESET}")
  fi
  echo ""
done

# --- Summary --------------------------------------------------------------
echo "${BOLD}${CYAN}══════════════════════════════════════════════════════════════${RESET}"
echo "${BOLD}  DEMO SUMMARY${RESET}"
echo "${BOLD}${CYAN}══════════════════════════════════════════════════════════════${RESET}"
echo ""

for i in "${!SCENARIOS[@]}"; do
  IFS='|' read -r _ DESCRIPTION <<< "${SCENARIOS[$i]}"
  NUM=$((i + 1))
  echo "  ${RESULTS[$i]}  ${NUM}. ${DESCRIPTION}"
done

echo ""
echo "  ${GREEN}${BOLD}${PASS_COUNT} passed${RESET}, ${RED}${BOLD}${FAIL_COUNT} failed${RESET} out of ${TOTAL} scenarios"
echo ""

if [ "$FAIL_COUNT" -gt 0 ]; then
  echo "  ${RED}${BOLD}Some scenarios failed.${RESET}"
  echo ""
  exit 1
else
  echo "  ${GREEN}${BOLD}All scenarios passed!${RESET}"
  echo ""
  exit 0
fi
