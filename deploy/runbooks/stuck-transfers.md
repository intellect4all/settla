# Stuck Transfers

## When to Use

- Alert: Transfers in non-terminal state (`initiated`, `quoted`, `reserved`, `ledger_posting`, `executing`) for > 10 minutes
- Alert: `settla_transfer_duration_seconds` p99 > 30 minutes
- Alert: NATS consumer lag increasing (`settla_nats_partition_queue_depth` growing)
- Tenant reports transfers not completing
- Grafana Settla Overview shows transfer completion rate dropping

## Impact

- Affected transfers are delayed; tenant customers waiting for settlement.
- Treasury reservations held by stuck transfers reduce available balance.
- If widespread, may trigger tenant over-reservation alerts.
- No data loss; transfers can be retried or manually resolved.

**Severity:** P2 -- High (if affecting multiple tenants) or P3 -- Medium (single tenant, few transfers).

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to Transfer DB (via PgBouncer)
- Access to NATS monitoring (:8222)
- Access to Grafana dashboards (Settla Overview, Capacity Planning)

## Steps

### 1. Identify stuck transfers

```bash
# Find transfers stuck for > 10 minutes
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT id, tenant_id, status, corridor, amount, currency, created_at, updated_at,
          EXTRACT(EPOCH FROM (NOW() - updated_at)) / 60 AS minutes_since_update
   FROM transfers
   WHERE status NOT IN ('completed', 'failed', 'cancelled')
     AND updated_at < NOW() - INTERVAL '10 minutes'
   ORDER BY updated_at ASC
   LIMIT 50;"
```

Note the `status` of stuck transfers. This determines the investigation path:

| Status | Stuck At | Likely Cause |
|--------|----------|-------------|
| `initiated` | Before quoting | NATS consumer not processing, settla-node down |
| `quoted` | Before reservation | Treasury module issue, settla-node down |
| `reserved` | Before ledger posting | TigerBeetle issue, settla-node down |
| `ledger_posting` | During/after TB write | TigerBeetle write failure, TB->PG sync lag |
| `executing` | Provider execution | Provider timeout, provider API down, blockchain confirmation slow |

### 2. Check NATS consumer health

```bash
# Check NATS stream and consumer status
kubectl -n settla exec statefulset/nats-0 -- nats stream info SETTLA --json 2>/dev/null | jq '{
  messages: .state.messages,
  consumer_count: .state.consumer_count,
  last_seq: .state.last_seq
}'

# Check consumer lag per partition
kubectl -n settla exec statefulset/nats-0 -- nats consumer ls SETTLA --json 2>/dev/null | jq '.'

# Check Prometheus for queue depth
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '.data.result'
```

If NATS consumers have high pending counts, settla-node may be unhealthy.

### 3. Check settla-node health

```bash
# Check node pod status
kubectl -n settla get pods -l app.kubernetes.io/name=settla-node

# Check for recent restarts or errors
kubectl -n settla logs statefulset/settla-node --tail=100 | grep -i "error\|panic\|fatal\|timeout"

# Check node metrics
kubectl -n settla exec statefulset/settla-node-0 -- curl -s localhost:9091/metrics | grep settla_nats_messages_total
```

If settla-node pods are down or erroring:

```bash
# Restart settla-node
kubectl -n settla rollout restart statefulset/settla-node
kubectl -n settla rollout status statefulset/settla-node --timeout=120s
```

### 4. Check provider status (for transfers stuck in `executing`)

```bash
# Check provider error rates
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_provider_requests_total{status='error'}[5m]))by(provider)" | jq '.data.result'

# Check provider latency
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.95,sum(rate(settla_provider_latency_seconds_bucket[5m]))by(le,provider))" | jq '.data.result'

# Check recent transfer events for failure details
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT t.id AS transfer_id, t.status, e.event_type, e.payload, e.created_at
   FROM transfers t
   JOIN transfer_events e ON e.transfer_id = t.id
   WHERE t.status = 'executing'
     AND t.updated_at < NOW() - INTERVAL '10 minutes'
   ORDER BY e.created_at DESC
   LIMIT 20;"
```

### 5. Manual retry for stuck transfers

For transfers stuck due to transient failures (provider timeout, temporary NATS issue):

```bash
# Re-publish the transfer event to NATS for reprocessing
# This triggers the saga to retry from the current state
kubectl -n settla exec statefulset/nats-0 -- \
  nats pub "settla.transfer.retry" '{"transfer_id": "<TRANSFER_ID>"}'
```

For multiple stuck transfers:

```bash
# Get list of stuck transfer IDs
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -t -c \
  "SELECT id FROM transfers
   WHERE status NOT IN ('completed', 'failed', 'cancelled')
     AND updated_at < NOW() - INTERVAL '30 minutes';" | while read -r tid; do
  tid=$(echo "$tid" | xargs)
  [ -n "$tid" ] && kubectl -n settla exec statefulset/nats-0 -- \
    nats pub "settla.transfer.retry" "{\"transfer_id\": \"$tid\"}"
done
```

### 6. Manual failure (last resort)

If a transfer cannot be retried (provider confirmed failure, funds not sent):

```bash
# Fail the transfer (this triggers reservation release and webhook notification)
kubectl -n settla exec statefulset/nats-0 -- \
  nats pub "settla.transfer.fail" '{"transfer_id": "<TRANSFER_ID>", "reason": "manual_failure: <description>"}'

# Verify the transfer status changed
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT id, status, updated_at FROM transfers WHERE id = '<TRANSFER_ID>';"
```

### 7. Verify resolution

```bash
# Check that no transfers are stuck for > 10 minutes
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT COUNT(*) AS stuck_count
   FROM transfers
   WHERE status NOT IN ('completed', 'failed', 'cancelled')
     AND updated_at < NOW() - INTERVAL '10 minutes';"
# Expected: 0

# Check NATS consumer lag is back to normal
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '.data.result'
# Expected: low values (< 100 per partition)

# Check transfer completion rate in Grafana
# Settla Overview -> Transfer Success Rate gauge should be > 99%
```

## Verification

- [ ] No transfers stuck for > 10 minutes
- [ ] NATS consumer lag (`settla_nats_partition_queue_depth`) < 100 per partition
- [ ] Transfer completion rate > 99% on Grafana Settla Overview
- [ ] `settla_transfer_duration_seconds` p99 < 30 minutes
- [ ] Provider error rate back to normal

## Post-Incident

1. **Root cause:** Was this a NATS issue, settla-node crash, provider outage, or TigerBeetle problem?
2. **Affected scope:** How many transfers were stuck? Which tenants? Which corridors?
3. **Tenant notification:** Notify affected tenants if transfers were delayed > 30 minutes.
4. **Action items:**
   - If NATS related: review consumer configuration, partition count
   - If provider related: review provider health checks, circuit breaker configuration
   - If settla-node related: review memory limits, check for resource exhaustion
5. **Post-mortem:** Required if > 100 transfers were stuck or any transfer was delayed > 1 hour.
