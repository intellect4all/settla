# Treasury Over-Reservation

## When to Use

- Alert: `settla_treasury_locked / settla_treasury_balance > 0.95` for any tenant/currency pair
- Alert: `settla_treasury_flush_lag_seconds > 1` (flush goroutine falling behind)
- Tenant reports insufficient funds despite expected available balance
- Dashboard (Treasury Health) shows locked funds approaching or exceeding total balance

## Impact

- **Affected tenant cannot initiate new transfers** for the over-reserved currency.
- Other tenants are unaffected (treasury positions are per-tenant, fully isolated).
- Existing in-flight transfers continue normally.
- No data loss risk; this is a liveness issue, not a safety issue.

**Severity:** P2 -- High. Respond within 1 hour.

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to Grafana Treasury Health dashboard
- Access to Treasury DB (via PgBouncer or direct)

## Steps

### 1. Identify the affected tenant and currency

```bash
# Query Prometheus for over-reserved positions
curl -s "http://prometheus:9090/api/v1/query?query=settla_treasury_locked/settla_treasury_balance>0.9" | jq '.data.result'

# Check Grafana Treasury Health dashboard for:
# - "Treasury Balance vs Locked" panel
# - "Reservation Rate by Tenant" panel
```

Note the `tenant` and `currency` labels from the alert.

### 2. Suspend the tenant (if critically over-reserved)

If locked > balance (over-reservation), immediately suspend the tenant to prevent further damage:

```bash
# Suspend tenant API key in Transfer DB (prevents new API calls)
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "UPDATE api_keys SET suspended_at = NOW(), suspend_reason = 'over-reservation investigation' WHERE tenant_id = '<TENANT_ID>' AND suspended_at IS NULL;"

# Verify suspension
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT id, tenant_id, suspended_at, suspend_reason FROM api_keys WHERE tenant_id = '<TENANT_ID>';"
```

### 3. Check for stale reservations

Reservations are held in-memory by settla-server pods. A pod that crashed without releasing its reservations (before the preStop flush completed) can leave orphaned locks.

```bash
# Check which settla-server pods are running
kubectl -n settla get pods -l app.kubernetes.io/name=settla-server -o wide

# Check for recently restarted pods (indicates a crash)
kubectl -n settla get pods -l app.kubernetes.io/name=settla-server \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.containerStatuses[0].restartCount}{"\t"}{.status.containerStatuses[0].lastState.terminated.reason}{"\n"}{end}'

# Check the treasury flush lag (should be < 200ms normally)
curl -s "http://prometheus:9090/api/v1/query?query=settla_treasury_flush_lag_seconds"
```

### 4. Check for stuck transfers holding reservations

```bash
# Find transfers in non-terminal states for the affected tenant
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT id, status, amount, currency, created_at, updated_at
   FROM transfers
   WHERE tenant_id = '<TENANT_ID>'
     AND currency = '<CURRENCY>'
     AND status NOT IN ('completed', 'failed', 'cancelled')
   ORDER BY created_at DESC
   LIMIT 20;"

# Check for transfers older than 30 minutes in non-terminal state (likely stuck)
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT id, status, amount, currency, created_at
   FROM transfers
   WHERE tenant_id = '<TENANT_ID>'
     AND currency = '<CURRENCY>'
     AND status NOT IN ('completed', 'failed', 'cancelled')
     AND created_at < NOW() - INTERVAL '30 minutes'
   ORDER BY created_at;"
```

### 5. Release stale reservations

If a pod crashed, its in-memory reservations are lost. On restart, the pod reconstructs positions from the Treasury DB. If the DB snapshot is stale, reservations may be incorrect.

```bash
# Option A: Rolling restart of settla-server pods (reconstructs all positions from DB)
kubectl -n settla rollout restart deployment/settla-server

# Wait for rollout to complete
kubectl -n settla rollout status deployment/settla-server --timeout=120s
```

If rolling restart does not resolve the issue, the Treasury DB position snapshot may be incorrect:

```bash
# Option B: Check the treasury position in the database
kubectl -n settla exec statefulset/postgres-treasury-0 -- \
  psql -U settla -d settla_treasury -c \
  "SELECT tenant_id, currency, total_balance, locked_amount, available_balance, updated_at
   FROM treasury_positions
   WHERE tenant_id = '<TENANT_ID>' AND currency = '<CURRENCY>';"

# Compare locked_amount against sum of in-flight transfer amounts
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT SUM(amount) AS expected_locked
   FROM transfers
   WHERE tenant_id = '<TENANT_ID>'
     AND currency = '<CURRENCY>'
     AND status IN ('initiated', 'quoted', 'reserved', 'ledger_posting', 'executing');"
```

If `locked_amount` in the treasury position exceeds the sum of in-flight transfer amounts, the position needs correction:

```bash
# Option C: Correct the treasury position (use expected_locked from above)
kubectl -n settla exec statefulset/postgres-treasury-0 -- \
  psql -U settla -d settla_treasury -c \
  "UPDATE treasury_positions
   SET locked_amount = <EXPECTED_LOCKED>,
       available_balance = total_balance - <EXPECTED_LOCKED>,
       updated_at = NOW()
   WHERE tenant_id = '<TENANT_ID>' AND currency = '<CURRENCY>';"

# Then restart settla-server to reload corrected positions
kubectl -n settla rollout restart deployment/settla-server
kubectl -n settla rollout status deployment/settla-server --timeout=120s
```

### 6. Unsuspend the tenant

Once positions are verified correct:

```bash
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "UPDATE api_keys SET suspended_at = NULL, suspend_reason = NULL WHERE tenant_id = '<TENANT_ID>';"
```

### 7. Verify resolution

```bash
# Check treasury position is healthy
curl -s "http://prometheus:9090/api/v1/query?query=settla_treasury_locked{tenant='<TENANT_SLUG>'}/settla_treasury_balance{tenant='<TENANT_SLUG>'}"
# Expected: < 0.9

# Verify new transfers can be created for the tenant
# (Test via gateway or check Grafana Tenant Health dashboard)
```

## Verification

- [ ] Treasury locked/balance ratio < 0.9 for affected tenant
- [ ] `settla_treasury_flush_lag_seconds` < 200ms
- [ ] Tenant can create new transfers (if previously suspended, now unsuspended)
- [ ] No stuck transfers older than 30 minutes
- [ ] Grafana Treasury Health shows stable positions

## Post-Incident

1. **Root cause analysis:** Why did over-reservation occur?
   - Pod crash without clean shutdown?
   - Stuck transfers holding reservations indefinitely?
   - Bug in release logic (transfers completing without releasing reservation)?
2. **Check other tenants:** Verify no other tenants have similar over-reservation patterns.
3. **Post-mortem:** Required if a tenant was suspended for > 30 minutes.
4. **Action items:** Consider adding automated stale reservation detection (cron job comparing DB positions against in-flight transfer sums).
5. **Communication:** Notify the tenant of the incident and resolution.
