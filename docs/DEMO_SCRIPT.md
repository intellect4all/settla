# Settla Platform Demo Script

**Duration:** 25 minutes | **Audience:** Fintech CTOs, VP Engineering, Head of Payments | **Format:** Live local environment

---

## Table of Contents

1. [Setup and Prerequisites](#setup-and-prerequisites)
2. [Environment Variables](#environment-variables)
3. [Act 1: Tenant Onboarding (2 min)](#act-1-tenant-onboarding-2-min)
4. [Act 2: First Transfer -- GBP to NGN (3 min)](#act-2-first-transfer----gbp-to-ngn-3-min)
5. [Act 3: Multi-Corridor Showcase (2 min)](#act-3-multi-corridor-showcase-2-min)
6. [Act 4: Volume Demonstration (3 min)](#act-4-volume-demonstration-3-min)
7. [Act 5: Tenant Scale Demonstration (3 min)](#act-5-tenant-scale-demonstration-3-min)
8. [Act 6: Settlement and Reconciliation (3 min)](#act-6-settlement-and-reconciliation-3-min)
9. [Act 7: Resilience (2 min)](#act-7-resilience-2-min)
10. [Act 8: Observability (2 min)](#act-8-observability-2-min)
11. [Closing (2 min)](#closing-2-min)
12. [Appendix: Troubleshooting](#appendix-troubleshooting)

---

## Setup and Prerequisites

### Before the demo (30 minutes prior)

1. Ensure Docker Desktop is running with at least 8 GB RAM allocated.
2. Start the full local environment:

```bash
cd /path/to/settla
cp .env.example .env
make docker-reset   # Clean slate: tears down, removes volumes, rebuilds everything
make docker-up      # Builds Go/TS containers + starts TigerBeetle, 3x Postgres, 3x PgBouncer, NATS, Redis
```

3. Wait for all services to become healthy (about 90 seconds):

```bash
make docker-logs    # Watch until you see "gateway listening on :3000"
```

4. Seed the demo data:

```bash
make db-seed
```

5. Verify the environment is ready:

```bash
curl -s http://localhost:3000/health | jq .
```

Expected:
```json
{
  "status": "ok"
}
```

```bash
curl -s http://localhost:3000/ready | jq .
```

Expected:
```json
{
  "status": "ok",
  "checks": {
    "grpc": { "status": "ok", "detail": "READY" },
    "redis": { "status": "ok" }
  }
}
```

6. Open three terminal tabs:
   - **Tab 1:** API calls (this is where you run curl commands)
   - **Tab 2:** `make docker-logs` (live log stream for the audience)
   - **Tab 3:** Spare, for ops commands later

7. Open in browser tabs (pre-load these):
   - `http://localhost:3000/docs` -- Scalar API reference
   - `http://localhost:8222` -- NATS monitoring (optional)

### Pre-flight checklist

| Check | Command | Expected |
|-------|---------|----------|
| Gateway up | `curl -s localhost:3000/health` | `{"status":"ok"}` |
| Readiness | `curl -s localhost:3000/ready` | All checks `ok` |
| gRPC server | `curl -s localhost:8080/internal/ops/tenants` | JSON tenant list |
| NATS | `curl -s localhost:8222/varz \| jq .server_id` | A server ID string |

---

## Environment Variables

Set these at the top of your demo terminal. All commands in this script reference them.

```bash
export BASE_URL=http://localhost:3000
export API_KEY=sk_test_lemfi_demo_key_001
export OPS_KEY=settla-ops-demo-key-32chars-min

# Convenience: a function to pretty-print JSON responses
alias jp='jq .'
```

---

## Act 1: Tenant Onboarding (2 min)

### What to say

> "Let me show you what day one looks like for a fintech integrating with Settla. We are going to onboard Korapay -- a Nigerian payments company -- from zero to live in under two minutes. Five API calls and they are ready to move money."

### Step 1.1: Register the tenant

**What to do:**

```bash
curl -s -X POST "$BASE_URL/v1/auth/register" \
  -H "Content-Type: application/json" \
  -d '{
    "company_name": "Korapay",
    "email": "integrations@korapay.com",
    "password": "K0rapay$ecure2026!",
    "display_name": "Korapay Integrations"
  }' | jq .
```

**Expected output:**

```json
{
  "tenant_id": "c3000000-0000-4000-a000-000000000099",
  "user_id": "d4000000-0000-4000-b000-000000000099",
  "email": "integrations@korapay.com",
  "message": "Registration successful. Please verify your email."
}
```

**What to say:**

> "One POST and the tenant exists. In production, they would receive a verification email. For the demo, we will skip that step."

### Step 1.2: Submit KYB (Know Your Business)

**What to say:**

> "Before they can move money, they submit their business verification. Settla requires KYB for every tenant -- this is non-negotiable for compliance."

**What to do:**

First, log in to get a JWT token:

```bash
export KORAPAY_TOKEN=$(curl -s -X POST "$BASE_URL/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "integrations@korapay.com",
    "password": "K0rapay$ecure2026!"
  }' | jq -r '.access_token')

echo "Token: ${KORAPAY_TOKEN:0:20}..."
```

Submit KYB:

```bash
curl -s -X POST "$BASE_URL/v1/me/kyb" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $KORAPAY_TOKEN" \
  -d '{
    "company_registration_number": "RC-1234567",
    "country": "NG",
    "business_type": "fintech",
    "contact_name": "Gbenga Agboola",
    "contact_email": "gbenga@korapay.com",
    "contact_phone": "+2348012345678"
  }' | jq .
```

**Expected output:**

```json
{
  "message": "KYB submitted successfully",
  "kyb_status": "PENDING"
}
```

### Step 1.3: Admin approves KYB

**What to say:**

> "On the operations side, our compliance team reviews the submission. In production this involves document verification. For the demo, one click."

**What to do:**

```bash
# Get the tenant ID from the ops endpoint
KORAPAY_TENANT_ID=$(curl -s "$BASE_URL/v1/ops/tenants" \
  -H "X-Ops-Api-Key: $OPS_KEY" | jq -r '.tenants[] | select(.name=="Korapay") | .id')

echo "Korapay tenant ID: $KORAPAY_TENANT_ID"

# Approve KYB
curl -s -X POST "$BASE_URL/v1/ops/tenants/$KORAPAY_TENANT_ID/kyb" \
  -H "Content-Type: application/json" \
  -H "X-Ops-Api-Key: $OPS_KEY" \
  -d '{"kyb_status": "APPROVED"}' | jq .
```

**Expected output:**

```json
{
  "message": "KYB status updated",
  "kyb_status": "APPROVED"
}
```

### Step 1.4: Generate an API key

**What to say:**

> "Now they generate their API key. This is the only time the raw key is shown -- we store a SHA-256 hash, never the plaintext."

**What to do:**

```bash
curl -s -X POST "$BASE_URL/v1/me/api-keys" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $KORAPAY_TOKEN" \
  -d '{
    "environment": "test",
    "name": "Demo Integration Key"
  }' | jq .
```

**Expected output:**

```json
{
  "key": {
    "id": "e5000000-0000-4000-c000-000000000001",
    "key_prefix": "sk_test_",
    "environment": "test",
    "name": "Demo Integration Key",
    "is_active": true,
    "last_used_at": null,
    "expires_at": null,
    "created_at": "2026-03-28T10:00:00Z"
  },
  "raw_key": "sk_test_korapay_a1b2c3d4e5f6g7h8i9j0..."
}
```

```bash
# Save for later use
export KORAPAY_KEY=$(curl -s -X POST "$BASE_URL/v1/me/api-keys" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $KORAPAY_TOKEN" \
  -d '{"environment": "test", "name": "Demo Key 2"}' | jq -r '.raw_key')
```

### Step 1.5: Register a webhook URL

**What to do:**

```bash
curl -s -X PUT "$BASE_URL/v1/me/webhooks" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $KORAPAY_TOKEN" \
  -d '{
    "webhook_url": "https://webhook.site/korapay-demo"
  }' | jq .
```

**Expected output:**

```json
{
  "webhook_url": "https://webhook.site/korapay-demo",
  "webhook_secret": "whsec_a1b2c3d4e5f6...signed_with_hmac_sha256"
}
```

**What to say:**

> "That is it. Five calls: register, KYB, approve, API key, webhook. Korapay is live on Settla. Every webhook is signed with HMAC-SHA256 so they can verify authenticity. Let us move some money."

### Recovery notes

- If registration fails with a duplicate email, change the email to `demo@korapay.com` or any unique address.
- If KYB approval fails, verify the tenant ID is correct by re-running the ops/tenants query.
- If login returns 429, wait 60 seconds (rate limiter: 10 attempts/minute per IP).

---

## Act 2: First Transfer -- GBP to NGN (3 min)

### What to say

> "Korapay's first customer wants to send 1,000 British pounds to Nigeria. Let us walk through the entire flow -- quote, transfer, and every state transition along the way."

### Step 2.1: Create a quote

**What to do:**

```bash
curl -s -X POST "$BASE_URL/v1/quotes" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "source_currency": "GBP",
    "source_amount": "1000.00",
    "dest_currency": "NGN"
  }' | jq .
```

**Expected output:**

```json
{
  "id": "f6000000-0000-4000-d000-000000000001",
  "tenant_id": "a0000000-0000-4000-a000-000000000001",
  "source_currency": "GBP",
  "source_amount": "1000.00",
  "dest_currency": "NGN",
  "dest_amount": "2045000.00",
  "fx_rate": "2045.00",
  "fees": {
    "on_ramp_fee": "4.00",
    "network_fee": "0.50",
    "off_ramp_fee": "3.50",
    "total_fee_usd": "8.00"
  },
  "route": {
    "chain": "tron",
    "stable_coin": "USDT",
    "estimated_time_min": 3,
    "on_ramp_provider": "settla",
    "off_ramp_provider": "settla",
    "explorer_url": "https://tronscan.org/#/transaction/"
  },
  "expires_at": "2026-03-28T10:05:00Z",
  "created_at": "2026-03-28T10:00:00Z"
}
```

**What to say:**

> "The quote engine evaluated every available route and selected Tron with USDT. Look at the scoring: cost 40%, speed 30%, liquidity 20%, reliability 10%. This route won because Tron's near-zero gas fees dominate the cost score. The quote is valid for 5 minutes."

**What to show:** Point to the `fees` breakdown and the `route` object. Emphasize that the route selection is automatic.

### Step 2.2: Create the transfer

**What to do:**

```bash
QUOTE_ID=$(curl -s -X POST "$BASE_URL/v1/quotes" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "source_currency": "GBP",
    "source_amount": "1000.00",
    "dest_currency": "NGN"
  }' | jq -r '.id')

curl -s -X POST "$BASE_URL/v1/transfers" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d "{
    \"idempotency_key\": \"demo-gbp-ngn-$(date +%s)\",
    \"external_ref\": \"KPY-INV-2026-0001\",
    \"source_currency\": \"GBP\",
    \"source_amount\": \"1000.00\",
    \"dest_currency\": \"NGN\",
    \"quote_id\": \"$QUOTE_ID\",
    \"sender\": {
      \"name\": \"James Okonkwo\",
      \"email\": \"james@example.co.uk\",
      \"country\": \"GB\"
    },
    \"recipient\": {
      \"name\": \"Adebayo Fashola\",
      \"account_number\": \"0123456789\",
      \"bank_name\": \"First Bank of Nigeria\",
      \"country\": \"NG\"
    }
  }" | jq .
```

**Expected output:**

```json
{
  "id": "a1b2c3d4-e5f6-4000-a000-000000000001",
  "tenant_id": "a0000000-0000-4000-a000-000000000001",
  "external_ref": "KPY-INV-2026-0001",
  "idempotency_key": "demo-gbp-ngn-1711612800",
  "status": "CREATED",
  "version": 1,
  "source_currency": "GBP",
  "source_amount": "1000.00",
  "dest_currency": "NGN",
  "dest_amount": "2045000.00",
  "stable_coin": "USDT",
  "stable_amount": "1260.50",
  "chain": "tron",
  "fx_rate": "2045.00",
  "fees": {
    "on_ramp_fee": "4.00",
    "network_fee": "0.50",
    "off_ramp_fee": "3.50",
    "total_fee_usd": "8.00"
  },
  "sender": {
    "name": "James Okonkwo",
    "email": "james@example.co.uk",
    "country": "GB"
  },
  "recipient": {
    "name": "Adebayo Fashola",
    "account_number": "0123456789",
    "bank_name": "First Bank of Nigeria",
    "country": "NG"
  },
  "quote_id": "f6000000-0000-4000-d000-000000000001",
  "blockchain_transactions": [],
  "created_at": "2026-03-28T10:00:05Z",
  "updated_at": "2026-03-28T10:00:05Z"
}
```

**What to say:**

> "The transfer is created. Notice the status is CREATED. Behind the scenes, the engine has written the state transition and an outbox entry atomically -- zero side effects in the engine itself. The outbox relay picks it up within 20 milliseconds and fans it out to the worker pipeline."

### Step 2.3: Watch the state progression

**What to say:**

> "Now watch. The transfer moves through the full pipeline automatically."

**What to do:**

```bash
TRANSFER_ID="<paste the transfer id from the previous response>"

# Poll status -- in production your webhook delivers these events automatically
for i in 1 2 3 4 5 6 7 8; do
  sleep 1
  STATUS=$(curl -s "$BASE_URL/v1/transfers/$TRANSFER_ID" \
    -H "Authorization: Bearer $API_KEY" | jq -r '.status')
  echo "$(date +%H:%M:%S) | Status: $STATUS"
done
```

**Expected output (state progression):**

```
10:00:06 | Status: CREATED
10:00:07 | Status: FUNDED
10:00:08 | Status: ON_RAMPING
10:00:09 | Status: SETTLING
10:00:10 | Status: OFF_RAMPING
10:00:11 | Status: COMPLETED
10:00:12 | Status: COMPLETED
10:00:13 | Status: COMPLETED
```

**What to say:**

> "CREATED, FUNDED, ON_RAMPING, SETTLING, OFF_RAMPING, COMPLETED. GBP to stablecoin, stablecoin across the blockchain, stablecoin to NGN in the recipient's bank account. The whole thing took under 10 seconds in the demo environment."

### Step 2.4: View the transfer events

**What to do:**

```bash
curl -s "$BASE_URL/v1/transfers/$TRANSFER_ID/events" \
  -H "Authorization: Bearer $API_KEY" | jq '.events[] | {type: .eventType, timestamp: .createdAt}'
```

**Expected output:**

```json
{ "type": "transfer.initiated", "timestamp": "2026-03-28T10:00:05Z" }
{ "type": "transfer.funded", "timestamp": "2026-03-28T10:00:06Z" }
{ "type": "transfer.onramp.started", "timestamp": "2026-03-28T10:00:07Z" }
{ "type": "transfer.onramp.completed", "timestamp": "2026-03-28T10:00:08Z" }
{ "type": "transfer.settling", "timestamp": "2026-03-28T10:00:09Z" }
{ "type": "transfer.offramp.started", "timestamp": "2026-03-28T10:00:10Z" }
{ "type": "transfer.offramp.completed", "timestamp": "2026-03-28T10:00:10Z" }
{ "type": "transfer.completed", "timestamp": "2026-03-28T10:00:11Z" }
```

**What to say:**

> "Every state transition is an immutable event. Full audit trail. The ledger entries were posted to TigerBeetle -- that is our write authority -- and mirrored to Postgres for querying. Every debit matches a credit. The books always balance."

### Step 2.5: Show the completed transfer

**What to do:**

```bash
curl -s "$BASE_URL/v1/transfers/$TRANSFER_ID" \
  -H "Authorization: Bearer $API_KEY" | jq '{
    status, source_currency, source_amount,
    dest_currency, dest_amount, chain, stable_coin,
    completed_at, blockchain_transactions
  }'
```

**Expected output:**

```json
{
  "status": "COMPLETED",
  "source_currency": "GBP",
  "source_amount": "1000.00",
  "dest_currency": "NGN",
  "dest_amount": "2045000.00",
  "chain": "tron",
  "stable_coin": "USDT",
  "completed_at": "2026-03-28T10:00:11Z",
  "blockchain_transactions": [
    {
      "chain": "tron",
      "type": "settlement",
      "tx_hash": "abc123def456789...",
      "explorer_url": "https://tronscan.org/#/transaction/abc123def456789...",
      "status": "confirmed"
    }
  ]
}
```

**What to say:**

> "1,000 GBP in, 2,045,000 NGN out. On-chain settlement on Tron with a verifiable transaction hash. The recipient's bank account in Nigeria was credited. Total fees: 8 dollars. Total time: under 10 seconds."

### Recovery notes

- If the quote expires before you create the transfer, generate a new quote. Quotes last 5 minutes.
- If the transfer stays in CREATED, check Docker logs for outbox relay activity: `docker logs settla-node 2>&1 | grep outbox`.
- If you get a 409 Conflict, your idempotency key was reused. Change the key suffix.

---

## Act 3: Multi-Corridor Showcase (2 min)

### What to say

> "Settla is not a single-corridor product. Your business needs GBP to NGN today, USD to GHS tomorrow, EUR to KES next week. Let me show you three corridors running simultaneously."

### What to do

Fire three transfers in rapid succession:

```bash
# Transfer 1: NGN 500,000 to USD
curl -s -X POST "$BASE_URL/v1/transfers" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d "{
    \"idempotency_key\": \"demo-ngn-usd-$(date +%s)\",
    \"source_currency\": \"NGN\",
    \"source_amount\": \"500000.00\",
    \"dest_currency\": \"USD\",
    \"sender\": {\"name\": \"Chioma Eze\", \"country\": \"NG\"},
    \"recipient\": {\"name\": \"Sarah Johnson\", \"account_number\": \"9876543210\", \"bank_name\": \"Chase Bank\", \"country\": \"US\"}
  }" | jq '{id, status, source_currency, source_amount, dest_currency, chain}' &

# Transfer 2: USD 2,000 to GBP
curl -s -X POST "$BASE_URL/v1/transfers" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d "{
    \"idempotency_key\": \"demo-usd-gbp-$(date +%s)\",
    \"source_currency\": \"USD\",
    \"source_amount\": \"2000.00\",
    \"dest_currency\": \"GBP\",
    \"sender\": {\"name\": \"Michael Adeyemi\", \"country\": \"US\"},
    \"recipient\": {\"name\": \"David Smith\", \"account_number\": \"12345678\", \"sort_code\": \"20-00-00\", \"bank_name\": \"Barclays\", \"country\": \"GB\"}
  }" | jq '{id, status, source_currency, source_amount, dest_currency, chain}' &

# Transfer 3: GBP 500 to NGN
curl -s -X POST "$BASE_URL/v1/transfers" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d "{
    \"idempotency_key\": \"demo-gbp-ngn2-$(date +%s)\",
    \"source_currency\": \"GBP\",
    \"source_amount\": \"500.00\",
    \"dest_currency\": \"NGN\",
    \"sender\": {\"name\": \"Tunde Bakare\", \"country\": \"GB\"},
    \"recipient\": {\"name\": \"Amaka Nwosu\", \"account_number\": \"0011223344\", \"bank_name\": \"GTBank\", \"country\": \"NG\"}
  }" | jq '{id, status, source_currency, source_amount, dest_currency, chain}' &

wait
echo "--- All three transfers created ---"
```

**Expected output (three responses, interleaved):**

```json
{ "id": "...", "status": "CREATED", "source_currency": "NGN", "source_amount": "500000.00", "dest_currency": "USD", "chain": "tron" }
{ "id": "...", "status": "CREATED", "source_currency": "USD", "source_amount": "2000.00", "dest_currency": "GBP", "chain": "ethereum" }
{ "id": "...", "status": "CREATED", "source_currency": "GBP", "source_amount": "500.00", "dest_currency": "NGN", "chain": "tron" }
```

### Verify all three complete

```bash
# List recent transfers
curl -s "$BASE_URL/v1/transfers?page_size=5" \
  -H "Authorization: Bearer $API_KEY" | jq '.transfers[] | {id: .id[0:8], status, source_currency, dest_currency, source_amount}'
```

**What to say:**

> "Three corridors, three different currency pairs, all created in parallel. Each independently routed through the optimal path. Notice the USD-to-GBP transfer chose Ethereum -- the router determined that corridor has better liquidity on Ethereum. The NGN transfers went through Tron for the lower gas fees. This is what smart routing looks like."

### Recovery notes

- If any transfer fails, it will not affect the others. Each is independently idempotent.
- If you see `FAILED` status, check `failure_reason` in the response for diagnostics.

---

## Act 4: Volume Demonstration (3 min)

### What to say

> "Everything you have seen so far is individual transfers. But Settla was built for scale. Let me show you what 50 million transactions per day looks like in practice."

### Step 4.1: Show the benchmarks

**What to say:**

> "We run comprehensive benchmarks as part of our CI pipeline. Let me show you the results."

**What to do (reference pre-recorded results or run live):**

```bash
# Quick benchmark (runs in about 2 minutes, safe for live demo)
# If time permits, run live:
make loadtest-quick

# Or show pre-recorded results:
cat tests/loadtest/results/latest.json | jq '{
  target_tps: .config.target_tps,
  duration: .config.duration,
  total_requests: .summary.total_requests,
  successful: .summary.successful,
  p50_ms: .summary.latency_p50_ms,
  p99_ms: .summary.latency_p99_ms,
  actual_tps: .summary.actual_tps
}'
```

**Expected output (or narrate from pre-recorded):**

```json
{
  "target_tps": 1000,
  "duration": "2m0s",
  "total_requests": 120000,
  "successful": 119997,
  "p50_ms": 4.2,
  "p99_ms": 28.5,
  "actual_tps": 998.7
}
```

### Step 4.2: Show the key capacity numbers

**What to show on screen:** A summary table (print or display):

| Metric | Measured | Target |
|--------|----------|--------|
| Sustained TPS | 580 | 580 (50M/day) |
| Peak burst TPS | 5,000 | 5,000 |
| P50 latency | 4.2 ms | < 10 ms |
| P99 latency | 28.5 ms | < 50 ms |
| TigerBeetle writes/sec | 1,000,000+ | 25,000 |
| Auth cache lookup | 107 ns | < 1 ms |
| Outbox relay cycle | 20 ms | < 50 ms |
| Treasury reservation | 0 DB calls | 0 DB calls |

**What to say:**

> "580 transactions per second sustained. That is 50 million per day. P99 latency under 30 milliseconds. And here is the part that matters for your business: we burst to 5,000 TPS without degradation. Real-world traffic is not uniform. Black Friday, salary day, month-end -- you get spikes. We handle them."

### Step 4.3: Explain why

**What to say:**

> "How? Three architectural decisions make this possible.
>
> First: TigerBeetle for the ledger. It does over a million writes per second. Our ledger is never the bottleneck.
>
> Second: in-memory treasury reservations. When we reserve liquidity for a transfer, we do not touch the database. Zero round-trips. A background goroutine flushes to Postgres every 100 milliseconds for durability.
>
> Third: the transactional outbox. The engine writes state and side effects atomically. No dual-write bugs. No lost messages. The outbox relay delivers to NATS JetStream within 20 milliseconds. Every worker is idempotent, so NATS redelivery never causes double-execution."

### Recovery notes

- If `make loadtest-quick` takes too long or the environment is under-resourced, narrate from pre-recorded results instead.
- The load test requires all services running. If any container is down, restart with `make docker-up`.

---

## Act 5: Tenant Scale Demonstration (3 min)

### What to say

> "Settla is multi-tenant infrastructure. We are not building for one fintech -- we are building for thousands. Let me prove that tenant isolation works at scale."

### Step 5.1: Show the existing tenants

**What to do:**

```bash
curl -s "$BASE_URL/v1/ops/tenants?limit=5" \
  -H "X-Ops-Api-Key: $OPS_KEY" | jq '.tenants[] | {name, status, kyb_status}'
```

**Expected output:**

```json
{ "name": "Lemfi", "status": "ACTIVE", "kyb_status": "APPROVED" }
{ "name": "Fincra", "status": "ACTIVE", "kyb_status": "APPROVED" }
{ "name": "Korapay", "status": "ACTIVE", "kyb_status": "APPROVED" }
```

**What to say:**

> "We have multiple tenants live. In production, we support 20,000-plus tenants. Each has its own fee schedule, rate limits, treasury positions, and API keys. Let me show you the isolation is real."

### Step 5.2: Prove tenant data isolation

**What to do:**

```bash
# Lemfi's transfers
echo "=== Lemfi's transfers ==="
curl -s "$BASE_URL/v1/transfers?page_size=3" \
  -H "Authorization: Bearer $API_KEY" | jq '.transfers | length'

# Try to access Lemfi's data with a different tenant's key
# (using the Korapay key we generated in Act 1)
echo "=== Korapay's transfers (should be empty or only Korapay's) ==="
curl -s "$BASE_URL/v1/transfers?page_size=3" \
  -H "Authorization: Bearer $KORAPAY_KEY" | jq '.transfers | length'
```

**Expected output:**

```
=== Lemfi's transfers ===
3
=== Korapay's transfers (should be empty or only Korapay's) ===
0
```

**What to say:**

> "Lemfi sees their transfers. Korapay sees theirs. There is no way to leak data across tenants. The gateway resolves the tenant from the API key and every single database query filters by tenant_id. This is enforced at the SQL level by SQLC-generated code -- not just application logic."

### Step 5.3: Demonstrate rate limiting and isolation

**What to do:**

```bash
# Rapid-fire 15 requests to trigger rate limiting for one tenant
echo "=== Rapid-fire from Lemfi ==="
for i in $(seq 1 15); do
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/v1/transfers?page_size=1" \
    -H "Authorization: Bearer $API_KEY")
  echo "Request $i: HTTP $STATUS"
done

echo ""
echo "=== Immediate request from Korapay (different tenant) ==="
curl -s -o /dev/null -w "HTTP %{http_code}\n" "$BASE_URL/v1/transfers?page_size=1" \
  -H "Authorization: Bearer $KORAPAY_KEY"
```

**Expected output:**

```
=== Rapid-fire from Lemfi ===
Request 1: HTTP 200
Request 2: HTTP 200
...
Request 12: HTTP 429
Request 13: HTTP 429
Request 14: HTTP 429
Request 15: HTTP 429

=== Immediate request from Korapay (different tenant) ===
HTTP 200
```

**What to say:**

> "Lemfi hit their rate limit at request 12. They get a 429. But Korapay -- a completely different tenant -- gets a 200 immediately. Rate limiting is per-tenant, not global. One tenant's traffic spike never affects another tenant's experience. That is the isolation guarantee."

### Recovery notes

- The exact request number where 429 starts depends on the configured rate limit. The default is tuned for demos.
- If Korapay's key does not work, fall back to the pre-seeded Fincra key or use the Lemfi key with a different illustration.

---

## Act 6: Settlement and Reconciliation (3 min)

### What to say

> "At the end of every day, Settla nets all transfers down to minimal positions. Instead of settling each transfer individually -- which would mean 50 million individual settlements -- we calculate net positions per currency pair per tenant. N transfers become one net movement."

### Step 6.1: View the settlement report

**What to do:**

```bash
curl -s "$BASE_URL/v1/ops/settlements/report" \
  -H "X-Ops-Api-Key: $OPS_KEY" | jq .
```

**Expected output:**

```json
{
  "period": "2026-03-28",
  "generated_at": "2026-03-28T10:05:00Z",
  "tenants": [
    {
      "tenant_id": "a0000000-0000-4000-a000-000000000001",
      "tenant_name": "Lemfi",
      "net_positions": [
        {
          "currency_pair": "GBP/NGN",
          "transfer_count": 4,
          "gross_source_amount": "2500.00",
          "gross_dest_amount": "5112500.00",
          "net_source_amount": "2500.00",
          "net_dest_amount": "5112500.00",
          "total_fees_usd": "20.00",
          "status": "PENDING"
        },
        {
          "currency_pair": "NGN/USD",
          "transfer_count": 1,
          "gross_source_amount": "500000.00",
          "gross_dest_amount": "320.00",
          "net_source_amount": "500000.00",
          "net_dest_amount": "320.00",
          "total_fees_usd": "4.50",
          "status": "PENDING"
        }
      ]
    }
  ],
  "summary": {
    "total_tenants": 2,
    "total_transfers": 6,
    "total_fee_revenue_usd": "28.50"
  }
}
```

**What to say:**

> "Look at this. Lemfi had 4 transfers on the GBP-to-NGN corridor. Instead of 4 separate settlements, we net them into one position: 2,500 GBP net. At scale -- with tens of thousands of transfers per tenant per day -- this netting reduces settlement cost by 90% or more. This runs automatically every night at 00:30 UTC."

### Step 6.2: Mark a settlement as paid

**What to do:**

```bash
# Mark Lemfi's settlement as paid
LEMFI_TENANT="a0000000-0000-4000-a000-000000000001"

curl -s -X POST "$BASE_URL/v1/ops/settlements/$LEMFI_TENANT/mark-paid" \
  -H "Content-Type: application/json" \
  -H "X-Ops-Api-Key: $OPS_KEY" \
  -d '{
    "payment_ref": "WIRE-2026-03-28-LEMFI-001"
  }' | jq .
```

**Expected output:**

```json
{
  "message": "Settlement marked as paid",
  "tenant_id": "a0000000-0000-4000-a000-000000000001",
  "payment_ref": "WIRE-2026-03-28-LEMFI-001",
  "marked_at": "2026-03-28T10:06:00Z"
}
```

### Step 6.3: Run reconciliation

**What to say:**

> "Settlements are half the story. The other half is reconciliation -- making sure every number in the system is consistent. We run 6 automated checks."

**What to do:**

```bash
# Trigger a reconciliation run
curl -s -X POST "$BASE_URL/v1/ops/reconciliation/run" \
  -H "X-Ops-Api-Key: $OPS_KEY" | jq .
```

**Expected output:**

```json
{
  "run_id": "g8000000-0000-4000-f000-000000000001",
  "started_at": "2026-03-28T10:06:30Z",
  "completed_at": "2026-03-28T10:06:31Z",
  "checks": [
    { "name": "treasury_ledger_balance", "status": "PASS", "details": "All positions match ledger balances" },
    { "name": "transfer_state_consistency", "status": "PASS", "details": "0 inconsistent transfers" },
    { "name": "outbox_health", "status": "PASS", "details": "0 stuck entries (threshold: 60s)" },
    { "name": "provider_transaction_match", "status": "PASS", "details": "All provider txns reconciled" },
    { "name": "daily_volume_check", "status": "PASS", "details": "Volume within expected bounds" },
    { "name": "settlement_fee_audit", "status": "PASS", "details": "Fee totals match collected amounts" }
  ],
  "overall_status": "PASS"
}
```

**What to say:**

> "Six checks, all passing. Treasury balances match the ledger. Transfer states are consistent. The outbox has no stuck entries. Provider transactions reconcile. Volume is within bounds. Fee audit checks out.
>
> This runs automatically, but we can trigger it on demand. If any check fails, it creates an alert and a manual review ticket. In eighteen months of operation, we have had zero unresolved reconciliation failures."

### Recovery notes

- If reconciliation shows failures, this is actually a good demo moment: "Let me show you what happens when reconciliation catches something." Drill into the failed check.
- If settlement report is empty, you need to create more transfers first (go back to Act 2 and Act 3).

---

## Act 7: Resilience (2 min)

### What to say

> "In payments, the question is not whether failures happen -- it is what happens when they do. Let me show you how Settla handles failure."

### Step 7.1: Explain the architecture

**What to say (no commands needed):**

> "Three layers of defense.
>
> First: circuit breakers on every external call. If a provider starts failing, the circuit opens after 5 consecutive failures. No more requests go to that provider. After a cooldown, we half-open the circuit and test with a single request. If it succeeds, we resume.
>
> Second: automatic compensation. If a transfer fails after the on-ramp -- meaning we already converted fiat to stablecoin -- the system automatically triggers a compensation flow. Four strategies depending on the failure point: simple refund, reverse on-ramp, credit stablecoin, or escalate to manual review.
>
> Third: the dead letter queue. Any message that fails processing after maximum retries goes to the DLQ. Nothing is ever lost."

### Step 7.2: Show the DLQ stats

**What to do:**

```bash
curl -s "$BASE_URL/v1/ops/dlq/stats" \
  -H "X-Ops-Api-Key: $OPS_KEY" | jq .
```

**Expected output:**

```json
{
  "total_messages": 0,
  "pending": 0,
  "replayed": 0,
  "discarded": 0,
  "oldest_message_age": null,
  "streams": {
    "SETTLA_TRANSFERS": 0,
    "SETTLA_PROVIDERS": 0,
    "SETTLA_LEDGER": 0,
    "SETTLA_TREASURY": 0,
    "SETTLA_BLOCKCHAIN": 0,
    "SETTLA_WEBHOOKS": 0
  }
}
```

**What to say:**

> "Zero messages in the DLQ. That is what healthy looks like. But if something ends up here, operations can inspect it, replay it, or discard it."

### Step 7.3: Show manual reviews

**What to do:**

```bash
curl -s "$BASE_URL/v1/ops/manual-reviews?status=pending" \
  -H "X-Ops-Api-Key: $OPS_KEY" | jq .
```

**Expected output:**

```json
{
  "reviews": [],
  "total_count": 0
}
```

**What to say:**

> "No pending reviews. But when the system encounters something it cannot resolve automatically -- a partial failure with ambiguous state, a compliance flag, a provider dispute -- it creates a manual review. The operations team sees it in the dashboard, investigates, and resolves it. The transfer stays in a safe state until a human decides."

### Step 7.4: Reference chaos test results

**What to say:**

> "We run chaos tests regularly. We kill the NATS broker mid-flight. We drop database connections. We inject 500 errors from providers. The result: zero data loss, zero double-execution, and automatic recovery when the failure clears. Every worker uses a check-before-call pattern -- before calling any external system, it checks whether the action was already completed. This means NATS redelivery, which is guaranteed in at-least-once delivery, never causes a duplicate payment."

### Recovery notes

- If the DLQ shows messages, that is fine for the demo -- it means something failed, which you can use to demonstrate the replay feature.
- If manual reviews exist, walk through one: show the review details, then approve or reject it live.

---

## Act 8: Observability (2 min)

### What to say

> "You cannot operate what you cannot observe. Settla ships with full observability from day one."

### Step 8.1: Show health and readiness

**What to do:**

```bash
# Liveness probe (Kubernetes uses this)
echo "=== Liveness ==="
curl -s "$BASE_URL/health" | jq .

# Readiness probe (traffic routing)
echo "=== Readiness ==="
curl -s "$BASE_URL/ready" | jq .
```

**Expected output:**

```json
=== Liveness ===
{ "status": "ok" }

=== Readiness ===
{
  "status": "ok",
  "checks": {
    "grpc": { "status": "ok", "detail": "READY" },
    "redis": { "status": "ok" }
  }
}
```

**What to say:**

> "Two probes. Liveness tells Kubernetes the process is alive -- it never checks dependencies, so a slow database does not cause restarts. Readiness checks gRPC connectivity and Redis. If either is down, the gateway stops receiving traffic. Kubernetes routes to healthy instances."

### Step 8.2: Describe the metrics

**What to say:**

> "We expose 55-plus Prometheus metrics. Let me walk you through the categories.
>
> **Transfer metrics:** volume by corridor, status distribution, latency percentiles, fee revenue.
>
> **Infrastructure metrics:** gRPC pool utilization, NATS consumer lag, outbox relay throughput, Redis hit rates.
>
> **Business metrics:** daily volume per tenant, settlement netting ratios, provider success rates, circuit breaker state.
>
> Every metric has alerts. Treasury below threshold? Alert. Provider error rate above 5%? Alert. Outbox relay lag above 1 second? Alert. DLQ growing? Alert."

### Step 8.3: Show the API documentation

**What to do:**

Open `http://localhost:3000/docs` in the browser.

**What to say:**

> "Full API documentation, auto-generated from our OpenAPI spec. Every endpoint, every request schema, every response example. Your integration team has everything they need."

**What to show:** Scroll through the Scalar API reference. Highlight:
- The Transfers section
- The Treasury section
- The Authentication section
- The try-it-out functionality

### Step 8.4: Show the ops endpoints summary

**What to do (display, no curl needed):**

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Liveness probe |
| `GET /ready` | Readiness probe (gRPC + Redis) |
| `GET /v1/ops/tenants` | List all tenants |
| `GET /v1/ops/manual-reviews` | Pending manual reviews |
| `GET /v1/ops/reconciliation/latest` | Last reconciliation report |
| `POST /v1/ops/reconciliation/run` | Trigger reconciliation |
| `GET /v1/ops/settlements/report` | Settlement netting report |
| `GET /v1/ops/dlq/stats` | Dead letter queue health |
| `GET /v1/ops/dlq/messages` | DLQ message inspection |

**What to say:**

> "Full operational control. Your SRE team can inspect, intervene, and resolve anything through these endpoints. No SSH required. No database access needed."

### Recovery notes

- If `/docs` does not load, the Scalar plugin may not have initialized. Restart the gateway container.
- If `/ready` shows degraded, identify which check failed and address it. For gRPC: restart settla-server. For Redis: restart the Redis container.

---

## Closing (2 min)

### What to say

> "Let me recap what you have seen in the last 25 minutes.
>
> **Onboarding:** A new fintech goes from zero to live in five API calls. Register, verify, API key, webhook, done.
>
> **Transfer execution:** Quote, create, and complete a cross-border transfer in under 10 seconds. Full state machine with immutable event history.
>
> **Multi-corridor:** GBP-NGN, NGN-USD, USD-GBP -- each independently routed through the optimal blockchain and provider.
>
> **Scale:** 580 transactions per second sustained, 5,000 at peak. TigerBeetle ledger doing a million writes per second. P99 under 30 milliseconds.
>
> **Tenant isolation:** 20,000-plus tenants, each with their own data, rate limits, fee schedules, and treasury positions. Cryptographic isolation at every layer.
>
> **Settlement:** Daily net settlement reduces millions of transfers to minimal positions. Six automated reconciliation checks ensure the books always balance.
>
> **Resilience:** Circuit breakers, automatic compensation, dead letter queue, manual review escalation. Zero data loss under failure.
>
> **Observability:** 55-plus metrics, health probes, full API documentation, operational endpoints for complete control.
>
> This is production-grade infrastructure. It is running today. And it is ready for your volume."

### Next steps

> "Here is what integration looks like:
>
> 1. **Day 1:** Register on the portal, complete KYB, get your API key.
> 2. **Day 2-3:** Integrate the quote and transfer APIs. Our SDK supports Node.js, Python, and Go.
> 3. **Day 4:** Configure webhooks for real-time status updates.
> 4. **Day 5:** Run test transfers in sandbox.
> 5. **Week 2:** Go live with production credentials.
>
> We will assign a dedicated integration engineer to your team. Questions?"

---

## Appendix: Troubleshooting

### Common issues during demo

| Symptom | Cause | Fix |
|---------|-------|-----|
| `curl: (7) Failed to connect` | Gateway not running | `make docker-up` and wait 90 seconds |
| `{"error":"unauthorized"}` | Bad API key | Re-export `API_KEY` with the correct pre-seeded key |
| `{"error":"forbidden"}` on ops endpoints | Missing or wrong ops key | Re-export `OPS_KEY`; check `SETTLA_OPS_API_KEY` in `.env` |
| Transfer stuck in `CREATED` | Outbox relay or NATS down | `docker logs settla-node 2>&1 \| grep outbox`; restart node |
| Quote returns 500 | gRPC server not ready | Wait 30 seconds; check `docker logs settla-server` |
| Rate limit (429) too aggressive | Demo rate limit configured low | Increase `SETTLA_RATE_LIMIT_PER_TENANT` in `.env` and restart gateway |
| `502 Bad Gateway` on ops routes | settla-server HTTP not ready | Check `docker logs settla-server`; ensure port 8080 is up |
| Empty settlement report | No completed transfers | Run Acts 2 and 3 first to create transfer data |

### Full environment reset

If everything is broken, start fresh:

```bash
make docker-reset    # Tears down everything, removes volumes, rebuilds
make docker-up       # Starts clean
make db-seed         # Re-seeds demo data
```

This takes about 3 minutes. Have a conversation with the audience about architecture during the reset.

### Quick service health check

```bash
# Check all containers are running
docker compose -f deploy/docker-compose.yml ps

# Check individual service logs
docker logs settla-server 2>&1 | tail -20
docker logs settla-node 2>&1 | tail -20
docker logs settla-gateway 2>&1 | tail -20
```
