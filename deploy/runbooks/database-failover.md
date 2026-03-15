# Database Failover (PostgreSQL)

## When to Use

- Alert: PgBouncer health check fails for a database (ledger, transfer, or treasury)
- Alert: PostgreSQL primary not responding to `pg_isready`
- Alert: Replication lag increasing on read replicas with no writes from primary
- Application logs show persistent database connection errors
- `settla_gateway_requests_total{status="503"}` rate spikes (database-dependent endpoints)

## Impact

Impact depends on which database is affected:

| Database | Write Impact | Read Impact |
|----------|-------------|-------------|
| **Ledger DB** (PgBouncer :6432) | TB->PG sync consumer fails (PG is read model, TB is write authority) | Ledger queries fail (journal entries, balance snapshots) |
| **Transfer DB** (PgBouncer :6432) | New transfers cannot be created; state transitions fail | Transfer lookups, quote lookups fail |
| **Treasury DB** (PgBouncer :6432) | Treasury flush goroutine fails (positions persist in memory, at risk if pod restarts) | Treasury position queries fail |

**Severity:** P1 -- Critical (Transfer DB), P2 -- High (Ledger DB, Treasury DB).

## Prerequisites

- `kubectl` access to the `settla` namespace
- `psql` client access (via bastion or kubectl exec)
- Knowledge of which database is affected (check the alert labels)
- Access to PgBouncer admin console
- For managed Postgres (RDS): AWS console access or `aws rds` CLI

## Steps

### 1. Identify the affected database

```bash
# Check all Postgres pod statuses
kubectl -n settla get pods -l app.kubernetes.io/component=database

# Check PgBouncer pod statuses
kubectl -n settla get pods -l app.kubernetes.io/name=pgbouncer

# Test connectivity to each database via PgBouncer
for db in ledger transfer treasury; do
  echo "--- $db ---"
  kubectl -n settla exec deploy/pgbouncer-$db -- \
    psql -U settla -d settla_$db -c "SELECT 1;" 2>&1 | head -5
done
```

### 2. Check PgBouncer stats

```bash
# Connect to PgBouncer admin console for the affected database
# Replace <DB> with: ledger, transfer, or treasury
kubectl -n settla exec deploy/pgbouncer-<DB> -- \
  psql -p 6432 -U settla pgbouncer -c "SHOW POOLS;"

kubectl -n settla exec deploy/pgbouncer-<DB> -- \
  psql -p 6432 -U settla pgbouncer -c "SHOW SERVERS;"

kubectl -n settla exec deploy/pgbouncer-<DB> -- \
  psql -p 6432 -U settla pgbouncer -c "SHOW STATS;"
```

Look for:
- `sv_active` = 0 and `sv_used` = 0: PgBouncer cannot connect to Postgres
- `cl_waiting` high: Clients queued, waiting for connections

### 3. Check PostgreSQL primary health

```bash
# Check if primary Postgres pod is running
kubectl -n settla get pod postgres-<DB>-0

# Check primary logs
kubectl -n settla logs postgres-<DB>-0 --tail=100

# Test direct connectivity (bypassing PgBouncer)
kubectl -n settla exec postgres-<DB>-0 -- pg_isready -U settla -d settla_<DB>
```

### 4. Attempt primary recovery

```bash
# If the pod is in CrashLoopBackOff, check for disk space or OOM
kubectl -n settla describe pod postgres-<DB>-0

# Delete the pod to trigger restart
kubectl -n settla delete pod postgres-<DB>-0

# Wait for recovery
kubectl -n settla wait --for=condition=Ready pod/postgres-<DB>-0 --timeout=120s
```

If the primary recovers, skip to **Step 7 (Verification)**.

### 5. Promote a read replica

If the primary cannot be recovered:

**Self-managed PostgreSQL:**

```bash
# Identify the replica pod
kubectl -n settla get pods -l app.kubernetes.io/name=postgres-<DB>

# Verify the replica is in sync (check replication lag)
kubectl -n settla exec postgres-<DB>-1 -- \
  psql -U settla -d settla_<DB> -c \
  "SELECT CASE WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn()
          THEN 0
          ELSE EXTRACT(EPOCH FROM NOW() - pg_last_xact_replay_timestamp())
          END AS replication_lag_seconds;"

# Promote the replica
kubectl -n settla exec postgres-<DB>-1 -- pg_ctl promote -D /var/lib/postgresql/data/pgdata

# Verify it is now accepting writes
kubectl -n settla exec postgres-<DB>-1 -- \
  psql -U settla -d settla_<DB> -c "SELECT pg_is_in_recovery();"
# Expected: f (false = primary)
```

**Managed PostgreSQL (RDS):**

```bash
# Promote read replica to standalone
aws rds promote-read-replica \
  --db-instance-identifier settla-<DB>-replica-1 \
  --region us-east-1

# Wait for promotion (usually 1-5 minutes)
aws rds wait db-instance-available \
  --db-instance-identifier settla-<DB>-replica-1
```

### 6. Update PgBouncer to point to the new primary

```bash
# Edit the PgBouncer ConfigMap to update the host
kubectl -n settla edit configmap pgbouncer-<DB>-config
# Change the host from postgres-<DB>-0 to postgres-<DB>-1 (or the new RDS endpoint)

# Reload PgBouncer configuration (no restart needed)
kubectl -n settla exec deploy/pgbouncer-<DB> -- \
  psql -p 6432 -U settla pgbouncer -c "RELOAD;"

# Verify PgBouncer is connecting to the new primary
kubectl -n settla exec deploy/pgbouncer-<DB> -- \
  psql -p 6432 -U settla pgbouncer -c "SHOW SERVERS;"
```

### 7. Verify application reconnection

```bash
# Check settla-server logs for successful database connections
kubectl -n settla logs deploy/settla-server --tail=50 | grep -i "database\|postgres\|connection"

# Check settla-node logs
kubectl -n settla logs statefulset/settla-node --tail=50 | grep -i "database\|postgres\|connection"

# Verify API health endpoint
kubectl -n settla exec deploy/gateway -- curl -s http://settla-server:8080/health | jq .
```

### 8. Run reconciliation (if failover occurred)

```bash
# For Ledger DB: verify TB and PG balances match
# The TB->PG sync consumer will catch up automatically
curl -s "http://prometheus:9090/api/v1/query?query=settla_ledger_pg_sync_lag_seconds"

# For Transfer DB: check for any state inconsistencies
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT status, COUNT(*) FROM transfers
   WHERE updated_at > NOW() - INTERVAL '1 hour'
   GROUP BY status;"

# For Treasury DB: verify positions
kubectl -n settla exec statefulset/postgres-treasury-0 -- \
  psql -U settla -d settla_treasury -c \
  "SELECT tenant_id, currency, total_balance, locked_amount, available_balance
   FROM treasury_positions
   ORDER BY updated_at DESC LIMIT 20;"
```

### 9. Create a new replica

```bash
# After the incident is resolved, create a new replica from the promoted primary
# Self-managed: configure streaming replication on a new pod
# RDS: create a new read replica
aws rds create-db-instance-read-replica \
  --db-instance-identifier settla-<DB>-replica-new \
  --source-db-instance-identifier settla-<DB>-replica-1 \
  --region us-east-1
```

## Verification

- [ ] PgBouncer `SHOW POOLS` shows active server connections
- [ ] PgBouncer `SHOW SERVERS` shows the correct primary host
- [ ] `pg_isready` returns success on the new primary
- [ ] Application health endpoint returns healthy
- [ ] No 503 errors in gateway logs
- [ ] `settla_gateway_requests_total{status="503"}` rate back to zero
- [ ] Replication set up to new replica
- [ ] If Ledger DB: `settla_ledger_pg_sync_lag_seconds` < 1

## Post-Incident

1. **Data loss assessment:** Check the replication lag at the time of failover. Any WAL data not yet replicated to the promoted replica is lost.
2. **Old primary:** Once recovered, the old primary should be reconfigured as a replica (not promoted back without verification).
3. **Backups:** Verify a fresh backup is taken of the new primary.
4. **Post-mortem:** Required for any database failover event.
5. **Action items:**
   - If disk failure: review storage monitoring and alerting
   - If OOM: review memory limits and shared_buffers configuration
   - If network: review VPC/security group configuration
6. **Communication:** Notify affected tenants if any API errors were returned during the failover window.
