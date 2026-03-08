# ADR-005: NATS JetStream Partitioned Events

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla's settlement engine is event-driven: each transfer progresses through a state machine (initiated → quoted → reserved → submitted → settled) via async events. At 50M transactions/day, the event system must handle ~580 events/sec sustained with peaks of 3,000–5,000 events/sec.

The critical constraint is **per-tenant ordering**: events for the same tenant must be processed in order to prevent race conditions (e.g., a release event processed before the corresponding reserve event). However, events for different tenants are independent and can be processed in parallel.

We evaluated several approaches:

| Approach | Ordering guarantee | Throughput | Complexity |
|----------|-------------------|-----------|------------|
| Single consumer, single queue | Global ordering | ~200 events/sec | Low |
| Multiple consumers, no ordering | None | ~5,000 events/sec | Low |
| Kafka partitioned by tenant_id | Per-partition ordering | ~10,000 events/sec | High (Kafka ops) |
| NATS JetStream partitioned by tenant hash | Per-partition ordering | ~5,000 events/sec | Medium |

**The threshold: 580 events/sec with per-tenant ordering cannot be achieved with a single consumer.** A single consumer processing events sequentially maxes out at ~200 events/sec (assuming ~5ms processing time per event). We need at least 3x parallelism to handle sustained load, and 25x for peak. But naive parallelism breaks per-tenant ordering.

The solution is partitioning: hash tenant_id to a fixed number of partitions, route all events for the same tenant to the same partition, and run one consumer per partition. This guarantees per-tenant ordering while achieving cross-tenant parallelism.

We chose NATS JetStream over Kafka because: (1) NATS is operationally simpler — single binary, no ZooKeeper/KRaft, no topic partition rebalancing; (2) NATS JetStream provides at-least-once delivery with ack-based flow control; (3) NATS's memory footprint is ~50MB vs Kafka's ~1GB+; (4) the team has existing NATS experience.

## Decision

We implement **8 NATS JetStream partitions** for event processing, with tenant-hash-based routing.

### Partition Routing
```
partition = fnv32(tenant_id) % 8
subject   = settla.transfer.partition.{partition}.{event_type}
```

- `fnv32` hash ensures deterministic, uniform distribution across partitions
- Same tenant always routes to the same partition number
- Event types: `transfer.initiated`, `transfer.quoted`, `settlement.completed`, etc.

### Stream Configuration
- Stream name: `SETTLA_TRANSFERS`
- Subjects: `settla.transfer.partition.*.>`
- Retention: limits-based (7 days or 10GB, whichever comes first)
- Storage: file-based for durability
- Replicas: 3 in production (single in development)
- Ack policy: explicit (consumer must ack each message)
- Max deliver: 5 (then dead letter)

### Consumer Model
- 8 durable consumers, one per partition: `settla-worker-partition-{0..7}`
- Each `settla-node` instance binds to one or more partitions
- With 8 `settla-node` instances, each handles exactly one partition
- Ack wait: 30 seconds (processing timeout)
- Max ack pending: 1 (ensures strict ordering within partition)

### Event Flow
```
Core Engine → publisher.Publish(event) → NATS subject (partitioned)
    → settla-node consumer (partition N) → worker.ProcessTransferEvent()
    → state machine transition → next event published
```

## Consequences

### Benefits
- **Per-tenant ordering guaranteed**: all events for tenant X always land on partition `fnv32(X) % 8`, processed by a single consumer in FIFO order. No out-of-order processing, no race conditions.
- **8x parallelism**: 8 partitions × 1 consumer each = 8 concurrent event processors. At ~200 events/sec per consumer, this yields ~1,600 events/sec sustained — 2.7x headroom over the 580 TPS requirement.
- **Horizontal scaling with settla-node**: adding more `settla-node` instances up to 8 distributes partitions across processes. Each instance handles `8 / N` partitions.
- **Operational simplicity**: NATS JetStream is a single binary with built-in clustering. No ZooKeeper, no broker rebalancing, no ISR management.
- **At-least-once delivery**: explicit ack with max deliver = 5 ensures events are not lost. Failed events are retried up to 5 times before dead lettering.

### Trade-offs
- **Fixed partition count**: 8 partitions is a static choice. Increasing partitions later requires stream migration and rebalancing. 8 was chosen to match the target of 8 `settla-node` instances.
- **Hot partition risk**: if one tenant generates disproportionate traffic (e.g., Lemfi at 60% of volume), its partition becomes a bottleneck. At 60% of 580 TPS = 348 events/sec on one partition, this is within the ~200 events/sec single-consumer capacity only if processing time stays under 3ms.
- **Partition rebalancing on scale-up**: adding `settla-node` instances beyond 8 provides no additional parallelism. Scaling beyond 8 requires increasing the partition count, which requires stream reconfiguration.
- **Max ack pending = 1 limits throughput**: strict ordering (one message in flight per partition) caps per-partition throughput. This is the cost of the ordering guarantee.

### Mitigations
- **Hot partition monitoring**: per-partition lag metrics (`settla.events.partition.{N}.lag`) alert when any single partition falls behind. If a partition consistently lags, we can split the hot tenant into a dedicated stream.
- **Dead letter queue**: after 5 failed delivery attempts, events move to a dead letter subject (`settla.transfer.deadletter.>`) for manual investigation. This prevents poison messages from blocking the partition.
- **Partition count as config**: the partition count is a configuration value (`SETTLA_PARTITIONS=8`), not hardcoded. Increasing it requires a stream migration but no code changes.
- **Burst absorption**: NATS JetStream buffers messages during bursts. Consumers process at their own pace, smoothing out peak traffic spikes.

## References

- [NATS JetStream Documentation](https://docs.nats.io/nats-concepts/jetstream)
- [Kafka Partitioning Best Practices](https://www.confluent.io/blog/how-choose-number-topics-partitions-kafka-cluster/) — partition sizing principles apply to NATS too
- ADR-001 (Modular Monolith) — `settla-node` is already a separate binary, validating the event-driven architecture
