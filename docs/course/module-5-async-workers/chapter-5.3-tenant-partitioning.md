# Chapter 5.3: Tenant Partitioning -- Ordering vs. Parallelism

**Reading time: 20 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain why per-tenant ordering is a hard requirement for a settlement system
2. Describe how `TenantPartition()` uses FNV-1a hashing for deterministic partition assignment
3. Trace a concrete subject from tenant UUID through hash to NATS subject string
4. Calculate the optimal partition count given tenant count and throughput targets
5. Understand the consumer-per-partition model and its scaling implications
6. Distinguish between partitioned streams (10 of 12) and non-partitioned streams (POSITION_EVENTS, DLQ)

---

## The Fundamental Problem

Settla processes transfers through a multi-step state machine:

```
CREATED --> FUNDED --> ON_RAMP_INITIATED --> SETTLEMENT_STARTED
    --> OFF_RAMP_INITIATED --> COMPLETED
```

Each transition generates an outbox entry that a worker must process. Consider what happens when two events for the same transfer -- say `transfer.created` and `transfer.funded` -- arrive at two different workers simultaneously:

```
Worker A receives: transfer.created  --> calls Engine.FundTransfer()
Worker B receives: transfer.funded   --> calls Engine.InitiateOnRamp()

If Worker B runs first:
  Engine.InitiateOnRamp() fails because transfer is still CREATED, not FUNDED.
  NATS retries. Eventually Worker A completes, then Worker B succeeds.
  But this wastes retries and time.

Worse case:
  Worker B runs, fails, gets dead-lettered after 6 retries.
  The transfer is stuck in FUNDED forever because no one calls InitiateOnRamp.
```

The fix: **all events for the same tenant must be processed in order.** Since a tenant's transfers share the same tenant ID, routing all events for a tenant to the same partition -- and processing that partition serially -- guarantees ordering.

But serial processing of ALL events across ALL tenants would be a bottleneck at 580 TPS. The solution: hash tenants across N partitions, giving per-tenant ordering with N-way parallelism.

---

## The TenantPartition Function

From `node/messaging/nats.go`:

```go
// TenantPartition deterministically maps a tenant ID to a partition number
// using FNV-1a hashing. All events for the same tenant always land on the
// same partition, guaranteeing per-tenant ordering.
func TenantPartition(tenantID uuid.UUID, numPartitions int) int {
    h := fnv.New32a()
    h.Write(tenantID[:])
    return int(h.Sum32() % uint32(numPartitions))
}
```

**Why FNV-1a?**

1. **Deterministic:** Same input always produces the same output. No randomness, no seed.
2. **Fast:** FNV-1a is a non-cryptographic hash. It processes 16 bytes (UUID) in ~10ns. Cryptographic hashes like SHA-256 are 10-50x slower and provide no benefit here -- we need distribution, not collision resistance.
3. **Good distribution:** FNV-1a has excellent avalanche properties for short inputs. A single-bit change in the UUID changes ~50% of the hash bits, spreading tenants evenly across partitions.
4. **Standard library:** `hash/fnv` is in Go's standard library. No external dependencies.

---

## Tracing a Concrete Example

Let us trace the seed tenant Lemfi (UUID `a0000000-0000-0000-0000-000000000001`) through the partitioning logic:

```
Input:  a0000000-0000-0000-0000-000000000001 (16 bytes)
        [0xa0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
         0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01]

FNV-1a: h.Write(tenantID[:]) --> produces a uint32 hash

Partition: hash % 8 = some value in [0, 7]

Subject: settla.transfer.partition.{partition}.transfer.created
```

The test `TestTenantPartition_SameTenantAlwaysSamePartition` verifies determinism across 1000 iterations:

```go
func TestTenantPartition_SameTenantAlwaysSamePartition(t *testing.T) {
    tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
    numPartitions := 8

    first := TenantPartition(tenantID, numPartitions)

    for i := 0; i < 1000; i++ {
        got := TenantPartition(tenantID, numPartitions)
        if got != first {
            t.Fatalf("iteration %d: expected partition %d, got %d",
                i, first, got)
        }
    }
}
```

And `TestTenantPartition_DifferentTenantsCanDiffer` verifies distribution:

```go
func TestTenantPartition_DifferentTenantsCanDiffer(t *testing.T) {
    numPartitions := 8
    tenants := []uuid.UUID{
        uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
        uuid.MustParse("b0000000-0000-0000-0000-000000000002"),
        // ... 6 tenants total
    }

    partitions := make(map[int]bool)
    for _, tenantID := range tenants {
        p := TenantPartition(tenantID, numPartitions)
        partitions[p] = true
    }

    if len(partitions) < 2 {
        t.Errorf("expected at least 2 distinct partitions for 6 tenants")
    }
}
```

---

## Subject Builder Functions

Each stream has its own subject builder that incorporates the partition number:

```go
// Transfer events
func TransferSubject(tenantID uuid.UUID, numPartitions int,
                      eventType string) string {
    partition := TenantPartition(tenantID, numPartitions)
    return PartitionSubject(partition, eventType)
}

func PartitionSubject(partition int, eventType string) string {
    return fmt.Sprintf("%s.partition.%d.%s",
        SubjectPrefixTransfer, partition, eventType)
}

// Provider commands
func ProviderSubject(tenantID uuid.UUID, numPartitions int,
                      eventType string) string {
    partition := TenantPartition(tenantID, numPartitions)
    return fmt.Sprintf("%s.partition.%d.%s",
        SubjectPrefixProvider, partition, eventType)
}

// Treasury events -- now partitioned by tenant
func TreasurySubject(tenantID uuid.UUID, numPartitions int,
                      eventType string) string {
    partition := TenantPartition(tenantID, numPartitions)
    return fmt.Sprintf("%s.partition.%d.%s",
        SubjectPrefixTreasury, partition, eventType)
}

// Deposit events
func DepositSubject(tenantID uuid.UUID, numPartitions int,
                     eventType string) string {
    partition := TenantPartition(tenantID, numPartitions)
    return fmt.Sprintf("%s.partition.%d.%s",
        SubjectPrefixDeposit, partition, eventType)
}

// Email events
func EmailSubject(tenantID uuid.UUID, numPartitions int,
                   eventType string) string {
    partition := TenantPartition(tenantID, numPartitions)
    return fmt.Sprintf("%s.partition.%d.%s",
        SubjectPrefixEmail, partition, eventType)
}

// Bank deposit events
func BankDepositSubject(tenantID uuid.UUID, numPartitions int,
                         eventType string) string {
    partition := TenantPartition(tenantID, numPartitions)
    return fmt.Sprintf("%s.partition.%d.%s",
        SubjectPrefixBankDeposit, partition, eventType)
}
```

All 10 partitioned streams use the same `TenantPartition()` function, ensuring a tenant always maps to the same partition number across all streams.

---

## Partitioned vs. Non-Partitioned Streams

Of the 12 streams, 10 use tenant-based partitioning. The two exceptions:

```
+-----------------------------+-------------------------------------------+------------------+
| Stream                      | Subject Pattern                           | Partitioned?     |
+-----------------------------+-------------------------------------------+------------------+
| SETTLA_TRANSFERS            | settla.transfer.partition.*.>             | Yes (by tenant)  |
| SETTLA_PROVIDERS            | settla.provider.command.partition.*.>     | Yes (by tenant)  |
| SETTLA_LEDGER               | settla.ledger.partition.*.>               | Yes (by tenant)  |
| SETTLA_TREASURY             | settla.treasury.partition.*.>             | Yes (by tenant)  |
| SETTLA_BLOCKCHAIN           | settla.blockchain.partition.*.>           | Yes (by tenant)  |
| SETTLA_WEBHOOKS             | settla.webhook.partition.*.>              | Yes (by tenant)  |
| SETTLA_PROVIDER_WEBHOOKS    | settla.provider.inbound.partition.*.>     | Yes (by tenant)  |
| SETTLA_CRYPTO_DEPOSITS      | settla.deposit.partition.*.>              | Yes (by tenant)  |
| SETTLA_BANK_DEPOSITS        | settla.bank_deposit.partition.*.>         | Yes (by tenant)  |
| SETTLA_EMAILS               | settla.email.partition.*.>                | Yes (by tenant)  |
+-----------------------------+-------------------------------------------+------------------+
| SETTLA_POSITION_EVENTS      | settla.position.event.>                   | No               |
| SETTLA_DLQ                  | settla.dlq.>                              | No               |
+-----------------------------+-------------------------------------------+------------------+
```

**Why is SETTLA_POSITION_EVENTS unpartitioned?** It is an event streaming/audit log stream, not a command-processing stream. The `PositionEventWriter` batch-writes position events to the database in arrival order. It does not need per-tenant ordering because events are audit records -- the order they arrive is the order they are stored. There is no state machine that could be corrupted by out-of-order delivery.

**Why is SETTLA_DLQ unpartitioned?** Dead-lettered messages come from all streams and have already exhausted their retries. The DLQ monitor processes them for alerting and optional replay, not for state transitions. Ordering is irrelevant.

---

## The Partition-to-Consumer Mapping

```
                         SETTLA_TRANSFERS Stream
                    Subject: settla.transfer.partition.*.>

+------------------------------------------------------------------+
|                                                                  |
|  Partition 0          Partition 1          Partition 2            |
|  Tenant A, E          Tenant B, F          Tenant C              |
|  .partition.0.>       .partition.1.>       .partition.2.>        |
|       |                    |                    |                 |
|       v                    v                    v                 |
|  Consumer:            Consumer:            Consumer:             |
|  settla-transfer-     settla-transfer-     settla-transfer-      |
|  worker-0             worker-1             worker-2              |
|       |                    |                    |                 |
|       v                    v                    v                 |
|  TransferWorker(0)    TransferWorker(1)    TransferWorker(2)     |
|  (serial within       (serial within       (serial within        |
|   partition)           partition)           partition)            |
|                                                                  |
|  ... continues through partitions 3-7 ...                        |
+------------------------------------------------------------------+
```

Each consumer uses a filter subject to only receive messages for its partition:

```go
// Format: settla.transfer.partition.{N}.>
func PartitionFilter(partition int) string {
    return fmt.Sprintf("%s.partition.%d.>", SubjectPrefixTransfer, partition)
}

// Generic version for any stream prefix
func StreamPartitionFilter(subjectPrefix string, partition int) string {
    return fmt.Sprintf("%s.partition.%d.>", subjectPrefix, partition)
}
```

The consumer name is also partition-specific to ensure NATS creates a separate durable consumer per partition:

```go
func ConsumerName(partition int) string {
    return fmt.Sprintf("settla-transfer-worker-%d", partition)
}

func StreamConsumerName(baseName string, partition int) string {
    return fmt.Sprintf("%s-%d", baseName, partition)
}
```

---

## Why 8 Partitions? The Math

The partition count is a balance between three forces:

**1. Parallelism ceiling:** With N partitions, at most N events are processed concurrently. At 580 TPS sustained with an average processing time of 50ms per event, the minimum partitions needed is:

```
580 TPS * 0.050s = 29 concurrent events needed

But each partition processes serially, so:
  8 partitions * (1000ms / 50ms) = 160 events/sec capacity per partition
  8 * 160 = 1,280 events/sec total capacity

  580 TPS / 1,280 capacity = 45% utilization at sustained load
  5,000 TPS / 1,280 capacity = needs burst handling (backpressure)
```

For peak loads of 5,000 TPS, the system relies on NATS buffering (messages queue on the stream) while the 8 workers drain the backlog. At 50ms average processing time, 8 partitions can drain a 10-second 5,000 TPS burst (50,000 messages) in about 39 seconds.

**2. Tenant distribution:** With 8 partitions and the birthday paradox, you need at least 8+ tenants for even utilization. Settla targets enterprise fintechs (Lemfi, Fincra, Paystack), expecting 10-50 tenants. 8 partitions gives acceptable distribution.

**3. Resource overhead:** Each partition requires a dedicated NATS consumer, a goroutine for the worker, and (for the Subscriber) a per-tenant mutex map. 8 partitions means 8 goroutines per stream across 10 partitioned streams = 80 goroutines total, which is negligible.

**Why not 16 or 32?** Diminishing returns. Going from 8 to 16 partitions doubles resource usage but only helps if a single partition is hot (many tenants hashing to the same partition). With 10-50 tenants across 8 partitions, the expected max load per partition is ~2x the average -- well within tolerance.

---

## Per-Tenant Ordering Within a Partition

Within a partition, multiple tenants may coexist. The `Subscriber` in `subscriber.go` uses per-tenant mutexes when pool size > 1:

```go
func (s *Subscriber) dispatchPooled(ctx context.Context, msg jetstream.Msg,
                                     handler EventHandler,
                                     streamName string) {
    // Acquire semaphore slot
    select {
    case s.workerPool <- struct{}{}:
    case <-ctx.Done():
        return
    }

    go func() {
        defer func() { <-s.workerPool }()

        event, err := unmarshalEvent(msg.Data())
        if err != nil {
            processMessage(ctx, msg, handler, streamName,
                          s.client, s.logger, nil)
            return
        }

        // Per-tenant mutex ensures ordering within a tenant
        tenantID := event.TenantID.String()
        mu := s.getTenantMutex(tenantID)
        mu.Lock()
        defer mu.Unlock()

        processMessage(ctx, msg, handler, streamName,
                      s.client, s.logger, &event)
    }()
}
```

This allows concurrent processing of events for different tenants on the same partition while maintaining strict ordering for any single tenant.

The tenant mutexes are lazily created and evicted after 5 minutes of inactivity:

```go
func (s *Subscriber) cleanupTenantLocks(ctx context.Context) {
    const idleTimeout = 5 * time.Minute
    ticker := time.NewTicker(idleTimeout)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            cutoff := time.Now().Add(-idleTimeout).Unix()
            s.tenantLocks.Range(func(key, value any) bool {
                entry := value.(*tenantMutexEntry)
                if entry.lastUsed.Load() < cutoff {
                    s.tenantLocks.Delete(key)
                }
                return true
            })
        }
    }
}
```

---

## Partition Consistency Across Streams

A critical invariant: the same tenant always maps to the same partition number regardless of which stream the event targets. This is because all subject builders call the same `TenantPartition()` function:

```go
// All of these produce the same partition number for the same tenant:
TransferSubject(tenantID, 8, "transfer.created")        // partition N
ProviderSubject(tenantID, 8, "provider.onramp.execute")  // partition N
LedgerSubject(tenantID, 8, "ledger.post")                // partition N
TreasurySubject(tenantID, 8, "treasury.reserve")         // partition N
BlockchainSubject(tenantID, 8, "blockchain.send")        // partition N
WebhookSubject(tenantID, 8, "webhook.deliver")           // partition N
DepositSubject(tenantID, 8, "deposit.tx.detected")       // partition N
BankDepositSubject(tenantID, 8, "bank_deposit.credit")   // partition N
EmailSubject(tenantID, 8, "email.send")                  // partition N
```

This means if you run ProviderWorker(partition=3), it processes provider intents for exactly the same set of tenants that TransferWorker(partition=3) handles. While there is no strict requirement for this (each worker type is independent), consistent partition assignment simplifies debugging -- you know all events for tenant X flow through partition N across all 10 partitioned streams.

The consistency test in `TestSubjectForEventType_PartitionConsistency` verifies this:

```go
func TestSubjectForEventType_PartitionConsistency(t *testing.T) {
    tenantID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")
    numPartitions := 8

    first := SubjectForEventType(domain.EventTransferCreated,
                                  tenantID, numPartitions)
    for i := 0; i < 1000; i++ {
        got := SubjectForEventType(domain.EventTransferCreated,
                                    tenantID, numPartitions)
        if got != first {
            t.Fatalf("iteration %d: subject changed", i)
        }
    }
}
```

---

## Visualization: Tenant Distribution

```
Tenants:  Lemfi    Fincra    Paystack   FlexFi   SendWave
UUIDs:    a000..1  b000..2   c000..3    d000..4  e000..5

FNV-1a hashes (mod 8):

  Lemfi    --> partition 3
  Fincra   --> partition 5
  Paystack --> partition 1
  FlexFi   --> partition 7
  SendWave --> partition 0

Partition  |  Tenants
-----------|----------
    0      |  SendWave
    1      |  Paystack
    2      |  (empty)
    3      |  Lemfi
    4      |  (empty)
    5      |  Fincra
    6      |  (empty)
    7      |  FlexFi

With 5 tenants and 8 partitions, 3 partitions are idle.
With 50 tenants, average ~6 tenants per partition.
```

---

## Key Insight

> Partitioning is not about load balancing -- it is about ordering guarantees. The partition count determines the maximum parallelism, but the primary purpose is ensuring all events for a given tenant are processed in FIFO order. The hash function does not need to distribute perfectly; it only needs to be deterministic. A "hot" partition (one high-volume tenant) is handled by NATS backpressure, not by re-partitioning.

---

## Common Mistakes

1. **Using random partition assignment.** Without deterministic hashing, the same tenant's events could land on different partitions, breaking ordering. The system would silently process events out of order, causing state machine transitions to fail unpredictably.

2. **Not partitioning streams that need ordering.** All command-processing streams (transfers, providers, ledger, treasury, blockchain, webhooks, deposits, bank deposits, emails) use tenant-based partitioning. Only audit/event streams (POSITION_EVENTS) and the DLQ skip partitioning.

3. **Changing numPartitions without migration.** If you change from 8 to 16 partitions, `TenantPartition()` produces different results for existing tenants. Events already queued on partition 3 would have consumers on partition 3 (old mapping), but new events would go to partition 11 (new mapping). The old consumer would drain, but during the transition, ordering is broken. Partition count changes require a coordinated drain-and-restart.

4. **Assuming equal load per partition.** With FNV-1a and a small number of tenants, some partitions may carry 3x the load of others. This is acceptable because the bottleneck is the external system (provider API, blockchain RPC), not the worker goroutine.

---

## Exercises

1. **Hash Distribution:** Write a program that generates 100 random UUIDs and calculates their partition assignments with 8 partitions. Plot the distribution. How does it compare to the theoretical uniform distribution?

2. **Hot Partition Analysis:** If Lemfi processes 60% of all transfers and hashes to partition 3, calculate the throughput impact on partition 3 compared to the average partition. At what Lemfi volume does partition 3 become a bottleneck (assuming 50ms average processing time)?

3. **Partition Migration Plan:** Design a zero-downtime migration strategy for changing from 8 to 16 partitions. Consider: draining in-flight messages, updating consumer filters, handling the transition period where old and new partition assignments coexist.

4. **Non-Partitioned Stream Design:** Explain why SETTLA_POSITION_EVENTS does not need tenant partitioning, while SETTLA_TREASURY does. Consider the difference between event streaming (audit log) and command processing (reserve/release).

---

## What's Next

In Chapter 5.4, we will examine the CHECK-BEFORE-CALL pattern that prevents double-execution when NATS redelivers a message, including the `ClaimProviderTransaction` function with its `INSERT ON CONFLICT DO NOTHING` semantics.
