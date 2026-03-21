# Transfer Success Rate Low (SettlaTransferSuccessRateLow)

## Alert Description

- **Alert:** `SettlaTransferSuccessLow` (warning, <99.5% over 30m for 10m) / `SettlaTransferSuccessCritical` (critical, <95% over 10m for 5m)
- **Metric:** `settla_transfers_total{status="completed"}` / `settla_transfers_total`
- **Threshold:** Warning <99.5%, Critical <95%
- This is the business-level SLI -- distinct from HTTP error rate. Measures the percentage of transfers that reach `COMPLETED` state vs all initiated transfers.

**Severity:** P2 at <99.5%, P1 at <95%. Tenants are experiencing failed transfers. Revenue impact is direct.

## Diagnostic Steps

### 1. Check current success rate

```bash
curl -s "http://prometheus:9090/api/v1/query?query=(sum(rate(settla_transfers_total{status=%22completed%22}[30m]))/sum(rate(settla_transfers_total[30m])))" | jq '.data.result'
```

### 2. Identify which terminal state transfers are landing in

```bash
# Check failure breakdown by status
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_transfers_total[30m]))by(status)" | jq '.data.result'
```

### 3. Check for stuck transfers

```bash
# Transfers stuck in non-terminal states for >60s trigger recovery
kubectl -n settla logs deploy/settla-server --tail=300 | grep -iE 'stuck|recovery|manual.review'

# Check stuck transfer count from DB
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT status, COUNT(*) as count
   FROM transfers
   WHERE created_at > NOW() - INTERVAL '1 hour'
     AND status NOT IN ('COMPLETED', 'FAILED')
   GROUP BY status
   ORDER BY count DESC;"
```

### 4. Check provider health

```bash
# Circuit breakers
curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state>0" | jq '.data.result'

# Provider error rates
kubectl -n settla logs deploy/settla-server --tail=300 | grep -iE 'provider.*error|onramp.*fail|offramp.*fail'
```

### 5. Check NATS consumer lag (workers may not be processing)

```bash
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '.data.result'
```

### 6. Check outbox relay (intents may not be published)

```bash
curl -s "http://prometheus:9090/api/v1/query?query=settla_outbox_relay_lag_seconds" | jq '.data.result'
```

## Remediation Steps

1. **If provider is failing:** Check circuit breaker state. If open, the router should failover. If no alternative provider exists for the corridor, transfers will fail until the provider recovers.

2. **If transfers are stuck (not failed, not completed):** The recovery detector (`core/recovery`) should re-publish stalled intents after 60s. If recovery is not working:
   ```bash
   kubectl -n settla logs deploy/settla-server --tail=100 | grep -i 'recovery.*detector'
   ```

3. **If outbox relay is lagged:** See `deploy/runbooks/outbox-relay-lag.md`.

4. **If NATS consumers are lagged:** See `deploy/runbooks/nats-consumer-lag.md`.

5. **If compensation flows are failing:** Check compensation strategy execution:
   ```bash
   kubectl -n settla logs deploy/settla-server --tail=200 | grep -iE 'compensation|refund|reverse'
   ```

### Verify recovery

```bash
watch -n 30 'curl -s "http://prometheus:9090/api/v1/query?query=(sum(rate(settla_transfers_total{status=%22completed%22}[10m]))/sum(rate(settla_transfers_total[10m])))" | jq ".data.result[0].value[1]"'
```

## Escalation Criteria

- If success rate <95% for more than 10 minutes, page the engineering lead.
- If success rate <90%, treat as P0 -- the transfer pipeline is broken.
- If a specific corridor is entirely failing (e.g., all NGN-to-USDT transfers), notify affected tenants immediately.
- If manual reviews are piling up, alert the ops team for manual intervention.
