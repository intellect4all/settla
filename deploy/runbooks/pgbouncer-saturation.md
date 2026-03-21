# PgBouncer Pool Saturation (SettlaPgBouncerPoolSaturation)

## Alert Description

- **Alert:** `SettlaPgBouncerPoolSaturated` (warning, >85% for 2m) / `SettlaPgBouncerPoolCritical` (critical, >95% for 1m)
- **Metric:** `pgbouncer_pools_server_active / pgbouncer_pools_server_max`
- **Labels:** `database`, `instance`
- **Threshold:** Warning >85%, Critical >95%
- Settla uses 3 PgBouncer instances (ledger :6433, transfer :6434, treasury :6435) fronting 3 Postgres databases. With 6+ settla-server replicas sharing pools, saturation means new connections will queue or be rejected.

**Severity:** P2 at 85%, P1 at 95%. Queries will start queuing, increasing latency across the system.

## Diagnostic Steps

### 1. Check pool utilization

```bash
# Check all PgBouncer pools
for db in ledger transfer treasury; do
  echo "=== pgbouncer-$db ==="
  kubectl -n settla exec deploy/pgbouncer-$db -- psql -p 6433 -U settla -d pgbouncer -c "SHOW POOLS;"
done
```

### 2. Check waiting clients

```bash
curl -s "http://prometheus:9090/api/v1/query?query=pgbouncer_pools_client_waiting" | jq '.data.result'

for db in ledger transfer treasury; do
  echo "=== pgbouncer-$db ==="
  kubectl -n settla exec deploy/pgbouncer-$db -- psql -p 6433 -U settla -d pgbouncer -c "SHOW CLIENTS;" | grep -c "waiting"
done
```

### 3. Check for long-running transactions holding connections

```bash
# Identify which DB is saturated, then check it
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pid, state, wait_event_type, wait_event,
          EXTRACT(EPOCH FROM (NOW() - xact_start)) AS tx_duration_seconds,
          LEFT(query, 120) AS query_preview
   FROM pg_stat_activity
   WHERE state != 'idle'
   ORDER BY xact_start
   LIMIT 20;"
```

### 4. Check connection count vs max

```bash
curl -s "http://prometheus:9090/api/v1/query?query=pg_stat_activity_count/pg_settings_max_connections" | jq '.data.result'
```

### 5. Check if settla-server replicas recently scaled up

```bash
kubectl -n settla get pods -l app.kubernetes.io/name=settla-server --sort-by=.metadata.creationTimestamp
```

## Remediation Steps

1. **Kill long-running transactions** that are holding connections:
   ```bash
   # Identify and terminate (use with caution)
   kubectl -n settla exec statefulset/postgres-transfer-0 -- \
     psql -U settla -d settla_transfer -c \
     "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
      WHERE state != 'idle' AND xact_start < NOW() - INTERVAL '60 seconds';"
   ```

2. **Increase PgBouncer pool size** if connections are legitimately needed:
   ```bash
   # Edit PgBouncer configmap
   kubectl -n settla edit configmap pgbouncer-<DB>-config
   # Increase default_pool_size and max_client_conn
   # Restart PgBouncer
   kubectl -n settla rollout restart deployment/pgbouncer-<DB>
   ```

3. **If a specific DB is saturated** (e.g., transfer DB during peak):
   - Check if outbox relay polling is too aggressive (20ms interval, batch 500)
   - Check if partition manager or vacuum manager is running during peak hours

4. **If all pools are saturated:** Reduce settla-server replica count or increase Postgres `max_connections`.

### Verify recovery

```bash
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=pgbouncer_pools_server_active/pgbouncer_pools_server_max" | jq ".data.result"'
```

## Escalation Criteria

- If pool is at 95% and queries are timing out, page the DBA on-call.
- If killing long-running transactions does not free connections, investigate for connection leaks in application code.
- If multiple PgBouncer instances are saturated simultaneously, treat as infrastructure incident (P1).
