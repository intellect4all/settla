# Chapter 10.3: Webhook Signatures -- Proving Authenticity

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why both outbound and inbound webhooks require signature verification and how the threat models differ
2. Trace the HMAC-SHA256 signing flow in Settla's WebhookWorker, from payload construction through header delivery
3. Implement webhook signature verification in Node.js, Python, and Go
4. Describe the inbound webhook verification pipeline and how per-provider secrets isolate trust boundaries
5. Evaluate the tradeoffs of HMAC-SHA256 vs asymmetric signatures and articulate why Settla chose the symmetric approach
6. Design a webhook secret rotation procedure that avoids delivery gaps

---

## Two Directions of Webhooks

Settla has webhooks flowing in both directions, and each direction faces a different threat:

```
                    OUTBOUND                              INBOUND
           Settla --> Tenant                       Provider --> Settla

  +--------+    HTTPS POST     +---------+    +-----------+   HTTPS POST   +--------+
  | Settla | ----------------> | Tenant  |    | Provider  | -------------> | Settla |
  | Server |  signed payload   | Webhook |    | (Paystack |  signed body   | Webhook|
  +--------+                   | Handler |    |  Flutterw)|                | Rcvr   |
                               +---------+    +-----------+                +--------+

  Threat: attacker forges       Threat: attacker forges
  a "transfer.completed"        a payment callback to
  event to trick tenant         trick Settla into crediting
  into releasing funds          a tenant's treasury
```

**Outbound webhooks** (Settla to tenant) notify tenants of lifecycle events: `transfer.completed`, `deposit.confirmed`, `settlement.processed`, and so on. The threat is forgery -- an attacker who knows the tenant's webhook URL can send a fake `transfer.completed` event, causing the tenant's system to release funds for a transfer that never settled. At 50M transactions/day, even a brief window of forged events can cause significant financial damage.

**Inbound webhooks** (provider to Settla) carry async payment callbacks from banking partners and payment processors. The threat is similar but reversed -- an attacker forging a payment callback could trick Settla into crediting a tenant's treasury for a payment that never arrived.

Both directions use HMAC-SHA256, but with different keys and different verification endpoints.

---

## Outbound Webhook Signing (Settla to Tenant)

### The Signing Decision

ADR-012 evaluated four options for outbound webhook authentication:

| Option | Pros | Cons |
|--------|------|------|
| **HMAC-SHA256 (chosen)** | Simple, industry-standard, ~10 lines to verify | Shared secret -- both sides know the key |
| Asymmetric (Ed25519/RSA) | Tenant only needs public key | More complex, non-standard for webhooks |
| Mutual TLS | Strong auth at transport layer | Heavy operational burden per tenant |
| JWT-signed payloads | Self-contained verification | JWT parsing overhead, larger payloads |

HMAC-SHA256 won because it is the industry standard. Stripe, GitHub, Shopify, and virtually every webhook provider uses it. Tenant engineering teams already know the pattern and likely have verification code from other integrations.

### How the WebhookWorker Signs Payloads

The signing happens in `node/worker/webhook_worker.go`. Let us trace the full flow from NATS message to signed HTTP request.

**Step 1: Load the tenant's webhook configuration.**

The worker receives a `webhook.deliver` intent from the NATS `SETTLA_WEBHOOKS` stream. It deserializes the `WebhookDeliverPayload` and loads the tenant record, which contains the webhook URL and the per-tenant signing secret:

```go
// domain/tenant.go

type Tenant struct {
    ID                 uuid.UUID
    Name               string
    Slug               string
    WebhookURL         string
    WebhookSecret      string
    // ... other fields
}
```

```go
// node/worker/webhook_worker.go

// Load tenant to get webhook URL and secret
tenant, err := w.tenantStore.GetTenant(ctx, payload.TenantID)
if err != nil {
    w.logger.Error("settla-webhook-worker: failed to load tenant",
        "tenant_id", payload.TenantID,
        "error", err,
    )
    return fmt.Errorf("settla-webhook-worker: loading tenant %s: %w", payload.TenantID, err)
}

if tenant.WebhookURL == "" {
    w.logger.Info("settla-webhook-worker: tenant has no webhook URL, skipping",
        "tenant_id", payload.TenantID,
    )
    return nil // ACK -- no webhook configured
}
```

If a tenant has no webhook URL configured, the worker ACKs the message silently. No point retrying something that has nowhere to go.

**Step 2: Build a deterministic delivery payload.**

The worker constructs a `WebhookPayload` with a deterministic delivery ID derived from the NATS event ID. This is critical -- NATS provides at-least-once delivery, so the same event may be redelivered. The deterministic ID allows tenants to deduplicate:

```go
// node/worker/webhook_worker.go

// WebhookPayload is the JSON body sent to tenant webhook endpoints.
type WebhookPayload struct {
    ID         string          `json:"id"`
    EventType  string          `json:"event_type"`
    TransferID string          `json:"transfer_id,omitempty"`
    SessionID  string          `json:"session_id,omitempty"`
    TenantID   string          `json:"tenant_id"`
    Data       json.RawMessage `json:"data,omitempty"`
    CreatedAt  time.Time       `json:"created_at"`
}

// Build webhook body with a deterministic delivery ID so tenants can
// deduplicate retries. The ID is derived from the NATS event ID which
// is stable across redeliveries.
deliveryID := deterministicDeliveryID(event.ID, payload.EventType)
webhookBody := WebhookPayload{
    ID:        deliveryID,
    EventType: payload.EventType,
    TenantID:  payload.TenantID.String(),
    Data:      payload.Data,
    CreatedAt: time.Now().UTC(),
}
```

The `deterministicDeliveryID` function hashes the NATS event ID and event type with SHA-256, then formats the result as a UUID-like string:

```go
// node/worker/webhook_worker.go

func deterministicDeliveryID(eventID uuid.UUID, eventType string) string {
    h := sha256.New()
    h.Write(eventID[:])
    h.Write([]byte(eventType))
    sum := h.Sum(nil)
    return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
```

**Step 3: Sign the serialized JSON body.**

After marshalling the payload to JSON, the worker computes the HMAC-SHA256 signature:

```go
// node/worker/webhook_worker.go

// Sign with HMAC-SHA256
signature := signWebhook(body, tenant.WebhookSecret)
```

The signing function itself is straightforward:

```go
// node/worker/webhook_worker.go

// signWebhook computes the HMAC-SHA256 signature of the payload using the secret.
func signWebhook(payload []byte, secret string) string {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(payload)
    return hex.EncodeToString(mac.Sum(nil))
}
```

This produces a 64-character hex string (256 bits). The key input is the tenant's `WebhookSecret`, and the message input is the exact JSON bytes that will be sent in the HTTP body.

**Step 4: Attach signature headers and send.**

The worker sets four custom headers on the outbound HTTP request:

```go
// node/worker/webhook_worker.go

req.Header.Set("Content-Type", "application/json")
req.Header.Set("X-Settla-Signature", signature)
req.Header.Set("X-Settla-Event", payload.EventType)
req.Header.Set("X-Settla-Delivery", webhookBody.ID)
```

| Header | Purpose |
|--------|---------|
| `X-Settla-Signature` | HMAC-SHA256 hex digest of the request body |
| `X-Settla-Event` | Event type (e.g., `transfer.completed`) for routing without parsing body |
| `X-Settla-Delivery` | Deterministic delivery ID for deduplication |
| `Content-Type` | Always `application/json` |

### The Complete Signing Flow

```
WebhookWorker receives NATS message (webhook.deliver intent)
    |
    v
Load tenant from DB --> get WebhookURL + WebhookSecret
    |
    v
Build WebhookPayload (deterministic ID from event hash)
    |
    v
Marshal to JSON bytes
    |
    v
HMAC-SHA256(key=WebhookSecret, message=JSON bytes) --> hex signature
    |
    v
HTTP POST to tenant.WebhookURL
    Headers:
      X-Settla-Signature: <signature>
      X-Settla-Event: transfer.completed
      X-Settla-Delivery: <deterministic-id>
    Body: <JSON bytes>
    |
    v
Check HTTP response:
    2xx --> ACK (success)
    5xx/408/429 --> NAK (retry with backoff)
    4xx --> ACK (permanent failure, do not retry)
```

> **Key Insight:** The signature covers the exact JSON bytes sent in the body. If a proxy, CDN, or middleware reformats the JSON (e.g., re-indenting or reordering keys), the signature will not match. Tenants must verify against the raw request body, not a re-parsed and re-serialized version.

---

## How Tenants Verify Signatures

Verification is the tenant's responsibility. If they skip it, they lose all forgery protection -- and Settla cannot enforce it. This is an inherent tradeoff of webhook-based architectures.

### The Verification Algorithm

```
1. Extract the X-Settla-Signature header from the request
2. Read the raw request body (do NOT parse and re-serialize)
3. Compute HMAC-SHA256(key=your_webhook_secret, message=raw_body)
4. Convert the result to a lowercase hex string
5. Compare your computed signature to X-Settla-Signature
   using constant-time comparison
6. If they match, the webhook is authentic
7. Optionally: check X-Settla-Delivery to deduplicate
```

### Verification Examples

**Node.js:**

```javascript
import { createHmac, timingSafeEqual } from "node:crypto";

function verifySettlaWebhook(rawBody, secret, signatureHeader) {
  const expected = createHmac("sha256", secret)
    .update(rawBody, "utf8")
    .digest("hex");

  // Constant-time comparison to prevent timing attacks
  if (expected.length !== signatureHeader.length) return false;
  return timingSafeEqual(
    Buffer.from(expected),
    Buffer.from(signatureHeader)
  );
}

// In your Express/Fastify handler:
app.post("/webhooks/settla", (req, res) => {
  const signature = req.headers["x-settla-signature"];
  const rawBody = req.rawBody; // Must be the raw bytes, not parsed JSON

  if (!verifySettlaWebhook(rawBody, process.env.SETTLA_WEBHOOK_SECRET, signature)) {
    return res.status(401).json({ error: "invalid signature" });
  }

  const event = JSON.parse(rawBody);
  // Process the event...
  res.status(200).json({ ok: true });
});
```

**Python:**

```python
import hmac
import hashlib

def verify_settla_webhook(raw_body: bytes, secret: str, signature_header: str) -> bool:
    expected = hmac.new(
        secret.encode("utf-8"),
        raw_body,
        hashlib.sha256
    ).hexdigest()

    # Constant-time comparison
    return hmac.compare_digest(expected, signature_header)

# In your Flask handler:
@app.route("/webhooks/settla", methods=["POST"])
def handle_webhook():
    signature = request.headers.get("X-Settla-Signature")
    raw_body = request.get_data()  # Raw bytes, not parsed

    if not verify_settla_webhook(raw_body, WEBHOOK_SECRET, signature):
        return jsonify({"error": "invalid signature"}), 401

    event = request.get_json()
    # Process the event...
    return jsonify({"ok": True}), 200
```

**Go:**

```go
import (
    "crypto/hmac"
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "io"
    "net/http"
)

func verifySettlaWebhook(rawBody []byte, secret, signatureHeader string) bool {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(rawBody)
    expected := hex.EncodeToString(mac.Sum(nil))

    // Constant-time comparison
    return subtle.ConstantTimeCompare([]byte(expected), []byte(signatureHeader)) == 1
}

func webhookHandler(w http.ResponseWriter, r *http.Request) {
    signature := r.Header.Get("X-Settla-Signature")
    rawBody, _ := io.ReadAll(r.Body)

    if !verifySettlaWebhook(rawBody, webhookSecret, signature) {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }

    // Process the event...
    w.WriteHeader(http.StatusOK)
}
```

### Why Constant-Time Comparison Matters

A naive string comparison (`==`) short-circuits on the first mismatched byte. An attacker can measure the response time to determine how many leading bytes of their forged signature match the correct one, then brute-force the signature byte by byte.

```
Timing attack against naive comparison:

  Attacker sends signature: "a000...0000"
  Server compares: "a" == "f" --> returns FAST (1 byte compared)

  Attacker sends signature: "f000...0000"
  Server compares: "f" == "f", "0" == "3" --> returns SLIGHTLY SLOWER

  Attacker sends signature: "f300...0000"
  Server compares: "f" == "f", "3" == "3", "0" == "a" --> EVEN SLOWER

  ... repeat 64 times to recover the full 64-hex-char signature
```

A 256-bit HMAC has `2^256` possible values, which is computationally infeasible to brute-force. But with a timing side channel, an attacker only needs `16 * 64 = 1,024` requests to recover the signature for a specific payload (16 hex digits per position, 64 positions). In practice it takes more due to network noise, but it is entirely feasible with statistical analysis over a few thousand requests.

Constant-time comparison functions (`timingSafeEqual` in Node.js, `hmac.compare_digest` in Python, `subtle.ConstantTimeCompare` in Go) always examine every byte, taking the same time regardless of where the first mismatch occurs.

> **Key Insight:** Settla's own inbound webhook verification uses `timingSafeEqual` in `api/webhook/src/signature.ts`. This is not optional -- it is a security requirement. Any code review that finds a `===` comparison of HMAC signatures should be flagged as a vulnerability.

---

## Inbound Webhook Verification (Provider to Settla)

Inbound webhooks arrive from external payment providers (banking partners, payment processors) carrying async status updates: "payment received," "transfer failed," "account credited." Settla must verify these before acting on them.

### The Inbound Pipeline

**File:** `api/webhook/src/provider-inbound.ts`

The inbound webhook receiver is a Fastify HTTP server that acts as a pure relay: verify the signature, then forward the raw payload to NATS for processing by the Go `InboundWebhookWorker`.

```
Provider (Paystack, etc.)
    |
    | HTTPS POST /webhooks/providers/:providerSlug
    v
+---------------------------------+
| Fastify Webhook Receiver (TS)   |
|                                 |
| 1. Extract provider slug        |
| 2. Look up per-provider secret  |
| 3. Verify HMAC-SHA256 signature |
| 4. Forward raw body to NATS     |
+---------------------------------+
    |
    | NATS: settla.provider.inbound.raw
    v
+---------------------------------+
| InboundWebhookWorker (Go)       |
|                                 |
| 1. Deserialize raw payload      |
| 2. Normalize per-provider format|
| 3. Log to provider_webhook_logs |
| 4. Publish domain event         |
+---------------------------------+
```

### Per-Provider Secret Lookup

Each provider has its own signing secret, configured independently. This is essential -- a compromised provider secret only affects that one provider, not all of them:

```typescript
// api/webhook/src/provider-inbound.ts

interface ProviderInboundConfig {
  natsUrl: string;
  /**
   * HMAC-SHA256 signing secrets keyed by providerSlug.
   * Webhooks with an invalid signature are rejected with 401.
   * Webhooks with no configured secret are rejected with 403.
   */
  signingSecrets?: Record<string, string>;
  /**
   * Optional per-provider override for the HTTP header that carries the
   * signature. Defaults to "x-webhook-signature".
   */
  signatureHeaders?: Record<string, string>;
}
```

Different providers use different header names for their signatures. Stripe uses `Stripe-Signature`, Paystack uses `X-Paystack-Signature`, and so on. The `signatureHeaders` map handles this without hardcoding provider-specific logic into the route handler.

### The Verification Code

```typescript
// api/webhook/src/provider-inbound.ts

server.post<{
  Params: { providerSlug: string };
  Body: Buffer;
}>(
  "/webhooks/providers/:providerSlug",
  async (request, reply) => {
    const { providerSlug } = request.params;
    const rawBuffer = request.body;

    // Determine which header carries the signature for this provider
    const sigHeaderName =
      (config.signatureHeaders ?? {})[providerSlug] ?? DEFAULT_SIGNATURE_HEADER;
    const signature = request.headers[sigHeaderName] as string | undefined;

    // ── Signature verification ──────────────────────────────────
    const secret = (config.signingSecrets ?? {})[providerSlug];
    if (!secret) {
      logger.error("provider-inbound: no webhook secret configured", { providerSlug });
      return reply.status(403).send({ error: "provider not configured" });
    }

    if (!signature) {
      logger.warn("provider-inbound: webhook received without signature", { providerSlug });
      return reply.status(401).send({ error: "missing signature" });
    }

    const rawBody = rawBuffer.toString("utf8");
    if (!verifySignature(rawBody, secret, signature)) {
      signatureFailuresTotal.inc({ provider: providerSlug });
      logger.warn("provider-inbound: invalid webhook signature", { providerSlug });
      return reply.status(401).send({ error: "invalid signature" });
    }
```

Three rejection paths, each with a distinct HTTP status:

| Condition | HTTP Status | Meaning |
|-----------|-------------|---------|
| No secret configured for provider | 403 Forbidden | Settla does not recognize this provider |
| No signature header in request | 401 Unauthorized | Provider sent unsigned request |
| Signature does not match | 401 Unauthorized | Signature is forged or corrupted |

### The Verification Function

The actual HMAC comparison lives in `api/webhook/src/signature.ts`:

```typescript
// api/webhook/src/signature.ts

import { createHmac, timingSafeEqual } from "node:crypto";

export function computeSignature(body: string, secret: string): string {
  return createHmac("sha256", secret).update(body, "utf8").digest("hex");
}

export function verifySignature(
  body: string,
  secret: string,
  signature: string
): boolean {
  const expected = computeSignature(body, secret);
  if (expected.length !== signature.length) return false;
  return timingSafeEqual(Buffer.from(expected), Buffer.from(signature));
}
```

Note the length check before `timingSafeEqual`. The Node.js `timingSafeEqual` function throws if the two buffers have different lengths. The length check is a fast-path rejection -- if the provided signature is not exactly 64 hex characters, it cannot possibly be a valid HMAC-SHA256 hex digest. This early return does leak timing information (an attacker learns the signature is the wrong length), but that reveals nothing useful -- the correct length is publicly documented.

### Raw Body Preservation

A subtle but critical detail: the Fastify server parses the request body as a raw buffer, not as JSON:

```typescript
// api/webhook/src/provider-inbound.ts

// Parse the body as a raw buffer so we can compute HMAC over the exact
// bytes the provider sent.
server.addContentTypeParser(
  "application/json",
  { parseAs: "buffer" },
  (_req, body, done) => {
    done(null, body as Buffer);
  }
);
```

This is essential. If Fastify parsed the body as JSON and then re-serialized it, the bytes might differ from what the provider signed (different key ordering, whitespace, Unicode escaping). The HMAC must be computed over the exact bytes the provider sent.

### Idempotency and Forwarding

After verification succeeds, the receiver computes an idempotency key from the provider slug and a SHA-256 hash of the raw body, then publishes to NATS:

```typescript
// api/webhook/src/provider-inbound.ts

// Build idempotency key: provider slug + SHA-256 prefix of raw body.
const bodyHash = createHash("sha256").update(rawBuffer).digest("hex").slice(0, 32);
const idempotencyKey = `${providerSlug}-${bodyHash}`;

// Forward raw payload to NATS -- normalization happens in Go.
const event = {
  ID: randomUUID(),
  Type: RAW_WEBHOOK_EVENT_TYPE,
  Timestamp: new Date().toISOString(),
  Data: {
    provider_slug: providerSlug,
    raw_body: rawBuffer.toString("base64"),
    idempotency_key: idempotencyKey,
    http_headers: {
      "content-type": request.headers["content-type"] ?? "",
      "user-agent": request.headers["user-agent"] ?? "",
    },
    source_ip: request.ip,
  },
};

await js.publish(RAW_WEBHOOK_SUBJECT, data, {
  msgID: idempotencyKey,
});
```

The raw body is base64-encoded for safe transport through NATS. The `msgID` field uses the idempotency key to leverage NATS JetStream's built-in deduplication (5-minute window). If a provider retries the same webhook, NATS silently drops the duplicate.

---

## Webhook Secret Rotation

Secrets must be rotatable. A webhook secret could be leaked through log exposure, a compromised tenant system, or an employee departure. The rotation procedure must not cause a gap where valid webhooks are rejected.

### The Dual-Signature Rotation Protocol

ADR-012 specifies dual-signature rotation:

```
Phase 1: Normal operation
    Settla signs with: secret_v1
    Tenant verifies with: secret_v1

Phase 2: Rotation begins (Settla generates secret_v2)
    Settla signs with: secret_v1 AND secret_v2
    Headers sent:
      X-Settla-Signature: HMAC(secret_v1, body)
      X-Settla-Signature-V2: HMAC(secret_v2, body)
    Tenant still verifies with: secret_v1 (works)

Phase 3: Tenant updates their verification code
    Tenant switches to verifying X-Settla-Signature-V2 with secret_v2
    Both headers still sent (old tenant code still works if rollback needed)

Phase 4: Rotation complete
    Settla drops secret_v1, stops sending X-Settla-Signature
    Only X-Settla-Signature (now with secret_v2) is sent
    Tenant verifies with: secret_v2
```

The critical property is that at no point during this process are valid webhooks rejected. The dual-signature period can last as long as needed -- hours, days, or weeks -- until the tenant confirms they have migrated.

### Why Per-Tenant Secrets Matter

Each tenant has its own `WebhookSecret` in their tenant record:

```
Tenant: Lemfi    --> WebhookSecret: "whsec_a1b2c3..."
Tenant: Fincra   --> WebhookSecret: "whsec_d4e5f6..."
Tenant: Paystack --> WebhookSecret: "whsec_g7h8i9..."
```

If Settla used a single shared signing key for all tenants, then:

1. Compromising one tenant's infrastructure leaks the key for ALL tenants
2. A malicious tenant who knows the shared key can forge webhooks for other tenants
3. Rotating the key requires coordinating with ALL tenants simultaneously

With per-tenant secrets, the blast radius of a key compromise is exactly one tenant. Rotation is a bilateral operation between Settla and that single tenant.

### Secret Generation

Webhook secrets are cryptographically random, 32 bytes, hex-encoded (64 characters). They are generated at tenant onboarding and stored in the `tenants` table in Transfer DB. The secret never appears in logs, error messages, or API responses after initial creation.

---

## Delivery Guarantees and Retry Behavior

Signing a webhook is pointless if it never arrives. The WebhookWorker implements a robust delivery pipeline with backpressure, retries, and dead-lettering.

### Retry Classification

The worker classifies HTTP responses into three categories:

```go
// node/worker/webhook_worker.go

if resp.StatusCode >= 200 && resp.StatusCode < 300 {
    // Success -- ACK the NATS message
    return nil
}

// Retryable: server errors, timeout, rate limited
if resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 429 {
    return fmt.Errorf("settla-webhook-worker: retryable HTTP %d", resp.StatusCode)
}

// Non-retryable: 4xx client errors
return nil // ACK -- don't retry permanent failures
```

| Response | Action | Rationale |
|----------|--------|-----------|
| 2xx | ACK | Delivery succeeded |
| 5xx, 408, 429 | NAK (retry) | Transient server error, timeout, or rate limit |
| Other 4xx | ACK (drop) | Client error -- retrying will not help |

Returning an error from the handler causes NATS to redeliver the message with exponential backoff. Returning `nil` ACKs the message, removing it from the stream permanently.

### SSRF Protection

Before sending any outbound webhook, the worker validates the target URL to prevent Server-Side Request Forgery attacks:

```go
// node/worker/webhook_worker.go

// Validate webhook URL to prevent SSRF attacks targeting internal services.
if !w.allowPrivateURLs {
    if err := validateWebhookURL(tenant.WebhookURL); err != nil {
        w.logger.Error("settla-webhook-worker: webhook URL rejected (SSRF protection)",
            "tenant_id", payload.TenantID,
            "webhook_url", tenant.WebhookURL,
            "error", err,
        )
        return nil // ACK -- unsafe URL won't resolve on retry
    }
}
```

The `validateWebhookURL` function resolves the hostname to IP addresses and rejects any that fall in private, loopback, link-local, or cloud metadata ranges. A malicious tenant who configures `http://169.254.169.254/latest/meta-data/` as their webhook URL (the AWS metadata endpoint) will be blocked.

### Backpressure: Global and Per-Tenant Semaphores

The worker limits concurrent HTTP calls with two layers of semaphores:

```
                     Per-tenant semaphore (max 10 concurrent per tenant)
                            |
                            v
Global HTTP semaphore (max 100 concurrent total)
                            |
                            v
                      HTTP POST to tenant
```

This two-tier approach prevents a single high-volume tenant from monopolizing all HTTP slots. Without it, Lemfi's 10,000 pending webhooks could starve Fincra's webhooks entirely.

### Per-Tenant Circuit Breakers

If a tenant's webhook endpoint is consistently failing, the worker opens a per-tenant circuit breaker (5 failures, 30-second reset timeout). This prevents wasting HTTP resources on a known-broken endpoint:

```go
// node/worker/webhook_worker.go

// Use per-tenant circuit breaker to prevent one broken tenant's webhook
// endpoint from opening the circuit for all other tenants.
tenantCB := w.getTenantCB(payload.TenantID.String())

var resp *http.Response
cbErr := tenantCB.Execute(ctx, func(ctx context.Context) error {
    var doErr error
    resp, doErr = w.httpClient.Do(req)
    return doErr
})
```

Both per-tenant semaphores and circuit breakers are evicted after 5 minutes of inactivity to prevent unbounded memory growth as new tenants are encountered.

### Dead Letter Queue

After NATS exhausts its retry policy (exponential backoff, configurable max attempts), undeliverable messages land in the `SETTLA_DLQ` stream. The DLQ Monitor alerts operations, and the messages can be replayed after the tenant fixes their endpoint.

---

## Common Mistakes

**1. Verifying against parsed-and-reserialized JSON instead of raw bytes.**

This is the most common verification bug. JSON serializers do not preserve key order, whitespace, or Unicode escape sequences. If you parse the body with `JSON.parse()` and then `JSON.stringify()` it before computing the HMAC, you will get a different byte sequence than what Settla signed. Always use the raw request body.

**2. Using `==` or `===` instead of constant-time comparison.**

Standard string comparison leaks timing information. Use `timingSafeEqual` (Node.js), `hmac.compare_digest` (Python), or `subtle.ConstantTimeCompare` (Go). This is not theoretical -- timing attacks against HMAC verification have been demonstrated in practice.

**3. Logging the webhook secret.**

Webhook secrets must never appear in application logs, error messages, or stack traces. If your logging framework serializes the entire tenant object, the secret will end up in your log aggregator. Redact it.

**4. Sharing one webhook secret across multiple environments.**

Your production and staging environments should have different webhook secrets. If staging uses production's secret, a compromised staging environment leaks your production credentials.

**5. Not implementing deduplication on the tenant side.**

Settla delivers webhooks at least once. The same event may arrive twice (or more) due to NATS redelivery, network retries, or disaster recovery replays. The `X-Settla-Delivery` header contains a deterministic delivery ID -- store it and reject duplicates.

**6. Hardcoding the signature header name for inbound webhooks.**

Different providers use different header names. The inbound receiver uses a configurable `signatureHeaders` map per provider slug. Hardcoding `x-webhook-signature` will break when integrating a provider that uses a different convention.

---

## Exercises

### Exercise 1: Implement Verification from Scratch

Without looking at the examples above, implement a webhook signature verification function in your language of choice. Your function should:

- Accept the raw body (bytes), secret (string), and signature header (string)
- Compute HMAC-SHA256 of the body with the secret
- Compare using constant-time comparison
- Return a boolean

Test it against this known-good pair:

```
Secret: "whsec_test_secret_key"
Body:   {"event":"transfer.completed","id":"evt_001"}
```

Compute the signature yourself and verify your function accepts it.

### Exercise 2: Spot the Vulnerability

The following webhook handler has three security issues. Find all three:

```javascript
app.post("/webhooks/settla", async (req, res) => {
  const sig = req.headers["x-settla-signature"];
  const body = JSON.stringify(req.body);

  const expected = createHmac("sha256", process.env.WEBHOOK_SECRET)
    .update(body)
    .digest("hex");

  if (sig === expected) {
    await processEvent(req.body);
    res.status(200).send("ok");
  } else {
    console.log(`Invalid signature. Secret: ${process.env.WEBHOOK_SECRET}`);
    res.status(401).send("unauthorized");
  }
});
```

<details>
<summary>Answers</summary>

1. **`JSON.stringify(req.body)` instead of raw body.** The body has been parsed by Express's JSON middleware and re-serialized. Key order and formatting may differ from the original bytes.
2. **`sig === expected` instead of `timingSafeEqual`.** Standard equality comparison is vulnerable to timing attacks.
3. **Logging the webhook secret** in the error branch. The secret will appear in application logs.

</details>

### Exercise 3: Design a Rotation Runbook

Write a step-by-step operational runbook for rotating a single tenant's webhook secret. Your runbook should:

1. Define who initiates the rotation (Settla ops or tenant)
2. Specify the exact API calls or database operations at each step
3. Include a verification step where the tenant confirms the new secret works
4. Define the rollback procedure if something goes wrong
5. Specify when it is safe to delete the old secret

### Exercise 4: Trace the Inbound Flow

Starting from a provider (e.g., Paystack) sending an HTTP POST to `/webhooks/providers/paystack`:

1. List every system component the request passes through
2. At which point is the signature verified?
3. At which point is the raw body logged for audit?
4. What happens if NATS is unavailable? What HTTP status does the provider receive?
5. How does NATS JetStream deduplication prevent double-processing if Paystack retries?

### Exercise 5: Timing Attack Simulation

Write a test that demonstrates the timing difference between naive comparison and constant-time comparison. Measure the average response time for:

- A signature where the first byte is wrong
- A signature where only the last byte is wrong
- The correct signature

With naive comparison, the first case should be measurably faster than the second. With `timingSafeEqual`, all three should take approximately the same time. Note: you may need thousands of iterations and statistical analysis to observe the difference due to network and OS scheduling noise.

---

## Summary

Webhook signatures are the last line of defense at the trust boundary between Settla and the outside world. The implementation is simple -- HMAC-SHA256 is a well-understood primitive -- but the details matter enormously: raw body preservation, constant-time comparison, per-tenant secret isolation, SSRF protection, and dual-signature rotation.

The outbound path (Settla to tenant) signs with the tenant's `WebhookSecret` and attaches `X-Settla-Signature`. The inbound path (provider to Settla) verifies with per-provider secrets and forwards raw payloads to NATS for normalization in Go. Both directions use the same cryptographic primitive but serve different trust models.

In the next chapter, we will examine API key management -- how Settla stores, validates, and rotates the keys that authenticate every API request.

---

## Further Reading

- [ADR-012: HMAC-SHA256 Webhook Signatures](../../adr/012-hmac-webhook-signatures.md) -- the architectural decision record for this design
- [Stripe Webhook Signatures](https://stripe.com/docs/webhooks/signatures) -- industry reference implementation
- [RFC 2104: HMAC](https://tools.ietf.org/html/rfc2104) -- the specification behind HMAC-SHA256
- `node/worker/webhook_worker.go` -- outbound webhook signing and delivery
- `api/webhook/src/provider-inbound.ts` -- inbound webhook verification and NATS forwarding
- `api/webhook/src/signature.ts` -- HMAC-SHA256 compute and verify functions
