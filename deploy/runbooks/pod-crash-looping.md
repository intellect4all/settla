# Pod CrashLoopBackOff (SettlaPodCrashLooping)

## When to Use

- Alert: `SettlaPodCrashLooping` — a pod's restart count exceeds threshold (> 5 restarts in 10 minutes)
- `kubectl get pods` shows `CrashLoopBackOff` status for any Settla pod
- `kube_pod_container_status_restarts_total` rate is above threshold for a pod
- Application logs show repeated startup failures

## Impact

- Depends on which pod is crash-looping and how many replicas are healthy:
  - **settla-server** (6+ replicas): 1 crash-looping pod has minimal impact; PDB ensures 4 remain available
  - **settla-node** (8 instances): 1 crash-looping pod reduces worker throughput; other partitions unaffected
  - **gateway** (4+ replicas): 1 crash-looping pod has minimal impact; HPA maintains minimum
  - **Single-replica infra pods** (PgBouncer, NATS node, TigerBeetle): see dedicated runbooks

**Severity:** P2 — High (single pod). P1 — Critical (multiple pods or infrastructure).

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to pod logs (including previous container logs via `--previous`)
- Access to Grafana dashboards (Settla Overview)

## Steps

### 1. Identify the crash-looping pod

```bash
# List all pods with restart counts
kubectl -n settla get pods --sort-by='.status.containerStatuses[0].restartCount'

# Filter specifically for crash-looping pods
kubectl -n settla get pods | grep -E 'CrashLoop|Error|OOMKilled'

# Check restart counts across all pods
kubectl -n settla get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.containerStatuses[0].restartCount}{"\t"}{.status.phase}{"\n"}{end}' | sort -k2 -rn | head -20
```

### 2. Examine the crash reason

```bash
# Get the exit code and reason for the last crash
kubectl -n settla describe pod <POD_NAME> | grep -A 20 "Last State:"

# Check the previous container logs (before the last crash)
kubectl -n settla logs <POD_NAME> --previous --tail=200

# Check for OOM kills
kubectl -n settla describe pod <POD_NAME> | grep -i "OOMKilled\|Reason\|Exit Code"
```

Common crash causes:

| Exit Code | Reason | Likely Cause |
|-----------|--------|-------------|
| 137 | OOMKilled | Memory limit exceeded — see high-memory-usage.md |
| 1 | Error | Application startup failure (bad config, missing env var) |
| 2 | Misuse | Shell/binary not found |
| 139 | SIGSEGV | Segmentation fault (Go panic with signal) |
| 143 | SIGTERM | Pod terminated by Kubernetes (correct behavior, not a crash) |

### 3. Check for configuration issues

```bash
# Verify all required environment variables are set
kubectl -n settla describe pod <POD_NAME> | grep -A 100 "Environment:"

# Check ConfigMap and Secret references resolve
kubectl -n settla get configmap
kubectl -n settla get secret

# Verify the image pulled correctly
kubectl -n settla describe pod <POD_NAME> | grep -E "Image:|Image ID:|Pull"
```

### 4. Delete the crash-looping pod

Auto-remediation (`SettlaPodCrashLooping`) handles this automatically. To do it manually:

```bash
# Delete the pod — Deployment/StatefulSet controller reschedules a fresh replacement
kubectl -n settla delete pod <POD_NAME> --grace-period=30

# Watch the replacement pod start
kubectl -n settla get pods -w | grep <DEPLOYMENT_PREFIX>
```

If the replacement also crash-loops, the issue is not transient. Continue to Step 5.

### 5. Investigate persistent crash loops

```bash
# Check if the issue is node-specific
kubectl -n settla get pod <POD_NAME> -o jsonpath='{.spec.nodeName}'
kubectl get node <NODE_NAME>

# If node has issues, cordon it and delete the pod to reschedule on a healthy node
kubectl cordon <NODE_NAME>
kubectl -n settla delete pod <POD_NAME>

# Check for image pull issues
kubectl -n settla describe pod <POD_NAME> | grep -A 5 "Events:"

# Check resource limits — is the pod hitting CPU/memory limits?
kubectl -n settla top pod <POD_NAME>
```

### 6. Scale down crash-looping StatefulSet member (settla-node)

If a specific settla-node pod keeps crash-looping and restarting, temporarily reduce replicas:

```bash
# Check current replica count
kubectl -n settla get statefulset settla-node -o jsonpath='{.spec.replicas}'

# Reduce replicas by 1 to remove the crash-looping instance
kubectl -n settla scale statefulset settla-node --replicas=7

# Once root cause is fixed, restore
kubectl -n settla scale statefulset settla-node --replicas=8
```

### 7. Roll back a bad deployment

If the crash loop started immediately after a deployment:

```bash
# Check deployment history
kubectl -n settla rollout history deployment/<DEPLOYMENT_NAME>

# Roll back to previous revision
kubectl -n settla rollout undo deployment/<DEPLOYMENT_NAME>

# Wait for completion
kubectl -n settla rollout status deployment/<DEPLOYMENT_NAME> --timeout=120s
```

## Verification

- [ ] `kubectl get pods -n settla` shows no pods in `CrashLoopBackOff`
- [ ] Restart counts are stable (not increasing)
- [ ] Application health endpoint returns healthy: `curl -s http://settla-server:8080/health | jq .`
- [ ] `SettlaPodCrashLooping` alert resolved in AlertManager
- [ ] Grafana Settla Overview shows normal pod count

## Post-Incident

1. **Root cause:** OOM? Bad config? Image pull failure? Application bug?
2. **If OOM:** Increase memory limits in the deployment resource spec (see `deploy/k8s/base/`), then open a ticket to profile memory usage.
3. **If bad deploy:** Add the failure case to the analysis template to catch it earlier in canary rollout.
4. **If node issue:** Check node health; consider draining and replacing the node.
5. **Post-mortem:** Required if the crash loop affected > 2 replicas simultaneously or lasted > 30 minutes.
