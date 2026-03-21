# Treasury Flush Lag (SettlaTreasuryFlushLagHigh)

## Alert Description

- **Alert:** `SettlaTreasuryFlushLagHigh` (critical, >1s for 30s) / `SettlaTreasuryFlushLagWarning` (warning, >0.5s for 2m)
- **Metric:** `settla_treasury_flush_lag_seconds`
- **Threshold:** Critical >1s, Warning >0.5s (target: 100ms flush interval)
- Treasury positions are held in-memory and flushed to Postgres every 100ms. Lag means the flush goroutine is falling behind, risking position data loss if the pod is killed before persistence.

**Severity:** P1 -- Critical. In-memory positions may be lost on pod termination.

## Diagnostic Steps

### 1. Check current flush lag

```bash
curl -s "http://prometheus:9090/api/v1/query?query=settla_treasury_flush_lag_seconds" | jq '.data.result'
```

### 2. Check settla-server logs for flush errors

```bash
kubectl -n settla logs deploy/settla-server --tail=300 | grep -iE 'treasury|flush|position'
```

### 3. Check Treasury DB (PgBouncer :6435) connectivity and latency

```bash
# PgBouncer pool status
kubectl -n settla exec deploy/pgbouncer-treasury -- psql -p 6435 -U settla -d pgbouncer -c "SHOW POOLS;"

# Check for long-running transactions on treasury DB
kubectl -n settla exec statefulset/postgres-treasury-0 -- \
  psql -U settla -d settla_treasury -c \
  "SELECT pid, state, query_start,
          EXTRACT(EPOCH FROM (NOW() - query_start)) AS duration_seconds,
          LEFT(query, 100) AS query_preview
   FROM pg_stat_activity
   WHERE state != 'idle'
   ORDER BY query_start;"
```

### 4. Check disk I/O on treasury DB node

```bash
curl -s "http://prometheus:9090/api/v1/query?query=rate(node_disk_io_time_seconds_total[5m])" | jq '.data.result'
```

### 5. Check settla-server resource pressure

```bash
kubectl -n settla top pod -l app.kubernetes.io/name=settla-server
```

## Remediation Steps

1. **If Treasury DB is slow:** Check PgBouncer pool saturation, kill long-running transactions, verify disk I/O is not saturated.
2. **If settla-server is CPU/memory constrained:** Scale up replicas or increase resource limits.
3. **If PgBouncer pool is exhausted:** Increase pool size in PgBouncer config, restart PgBouncer.
4. **Graceful pod draining:** Do NOT kill settla-server pods while flush lag is high -- positions in memory will be lost. Wait for lag to recover before any rolling restart.

### Verify recovery

```bash
watch -n 5 'curl -s "http://prometheus:9090/api/v1/query?query=settla_treasury_flush_lag_seconds" | jq ".data.result[0].value[1]"'
```

## Escalation Criteria

- If flush lag >5s and not recovering after 2 minutes, page the engineering lead.
- If a pod is OOM-killed while flush lag is high, treat as data loss incident (P0). Cross-reference treasury positions with TigerBeetle balances to detect discrepancies.
- If Treasury DB is unreachable, escalate to DBA on-call.
