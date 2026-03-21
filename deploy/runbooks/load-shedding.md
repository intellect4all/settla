# Load Shedding Critical (SettlaLoadSheddingCritical)

## Alert Description

- **Alert:** `SettlaLoadSheddingActive` (warning, any shedding for 1m) / `SettlaLoadSheddingCritical` (critical, >100 req/s rejected for 1m)
- **Metric:** `rate(settla_loadshed_rejected_total[5m])`
- **Threshold:** Warning >0 req/s, Critical >100 req/s
- Load shedding is rejecting incoming requests to protect system stability. The system is at or beyond capacity. Tenants will receive HTTP 503 responses for shed requests.

**Severity:** P1 at >100 req/s. Significant portion of tenant traffic is being dropped.

## Diagnostic Steps

### 1. Check current shed rate

```bash
curl -s "http://prometheus:9090/api/v1/query?query=rate(settla_loadshed_rejected_total[5m])" | jq '.data.result'
```

### 2. Check what is driving the overload

```bash
# CPU pressure
curl -s "http://prometheus:9090/api/v1/query?query=(1-avg(rate(node_cpu_seconds_total{mode=%22idle%22}[5m]))by(instance))" | jq '.data.result'

# Memory pressure
curl -s "http://prometheus:9090/api/v1/query?query=(1-(node_memory_MemAvailable_bytes/node_memory_MemTotal_bytes))" | jq '.data.result'

# Request rate (is there a traffic surge?)
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_gateway_requests_total[5m]))" | jq '.data.result'
```

### 3. Check if a specific tenant is flooding

```bash
# Check per-tenant request rates
kubectl -n settla logs deploy/gateway --tail=500 | grep -oP '"tenant_id":"[^"]*"' | sort | uniq -c | sort -rn | head -10
```

### 4. Check settla-server resource usage

```bash
kubectl -n settla top pod -l app.kubernetes.io/name=settla-server
kubectl -n settla top pod -l app.kubernetes.io/name=gateway
```

### 5. Check downstream bottlenecks

```bash
# PgBouncer saturation
curl -s "http://prometheus:9090/api/v1/query?query=pgbouncer_pools_server_active/pgbouncer_pools_server_max" | jq '.data.result'

# NATS consumer lag
curl -s "http://prometheus:9090/api/v1/query?query=settla_nats_partition_queue_depth" | jq '.data.result'
```

## Remediation Steps

1. **Scale up immediately:**
   ```bash
   # Scale gateway
   kubectl -n settla scale deployment/gateway --replicas=8

   # Scale settla-server
   kubectl -n settla scale deployment/settla-server --replicas=10

   # Scale settla-node workers
   kubectl -n settla scale statefulset/settla-node --replicas=12
   ```

2. **If a single tenant is flooding:** Apply stricter rate limits:
   ```bash
   # Check current rate limit config
   kubectl -n settla logs deploy/gateway --tail=100 | grep -i 'rate.limit'
   ```
   Consider temporarily reducing the tenant's rate limit in the tenant configuration.

3. **If traffic is legitimate (peak hours):** The system needs more capacity. Scale up and plan for permanent capacity increase.

4. **If traffic is illegitimate (attack or retry storm):** Block at the ingress/WAF level:
   ```bash
   # Check for suspicious traffic patterns
   kubectl -n settla logs deploy/gateway --tail=1000 | grep -oP '"method":"\w+","path":"[^"]*"' | sort | uniq -c | sort -rn | head -20
   ```

### Verify recovery

```bash
watch -n 10 'curl -s "http://prometheus:9090/api/v1/query?query=rate(settla_loadshed_rejected_total[5m])" | jq ".data.result[0].value[1]"'
```

Shed rate should drop to 0 after scaling or traffic reduction.

## Escalation Criteria

- If shedding >500 req/s, page the engineering lead and infrastructure team.
- If shedding persists after scaling to max replicas, escalate to capacity planning -- the system has hit its horizontal scaling limit.
- If caused by a tenant abuse, escalate to the tenant relations team for communication.
- If caused by a retry storm (e.g., tenant retrying 503s aggressively), contact the tenant to implement exponential backoff.
