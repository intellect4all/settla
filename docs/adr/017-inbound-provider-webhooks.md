# ADR-017: Inbound Provider Webhook Architecture

**Status:** Accepted
**Date:** 2026-03-09
**Authors:** Engineering Team

## Context

Settla integrates with external payment providers (fiat on/off-ramp services, blockchain networks) that send asynchronous notifications via webhooks. These notifications report the outcome of operations that Settla initiated: payment completed, payment failed, funds received, blockchain transaction confirmed.

At 50M transfers/day, each transfer generates 2-4 provider webhook callbacks across its lifecycle (e.g., payment initiated confirmation, payment completed, settlement confirmation). This means **100-200M inbound webhooks per day**, or **1,150-2,300 webhooks/second sustained**.

The reliability requirements are strict:

1. **No lost notifications**: a missed "payment completed" webhook means a transfer stays in "submitted" indefinitely. At 50M transfers/day, even 0.001% loss = 500 stuck transfers/day.
2. **No duplicate processing**: providers often retry webhooks (when they don't receive a 200 response quickly enough). Processing a "payment completed" webhook twice could result in double ledger postings or double treasury releases.
3. **No provider blocking**: webhook endpoints must respond within the provider's timeout (typically 5-30 seconds). If our processing takes longer, the provider marks the webhook as failed and retries, creating duplicate delivery.
4. **Provider format isolation**: each provider has a different payload format, signature scheme, and event taxonomy. Changes to one provider's format must not affect others.

We evaluated two approaches:

| Approach | Response time | Dedup | Processing | Provider isolation |
|----------|-------------|-------|------------|-------------------|
| Synchronous processing | 50-500ms | Application-level | Inline | Shared code path |
| Async with immediate ack | <10ms | Redis dedup | NATS workers | Per-provider normalizer |

The synchronous approach processes the webhook inline before responding. At 2,300 webhooks/sec, this means 2,300 concurrent processing goroutines, each potentially making DB queries, NATS publishes, and state machine transitions. A slow database query or NATS hiccup causes webhook response times to spike, triggering provider retries and creating a cascade.

## Decision

We chose **immediate acknowledgment with async processing**: the webhook endpoint always returns HTTP 200 within milliseconds, then processes the notification asynchronously via NATS workers.

### Webhook Endpoint Flow

```
Provider HTTP POST → Webhook Endpoint
  │
  ├─ 1. Verify signature (provider-specific: HMAC-SHA256, RSA, etc.)
  │     → 401 if invalid (reject immediately)
  │
  ├─ 2. Dedup check: Redis SETNX on key "webhook:dedup:{provider}:{event_id}"
  │     → TTL: 72 hours
  │     → If key exists: return 200 (already processed, idempotent response)
  │
  ├─ 3. Normalize payload via provider-specific normalizer
  │     → Converts provider format to internal ProviderEvent struct
  │     → Extracts: event_type, transfer_id, status, amount, metadata
  │
  ├─ 4. Publish normalized event to NATS
  │     → Subject: settla.provider.inbound.{onramp|offramp}.webhook
  │     → Nats-Msg-Id: webhook-{provider}-{transfer_id}-{status} (NATS-level dedup)
  │
  └─ 5. Return HTTP 200 with empty body
        → Total time: <10ms
```

### Deduplication Strategy

Dedup currently operates at two levels (Redis dedup is planned but not yet implemented):

1. **NATS message dedup (transport level)**: the `Nats-Msg-Id` header is set to `webhook-{provider}-{transfer_id}-{status}`. NATS JetStream's built-in dedup window (2 minutes) catches rapid retries and concurrent duplicate requests.

2. **Idempotency key (engine level)**: the engine's `Handle*Result()` methods use the provider event ID as an idempotency key. Even if transport-layer dedup fails, the engine rejects duplicate state transitions.

> **Planned**: Redis endpoint-level dedup (`SETNX webhook:dedup:{provider}:{transfer_id}:{status}` with 72-hour TTL) is not yet implemented. Until it is, the NATS dedup window of 2 minutes is the first line of defence against rapid retries. The engine idempotency key is the final safety net.

### Provider Normalizers

Each provider has a dedicated normalizer that implements a common interface:

```go
type WebhookNormalizer interface {
    VerifySignature(req *http.Request, secret []byte) error
    Normalize(payload []byte) (*ProviderEvent, error)
    ProviderName() string
}
```

Normalizers are registered at startup:

```go
registry.Register("chimoney", chimoney.NewNormalizer())
registry.Register("flutterwave", flutterwave.NewNormalizer())
registry.Register("yellowcard", yellowcard.NewNormalizer())
```

The webhook endpoint routes to the correct normalizer based on the URL path: `/webhooks/{provider_name}`. Adding a new provider means implementing a new normalizer — no changes to the endpoint, dedup, or processing infrastructure.

### Async Processing Worker

The provider webhook worker consumes normalized events from NATS:

```
NATS: settla.provider.webhook.{provider}.{event_type}
  → Provider Webhook Worker
    → Map provider event to engine method:
        payment.completed → Engine.HandleProviderPaymentCompleted()
        payment.failed    → Engine.HandleProviderPaymentFailed()
        tx.confirmed      → Engine.HandleBlockchainConfirmation()
    → Engine writes state change + outbox entries (per ADR-014, ADR-015)
```

### Failure Handling

| Failure | Behavior |
|---------|----------|
| Signature verification fails | Return 401, do not process |
| Redis dedup unavailable | Log warning, proceed without dedup (NATS + engine dedup still active) |
| Normalization fails (bad payload) | Return 400 (payload rejected), provider must retry with corrected payload |
| NATS unavailable | Return 503 (service unavailable), provider retries |
| Worker processing fails | NATS redelivery (max 5 attempts), then dead letter |

The critical design choice is **always returning 200 to the provider**, even when internal processing has issues. Provider retry storms (caused by returning 500) create cascading failures. It is better to ack the webhook and handle failures internally through NATS redelivery and dead letter queues.

## Consequences

### Benefits
- **Providers never see errors**: returning 200 within <10ms means providers never retry due to timeouts or errors from our side. This eliminates retry storms, which are the most common cause of webhook-related outages.
- **Triple-layer dedup**: Redis (72h), NATS (2min), and engine idempotency keys ensure that duplicate webhooks are harmless at every processing stage. No double ledger postings, no double treasury releases.
- **Provider isolation**: each provider's payload format, signature scheme, and event taxonomy is encapsulated in a normalizer. A breaking change in Flutterwave's webhook format requires only updating `flutterwave.NewNormalizer()` — no changes to the endpoint, dedup, or processing infrastructure.
- **Backpressure handling**: if processing falls behind, NATS buffers the events. The endpoint continues acking webhooks at line speed while workers catch up. This decouples ingestion throughput from processing throughput.
- **Auditable**: every normalized webhook event is published to NATS (and stored in JetStream for 7 days per ADR-005). This provides a complete audit trail of every provider notification received.

### Trade-offs
- **Delayed processing**: the async architecture adds latency between webhook receipt and transfer state advancement. Typical: 50-100ms (outbox relay + NATS delivery). Maximum: seconds if worker queues are backed up. This is acceptable since provider operations themselves take seconds to minutes.
- **Redis dedup storage**: at 200M webhooks/day with 72-hour TTL, the dedup keyspace holds up to 600M keys. Each key is ~60 bytes (prefix + provider + event_id), totaling ~36GB. This requires dedicated Redis memory or a separate Redis instance for dedup.
- **File buffer recovery complexity**: when NATS is unavailable and webhooks are buffered to local files, recovery requires replaying files in order. This is a rare failure mode (NATS cluster outage) but requires operational procedures.
- **Always-200 masks real errors**: if the normalizer has a bug that silently drops events, the provider sees success while events are lost. Monitoring must detect the gap between received webhooks and processed events.

### Mitigations
- **Webhook receipt vs processing reconciliation**: a metric compares `settla.webhooks.received` (incremented at endpoint) with `settla.webhooks.processed` (incremented at worker). A divergence >1% over 5 minutes triggers an alert.
- **Dead letter monitoring**: normalized events that fail processing are routed to `settla.provider.webhook.deadletter.>`. Alert on dead letter queue depth >0. Daily review of dead letter events is an operational procedure.
- **Redis dedup memory management**: dedup keys use a separate Redis database (logical separation) with `maxmemory-policy: volatile-ttl` to ensure keys with the shortest remaining TTL are evicted first under memory pressure.
- **File buffer rotation and replay**: buffered files are rotated hourly with a maximum of 100MB per file. A recovery script replays files in chronological order when NATS recovers, with dedup preventing duplicates from replay.

## Threshold Triggers for Revisiting

- **Webhook volume exceeds Redis dedup capacity (>600M concurrent keys)**: if provider webhook frequency increases beyond 72-hour dedup window capacity. Migration path: Bloom filter for dedup (probabilistic, ~1% false positive rate, 10x memory reduction) or database-backed dedup with TTL indexes.
- **Providers require synchronous confirmation**: if a provider requires the webhook response to include processing status (e.g., "transfer advanced to settled"). Migration path: synchronous processing with tight SLAs, or webhook response callback pattern.
- **File buffer events during NATS outage exceed 1 hour of volume**: indicates NATS reliability is insufficient. Migration path: persistent message queue (Kafka, Amazon SQS) as primary transport instead of NATS.

## References

- [Webhook Best Practices](https://webhooks.fyi/) — community webhook standards
- [Idempotent Receiver Pattern](https://www.enterpriseintegrationpatterns.com/patterns/messaging/IdempotentReceiver.html) — Gregor Hohpe
- ADR-014 (Transactional Outbox) — how worker results flow back into the engine
- ADR-015 (Pure State Machine Engine) — the engine methods that process provider results
- ADR-012 (HMAC-SHA256 Webhook Signatures) — outbound webhook signing (this ADR covers inbound)
