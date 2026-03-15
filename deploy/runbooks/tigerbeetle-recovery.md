# TigerBeetle Recovery

## When to Use

- Alert: TigerBeetle TCP health check failure on port 3001
- Alert: `settla_ledger_tb_write_latency_seconds` p99 > 100ms sustained for 5 minutes
- Alert: `settla_ledger_tb_writes_total` rate drops to zero
- Transfer pipeline stalled: transfers stuck in `ledger_posting` state
- Manual report of TigerBeetle node unresponsive

## Impact

- **Ledger writes stop.** No new transfers can progress past the `ledger_posting` state.
- Quotes can still be created (read path unaffected).
- Treasury reservations continue (in-memory, independent of TigerBeetle).
- NATS consumers will back up; messages will be redelivered once TB recovers.
- PostgreSQL read model becomes stale (TB->PG sync consumer stops producing).

**Severity:** P1 -- Critical. Page on-call immediately.

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to AWS S3 bucket containing TigerBeetle snapshots
- TigerBeetle CLI (`tigerbeetle`) installed locally or available in the pod
- Access to Grafana dashboards (Capacity Planning, Settla Overview)

## Steps

### 1. Confirm the failure

```bash
# Check TigerBeetle pod status
kubectl -n settla get pods -l app.kubernetes.io/name=tigerbeetle

# Check TigerBeetle logs
kubectl -n settla logs statefulset/tigerbeetle --tail=100

# Verify TCP connectivity from a settla-server pod
kubectl -n settla exec deploy/settla-server -- nc -zv tigerbeetle 3001
```

**Expected if healthy:** `tigerbeetle 3001 open`
**Expected if down:** `Connection refused` or timeout

### 2. Check Prometheus metrics

```bash
# Open Grafana Capacity Planning dashboard or query directly:
# Rate of TB writes (should be > 0 in production)
curl -s "http://prometheus:9090/api/v1/query?query=rate(settla_ledger_tb_writes_total[5m])"

# TB write latency p99
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,rate(settla_ledger_tb_write_latency_seconds_bucket[5m]))"

# PG sync lag (will be increasing if TB is down)
curl -s "http://prometheus:9090/api/v1/query?query=settla_ledger_pg_sync_lag_seconds"
```

### 3. Attempt restart

```bash
# Delete the pod to trigger a restart (StatefulSet will recreate it)
kubectl -n settla delete pod tigerbeetle-0

# Wait for pod to become ready (up to 60 seconds)
kubectl -n settla wait --for=condition=Ready pod/tigerbeetle-0 --timeout=60s
```

If the pod restarts successfully, skip to **Step 6 (Verification)**.

### 4. Check for data corruption

If the pod fails to start or enters CrashLoopBackOff:

```bash
# Check the pod logs for corruption indicators
kubectl -n settla logs tigerbeetle-0 --previous

# Look for: "data file corrupted", "checksum mismatch", "superblock"
```

### 5. Restore from snapshot (if data is corrupt)

```bash
# List available snapshots in S3
aws s3 ls s3://settla-backups/tigerbeetle/snapshots/ --recursive | tail -10

# Stop the StatefulSet to prevent restarts during restore
kubectl -n settla scale statefulset/tigerbeetle --replicas=0

# Wait for pod termination
kubectl -n settla wait --for=delete pod/tigerbeetle-0 --timeout=60s

# Create a temporary pod to access the PVC
cat <<'EOF' | kubectl -n settla apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: tb-restore
  namespace: settla
spec:
  containers:
  - name: restore
    image: amazon/aws-cli:latest
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: data-tigerbeetle-0
EOF

# Wait for restore pod
kubectl -n settla wait --for=condition=Ready pod/tb-restore --timeout=60s

# Download latest snapshot
kubectl -n settla exec tb-restore -- \
  aws s3 cp s3://settla-backups/tigerbeetle/snapshots/latest/0_0.tigerbeetle /data/0_0.tigerbeetle

# Clean up restore pod
kubectl -n settla delete pod tb-restore

# Restart TigerBeetle
kubectl -n settla scale statefulset/tigerbeetle --replicas=1
kubectl -n settla wait --for=condition=Ready pod/tigerbeetle-0 --timeout=120s
```

### 6. Verification

```bash
# Verify TigerBeetle is accepting connections
kubectl -n settla exec deploy/settla-server -- nc -zv tigerbeetle 3001

# Check that settla-server can write to TB (look for successful ledger operations in logs)
kubectl -n settla logs deploy/settla-server --tail=50 | grep -i "tigerbeetle\|ledger"

# Verify TB write rate has resumed in Prometheus
curl -s "http://prometheus:9090/api/v1/query?query=rate(settla_ledger_tb_writes_total[1m])"
# Expected: > 0

# Verify PG sync lag is decreasing
curl -s "http://prometheus:9090/api/v1/query?query=settla_ledger_pg_sync_lag_seconds"
# Expected: decreasing toward 0

# Check for stalled transfers that should now resume
kubectl -n settla exec deploy/settla-server -- curl -s localhost:8080/metrics | grep settla_transfers_total
```

### 7. Verify stalled transfers resume

```bash
# Check NATS consumer for redelivered messages (transfers that were waiting for TB)
kubectl -n settla exec statefulset/nats -- nats stream info SETTLA --json | jq '.state'

# Verify transfers are progressing past ledger_posting state
# (Check Grafana Settla Overview -> Transfers by Status)
```

### 8. If restored from snapshot: reconcile balances

```bash
# The PG read model may have entries that postdate the snapshot.
# The TB->PG sync consumer will detect discrepancies on the next sync cycle.
# Monitor the sync lag metric until it returns to normal.

# Run a manual balance reconciliation (if available)
kubectl -n settla exec deploy/settla-server -- \
  curl -s localhost:8080/admin/reconcile-balances

# Or compare specific accounts via psql:
kubectl -n settla exec statefulset/postgres-ledger-0 -- \
  psql -U settla -d settla_ledger -c \
  "SELECT account_code, balance, last_synced_at FROM balance_snapshots ORDER BY last_synced_at DESC LIMIT 20;"
```

## Verification

- [ ] TigerBeetle pod is Running and Ready
- [ ] `settla_ledger_tb_writes_total` rate > 0
- [ ] `settla_ledger_tb_write_latency_seconds` p99 < 5ms
- [ ] `settla_ledger_pg_sync_lag_seconds` < 1 second and stable
- [ ] No transfers stuck in `ledger_posting` state
- [ ] Grafana Settla Overview shows normal transfer completion rate

## Post-Incident

1. **If restored from snapshot:** Document the time window between snapshot and failure. Verify all accounts balance correctly. Run a full reconciliation report.
2. **Root cause analysis:** Was this a node failure, disk issue, memory pressure, or operator error?
3. **Post-mortem:** Required for any incident where TB was down > 5 minutes.
4. **Action items:** Consider whether snapshot frequency should be increased (currently hourly).
5. **Communication:** Notify affected tenants if any transfers were delayed > 30 minutes.
