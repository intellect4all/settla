#!/bin/bash
set -euo pipefail

echo "=== Settla Testnet Wallet Initialization ==="
echo ""

# Check if .env exists
if [ ! -f .env ]; then
    echo "ERROR: .env file not found. Run 'cp .env.example .env' first."
    exit 1
fi

# Check if wallet credentials already exist
if grep -q "^SETTLA_WALLET_ENCRYPTION_KEY=.\+" .env 2>/dev/null; then
    echo "Wallet credentials already exist in .env"
    echo "To regenerate, remove SETTLA_WALLET_ENCRYPTION_KEY and SETTLA_MASTER_SEED from .env"
    exit 0
fi

# Generate credentials
ENC_KEY=$(openssl rand -hex 32)
MASTER_SEED=$(openssl rand -hex 64)

# Create wallet storage directory
mkdir -p .settla/wallets

# Append to .env
cat >> .env << EOF

# ── Wallet Management (auto-generated $(date -u +%Y-%m-%dT%H:%M:%SZ)) ──
SETTLA_WALLET_ENCRYPTION_KEY=${ENC_KEY}
SETTLA_MASTER_SEED=${MASTER_SEED}
SETTLA_WALLET_STORAGE_PATH=.settla/wallets
EOF

echo "Wallet encryption key generated (32 bytes)"
echo "Master seed generated (64 bytes)"
echo "Storage directory created at .settla/wallets"
echo ""
echo "IMPORTANT: Back up your .env file securely."
echo "           Losing the master seed = losing all derived wallet addresses."
echo ""
echo "Next steps:"
echo "  1. Ensure SETTLA_PROVIDER_MODE=testnet in .env"
echo "  2. Add Alchemy RPC URLs to .env (optional, public defaults work)"
echo "  3. Start the server: make run  (or go run ./cmd/settla-server/...)"
echo "  4. Check logs for 'registered system wallet' lines with addresses"
echo "  5. Fund wallets using faucets: make fund-testnet-wallets"
