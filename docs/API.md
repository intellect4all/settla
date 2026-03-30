# Settla API Reference

Settla is B2B stablecoin settlement infrastructure for fintechs. This document is a standalone, comprehensive reference for the Settla REST API.

---

## Base URLs

| Environment | URL                          |
|-------------|------------------------------|
| Production  | `https://api.settla.io`      |
| Sandbox     | `https://sandbox.settla.io`  |
| Local       | `http://localhost:3000`       |

---

## Authentication

The API supports three authentication methods.

### 1. API Key (Bearer Token)

Used by fintech integrations. Pass the API key in the `Authorization` header.

```
Authorization: Bearer sk_live_abc123...
```

API keys are prefixed with `sk_live_` (production) or `sk_test_` (sandbox). The gateway hashes the key with SHA-256 and resolves the tenant from a three-tier cache (local LRU 30s, Redis 5min, DB).

### 2. JWT (Portal Token)

Used by the tenant portal. Obtain a JWT via `POST /v1/auth/login`, then pass it as a Bearer token.

```
Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
```

JWTs contain `tenant_id`, `user_id`, and `role` claims. Refresh tokens are used to obtain new access tokens without re-authenticating.

### 3. Ops Key (Internal)

Used by the ops dashboard for internal administration endpoints. Pass the key in a custom header.

```
X-Ops-Api-Key: <ops-api-key>
```

Only endpoints under `/v1/ops/*` accept this authentication method.

---

## Common Patterns

### Monetary Amounts

All monetary amounts are represented as **strings** with decimal precision. Never as floating-point numbers. Example: `"1500.50"`, `"0.01"`, `"10000000"`.

### Identifiers

All entity IDs are **UUIDs** in standard format: `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`.

### Timestamps

All timestamps are **ISO 8601 UTC** strings: `"2025-01-15T14:30:00.000Z"`.

### Pagination

Two pagination styles are used depending on the endpoint:

**Cursor-based** (transfers, ledger):

| Parameter    | Type   | Default | Description                  |
|--------------|--------|---------|------------------------------|
| `page_size`  | integer| 20      | Number of items per page     |
| `page_token` | string | -       | Opaque cursor from previous response |

**Offset-based** (deposits, treasury, payment links):

| Parameter | Type    | Default | Description                   |
|-----------|---------|---------|-------------------------------|
| `limit`   | integer | 20      | Number of items (1-100)       |
| `offset`  | integer | 0       | Number of items to skip       |

### Idempotency

All mutation endpoints that create resources accept an `idempotency_key`. Duplicate requests with the same key and tenant return the cached response. Idempotency keys are scoped per-tenant: `UNIQUE(tenant_id, idempotency_key)`. Cache TTL is 2 hours (configurable).

### Error Response Format

All errors follow a consistent structure:

```json
{
  "error": "ERROR_TYPE",
  "code": "MACHINE_READABLE_CODE",
  "message": "Human-readable description of what went wrong",
  "request_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

### Request IDs

Every request is assigned a unique `request_id` (UUID). It appears in error responses and can be used for support inquiries.

---

## Quotes

### POST /v1/quotes -- Create Quote

Get a real-time FX quote for a cross-border transfer. Quotes are stateless price lookups cached for 30 seconds per corridor and amount bucket.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field            | Type   | Required | Description                                      | Constraints            |
|------------------|--------|----------|--------------------------------------------------|------------------------|
| `source_currency`| string | Yes      | ISO currency code for the source                 | 3-5 uppercase letters  |
| `source_amount`  | string | Yes      | Amount to send                                   | Decimal string         |
| `dest_currency`  | string | Yes      | ISO currency code for the destination            | 3-5 uppercase letters  |
| `dest_country`   | string | No       | ISO country code for destination routing         | 2 uppercase letters    |

**Response (201):**

| Field            | Type   | Description                                     |
|------------------|--------|-------------------------------------------------|
| `id`             | string | Quote UUID                                      |
| `tenant_id`      | string | Tenant UUID                                     |
| `source_currency`| string | Source currency code                             |
| `source_amount`  | string | Source amount                                    |
| `dest_currency`  | string | Destination currency code                        |
| `dest_amount`    | string | Estimated destination amount after fees          |
| `fx_rate`        | string | Applied FX rate                                  |
| `fees`           | object | Fee breakdown (see FeeBreakdown type)            |
| `route`          | object | Selected route details                           |
| `expires_at`     | string | ISO 8601 UTC expiration time                     |
| `created_at`     | string | ISO 8601 UTC creation time                       |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/quotes \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "source_currency": "NGN",
    "source_amount": "500000",
    "dest_currency": "GBP",
    "dest_country": "GB"
  }'
```

```json
{
  "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "tenant_id": "a0000000-0000-0000-0000-000000000001",
  "source_currency": "NGN",
  "source_amount": "500000",
  "dest_currency": "GBP",
  "dest_amount": "245.50",
  "fx_rate": "2036.66",
  "fees": {
    "on_ramp_fee": "2.00",
    "network_fee": "0.50",
    "off_ramp_fee": "1.75",
    "total_fee_usd": "4.25"
  },
  "route": {
    "chain": "tron",
    "stable_coin": "USDT",
    "estimated_time_min": 15,
    "on_ramp_provider": "settla",
    "off_ramp_provider": "settla",
    "explorer_url": "https://tronscan.org"
  },
  "expires_at": "2025-01-15T14:35:00.000Z",
  "created_at": "2025-01-15T14:30:00.000Z"
}
```

**Error Codes:**

| HTTP | Code                | Description                      |
|------|---------------------|----------------------------------|
| 400  | `CORRIDOR_DISABLED` | Currency pair not supported      |
| 400  | `AMOUNT_TOO_LOW`    | Below minimum transfer amount    |
| 400  | `AMOUNT_TOO_HIGH`   | Exceeds maximum transfer amount  |
| 401  | `UNAUTHORIZED`      | Invalid or missing API key       |
| 429  | `RATE_LIMIT_EXCEEDED`| Too many requests               |

---

### GET /v1/quotes/{quoteId} -- Get Quote

Retrieve a previously created quote by ID.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description        |
|-----------|--------|----------|--------------------|
| `quoteId` | string | Yes      | Quote UUID         |

**Response (200):** Same schema as Create Quote response.

**Example:**

```bash
curl https://api.settla.io/v1/quotes/a1b2c3d4-e5f6-7890-abcd-ef1234567890 \
  -H "Authorization: Bearer sk_live_abc123"
```

**Error Codes:**

| HTTP | Code             | Description               |
|------|------------------|---------------------------|
| 404  | `NOT_FOUND`      | Quote not found           |
| 404  | `QUOTE_EXPIRED`  | Quote has expired         |

---

## Transfers

### POST /v1/transfers -- Create Transfer

Create a cross-border stablecoin transfer. This is the primary settlement endpoint. The transfer moves through the state machine: CREATED -> FUNDED -> ON_RAMPING -> SETTLING -> OFF_RAMPING -> COMPLETED.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field             | Type   | Required | Description                                           | Constraints                        |
|-------------------|--------|----------|-------------------------------------------------------|------------------------------------|
| `idempotency_key` | string | Yes      | Unique key to prevent duplicate transfers              | Max 255 characters                 |
| `external_ref`    | string | No       | Your external reference for reconciliation             | Max 255 characters                 |
| `source_currency` | string | Yes      | ISO currency code of the source funds                  | 3-5 uppercase letters              |
| `source_amount`   | string | Yes      | Amount to send in source currency                      | 0.01 to 10,000,000                 |
| `dest_currency`   | string | Yes      | ISO currency code of the destination funds             | 3-5 uppercase letters              |
| `sender`          | object | No       | Sender information                                     |                                    |
| `sender.id`       | string | No       | Sender UUID in your system                             |                                    |
| `sender.name`     | string | No       | Sender full name                                       |                                    |
| `sender.email`    | string | No       | Sender email                                           |                                    |
| `sender.country`  | string | No       | Sender country code                                    | 2 uppercase letters                |
| `recipient`       | object | Yes      | Recipient information                                  |                                    |
| `recipient.name`  | string | Yes      | Recipient full name                                    |                                    |
| `recipient.account_number` | string | No | Bank account number                              |                                    |
| `recipient.sort_code`     | string | No | UK sort code (mutually exclusive with `iban`)    | 6 digits                           |
| `recipient.bank_name`     | string | No | Bank name                                        |                                    |
| `recipient.country`       | string | Yes | Recipient country code                           | 2 uppercase letters                |
| `recipient.iban`          | string | No | IBAN (mutually exclusive with `sort_code`)       |                                    |
| `quote_id`        | string | No       | Lock in a previously obtained quote                    | UUID format                        |

**Response (201):**

| Field                     | Type   | Description                                      |
|---------------------------|--------|--------------------------------------------------|
| `id`                      | string | Transfer UUID                                    |
| `tenant_id`               | string | Tenant UUID                                      |
| `external_ref`            | string | Your external reference                          |
| `idempotency_key`         | string | Idempotency key used                             |
| `status`                  | string | Current transfer status                          |
| `version`                 | integer| Optimistic lock version                          |
| `source_currency`         | string | Source currency code                             |
| `source_amount`           | string | Source amount                                    |
| `dest_currency`           | string | Destination currency code                        |
| `dest_amount`             | string | Destination amount                               |
| `stable_coin`             | string | Stablecoin used (USDT, USDC)                     |
| `stable_amount`           | string | Amount in stablecoin                             |
| `chain`                   | string | Blockchain used                                  |
| `fx_rate`                 | string | Applied FX rate                                  |
| `fees`                    | object | Fee breakdown                                    |
| `sender`                  | object | Sender details                                   |
| `recipient`               | object | Recipient details                                |
| `quote_id`                | string | Quote UUID if provided                           |
| `blockchain_transactions` | array  | On-chain transaction details                     |
| `created_at`              | string | ISO 8601 UTC creation time                       |
| `updated_at`              | string | ISO 8601 UTC last update time                    |
| `funded_at`               | string | ISO 8601 UTC time when funded                    |
| `completed_at`            | string | ISO 8601 UTC time when completed                 |
| `failed_at`               | string | ISO 8601 UTC time when failed                    |
| `failure_reason`          | string | Human-readable failure description               |
| `failure_code`            | string | Machine-readable failure code                    |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/transfers \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "idempotency_key": "txn-2025-001",
    "external_ref": "INV-12345",
    "source_currency": "NGN",
    "source_amount": "500000",
    "dest_currency": "GBP",
    "recipient": {
      "name": "John Smith",
      "account_number": "12345678",
      "sort_code": "090128",
      "bank_name": "Monzo",
      "country": "GB"
    }
  }'
```

```json
{
  "id": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
  "tenant_id": "a0000000-0000-0000-0000-000000000001",
  "external_ref": "INV-12345",
  "idempotency_key": "txn-2025-001",
  "status": "CREATED",
  "version": 1,
  "source_currency": "NGN",
  "source_amount": "500000",
  "dest_currency": "GBP",
  "dest_amount": "245.50",
  "stable_coin": "USDT",
  "stable_amount": "308.00",
  "chain": "tron",
  "fx_rate": "2036.66",
  "fees": {
    "on_ramp_fee": "2.00",
    "network_fee": "0.50",
    "off_ramp_fee": "1.75",
    "total_fee_usd": "4.25"
  },
  "sender": null,
  "recipient": {
    "name": "John Smith",
    "account_number": "12345678",
    "sort_code": "090128",
    "bank_name": "Monzo",
    "country": "GB"
  },
  "quote_id": null,
  "blockchain_transactions": [],
  "created_at": "2025-01-15T14:30:00.000Z",
  "updated_at": "2025-01-15T14:30:00.000Z",
  "funded_at": null,
  "completed_at": null,
  "failed_at": null,
  "failure_reason": null,
  "failure_code": null
}
```

**Error Codes:**

| HTTP | Code                       | Description                                    |
|------|----------------------------|------------------------------------------------|
| 400  | `BAD_REQUEST`              | Invalid request body or missing required fields|
| 400  | `AMOUNT_OUT_OF_RANGE`      | Amount below 0.01 or above 10,000,000          |
| 400  | `MISSING_PAYMENT_DETAILS`  | GB recipients need sort_code or iban           |
| 400  | `CORRIDOR_DISABLED`        | Currency pair not supported                    |
| 400  | `QUOTE_EXPIRED`            | Referenced quote has expired                   |
| 400  | `DAILY_LIMIT_EXCEEDED`     | Tenant daily volume limit exceeded             |
| 401  | `UNAUTHORIZED`             | Invalid or missing API key                     |
| 409  | `IDEMPOTENCY_CONFLICT`     | Idempotency key used with different parameters |
| 429  | `RATE_LIMIT_EXCEEDED`      | Too many requests                              |

---

### GET /v1/transfers/{transferId} -- Get Transfer

Retrieve a transfer by its UUID.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter    | Type   | Required | Description      |
|--------------|--------|----------|------------------|
| `transferId` | string | Yes      | Transfer UUID    |

**Response (200):** Same schema as Create Transfer response.

**Example:**

```bash
curl https://api.settla.io/v1/transfers/b2c3d4e5-f6a7-8901-bcde-f12345678901 \
  -H "Authorization: Bearer sk_live_abc123"
```

**Error Codes:**

| HTTP | Code              | Description           |
|------|-------------------|-----------------------|
| 404  | `TRANSFER_NOT_FOUND` | Transfer not found |

---

### GET /v1/transfers -- List Transfers

List transfers for the authenticated tenant with optional filtering.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter    | Type    | Default | Description                                           |
|--------------|---------|---------|-------------------------------------------------------|
| `page_size`  | integer | 20      | Number of transfers per page (1-100)                  |
| `page_token` | string  | -       | Pagination cursor from previous response              |
| `status`     | string  | -       | Filter by exact status (e.g., `COMPLETED`, `FAILED`)  |
| `search`     | string  | -       | Substring search on id, external_ref, idempotency_key |

**Response (200):**

| Field             | Type    | Description                              |
|-------------------|---------|------------------------------------------|
| `transfers`       | array   | Array of Transfer objects                |
| `next_page_token` | string  | Cursor for next page (empty if last)     |
| `total_count`     | integer | Total matching transfers                 |

**Example:**

```bash
curl "https://api.settla.io/v1/transfers?status=COMPLETED&page_size=10" \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "transfers": [
    {
      "id": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
      "status": "COMPLETED",
      "source_currency": "NGN",
      "source_amount": "500000",
      "dest_currency": "GBP",
      "dest_amount": "245.50",
      "created_at": "2025-01-15T14:30:00.000Z",
      "completed_at": "2025-01-15T14:45:00.000Z"
    }
  ],
  "next_page_token": "eyJpZCI6...",
  "total_count": 1
}
```

---

### GET /v1/transfers/{transferId}/events -- Get Transfer Events

Retrieve the event history (state transitions) for a transfer.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter    | Type   | Required | Description      |
|--------------|--------|----------|------------------|
| `transferId` | string | Yes      | Transfer UUID    |

**Response (200):**

| Field    | Type  | Description                    |
|----------|-------|--------------------------------|
| `events` | array | Array of TransferEvent objects |

**Example:**

```bash
curl https://api.settla.io/v1/transfers/b2c3d4e5-f6a7-8901-bcde-f12345678901/events \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "events": [
    {
      "id": "e1e2e3e4-e5e6-e7e8-e9ea-ebecedeeeff0",
      "transfer_id": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
      "event_type": "transfer.created",
      "from_status": "",
      "to_status": "CREATED",
      "created_at": "2025-01-15T14:30:00.000Z"
    },
    {
      "id": "f1f2f3f4-f5f6-f7f8-f9fa-fbfcfdfeff00",
      "transfer_id": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
      "event_type": "transfer.funded",
      "from_status": "CREATED",
      "to_status": "FUNDED",
      "created_at": "2025-01-15T14:30:02.000Z"
    }
  ]
}
```

**Error Codes:**

| HTTP | Code              | Description           |
|------|-------------------|-----------------------|
| 404  | `TRANSFER_NOT_FOUND` | Transfer not found |

---

### POST /v1/transfers/{transferId}/cancel -- Cancel Transfer

Cancel a transfer that has not yet progressed past the CREATED state. Transfers in later states cannot be cancelled through this endpoint.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter    | Type   | Required | Description      |
|--------------|--------|----------|------------------|
| `transferId` | string | Yes      | Transfer UUID    |

**Request Body:**

| Field    | Type   | Required | Description               |
|----------|--------|----------|---------------------------|
| `reason` | string | No       | Reason for cancellation   |

**Response (200):** Updated Transfer object.

**Example:**

```bash
curl -X POST https://api.settla.io/v1/transfers/b2c3d4e5-f6a7-8901-bcde-f12345678901/cancel \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{"reason": "Customer requested cancellation"}'
```

**Error Codes:**

| HTTP | Code                 | Description                                |
|------|----------------------|--------------------------------------------|
| 400  | `INVALID_TRANSITION` | Transfer cannot be cancelled in its current state |
| 404  | `TRANSFER_NOT_FOUND` | Transfer not found                         |

---

## Treasury

### GET /v1/treasury/positions -- List Positions

List all treasury positions for the authenticated tenant. Each position represents a balance in a specific currency at a specific location.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field       | Type  | Description                    |
|-------------|-------|--------------------------------|
| `positions` | array | Array of Position objects      |

**Example:**

```bash
curl https://api.settla.io/v1/treasury/positions \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "positions": [
    {
      "tenantId": "a0000000-0000-0000-0000-000000000001",
      "currency": "USDT",
      "location": "tron",
      "balance": "50000.00",
      "locked": "5000.00",
      "available": "45000.00",
      "updatedAt": "2025-01-15T14:30:00.000Z"
    },
    {
      "tenantId": "a0000000-0000-0000-0000-000000000001",
      "currency": "NGN",
      "location": "bank",
      "balance": "25000000.00",
      "locked": "1000000.00",
      "available": "24000000.00",
      "updatedAt": "2025-01-15T14:30:00.000Z"
    }
  ]
}
```

---

### GET /v1/treasury/positions/{currency}/{location} -- Get Position

Retrieve a specific treasury position by currency and location.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter  | Type   | Required | Description                         | Constraints           |
|------------|--------|----------|-------------------------------------|-----------------------|
| `currency` | string | Yes      | Currency code (e.g., USDT, NGN)     | 3-5 uppercase letters |
| `location` | string | Yes      | Location (e.g., tron, bank, ethereum)| Non-empty string     |

**Response (200):** Single Position object.

**Example:**

```bash
curl https://api.settla.io/v1/treasury/positions/USDT/tron \
  -H "Authorization: Bearer sk_live_abc123"
```

**Error Codes:**

| HTTP | Code       | Description                |
|------|------------|----------------------------|
| 404  | `NOT_FOUND`| Position not found         |

---

### GET /v1/treasury/liquidity -- Get Liquidity Report

Generate a liquidity report showing all positions, total available funds per currency, and positions that are below alert thresholds.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field             | Type   | Description                                       |
|-------------------|--------|---------------------------------------------------|
| `tenant_id`       | string | Tenant UUID                                       |
| `positions`       | array  | All Position objects                              |
| `total_available` | object | Map of currency code to total available amount    |
| `alert_positions` | array  | Positions below low-liquidity thresholds          |
| `generated_at`    | string | ISO 8601 UTC report generation time               |

**Example:**

```bash
curl https://api.settla.io/v1/treasury/liquidity \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "tenant_id": "a0000000-0000-0000-0000-000000000001",
  "positions": [],
  "total_available": {
    "USDT": "95000.00",
    "NGN": "24000000.00",
    "GBP": "12000.00"
  },
  "alert_positions": [],
  "generated_at": "2025-01-15T14:30:00.000Z"
}
```

---

### POST /v1/treasury/topup -- Request Top-Up

Request a top-up to increase funds in a treasury position.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field      | Type   | Required | Description                              | Constraints                             |
|------------|--------|----------|------------------------------------------|-----------------------------------------|
| `currency` | string | Yes      | Currency code                            | 3-5 uppercase letters                   |
| `location` | string | Yes      | Position location                        | Non-empty string                        |
| `amount`   | string | Yes      | Amount to top up                         | Positive decimal string                 |
| `method`   | string | No       | Funding method                           | `bank_transfer`, `crypto`, `internal` (default: `bank_transfer`) |

**Response (200):**

| Field         | Type   | Description                    |
|---------------|--------|--------------------------------|
| `transaction` | object | PositionTransaction object     |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/treasury/topup \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "currency": "USDT",
    "location": "tron",
    "amount": "10000.00",
    "method": "crypto"
  }'
```

```json
{
  "transaction": {
    "id": "c3d4e5f6-a7b8-9012-cdef-123456789012",
    "tenantId": "a0000000-0000-0000-0000-000000000001",
    "type": "topup",
    "currency": "USDT",
    "location": "tron",
    "amount": "10000.00",
    "status": "pending",
    "method": "crypto",
    "reference": "",
    "createdAt": "2025-01-15T14:30:00.000Z",
    "updatedAt": "2025-01-15T14:30:00.000Z"
  }
}
```

**Error Codes:**

| HTTP | Code            | Description                 |
|------|-----------------|-----------------------------|
| 400  | `BAD_REQUEST`   | Invalid request body        |

---

### POST /v1/treasury/withdraw -- Request Withdrawal

Request a withdrawal from a treasury position.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field         | Type   | Required | Description                        | Constraints                       |
|---------------|--------|----------|------------------------------------|-----------------------------------|
| `currency`    | string | Yes      | Currency code                      | 3-5 uppercase letters             |
| `location`    | string | Yes      | Position location                  | Non-empty string                  |
| `amount`      | string | Yes      | Amount to withdraw                 | Positive decimal string           |
| `method`      | string | No       | Withdrawal method                  | `bank_transfer`, `crypto` (default: `bank_transfer`) |
| `destination` | string | Yes      | Destination address or account     | Non-empty string                  |

**Response (200):**

| Field         | Type   | Description                    |
|---------------|--------|--------------------------------|
| `transaction` | object | PositionTransaction object     |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/treasury/withdraw \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "currency": "USDT",
    "location": "tron",
    "amount": "5000.00",
    "method": "crypto",
    "destination": "TXyz...abc"
  }'
```

**Error Codes:**

| HTTP | Code                              | Description                       |
|------|-----------------------------------|-----------------------------------|
| 400  | `INSUFFICIENT_FUNDS`              | Not enough available balance      |
| 400  | `RESERVATION_INSUFFICIENT_FUNDS`  | Treasury position too low         |

---

### GET /v1/treasury/transactions -- List Position Transactions

List position transactions (top-ups, withdrawals) for the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type    | Default | Description            |
|-----------|---------|---------|------------------------|
| `limit`   | integer | 20      | Items per page (1-100) |
| `offset`  | integer | 0       | Items to skip          |

**Response (200):**

| Field          | Type    | Description                           |
|----------------|---------|---------------------------------------|
| `transactions` | array   | Array of PositionTransaction objects  |
| `totalCount`   | integer | Total matching transactions           |

**Example:**

```bash
curl "https://api.settla.io/v1/treasury/transactions?limit=10" \
  -H "Authorization: Bearer sk_live_abc123"
```

---

### GET /v1/treasury/transactions/{id} -- Get Position Transaction

Retrieve a specific position transaction by ID.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description            |
|-----------|--------|----------|------------------------|
| `id`      | string | Yes      | Transaction UUID       |

**Response (200):**

| Field         | Type   | Description                    |
|---------------|--------|--------------------------------|
| `transaction` | object | PositionTransaction object     |

**Error Codes:**

| HTTP | Code       | Description                  |
|------|------------|------------------------------|
| 404  | `NOT_FOUND`| Transaction not found        |

---

### GET /v1/treasury/positions/{currency}/{location}/events -- Get Position Event History

Retrieve the audit log of events for a treasury position (reserves, releases, flushes).

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter  | Type   | Required | Description            |
|------------|--------|----------|------------------------|
| `currency` | string | Yes      | Currency code          |
| `location` | string | Yes      | Position location      |

**Query Parameters:**

| Parameter | Type    | Default | Description                        |
|-----------|---------|---------|------------------------------------|
| `from`    | string  | -       | Start time (ISO 8601 date-time)    |
| `to`      | string  | -       | End time (ISO 8601 date-time)      |
| `limit`   | integer | 50      | Items per page (1-100)             |
| `offset`  | integer | 0       | Items to skip                      |

**Response (200):**

| Field        | Type    | Description                       |
|--------------|---------|-----------------------------------|
| `events`     | array   | Array of PositionEvent objects    |
| `totalCount` | integer | Total matching events             |

**Example:**

```bash
curl "https://api.settla.io/v1/treasury/positions/USDT/tron/events?limit=20&from=2025-01-01T00:00:00Z" \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "events": [
    {
      "id": "d4e5f6a7-b8c9-0123-def0-123456789abc",
      "positionId": "p1p2p3p4-p5p6-p7p8-p9pa-pbpcpdpepfp0",
      "tenantId": "a0000000-0000-0000-0000-000000000001",
      "eventType": "reserve",
      "amount": "308.00",
      "balanceAfter": "49692.00",
      "lockedAfter": "5308.00",
      "referenceId": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
      "referenceType": "transfer",
      "recordedAt": "2025-01-15T14:30:01.000Z"
    }
  ],
  "totalCount": 1
}
```

---

## Ledger

### GET /v1/accounts -- List Ledger Accounts

List all ledger accounts for the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter    | Type   | Default | Description              |
|--------------|--------|---------|--------------------------|
| `page_size`  | string | -       | Number of items per page |
| `page_token` | string | -       | Pagination cursor        |

**Response (200):**

| Field           | Type    | Description                        |
|-----------------|---------|------------------------------------|
| `accounts`      | array   | Array of Account objects           |
| `nextPageToken` | string  | Cursor for next page               |
| `totalCount`    | number  | Total number of accounts           |

Each Account object:

| Field       | Type    | Description                                              |
|-------------|---------|----------------------------------------------------------|
| `id`        | string  | Account UUID                                             |
| `tenantId`  | string  | Tenant UUID                                              |
| `code`      | string  | Account code (e.g., `tenant:lemfi:assets:bank:gbp:clearing`) |
| `name`      | string  | Human-readable account name                              |
| `type`      | string  | Account type                                             |
| `currency`  | string  | Account currency                                         |
| `isActive`  | boolean | Whether the account is active                            |
| `createdAt` | string  | ISO 8601 UTC creation time                               |
| `updatedAt` | string  | ISO 8601 UTC last update time                            |

**Example:**

```bash
curl "https://api.settla.io/v1/accounts?page_size=20" \
  -H "Authorization: Bearer sk_live_abc123"
```

---

### GET /v1/accounts/{code}/balance -- Get Account Balance

Get the current balance of a ledger account by its account code.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description                                            |
|-----------|--------|----------|--------------------------------------------------------|
| `code`    | string | Yes      | URL-encoded account code (alphanumeric, colons, dots, hyphens, underscores) |

**Response (200):**

| Field                     | Type   | Description      |
|---------------------------|--------|------------------|
| `accountBalance.accountCode` | string | Account code  |
| `accountBalance.balance`     | string | Current balance |
| `accountBalance.currency`    | string | Currency code  |

**Example:**

```bash
curl "https://api.settla.io/v1/accounts/tenant%3Alemfi%3Aassets%3Abank%3Agbp%3Aclearing/balance" \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "accountBalance": {
    "accountCode": "tenant:lemfi:assets:bank:gbp:clearing",
    "balance": "12500.00",
    "currency": "GBP"
  }
}
```

**Error Codes:**

| HTTP | Code                  | Description                          |
|------|-----------------------|--------------------------------------|
| 400  | `INVALID_ACCOUNT_CODE`| Account code contains invalid characters |
| 404  | `ACCOUNT_NOT_FOUND`   | Account not found                    |

---

### GET /v1/accounts/{code}/transactions -- List Account Transactions

List ledger entries (debits/credits) for a specific account.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description                   |
|-----------|--------|----------|-------------------------------|
| `code`    | string | Yes      | URL-encoded account code      |

**Query Parameters:**

| Parameter    | Type   | Default | Description                       |
|--------------|--------|---------|-----------------------------------|
| `from`       | string | -       | Start time (ISO 8601 date-time)   |
| `to`         | string | -       | End time (ISO 8601 date-time)     |
| `page_size`  | string | -       | Number of items per page          |
| `page_token` | string | -       | Pagination cursor                 |

**Response (200):**

| Field           | Type    | Description                     |
|-----------------|---------|---------------------------------|
| `entries`       | array   | Array of ledger entry objects   |
| `nextPageToken` | string  | Cursor for next page            |
| `totalCount`    | number  | Total number of entries         |

Each entry object:

| Field         | Type   | Description                      |
|---------------|--------|----------------------------------|
| `id`          | string | Entry UUID                       |
| `accountId`   | string | Account UUID                     |
| `accountCode` | string | Account code                     |
| `entryType`   | string | `debit` or `credit`              |
| `amount`      | string | Entry amount                     |
| `currency`    | string | Currency code                    |
| `description` | string | Entry description                |

**Example:**

```bash
curl "https://api.settla.io/v1/accounts/tenant%3Alemfi%3Aassets%3Abank%3Agbp%3Aclearing/transactions?from=2025-01-01T00:00:00Z&page_size=50" \
  -H "Authorization: Bearer sk_live_abc123"
```

---

## Crypto Deposits

### POST /v1/deposits -- Create Deposit Session

Create a new crypto deposit session. The system assigns a deposit address on the specified chain and monitors for incoming payments.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field             | Type    | Required | Description                                     | Constraints                             |
|-------------------|---------|----------|-------------------------------------------------|-----------------------------------------|
| `chain`           | string  | Yes      | Blockchain to receive on                        | `tron`, `ethereum`, `solana`, `base`, `polygon`, `arbitrum` |
| `token`           | string  | Yes      | Token to receive                                | `USDT`, `USDC`                          |
| `expected_amount` | string  | Yes      | Expected deposit amount                         | Positive decimal string                 |
| `currency`        | string  | No       | Settlement currency (for AUTO_CONVERT)          | ISO currency code                       |
| `settlement_pref` | string  | No       | What to do after deposit is credited            | `AUTO_CONVERT`, `HOLD`, `THRESHOLD`     |
| `idempotency_key` | string  | No       | Unique key to prevent duplicate sessions        |                                         |
| `ttl_seconds`     | integer | No       | Session time-to-live in seconds                 | Non-negative integer                    |

**Response (201):**

| Field     | Type   | Description                    |
|-----------|--------|--------------------------------|
| `session` | object | DepositSession object          |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/deposits \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "chain": "tron",
    "token": "USDT",
    "expected_amount": "500.00",
    "settlement_pref": "AUTO_CONVERT",
    "currency": "NGN"
  }'
```

```json
{
  "session": {
    "id": "e5f6a7b8-c9d0-1234-ef56-789012345678",
    "tenantId": "a0000000-0000-0000-0000-000000000001",
    "status": "PENDING_PAYMENT",
    "chain": "tron",
    "token": "USDT",
    "depositAddress": "TXyz...abc",
    "expectedAmount": "500.00",
    "receivedAmount": "0",
    "settlementPref": "AUTO_CONVERT",
    "expiresAt": "2025-01-15T15:30:00.000Z",
    "createdAt": "2025-01-15T14:30:00.000Z"
  }
}
```

**Error Codes:**

| HTTP | Code                   | Description                            |
|------|------------------------|----------------------------------------|
| 400  | `CRYPTO_DISABLED`      | Crypto deposits disabled for tenant    |
| 400  | `CHAIN_NOT_SUPPORTED`  | Requested chain not supported          |
| 400  | `ADDRESS_POOL_EMPTY`   | No deposit addresses available         |
| 409  | `IDEMPOTENCY_CONFLICT` | Duplicate idempotency key              |

---

### GET /v1/deposits/{id} -- Get Deposit Session

Retrieve a crypto deposit session by ID.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description       |
|-----------|--------|----------|-------------------|
| `id`      | string | Yes      | Session UUID      |

**Response (200):**

| Field     | Type   | Description               |
|-----------|--------|---------------------------|
| `session` | object | DepositSession object     |

**Error Codes:**

| HTTP | Code               | Description             |
|------|--------------------|-------------------------|
| 404  | `DEPOSIT_NOT_FOUND`| Deposit session not found|

---

### GET /v1/deposits -- List Deposit Sessions

List crypto deposit sessions for the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type    | Default | Description            |
|-----------|---------|---------|------------------------|
| `limit`   | integer | 20      | Items per page (1-100) |
| `offset`  | integer | 0       | Items to skip          |

**Response (200):**

| Field      | Type    | Description                         |
|------------|---------|-------------------------------------|
| `sessions` | array   | Array of DepositSession objects     |
| `total`    | integer | Total matching sessions             |

---

### POST /v1/deposits/{id}/cancel -- Cancel Deposit Session

Cancel a pending deposit session. Only sessions in `PENDING_PAYMENT` status can be cancelled.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description       |
|-----------|--------|----------|-------------------|
| `id`      | string | Yes      | Session UUID      |

**Response (200):**

| Field     | Type   | Description                    |
|-----------|--------|--------------------------------|
| `session` | object | Updated DepositSession object  |

**Error Codes:**

| HTTP | Code                 | Description                         |
|------|----------------------|-------------------------------------|
| 404  | `DEPOSIT_NOT_FOUND`  | Deposit session not found           |
| 409  | `INVALID_TRANSITION` | Cannot cancel in current state      |

---

### GET /v1/deposits/balance -- Get Crypto Balances

Get aggregated crypto deposit balances (USDT, USDC) for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field           | Type   | Description                                |
|-----------------|--------|--------------------------------------------|
| `balances`      | array  | Array of chain/token balance objects       |
| `total_value_usd` | string | Total value in USD                      |

Each balance object:

| Field       | Type   | Description              |
|-------------|--------|--------------------------|
| `chain`     | string | Blockchain (e.g., tron)  |
| `token`     | string | Token (e.g., USDT)       |
| `balance`   | string | Available balance        |
| `value_usd` | string | USD equivalent           |

**Example:**

```bash
curl https://api.settla.io/v1/deposits/balance \
  -H "Authorization: Bearer sk_live_abc123"
```

```json
{
  "balances": [
    {"chain": "tron", "token": "USDT", "balance": "45000.00", "value_usd": "45000.00"},
    {"chain": "ethereum", "token": "USDC", "balance": "12000.00", "value_usd": "12000.00"}
  ],
  "total_value_usd": "57000.00"
}
```

---

### POST /v1/deposits/convert -- Convert Crypto to Fiat

Initiate conversion of a crypto balance to fiat via the settlement engine.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field   | Type   | Required | Description                 |
|---------|--------|----------|-----------------------------|
| `chain` | string | Yes      | Source blockchain            |
| `token` | string | Yes      | Token to convert             |
| `amount`| string | Yes      | Amount to convert            |

**Response (200):**

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| `message` | string | Confirmation message                |

---

### GET /v1/deposits/{id}/public-status -- Get Deposit Public Status

Retrieve limited deposit session information without authentication. Used by payers to check payment status via payment link pages.

**Authentication:** None

**Path Parameters:**

| Parameter | Type   | Required | Description       |
|-----------|--------|----------|-------------------|
| `id`      | string | Yes      | Session UUID      |

**Response (200):**

| Field             | Type   | Description                    |
|-------------------|--------|--------------------------------|
| `id`              | string | Session UUID                   |
| `status`          | string | Current session status         |
| `chain`           | string | Blockchain                     |
| `token`           | string | Token type                     |
| `deposit_address` | string | Address to send payment to     |
| `expected_amount` | string | Expected amount                |
| `received_amount` | string | Amount received so far         |
| `expires_at`      | string | Session expiration time        |

---

## Bank Deposits

### POST /v1/bank-deposits -- Create Bank Deposit Session

Create a new fiat bank deposit session. The system assigns a virtual bank account and monitors for incoming credits.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field               | Type    | Required | Description                                  | Constraints                         |
|---------------------|---------|----------|----------------------------------------------|-------------------------------------|
| `currency`          | string  | Yes      | Fiat currency to receive                     | ISO currency code                   |
| `expected_amount`   | string  | Yes      | Expected deposit amount                      | Positive decimal string             |
| `banking_partner_id`| string  | No       | Preferred banking partner                    |                                     |
| `account_type`      | string  | No       | Virtual account type                         | `PERMANENT`, `TEMPORARY`            |
| `min_amount`        | string  | No       | Minimum acceptable amount                    | Positive decimal string             |
| `max_amount`        | string  | No       | Maximum acceptable amount                    | Positive decimal string             |
| `mismatch_policy`   | string  | No       | How to handle under/overpayment              | `ACCEPT`, `REJECT`                  |
| `settlement_pref`   | string  | No       | What to do after deposit is credited         | `AUTO_CONVERT`, `HOLD`, `THRESHOLD` |
| `idempotency_key`   | string  | No       | Unique key to prevent duplicate sessions     |                                     |
| `ttl_seconds`       | integer | No       | Session time-to-live in seconds              | Non-negative integer                |

**Response (201):**

| Field     | Type   | Description                        |
|-----------|--------|------------------------------------|
| `session` | object | BankDepositSession object          |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/bank-deposits \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "currency": "NGN",
    "expected_amount": "1000000",
    "account_type": "TEMPORARY",
    "mismatch_policy": "ACCEPT",
    "settlement_pref": "AUTO_CONVERT"
  }'
```

```json
{
  "session": {
    "id": "f6a7b8c9-d0e1-2345-f678-901234567890",
    "tenantId": "a0000000-0000-0000-0000-000000000001",
    "status": "PENDING_PAYMENT",
    "currency": "NGN",
    "expectedAmount": "1000000",
    "virtualAccountNumber": "0012345678",
    "virtualAccountBankName": "Test Bank",
    "virtualAccountBankCode": "000",
    "expiresAt": "2025-01-16T14:30:00.000Z",
    "createdAt": "2025-01-15T14:30:00.000Z"
  }
}
```

**Error Codes:**

| HTTP | Code                          | Description                          |
|------|-------------------------------|--------------------------------------|
| 400  | `BANK_DEPOSITS_DISABLED`      | Bank deposits disabled for tenant    |
| 400  | `CURRENCY_NOT_SUPPORTED`      | Currency not supported for deposits  |
| 400  | `VIRTUAL_ACCOUNT_POOL_EMPTY`  | No virtual accounts available        |
| 409  | `IDEMPOTENCY_CONFLICT`        | Duplicate idempotency key            |

---

### GET /v1/bank-deposits/{id} -- Get Bank Deposit Session

Retrieve a bank deposit session by ID.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description  |
|-----------|--------|----------|--------------|
| `id`      | string | Yes      | Session UUID |

**Response (200):**

| Field     | Type   | Description                    |
|-----------|--------|--------------------------------|
| `session` | object | BankDepositSession object      |

**Error Codes:**

| HTTP | Code                     | Description                    |
|------|--------------------------|--------------------------------|
| 404  | `BANK_DEPOSIT_NOT_FOUND` | Session not found              |

---

### GET /v1/bank-deposits -- List Bank Deposit Sessions

List bank deposit sessions for the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type    | Default | Description            |
|-----------|---------|---------|------------------------|
| `limit`   | integer | 20      | Items per page (1-100) |
| `offset`  | integer | 0       | Items to skip          |

**Response (200):**

| Field      | Type    | Description                              |
|------------|---------|------------------------------------------|
| `sessions` | array   | Array of BankDepositSession objects      |
| `total`    | integer | Total matching sessions                  |

---

### POST /v1/bank-deposits/{id}/cancel -- Cancel Bank Deposit Session

Cancel a pending bank deposit session.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description  |
|-----------|--------|----------|--------------|
| `id`      | string | Yes      | Session UUID |

**Response (200):**

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| `session` | object | Updated BankDepositSession object   |

**Error Codes:**

| HTTP | Code                     | Description                            |
|------|--------------------------|----------------------------------------|
| 404  | `BANK_DEPOSIT_NOT_FOUND` | Session not found                      |
| 409  | `INVALID_TRANSITION`     | Cannot cancel in current state         |

---

### GET /v1/bank-deposits/accounts -- List Virtual Accounts

List virtual bank accounts assigned to the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter      | Type    | Default | Description                       |
|----------------|---------|---------|-----------------------------------|
| `limit`        | integer | 20      | Items per page (1-100)            |
| `offset`       | integer | 0       | Items to skip                     |
| `currency`     | string  | -       | Filter by currency                |
| `account_type` | string  | -       | Filter by type: `PERMANENT`, `TEMPORARY` |

**Response (200):**

| Field      | Type    | Description                         |
|------------|---------|-------------------------------------|
| `accounts` | array   | Array of VirtualAccount objects     |
| `total`    | integer | Total matching accounts             |

---

## Payment Links

### POST /v1/payment-links -- Create Payment Link

Create a shareable payment link for collecting crypto deposits. Generates a short code that can be shared with payers.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field             | Type    | Required | Description                                 | Constraints                         |
|-------------------|---------|----------|---------------------------------------------|-------------------------------------|
| `amount`          | string  | Yes      | Payment amount                              | Positive decimal string             |
| `currency`        | string  | Yes      | Currency code                               | Non-empty string                    |
| `chain`           | string  | Yes      | Blockchain for payment                      | Non-empty string                    |
| `token`           | string  | Yes      | Token to accept                             | Non-empty string                    |
| `description`     | string  | No       | Payment description                         |                                     |
| `redirect_url`    | string  | No       | URL to redirect after payment               |                                     |
| `use_limit`       | integer | No       | Maximum number of redemptions               | Minimum 1                           |
| `expires_at_unix` | integer | No       | Expiration time as Unix timestamp           |                                     |
| `settlement_pref` | string  | No       | Settlement preference                       | `AUTO_CONVERT`, `HOLD`, `THRESHOLD` |
| `ttl_seconds`     | integer | No       | Deposit session TTL in seconds              | Non-negative integer                |

**Response (201):**

| Field  | Type   | Description                    |
|--------|--------|--------------------------------|
| `link` | object | PaymentLink object             |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/payment-links \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "amount": "100.00",
    "currency": "USDT",
    "chain": "tron",
    "token": "USDT",
    "description": "Invoice #1234",
    "use_limit": 1,
    "settlement_pref": "AUTO_CONVERT"
  }'
```

```json
{
  "link": {
    "id": "a7b8c9d0-e1f2-3456-a789-0123456789ab",
    "tenantId": "a0000000-0000-0000-0000-000000000001",
    "shortCode": "PAY-A1B2C3",
    "amount": "100.00",
    "currency": "USDT",
    "chain": "tron",
    "token": "USDT",
    "description": "Invoice #1234",
    "useLimit": 1,
    "useCount": 0,
    "isActive": true,
    "paymentUrl": "https://pay.settla.io/PAY-A1B2C3",
    "createdAt": "2025-01-15T14:30:00.000Z"
  }
}
```

**Error Codes:**

| HTTP | Code                     | Description                            |
|------|--------------------------|----------------------------------------|
| 400  | `BAD_REQUEST`            | Invalid request body                   |
| 400  | `SHORT_CODE_COLLISION`   | Short code generation collision (retry)|

---

### GET /v1/payment-links -- List Payment Links

List payment links for the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type    | Default | Description            |
|-----------|---------|---------|------------------------|
| `limit`   | integer | 20      | Items per page (1-100) |
| `offset`  | integer | 0       | Items to skip          |

**Response (200):**

| Field   | Type    | Description                       |
|---------|---------|-----------------------------------|
| `links` | array   | Array of PaymentLink objects      |
| `total` | integer | Total matching links              |

---

### GET /v1/payment-links/{id} -- Get Payment Link

Retrieve a payment link by ID.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description       |
|-----------|--------|----------|-------------------|
| `id`      | string | Yes      | Payment link UUID |

**Response (200):**

| Field  | Type   | Description               |
|--------|--------|---------------------------|
| `link` | object | PaymentLink object        |

**Error Codes:**

| HTTP | Code                    | Description              |
|------|-------------------------|--------------------------|
| 404  | `PAYMENT_LINK_NOT_FOUND`| Payment link not found   |

---

### DELETE /v1/payment-links/{id} -- Disable Payment Link

Permanently disable a payment link. Disabled links cannot be redeemed.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description       |
|-----------|--------|----------|-------------------|
| `id`      | string | Yes      | Payment link UUID |

**Response:** 204 No Content

**Error Codes:**

| HTTP | Code                    | Description              |
|------|-------------------------|--------------------------|
| 404  | `PAYMENT_LINK_NOT_FOUND`| Payment link not found   |

---

### GET /v1/payment-links/resolve/{code} -- Resolve Payment Link

Resolve a payment link by its short code. Used by payment pages to display payment details.

**Authentication:** None (public endpoint)

**Path Parameters:**

| Parameter | Type   | Required | Description          |
|-----------|--------|----------|----------------------|
| `code`    | string | Yes      | Payment link short code |

**Response (200):**

| Field  | Type   | Description               |
|--------|--------|---------------------------|
| `link` | object | PaymentLink object        |

**Error Codes:**

| HTTP | Code                      | Description                    |
|------|---------------------------|--------------------------------|
| 404  | `PAYMENT_LINK_NOT_FOUND`  | Payment link not found         |
| 404  | `PAYMENT_LINK_EXPIRED`    | Payment link has expired       |
| 404  | `PAYMENT_LINK_DISABLED`   | Payment link is disabled       |
| 404  | `PAYMENT_LINK_EXHAUSTED`  | Payment link use limit reached |

---

### POST /v1/payment-links/redeem/{code} -- Redeem Payment Link

Redeem a payment link, creating a crypto deposit session for the payer.

**Authentication:** None (public endpoint)

**Path Parameters:**

| Parameter | Type   | Required | Description             |
|-----------|--------|----------|-------------------------|
| `code`    | string | Yes      | Payment link short code |

**Response (201):**

| Field     | Type   | Description                                   |
|-----------|--------|-----------------------------------------------|
| `session` | object | DepositSession object (for the payer)         |
| `link`    | object | PaymentLink object (updated use count)        |

**Error Codes:**

| HTTP | Code                      | Description                    |
|------|---------------------------|--------------------------------|
| 404  | `PAYMENT_LINK_NOT_FOUND`  | Payment link not found         |
| 409  | `PAYMENT_LINK_EXPIRED`    | Payment link has expired       |
| 409  | `PAYMENT_LINK_EXHAUSTED`  | Payment link use limit reached |
| 409  | `PAYMENT_LINK_DISABLED`   | Payment link is disabled       |

---

## Routes

### POST /v1/routes -- Get Routing Options

Get scored routing options for a currency corridor. The router evaluates all available provider and chain combinations, scoring each on cost (40%), speed (30%), liquidity (20%), and reliability (10%).

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field           | Type   | Required | Description                  |
|-----------------|--------|----------|------------------------------|
| `from_currency` | string | Yes      | Source currency code         |
| `to_currency`   | string | Yes      | Destination currency code    |
| `amount`        | string | Yes      | Transfer amount              |

**Response (200):**

| Field                | Type    | Description                         |
|----------------------|---------|-------------------------------------|
| `routes`             | array   | Array of scored route options       |
| `quoted_at`          | string  | ISO 8601 UTC time of evaluation     |
| `valid_for_seconds`  | integer | How long these routes remain valid  |

Each route object:

| Field                          | Type   | Description                              |
|--------------------------------|--------|------------------------------------------|
| `provider`                     | string | On-ramp provider name                    |
| `off_ramp_provider`            | string | Off-ramp provider name                   |
| `chain`                        | string | Blockchain used                          |
| `stablecoin`                   | string | Stablecoin used (USDT, USDC)             |
| `score`                        | number | Composite score (0-100)                  |
| `estimated_fee_usd`            | string | Estimated fee in USD                     |
| `estimated_settlement_seconds` | integer| Estimated settlement time                |
| `score_breakdown`              | object | Individual scoring components            |
| `score_breakdown.cost`         | number | Cost score (0-100)                       |
| `score_breakdown.speed`        | number | Speed score (0-100)                      |
| `score_breakdown.liquidity`    | number | Liquidity score (0-100)                  |
| `score_breakdown.reliability`  | number | Reliability score (0-100)                |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/routes \
  -H "Authorization: Bearer sk_live_abc123" \
  -H "Content-Type: application/json" \
  -d '{
    "from_currency": "NGN",
    "to_currency": "GBP",
    "amount": "500000"
  }'
```

```json
{
  "routes": [
    {
      "provider": "settla",
      "off_ramp_provider": "settla",
      "chain": "tron",
      "stablecoin": "USDT",
      "score": 87.5,
      "estimated_fee_usd": "4.25",
      "estimated_settlement_seconds": 900,
      "score_breakdown": {
        "cost": 92,
        "speed": 85,
        "liquidity": 80,
        "reliability": 95
      }
    }
  ],
  "quoted_at": "2025-01-15T14:30:00.000Z",
  "valid_for_seconds": 300
}
```

---

## Verification

### GET /v1/transactions/verify/{id} -- Verify Transaction

Verify a transaction by UUID. Searches both transfers and deposit sessions, returning whichever matches.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description                    |
|-----------|--------|----------|--------------------------------|
| `id`      | string | Yes      | Transfer or deposit UUID       |

**Response (200):**

| Field              | Type   | Description                                    |
|--------------------|--------|------------------------------------------------|
| `type`             | string | `transfer` or `deposit`                        |
| `id`               | string | Entity UUID                                    |
| `tenant_id`        | string | Tenant UUID                                    |
| `status`           | string | Current status                                 |
| `external_ref`     | string | External reference (transfers only)            |
| `source_currency`  | string | Source currency (transfers only)               |
| `source_amount`    | string | Source amount (transfers only)                 |
| `dest_currency`    | string | Destination currency (transfers only)          |
| `dest_amount`      | string | Destination amount (transfers only)            |
| `chain`            | string | Blockchain used                                |
| `token`            | string | Token (deposits only)                          |
| `expected_amount`  | string | Expected amount (deposits only)                |
| `received_amount`  | string | Received amount (deposits only)                |
| `deposit_address`  | string | Deposit address (deposits only)                |
| `fees`             | object | Fee breakdown (transfers only)                 |
| `sender`           | object | Sender details (transfers only)                |
| `recipient`        | object | Recipient details (transfers only)             |
| `transactions`     | array  | On-chain transactions (deposits only)          |
| `created_at`       | string | Creation time                                  |
| `updated_at`       | string | Last update time                               |
| `completed_at`     | string | Completion time (transfers only)               |
| `failed_at`        | string | Failure time (transfers only)                  |
| `failure_reason`   | string | Failure reason (transfers only)                |
| `expires_at`       | string | Expiration time (deposits only)                |

**Example:**

```bash
curl https://api.settla.io/v1/transactions/verify/b2c3d4e5-f6a7-8901-bcde-f12345678901 \
  -H "Authorization: Bearer sk_live_abc123"
```

**Error Codes:**

| HTTP | Code       | Description                                     |
|------|------------|-------------------------------------------------|
| 404  | `NOT_FOUND`| No transfer or deposit found with this ID       |

---

### GET /v1/transactions/lookup -- Look Up Transaction

Look up a transaction by ID, external reference, or on-chain transaction hash. Exactly one query parameter must be provided.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter   | Type   | Required | Description                               |
|-------------|--------|----------|-------------------------------------------|
| `id`        | string | One of   | Transfer or deposit UUID                  |
| `reference` | string | One of   | External reference (transfers only)       |
| `tx_hash`   | string | One of   | On-chain transaction hash (deposits only) |
| `chain`     | string | No       | Blockchain for tx_hash lookup (default: tron) |

**Response (200):** Same schema as Verify Transaction.

**Example:**

```bash
curl "https://api.settla.io/v1/transactions/lookup?reference=INV-12345" \
  -H "Authorization: Bearer sk_live_abc123"
```

**Error Codes:**

| HTTP | Code          | Description                                   |
|------|---------------|-----------------------------------------------|
| 400  | `BAD_REQUEST` | No lookup key or multiple keys provided       |
| 404  | `NOT_FOUND`   | No matching transaction found                 |

---

## Analytics

### GET /v1/analytics/transfers -- Transfer Analytics

Get aggregated transfer analytics including corridor breakdown, status distribution, and latency percentiles.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description                    |
|-----------|--------|---------|--------------------------------|
| `period`  | string | `7d`    | Time period: `24h`, `7d`, `30d`, `90d` |

**Response (200):**

| Field              | Type    | Description                                 |
|--------------------|---------|---------------------------------------------|
| `corridors`        | array   | Per-corridor breakdown                      |
| `statuses`         | array   | Per-status counts                           |
| `total_count`      | integer | Total transfers in period                   |
| `total_volume_usd` | string  | Total volume in USD                         |
| `total_fees_usd`   | string  | Total fees in USD                           |
| `sample_count`     | integer | Number of latency samples                   |
| `p50_ms`           | integer | 50th percentile latency in ms               |
| `p90_ms`           | integer | 90th percentile latency in ms               |
| `p95_ms`           | integer | 95th percentile latency in ms               |
| `p99_ms`           | integer | 99th percentile latency in ms               |

Each corridor object:

| Field             | Type    | Description                        |
|-------------------|---------|------------------------------------|
| `source_currency` | string  | Source currency code               |
| `dest_currency`   | string  | Destination currency code          |
| `transfer_count`  | integer | Number of transfers                |
| `volume_usd`      | string  | Volume in USD                      |
| `fees_usd`        | string  | Fees in USD                        |
| `completed`       | integer | Completed count                    |
| `failed`          | integer | Failed count                       |
| `success_rate`    | number  | Success rate (0-1)                 |
| `avg_latency_ms`  | integer | Average latency in ms              |

**Example:**

```bash
curl "https://api.settla.io/v1/analytics/transfers?period=7d" \
  -H "Authorization: Bearer sk_live_abc123"
```

---

### GET /v1/analytics/fees -- Fee Analytics

Get fee analytics broken down by corridor.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description                    |
|-----------|--------|---------|--------------------------------|
| `period`  | string | `7d`    | Time period: `24h`, `7d`, `30d`, `90d` |

**Response (200):**

| Field            | Type   | Description                           |
|------------------|--------|---------------------------------------|
| `entries`        | array  | Per-corridor fee breakdown            |
| `total_fees_usd` | string | Total fees across all corridors      |

Each entry:

| Field               | Type    | Description                  |
|---------------------|---------|------------------------------|
| `source_currency`   | string  | Source currency code         |
| `dest_currency`     | string  | Destination currency code    |
| `transfer_count`    | integer | Number of transfers          |
| `volume_usd`        | string  | Volume in USD                |
| `on_ramp_fees_usd`  | string  | On-ramp fees in USD          |
| `off_ramp_fees_usd` | string  | Off-ramp fees in USD         |
| `network_fees_usd`  | string  | Network fees in USD          |
| `total_fees_usd`    | string  | Total fees in USD            |

---

### GET /v1/analytics/providers -- Provider Analytics

Get provider performance analytics.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description                    |
|-----------|--------|---------|--------------------------------|
| `period`  | string | `7d`    | Time period: `24h`, `7d`, `30d`, `90d` |

**Response (200):**

| Field       | Type  | Description                         |
|-------------|-------|-------------------------------------|
| `providers` | array | Per-provider performance metrics    |

Each provider object:

| Field                | Type    | Description                    |
|----------------------|---------|--------------------------------|
| `provider`           | string  | Provider name                  |
| `source_currency`    | string  | Source currency code           |
| `dest_currency`      | string  | Destination currency code      |
| `transaction_count`  | integer | Number of transactions         |
| `completed`          | integer | Completed count                |
| `failed`             | integer | Failed count                   |
| `success_rate`       | number  | Success rate (0-1)             |
| `avg_settlement_ms`  | integer | Average settlement time in ms  |
| `total_volume`       | string  | Total volume                   |

---

### GET /v1/analytics/reconciliation -- Reconciliation Analytics

Get reconciliation health metrics.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field                | Type    | Description                           |
|----------------------|---------|---------------------------------------|
| `total_runs`         | integer | Total reconciliation runs             |
| `checks_passed`      | integer | Total checks passed                   |
| `checks_failed`      | integer | Total checks failed                   |
| `pass_rate`          | number  | Pass rate (0-1)                       |
| `last_run_at`        | string  | ISO 8601 UTC time of last run         |
| `needs_review_count` | integer | Items needing manual review           |

---

### GET /v1/analytics/deposits -- Deposit Analytics

Get deposit analytics for both crypto and bank deposits.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description                    |
|-----------|--------|---------|--------------------------------|
| `period`  | string | `7d`    | Time period: `24h`, `7d`, `30d`, `90d` |

**Response (200):**

| Field    | Type   | Description                      |
|----------|--------|----------------------------------|
| `crypto` | object | Crypto deposit analytics         |
| `bank`   | object | Bank deposit analytics           |

Each analytics object:

| Field                | Type    | Description                     |
|----------------------|---------|---------------------------------|
| `total_sessions`     | integer | Total sessions created          |
| `completed_sessions` | integer | Successfully completed          |
| `expired_sessions`   | integer | Expired without payment         |
| `failed_sessions`    | integer | Failed sessions                 |
| `conversion_rate`    | number  | Completion rate (0-1)           |
| `total_received`     | string  | Total amount received           |
| `total_fees`         | string  | Total fees charged              |
| `total_net`          | string  | Net amount after fees           |

---

### POST /v1/analytics/export -- Create Analytics Export

Create an asynchronous analytics export job. Results are available for download once the job completes.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field         | Type   | Required | Description                                    | Constraints                    |
|---------------|--------|----------|------------------------------------------------|--------------------------------|
| `export_type` | string | Yes      | Type of data to export                         | `transfers`, `fees`, `providers`, `deposits` |
| `period`      | string | No       | Time period                                    | `24h`, `7d`, `30d`, `90d` (default: `7d`) |
| `format`      | string | No       | Export format                                  | `csv`, `json` (default: `csv`) |

**Response (201):**

| Field | Type   | Description                  |
|-------|--------|------------------------------|
| `job` | object | ExportJob object             |

ExportJob object:

| Field                 | Type    | Description                          |
|-----------------------|---------|--------------------------------------|
| `id`                  | string  | Job UUID                             |
| `status`              | string  | Job status: `pending`, `processing`, `completed`, `failed` |
| `export_type`         | string  | Type of export                       |
| `row_count`           | integer | Number of rows exported              |
| `download_url`        | string  | URL to download results              |
| `download_expires_at` | string  | Download link expiration time        |
| `error_message`       | string  | Error message if failed              |
| `created_at`          | string  | Job creation time                    |
| `completed_at`        | string  | Job completion time                  |

---

### GET /v1/analytics/export/{jobId} -- Get Export Job

Get the status of an analytics export job.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description    |
|-----------|--------|----------|----------------|
| `jobId`   | string | Yes      | Export job UUID |

**Response (200):**

| Field | Type   | Description          |
|-------|--------|----------------------|
| `job` | object | ExportJob object     |

**Error Codes:**

| HTTP | Code       | Description          |
|------|------------|----------------------|
| 404  | `NOT_FOUND`| Export job not found |

---

## Tenant Portal

These endpoints support tenant self-service via the portal UI.

### GET /v1/me -- Get Tenant Profile

Retrieve the authenticated tenant's profile including fee schedule and limits.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field               | Type   | Description                                 |
|---------------------|--------|---------------------------------------------|
| `id`                | string | Tenant UUID                                 |
| `name`              | string | Company name                                |
| `slug`              | string | URL-safe identifier                         |
| `status`            | string | Tenant status: `active`, `pending`, `suspended` |
| `settlement_model`  | string | Settlement model                            |
| `kyb_status`        | string | KYB verification status: `not_started`, `pending`, `approved`, `rejected` |
| `kyb_verified_at`   | string | ISO 8601 UTC KYB approval time              |
| `fee_schedule`      | object | Fee schedule details                        |
| `fee_schedule.on_ramp_bps`  | integer | On-ramp fee in basis points        |
| `fee_schedule.off_ramp_bps` | integer | Off-ramp fee in basis points       |
| `fee_schedule.min_fee_usd`  | string  | Minimum fee in USD                 |
| `fee_schedule.max_fee_usd`  | string  | Maximum fee in USD                 |
| `daily_limit_usd`   | string | Daily volume limit in USD                   |
| `per_transfer_limit` | string | Per-transfer amount limit                  |
| `webhook_url`       | string | Configured webhook URL                      |
| `created_at`        | string | ISO 8601 UTC creation time                  |
| `updated_at`        | string | ISO 8601 UTC last update time               |

**Example:**

```bash
curl https://api.settla.io/v1/me \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIs..."
```

---

### PUT /v1/me/webhooks -- Update Webhook Config

Update the tenant's webhook URL. Returns a new webhook signing secret.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field         | Type   | Required | Description        |
|---------------|--------|----------|--------------------|
| `webhook_url` | string | Yes      | Webhook endpoint URL |

**Response (200):**

| Field            | Type   | Description                              |
|------------------|--------|------------------------------------------|
| `webhook_url`    | string | Configured webhook URL                   |
| `webhook_secret` | string | HMAC-SHA256 signing secret for verification |

---

### GET /v1/me/api-keys -- List API Keys

List all API keys for the authenticated tenant.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field  | Type  | Description                 |
|--------|-------|-----------------------------|
| `keys` | array | Array of APIKey objects      |

Each APIKey object:

| Field          | Type    | Description                      |
|----------------|---------|----------------------------------|
| `id`           | string  | Key UUID                         |
| `key_prefix`   | string  | First characters of the key      |
| `environment`  | string  | `live` or `test`                 |
| `name`         | string  | Human-readable key name          |
| `is_active`    | boolean | Whether the key is active        |
| `last_used_at` | string  | Last usage time                  |
| `expires_at`   | string  | Expiration time                  |
| `created_at`   | string  | Creation time                    |

---

### POST /v1/me/api-keys -- Create API Key

Generate a new API key for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field         | Type   | Required | Description            | Constraints         |
|---------------|--------|----------|------------------------|---------------------|
| `environment` | string | Yes      | Key environment        | `live` or `test`    |
| `name`        | string | No       | Human-readable name    | Max 255 characters  |

**Response (201):**

| Field     | Type   | Description                                      |
|-----------|--------|--------------------------------------------------|
| `key`     | object | APIKey object (metadata)                         |
| `raw_key` | string | Full API key (shown only once, store securely)   |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/me/api-keys \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIs..." \
  -H "Content-Type: application/json" \
  -d '{"environment": "live", "name": "Production Key"}'
```

```json
{
  "key": {
    "id": "k1k2k3k4-k5k6-k7k8-k9ka-kbkckdkekfk0",
    "key_prefix": "sk_live_abc",
    "environment": "live",
    "name": "Production Key",
    "is_active": true,
    "created_at": "2025-01-15T14:30:00.000Z"
  },
  "raw_key": "sk_live_EXAMPLE_KEY_DO_NOT_USE_000000000000"
}
```

---

### DELETE /v1/me/api-keys/{keyId} -- Revoke API Key

Permanently revoke an API key. The key is immediately invalidated across all gateway instances.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `keyId`   | string | Yes      | Key UUID    |

**Response:** 204 No Content

**Error Codes:**

| HTTP | Code       | Description        |
|------|------------|--------------------|
| 404  | `NOT_FOUND`| API key not found  |

---

### POST /v1/me/api-keys/{keyId}/rotate -- Rotate API Key

Rotate an API key, revoking the old one and generating a new key in one atomic operation.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter | Type   | Required | Description    |
|-----------|--------|----------|----------------|
| `keyId`   | string | Yes      | Old key UUID   |

**Request Body:**

| Field  | Type   | Required | Description              |
|--------|--------|----------|--------------------------|
| `name` | string | No       | Name for the new key     |

**Response (200):**

| Field     | Type   | Description                          |
|-----------|--------|--------------------------------------|
| `key`     | object | New APIKey object                    |
| `raw_key` | string | New full API key (store securely)    |

---

### GET /v1/me/dashboard -- Get Dashboard Metrics

Get dashboard overview metrics for the tenant portal.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field               | Type    | Description                        |
|---------------------|---------|------------------------------------|
| `transfers_today`   | integer | Transfers created today            |
| `volume_today_usd`  | string  | Volume today in USD                |
| `completed_today`   | integer | Completed transfers today          |
| `failed_today`      | integer | Failed transfers today             |
| `transfers_7d`      | integer | Transfers in last 7 days           |
| `volume_7d_usd`     | string  | Volume in last 7 days              |
| `fees_7d_usd`       | string  | Fees in last 7 days                |
| `transfers_30d`     | integer | Transfers in last 30 days          |
| `volume_30d_usd`    | string  | Volume in last 30 days             |
| `fees_30d_usd`      | string  | Fees in last 30 days               |
| `success_rate_30d`  | number  | 30-day success rate (0-1)          |
| `daily_limit_usd`   | string  | Configured daily limit             |
| `daily_usage_usd`   | string  | Amount used toward daily limit     |

---

### GET /v1/me/transfers/stats -- Get Transfer Stats

Get time-series transfer statistics with configurable granularity.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter     | Type   | Default | Description                            |
|---------------|--------|---------|----------------------------------------|
| `period`      | string | `24h`   | Time period: `24h`, `7d`, `30d`        |
| `granularity` | string | `hour`  | Bucket size: `hour`, `day`, `week`     |

**Response (200):**

| Field     | Type  | Description                     |
|-----------|-------|---------------------------------|
| `buckets` | array | Time-series data points         |

Each bucket:

| Field        | Type    | Description                 |
|--------------|---------|-----------------------------|
| `timestamp`  | string  | Bucket start time           |
| `total`      | integer | Total transfers             |
| `completed`  | integer | Completed transfers         |
| `failed`     | integer | Failed transfers            |
| `volume_usd` | string  | Volume in USD               |
| `fees_usd`   | string  | Fees in USD                 |

---

### GET /v1/me/fees/report -- Get Fee Breakdown

Get detailed fee breakdown by corridor for a time range.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description                     |
|-----------|--------|---------|---------------------------------|
| `from`    | string | -       | Start time (ISO 8601 date-time) |
| `to`      | string | -       | End time (ISO 8601 date-time)   |

**Response (200):**

| Field            | Type   | Description                    |
|------------------|--------|--------------------------------|
| `entries`        | array  | Per-corridor fee entries       |
| `total_fees_usd` | string | Total fees across all corridors|

Each entry:

| Field               | Type    | Description                  |
|---------------------|---------|------------------------------|
| `source_currency`   | string  | Source currency code         |
| `dest_currency`     | string  | Destination currency code    |
| `transfer_count`    | integer | Number of transfers          |
| `total_volume_usd`  | string  | Volume in USD                |
| `on_ramp_fees_usd`  | string  | On-ramp fees                 |
| `off_ramp_fees_usd` | string  | Off-ramp fees                |
| `network_fees_usd`  | string  | Network fees                 |
| `total_fees_usd`    | string  | Total fees                   |

---

### GET /v1/me/analytics/status-distribution -- Status Distribution

Get transfer status distribution for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description    |
|-----------|--------|---------|----------------|
| `period`  | string | `7d`    | Time period    |

**Response (200):**

| Field      | Type  | Description                           |
|------------|-------|---------------------------------------|
| `statuses` | array | Array of `{status, count}` objects    |

---

### GET /v1/me/analytics/corridors -- Corridor Metrics

Get per-corridor transfer metrics for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description    |
|-----------|--------|---------|----------------|
| `period`  | string | `7d`    | Time period    |

**Response (200):**

| Field       | Type  | Description                              |
|-------------|-------|------------------------------------------|
| `corridors` | array | Array of corridor metric objects (same schema as Transfer Analytics corridors) |

---

### GET /v1/me/analytics/latency -- Latency Percentiles

Get transfer latency percentiles for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description    |
|-----------|--------|---------|----------------|
| `period`  | string | `7d`    | Time period    |

**Response (200):**

| Field          | Type    | Description                   |
|----------------|---------|-------------------------------|
| `sample_count` | integer | Number of latency samples     |
| `p50_ms`       | integer | 50th percentile latency in ms |
| `p90_ms`       | integer | 90th percentile latency in ms |
| `p95_ms`       | integer | 95th percentile latency in ms |
| `p99_ms`       | integer | 99th percentile latency in ms |

---

### GET /v1/me/analytics/comparison -- Volume Comparison

Get current vs. previous period volume comparison for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description    |
|-----------|--------|---------|----------------|
| `period`  | string | `7d`    | Time period    |

**Response (200):**

| Field                  | Type    | Description                      |
|------------------------|---------|----------------------------------|
| `current_count`        | integer | Transfers in current period      |
| `current_volume_usd`   | string  | Volume in current period         |
| `current_fees_usd`     | string  | Fees in current period           |
| `previous_count`       | integer | Transfers in previous period     |
| `previous_volume_usd`  | string  | Volume in previous period        |
| `previous_fees_usd`    | string  | Fees in previous period          |

---

### GET /v1/me/analytics/activity -- Recent Activity

Get recent transfer activity for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type    | Default | Description               |
|-----------|---------|---------|---------------------------|
| `limit`   | integer | 20      | Number of items to return |

**Response (200):**

| Field   | Type  | Description                    |
|---------|-------|--------------------------------|
| `items` | array | Array of recent activity items |

Each item:

| Field             | Type   | Description                 |
|-------------------|--------|-----------------------------|
| `transfer_id`     | string | Transfer UUID               |
| `external_ref`    | string | External reference          |
| `status`          | string | Current status              |
| `source_currency` | string | Source currency              |
| `source_amount`   | string | Source amount                |
| `dest_currency`   | string | Destination currency        |
| `dest_amount`     | string | Destination amount          |
| `updated_at`      | string | Last update time            |
| `failure_reason`  | string | Failure reason if applicable|

---

### GET /v1/me/webhooks/deliveries -- List Webhook Deliveries

List outbound webhook delivery history for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter     | Type    | Default | Description                            |
|---------------|---------|---------|----------------------------------------|
| `event_type`  | string  | -       | Filter by event type                   |
| `status`      | string  | -       | Filter by status: `pending`, `delivered`, `failed`, `dead_letter` |
| `page_size`   | integer | 50      | Items per page                         |
| `page_offset` | integer | 0       | Items to skip                          |

**Response (200):**

| Field          | Type    | Description                             |
|----------------|---------|-----------------------------------------|
| `deliveries`   | array   | Array of WebhookDelivery objects        |
| `total_count`  | integer | Total matching deliveries               |

---

### GET /v1/me/webhooks/deliveries/{deliveryId} -- Get Webhook Delivery

Get details of a specific webhook delivery including the request body.

**Authentication:** Bearer token (API key or JWT)

**Path Parameters:**

| Parameter    | Type   | Required | Description      |
|--------------|--------|----------|------------------|
| `deliveryId` | string | Yes      | Delivery UUID    |

**Response (200):**

| Field          | Type   | Description                        |
|----------------|--------|------------------------------------|
| `delivery`     | object | WebhookDelivery object             |
| `request_body` | object | The JSON payload that was sent     |

---

### GET /v1/me/webhooks/stats -- Get Webhook Stats

Get aggregated webhook delivery statistics.

**Authentication:** Bearer token (API key or JWT)

**Query Parameters:**

| Parameter | Type   | Default | Description    |
|-----------|--------|---------|----------------|
| `period`  | string | `24h`   | Time period    |

**Response (200):**

| Field               | Type    | Description                    |
|---------------------|---------|--------------------------------|
| `total_deliveries`  | integer | Total delivery attempts        |
| `successful`        | integer | Successful deliveries          |
| `failed`            | integer | Failed deliveries              |
| `dead_lettered`     | integer | Moved to dead letter queue     |
| `pending`           | integer | Pending delivery               |
| `avg_latency_ms`    | integer | Average delivery latency in ms |
| `p95_latency_ms`    | integer | 95th percentile latency in ms  |

---

### GET /v1/me/webhooks/subscriptions -- Get Event Subscriptions

List webhook event type subscriptions for the tenant.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field                     | Type  | Description                             |
|---------------------------|-------|-----------------------------------------|
| `subscriptions`           | array | Array of subscription objects           |
| `available_event_types`   | array | All possible event types                |

Each subscription:

| Field        | Type   | Description                |
|--------------|--------|----------------------------|
| `id`         | string | Subscription UUID          |
| `event_type` | string | Event type name            |
| `created_at` | string | Subscription creation time |

---

### PUT /v1/me/webhooks/subscriptions -- Update Event Subscriptions

Update which webhook event types the tenant is subscribed to.

**Authentication:** Bearer token (API key or JWT)

**Request Body:**

| Field         | Type  | Required | Description                    |
|---------------|-------|----------|--------------------------------|
| `event_types` | array | Yes      | Array of event type strings    |

**Response (200):**

| Field           | Type  | Description                       |
|-----------------|-------|-----------------------------------|
| `subscriptions` | array | Updated array of subscriptions    |

---

### POST /v1/me/webhooks/test -- Send Test Webhook

Send a test webhook to the tenant's configured endpoint to verify connectivity.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field         | Type    | Description                         |
|---------------|---------|-------------------------------------|
| `success`     | boolean | Whether the webhook was delivered   |
| `status_code` | integer | HTTP status code from the endpoint  |
| `duration_ms` | integer | Round-trip time in milliseconds     |
| `error`       | string  | Error message if delivery failed    |

---

### GET /v1/portal/crypto-settings -- Get Crypto Settings

Get the tenant's crypto deposit configuration.

**Authentication:** Bearer token (API key or JWT)

**Response (200):**

| Field                        | Type    | Description                              |
|------------------------------|---------|------------------------------------------|
| `crypto_enabled`             | boolean | Whether crypto deposits are enabled      |
| `supported_chains`           | array   | List of supported blockchain names       |
| `default_settlement_pref`    | string  | Default settlement preference            |
| `payment_tolerance_bps`      | number  | Payment amount tolerance in basis points |
| `default_session_ttl_secs`   | number  | Default deposit session TTL in seconds   |
| `min_confirmations_tron`     | number  | Required confirmations on Tron           |
| `min_confirmations_eth`      | number  | Required confirmations on Ethereum       |
| `min_confirmations_base`     | number  | Required confirmations on Base           |

---

### POST /v1/portal/crypto-settings -- Update Crypto Settings

Update the tenant's crypto deposit configuration.

**Authentication:** Bearer token (API key or JWT)

**Request Body:** Same fields as the GET response (all optional).

**Response (200):** Updated crypto settings object.

---

## Auth

### POST /v1/auth/register -- Register Tenant

Register a new tenant account. Sends a verification email.

**Authentication:** None (public endpoint)

**Request Body:**

| Field          | Type   | Required | Description              | Constraints       |
|----------------|--------|----------|--------------------------|--------------------|
| `company_name` | string | Yes      | Company name             | Non-empty string   |
| `email`        | string | Yes      | Email address            | Valid email format |
| `password`     | string | Yes      | Password                 | Minimum 8 characters |
| `display_name` | string | No       | Display name for the user|                    |

**Response (201):**

| Field       | Type   | Description                          |
|-------------|--------|--------------------------------------|
| `tenant_id` | string | New tenant UUID                      |
| `user_id`   | string | New user UUID                        |
| `email`     | string | Registered email                     |
| `message`   | string | Confirmation message                 |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "company_name": "Acme Fintech",
    "email": "admin@acmefintech.com",
    "password": "secureP@ss123",
    "display_name": "Admin User"
  }'
```

```json
{
  "tenant_id": "d4e5f6a7-b8c9-0123-def0-123456789abc",
  "user_id": "e5f6a7b8-c9d0-1234-ef56-789012345678",
  "email": "admin@acmefintech.com",
  "message": "Registration successful. Please verify your email."
}
```

**Error Codes:**

| HTTP | Code                    | Description                 |
|------|-------------------------|-----------------------------|
| 400  | `EMAIL_ALREADY_EXISTS`  | Email already registered    |
| 400  | `SLUG_CONFLICT`         | Company slug already taken  |

---

### POST /v1/auth/login -- Login

Authenticate with email and password to receive JWT tokens.

**Authentication:** None (public endpoint)

**Rate Limits:** 10 requests/minute, 100 requests/hour per IP

**Request Body:**

| Field      | Type   | Required | Description    |
|------------|--------|----------|----------------|
| `email`    | string | Yes      | Email address  |
| `password` | string | Yes      | Password       |

**Response (200):**

| Field           | Type    | Description                           |
|-----------------|---------|---------------------------------------|
| `access_token`  | string  | JWT access token                      |
| `refresh_token` | string  | JWT refresh token                     |
| `expires_in`    | integer | Access token expiry in seconds        |
| `user`          | object  | Authenticated user details            |

User object:

| Field           | Type   | Description                  |
|-----------------|--------|------------------------------|
| `id`            | string | User UUID                    |
| `email`         | string | Email address                |
| `display_name`  | string | Display name                 |
| `role`          | string | User role                    |
| `tenant_id`     | string | Tenant UUID                  |
| `tenant_name`   | string | Company name                 |
| `tenant_slug`   | string | Tenant slug                  |
| `tenant_status` | string | Tenant status                |
| `kyb_status`    | string | KYB verification status      |

**Example:**

```bash
curl -X POST https://api.settla.io/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "admin@acmefintech.com",
    "password": "secureP@ss123"
  }'
```

**Error Codes:**

| HTTP | Code                  | Description                     |
|------|-----------------------|---------------------------------|
| 401  | `INVALID_CREDENTIALS` | Wrong email or password         |
| 401  | `EMAIL_NOT_VERIFIED`  | Email verification required     |
| 429  | `TOO_MANY_REQUESTS`   | Login rate limit exceeded       |

---

### POST /v1/auth/verify-email -- Verify Email

Verify the email address using the token from the verification email.

**Authentication:** None (public endpoint)

**Request Body:**

| Field   | Type   | Required | Description            |
|---------|--------|----------|------------------------|
| `token` | string | Yes      | Verification token     |

**Response (200):**

| Field     | Type   | Description              |
|-----------|--------|--------------------------|
| `message` | string | Confirmation message     |

**Error Codes:**

| HTTP | Code            | Description                 |
|------|-----------------|-----------------------------|
| 400  | `TOKEN_EXPIRED` | Verification token expired  |

---

### POST /v1/auth/refresh -- Refresh Token

Obtain a new access token using a refresh token.

**Authentication:** None (public endpoint)

**Request Body:**

| Field           | Type   | Required | Description       |
|-----------------|--------|----------|-------------------|
| `refresh_token` | string | Yes      | Refresh token     |

**Response (200):**

| Field          | Type    | Description                    |
|----------------|---------|--------------------------------|
| `access_token` | string  | New JWT access token           |
| `expires_in`   | integer | Token expiry in seconds        |

**Error Codes:**

| HTTP | Code            | Description              |
|------|-----------------|--------------------------|
| 401  | `TOKEN_EXPIRED` | Refresh token expired    |

---

### POST /v1/me/kyb -- Submit KYB Verification

Submit Know Your Business (KYB) verification documents.

**Authentication:** Bearer token (JWT required)

**Request Body:**

| Field                           | Type   | Required | Description                         | Constraints                                     |
|---------------------------------|--------|----------|-------------------------------------|-------------------------------------------------|
| `company_registration_number`   | string | Yes      | Company registration/incorporation number | Non-empty string                           |
| `country`                       | string | Yes      | Country of registration             | 2-letter ISO code                               |
| `business_type`                 | string | Yes      | Type of business                    | `fintech`, `bank`, `payment_processor`, `other` |
| `contact_name`                  | string | Yes      | Primary contact name                | Non-empty string                                |
| `contact_email`                 | string | Yes      | Primary contact email               | Valid email format                              |
| `contact_phone`                 | string | No       | Primary contact phone               |                                                 |

**Response (200):**

| Field        | Type   | Description                        |
|--------------|--------|------------------------------------|
| `message`    | string | Confirmation message               |
| `kyb_status` | string | Updated KYB status (`pending`)     |

---

### POST /v1/admin/tenants/{tenantId}/approve-kyb -- Approve KYB (Admin)

Approve a tenant's KYB verification. Requires admin role.

**Authentication:** Bearer token (JWT with admin role)

**Path Parameters:**

| Parameter  | Type   | Required | Description    |
|------------|--------|----------|----------------|
| `tenantId` | string | Yes      | Tenant UUID    |

**Response (200):**

| Field           | Type   | Description                    |
|-----------------|--------|--------------------------------|
| `message`       | string | Confirmation message           |
| `tenant_status` | string | Updated tenant status          |
| `kyb_status`    | string | Updated KYB status (`approved`)|

**Error Codes:**

| HTTP | Code        | Description                     |
|------|-------------|---------------------------------|
| 403  | `FORBIDDEN` | Requires admin role             |

---

## Ops (Internal)

All ops endpoints require the `X-Ops-Api-Key` header. These are used by the Settla ops dashboard.

### GET /v1/ops/tenants -- List Tenants

List all registered tenants.

**Authentication:** Ops key

**Query Parameters:**

| Parameter | Type   | Default | Description            |
|-----------|--------|---------|------------------------|
| `limit`   | string | `50`    | Items per page         |
| `offset`  | string | `0`     | Items to skip          |

**Response (200):** Array of tenant objects.

---

### GET /v1/ops/tenants/{id} -- Get Tenant

Get details of a specific tenant by ID or slug.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description         |
|-----------|--------|----------|---------------------|
| `id`      | string | Yes      | Tenant UUID or slug |

**Response (200):** Tenant object.

---

### POST /v1/ops/tenants/{id}/status -- Update Tenant Status

Update a tenant's status (activate, suspend, etc.).

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `id`      | string | Yes      | Tenant UUID |

**Request Body:**

| Field    | Type   | Required | Description                         |
|----------|--------|----------|-------------------------------------|
| `status` | string | Yes      | New status: `active`, `suspended`   |

---

### POST /v1/ops/tenants/{id}/kyb -- Update Tenant KYB

Update a tenant's KYB verification status.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `id`      | string | Yes      | Tenant UUID |

**Request Body:**

| Field        | Type   | Required | Description                                     |
|--------------|--------|----------|-------------------------------------------------|
| `kyb_status` | string | Yes      | New status: `approved`, `rejected`, `pending`   |

---

### POST /v1/ops/tenants/{id}/fees -- Update Tenant Fees

Update a tenant's fee schedule.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `id`      | string | Yes      | Tenant UUID |

**Request Body:**

| Field          | Type    | Required | Description                  |
|----------------|---------|----------|------------------------------|
| `on_ramp_bps`  | integer | Yes      | On-ramp fee in basis points  |
| `off_ramp_bps` | integer | Yes      | Off-ramp fee in basis points |
| `min_fee_usd`  | string  | Yes      | Minimum fee in USD           |
| `max_fee_usd`  | string  | Yes      | Maximum fee in USD           |

---

### POST /v1/ops/tenants/{id}/limits -- Update Tenant Limits

Update a tenant's transfer limits.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `id`      | string | Yes      | Tenant UUID |

**Request Body:**

| Field                | Type   | Required | Description               |
|----------------------|--------|----------|---------------------------|
| `daily_limit_usd`    | string | Yes      | Daily volume limit in USD |
| `per_transfer_limit`  | string | Yes      | Per-transfer limit        |

---

### GET /v1/ops/manual-reviews -- List Manual Reviews

List transfers flagged for manual review.

**Authentication:** Ops key

**Query Parameters:**

| Parameter | Type   | Default | Description              |
|-----------|--------|---------|--------------------------|
| `status`  | string | -       | Filter by review status  |

**Response (200):** Array of manual review objects.

---

### POST /v1/ops/manual-reviews/{id}/approve -- Approve Manual Review

Approve a flagged transfer.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description        | Constraints |
|-----------|--------|----------|--------------------|-------------|
| `id`      | string | Yes      | Review/transfer UUID | UUID format |

**Request Body:**

| Field   | Type   | Required | Description       |
|---------|--------|----------|-------------------|
| `notes` | string | No       | Approval notes    |

---

### POST /v1/ops/manual-reviews/{id}/reject -- Reject Manual Review

Reject a flagged transfer.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description        | Constraints |
|-----------|--------|----------|--------------------|-------------|
| `id`      | string | Yes      | Review/transfer UUID | UUID format |

**Request Body:**

| Field   | Type   | Required | Description       |
|---------|--------|----------|-------------------|
| `notes` | string | No       | Rejection notes   |

---

### GET /v1/ops/reconciliation/latest -- Get Latest Reconciliation

Get the latest reconciliation report.

**Authentication:** Ops key

**Response (200):** Reconciliation report object.

---

### POST /v1/ops/reconciliation/run -- Trigger Reconciliation

Trigger an ad-hoc reconciliation run.

**Authentication:** Ops key

**Response (200):** Reconciliation result object.

---

### GET /v1/ops/dlq/stats -- Get DLQ Stats

Get dead letter queue statistics.

**Authentication:** Ops key

**Response (200):** DLQ statistics object.

---

### GET /v1/ops/dlq/messages -- List DLQ Messages

List messages in the dead letter queue.

**Authentication:** Ops key

**Query Parameters:**

| Parameter | Type   | Default | Description            |
|-----------|--------|---------|------------------------|
| `limit`   | string | `50`    | Items to return        |

**Response (200):** Array of DLQ message objects.

---

### POST /v1/ops/dlq/messages/{id}/replay -- Replay DLQ Message

Replay a dead-lettered message back to its original stream.

**Authentication:** Ops key

**Path Parameters:**

| Parameter | Type   | Required | Description     | Constraints |
|-----------|--------|----------|-----------------|-------------|
| `id`      | string | Yes      | Message UUID    | UUID format |

---

### GET /v1/ops/settlements/report -- Get Settlement Report

Get the settlement report for net settlement positions.

**Authentication:** Ops key

**Query Parameters:**

| Parameter | Type   | Default | Description    |
|-----------|--------|---------|----------------|
| `period`  | string | -       | Report period  |

**Response (200):** Settlement report object.

---

### POST /v1/ops/settlements/{tenantId}/mark-paid -- Mark Settlement Paid

Mark a tenant's net settlement as paid.

**Authentication:** Ops key

**Path Parameters:**

| Parameter  | Type   | Required | Description | Constraints |
|------------|--------|----------|-------------|-------------|
| `tenantId` | string | Yes      | Tenant UUID | UUID format |

**Request Body:**

| Field         | Type   | Required | Description            |
|---------------|--------|----------|------------------------|
| `payment_ref` | string | No       | Payment reference      |

---

## Webhooks Inbound

### POST /webhooks/providers/{providerId} -- Receive Provider Webhook

Receive and process an inbound webhook from a payment provider. Validates HMAC-SHA256 signature, deduplicates, normalizes, and publishes to the internal event bus.

**Authentication:** HMAC-SHA256 signature

**Path Parameters:**

| Parameter    | Type   | Required | Description            |
|--------------|--------|----------|------------------------|
| `providerId` | string | Yes      | Provider identifier    |

**Request Headers:**

| Header                 | Type   | Required | Description                                |
|------------------------|--------|----------|--------------------------------------------|
| `X-Webhook-Signature`  | string | Yes      | HMAC-SHA256 hex digest of the request body |
| `X-Webhook-Timestamp`  | string | No       | Unix timestamp for replay protection       |
| `Content-Type`         | string | Yes      | Must be `application/json`                 |

**Response (200):**

| Field    | Type   | Description                                   |
|----------|--------|-----------------------------------------------|
| `status` | string | `accepted` or `duplicate`                     |

**Error Codes:**

| HTTP | Code                    | Description                          |
|------|-------------------------|--------------------------------------|
| 401  | `missing signature`     | Signature header not present         |
| 401  | `invalid signature`     | HMAC validation failed               |
| 401  | `timestamp_expired`     | Timestamp older than 5 minutes       |
| 403  | `provider not configured`| No webhook secret for this provider |
| 429  | `rate_limit_exceeded`   | Webhook rate limit exceeded          |
| 503  | `event_bus_unavailable` | NATS unavailable                     |

---

## Health

### GET /health -- Liveness Probe

Simple liveness check. Always returns OK if the process is running. Used by Kubernetes to detect deadlocked processes.

**Authentication:** None

**Response (200):**

```json
{
  "status": "ok"
}
```

---

### GET /ready -- Readiness Probe

Readiness check that verifies connectivity to critical dependencies (gRPC backend, Redis). Returns 503 if any dependency is unavailable.

**Authentication:** None

**Response (200):**

```json
{
  "status": "ok",
  "checks": {
    "grpc": {"status": "ok", "detail": "READY"},
    "redis": {"status": "ok"}
  }
}
```

**Response (503):**

```json
{
  "status": "not_ready",
  "checks": {
    "grpc": {"status": "error", "detail": "CONNECTING"},
    "redis": {"status": "ok"}
  }
}
```

---

## Data Types

### Transfer

| Field                     | Type    | Description                                                |
|---------------------------|---------|------------------------------------------------------------|
| `id`                      | string  | UUID                                                       |
| `tenant_id`               | string  | Tenant UUID                                                |
| `external_ref`            | string  | Client-provided external reference                         |
| `idempotency_key`         | string  | Idempotency key used to create this transfer               |
| `status`                  | string  | Current status (see Transfer States below)                 |
| `version`                 | integer | Optimistic lock version number                             |
| `source_currency`         | string  | Source currency code (e.g., NGN, USD, GBP)                 |
| `source_amount`           | string  | Source amount (decimal string)                             |
| `dest_currency`           | string  | Destination currency code                                  |
| `dest_amount`             | string  | Destination amount after FX and fees                       |
| `stable_coin`             | string  | Stablecoin used for settlement (USDT, USDC)                |
| `stable_amount`           | string  | Amount converted to stablecoin                             |
| `chain`                   | string  | Blockchain used (tron, ethereum, base, etc.)               |
| `fx_rate`                 | string  | Applied FX rate                                            |
| `fees`                    | object  | FeeBreakdown object                                       |
| `sender`                  | object  | Sender details (id, name, email, country)                  |
| `recipient`               | object  | Recipient details (name, account_number, sort_code, bank_name, country, iban) |
| `quote_id`                | string  | Quote UUID if a pre-obtained quote was used                |
| `blockchain_transactions` | array   | Array of on-chain transaction records                      |
| `created_at`              | string  | ISO 8601 UTC creation timestamp                            |
| `updated_at`              | string  | ISO 8601 UTC last update timestamp                         |
| `funded_at`               | string  | ISO 8601 UTC timestamp when treasury funds were reserved   |
| `completed_at`            | string  | ISO 8601 UTC timestamp when transfer completed             |
| `failed_at`               | string  | ISO 8601 UTC timestamp when transfer failed                |
| `failure_reason`          | string  | Human-readable failure description                         |
| `failure_code`            | string  | Machine-readable failure code                              |

### Quote

| Field            | Type   | Description                                         |
|------------------|--------|-----------------------------------------------------|
| `id`             | string | UUID                                                |
| `tenant_id`      | string | Tenant UUID                                         |
| `source_currency`| string | Source currency code                                |
| `source_amount`  | string | Source amount (decimal string)                      |
| `dest_currency`  | string | Destination currency code                           |
| `dest_amount`    | string | Estimated destination amount after fees             |
| `fx_rate`        | string | Applied FX rate                                     |
| `fees`           | object | FeeBreakdown object                                 |
| `route`          | object | Route details (chain, stable_coin, providers, etc.) |
| `expires_at`     | string | ISO 8601 UTC quote expiration time                  |
| `created_at`     | string | ISO 8601 UTC creation time                          |

### FeeBreakdown

| Field            | Type   | Description                        |
|------------------|--------|------------------------------------|
| `on_ramp_fee`    | string | Fee for fiat-to-stablecoin leg     |
| `network_fee`    | string | Blockchain network/gas fee         |
| `off_ramp_fee`   | string | Fee for stablecoin-to-fiat leg     |
| `total_fee_usd`  | string | Total fees in USD equivalent       |

### Position

| Field       | Type   | Description                                    |
|-------------|--------|------------------------------------------------|
| `tenantId`  | string | Tenant UUID                                    |
| `currency`  | string | Currency code (e.g., USDT, NGN, GBP)           |
| `location`  | string | Location identifier (e.g., tron, bank, ethereum)|
| `balance`   | string | Total balance (available + locked)              |
| `locked`    | string | Amount locked for in-flight transfers           |
| `available` | string | Amount available for new transfers              |
| `updatedAt` | string | ISO 8601 UTC last update time                   |

### PositionTransaction

| Field           | Type   | Description                                 |
|-----------------|--------|---------------------------------------------|
| `id`            | string | UUID                                        |
| `tenantId`      | string | Tenant UUID                                 |
| `type`          | string | Transaction type (topup, withdrawal)        |
| `currency`      | string | Currency code                               |
| `location`      | string | Position location                           |
| `amount`        | string | Transaction amount                          |
| `status`        | string | Status (pending, completed, failed)         |
| `method`        | string | Method (bank_transfer, crypto, internal)    |
| `destination`   | string | Destination for withdrawals                 |
| `reference`     | string | Reference identifier                        |
| `failureReason` | string | Failure reason if applicable                |
| `createdAt`     | string | ISO 8601 UTC creation time                  |
| `updatedAt`     | string | ISO 8601 UTC last update time               |

### PositionEvent

| Field           | Type   | Description                                   |
|-----------------|--------|-----------------------------------------------|
| `id`            | string | UUID                                          |
| `positionId`    | string | Position UUID                                 |
| `tenantId`      | string | Tenant UUID                                   |
| `eventType`     | string | Event type (reserve, release, flush, topup)   |
| `amount`        | string | Amount involved                               |
| `balanceAfter`  | string | Position balance after event                  |
| `lockedAfter`   | string | Locked amount after event                     |
| `referenceId`   | string | Reference entity UUID (e.g., transfer ID)     |
| `referenceType` | string | Reference entity type (e.g., transfer)        |
| `recordedAt`    | string | ISO 8601 UTC event timestamp                  |

### DepositSession

| Field             | Type    | Description                                    |
|-------------------|---------|------------------------------------------------|
| `id`              | string  | UUID                                           |
| `tenantId`        | string  | Tenant UUID                                    |
| `status`          | string  | Current status (see Deposit States below)      |
| `version`         | integer | Optimistic lock version                        |
| `chain`           | string  | Blockchain (tron, ethereum, base, etc.)        |
| `token`           | string  | Token (USDT, USDC)                             |
| `depositAddress`  | string  | On-chain address to send payment to            |
| `expectedAmount`  | string  | Expected deposit amount                        |
| `receivedAmount`  | string  | Amount received so far                         |
| `settlementPref`  | string  | Settlement preference (AUTO_CONVERT, HOLD, THRESHOLD) |
| `currency`        | string  | Fiat currency for AUTO_CONVERT settlement      |
| `idempotencyKey`  | string  | Idempotency key                                |
| `expiresAt`       | string  | ISO 8601 UTC session expiration time           |
| `createdAt`       | string  | ISO 8601 UTC creation time                     |
| `updatedAt`       | string  | ISO 8601 UTC last update time                  |
| `transactions`    | array   | On-chain transactions detected                 |

### BankDepositSession

| Field                     | Type    | Description                                    |
|---------------------------|---------|------------------------------------------------|
| `id`                      | string  | UUID                                           |
| `tenantId`                | string  | Tenant UUID                                    |
| `status`                  | string  | Current status (see Bank Deposit States below) |
| `version`                 | integer | Optimistic lock version                        |
| `currency`                | string  | Fiat currency (NGN, GBP, USD, etc.)            |
| `expectedAmount`          | string  | Expected deposit amount                        |
| `receivedAmount`          | string  | Amount received from the bank                  |
| `virtualAccountNumber`    | string  | Virtual account number to pay into             |
| `virtualAccountBankName`  | string  | Bank name for the virtual account              |
| `virtualAccountBankCode`  | string  | Bank code                                      |
| `accountType`             | string  | PERMANENT or TEMPORARY                         |
| `mismatchPolicy`          | string  | ACCEPT or REJECT                               |
| `settlementPref`          | string  | Settlement preference                          |
| `idempotencyKey`          | string  | Idempotency key                                |
| `expiresAt`               | string  | ISO 8601 UTC session expiration time           |
| `createdAt`               | string  | ISO 8601 UTC creation time                     |
| `updatedAt`               | string  | ISO 8601 UTC last update time                  |

### PaymentLink

| Field           | Type    | Description                                    |
|-----------------|---------|------------------------------------------------|
| `id`            | string  | UUID                                           |
| `tenantId`      | string  | Tenant UUID                                    |
| `shortCode`     | string  | Short code for the payment URL                 |
| `amount`        | string  | Payment amount                                 |
| `currency`      | string  | Currency code                                  |
| `chain`         | string  | Blockchain for payment                         |
| `token`         | string  | Token to accept                                |
| `description`   | string  | Payment description                            |
| `redirectUrl`   | string  | Redirect URL after payment                     |
| `useLimit`      | integer | Maximum number of redemptions (0 = unlimited)  |
| `useCount`      | integer | Current number of redemptions                  |
| `isActive`      | boolean | Whether the link is active                     |
| `settlementPref`| string  | Settlement preference                          |
| `paymentUrl`    | string  | Full payment URL                               |
| `expiresAt`     | string  | ISO 8601 UTC expiration time                   |
| `createdAt`     | string  | ISO 8601 UTC creation time                     |

### Tenant

| Field               | Type    | Description                                 |
|---------------------|---------|---------------------------------------------|
| `id`                | string  | UUID                                        |
| `name`              | string  | Company name                                |
| `slug`              | string  | URL-safe identifier                         |
| `status`            | string  | `active`, `pending`, `suspended`            |
| `settlement_model`  | string  | Settlement model                            |
| `kyb_status`        | string  | `not_started`, `pending`, `approved`, `rejected` |
| `kyb_verified_at`   | string  | ISO 8601 UTC KYB approval time              |
| `fee_schedule`      | object  | FeeSchedule object                          |
| `daily_limit_usd`   | string  | Daily volume limit in USD                   |
| `per_transfer_limit` | string | Per-transfer amount limit                   |
| `webhook_url`       | string  | Configured webhook URL                      |
| `created_at`        | string  | ISO 8601 UTC creation time                  |
| `updated_at`        | string  | ISO 8601 UTC last update time               |

### TransferEvent

| Field         | Type   | Description                                  |
|---------------|--------|----------------------------------------------|
| `id`          | string | Event UUID                                   |
| `transfer_id` | string | Transfer UUID                               |
| `event_type`  | string | Event type (e.g., `transfer.created`)        |
| `from_status` | string | Previous status                              |
| `to_status`   | string | New status                                   |
| `metadata`    | object | Additional event-specific data               |
| `created_at`  | string | ISO 8601 UTC event timestamp                 |

### WebhookDelivery

| Field           | Type    | Description                                   |
|-----------------|---------|-----------------------------------------------|
| `id`            | string  | Delivery UUID                                 |
| `tenant_id`     | string  | Tenant UUID                                   |
| `event_type`    | string  | Event type that triggered the webhook         |
| `transfer_id`   | string  | Associated transfer UUID                      |
| `delivery_id`   | string  | Unique delivery identifier                    |
| `webhook_url`   | string  | URL the webhook was sent to                   |
| `status`        | string  | `pending`, `delivered`, `failed`, `dead_letter`|
| `status_code`   | integer | HTTP response status code                     |
| `attempt`       | integer | Current attempt number                        |
| `max_attempts`  | integer | Maximum retry attempts                        |
| `error_message` | string  | Error message if delivery failed              |
| `duration_ms`   | integer | Round-trip delivery time in milliseconds      |
| `created_at`    | string  | ISO 8601 UTC creation time                    |
| `delivered_at`  | string  | ISO 8601 UTC delivery time                    |

### BlockchainTransaction

| Field          | Type   | Description                              |
|----------------|--------|------------------------------------------|
| `chain`        | string | Blockchain name                          |
| `type`         | string | Transaction type                         |
| `tx_hash`      | string | On-chain transaction hash                |
| `explorer_url` | string | Block explorer URL for the transaction   |
| `status`       | string | Transaction status                       |

### RouteOption

| Field                          | Type    | Description                              |
|--------------------------------|---------|------------------------------------------|
| `provider`                     | string  | On-ramp provider name                    |
| `off_ramp_provider`            | string  | Off-ramp provider name                   |
| `chain`                        | string  | Blockchain used                          |
| `stablecoin`                   | string  | Stablecoin used                          |
| `score`                        | number  | Composite score (0-100)                  |
| `estimated_fee_usd`            | string  | Estimated total fee in USD               |
| `estimated_settlement_seconds` | integer | Estimated end-to-end time in seconds     |
| `score_breakdown`              | object  | Per-factor scores (cost, speed, liquidity, reliability) |

### ExportJob

| Field                 | Type    | Description                          |
|-----------------------|---------|--------------------------------------|
| `id`                  | string  | UUID                                 |
| `status`              | string  | `pending`, `processing`, `completed`, `failed` |
| `export_type`         | string  | Type of export                       |
| `row_count`           | integer | Number of rows exported              |
| `download_url`        | string  | URL to download results              |
| `download_expires_at` | string  | ISO 8601 UTC download link expiry    |
| `error_message`       | string  | Error message if failed              |
| `created_at`          | string  | ISO 8601 UTC creation time           |
| `completed_at`        | string  | ISO 8601 UTC completion time         |

---

## Error Codes

### Complete Error Code Reference

| Code                              | HTTP | Description                                           | Retryable | Common Fix                                           |
|-----------------------------------|------|-------------------------------------------------------|-----------|------------------------------------------------------|
| `QUOTE_EXPIRED`                   | 400  | Quote has expired                                     | No        | Create a new quote                                   |
| `INSUFFICIENT_FUNDS`              | 400  | Insufficient balance for the operation                | No        | Top up treasury position                             |
| `INVALID_TRANSITION`              | 400  | Invalid state machine transition                      | No        | Check entity status before operation                 |
| `PROVIDER_ERROR`                  | 502  | Payment provider returned an error                    | Yes       | Retry after a short delay                            |
| `PROVIDER_UNAVAILABLE`            | 503  | Payment provider is unreachable                       | Yes       | Retry after a short delay                            |
| `CHAIN_ERROR`                     | 502  | Blockchain RPC error                                  | No        | Contact support if persistent                        |
| `LEDGER_IMBALANCE`                | 500  | Ledger debits/credits do not balance                  | No        | Contact support immediately                          |
| `POSITION_LOCKED`                 | 423  | Treasury position is temporarily locked               | No        | Wait for in-flight operations to complete            |
| `CORRIDOR_DISABLED`               | 400  | Currency pair is not enabled                          | No        | Use a supported currency corridor                    |
| `AMOUNT_TOO_LOW`                  | 400  | Amount below minimum (0.01)                           | No        | Increase the amount                                  |
| `AMOUNT_TOO_HIGH`                 | 400  | Amount above maximum (10,000,000)                     | No        | Decrease the amount                                  |
| `IDEMPOTENCY_CONFLICT`            | 409  | Idempotency key already used with different params    | No        | Use a new idempotency key                            |
| `OPTIMISTIC_LOCK`                 | 409  | Concurrent modification detected                      | Yes       | Retry with the latest version                        |
| `TENANT_SUSPENDED`                | 403  | Tenant account is suspended                           | No        | Contact Settla support                               |
| `TENANT_NOT_FOUND`                | 404  | Tenant does not exist                                 | No        | Verify API key and tenant ID                         |
| `DAILY_LIMIT_EXCEEDED`            | 400  | Tenant daily volume limit reached                     | No        | Wait until the next day or request limit increase    |
| `UNAUTHORIZED`                    | 401  | Invalid or missing authentication                     | No        | Check API key or JWT token                           |
| `RESERVATION_LOCK_TIMEOUT`        | 503  | Treasury lock contention timeout                      | Yes       | Retry after a short delay                            |
| `RESERVATION_INSUFFICIENT_FUNDS`  | 400  | Treasury position has insufficient funds              | No        | Top up treasury position                             |
| `CURRENCY_MISMATCH`               | 400  | Currency does not match the expected value             | No        | Verify currency parameters                           |
| `ACCOUNT_NOT_FOUND`               | 404  | Ledger account not found                              | No        | Verify account code                                  |
| `TRANSFER_NOT_FOUND`              | 404  | Transfer not found                                    | No        | Verify transfer ID                                   |
| `NETWORK_ERROR`                   | 503  | Network connectivity issue                            | Yes       | Retry after a short delay                            |
| `BLOCKCHAIN_REORG`                | 500  | Blockchain reorganization detected                    | No        | Wait for chain stabilization, contact support        |
| `COMPENSATION_FAILED`             | 500  | Refund/compensation flow failed                       | No        | Contact support for manual resolution                |
| `RATE_LIMIT_EXCEEDED`             | 429  | Too many requests                                     | Yes       | Back off and retry (check Retry-After header)        |
| `EMAIL_ALREADY_EXISTS`            | 400  | Email address already registered                      | No        | Use a different email or log in                      |
| `INVALID_CREDENTIALS`             | 401  | Wrong email or password                               | No        | Check credentials                                    |
| `EMAIL_NOT_VERIFIED`              | 401  | Email verification required before login              | No        | Check inbox for verification email                   |
| `TOKEN_EXPIRED`                   | 401  | JWT or verification token has expired                 | No        | Refresh the token or request a new one               |
| `SLUG_CONFLICT`                   | 400  | Company slug already taken                            | No        | Use a different company name                         |
| `DEPOSIT_NOT_FOUND`               | 404  | Crypto deposit session not found                      | No        | Verify session ID                                    |
| `DEPOSIT_EXPIRED`                 | 400  | Deposit session has expired                           | No        | Create a new deposit session                         |
| `CRYPTO_DISABLED`                 | 400  | Crypto deposits disabled for tenant                   | No        | Enable crypto in tenant settings                     |
| `CHAIN_NOT_SUPPORTED`             | 400  | Requested blockchain not supported                    | No        | Use a supported chain                                |
| `ADDRESS_POOL_EMPTY`              | 503  | No deposit addresses available                        | No        | Contact support to provision addresses               |
| `BANK_DEPOSITS_DISABLED`          | 400  | Bank deposits disabled for tenant                     | No        | Enable bank deposits in settings                     |
| `CURRENCY_NOT_SUPPORTED`          | 400  | Currency not supported for deposits                   | No        | Use a supported currency                             |
| `VIRTUAL_ACCOUNT_POOL_EMPTY`      | 503  | No virtual accounts available                         | No        | Contact support to provision accounts                |
| `BANK_DEPOSIT_NOT_FOUND`          | 404  | Bank deposit session not found                        | No        | Verify session ID                                    |
| `PAYMENT_MISMATCH`                | 400  | Received amount does not match expected               | No        | Depends on mismatch policy configuration             |
| `PAYMENT_LINK_NOT_FOUND`          | 404  | Payment link not found                                | No        | Verify link ID or short code                         |
| `PAYMENT_LINK_EXPIRED`            | 400  | Payment link has expired                              | No        | Create a new payment link                            |
| `PAYMENT_LINK_EXHAUSTED`          | 400  | Payment link use limit reached                        | No        | Create a new payment link                            |
| `PAYMENT_LINK_DISABLED`           | 400  | Payment link has been disabled                        | No        | Create a new payment link                            |
| `SHORT_CODE_COLLISION`            | 500  | Short code generation collision                       | No        | Retry the creation request                           |

---

## Rate Limits

| Endpoint Category           | Limit                       | Scope       |
|-----------------------------|-----------------------------|-------------|
| Login (`/v1/auth/login`)    | 10 req/min, 100 req/hr      | Per IP      |
| Public endpoints            | 20 req/s                     | Per IP      |
| General API                 | 1,000 req/s                  | Per tenant  |
| Webhook inbound (global)    | Configurable (default high)  | Global      |
| Webhook inbound (per-IP)    | Configurable                 | Per IP      |
| Webhook inbound (per-provider)| Configurable               | Per provider|

When rate-limited, responses include:
- HTTP status `429 Too Many Requests`
- `Retry-After` header (seconds until the limit resets)

---

## Webhook Events

Settla delivers outbound webhooks to your configured endpoint for transfer lifecycle events. Webhooks are signed with HMAC-SHA256 using your webhook secret.

### Delivery

- **Method:** POST
- **Content-Type:** application/json
- **Headers:**
  - `X-Webhook-Signature`: HMAC-SHA256 hex digest of the request body
  - `X-Webhook-Timestamp`: Unix timestamp of the delivery
  - `X-Webhook-Event`: Event type
  - `X-Webhook-Delivery-Id`: Unique delivery identifier

### Retry Schedule

| Attempt | Delay after previous |
|---------|---------------------|
| 1       | Immediate           |
| 2       | 1 minute            |
| 3       | 5 minutes           |
| 4       | 30 minutes          |
| 5       | 2 hours             |

After 5 failed attempts, the delivery is moved to the dead letter queue. Total window: approximately 2.5 hours.

### Event Types

| Event Type               | Description                                      | Trigger                                      |
|--------------------------|--------------------------------------------------|----------------------------------------------|
| `transfer.created`       | Transfer has been created                        | `POST /v1/transfers` succeeds                |
| `transfer.funded`        | Treasury funds have been reserved                | Engine transitions to FUNDED                 |
| `transfer.on_ramping`    | Fiat-to-stablecoin conversion started            | Engine transitions to ON_RAMPING             |
| `transfer.settling`      | On-chain settlement in progress                  | Engine transitions to SETTLING               |
| `transfer.off_ramping`   | Stablecoin-to-fiat conversion started            | Engine transitions to OFF_RAMPING            |
| `transfer.completing`    | Transfer is in final completion steps            | Engine is finalizing the transfer            |
| `transfer.completed`     | Transfer successfully completed                  | Engine transitions to COMPLETED              |
| `transfer.failed`        | Transfer has failed                              | Engine transitions to FAILED                 |
| `transfer.refunding`     | Refund is in progress                            | Engine transitions to REFUNDING              |
| `transfer.refunded`      | Refund has been completed                        | Engine transitions to REFUNDED               |

### Webhook Payload Example

```json
{
  "event": "transfer.completed",
  "delivery_id": "del-a1b2c3d4",
  "timestamp": 1705330200,
  "data": {
    "id": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
    "tenant_id": "a0000000-0000-0000-0000-000000000001",
    "status": "COMPLETED",
    "source_currency": "NGN",
    "source_amount": "500000",
    "dest_currency": "GBP",
    "dest_amount": "245.50",
    "stable_coin": "USDT",
    "chain": "tron",
    "fees": {
      "on_ramp_fee": "2.00",
      "network_fee": "0.50",
      "off_ramp_fee": "1.75",
      "total_fee_usd": "4.25"
    },
    "recipient": {
      "name": "John Smith",
      "country": "GB"
    },
    "blockchain_transactions": [
      {
        "chain": "tron",
        "type": "settlement",
        "tx_hash": "abc123...def456",
        "explorer_url": "https://tronscan.org/#/transaction/abc123...def456",
        "status": "confirmed"
      }
    ],
    "created_at": "2025-01-15T14:30:00.000Z",
    "completed_at": "2025-01-15T14:45:00.000Z"
  }
}
```

### Verifying Webhook Signatures

```javascript
const crypto = require('crypto');

function verifyWebhook(body, signature, timestamp, secret) {
  const payload = Buffer.concat([
    Buffer.from(body),
    Buffer.from(`.${timestamp}`)
  ]);
  const expected = crypto
    .createHmac('sha256', secret)
    .update(payload)
    .digest('hex');
  return crypto.timingSafeEqual(
    Buffer.from(signature, 'hex'),
    Buffer.from(expected, 'hex')
  );
}
```

---

## State Machines

### Transfer States

```
CREATED --> FUNDED --> ON_RAMPING --> SETTLING --> OFF_RAMPING --> COMPLETED
   |           |          |              |              |
   v           v          v              v              v
 FAILED    REFUNDING   REFUNDING      FAILED         FAILED
               |          |
               v          v
           REFUNDED    REFUNDED/FAILED
```

**Valid Transitions:**

| From          | To                                       |
|---------------|------------------------------------------|
| `CREATED`     | `FUNDED`, `FAILED`                       |
| `FUNDED`      | `ON_RAMPING`, `REFUNDING`                |
| `ON_RAMPING`  | `SETTLING`, `REFUNDING`, `FAILED`        |
| `SETTLING`    | `OFF_RAMPING`, `FAILED`                  |
| `OFF_RAMPING` | `COMPLETED`, `FAILED`                    |
| `FAILED`      | `REFUNDING`                              |
| `REFUNDING`   | `REFUNDED`                               |
| `COMPLETED`   | (terminal)                               |
| `REFUNDED`    | (terminal)                               |

### Crypto Deposit States

```
PENDING_PAYMENT --> DETECTED --> CONFIRMED --> CREDITING --> CREDITED --> SETTLING --> SETTLED
       |               |             |             |              |
       v               v             v             v              v
   EXPIRED/       PENDING_PAYMENT   FAILED       FAILED          HELD
   CANCELLED          FAILED
```

**Valid Transitions:**

| From              | To                                            |
|-------------------|-----------------------------------------------|
| `PENDING_PAYMENT` | `DETECTED`, `EXPIRED`, `CANCELLED`            |
| `DETECTED`        | `CONFIRMED`, `PENDING_PAYMENT`, `FAILED`      |
| `CONFIRMED`       | `CREDITING`, `FAILED`                         |
| `CREDITING`       | `CREDITED`, `FAILED`                          |
| `CREDITED`        | `SETTLING`, `HELD`                            |
| `SETTLING`        | `SETTLED`, `FAILED`                           |
| `EXPIRED`         | `DETECTED` (late payment)                     |
| `CANCELLED`       | `DETECTED` (late payment)                     |
| `SETTLED`         | (terminal)                                    |
| `HELD`            | (terminal)                                    |
| `FAILED`          | (terminal)                                    |

### Bank Deposit States

```
PENDING_PAYMENT --> PAYMENT_RECEIVED --> CREDITING --> CREDITED --> SETTLING --> SETTLED
       |                   |                |              |
       v                   v                v              v
   EXPIRED/            UNDERPAID/         FAILED          HELD
   CANCELLED           OVERPAID/
                       FAILED
```

**Valid Transitions:**

| From               | To                                                          |
|--------------------|-------------------------------------------------------------|
| `PENDING_PAYMENT`  | `PAYMENT_RECEIVED`, `EXPIRED`, `CANCELLED`                  |
| `PAYMENT_RECEIVED` | `CREDITING`, `UNDERPAID`, `OVERPAID`, `FAILED`              |
| `CREDITING`        | `CREDITED`, `FAILED`                                        |
| `CREDITED`         | `SETTLING`, `HELD`                                          |
| `SETTLING`         | `SETTLED`, `FAILED`                                         |
| `UNDERPAID`        | `FAILED`                                                    |
| `OVERPAID`         | `FAILED`                                                    |
| `EXPIRED`          | `PAYMENT_RECEIVED` (late payment)                           |
| `CANCELLED`        | `PAYMENT_RECEIVED` (late payment)                           |
| `SETTLED`          | (terminal)                                                  |
| `HELD`             | (terminal)                                                  |
| `FAILED`           | (terminal)                                                  |

---

## Supported Currencies and Chains

### Fiat Currencies

| Code | Currency             |
|------|----------------------|
| NGN  | Nigerian Naira       |
| USD  | US Dollar            |
| GBP  | British Pound        |
| EUR  | Euro                 |
| GHS  | Ghanaian Cedi        |
| KES  | Kenyan Shilling      |

### Stablecoins

| Code | Token                |
|------|----------------------|
| USDT | Tether USD           |
| USDC | USD Coin             |

### Blockchains

| Chain      | Description                         |
|------------|-------------------------------------|
| `tron`     | Tron network (TRC-20 tokens)        |
| `ethereum` | Ethereum mainnet (ERC-20 tokens)    |
| `solana`   | Solana network (SPL tokens)         |
| `base`     | Base L2 (ERC-20 tokens)             |
| `polygon`  | Polygon PoS (ERC-20 tokens)         |
| `arbitrum` | Arbitrum One L2 (ERC-20 tokens)     |
