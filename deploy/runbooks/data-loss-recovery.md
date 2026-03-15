# Data Loss Recovery Procedure

## When to Use

- Complete or partial data loss across one or more databases (Transfer DB, Ledger DB, Treasury DB, TigerBeetle)
- Database corruption detected that cannot be repaired in place
- Disaster recovery scenario requiring full environment restoration
- Failed migration or accidental data deletion

## Impact

- Depending on scope, transfers may be lost, balances incorrect, or tenant data unavailable.
- Active transfers in the pipeline will stall or fail.
- Tenant-facing APIs may return errors or stale data.

**Severity:** P0 -- Critical / Disaster Recovery.

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to backup storage (WAL archives, TB snapshots, PG base backups)
- Access to NATS JetStream (messages retained for 7 days)
- Familiarity with the recovery order and data dependencies
- At least two engineers available (one for recovery, one for verification)

## Recovery Order

**This order is critical and must be followed strictly.** The databases have dependencies:

1. **Transfer DB** -- source of truth for transfer state and the transactional outbox. All other systems derive work from it.
2. **Ledger DB** -- CQRS read model. Can be fully rebuilt from TigerBeetle if TB is intact.
3. **Treasury DB** -- position snapshots. Can be rebuilt from in-memory state on settla-server restart (loaded from TB balances).

TigerBeetle is the write authority for balances and should be restored before or in parallel with the Ledger DB.

## Pre-Recovery Steps

### 1. Stop all application services

```bash
# Prevent any new writes or state changes during recovery
kubectl -n settla scale deployment/settla-server --replicas=0
kubectl -n settla scale statefulset/settla-node --replicas=0
kubectl -n settla scale deployment/settla-gateway --replicas=0
kubectl -n settla scale deployment/settla-webhook --replicas=0

# Verify all application pods are terminated
kubectl -n settla get pods -l 'app.kubernetes.io/part-of=settla'
```

### 2. Assess the damage

```bash
# Check which databases are affected
for db in postgres-ledger postgres-transfer postgres-treasury; do
  echo "=== $db ==="
  kubectl -n settla exec statefulset/${db}-0 -- pg_isready 2>/dev/null && echo "HEALTHY" || echo "DOWN/CORRUPTED"
done

# Check TigerBeetle
kubectl -n settla get pods -l app.kubernetes.io/name=tigerbeetle

# Check latest available backups
# (Substitute your backup tool -- e.g., pgBackRest, WAL-G, Barman)
# Example with WAL-G:
# kubectl -n settla exec statefulset/postgres-transfer-0 -- wal-g backup-list
```

## Transfer DB Recovery

The Transfer DB is the most critical database. It contains transfers, transfer events, outbox entries, tenants, API keys, and quotes.

### 1. Restore from WAL backup

```bash
# Restore from the latest base backup + WAL replay
# This gives point-in-time recovery (PITR)

# Stop the Transfer DB pod
kubectl -n settla delete pod postgres-transfer-0

# Restore the data directory from backup
# (Procedure depends on your backup tool)
# Example with WAL-G:
# kubectl -n settla exec statefulset/postgres-transfer-0 -- wal-g backup-fetch /var/lib/postgresql/data LATEST

# Start the pod -- Postgres will replay WAL to reach consistency
kubectl -n settla wait --for=condition=Ready pod/postgres-transfer-0 --timeout=600s
```

### 2. Identify the gap window

```bash
# Find the latest transfer in the restored DB
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT MAX(created_at) AS latest_transfer, MAX(updated_at) AS latest_update FROM transfers;"

# Compare with NATS stream to identify messages created after the backup point
kubectl -n settla exec statefulset/nats-0 -- \
  nats stream info SETTLA_TRANSFERS --json | jq '{first_seq: .state.first_seq, last_seq: .state.last_seq, first_ts: .state.first_ts, last_ts: .state.last_ts}'
```

### 3. Replay NATS messages for the gap window

NATS JetStream retains messages for 7 days. If the gap is within this window, replay unprocessed messages:

```bash
# Reset consumer positions to replay from the gap start
# WARNING: This will reprocess messages, but idempotency keys prevent duplicate execution
kubectl -n settla exec statefulset/nats-0 -- \
  nats consumer edit SETTLA_TRANSFERS transfer-worker --deliver-policy=by-start-time --opt-start-time="<GAP_START_TIME_RFC3339>"
```

### 4. Verify transfer state consistency

```bash
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT status, COUNT(*) FROM transfers GROUP BY status ORDER BY COUNT(*) DESC;

   SELECT COUNT(*) AS orphaned_transfers FROM transfers
   WHERE status NOT IN ('completed', 'failed', 'cancelled')
     AND updated_at < NOW() - INTERVAL '1 hour';"
```

## Ledger DB Recovery

The Ledger DB is a CQRS read model populated by the TB-to-PG sync consumer. If TigerBeetle is intact, the Ledger DB can be fully rebuilt.

### Option A: TigerBeetle is intact (preferred)

```bash
# Truncate the Ledger DB read model tables
kubectl -n settla exec statefulset/postgres-ledger-0 -- \
  psql -U settla -d settla_ledger -c \
  "TRUNCATE journal_entries, entry_lines, balance_snapshots CASCADE;"

# Trigger a full TB -> PG sync
# Restart settla-server which will initiate the sync consumer
# The sync process will rebuild all read-model data from TB

# Monitor sync progress
kubectl -n settla logs deployment/settla-server --tail=100 | grep -i "sync\|ledger.*rebuild"
```

### Option B: TigerBeetle also lost

```bash
# Restore TigerBeetle first (see TigerBeetle Recovery section below)
# Then restore Ledger DB from its own WAL backup

# Restore Ledger DB
kubectl -n settla delete pod postgres-ledger-0
# (Restore data directory from backup)
kubectl -n settla wait --for=condition=Ready pod/postgres-ledger-0 --timeout=600s

# After both TB and Ledger DB are restored, run full sync to close any gaps
```

## Treasury DB Recovery

The Treasury DB stores position snapshots, flushed every 100ms from in-memory state. It can be rebuilt from TigerBeetle balances.

```bash
# Option A: Simply restart settla-server
# On startup, settla-server loads positions from TB balances into memory
# The 100ms flush goroutine will write fresh snapshots to Treasury DB

# If Treasury DB is corrupted, restore from backup first:
kubectl -n settla delete pod postgres-treasury-0
# (Restore data directory from backup)
kubectl -n settla wait --for=condition=Ready pod/postgres-treasury-0 --timeout=600s

# Then restart settla-server to rebuild in-memory positions from TB
```

Verify treasury positions after recovery:

```bash
kubectl -n settla exec statefulset/postgres-treasury-0 -- \
  psql -U settla -d settla_treasury -c \
  "SELECT tenant_id, currency, balance, reserved, available, updated_at
   FROM treasury_positions
   ORDER BY tenant_id, currency;"
```

## TigerBeetle Recovery

If TigerBeetle data is lost, restore from the latest snapshot.

```bash
# Stop application services (if not already stopped)
kubectl -n settla scale deployment/settla-server --replicas=0
kubectl -n settla scale statefulset/settla-node --replicas=0

# Restore TB from snapshot
# (Procedure depends on your snapshot storage)
# 1. Download the latest TB snapshot to each replica's PVC
# 2. Restart TB pods
kubectl -n settla delete pods -l app.kubernetes.io/name=tigerbeetle
kubectl -n settla wait --for=condition=Ready pod -l app.kubernetes.io/name=tigerbeetle --timeout=300s

# After TB is restored, replay any unprocessed outbox entries
# The outbox relay will automatically pick up unpublished entries
# and the LedgerWorker will post them to the restored TB
```

## Post-Recovery Verification

### 1. Run full reconciliation pass

```bash
# Start application services
kubectl -n settla scale deployment/settla-server --replicas=6
kubectl -n settla scale statefulset/settla-node --replicas=8

# Monitor reconciliation runs
curl -s "http://prometheus:9090/api/v1/query?query=settla_reconciliation_runs_total" | jq '.data.result'
curl -s "http://prometheus:9090/api/v1/query?query=settla_reconciliation_mismatches_total" | jq '.data.result'
```

### 2. Compare TB balances vs PG balances for all tenants

```bash
# Check for balance discrepancies
# The reconciliation module runs 5 automated checks:
# 1. Treasury-ledger balance consistency
# 2. Transfer state consistency
# 3. Outbox health
# 4. Provider transaction consistency
# 5. Daily volume reconciliation

# Review results in Grafana Reconciliation dashboard
# Or check directly:
curl -s "http://prometheus:9090/api/v1/query?query=settla_reconciliation_mismatches_total>0" | jq '.data.result'
```

### 3. Check for orphaned transfers

```bash
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT id, tenant_id, status, corridor, amount, currency, created_at, updated_at
   FROM transfers
   WHERE status NOT IN ('completed', 'failed', 'cancelled')
     AND updated_at < NOW() - INTERVAL '1 hour'
   ORDER BY created_at;"
```

For orphaned transfers, either retry or manually fail them (see `deploy/runbooks/stuck-transfers.md`).

### 4. Notify affected tenants

```bash
# Identify affected tenants
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT t.tenant_id, ten.name, COUNT(*) AS affected_transfers
   FROM transfers t
   JOIN tenants ten ON ten.id = t.tenant_id
   WHERE t.updated_at > '<INCIDENT_START_TIME>'
     AND t.status NOT IN ('completed')
   GROUP BY t.tenant_id, ten.name
   ORDER BY COUNT(*) DESC;"
```

Notify tenants with:
- Incident summary (what happened, when)
- List of affected transfer IDs (if any transactions were lost)
- Expected resolution timeline
- Compensation details (if applicable)

## Verification Checklist

- [ ] All databases are online and accepting connections
- [ ] TigerBeetle cluster has quorum (at least 2/3 nodes)
- [ ] Transfer DB contains all transfers up to the incident time
- [ ] Ledger DB read model is consistent with TigerBeetle
- [ ] Treasury positions match TB balances
- [ ] Reconciliation pass shows zero mismatches
- [ ] No orphaned transfers (or all orphans have been resolved)
- [ ] API gateway is serving requests successfully
- [ ] Webhook delivery is operational
- [ ] All affected tenants have been notified
- [ ] Monitoring alerts have cleared

## Prevention

1. **Automated backups:** WAL-based continuous archival for all Postgres databases, TB snapshots every 6 hours.
2. **Cross-region replication:** Postgres streaming replicas in a secondary region for all 3 databases.
3. **TigerBeetle snapshots:** Stored in object storage (S3/GCS) with 30-day retention.
4. **NATS JetStream retention:** 7-day message retention provides a replay window for gap recovery.
5. **Regular DR drills:** Quarterly disaster recovery exercises to validate backup/restore procedures.
6. **Backup verification:** Automated weekly restore-and-verify jobs to ensure backups are usable.
7. **Immutable backups:** Use write-once storage for backup archives to prevent accidental or malicious deletion.
