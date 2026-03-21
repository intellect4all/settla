# Circuit Breaker Open (SettlaCircuitBreakerOpen)

## Alert Description

- **Alert:** `SettlaCircuitBreakerOpen` (critical, fires after 10s)
- **Metric:** `settla_circuit_breaker_state` (0=closed, >0=open)
- **Labels:** `name` (breaker name), `target` (downstream dependency), `instance`
- A circuit breaker has tripped open, meaning a downstream dependency is unhealthy. All requests to that dependency are being short-circuited (fast-failed) to prevent cascade failures.

**Severity:** P1 -- Critical. Affected transfer flows will fail until the dependency recovers and the breaker closes.

## Diagnostic Steps

### 1. Identify which breaker is open

```bash
curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state>0" | jq '.data.result'
```

### 2. Check the target dependency health

```bash
# Check settla-server logs for the breaker trip reason
kubectl -n settla logs deploy/settla-server --tail=300 | grep -iE 'circuit.?breaker|tripped|open'

# Check recent error rate to the target
kubectl -n settla logs deploy/settla-server --tail=500 | grep -i "<TARGET_NAME>" | grep -iE 'error|timeout|refused'
```

### 3. Common targets and their health checks

**Provider (on-ramp/off-ramp):**
```bash
# Check provider adapter errors
kubectl -n settla logs deploy/settla-server --tail=200 | grep -iE 'provider|onramp|offramp'
```

**TigerBeetle:**
```bash
kubectl -n settla exec deploy/settla-server -- nc -zv tigerbeetle 3001
```

**Postgres (via PgBouncer):**
```bash
for db in ledger transfer treasury; do
  echo "--- pgbouncer-$db ---"
  kubectl -n settla exec deploy/pgbouncer-$db -- psql -U settla -d settla_$db -c "SELECT 1;" 2>&1 | head -3
done
```

**NATS:**
```bash
kubectl -n settla exec statefulset/nats-0 -- curl -s http://localhost:8222/healthz
```

### 4. Check breaker configuration

The breaker typically requires N consecutive failures to trip and a timeout period before half-open retry. Check settla-server config for breaker thresholds.

## Remediation Steps

1. **Fix the downstream dependency.** The breaker opened for a reason -- the dependency must be restored to health.
2. **Do NOT manually force the breaker closed** unless the dependency is confirmed healthy. The breaker will automatically transition to half-open and then closed once the timeout elapses and test requests succeed.
3. **If a provider is down:** The router should failover to alternate providers. Check if failover is happening:
   ```bash
   kubectl -n settla logs deploy/settla-server --tail=100 | grep -i 'failover\|fallback\|route'
   ```
4. **If the breaker is stuck open** after the dependency recovers, restart the affected settla-server pods:
   ```bash
   kubectl -n settla rollout restart deployment/settla-server
   ```

### Verify recovery

```bash
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state" | jq ".data.result"'
```

## Escalation Criteria

- If the downstream dependency cannot be restored within 5 minutes, escalate to the owning team (provider ops, DBA, infrastructure).
- If multiple breakers open simultaneously, treat as a broader infrastructure incident (P0).
- If provider breaker is open and no failover provider is available for the corridor, notify affected tenants.
