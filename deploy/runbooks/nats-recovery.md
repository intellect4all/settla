# NATS JetStream Recovery

## When to Use

- Alert: NATS health check (`/healthz` on port 8222) returns unhealthy
- Alert: `settla_nats_partition_queue_depth` growing continuously (consumers not draining)
- Alert: `settla_nats_messages_total` rate drops to zero (no messages being processed)
- settla-node logs show NATS connection errors
- Transfers stuck in early states (`initiated`, `quoted`) indicating event processing stopped

## Impact

- **Event processing halts.** Transfer state machine stops advancing (events drive state transitions).
- New transfers will be created in the Transfer DB but will not progress past `initiated`.
- TigerBeetle and PostgreSQL are unaffected (they do not depend on NATS).
- Treasury reservations in-memory are unaffected.
- Messages are durable (JetStream persists to disk); no message loss if NATS recovers.

**Severity:** P1 -- Critical. Event processing is the backbone of the transfer pipeline.

## Prerequisites

- `kubectl` access to the `settla` namespace
- NATS CLI (`nats`) available in NATS pods
- Access to NATS monitoring endpoint (:8222)
- Access to Grafana Capacity Planning dashboard

## Steps

### 1. Check NATS cluster health

```bash
# Check NATS pod status
kubectl -n settla get pods -l app.kubernetes.io/name=nats

# Check NATS logs for errors
kubectl -n settla logs statefulset/nats --tail=100

# Hit the NATS monitoring endpoint
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/healthz
# Expected: {"status":"ok"}

# Check JetStream status
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/jsz | jq '{
  streams: .streams,
  consumers: .consumers,
  messages: .messages,
  bytes: .bytes
}'

# Check NATS cluster connectivity (production: 3 nodes)
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/routez | jq '.routes | length'
# Expected: 2 (for a 3-node cluster)
```

### 2. Check stream and consumer details

```bash
# List all streams
kubectl -n settla exec statefulset/nats-0 -- nats stream ls

# Check the main SETTLA stream
kubectl -n settla exec statefulset/nats-0 -- nats stream info SETTLA --json | jq '{
  config: {
    subjects: .config.subjects,
    retention: .config.retention,
    replicas: .config.num_replicas,
    max_msgs: .config.max_msgs
  },
  state: {
    messages: .state.messages,
    bytes: .state.bytes,
    first_seq: .state.first_seq,
    last_seq: .state.last_seq,
    consumer_count: .state.consumer_count
  }
}'

# Check each consumer (one per partition, 8 total)
for i in $(seq 0 7); do
  echo "--- Partition $i ---"
  kubectl -n settla exec statefulset/nats-0 -- \
    nats consumer info SETTLA partition-$i --json 2>/dev/null | jq '{
      num_pending: .num_pending,
      num_ack_pending: .num_ack_pending,
      num_redelivered: .num_redelivered,
      last_delivery: .delivered.stream_seq
    }'
done
```

**Key indicators:**
- `num_pending` high and growing: consumers not keeping up
- `num_ack_pending` high: messages delivered but not acknowledged (settla-node processing issue)
- `num_redelivered` high: messages being redelivered repeatedly (processing failures)

### 3. Check settla-node consumers

```bash
# Check settla-node pods
kubectl -n settla get pods -l app.kubernetes.io/name=settla-node

# Check logs for NATS connection issues
kubectl -n settla logs statefulset/settla-node --tail=100 | grep -i "nats\|connection\|subscribe\|consumer"

# Check if nodes are processing messages
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_nats_messages_total[5m]))by(partition)" | jq '.data.result'
```

### 4. Restart NATS (if unhealthy)

```bash
# Option A: Rolling restart (preferred, maintains quorum in a 3-node cluster)
kubectl -n settla rollout restart statefulset/nats

# Wait for each pod to be ready before the next one restarts
kubectl -n settla rollout status statefulset/nats --timeout=180s
```

If rolling restart fails:

```bash
# Option B: Delete all pods (JetStream data is on PVC, will survive restart)
kubectl -n settla delete pods -l app.kubernetes.io/name=nats

# Wait for all pods to come back
kubectl -n settla wait --for=condition=Ready pod -l app.kubernetes.io/name=nats --timeout=120s
```

### 5. Verify consumers reconnect

After NATS restarts, settla-node consumers should automatically reconnect.

```bash
# Check that consumers are active
kubectl -n settla exec statefulset/nats-0 -- nats consumer ls SETTLA

# Check that pending counts are decreasing (consumers are draining)
# Run this twice with a 30-second gap
kubectl -n settla exec statefulset/nats-0 -- \
  nats stream info SETTLA --json | jq '.state.messages'
# Expected: decreasing

# If consumers did not reconnect, restart settla-node
kubectl -n settla rollout restart statefulset/settla-node
kubectl -n settla rollout status statefulset/settla-node --timeout=120s
```

### 6. Check for message redelivery issues

After NATS recovery, messages that were in-flight will be redelivered. The idempotency layer handles duplicates, but verify:

```bash
# Check redelivery counts
for i in $(seq 0 7); do
  kubectl -n settla exec statefulset/nats-0 -- \
    nats consumer info SETTLA partition-$i --json 2>/dev/null | jq "{partition: $i, redelivered: .num_redelivered}"
done

# Check settla-node logs for duplicate handling
kubectl -n settla logs statefulset/settla-node --tail=200 | grep -i "duplicate\|idempotent\|already processed"
```

### 7. Verify no duplicate processing

```bash
# Check for duplicate transfer events (same transfer_id, same event_type)
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT transfer_id, event_type, COUNT(*) AS cnt
   FROM transfer_events
   WHERE created_at > NOW() - INTERVAL '1 hour'
   GROUP BY transfer_id, event_type
   HAVING COUNT(*) > 1
   LIMIT 20;"
# Expected: 0 rows (idempotency prevents duplicates)

# If duplicates exist, check if they caused any issues
# (The state machine should reject invalid transitions, so duplicates are safe)
```

### 8. Drain backed-up messages

If a large backlog accumulated during the outage:

```bash
# Monitor the drain rate
watch -n 5 'kubectl -n settla exec statefulset/nats-0 -- nats stream info SETTLA --json 2>/dev/null | jq .state.messages'

# If drain is slow, consider temporarily scaling up settla-node
kubectl -n settla scale statefulset/settla-node --replicas=12
# (Remember to scale back down after the backlog is cleared)
# kubectl -n settla scale statefulset/settla-node --replicas=8
```

## Verification

- [ ] NATS `/healthz` returns `{"status":"ok"}`
- [ ] All 3 NATS cluster nodes connected (`/routez` shows 2 routes per node)
- [ ] `settla_nats_partition_queue_depth` stable and low (< 100 per partition)
- [ ] `settla_nats_messages_total` rate > 0 (messages being processed)
- [ ] settla-node logs show successful message processing (no connection errors)
- [ ] No stuck transfers (see `deploy/runbooks/stuck-transfers.md`)
- [ ] No duplicate transfer events in the database

## Post-Incident

1. **Root cause:** Was this a NATS cluster issue, disk space, memory pressure, or network partition?
2. **Message loss assessment:** JetStream messages are persisted to disk. If PVCs are intact, no messages were lost. Verify by checking stream sequence numbers.
3. **Check NATS storage:**
   ```bash
   kubectl -n settla exec statefulset/nats-0 -- df -h /data
   # Ensure > 20% free space
   ```
4. **Post-mortem:** Required if event processing was down for > 5 minutes.
5. **Action items:**
   - If disk space: increase PVC size (currently 50Gi) or configure stream max_bytes
   - If memory: increase NATS pod memory limits (currently 1Gi)
   - If network: review Kubernetes NetworkPolicy and DNS resolution
   - Consider adding NATS-specific Prometheus monitoring via nats-surveyor
6. **Communication:** Notify affected tenants if transfers were delayed due to event processing outage.
