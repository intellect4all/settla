# High Disk Usage (SettlaHighDiskUsage)

## When to Use

- Alert: `SettlaHighDiskUsage` — disk usage > 70% (warning) or > 85% (critical)
- Alert: `node_filesystem_avail_bytes / node_filesystem_size_bytes < 0.15` on any Settla node
- PVC usage approaching capacity (Postgres data, NATS JetStream, TigerBeetle data file)
- Grafana Capacity Planning shows disk usage trending toward 100%

## Impact

- At 100% disk: Postgres stops accepting writes (FATAL: could not write to file). TigerBeetle stops. NATS JetStream stops.
- **Critical if Transfer DB disk fills**: all transfers halt immediately. RPO = 0 only if Postgres is still up.
- Auto-remediation (`SettlaHighDiskUsage`) calls `make partition-cleanup` via the admin endpoint to drop expired monthly partitions, freeing space instantly.

**Severity:** P2 — Warning at 70%. P1 — Critical at 85%. Act before 100% or data writes stop.

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to AWS console for PVC resize (if needed)
- Access to Grafana Capacity Planning dashboard
- Admin endpoint access to settla-server for partition cleanup

## Steps

### 1. Identify the disk pressure source

```bash
# Check disk usage across all nodes
kubectl top nodes  # (shows CPU/memory but not disk)

# Check PVC usage via node stats
curl -s "http://prometheus:9090/api/v1/query?query=(1-node_filesystem_avail_bytes/node_filesystem_size_bytes)*100" | jq '.data.result | sort_by(-.value[1]) | .[0:10]'

# Check PVC capacity and usage
kubectl -n settla get pvc

# Check disk usage inside each Postgres pod
for db in ledger transfer treasury; do
  echo "--- postgres-$db-0 ---"
  kubectl -n settla exec statefulset/postgres-$db-0 -- df -h /var/lib/postgresql/data 2>/dev/null || true
done

# Check NATS JetStream disk usage
kubectl -n settla exec statefulset/nats-0 -- df -h /data 2>/dev/null || true

# Check TigerBeetle disk usage
kubectl -n settla exec statefulset/tigerbeetle-0 -- df -h /data 2>/dev/null || true
```

### 2. Identify large tables and partitions (Postgres)

```bash
# Find largest tables in Transfer DB (most likely culprit at 50M rows/day)
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT schemaname, tablename,
          pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS total_size,
          pg_total_relation_size(schemaname||'.'||tablename) AS size_bytes
   FROM pg_tables
   WHERE schemaname = 'public'
   ORDER BY size_bytes DESC LIMIT 20;"

# Find oldest partitions that can be dropped
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT tablename, pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size
   FROM pg_tables
   WHERE tablename LIKE '%_p%'
   ORDER BY tablename ASC LIMIT 20;"

# Check WAL directory size
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  du -sh /var/lib/postgresql/data/pg_wal/ 2>/dev/null || true
```

### 3. Drop expired partitions (immediate space reclamation)

Auto-remediation (`SettlaHighDiskUsage`) triggers this automatically. To do it manually:

```bash
# Via admin endpoint (preferred — uses partition manager)
curl -X POST "http://settla-server:8080/admin/maintenance/drop-old-partitions" \
  -H "Content-Type: application/json" \
  -d '{"older_than_months": 6}'

# Or directly via psql — drop partitions older than 6 months
# WARNING: this is irreversible. Ensure data is archived to S3 first.
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT tablename FROM pg_tables
   WHERE tablename LIKE 'transfers_p%'
     AND tablename < 'transfers_p' || to_char(NOW() - INTERVAL '6 months', 'YYYY_MM')
   ORDER BY tablename;"
# Review the output, then drop each one:
# DROP TABLE transfers_p2025_01;  -- etc.
```

**Important**: Partitions are the primary disk management tool. Each partition is a full table and can be dropped instantly (DROP TABLE, not DELETE), freeing disk space immediately.

### 4. Archive data before dropping (compliance requirement)

Per `docs/compliance.md`, transfer records must be retained for 7 years. Before dropping any partition:

```bash
# Export the partition to Parquet and upload to S3 Glacier
# (archive-cronjob.yaml handles this automatically monthly)
# For emergency manual archive:

kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "\COPY (SELECT * FROM transfers_p2025_01) TO '/tmp/transfers_p2025_01.csv' CSV HEADER;"

# Upload to S3 archive bucket
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  aws s3 cp /tmp/transfers_p2025_01.csv \
  s3://settla-archive/transfers/2025/01/transfers.csv \
  --storage-class GLACIER_IR
```

### 5. Expand PVC (if partition drop is insufficient)

If disk usage is > 85% and partition drop alone isn't enough:

```bash
# Check current PVC size
kubectl -n settla get pvc data-postgres-transfer-0

# Expand the PVC (requires StorageClass with allowVolumeExpansion: true)
kubectl -n settla patch pvc data-postgres-transfer-0 \
  --type='json' \
  -p='[{"op":"replace","path":"/spec/resources/requests/storage","value":"500Gi"}]'

# Kubernetes will trigger online resize (for ext4/xfs on EBS gp3)
# Monitor: kubectl -n settla describe pvc data-postgres-transfer-0

# After PVC is resized, resize the filesystem inside the pod
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  df -h /var/lib/postgresql/data
# The filesystem should automatically show the new size after PVC resize
```

### 6. Clean NATS JetStream storage

If NATS disk is growing:

```bash
# Check stream disk usage
kubectl -n settla exec statefulset/nats-0 -- \
  curl -s http://localhost:8222/jsz | jq '{total_bytes: .bytes, streams: .streams}'

# Purge old messages from streams (messages are already ack'd if processed)
# This is safe — WorkQueue streams auto-delete ack'd messages
# If orphaned messages exist:
kubectl -n settla exec statefulset/nats-0 -- \
  nats stream purge SETTLA_TRANSFERS --keep=0 --force 2>/dev/null || true
```

### 7. Clean WAL archives

```bash
# Check WAL archive directory size
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  du -sh /var/lib/postgresql/data/pg_wal/

# Clean old WAL segments (if WAL archiving to S3 is configured, this is safe)
# pg_archivecleanup keeps WAL needed for recovery
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  pg_archivecleanup /var/lib/postgresql/data/pg_wal/ \
  "$(kubectl -n settla exec statefulset/postgres-transfer-0 -- psql -U settla -t -c 'SELECT pg_walfile_name(pg_current_wal_lsn());' | tr -d ' ')" 2>/dev/null || true
```

## Verification

- [ ] Disk usage < 70% on all nodes and PVCs
- [ ] `SettlaHighDiskUsage` alert resolved in AlertManager
- [ ] Postgres still accepting writes: `psql -c "INSERT INTO ... SELECT 1;" `
- [ ] NATS JetStream still accepting messages
- [ ] Partition cleanup log shows dropped partitions
- [ ] S3 archive upload confirmed before any partition was dropped

## Post-Incident

1. **Root cause:** Insufficient partition cleanup schedule? PVC too small for current traffic? WAL bloat?
2. **Partition schedule:** If auto-cleanup ran too late, consider reducing `older_than_months` threshold or increasing cleanup frequency in `deploy/k8s/base/maintenance/archive-cronjob.yaml`.
3. **Capacity planning:** Update `docs/cost-estimation.md` with the revised storage requirements at current traffic levels.
4. **PVC resize:** If a PVC was expanded, update the base manifests in `deploy/k8s/base/` to reflect the new size.
5. **Post-mortem:** Required if disk reached 95%+ or if any write failures occurred.
6. **Communication:** Notify affected tenants if any API errors occurred during the disk pressure window.
