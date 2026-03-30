# Chapter 5.2: The Outbox Relay -- Bridging Postgres to NATS

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Describe the outbox relay's poll-publish-mark loop and its timing characteristics
2. Explain how `SubjectForEventType()` routes 30+ event types to the correct stream out of 12
3. Trace the message ID deduplication chain from outbox UUID to NATS Msg-Id header
4. Analyze the three failure scenarios and their recovery mechanisms
5. Understand the exponential backoff strategy for database polling failures
6. Explain why the relay does NOT return an error when MarkPublished fails

---

## The Core Problem: Atomic State + Event

The engine writes state changes and outbox entries in a single database transaction:

```
BEGIN;
  UPDATE transfers SET status = 'ON_RAMP_INITIATED' WHERE id = $1;
  INSERT INTO outbox (id, event_type, payload, ...) VALUES (...);
COMMIT;
```

If the engine tried to publish to NATS directly, a crash between the DB commit and the NATS publish would leave the system in an inconsistent state -- the transfer moved to ON_RAMP_INITIATED but no worker was notified. The transactional outbox pattern eliminates this dual-write bug by making the database the single source of truth.

---

## Relay Architecture

The relay is a separate goroutine that bridges the Postgres outbox table to all 12 NATS JetStream streams:

```
                                              NATS JetStream
                                         +-------------------------+
+------------------+    poll (20ms)      |                         |
|   Transfer DB    |<-------------------+|  SETTLA_TRANSFERS       |
|                  |    batch of 500    | |  SETTLA_PROVIDERS       |
|  outbox table    |----+               | |  SETTLA_LEDGER          |
| (partitioned     |    |  publishEntry | |  SETTLA_TREASURY        |
|  by created_at)  |    +-------------->| |  SETTLA_BLOCKCHAIN      |
|                  |                    | |  SETTLA_WEBHOOKS        |
|                  |<---mark published--+ |  SETTLA_PROVIDER_WEBH   |
+------------------+                      |  SETTLA_CRYPTO_DEPOSITS |
                                          |  SETTLA_BANK_DEPOSITS   |
                                          |  SETTLA_EMAILS          |
                                          |  SETTLA_POSITION_EVENTS |
                                          |  SETTLA_DLQ             |
                                          +-------------------------+
```

---

## The Relay Struct

From `node/outbox/relay.go`:

```go
const (
    DefaultPollInterval = 20 * time.Millisecond
    DefaultBatchSize    = int32(500)
    DefaultPartitions   = 8
)

type Relay struct {
    store         OutboxStore
    publisher     Publisher
    logger        *slog.Logger
    metrics       *RelayMetrics
    pollInterval  time.Duration
    batchSize     int32
    numPartitions int
}
```

**Why 20ms poll interval?** At 580 TPS sustained, approximately 12 entries are created per 20ms window. A 500-entry batch ensures the relay never falls behind -- it has 42x headroom. Even at 5,000 TPS peak (100 entries per 20ms), the batch size provides 5x headroom.

**Why batch of 500?** This is the sweet spot between latency and throughput:
- Smaller batches (50) increase poll frequency and database load
- Larger batches (5000) increase latency for the last entry in the batch
- 500 entries at ~2 KB each is ~1 MB per poll cycle -- well within Postgres query performance

---

## The Poll Loop

```go
func (r *Relay) Run(ctx context.Context) error {
    r.logger.Info("settla-outbox: relay started",
        "poll_interval", r.pollInterval,
        "batch_size", r.batchSize,
        "partitions", r.numPartitions,
    )

    consecutiveFailures := 0
    timer := time.NewTimer(r.pollInterval)
    defer timer.Stop()

    for {
        select {
        case <-ctx.Done():
            r.logger.Info("settla-outbox: relay stopped")
            return ctx.Err()
        case <-timer.C:
            if err := r.poll(ctx); err != nil {
                consecutiveFailures++
                shift := min(consecutiveFailures, 6)
                backoff := r.pollInterval * time.Duration(1<<shift)
                if backoff > maxBackoff {
                    backoff = maxBackoff
                }
                r.logger.Error("settla-outbox: poll cycle failed, backing off",
                    "error", err,
                    "consecutive_failures", consecutiveFailures,
                    "next_poll_in", backoff,
                )
                timer.Reset(backoff)
            } else {
                consecutiveFailures = 0
                timer.Reset(r.pollInterval)
            }
        }
    }
}
```

The backoff progression for a sustained failure:

```
Attempt 1:  20ms * 2^1 = 40ms
Attempt 2:  20ms * 2^2 = 80ms
Attempt 3:  20ms * 2^3 = 160ms
Attempt 4:  20ms * 2^4 = 320ms
Attempt 5:  20ms * 2^5 = 640ms
Attempt 6:  20ms * 2^6 = 1280ms
Attempt 7+: capped at 5s (maxBackoff)
```

On success, `consecutiveFailures` resets to 0 and the interval returns to 20ms immediately. This ensures the relay recovers quickly after a transient database hiccup.

---

## The Poll Function

```go
func (r *Relay) poll(ctx context.Context) error {
    entries, err := r.store.GetUnpublishedEntries(ctx, r.batchSize)
    if err != nil {
        return fmt.Errorf("settla-outbox: fetching unpublished entries: %w", err)
    }

    if r.metrics != nil {
        r.metrics.UnpublishedGauge.Set(float64(len(entries)))
        r.metrics.PollBatchSize.Observe(float64(len(entries)))
    }

    if len(entries) == 0 {
        return nil
    }

    for _, entry := range entries {
        if err := r.publishEntry(ctx, entry); err != nil {
            // Log per-entry failures but continue processing the batch.
            r.logger.Warn("settla-outbox: failed to publish entry",
                "entry_id", entry.ID,
                "event_type", entry.EventType,
                "tenant_id", entry.TenantID,
                "retry_count", entry.RetryCount,
                "error", err,
            )
        }
    }

    return nil
}
```

A critical design choice: **per-entry failures do not abort the batch.** If entry #3 in a batch of 500 fails to publish, entries #4-500 still get published. The failed entry's retry count is incremented, and it will be re-polled on the next cycle (if under max retries).

---

## Subject Routing: SubjectForEventType()

The routing function in `node/messaging/subjects.go` determines which NATS stream and subject each outbox entry is published to. With 12 streams, the relay must route over 30 distinct event types correctly:

```go
func SubjectForEventType(eventType string, tenantID uuid.UUID,
                          numPartitions int) string {
    prefix := eventPrefix(eventType)

    switch prefix {
    case "deposit":
        return DepositSubject(tenantID, numPartitions, eventType)

    case "transfer", "settlement", "onramp", "offramp", "refund":
        return TransferSubject(tenantID, numPartitions, eventType)

    case "provider":
        if strings.HasPrefix(eventType, "provider.inbound.") {
            rest := strings.TrimPrefix(eventType, "provider.inbound.")
            return ProviderWebhookSubject(tenantID, numPartitions, rest)
        }
        return ProviderSubject(tenantID, numPartitions, eventType)

    case "ledger":
        return LedgerSubject(tenantID, numPartitions, eventType)

    case "treasury", "position", "liquidity":
        return TreasurySubject(tenantID, numPartitions, eventType)

    case "blockchain":
        return BlockchainSubject(tenantID, numPartitions, eventType)

    case "webhook":
        return WebhookSubject(tenantID, numPartitions, eventType)

    case "email":
        return EmailSubject(tenantID, numPartitions, eventType)

    case "bank_deposit":
        return BankDepositSubject(tenantID, numPartitions, eventType)

    default:
        // Fallback: unknown events go to the transfer stream
        return TransferSubject(tenantID, numPartitions, eventType)
    }
}
```

The `eventPrefix()` helper extracts the first segment before the dot:

```go
func eventPrefix(eventType string) string {
    if idx := strings.IndexByte(eventType, '.'); idx > 0 {
        return eventType[:idx]
    }
    return eventType
}
```

**Routing examples:**

| Event Type | Prefix | Subject |
|---|---|---|
| `transfer.created` | `transfer` | `settla.transfer.partition.{N}.transfer.created` |
| `treasury.reserve` | `treasury` | `settla.treasury.partition.{N}.treasury.reserve` |
| `provider.onramp.execute` | `provider` | `settla.provider.command.partition.{N}.provider.onramp.execute` |
| `provider.inbound.onramp.webhook` | `provider` | `settla.provider.inbound.partition.{N}.onramp.webhook` |
| `ledger.post` | `ledger` | `settla.ledger.partition.{N}.ledger.post` |
| `blockchain.send` | `blockchain` | `settla.blockchain.partition.{N}.blockchain.send` |
| `webhook.deliver` | `webhook` | `settla.webhook.partition.{N}.webhook.deliver` |
| `deposit.tx.detected` | `deposit` | `settla.deposit.partition.{N}.deposit.tx.detected` |
| `email.send` | `email` | `settla.email.partition.{N}.email.send` |
| `bank_deposit.credit` | `bank_deposit` | `settla.bank_deposit.partition.{N}.bank_deposit.credit` |
| `position.updated` | `position` | `settla.treasury.partition.{N}.position.updated` |

Note the `provider` prefix requires a second-level check: `provider.inbound.*` events route to a different stream than `provider.onramp.*` events. This prevents outbound commands and inbound callbacks from interfering with each other.

Also note that `position.*` and `liquidity.*` events route to the treasury stream alongside `treasury.*` events, keeping all position-related processing centralized.

---

## The publishEntry Function

This is the heart of the relay:

```go
func (r *Relay) publishEntry(ctx context.Context, entry OutboxRow) error {
    subject := SubjectForEntry(entry, r.numPartitions)

    msg := outboxMessage{
        ID:            entry.ID,
        AggregateType: entry.AggregateType,
        AggregateID:   entry.AggregateID,
        TenantID:      entry.TenantID,
        EventType:     entry.EventType,
        Payload:       entry.Payload,
        IsIntent:      entry.IsIntent,
        CreatedAt:     entry.CreatedAt,
    }

    data, err := json.Marshal(msg)
    if err != nil {
        return fmt.Errorf("settla-outbox: marshalling entry %s: %w",
            entry.ID, err)
    }

    // Publish with outbox entry UUID as NATS dedup message ID.
    if err := r.publisher.Publish(ctx, subject,
                                   entry.ID.String(), data); err != nil {
        // Mark failed -- increments retry_count.
        if markErr := r.store.MarkFailed(ctx, entry.ID,
                                          entry.CreatedAt); markErr != nil {
            r.logger.Error("settla-outbox: failed to mark entry as failed",
                "entry_id", entry.ID,
                "mark_error", markErr,
                "publish_error", err,
            )
        }
        if r.metrics != nil {
            r.metrics.FailedTotal.Inc()
        }
        return fmt.Errorf("settla-outbox: publishing entry %s to %s: %w",
            entry.ID, subject, err)
    }

    // Mark published.
    if err := r.store.MarkPublished(ctx, entry.ID,
                                     entry.CreatedAt); err != nil {
        r.logger.Error("settla-outbox: failed to mark entry as published "+
            "(message already delivered to NATS, will retry mark on next poll)",
            "entry_id", entry.ID,
            "event_type", entry.EventType,
            "tenant_id", entry.TenantID,
            "error", err,
        )
        if r.metrics != nil {
            r.metrics.MarkFailedTotal.Inc()
        }
        // Don't return error -- message IS published.
        return nil
    }

    // Record metrics.
    if r.metrics != nil {
        latency := time.Since(entry.CreatedAt).Seconds()
        r.metrics.RelayLatency.Observe(latency)
        r.metrics.PublishedTotal.WithLabelValues(
            eventPrefix(entry.EventType)).Inc()
    }

    return nil
}
```

**The deduplication chain:**

```
Outbox Entry UUID (v7, time-ordered)
        |
        v
entry.ID.String() --> Nats-Msg-Id header
        |
        v
NATS dedup window (5 minutes)
        |
        v
Duplicate publish within window --> silently dropped by NATS
```

---

## Sequence Diagram: Normal Flow

```
Engine            DB (outbox)         Relay              NATS          Worker
  |                  |                  |                  |              |
  |-- BEGIN -------->|                  |                  |              |
  |   UPDATE status  |                  |                  |              |
  |   INSERT outbox  |                  |                  |              |
  |-- COMMIT ------->|                  |                  |              |
  |                  |                  |                  |              |
  |                  |<-- poll (20ms) --|                  |              |
  |                  |-- entries[] ---->|                  |              |
  |                  |                  |                  |              |
  |                  |                  |-- Publish ------>|              |
  |                  |                  |   (msgID=UUID)   |              |
  |                  |                  |<-- ack ----------|              |
  |                  |                  |                  |              |
  |                  |<-- MarkPublished-|                  |              |
  |                  |                  |                  |              |
  |                  |                  |                  |-- deliver -->|
  |                  |                  |                  |<-- ack -----|
```

---

## The Three Failure Scenarios

### Scenario 1: NATS Publish Fails

```
Relay              NATS              DB
  |                  |                |
  |-- Publish ------>|                |
  |<-- error --------|                |
  |                  |                |
  |-- MarkFailed ------------------>|  (retry_count++)
  |                  |                |
  |  (entry re-polled next cycle     |
  |   if retry_count < max_retries)  |
```

The entry stays unpublished. `MarkFailed` increments `retry_count`. When `retry_count >= max_retries` (5), the mock store's `GetUnpublishedEntries` excludes it. In production, a dead-letter process handles exhausted entries.

### Scenario 2: NATS Publish Succeeds, MarkPublished Fails

This is the most subtle case:

```
Relay              NATS              DB
  |                  |                |
  |-- Publish ------>|                |
  |<-- ack ----------|   (message    |
  |                  |    IS in NATS) |
  |                  |                |
  |-- MarkPublished ----------------->|
  |<-- DB error ----------------------|
  |                  |                |
  |  return nil (!)  |                |
  |                  |                |
  | (next poll: entry still unpublished)
  | (re-polls it)    |                |
  |-- Publish ------>|                |
  |  (same msgID)    |                |
  |<-- dedup drop ---|                |
  |                  |                |
  |-- MarkPublished ----------------->|
  |<-- success -----------------------|
```

**Why does the relay return nil (not an error)?** Because the message IS in NATS. Returning an error would cause the caller to log "failed to publish" -- misleading, since the message was published successfully. The relay's contract is: "ensure the message reaches NATS." The database mark is a bookkeeping optimization that prevents re-polling, not a correctness requirement.

NATS deduplication catches the duplicate on the next poll. The 5-minute dedup window provides ample time for the re-poll + re-publish cycle.

This behavior is tested explicitly in `TestMarkPublishedFailureDoesNotBlockRelay`:

```go
func TestMarkPublishedFailureDoesNotBlockRelay(t *testing.T) {
    // ...
    // poll should NOT return an error -- the NATS publish succeeded.
    if err := relay.poll(context.Background()); err != nil {
        t.Fatalf("poll should succeed even when MarkPublished fails: %v", err)
    }

    // Message was published to NATS.
    msgs := pub.getMessages()
    if len(msgs) != 1 {
        t.Fatalf("expected 1 published message, got %d", len(msgs))
    }

    // But entry is NOT marked published in DB (MarkPublished failed).
    if store.isPublished(entry.ID) {
        t.Error("entry should NOT be marked published...")
    }

    // Verify it was NOT counted as a publish failure.
    if got := store.failCount(entry.ID); got != 0 {
        t.Errorf("mark failure should not increment retry count, got %d", got)
    }
}
```

### Scenario 3: Relay Crashes After Publish, Before Mark

Identical to Scenario 2 from the perspective of recovery. On restart, the relay re-polls the unpublished entry, re-publishes with the same UUID as message ID, and NATS deduplication drops it.

---

## The Wire Format

The relay publishes this JSON structure to NATS:

```go
type outboxMessage struct {
    ID            uuid.UUID       `json:"id"`
    AggregateType string          `json:"aggregate_type"`
    AggregateID   uuid.UUID       `json:"aggregate_id"`
    TenantID      uuid.UUID       `json:"tenant_id"`
    EventType     string          `json:"event_type"`
    Payload       json.RawMessage `json:"payload"`
    IsIntent      bool            `json:"is_intent"`
    CreatedAt     time.Time       `json:"created_at"`
}
```

Workers receive this via the `unmarshalEvent()` function in `subscriber.go`, which maps `event_type` to `domain.Event.Type` and `payload` to `domain.Event.Data`.

---

## Outbox Table Cleanup

The outbox table is partitioned by `created_at` using daily partitions. The `Cleanup` goroutine in `node/outbox/cleanup.go` manages the lifecycle:

```go
type Cleanup struct {
    db             CleanupDB
    logger         *slog.Logger
    interval       time.Duration   // default: 1 hour
    retentionHours int             // default: 48
    lookAheadDays  int             // default: 3
}
```

Every hour, it:
1. **Drops** daily partitions older than 48 hours (instant DDL, no vacuum)
2. **Creates** daily partitions 3 days ahead
3. **Checks** the default partition for rows (indicates a partition gap)

At 50M entries/day, without cleanup the outbox table would grow by ~50M rows daily. Daily partitioning with 48-hour retention means at most ~100M rows exist at any time. `DROP TABLE` is an O(1) DDL operation -- no row-by-row deletion, no vacuum.

---

## Prometheus Metrics

The relay exposes five metrics:

```go
type RelayMetrics struct {
    PublishedTotal   *prometheus.CounterVec   // by event_prefix
    RelayLatency     prometheus.Histogram      // creation to publish
    UnpublishedGauge prometheus.Gauge          // batch depth
    PollBatchSize    prometheus.Histogram      // entries per poll
    FailedTotal      prometheus.Counter        // publish failures
    MarkFailedTotal  prometheus.Counter        // mark-after-publish failures
}
```

**Key alert thresholds:**
- `settla_outbox_unpublished_gauge > 1000` for 5 minutes: relay is falling behind
- `settla_outbox_relay_latency_seconds > 1.0` p99: relay latency is degrading
- `settla_outbox_failed_total` rate > 10/s: NATS may be unreachable
- `settla_outbox_mark_failed_total` rate > 1/s: database may be overloaded

---

## Key Insight

> The relay has exactly one job: get outbox entries from Postgres into NATS, exactly once. The combination of outbox UUID as NATS message ID and the 5-minute dedup window makes this safe across crashes, restarts, and database hiccups. The relay deliberately returns nil (not an error) when MarkPublished fails because the correctness guarantee is "message in NATS," not "database row updated." This subtle design choice prevents misleading error logs and unnecessary retry-count increments.

---

## Common Mistakes

1. **Returning an error when MarkPublished fails.** This would cause the poll function to log "failed to publish" for a message that IS in NATS. The relay's tests explicitly verify this does not happen.

2. **Using a short dedup window.** If the dedup window is shorter than the relay's worst-case restart time, crash-recovery re-publishes will not be deduplicated, causing double delivery to workers.

3. **Processing entries in parallel within publishEntry.** The relay publishes entries sequentially within a batch. Parallel publishing would lose ordering guarantees -- entries for the same tenant must be published in creation order to preserve causality on the partition.

4. **Aborting the batch on a single entry failure.** The relay logs per-entry failures but continues to the next entry. If entry #3 fails because its payload is malformed, entry #4 (a completely independent transfer) should still be published.

5. **Forgetting to partition the outbox table.** Without daily partitions, `GetUnpublishedEntries` would scan an ever-growing table. At 50M rows/day, query performance degrades rapidly.

---

## Exercises

1. **Latency Budget:** Calculate the end-to-end latency from engine commit to worker delivery, assuming: 10ms avg DB commit, 10ms avg poll wait (half of 20ms interval), 1ms NATS publish, 1ms NATS deliver. How does this compare to a direct publish from the engine?

2. **Backoff Analysis:** If the database is down for exactly 3 minutes, how many poll attempts does the relay make? What is the total time spent in backoff sleep? (Hint: trace the backoff progression from 40ms to 5s cap.)

3. **Dedup Window Sizing:** The current dedup window is 5 minutes. Calculate the minimum safe dedup window given: relay restart time of 30s, Kubernetes pod scheduling of 10s, NATS reconnect of 2s. What safety margin does 5 minutes provide?

4. **Batch Size Tuning:** Write a simulation that calculates relay throughput at different batch sizes (50, 100, 500, 1000, 5000) given a fixed poll interval of 20ms and a database query cost of 5ms + 0.01ms per entry. At what batch size does the relay become database-bound?

---

## What's Next

In Chapter 5.3, we will examine how `TenantPartition()` uses FNV-1a hashing to map tenants to partitions, solving the fundamental tension between per-tenant ordering and cross-tenant parallelism.
