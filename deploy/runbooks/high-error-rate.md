# High Error Rate (SettlaHighErrorRate)

## When to Use

- Alert: `SettlaHighErrorRate` or `SettlaHighErrorRateFastBurn` — error budget fast burn (14.4x rate over 5 min/1 hr windows)
- Alert: `settla_gateway_requests_total{status=~"5.."}` rate > 0.1% of total requests
- Grafana Settla Overview shows "Error Rate" gauge above threshold
- Canary deployment is in progress and error rate spiked after new version rolled out

## Impact

- Tenant API calls returning 5xx errors. Transfers may fail to initiate.
- If error budget is burning fast, SLA breach is imminent.
- Auto-remediation (`SettlaHighErrorRate`) triggers Argo Rollouts abort + undo if a canary is active.

**Severity:** P1 — Critical. Page on-call immediately.

## Prerequisites

- `kubectl` access to the `settla` namespace
- Argo Rollouts CLI (`kubectl argo rollouts`) if using canary deployments
- Access to Grafana dashboards (Settla Overview, Error Budget)
- Access to Loki/CloudWatch logs

## Steps

### 1. Identify the error source

```bash
# Check current error rate from Prometheus
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_gateway_requests_total{status=~'5..'}[5m]))/sum(rate(settla_gateway_requests_total[5m]))" | jq '.data.result'

# Check which endpoints are erroring
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_gateway_requests_total{status=~'5..'}[5m]))by(path,method)" | jq '.data.result | sort_by(-.value[1]) | .[0:10]'

# Check gateway logs for errors
kubectl -n settla logs deploy/gateway --tail=200 | grep -E '"status":(5[0-9]{2})'

# Check settla-server logs for panics or errors
kubectl -n settla logs deploy/settla-server --tail=200 | grep -iE 'error|panic|fatal'
```

### 2. Check if a canary deployment is in progress

```bash
# Check Argo Rollout status
kubectl -n settla get rollout settla-server

# Detailed canary status
kubectl -n settla argo rollouts get rollout settla-server
```

If a canary is active and the error rate spiked after the canary started, the auto-remediation will have already initiated a rollback. Verify:

```bash
# Check if rollback was triggered by auto-remediation
kubectl -n settla argo rollouts get rollout settla-server --watch
```

### 3. Manual rollback (if auto-remediation did not fire)

```bash
# Abort the current canary and revert to stable
kubectl -n settla argo rollouts abort settla-server
kubectl -n settla argo rollouts undo settla-server

# Wait for rollback to complete
kubectl -n settla argo rollouts get rollout settla-server --watch
```

If not using Argo Rollouts (standard Deployment):

```bash
# Check rollout history
kubectl -n settla rollout history deployment/settla-server

# Rollback to previous revision
kubectl -n settla rollout undo deployment/settla-server

# Wait for completion
kubectl -n settla rollout status deployment/settla-server --timeout=120s
```

### 4. Identify root cause of errors

```bash
# Check if errors are database-related
kubectl -n settla logs deploy/settla-server --tail=500 | grep -iE 'postgres|database|connection refused|timeout'

# Check if errors are NATS-related
kubectl -n settla logs deploy/settla-server --tail=500 | grep -iE 'nats|jetstream|publish'

# Check if errors are TigerBeetle-related
kubectl -n settla logs deploy/settla-server --tail=500 | grep -iE 'tigerbeetle|ledger'

# Check settla-server health endpoint
kubectl -n settla exec deploy/gateway -- curl -s http://settla-server:8080/health | jq .
```

### 5. Check downstream dependencies

```bash
# Database connectivity
for db in ledger transfer treasury; do
  echo "--- pgbouncer-$db ---"
  kubectl -n settla exec deploy/pgbouncer-$db -- psql -U settla -d settla_$db -c "SELECT 1;" 2>&1 | head -3
done

# NATS health
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/healthz

# TigerBeetle connectivity
kubectl -n settla exec deploy/settla-server -- nc -zv tigerbeetle 3001
```

### 6. Verify recovery

```bash
# Watch error rate — should drop to near 0 after rollback
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_gateway_requests_total{status=~\"5..\"}[2m]))/sum(rate(settla_gateway_requests_total[2m]))" | jq ".data.result[0].value[1]"'

# Verify gateway health
kubectl -n settla exec deploy/gateway -- curl -s http://settla-server:8080/health | jq .status
```

## Verification

- [ ] Error rate < 0.1% of total requests
- [ ] `SettlaHighErrorRate` alert resolved in AlertManager
- [ ] No 5xx errors in recent gateway logs
- [ ] Rollback (if triggered) completed successfully
- [ ] settla-server health endpoint returns healthy

## Post-Incident

1. **Root cause:** Was this a bad deployment? Infrastructure failure? Dependency outage?
2. **Error budget impact:** Calculate burn-down and update SLA tracking.
3. **Canary analysis:** If a canary caused this, review the analysis template thresholds — they should have caught this before auto-remediation was needed.
4. **Post-mortem:** Required for any incident where P1 alert fired.
5. **Communication:** Notify affected tenants if any transfer API calls returned 5xx errors.
