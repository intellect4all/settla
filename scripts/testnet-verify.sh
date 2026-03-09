#!/usr/bin/env bash
# testnet-verify.sh — Verify Settla testnet setup: RPC connectivity, wallets, balances.
#
# Usage:
#   make testnet-verify
#   bash scripts/testnet-verify.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}╔══════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     Settla Testnet Verification          ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════╝${NC}"
echo ""

# Load .env if it exists
if [ -f "$PROJECT_ROOT/.env" ]; then
    set -a
    # shellcheck disable=SC1091
    source "$PROJECT_ROOT/.env"
    set +a
fi

# Check required env vars
if [ -z "${SETTLA_WALLET_ENCRYPTION_KEY:-}" ]; then
    echo -e "${RED}ERROR: SETTLA_WALLET_ENCRYPTION_KEY is not set${NC}"
    echo "  Run 'make testnet-setup' first."
    exit 1
fi

# Run verification
go run ./cmd/testnet-tools/ verify

echo ""
echo -e "${GREEN}Verification complete.${NC}"
