#!/usr/bin/env bash
# Programmatic Tyk setup — creates API keys for seed tenants
set -euo pipefail

TYK_URL="${TYK_URL:-http://localhost:8080}"
TYK_SECRET="${TYK_SECRET:-settla-tyk-secret}"

echo "Waiting for Tyk gateway..."
until curl -sf "${TYK_URL}/hello" > /dev/null 2>&1; do sleep 2; done
echo "Tyk gateway is ready"

# Create API key for Lemfi (standard policy)
echo "Creating Lemfi API key..."
curl -sf -X POST "${TYK_URL}/tyk/keys/create" \
  -H "x-tyk-authorization: ${TYK_SECRET}" \
  -H "Content-Type: application/json" \
  -d '{
    "alias": "lemfi",
    "apply_policies": ["settla-standard"],
    "expires": 0,
    "tags": ["tenant:lemfi"]
  }' | jq .

# Create API key for Fincra (premium policy)
echo "Creating Fincra API key..."
curl -sf -X POST "${TYK_URL}/tyk/keys/create" \
  -H "x-tyk-authorization: ${TYK_SECRET}" \
  -H "Content-Type: application/json" \
  -d '{
    "alias": "fincra",
    "apply_policies": ["settla-premium"],
    "expires": 0,
    "tags": ["tenant:fincra"]
  }' | jq .

echo "Tyk setup complete"
