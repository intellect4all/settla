# NATS Consumer Lag (SettlaNATSConsumerLagHigh)

## Alert Description

- **Alert:** `SettlaNATSConsumerLagHigh` (warning, queue depth >1000 for 1m) / `SettlaNATSConsumerLagCritical` (critical, queue depth >5000 for 1m)
- **Metric:** `settla_nats_partition_queue_depth`
- **Labels:** `partition`, `stream`
- Partitioned consumers process transfer events across 8 partitions (by tenant hash). High queue depth means workers are falling behind -- transfers are stalling in intermediate states.

**Severity:** P2 at >1000, P1 at >5000. Transfers will be delayed proportionally to queue depth.

## Diagnostic Steps

### 1. Check queue depth per partition

```bash
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '.data.result | sort_by(-.value[1])'
```

### 2. Check if the lag is isolated to one partition or global

```bash
# If one partition is much higher than others, it may be a hot-tenant issue
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '[.data.result[] | {partition: .metric.partition, depth: .value[1]}]'
```

### 3. Check settla-node worker health

```bash
kubectl -n settla get pods -l app.kubernetes.io/name=settla-node
kubectl -n settla logs statefulset/settla-node --tail=200 | grep -iE 'error|slow|timeout|consumer'
```

### 4. Check NATS JetStream health

```bash
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/jsz | jq '{streams: .streams, messages: .messages, consumers: .consumers}'

# Check individual stream consumer info
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/jsz?consumers=true | jq '.account_details[].stream_detail[] | {name: .name, state: .state}'
```

### 5. Check worker processing rate

```bash
curl -s "http://prometheus:9090/api/v1/query?query=rate(settla_nats_messages_total[5m])" | jq '.data.result'
```

### 6. Check if downstream dependencies are slow

```bash
# Workers call back into the engine after processing. Slow dependencies cause slow consumption.
kubectl -n settla logs statefulset/settla-node --tail=300 | grep -iE 'provider.*latency|ledger.*slow|treasury.*timeout'
```

## Remediation Steps

1. **Scale up settla-node replicas** to increase consumer parallelism:
   ```bash
   kubectl -n settla scale statefulset/settla-node --replicas=12
   # Scale back to 8 after lag clears
   ```

2. **If a single partition is hot:** This indicates a high-volume tenant is dominating one partition. Short-term: scale workers. Long-term: consider rebalancing the partition hash.

3. **If all partitions are lagged equally:** The bottleneck is downstream (provider, DB, or network). Diagnose and fix the underlying slow dependency.

4. **If NATS itself is unhealthy:** See `deploy/runbooks/nats-recovery.md`.

### Verify recovery

```bash
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq "[.data.result[] | .value[1]]"'
```

Queue depths should decrease steadily. Expected drain rate: ~250 msgs/s per worker instance.

## Escalation Criteria

- If queue depth >5000 and not decreasing after scaling workers, page the engineering lead.
- If queue depth >10000, treat as P0 -- transfers are severely delayed and tenants will notice.
- If NATS cluster is unhealthy (not just consumer lag), escalate to infrastructure team.
