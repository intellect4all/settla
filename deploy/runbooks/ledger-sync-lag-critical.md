# Ledger Sync Lag Critical (SettlaLedgerSyncLagCritical)

## Alert Description

- **Alert:** `SettlaLedgerSyncLagCritical` (critical, >120s for 2m) / `SettlaLedgerSyncLagHigh` (warning, >30s for 2m)
- **Metric:** `settla_ledger_pg_sync_lag_seconds`
- **Threshold:** Critical >120s, Warning >30s
- TigerBeetle is the ledger write authority. Postgres is the CQRS read model, populated by the TB-to-PG sync consumer. Lag means the API, dashboard, and portal are showing stale balance data.

**Severity:** P1 -- Critical at >120s. Dashboard and API read queries return stale balances. Settlement calculations may use outdated data.

## Diagnostic Steps

### 1. Check current sync lag

```bash
curl -s "http://prometheus:9090/api/v1/query?query=settla_ledger_pg_sync_lag_seconds" | jq '.data.result'
```

### 2. Check sync consumer health

```bash
# Check settla-server logs for sync consumer errors
kubectl -n settla logs deploy/settla-server --tail=300 | grep -iE 'ledger.*sync|tb.*pg|sync.*consumer|sync.*lag'
```

### 3. Check TigerBeetle connectivity

```bash
kubectl -n settla exec deploy/settla-server -- nc -zv tigerbeetle 3001

# Check TB write latency
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,sum(rate(settla_ledger_tb_write_latency_seconds_bucket[5m]))by(le))" | jq '.data.result'
```

### 4. Check Ledger DB (Postgres) write throughput

```bash
# Check PgBouncer pool for ledger DB
kubectl -n settla exec deploy/pgbouncer-ledger -- psql -p 6433 -U settla -d pgbouncer -c "SHOW POOLS;"

# Check for lock contention on ledger tables
kubectl -n settla exec statefulset/postgres-ledger-0 -- \
  psql -U settla -d settla_ledger -c \
  "SELECT pid, state, wait_event_type, wait_event,
          EXTRACT(EPOCH FROM (NOW() - query_start)) AS duration_seconds,
          LEFT(query, 100) AS query_preview
   FROM pg_stat_activity
   WHERE state != 'idle'
   ORDER BY query_start;"
```

### 5. Check if bulk inserts are failing

```bash
# The sync consumer uses write-ahead batching (5-50ms collect, bulk insert)
kubectl -n settla logs deploy/settla-server --tail=500 | grep -iE 'batch.*insert|bulk.*write|ledger.*write.*error'
```

## Remediation Steps

1. **If sync consumer is crashed or stuck:** Restart settla-server pods.
   ```bash
   kubectl -n settla rollout restart deployment/settla-server
   ```
2. **If Ledger DB is slow:** Check disk I/O, PgBouncer pool saturation, and autovacuum activity on journal/entry_lines tables.
3. **If TigerBeetle is unreachable:** Check TB pod health and network connectivity. See `deploy/runbooks/tigerbeetle-recovery.md`.
4. **If PgBouncer pool is saturated:** Increase pool size or investigate connection leaks.

### Verify recovery

```bash
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=settla_ledger_pg_sync_lag_seconds" | jq ".data.result[0].value[1]"'
```

Lag should decrease steadily. The sync consumer will catch up by processing the backlog of TB entries.

## Escalation Criteria

- If lag >300s (5 min) and not decreasing, page the engineering lead.
- If TigerBeetle is unreachable, escalate immediately -- this affects all ledger writes (P0).
- If settlement is scheduled (daily at 00:30 UTC) and sync lag is high, notify settlement ops -- net position calculations may use stale data.
