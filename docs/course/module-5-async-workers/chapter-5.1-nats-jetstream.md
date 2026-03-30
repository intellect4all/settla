# Chapter 5.1: NATS JetStream -- The Message Backbone

**Reading time: 20 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why NATS JetStream was chosen over Kafka, RabbitMQ, and SQS for Settla's messaging layer
2. Describe the WorkQueue retention model and why it matters for worker-based architectures
3. Identify all 12 streams in Settla (11 domain + 1 DLQ) and explain the subject pattern each serves
4. Configure stream settings for deduplication, retention, and message size limits
5. Understand the DLQ stream's distinct retention policy and why it differs from other streams
6. Describe NATS authentication modes and why they are required in production

---

## Why NATS JetStream?

Settla processes 50M transactions per day -- roughly 580 TPS sustained with peaks of 3,000-5,000 TPS. The messaging layer must deliver messages reliably, support per-tenant ordering, and integrate cleanly with Go's concurrency model. Here is how the contenders compare:

```
+--------------------+--------+----------+--------+--------+
| Criterion          | NATS   | Kafka    | Rabbit | SQS    |
|                    | JS     |          | MQ     |        |
+--------------------+--------+----------+--------+--------+
| Latency (p99)      | <1ms   | 5-15ms   | 2-5ms  | 20-70ms|
| Operational weight | Light  | Heavy    | Medium | None   |
|                    |        | (ZK/KRaft|        | (managed)|
|                    |        | + JVMs)  |        |        |
| WorkQueue mode     | Native | No       | Basic  | Native |
| (consume-once)     |        | (consumer|        |        |
|                    |        |  groups) |        |        |
| Native Go client   | First  | Third    | Third  | SDK    |
|                    | party  | party    | party  |        |
| Dedup window       | Built  | Needs    | Needs  | 5-min  |
|                    | in     | external | plugin | built-in|
| Subject wildcards  | Yes    | No       | Routing| No     |
|                    | (>.*)  |          | keys   |        |
| Embedded mode      | Yes    | No       | No     | No     |
| (for testing)      |        |          |        |        |
| Partition ordering | Filter | Partition| No     | FIFO   |
| + parallelism      | subj.  | key      |        | groups |
| Max msg/sec        | 10M+   | 1M+      | 40-80K | ~3K/   |
| (single node)      |        |          |        | queue  |
+--------------------+--------+----------+--------+--------+
```

**The deciding factors for Settla:**

1. **WorkQueue retention** -- NATS JetStream has a first-class `WorkQueuePolicy` that removes messages once acknowledged. Kafka retains messages for a configurable period regardless of consumption, which means extra storage for 50M messages/day with no benefit.

2. **Sub-millisecond latency** -- When a transfer transitions from FUNDED to ON_RAMP_INITIATED, the relay publishes an outbox entry and the provider worker picks it up. Kafka's 5-15ms p99 adds up across the 6+ state transitions in a transfer lifecycle.

3. **Operational simplicity** -- NATS is a single static binary with zero external dependencies. Kafka requires ZooKeeper (or KRaft), JVM tuning, and separate schema registries. For a team that deploys 6+ server replicas and 8+ worker nodes, operational overhead is a real cost.

4. **Built-in deduplication** -- The outbox relay publishes with `Nats-Msg-Id = outbox_entry.UUID`. NATS drops duplicate message IDs within the 5-minute dedup window automatically. With Kafka, you would need to build deduplication logic in the consumer.

5. **Subject-based filtering** -- A single `SETTLA_TRANSFERS` stream can serve 8 partition consumers using subject filters like `settla.transfer.partition.3.>`. Kafka achieves the same with partition assignment, but cannot filter within a partition by subject pattern.

---

## Stream Configuration in Code

All stream definitions live in `node/messaging/streams.go`. Here is the actual `AllStreams()` function:

```go
func AllStreams() []StreamDefinition {
    return []StreamDefinition{
        {
            Name:     StreamTransfers,
            Subjects: []string{"settla.transfer.partition.*.>"},
        },
        {
            Name:     StreamProviders,
            Subjects: []string{"settla.provider.command.partition.*.>"},
        },
        {
            Name:     StreamLedger,
            Subjects: []string{"settla.ledger.partition.*.>"},
        },
        {
            Name:     StreamTreasury,
            Subjects: []string{"settla.treasury.partition.*.>"},
        },
        {
            Name:     StreamBlockchain,
            Subjects: []string{"settla.blockchain.partition.*.>"},
        },
        {
            Name:     StreamWebhooks,
            Subjects: []string{"settla.webhook.partition.*.>"},
        },
        {
            Name:     StreamProviderWebhooks,
            Subjects: []string{
                "settla.provider.inbound.partition.*.>",
                "settla.provider.inbound.raw",
            },
        },
        {
            Name:     StreamCryptoDeposits,
            Subjects: []string{"settla.deposit.partition.*.>"},
        },
        {
            Name:     StreamEmails,
            Subjects: []string{"settla.email.partition.*.>"},
        },
        {
            Name:     StreamBankDeposits,
            Subjects: []string{"settla.bank_deposit.partition.*.>",
                               "settla.inbound.bank.>"},
        },
        {
            Name:     StreamPositionEvents,
            Subjects: []string{"settla.position.event.>"},
        },
        {
            Name:     StreamNameDLQ,
            Subjects: []string{"settla.dlq.>"},
        },
    }
}
```

Notice how `SETTLA_PROVIDERS` uses `settla.provider.command.partition.*.>` while `SETTLA_PROVIDER_WEBHOOKS` uses `settla.provider.inbound.partition.*.>`. These subject prefixes prevent overlap -- a critical design choice tested explicitly in `TestAllStreams_NoSubjectOverlap`.

Also note the two special multi-subject streams:
- `SETTLA_PROVIDER_WEBHOOKS` captures both partitioned inbound webhooks and raw webhook payloads (`settla.provider.inbound.raw`) before Go-side normalization
- `SETTLA_BANK_DEPOSITS` captures both partitioned bank deposit events and non-partitioned inbound bank credit notifications (`settla.inbound.bank.>`)

---

## Shared Stream Settings

Every stream shares three critical constants:

```go
const (
    StreamMaxAge          = 7 * 24 * time.Hour    // 168 hours
    StreamMaxMsgSize      = 1_048_576              // 1 MB
    StreamDuplicateWindow = 5 * time.Minute        // dedup window
)
```

**Why 7-day max age?** It provides a generous window for debugging and replaying failed messages without unbounded storage growth. At 50M messages/day with an average payload of 500 bytes, that is roughly 250 GB/week -- manageable with FileStorage.

**Why 5-minute dedup window?** The outbox relay polls every 20ms. If it publishes a message but crashes before marking it as published in the database, on restart the same entry will be re-polled and re-published. The 5-minute window ensures NATS drops the duplicate. This covers even worst-case crash-recovery scenarios where the relay takes minutes to restart (Kubernetes pod scheduling, NATS reconnect, etc.).

**Why 1 MB max message size?** Transfer payloads are typically 1-5 KB. The 1 MB limit protects against malformed entries that could consume disproportionate memory. Webhook payloads with embedded response bodies are the largest at ~50 KB.

---

## The CreateStreams Function

Streams are created idempotently on startup via `CreateOrUpdateStream`:

```go
func CreateStreams(ctx context.Context, js jetstream.JetStream, replicas int) error {
    if replicas < 1 {
        replicas = 1
    }

    for _, def := range AllStreams() {
        cfg := jetstream.StreamConfig{
            Name:       def.Name,
            Subjects:   def.Subjects,
            Retention:  jetstream.WorkQueuePolicy,
            Storage:    jetstream.FileStorage,
            MaxAge:     StreamMaxAge,
            MaxMsgSize: StreamMaxMsgSize,
            Duplicates: StreamDuplicateWindow,
            Discard:    jetstream.DiscardOld,
            Replicas:   replicas,
        }

        if def.Name == StreamNameDLQ {
            cfg.Retention = jetstream.LimitsPolicy
            cfg.MaxAge = 30 * 24 * time.Hour
        }

        if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
            return fmt.Errorf("settla-messaging: ensuring stream %s: %w",
                def.Name, err)
        }
    }

    return nil
}
```

**Key configuration choices:**

| Setting | Value | Rationale |
|---------|-------|-----------|
| `Retention` | `WorkQueuePolicy` | Messages are deleted after ack. No need for replay -- the outbox is the source of truth. |
| `Storage` | `FileStorage` | Survives NATS restarts. MemoryStorage would lose all in-flight messages. |
| `Discard` | `DiscardOld` | When MaxAge is reached, oldest messages are dropped first. |
| `Replicas` | 1 (dev) / 3 (prod) | Production requires a 3-node NATS cluster for HA. |

---

## The Complete Streams Table

```
+-----------------------------+-------------------------------------------+--------------------+
| Stream                      | Subject Pattern                           | Consumer           |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_TRANSFERS            | settla.transfer.partition.*.>             | TransferWorker     |
|                             |                                           | (8 partitions)     |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_PROVIDERS            | settla.provider.command.partition.*.>     | ProviderWorker     |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_LEDGER               | settla.ledger.partition.*.>               | LedgerWorker       |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_TREASURY             | settla.treasury.partition.*.>             | TreasuryWorker     |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_BLOCKCHAIN           | settla.blockchain.partition.*.>           | BlockchainWorker   |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_WEBHOOKS             | settla.webhook.partition.*.>              | WebhookWorker      |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_PROVIDER_WEBHOOKS    | settla.provider.inbound.partition.*.>     | InboundWebhook-    |
|                             | settla.provider.inbound.raw               | Worker             |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_CRYPTO_DEPOSITS      | settla.deposit.partition.*.>              | DepositWorker      |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_BANK_DEPOSITS        | settla.bank_deposit.partition.*.>         | BankDepositWorker  |
|                             | settla.inbound.bank.>                     |                    |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_EMAILS               | settla.email.partition.*.>                | EmailWorker        |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_POSITION_EVENTS      | settla.position.event.>                   | PositionEvent-     |
|                             |                                           | Writer             |
+-----------------------------+-------------------------------------------+--------------------+
| SETTLA_DLQ                  | settla.dlq.>                              | DLQMonitor         |
+-----------------------------+-------------------------------------------+--------------------+
```

All 12 streams (11 domain + 1 DLQ). A few notable design choices:

- `SETTLA_POSITION_EVENTS` uses `settla.position.event.>` without partition-based subjects. It is an event streaming/audit log stream consumed by the `PositionEventWriter`, which batch-writes position events to the database. It does not need per-tenant ordering because it writes events in arrival order, not command-processing order.

- `SETTLA_BANK_DEPOSITS` has two subject patterns: partitioned bank deposit events and non-partitioned inbound bank credit notifications (`settla.inbound.bank.>`). The inbound credits arrive from banking partner webhooks and are routed to the correct session by account number lookup.

- `SETTLA_PROVIDER_WEBHOOKS` has two subject patterns: partitioned inbound webhooks and a raw subject (`settla.provider.inbound.raw`) for webhook payloads that need Go-side normalization before being routed to the correct tenant partition.

---

## The DLQ Stream -- A Different Beast

The dead letter queue uses `LimitsPolicy` instead of `WorkQueuePolicy`, with a 30-day retention:

```go
if def.Name == StreamNameDLQ {
    cfg.Retention = jetstream.LimitsPolicy
    cfg.MaxAge = 30 * 24 * time.Hour
}
```

**Why LimitsPolicy?** With `WorkQueuePolicy`, messages are deleted once acked by any consumer. If the DLQ monitor is down for maintenance, messages would be dropped silently. In a payment system, silently losing dead-lettered messages is unacceptable. `LimitsPolicy` ensures DLQ messages persist for 30 days regardless of consumer state.

The DLQ subject format is `settla.dlq.{streamName}.{eventType}`, built by:

```go
func DLQSubject(streamName string, eventType string) string {
    return fmt.Sprintf("settla.dlq.%s.%s", streamName, eventType)
}
```

For example: `settla.dlq.SETTLA_TRANSFERS.transfer.created`

---

## NATS Authentication

Settla supports two authentication modes for NATS connections, configured via environment variables:

1. **Token auth:** `SETTLA_NATS_TOKEN` -- a shared token applied via `WithNATSToken()` client option
2. **User/password auth:** `SETTLA_NATS_USER` / `SETTLA_NATS_PASSWORD` -- applied via `WithNATSUserInfo()` client option

Both `settla-server` and `settla-node` warn at startup if no auth is configured in production. Without authentication, any pod on the cluster network can subscribe to all streams (reading tenant financial data) or publish fake commands -- a critical security gap in a payment system.

---

## Consumer Configuration

When a worker subscribes to a stream, it creates a durable consumer with these settings:

```go
consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
    Name:          consumerName,
    Durable:       consumerName,
    FilterSubject: filterSubject,
    AckPolicy:     jetstream.AckExplicitPolicy,
    AckWait:       AckWait,         // 30 seconds
    MaxDeliver:    MaxRetries,      // 6 (1 initial + 5 retries)
    BackOff:       BackoffSchedule, // [1s, 5s, 30s, 2m, 10m]
    MaxAckPending: maxAckPending,   // poolSize * 4 (min 100)
})
```

The backoff schedule is defined as:

```go
var BackoffSchedule = []time.Duration{
    1 * time.Second,
    5 * time.Second,
    30 * time.Second,
    2 * time.Minute,
    10 * time.Minute,
}
```

After 6 delivery attempts (1 initial + 5 retries), the message is published to the DLQ and acked, removing it from the work queue. This prevents a single poison message from blocking an entire partition.

---

## Stream Topology Diagram

```
                    +-----------+
                    |  Engine   |
                    | (pure SM) |
                    +-----+-----+
                          |
                  writes atomically
                          |
                    +-----v-----+
                    |  Outbox   |
                    |  Table    |
                    +-----+-----+
                          |
                   polls every 20ms
                          |
                    +-----v-----+
                    |  Outbox   |
                    |  Relay    |
                    +-----+-----+
                          |
          SubjectForEventType() routing
                          |
    +------+------+------+------+------+------+
    |      |      |      |      |      |      |
+---v--+ +-v---+ +v---+ +v---+ +v----+ +v----+ +v---------+
|SETTLA| |SETTLA| |SETTLA| |SETTLA| |SETTLA | |SETTLA | |SETTLA_   |
|TRANS-| |_PROV-| |_LEDG-| |_TREA-| |_BLOCK-| |_WEB-  | |PROVIDER_ |
|FERS  | |IDERS | |ER    | |SURY  | |CHAIN  | |HOOKS  | |WEBHOOKS  |
+--+---+ +--+---+ +--+---+ +--+---+ +---+---+ +---+---+ +----+-----+
   |        |        |        |         |         |            |
   v        v        v        v         v         v            v
Transfer  Provider  Ledger  Treasury  Block-   Webhook    Inbound
Worker    Worker    Worker  Worker    chain    Worker     Webhook
(x8)      (x8)     (x8)    (x8)      Worker   (x8)       Worker
                                      (x8)                (x8)

    +------+------+------+------+
    |      |      |      |      |
+---v--+ +-v---+ +v---+ +v---------+ +v---------+
|SETTLA| |SETTLA| |SETTLA| |SETTLA_    | |SETTLA_    |
|_CRY- | |_BANK | |_EMAI-| |POSITION   | |DLQ        |
|PTO   | |_DEPS | |LS    | |_EVENTS    | |           |
+--+---+ +--+---+ +--+---+ +-----+-----+ +-----+-----+
   |        |        |            |              |
   v        v        v            v              v
Deposit  BankDep   Email     Position-       DLQ
Worker   Worker    Worker    EventWriter     Monitor
(x8)     (x8)     (x8)     (batch write)   (ring buf)

    All failed messages after MaxRetries --> SETTLA_DLQ
```

---

## Key Insight

> WorkQueue retention is the single most important stream configuration choice. It means NATS acts as a task queue, not a log. Once a worker acks a message, it is gone from the stream. The outbox table in Postgres is the durable record -- NATS is the delivery mechanism. This separation of concerns means NATS never needs to store more than the in-flight message volume (typically under 10,000 messages across all streams), regardless of daily transaction volume.

---

## Common Mistakes

1. **Using LimitsPolicy for domain streams.** LimitsPolicy retains messages even after ack. At 50M messages/day, this would grow NATS storage by ~25 GB/day with no benefit -- the outbox table already has the durable record. Only the DLQ uses LimitsPolicy (for 30-day persistence).

2. **Setting Replicas=3 without a NATS cluster.** NATS will refuse to create the stream if fewer nodes are available than replicas requested. The code defaults to 1 and only uses 3 in production.

3. **Overlapping subject patterns between streams.** If `SETTLA_PROVIDERS` used `settla.provider.>` instead of `settla.provider.command.>`, it would also capture inbound webhook subjects (`settla.provider.inbound.>`), causing messages to be delivered to the wrong worker.

4. **Ignoring the dedup window in crash recovery.** If you set `StreamDuplicateWindow` to 30 seconds and the relay takes 2 minutes to restart, re-published entries will not be deduplicated, potentially causing double delivery. The 5-minute window provides ample safety margin.

5. **Running NATS without authentication in production.** Without auth, any pod on the cluster network can subscribe to all streams (reading tenant financial data) or publish fake commands. Settla supports token auth (`SETTLA_NATS_TOKEN`) and user/password auth (`SETTLA_NATS_USER`/`SETTLA_NATS_PASSWORD`) via `WithNATSToken()` and `WithNATSUserInfo()` client options. Both `settla-server` and `settla-node` warn at startup if no auth is configured in production.

---

## Exercises

1. **Subject Overlap Analysis:** Write a test that verifies no two streams in `AllStreams()` could match the same concrete subject. Hint: generate sample subjects for each stream and verify `StreamForSubject()` returns the expected stream name.

2. **Retention Calculation:** Calculate the maximum NATS storage required at steady state with WorkQueue retention, assuming 580 TPS average, 2 KB average message size, and workers processing within 5 seconds. Compare this to what LimitsPolicy would require over 7 days.

3. **DLQ Impact Assessment:** If the DLQ monitor is offline for 48 hours during a 5,000 TPS peak, how many messages could accumulate in the DLQ stream? What is the storage impact at 2 KB average message size?

4. **Replicas Trade-off:** Explain why the code uses `Replicas: 1` for development. What failure scenarios does `Replicas: 3` protect against that `Replicas: 1` does not? What is the write latency impact?

---

## What's Next

In Chapter 5.2, we will trace how the outbox relay polls the database every 20ms, routes each entry to the correct NATS subject using `SubjectForEventType()`, and handles the edge case where NATS publish succeeds but the database mark-as-published fails.
