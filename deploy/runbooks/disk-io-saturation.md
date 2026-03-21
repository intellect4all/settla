# Disk I/O Saturation (SettlaDiskIOSaturation)

## Alert Description

- **Alert:** `SettlaDiskIOSaturation` (warning, >90% utilization for 10m)
- **Metric:** `rate(node_disk_io_time_seconds_total[5m])`
- **Labels:** `instance`, `device`
- **Threshold:** >0.9 (90% disk time spent on I/O)
- Disk is nearly fully utilized. Postgres WAL writes, TigerBeetle data files, and NATS JetStream storage all depend on disk throughput. Saturation causes write latency spikes across the entire system.

**Severity:** P2 -- Warning. Will degrade to P1 if sustained, as database and ledger write performance collapse.

## Diagnostic Steps

### 1. Identify the saturated disk and host

```bash
curl -s "http://prometheus:9090/api/v1/query?query=rate(node_disk_io_time_seconds_total[5m])>0.9" | jq '.data.result'
```

### 2. Check which process is driving I/O

```bash
# On the affected node
kubectl -n settla exec <POD_ON_NODE> -- iostat -xz 5 3

# Check disk read/write rates
curl -s "http://prometheus:9090/api/v1/query?query=rate(node_disk_read_bytes_total[5m])" | jq '.data.result'
curl -s "http://prometheus:9090/api/v1/query?query=rate(node_disk_written_bytes_total[5m])" | jq '.data.result'
```

### 3. Check Postgres WAL generation rate

```bash
# High WAL generation = heavy write load
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0') AS wal_bytes;"
```

### 4. Check if autovacuum is running

```bash
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pid, query, query_start,
          EXTRACT(EPOCH FROM (NOW() - query_start)) AS duration_seconds
   FROM pg_stat_activity
   WHERE query LIKE '%autovacuum%';"
```

### 5. Check TigerBeetle disk usage

```bash
kubectl -n settla exec statefulset/tigerbeetle-0 -- du -sh /data/
```

### 6. Check NATS JetStream storage

```bash
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/jsz | jq '{memory: .memory, storage: .storage}'
```

## Remediation Steps

1. **If autovacuum is the cause:** It is necessary but can be rescheduled. For immediate relief, reduce `autovacuum_max_workers` or adjust `autovacuum_vacuum_cost_delay`:
   ```sql
   ALTER SYSTEM SET autovacuum_vacuum_cost_delay = '20ms';
   SELECT pg_reload_conf();
   ```

2. **If WAL generation is excessive:** Check for bulk operations or partition maintenance running during peak hours. Reschedule maintenance windows.

3. **If TigerBeetle is the source:** Check if compaction is running. TB compaction is automatic but can spike I/O. Ensure TB is on a dedicated volume with provisioned IOPS.

4. **Expand volume IOPS** (cloud environments):
   ```bash
   # AWS example: increase gp3 IOPS
   aws ec2 modify-volume --volume-id <VOL_ID> --iops 6000
   ```

5. **Move workloads to separate volumes:** Ensure Postgres data, WAL, TigerBeetle, and NATS each have dedicated volumes to avoid I/O contention.

### Verify recovery

```bash
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=rate(node_disk_io_time_seconds_total[5m])" | jq "[.data.result[] | {device: .metric.device, util: .value[1]}]"'
```

## Escalation Criteria

- If I/O saturation >95% for more than 15 minutes, page infrastructure on-call.
- If database write latency p99 exceeds 100ms due to disk saturation, treat as P1.
- If TigerBeetle write latency degrades (>10ms p99), escalate -- ledger throughput is compromised.
- If disk space is also low (<15%), see `deploy/runbooks/high-disk-usage.md` and take immediate action.
