# Outbox Relay Lag

## When to Use

- Alert: `SettlaOutboxBacklogHigh` (warning) or `SettlaOutboxBacklogCritical` (critical) firing
- `settla_outbox_unpublished_gauge` is growing and not draining
- Transfers are not progressing past `initiated` state
- settla-node logs show outbox relay errors or NATS publish failures

## Impact

- Transfer state machine stalls. New transfers are created and outbox entries are written, but intents are not published to NATS for worker processing.
- All downstream workers (provider, ledger, treasury, blockchain, webhook) stop receiving new work.
- Treasury reservations are not released for stalled transfers, reducing available balance.
- No data loss; outbox entries are durable in the Transfer DB and will be processed once the relay catches up.

**Severity:** P2 -- Warning (backlog < 100K and growing slowly) or P1 -- Critical (backlog > 100K or growing rapidly).

## Prerequisites

- `kubectl` access to the `settla` namespace (or `docker` access for Docker Compose deployments)
- Access to Transfer DB (via PgBouncer on port 6434 or raw Postgres on port 5434)
- Access to NATS monitoring endpoint (:8222)
- Access to Prometheus / Grafana dashboards

## Steps

### 1. Assess the backlog size

```bash
# Check current outbox backlog from Prometheus
curl -s "http://prometheus:9090/api/v1/query?query=settla_outbox_unpublished_gauge" | jq '.data.result'

# Direct DB check for unpublished entries
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT COUNT(*) AS unpublished_count,
          MIN(created_at) AS oldest_entry,
          EXTRACT(EPOCH FROM (NOW() - MIN(created_at))) AS oldest_age_seconds
   FROM outbox_entries
   WHERE published = false;"
```

### 2. Check settla-node health

```bash
# Kubernetes deployment
kubectl -n settla get pods -l app.kubernetes.io/name=settla-node

# Docker Compose deployment
docker ps | grep settla-node

# Check settla-node logs for relay errors
kubectl -n settla logs statefulset/settla-node --tail=200 | grep -i "outbox\|relay\|publish\|nats.*error"
```

If settla-node pods are down or crash-looping, restart them:

```bash
kubectl -n settla rollout restart statefulset/settla-node
kubectl -n settla rollout status statefulset/settla-node --timeout=120s
```

### 3. Check NATS connectivity

```bash
# Check NATS health from settla-node perspective
kubectl -n settla logs statefulset/settla-node --tail=100 | grep -i "nats\|connection\|disconnect\|reconnect"

# Check NATS cluster health
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/healthz
# Expected: {"status":"ok"}

# Check NATS JetStream status
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/jsz | jq '{streams: .streams, messages: .messages}'
```

If NATS is unhealthy, see `deploy/runbooks/nats-recovery.md`.

### 4. Check for lock contention on the outbox table

```bash
# Check for locks on the outbox_entries table
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pid, mode, granted, query_start, state, query
   FROM pg_locks l
   JOIN pg_stat_activity a ON l.pid = a.pid
   WHERE l.relation = 'outbox_entries'::regclass
   ORDER BY query_start;"

# Check for long-running transactions that may be blocking
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pid, state, query_start,
          EXTRACT(EPOCH FROM (NOW() - query_start)) AS duration_seconds,
          LEFT(query, 100) AS query_preview
   FROM pg_stat_activity
   WHERE state != 'idle'
     AND query_start < NOW() - INTERVAL '30 seconds'
   ORDER BY query_start;"
```

If long-running transactions are blocking, consider terminating them:

```bash
# Terminate a blocking backend (use with caution)
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pg_terminate_backend(<PID>);"
```

### 5. Check if autovacuum is blocking

```bash
# Check for autovacuum processes on outbox table
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pid, query, state, query_start,
          EXTRACT(EPOCH FROM (NOW() - query_start)) AS duration_seconds
   FROM pg_stat_activity
   WHERE query LIKE '%autovacuum%'
   ORDER BY query_start;"

# Check outbox table bloat
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT n_live_tup, n_dead_tup, last_autovacuum, last_autoanalyze
   FROM pg_stat_user_tables
   WHERE relname = 'outbox_entries';"
```

### 6. Check relay performance metrics

```bash
# Check relay poll duration and batch size
curl -s "http://prometheus:9090/api/v1/query?query=settla_outbox_relay_poll_duration_seconds" | jq '.data.result'
curl -s "http://prometheus:9090/api/v1/query?query=settla_outbox_relay_batch_size" | jq '.data.result'

# Check relay publish rate
curl -s "http://prometheus:9090/api/v1/query?query=rate(settla_outbox_relay_published_total[5m])" | jq '.data.result'
```

## Recovery

### 1. Restart settla-node if crashed

```bash
kubectl -n settla rollout restart statefulset/settla-node
kubectl -n settla rollout status statefulset/settla-node --timeout=120s
```

### 2. If NATS unreachable

Check NATS cluster health and follow `deploy/runbooks/nats-recovery.md`.

### 3. If lock contention is the issue

Terminate long-running transactions on the Transfer DB and monitor for improvement.

### 4. Monitor backlog draining

```bash
# Watch the unpublished gauge decrease
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=settla_outbox_unpublished_gauge" | jq ".data.result[0].value[1]"'

# Or check directly from the DB
watch -n 10 'kubectl -n settla exec statefulset/postgres-transfer-0 -- psql -U settla -d settla_transfer -t -c "SELECT COUNT(*) FROM outbox_entries WHERE published = false;"'
```

The relay polls every 50ms with a batch size of 100. Expected drain rate is approximately 2,000 entries/second under normal conditions.

### 5. Scale up if draining too slowly

If the backlog is very large (>500K entries), consider temporarily scaling up settla-node:

```bash
kubectl -n settla scale statefulset/settla-node --replicas=12
# Scale back down after backlog is cleared
# kubectl -n settla scale statefulset/settla-node --replicas=8
```

## Verification

- [ ] `settla_outbox_unpublished_gauge` is decreasing and approaching 0
- [ ] settla-node logs show successful outbox relay polling (no errors)
- [ ] NATS streams are receiving messages (`settla_nats_messages_total` rate > 0)
- [ ] Transfers are progressing past `initiated` state
- [ ] No stuck transfers older than 10 minutes (see `deploy/runbooks/stuck-transfers.md`)

## Post-Incident

1. **Root cause:** Was this a settla-node crash, NATS outage, DB lock contention, or autovacuum issue?
2. **Backlog peak:** What was the maximum backlog size? How long did it take to drain?
3. **Affected scope:** Which tenants had delayed transfers?
4. **Action items:**
   - If DB-related: review autovacuum settings for outbox_entries table (see `core/maintenance` for partition management)
   - If NATS-related: review NATS cluster stability and storage capacity
   - If settla-node-related: review resource limits, OOM kill logs
   - Consider increasing relay batch size if drain rate is insufficient
5. **Tenant notification:** Notify affected tenants if transfers were delayed > 5 minutes.

## Escalation

- If backlog > 100K entries and not draining after 5 minutes of investigation, page the on-call engineer.
- If backlog > 1M entries, treat as P1 incident and page the engineering lead.
