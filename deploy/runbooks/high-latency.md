# High Latency (SettlaHighLatency)

## When to Use

- Alert: `SettlaHighLatency` — p95 or p99 latency exceeds SLO threshold
- Alert: `settla_gateway_request_duration_seconds` p95 > 200ms (quote) or p99 > 500ms (transfer)
- Grafana Settla Overview shows "Quote Latency p95" or "Transfer Latency p99" above SLO
- Tenant reports slow API response times
- SLI burn rate alert fires on latency error budget

## Impact

- Tenant API calls are slow. Transfers still complete but take longer.
- If latency is extreme (> 30s), client timeouts may cause apparent failures.
- Auto-remediation (`SettlaHighLatency`) scales up gateway and settla-server replicas by calling `handle_load_shedding_critical`.

**Severity:** P2 — High (latency SLO breach). P1 — Critical (if transfers timing out).

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to Grafana dashboards (Settla Overview, Capacity Planning)
- Access to Prometheus for latency breakdown queries

## Steps

### 1. Identify where latency is occurring

```bash
# Overall gateway request latency p95
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.95,sum(rate(settla_gateway_request_duration_seconds_bucket[5m]))by(le,path))" | jq '.data.result | sort_by(-.value[1])'

# gRPC call latency (gateway → settla-server)
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,sum(rate(settla_grpc_server_handling_seconds_bucket[5m]))by(le,grpc_method))" | jq '.data.result | sort_by(-.value[1])'

# Database query latency
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,sum(rate(settla_db_query_duration_seconds_bucket[5m]))by(le,query_name))" | jq '.data.result | sort_by(-.value[1])'

# TigerBeetle write latency
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,rate(settla_ledger_tb_write_latency_seconds_bucket[5m]))" | jq '.data.result'
```

### 2. Check system resource saturation

```bash
# CPU usage per pod
kubectl -n settla top pods --sort-by=cpu | head -20

# Memory usage per pod
kubectl -n settla top pods --sort-by=memory | head -20

# Node resource pressure
kubectl top nodes

# Check for throttled CPU (indicates requests too low, limits hit)
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(container_cpu_cfs_throttled_seconds_total{namespace='settla'}[5m]))by(pod)" | jq '.data.result | sort_by(-.value[1]) | .[0:10]'
```

### 3. Check PgBouncer connection pool saturation

```bash
# Check PgBouncer wait queue depth (high cl_waiting = connection starvation)
for db in ledger transfer treasury; do
  echo "--- pgbouncer-$db ---"
  kubectl -n settla exec deploy/pgbouncer-$db -- \
    psql -p 6432 -U settla pgbouncer -c "SHOW POOLS;" 2>/dev/null | grep -v "^$\|row\|---"
done

# Check slow queries in Postgres
kubectl -n settla exec statefulset/postgres-transfer-0 -- \
  psql -U settla -d settla_transfer -c \
  "SELECT pid, now() - pg_stat_activity.query_start AS duration, query
   FROM pg_stat_activity
   WHERE (now() - pg_stat_activity.query_start) > interval '1 second'
   AND state = 'active'
   ORDER BY duration DESC LIMIT 10;"
```

### 4. Check NATS queue depth (worker backlog)

```bash
# NATS consumer queue depth per partition
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '.data.result'

# If queue depth > 10K, workers are falling behind — scale settla-node
kubectl -n settla get statefulset settla-node -o jsonpath='{.spec.replicas}'
```

### 5. Scale up to handle load (if resource saturation)

Auto-remediation does this automatically via `handle_load_shedding_critical`. To do it manually:

```bash
# Scale settla-server
kubectl -n settla scale deployment settla-server --replicas=10

# Scale gateway
kubectl -n settla scale deployment gateway --replicas=6

# Scale settla-node (if NATS queue depth is high)
kubectl -n settla scale statefulset settla-node --replicas=12

# Wait for new pods to become ready
kubectl -n settla rollout status deployment/settla-server --timeout=120s
```

### 6. Check for slow external providers

```bash
# Provider latency p95 by provider
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.95,sum(rate(settla_provider_latency_seconds_bucket[5m]))by(le,provider))" | jq '.data.result'

# Check if circuit breakers have tripped (provider unreachable)
curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state" | jq '.data.result'
```

If a provider is slow, the transfer pipeline may back up. Check if the provider's status page shows an incident.

### 7. Check for cache miss storm

```bash
# Local cache hit rate (should be > 95% for auth lookups)
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_cache_hits_total[5m]))/sum(rate(settla_cache_requests_total[5m]))" | jq '.data.result'

# Redis latency
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,rate(settla_redis_command_duration_seconds_bucket[5m]))" | jq '.data.result'
```

A low cache hit rate forces every auth lookup to hit Redis or the database, adding ~0.5ms per request.

## Verification

- [ ] Gateway request p95 latency < 200ms (quotes) / p99 < 500ms (transfers)
- [ ] `SettlaHighLatency` alert resolved in AlertManager
- [ ] PgBouncer `cl_waiting` = 0 across all 3 databases
- [ ] NATS consumer queue depth < 1,000 per partition
- [ ] No CPU throttling on settla-server or gateway pods
- [ ] Provider latency p95 within normal bounds

## Post-Incident

1. **Root cause:** Traffic spike? Database slow query? Provider degraded? Cache miss storm?
2. **Capacity review:** If latency was caused by traffic exceeding current capacity, update HPA min/max replicas in `deploy/k8s/base/settla-server/hpa.yaml`.
3. **Slow query:** If a slow database query caused the issue, add an index and monitor query plans.
4. **Provider:** If a provider was slow, consider increasing circuit breaker thresholds or adding timeout/fallback logic.
5. **Post-mortem:** Required if latency SLO breach lasted > 30 minutes or affected > 10% of requests.
