# High Memory Usage (SettlaHighMemoryUsage)

## When to Use

- Alert: `SettlaHighMemoryUsage` — pod memory usage > 90% of limit
- Alert: `container_memory_working_set_bytes / container_spec_memory_limit_bytes > 0.9` for any Settla pod
- OOMKilled events appearing in `kubectl describe pod`
- Grafana Capacity Planning shows memory usage trending toward limits

## Impact

- Pod approaching OOM kill. If killed, a replacement pod is scheduled automatically (< 45s recovery).
- **Treasury positions are safe**: the preStop hook flushes positions to Postgres before termination.
- **In-flight transfers are safe**: the outbox guarantees all state is in the database; NATS redelivers any unack'd messages.
- Auto-remediation (`SettlaHighMemoryUsage`) deletes the highest-memory pod to clear heap fragmentation and restore headroom.

**Severity:** P2 — High (single pod at 90%). P1 — Critical (multiple pods or OOM kills occurring).

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to Grafana Capacity Planning dashboard
- Access to pprof endpoint (settla-server :6060/debug/pprof/) for heap analysis

## Steps

### 1. Identify high-memory pods

```bash
# Sort pods by memory usage
kubectl -n settla top pods --sort-by=memory

# Check memory usage as percentage of limit
kubectl -n settla get pods -o json | jq -r '
  .items[] |
  select(.status.containerStatuses != null) |
  .metadata.name as $pod |
  .spec.containers[0].resources.limits.memory as $limit |
  {pod: $pod, limit: $limit}
' | head -20

# Query Prometheus for exact percentage
curl -s "http://prometheus:9090/api/v1/query?query=container_memory_working_set_bytes{namespace='settla',container!='POD'}/container_spec_memory_limit_bytes{namespace='settla',container!='POD'}" | jq '.data.result | sort_by(-.value[1]) | .[0:10]'
```

### 2. Examine memory growth trend

```bash
# Memory growth over last hour for each pod
curl -s "http://prometheus:9090/api/v1/query_range?query=container_memory_working_set_bytes{namespace='settla',pod=~'settla-server.*'}&start=$(date -d '-1 hour' +%s)&end=$(date +%s)&step=60" | jq '.data.result'

# Check Go heap stats from pprof (settla-server only)
kubectl -n settla port-forward pod/<SETTLA_SERVER_POD> 6060:6060 &
curl -s 'http://localhost:6060/debug/pprof/heap?debug=1' | head -50
kill %1
```

### 3. Check for memory leaks (common patterns)

```bash
# Check goroutine count (goroutine leak increases memory over time)
curl -s 'http://localhost:6060/debug/pprof/goroutine?debug=1' | head -20

# Check for large caches (local LRU growing unbounded)
# Look for alloc_space in heap profile
curl -s 'http://localhost:6060/debug/pprof/alloc?debug=1' | head -30
```

Common memory growth causes in Settla:
- **Goroutine leak**: a background goroutine never exits (check NATS subscriber, background workers)
- **Local cache growth**: LRU eviction not working correctly (check cache package)
- **Large NATS buffer**: JetStream message backlog held in memory
- **Heap fragmentation**: GC not returning memory to OS (normal for Go — pod restart clears it)

### 4. Delete the high-memory pod (auto-remediation action)

Auto-remediation (`SettlaHighMemoryUsage`) handles this automatically. The pod is deleted gracefully with a 45-second grace period, allowing the preStop hook to flush treasury positions.

To do it manually:

```bash
# Identify the highest-memory pod
HIGHEST_MEM_POD=$(kubectl -n settla top pods --sort-by=memory --no-headers | awk '{print $1}' | head -1)
echo "Highest memory pod: $HIGHEST_MEM_POD"

# Delete it (grace period allows preStop hook to run — flushes treasury positions)
kubectl -n settla delete pod "$HIGHEST_MEM_POD" --grace-period=45

# Watch replacement pod start
kubectl -n settla get pods -w | grep settla-server
```

### 5. Verify memory is stable after restart

```bash
# Monitor memory of replacement pod over first 5 minutes
watch -n 30 'kubectl -n settla top pods | grep settla-server'

# Check that memory is not immediately spiking again (would indicate a leak)
curl -s "http://prometheus:9090/api/v1/query?query=container_memory_working_set_bytes{namespace='settla',pod=~'settla-server.*'}/container_spec_memory_limit_bytes{namespace='settla',pod=~'settla-server.*'}" | jq '.data.result'
```

### 6. Increase memory limits (if pods are consistently near limit)

If all pods are regularly at 80%+ memory, the limits are too low. Update the resource spec:

```bash
# Check current limits
kubectl -n settla get deployment settla-server -o jsonpath='{.spec.template.spec.containers[0].resources}'

# Edit to increase limits (e.g., 8Gi → 12Gi)
# Update deploy/k8s/base/settla-server/deployment.yaml or rollout.yaml
# Then apply:
kubectl -n settla apply -f deploy/k8s/base/settla-server/

# Rolling restart with new limits
kubectl -n settla rollout restart deployment/settla-server
kubectl -n settla rollout status deployment/settla-server --timeout=120s
```

### 7. Capture heap profile before pod deletion (for debugging)

If you want to investigate before deleting:

```bash
# Forward pprof port
kubectl -n settla port-forward pod/<HIGH_MEM_POD> 6060:6060 &

# Capture heap profile
curl -s 'http://localhost:6060/debug/pprof/heap' > /tmp/heap_$(date +%s).pprof

# Analyse with go tool pprof
go tool pprof -top /tmp/heap_*.pprof

kill %1
```

## Verification

- [ ] All settla-server pods memory < 80% of limit
- [ ] `SettlaHighMemoryUsage` alert resolved in AlertManager
- [ ] No OOMKilled events in recent pod describe output
- [ ] Replacement pod memory is stable (not growing rapidly)
- [ ] Treasury positions flushed correctly before pod deletion (check treasury DB)

## Post-Incident

1. **Root cause:** Memory leak? Insufficient limits? Traffic spike? Heap fragmentation?
2. **If memory leak:** Capture a heap profile, identify the growing allocator, open a bug ticket.
3. **If limits too low:** Update resource limits in `deploy/k8s/base/settla-server/` and note the new baseline in `docs/cost-estimation.md`.
4. **Heap fragmentation:** Normal for long-lived Go processes. Consider configuring `GOGC` or `GOMEMLIMIT` to tune GC pressure.
5. **Post-mortem:** Required if OOM kills caused customer-facing errors or treasury position inconsistency.
