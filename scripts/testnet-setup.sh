#!/usr/bin/env bash
# testnet-setup.sh — Initialize Settla testnet wallets and fund from faucets.
#
# This script:
#   1. Validates required environment variables
#   2. Creates the wallet storage directory
#   3. Generates system wallets for all chains (tron, ethereum, base, solana)
#   4. Generates tenant wallets for seed tenants (lemfi, fincra)
#   5. Funds wallets from automated faucets (tron, solana)
#   6. Prints manual faucet instructions (ethereum, base)
#
# Usage:
#   make testnet-setup      # reads .env automatically
#   bash scripts/testnet-setup.sh
#
# Required environment variables:
#   SETTLA_WALLET_ENCRYPTION_KEY  — 32-byte hex-encoded AES-256 key
#   SETTLA_MASTER_SEED            — hex-encoded BIP-39 master seed (64 bytes)
#
# Optional:
#   SETTLA_WALLET_STORAGE_PATH    — wallet storage dir (default: .settla/wallets)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}╔══════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     Settla Testnet Wallet Setup          ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════╝${NC}"
echo ""

# Load .env if it exists
if [ -f "$PROJECT_ROOT/.env" ]; then
    echo -e "${BLUE}Loading .env file...${NC}"
    set -a
    # shellcheck disable=SC1091
    source "$PROJECT_ROOT/.env"
    set +a
fi

# Validate required env vars
missing=0

if [ -z "${SETTLA_WALLET_ENCRYPTION_KEY:-}" ]; then
    echo -e "${RED}ERROR: SETTLA_WALLET_ENCRYPTION_KEY is not set${NC}"
    echo "  Generate one with: openssl rand -hex 32"
    missing=1
fi

if [ -z "${SETTLA_MASTER_SEED:-}" ]; then
    echo -e "${RED}ERROR: SETTLA_MASTER_SEED is not set${NC}"
    echo "  Generate one with: openssl rand -hex 64"
    missing=1
fi

if [ "$missing" -eq 1 ]; then
    echo ""
    echo -e "${YELLOW}Add these variables to your .env file and try again.${NC}"
    exit 1
fi

# Create storage directory
STORAGE_PATH="${SETTLA_WALLET_STORAGE_PATH:-.settla/wallets}"
echo -e "${BLUE}Wallet storage: ${STORAGE_PATH}${NC}"
mkdir -p "$STORAGE_PATH"

# Run the Go setup tool
echo ""
echo -e "${GREEN}Running wallet setup...${NC}"
echo ""

go run ./cmd/testnet-tools/ setup

echo ""
echo -e "${GREEN}Done.${NC}"
echo -e "${YELLOW}Next steps:${NC}"
echo "  1. Fund manual faucet wallets (Ethereum Sepolia, Base Sepolia) if needed"
echo "  2. Run 'make testnet-verify' to confirm balances"
echo "  3. Set SETTLA_PROVIDER_MODE=testnet in .env to use real blockchain"
