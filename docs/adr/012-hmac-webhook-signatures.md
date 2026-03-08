# ADR-012: HMAC-SHA256 Webhook Signatures

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla dispatches webhook events to tenant endpoints for transfer lifecycle updates (initiated, completed, failed, etc.). These webhooks are HTTP POST requests to URLs controlled by each tenant — which means they are publicly accessible on the internet.

The security threshold:

- **Without signatures, any attacker who knows the webhook URL can forge events.** A forged `transfer.completed` event could cause a tenant's system to release funds for a transfer that never settled. At 50M transactions/day, even a brief window of forged events could cause significant financial damage.
- **Without timestamps, captured webhook payloads can be replayed indefinitely.** An attacker who intercepts a legitimate webhook (via network sniffing, log exposure, or compromised tenant infrastructure) could replay it days later to trigger duplicate processing.
- **Per-tenant secrets are required** because a shared signing key across tenants would mean compromising one tenant's key exposes all tenants' webhook integrity. With ~50+ tenants, the blast radius of a key compromise must be contained to a single tenant.

We needed to decide between:

1. **HMAC-SHA256 with per-tenant secrets** — symmetric key, simple to implement, widely understood
2. **Asymmetric signatures (Ed25519/RSA)** — Settla signs with private key, tenants verify with public key
3. **Mutual TLS** — certificate-based authentication on the webhook connection
4. **JWT-signed payloads** — embed a signed JWT in the webhook body

## Decision

We chose **HMAC-SHA256 with per-tenant signing secrets** for webhook authentication.

Each webhook request includes:

- **`X-Settla-Signature` header**: HMAC-SHA256 hex digest of the request body, keyed with the tenant's webhook secret
- **`X-Settla-Timestamp` header**: Unix timestamp (seconds) of when the webhook was generated

**Signature computation**:
```
signature = HMAC-SHA256(
    key: tenant.webhook_secret,
    message: timestamp + "." + request_body
)
```

The timestamp is prepended to the body before signing to bind the signature to a specific point in time. This prevents replay attacks — the receiving tenant should reject any webhook where `abs(now - timestamp) > 300 seconds` (5-minute window).

**Delivery guarantees**:
- Webhooks are delivered with **exponential backoff** retry on failure (HTTP 5xx or timeout)
- After max retries are exhausted, failed events go to a **dead letter queue** for manual inspection and replay
- Each event includes an **event ID** for idempotent processing on the tenant side

**Per-tenant secrets**:
- Each tenant has a unique `webhook_secret` generated at tenant onboarding (cryptographically random, 32 bytes, hex-encoded)
- Secrets are stored in the tenant record in Transfer DB
- Key rotation: generate a new secret, send both old and new signatures during a transition period, then deprecate the old key

## Consequences

### Benefits
- **Forgery prevention**: Without the tenant's secret key, an attacker cannot produce a valid HMAC-SHA256 signature. The signature binds the exact payload bytes to the tenant's key — any modification invalidates it.
- **Replay protection**: The timestamp in the signed message means a captured webhook cannot be replayed after the 5-minute window. Even within the window, the event ID enables idempotent processing.
- **Tenant isolation**: Compromising Tenant A's webhook secret has zero impact on Tenant B's webhook integrity. Each tenant's key is independent.
- **Simple verification**: HMAC-SHA256 is available in every language's standard library. Tenant integration requires ~10 lines of code: compute HMAC of timestamp + body, compare to header value.
- **Industry standard**: Stripe, GitHub, Shopify, and most webhook providers use HMAC-SHA256. Tenants' engineering teams are familiar with this pattern and likely already have verification code.

### Trade-offs
- **Tenants must implement signature verification**: Every tenant must add verification logic to their webhook endpoint. If they skip this step, they lose all forgery protection. We cannot enforce this — it is a tenant-side responsibility.
- **Shared secret management**: Both Settla and the tenant know the secret. If the tenant's infrastructure is compromised and the secret is leaked, an attacker can forge webhooks for that tenant until the key is rotated. Asymmetric signatures (where the tenant only has the public key) would eliminate this risk, at the cost of implementation complexity.
- **Key rotation coordination**: Rotating a webhook secret requires the tenant to update their verification code. During rotation, Settla must send dual signatures (old key and new key) to avoid a window where valid webhooks are rejected. This adds operational complexity.
- **Clock skew sensitivity**: The 5-minute replay window assumes reasonable clock synchronization between Settla's servers and the tenant's servers. Tenants with significant clock drift may reject valid webhooks or accept replayed ones.

### Mitigations
- **Clear documentation and SDKs**: The API documentation includes signature verification examples in Python, Node.js, Go, Ruby, and PHP. This reduces the barrier to correct implementation.
- **Verification test endpoint**: The webhook service exposes a test endpoint that sends a sample webhook to the tenant's URL, allowing them to verify their implementation before going live.
- **Dual-signature rotation**: During key rotation, both `X-Settla-Signature` and `X-Settla-Signature-V2` headers are sent (signed with old and new keys respectively) for a configurable transition period. The tenant can switch to the new key at their convenience.
- **Dead letter queue**: Failed deliveries (including those rejected due to signature mismatches during rotation) are preserved in the dead letter queue and can be replayed after the issue is resolved.

## References

- [Stripe Webhook Signatures](https://stripe.com/docs/webhooks/signatures) — industry standard reference implementation
- [HMAC: Keyed-Hashing for Message Authentication (RFC 2104)](https://tools.ietf.org/html/rfc2104)
