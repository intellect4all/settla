# Settla Integration Guide

> Get your first cross-border transfer working in 5 minutes.

## Table of Contents

- [Quick Start](#quick-start)
- [Authentication](#authentication)
- [Webhook Setup and Verification](#webhook-setup-and-verification)
- [Transfer Lifecycle](#transfer-lifecycle)
- [Crypto Deposit Flow](#crypto-deposit-flow)
- [Bank Deposit Flow](#bank-deposit-flow)
- [Error Handling Best Practices](#error-handling-best-practices)
- [Sandbox / Testing Environment](#sandbox--testing-environment)
- [SDKs](#sdks)
- [FAQ](#faq)

---

## Quick Start

### 1. Get Your API Key

After onboarding, you'll receive an API key in the format `sk_live_<base64>` (production) or `sk_test_<base64>` (sandbox). The raw key is shown **once** -- store it securely.

### 2. Get a Quote

```bash
curl -X POST https://api.settla.io/v1/quotes \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: quote-$(uuidgen)" \
  -d '{
    "source_currency": "GBP",
    "dest_currency": "NGN",
    "source_amount": "1000.00"
  }'
```

**Response:**
```json
{
  "id": "q_01234567-89ab-cdef-0123-456789abcdef",
  "tenant_id": "a0000000-0000-0000-0000-000000000001",
  "source_currency": "GBP",
  "source_amount": "1000.00",
  "dest_currency": "NGN",
  "dest_amount": "1850000.00",
  "stable_amount": "1260.50",
  "fx_rate": "1850.00",
  "fees": {
    "on_ramp_fee": "4.00",
    "network_fee": "0.50",
    "off_ramp_fee": "3.50",
    "total_fee_usd": "8.00"
  },
  "route": {
    "chain": "tron",
    "stable_coin": "USDT",
    "estimated_time_min": 5,
    "on_ramp_provider": "settla",
    "off_ramp_provider": "settla",
    "explorer_url": "https://tronscan.org/#/transaction/..."
  },
  "expires_at": "2026-03-29T12:00:30Z",
  "created_at": "2026-03-29T12:00:00Z"
}
```

Quotes are valid for 30 seconds. The `stable_amount` is the intermediate stablecoin value used for on-chain settlement.

### 3. Create a Transfer

```bash
curl -X POST https://api.settla.io/v1/transfers \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: transfer-$(uuidgen)" \
  -d '{
    "quote_id": "q_01234567-89ab-cdef-0123-456789abcdef",
    "external_ref": "your-internal-reference-123",
    "recipient": {
      "name": "Recipient Name",
      "bank_code": "058",
      "account_number": "0123456789"
    }
  }'
```

**Response:**
```json
{
  "id": "t_fedcba98-7654-3210-fedc-ba9876543210",
  "tenant_id": "a0000000-0000-0000-0000-000000000001",
  "status": "CREATED",
  "version": 1,
  "quote_id": "q_01234567-89ab-cdef-0123-456789abcdef",
  "idempotency_key": "transfer-...",
  "external_ref": "your-internal-reference-123",
  "source_currency": "GBP",
  "source_amount": "1000.00",
  "dest_currency": "NGN",
  "dest_amount": "1850000.00",
  "stable_coin": "USDT",
  "stable_amount": "1260.50",
  "chain": "tron",
  "fx_rate": "1850.00",
  "fees": {
    "on_ramp_fee": "4.00",
    "network_fee": "0.50",
    "off_ramp_fee": "3.50",
    "total_fee_usd": "8.00"
  },
  "recipient": {
    "name": "Recipient Name",
    "account_number": "0123456789",
    "bank_name": "GTBank",
    "country": "NG"
  },
  "blockchain_transactions": [],
  "created_at": "2026-03-29T12:00:05Z",
  "updated_at": "2026-03-29T12:00:05Z"
}
```

### 4. Receive Webhook Notifications

As the transfer progresses, you'll receive webhooks at your configured URL:

```json
{
  "event": "transfer.completed",
  "transfer_id": "t_fedcba98-7654-3210-fedc-ba9876543210",
  "status": "COMPLETED",
  "timestamp": "2026-03-29T12:05:30Z"
}
```

That's it. Settla handles routing, on-ramping, blockchain settlement, off-ramping, ledger accounting, and treasury management automatically.

---

## Authentication

### API Keys

All API requests require a Bearer token:

```
Authorization: Bearer sk_live_<your_api_key>
```

| Prefix | Environment |
|--------|------------|
| `sk_live_` | Production |
| `sk_test_` | Sandbox (no real money movement) |

API keys are hashed with HMAC-SHA256 server-side. The raw key cannot be retrieved after initial issuance -- if lost, generate a new one from the tenant portal.

### Rate Limits

| Endpoint | Default Limit |
|----------|--------------|
| `POST /v1/quotes` | 1,000 req/min |
| `POST /v1/transfers` | 500 req/min |
| `GET /v1/treasury/*` | 2,000 req/min |

Rate limit headers are included in every response:

```
X-RateLimit-Limit: 500
X-RateLimit-Remaining: 498
X-RateLimit-Reset: 1711711260
```

When rate limited, you'll receive a `429 Too Many Requests` response. Back off and retry.

### Idempotency

All mutation endpoints (`POST`, `PUT`, `DELETE`) accept an `Idempotency-Key` header:

```
Idempotency-Key: your-unique-key-here
```

- Keys are scoped per-tenant (two tenants can use the same key without conflict)
- TTL: 24 hours (keys can be reused after expiry)
- Duplicate requests within the TTL return the cached response without re-execution
- **Always use idempotency keys** -- they protect against network retries and double-submissions

---

## Webhook Setup and Verification

### Configuring Your Webhook URL

Set your webhook URL and secret in the tenant portal or via the API. Settla will send POST requests to this URL for all transfer lifecycle events.

### Webhook Payload

```json
{
  "event": "transfer.funded",
  "transfer_id": "t_fedcba98-7654-3210-fedc-ba9876543210",
  "tenant_id": "a0000000-0000-0000-0000-000000000001",
  "status": "FUNDED",
  "data": {
    "source_amount": "1000.00",
    "source_currency": "GBP",
    "dest_amount": "1850000.00",
    "dest_currency": "NGN"
  },
  "timestamp": "2026-03-29T12:00:10Z"
}
```

### Signature Verification

Every webhook includes two headers for verification:

```
X-Settla-Signature: <hmac_hex>
X-Settla-Timestamp: <unix_seconds>
```

**Verification algorithm:**

```python
import hmac
import hashlib
import time

def verify_webhook(payload_body, signature, timestamp, secret):
    # 1. Check timestamp freshness (reject if > 5 minutes old)
    if abs(time.time() - int(timestamp)) > 300:
        return False

    # 2. Compute expected signature
    message = f"{timestamp}.{payload_body}"
    expected = hmac.new(
        secret.encode(),
        message.encode(),
        hashlib.sha256
    ).hexdigest()

    # 3. Compare (constant-time)
    return hmac.compare_digest(signature, expected)
```

```javascript
const crypto = require("crypto");

function verifyWebhook(body, signature, timestamp, secret) {
  // 1. Check timestamp freshness
  if (Math.abs(Date.now() / 1000 - parseInt(timestamp)) > 300) {
    return false;
  }

  // 2. Compute expected signature
  const message = `${timestamp}.${body}`;
  const expected = crypto
    .createHmac("sha256", secret)
    .update(message)
    .digest("hex");

  // 3. Compare (constant-time)
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(expected)
  );
}
```

### Webhook Events

| Event | Description |
|-------|-------------|
| `transfer.created` | Transfer submitted and accepted |
| `transfer.funded` | Treasury funds reserved |
| `transfer.on_ramping` | Fiat-to-stablecoin conversion started |
| `transfer.settling` | On-chain settlement in progress |
| `transfer.off_ramping` | Stablecoin-to-fiat conversion started |
| `transfer.completed` | Transfer completed successfully (terminal) |
| `transfer.failed` | Transfer failed (terminal unless refund initiated) |
| `transfer.refunding` | Refund in progress |
| `transfer.refunded` | Refund completed (terminal) |
| `deposit.detected` | On-chain payment detected (unconfirmed) |
| `deposit.confirmed` | On-chain payment confirmed |
| `deposit.credited` | Tenant ledger credited |
| `deposit.settled` | Crypto converted to fiat |
| `deposit.held` | Crypto held without conversion |

### Retry Policy

Failed webhook deliveries are retried with exponential backoff. After all retries are exhausted, the event is sent to a dead letter queue. You can retrieve missed events via the API.

**Your endpoint should:**
- Return 2xx within 10 seconds
- Be idempotent (you may receive the same event multiple times)
- Verify the signature before processing

---

## Transfer Lifecycle

### State Diagram

```
CREATED ──► FUNDED ──► ON_RAMPING ──► SETTLING ──► OFF_RAMPING ──► COMPLETED
  │           │           │              │              │
  ▼           ▼           ▼              ▼              ▼
FAILED    REFUNDING ◄── FAILED ◄───── FAILED ◄────── FAILED
              │
              ▼
          REFUNDED
```

### States Explained

| State | Description | Typical Duration |
|-------|-------------|-----------------|
| `CREATED` | Transfer accepted, pending treasury reservation | <1 second |
| `FUNDED` | Treasury funds reserved, pending on-ramp | <1 second |
| `ON_RAMPING` | Converting source fiat to stablecoin | 10-60 seconds |
| `SETTLING` | Sending stablecoin on-chain to destination | 30 seconds - 5 minutes (chain-dependent) |
| `OFF_RAMPING` | Converting stablecoin to destination fiat | 10-60 seconds |
| `COMPLETED` | Transfer completed successfully | Terminal |
| `FAILED` | Transfer failed (see `failure_reason`) | Terminal (unless refund initiated) |
| `REFUNDING` | Compensation/refund in progress | 10-60 seconds |
| `REFUNDED` | Refund completed | Terminal |

### Polling for Status (Alternative to Webhooks)

```bash
curl https://api.settla.io/v1/transfers/t_fedcba98-7654-3210-fedc-ba9876543210 \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY"
```

### Listing Transfers

```bash
curl "https://api.settla.io/v1/transfers?status=COMPLETED&limit=50" \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY"
```

### Cancelling a Transfer

Transfers can be cancelled while in `CREATED` or `FUNDED` state:

```bash
curl -X POST https://api.settla.io/v1/transfers/t_.../cancel \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY" \
  -H "Idempotency-Key: cancel-$(uuidgen)"
```

---

## Crypto Deposit Flow

Collect crypto payments from your users via on-chain stablecoin transfers.

### 1. Create a Deposit Session

```bash
curl -X POST https://api.settla.io/v1/deposits \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: deposit-$(uuidgen)" \
  -d '{
    "chain": "tron",
    "token": "USDT",
    "expected_amount": "500.00",
    "settlement_preference": "AUTO_CONVERT"
  }'
```

**Response includes a blockchain address for the depositor to send funds to.**

### 2. Deposit Lifecycle

```
PENDING_PAYMENT ──► DETECTED ──► CONFIRMED ──► CREDITING ──► CREDITED
       │                                                        │
       ▼                                                        ├──► SETTLING ──► SETTLED
    EXPIRED                                                     │
    CANCELLED                                                   └──► HELD
```

| State | Description |
|-------|-------------|
| `PENDING_PAYMENT` | Waiting for on-chain payment to the assigned address |
| `DETECTED` | Transaction found on-chain (unconfirmed) |
| `CONFIRMED` | Required block confirmations reached (19 for Tron, 12 for Ethereum) |
| `CREDITING` | Crediting tenant's ledger account |
| `CREDITED` | Ledger credited successfully |
| `SETTLING` | Converting crypto to fiat (AUTO_CONVERT preference) |
| `SETTLED` | Conversion complete (terminal) |
| `HELD` | Crypto held without conversion (HOLD preference, terminal) |

### Settlement Preferences

| Preference | Behavior |
|-----------|----------|
| `AUTO_CONVERT` | Automatically convert to fiat after credit |
| `HOLD` | Keep as stablecoin balance |

---

## Bank Deposit Flow

Collect fiat payments via virtual bank accounts.

### 1. Create a Bank Deposit Session

```bash
curl -X POST https://api.settla.io/v1/bank-deposits \
  -H "Authorization: Bearer sk_test_YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: bank-deposit-$(uuidgen)" \
  -d '{
    "currency": "GBP",
    "expected_amount": "5000.00",
    "settlement_preference": "AUTO_CONVERT"
  }'
```

**Response includes virtual bank account details for the depositor.**

### 2. Bank Deposit Lifecycle

```
PENDING_PAYMENT ──► PAYMENT_RECEIVED ──► CREDITING ──► CREDITED
       │                   │                              │
       ▼                   ├──► UNDERPAID                 ├──► SETTLING ──► SETTLED
    EXPIRED                └──► OVERPAID                  └──► HELD
    CANCELLED
```

Additional states `UNDERPAID` and `OVERPAID` handle payment amount mismatches based on your configured mismatch policy (`ACCEPT` or `REJECT`).

---

## Error Handling Best Practices

### Error Response Format

```json
{
  "error": {
    "code": "INSUFFICIENT_FUNDS",
    "message": "Insufficient treasury balance for GBP position",
    "retriable": false
  }
}
```

### Retryable vs Non-Retryable Errors

**Retryable (retry with exponential backoff):**

| Code | Description |
|------|-------------|
| `PROVIDER_ERROR` | Upstream provider temporary failure |
| `PROVIDER_UNAVAILABLE` | Provider is down |
| `NETWORK_ERROR` | Transient network issue |
| `RATE_LIMIT_EXCEEDED` | Too many requests |
| `OPTIMISTIC_LOCK` | Concurrent modification (retry immediately) |

**Non-retryable (do not retry, fix the root cause):**

| Code | Description |
|------|-------------|
| `INSUFFICIENT_FUNDS` | Treasury balance too low |
| `TENANT_SUSPENDED` | Your account is suspended |
| `QUOTA_EXCEEDED` | Daily or per-transfer limit exceeded |
| `INVALID_TRANSITION` | Transfer is not in the expected state |
| `IDEMPOTENCY_CONFLICT` | Same idempotency key with different parameters |
| `QUOTE_EXPIRED` | Quote TTL exceeded (get a new quote) |
| `CORRIDOR_DISABLED` | Currency pair not currently available |

### Recommended Retry Strategy

```python
import time
import random

def retry_with_backoff(fn, max_retries=5):
    for attempt in range(max_retries):
        try:
            return fn()
        except RetryableError:
            if attempt == max_retries - 1:
                raise
            delay = min(2 ** attempt + random.uniform(0, 1), 30)
            time.sleep(delay)
```

### Best Practices

1. **Always use idempotency keys.** This protects against accidental double-submissions during retries.
2. **Check `retriable` field** before retrying. Non-retryable errors will never succeed on retry.
3. **Implement webhook verification.** Always verify HMAC signatures before processing webhooks.
4. **Handle terminal states.** `COMPLETED`, `REFUNDED`, `SETTLED`, and `HELD` are final -- no further state changes will occur.
5. **Store our transfer ID.** Map your `external_ref` to Settla's `transfer_id` for reconciliation.
6. **Monitor webhook delivery.** If you stop receiving webhooks, check your endpoint health and contact support.

---

## Sandbox / Testing Environment

### Sandbox Access

Use `sk_test_` prefixed API keys to access the sandbox. The sandbox simulates the full transfer lifecycle without moving real money or executing real blockchain transactions.

### Sandbox Behavior

| Feature | Sandbox | Production |
|---------|---------|-----------|
| API endpoints | Same as production | Same |
| Transfers | Simulated (auto-progress through states) | Real money movement |
| Blockchain | Simulated confirmations | Real on-chain transactions |
| Webhooks | Delivered to your configured URL | Same |
| Rate limits | Same as production | Same |
| Settlement | Simulated T+3 netting | Real settlement |

### Sandbox Base URL

Sandbox uses a dedicated base URL:

```
https://sandbox.settla.io/v1/
```

All sandbox requests use `sk_test_` API keys. The API shape is identical to production.

### Sandbox Webhook Testing

Webhook events in sandbox are identical to production. Use tools like [webhook.site](https://webhook.site) for initial testing, then switch to your actual endpoint. Sandbox transfers auto-progress through the full state machine, delivering webhooks at each transition.

---

## SDKs

### REST API (All Languages)

The REST API is the primary interface. Any HTTP client works:

```bash
# Production
https://api.settla.io/v1/

# Sandbox
https://sandbox.settla.io/v1/
```

### OpenAPI Specification

A full OpenAPI 3.1 specification is available at `/docs` on the gateway, or as a static file in `docs-site/openapi.json`. Import it into Postman, Insomnia, or any API client.

### Generated Typed Clients

Settla does not publish pre-built SDK packages. Instead, use [OpenAPI Generator](https://openapi-generator.tech/) to generate a typed client in your language of choice:

```bash
# TypeScript
openapi-generator-cli generate -i https://api.settla.io/openapi.json -g typescript-fetch -o ./settla-client

# Python
openapi-generator-cli generate -i https://api.settla.io/openapi.json -g python -o ./settla-client

# Go, Java, C#, Ruby, etc. — same approach
```

This gives you typed request/response models and method signatures derived directly from the live API schema.

---

## FAQ

### How long does a transfer take?

End-to-end, a typical transfer completes in 2-10 minutes depending on the corridor and blockchain congestion. The breakdown:
- On-ramp (fiat to stablecoin): 10-60 seconds
- Blockchain settlement: 30 seconds - 5 minutes (Tron is fastest at ~3 seconds/block)
- Off-ramp (stablecoin to fiat): 10-60 seconds

### What happens if a transfer fails midway?

Settla automatically initiates compensation. Depending on the failure point, this may be a simple refund (treasury release), a reverse on-ramp, or a stablecoin credit. You'll receive a `transfer.refunding` webhook followed by `transfer.refunded`. Failed transfers that cannot be automatically compensated are escalated to manual review.

### Can I cancel a transfer?

Yes, if it's in `CREATED` or `FUNDED` state. Once on-ramping begins, cancellation is not possible -- the system will complete or compensate automatically.

### How does settlement netting work?

Daily at 00:30 UTC, Settla calculates net positions per currency for each tenant. Instead of settling N individual transfers, one net amount per currency is settled. Settlement is due T+3. This reduces operational costs and simplifies reconciliation.

### What blockchains are supported?

Ethereum, Tron, Base, Polygon, Arbitrum, and Solana. The smart router automatically selects the optimal chain based on cost, speed, liquidity, and reliability. You don't need to specify a chain -- the router handles it.

### How is tenant data isolated?

Every API call, database query, ledger entry, treasury position, and event is scoped to your tenant ID. There is no API or database path that can access another tenant's data. This is enforced at compile time (SQLC-generated queries) and runtime (API gateway auth plugin).

### What's the maximum transfer amount?

Configurable per tenant via `PerTransferLimit` and `DailyLimitUSD`. Contact your account manager to adjust limits.

### Do I need blockchain expertise to integrate?

No. Settla abstracts all blockchain complexity. You interact with a standard REST API using fiat amounts. Blockchain selection, gas fees, confirmations, and on-chain monitoring are handled internally.

### How do I handle idempotency key conflicts?

If you send the same `Idempotency-Key` with different request parameters, you'll receive a `409 IDEMPOTENCY_CONFLICT` error. Use unique keys per unique request. UUIDs or `{entity}-{action}-{your-id}` patterns work well.

### What monitoring should I set up?

- **Webhook delivery:** Alert if webhook endpoint returns non-2xx for >5 minutes
- **Transfer completion time:** Alert if transfers take >15 minutes
- **Failed transfer rate:** Alert if failure rate exceeds your baseline
- **Rate limit proximity:** Alert when approaching 80% of rate limit
