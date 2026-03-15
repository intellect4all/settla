# TigerBeetle Cluster Quorum Loss

## When to Use

- Alert: `SettlaTigerBeetleWriteStall` firing
- Ledger writes are timing out or returning errors
- Transfers are stuck at `reserved` state (cannot proceed to `ledger_posting`)
- settla-server logs show TigerBeetle connection or write errors

## Impact

- **All ledger writes are stalled.** No new transfers can proceed past treasury reservation.
- Transfers already in `executing` state (provider has been called) will complete at the provider level but their ledger entries cannot be posted until TB recovers.
- TigerBeetle is the write authority for balances. Without quorum, no balance mutations can occur.
- Read-side Postgres (CQRS) continues serving existing data but will become stale.
- Treasury in-memory reservations continue to work but cannot be confirmed by ledger posts.

**Severity:** P1 -- Critical. Ledger write path is completely blocked.

## Architecture

- **3-node TigerBeetle cluster** with consensus-based replication.
- **Quorum requirement:** 2 out of 3 nodes must be available for writes to succeed.
- **Single node loss:** cluster continues operating normally (2/3 quorum maintained).
- **Two node loss:** quorum lost, all writes stall until at least one node is restored.
- Each replica stores its own data file on a persistent volume.
- TigerBeetle handles recovery automatically when a failed replica rejoins -- it replays the write-ahead log from peers.

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to TigerBeetle pod logs and data volumes
- Access to Prometheus / Grafana dashboards
- Knowledge of TigerBeetle backup/snapshot procedures

## Steps

### 1. Identify which replicas are down

```bash
# Check TB pod status
kubectl -n settla get pods -l app.kubernetes.io/name=tigerbeetle

# Check for OOMKilled or CrashLoopBackOff
kubectl -n settla describe pods -l app.kubernetes.io/name=tigerbeetle | grep -A 5 "State:\|Last State:\|Reason:"

# Check TB pod events
kubectl -n settla get events --field-selector involvedObject.kind=Pod --sort-by='.lastTimestamp' | grep tigerbeetle
```

### 2. Check TB logs for crash reason

```bash
# Check logs from all TB replicas
for i in 0 1 2; do
  echo "=== tigerbeetle-$i ==="
  kubectl -n settla logs tigerbeetle-$i --tail=50 2>/dev/null || echo "Pod not running"
done

# Common crash reasons:
# - OOM: check container memory limits vs actual usage
# - Disk full: data file cannot grow
# - io_uring errors: kernel compatibility issues
# - Data file corruption: checksum failures
```

### 3. Check disk space on TB data volumes

```bash
# Check disk usage on each TB PVC
for i in 0 1 2; do
  echo "=== tigerbeetle-$i disk ==="
  kubectl -n settla exec tigerbeetle-$i -- df -h /data 2>/dev/null || echo "Pod not running"
done

# Check PVC status
kubectl -n settla get pvc -l app.kubernetes.io/name=tigerbeetle
```

### 4. Check settla-server impact

```bash
# Check for TB write errors in settla-server logs
kubectl -n settla logs deployment/settla-server --tail=200 | grep -i "tigerbeetle\|ledger.*error\|write.*stall\|quorum"

# Check ledger write latency (will be very high or timing out)
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,sum(rate(settla_ledger_write_duration_seconds_bucket[5m]))by(le))" | jq '.data.result'

# Check transfer state distribution (expect buildup at reserved/ledger_posting)
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT status, COUNT(*) FROM transfers
   WHERE created_at > NOW() - INTERVAL '30 minutes'
   GROUP BY status ORDER BY COUNT(*) DESC;"
```

## Recovery: Single Node Loss (quorum maintained)

If only 1 of 3 TB nodes is down, the cluster is still operational. This is a non-emergency but should be resolved quickly.

### 1. Restart the failed replica

```bash
# Simple restart -- TB recovers from its data file automatically
kubectl -n settla delete pod tigerbeetle-<N>

# Wait for it to come back
kubectl -n settla wait --for=condition=Ready pod/tigerbeetle-<N> --timeout=120s
```

### 2. If data file is corrupted

```bash
# Delete the PVC and let TB rebuild from peers
kubectl -n settla delete pod tigerbeetle-<N>
kubectl -n settla delete pvc data-tigerbeetle-<N>

# The StatefulSet will recreate the pod with a fresh PVC
# TB will replicate data from the other 2 healthy nodes
kubectl -n settla wait --for=condition=Ready pod/tigerbeetle-<N> --timeout=300s
```

### 3. Verify replica rejoins cluster

```bash
# Check TB logs for successful replication
kubectl -n settla logs tigerbeetle-<N> --tail=50 | grep -i "connected\|replicate\|sync\|ready"

# Verify all 3 nodes are healthy
kubectl -n settla get pods -l app.kubernetes.io/name=tigerbeetle
```

## Recovery: Multi-Node Loss / Quorum Loss

This is a critical incident. Writes are completely stalled.

### 1. STOP application services to prevent data inconsistency

```bash
# Scale down settla-server to prevent new transfers from entering the pipeline
kubectl -n settla scale deployment/settla-server --replicas=0

# Scale down settla-node to prevent workers from processing with a broken ledger
kubectl -n settla scale statefulset/settla-node --replicas=0

# Verify all application pods are terminated
kubectl -n settla get pods -l 'app.kubernetes.io/name in (settla-server,settla-node)'
```

### 2. Restore at least 2 replicas

```bash
# Attempt restart of all TB pods
kubectl -n settla delete pods -l app.kubernetes.io/name=tigerbeetle

# Wait for pods to come back
kubectl -n settla wait --for=condition=Ready pod -l app.kubernetes.io/name=tigerbeetle --timeout=300s

# If pods do not recover, restore from backup snapshots
# (Follow your TB backup restoration procedure -- restore the data file to the PVC)
```

### 3. Verify cluster quorum restored

```bash
# Check that at least 2 of 3 TB nodes are healthy
kubectl -n settla get pods -l app.kubernetes.io/name=tigerbeetle

# Check TB logs for quorum restoration
for i in 0 1 2; do
  echo "=== tigerbeetle-$i ==="
  kubectl -n settla logs tigerbeetle-$i --tail=20 2>/dev/null | grep -i "quorum\|leader\|ready"
done
```

### 4. Restart application services

```bash
# Restart settla-server
kubectl -n settla scale deployment/settla-server --replicas=6
kubectl -n settla rollout status deployment/settla-server --timeout=120s

# Restart settla-node
kubectl -n settla scale statefulset/settla-node --replicas=8
kubectl -n settla rollout status statefulset/settla-node --timeout=120s
```

### 5. Run full reconciliation pass

```bash
# Trigger reconciliation to verify no gaps between TB and Postgres
# Check reconciliation results
curl -s "http://prometheus:9090/api/v1/query?query=settla_reconciliation_runs_total" | jq '.data.result'
curl -s "http://prometheus:9090/api/v1/query?query=settla_reconciliation_mismatches_total" | jq '.data.result'

# Manually verify TB balances vs PG balances for key tenants
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT tenant_id, COUNT(*) AS stuck_transfers
   FROM transfers
   WHERE status NOT IN ('completed', 'failed', 'cancelled')
     AND updated_at < NOW() - INTERVAL '10 minutes'
   GROUP BY tenant_id;"
```

## Verification

- [ ] At least 2 of 3 TigerBeetle nodes are healthy and in quorum
- [ ] `SettlaTigerBeetleWriteStall` alert has resolved
- [ ] Ledger write latency (`settla_ledger_write_duration_seconds` p99) is back to normal (< 10ms)
- [ ] Transfers are progressing past `reserved` state
- [ ] Reconciliation pass shows no mismatches between TB and PG
- [ ] No stuck transfers older than 10 minutes

## Post-Incident

1. **Root cause:** OOM kill, disk space exhaustion, io_uring errors, hardware failure, or network partition?
2. **Duration:** How long was quorum lost? How many transfers were affected?
3. **Data integrity:** Did the reconciliation pass find any mismatches?
4. **Action items:**
   - Review TB memory limits and disk space allocation
   - Ensure TB replicas are on separate failure domains (different nodes / availability zones)
   - Verify automated TB snapshot schedule is running (recommended: every 6 hours)
   - Add disk space alerts at 70% and 85% thresholds for TB volumes
   - Review io_uring kernel compatibility if io_uring errors were the cause
5. **Prevention:**
   - Regular TB snapshots stored in object storage (S3/GCS)
   - Disk space monitoring with alerts well before 100%
   - Separate failure domains for all 3 replicas (anti-affinity rules)
   - Consider 5-replica cluster for higher fault tolerance if budget allows
6. **Post-mortem:** Required for any quorum loss event.
