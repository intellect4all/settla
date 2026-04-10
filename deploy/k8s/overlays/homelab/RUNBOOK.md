# Settla Homelab Operations Runbook

Day-2 operations guide for the Settla homelab k3s deployment. Covers monitoring, scaling, troubleshooting, load testing, and recovery procedures.

---

## Table of Contents

1. [Daily Operations](#1-daily-operations)
2. [Monitoring](#2-monitoring)
3. [Scaling](#3-scaling)
4. [Load Testing](#4-load-testing)
5. [Troubleshooting](#5-troubleshooting)
6. [Node Management](#6-node-management)
7. [Secret Rotation](#7-secret-rotation)
8. [Backup and Recovery](#8-backup-and-recovery)
9. [Teardown and Redeployment](#9-teardown-and-redeployment)

---

## 1. Daily Operations

### Health Check

```bash
# Quick status overview
make k8s-homelab-status

# Full validation (nodes, pods, connectivity, PVCs, NATS)
make k8s-homelab-validate
```

### View Logs

```bash
# settla-server logs (all replicas, follow)
kubectl logs -n settla -l app.kubernetes.io/name=settla-server -f --tail=100

# settla-node logs (all replicas)
kubectl logs -n settla -l app.kubernetes.io/name=settla-node -f --tail=100

# Gateway logs
kubectl logs -n settla -l app.kubernetes.io/name=gateway -f --tail=100

# Specific pod
kubectl logs -n settla settla-server-<hash> -f

# Previous container (after crash)
kubectl logs -n settla <pod-name> --previous
```

### Restart a Service

```bash
# Rolling restart (zero-downtime for Deployments with >1 replica)
kubectl rollout restart deployment/settla-server -n settla
kubectl rollout restart statefulset/settla-node -n settla

# Watch rollout progress
kubectl rollout status deployment/settla-server -n settla

# Force-restart a single pod (it will be recreated)
kubectl delete pod settla-server-<hash> -n settla
```

### Redeploy After Config Change

If you edited a patch file:

```bash
make k8s-homelab-deploy
```

If you only changed a ConfigMap, pods must restart to pick up the change:

```bash
make k8s-homelab-deploy
kubectl rollout restart deployment/settla-server -n settla
kubectl rollout restart statefulset/settla-node -n settla
kubectl rollout restart deployment/gateway -n settla
```

---

## 2. Monitoring

### Grafana

Access: `http://<any-node-ip>:30030`
Default credentials: admin / `<grafana-admin-password from secrets.yaml>`

Key dashboards to watch during load tests:
- **Transaction throughput** — should hit 580 TPS sustained
- **p99 latency** — should stay under SLA target (200ms)
- **PgBouncer pool utilization** — should stay below 80%
- **NATS consumer lag** — should not build up
- **Node CPU/memory** — no node should exceed 80% CPU

### Prometheus

```bash
# Port-forward to access Prometheus UI
kubectl port-forward -n settla svc/prometheus 9090:9090

# Open http://localhost:9090
```

Useful PromQL queries:

```promql
# Transaction rate (per second)
rate(settla_transfers_total[1m])

# p99 latency
histogram_quantile(0.99, rate(settla_transfer_duration_seconds_bucket[5m]))

# PgBouncer active connections by pool
pgbouncer_pools_server_active_connections

# NATS consumer pending messages (lag)
nats_consumer_num_pending

# Container CPU usage
sum(rate(container_cpu_usage_seconds_total{namespace="settla"}[5m])) by (pod)

# Container memory usage
sum(container_memory_working_set_bytes{namespace="settla"}) by (pod) / 1024 / 1024
```

### Resource Usage

```bash
# Node-level CPU and memory
kubectl top nodes

# Pod-level CPU and memory
kubectl top pods -n settla --sort-by=cpu

# Detailed pod resource usage
kubectl top pods -n settla --containers
```

### NATS JetStream

```bash
# Exec into NATS pod
NATS_POD=$(kubectl get pods -n settla -l app.kubernetes.io/name=nats -o jsonpath='{.items[0].metadata.name}')

# Health check
kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/healthz

# JetStream info
kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/jsz

# Connections
kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/connz

# Subscriptions
kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/subsz
```

### PgBouncer

```bash
# Check PgBouncer stats (connect to admin console)
PGBOUNCER_POD=$(kubectl get pods -n settla -l app.kubernetes.io/name=pgbouncer-transfer -o jsonpath='{.items[0].metadata.name}')

# Show pools (active, waiting, idle connections)
kubectl exec -n settla $PGBOUNCER_POD -- psql -p 6432 -U pgbouncer pgbouncer -c "SHOW POOLS;"

# Show stats
kubectl exec -n settla $PGBOUNCER_POD -- psql -p 6432 -U pgbouncer pgbouncer -c "SHOW STATS;"

# Show clients
kubectl exec -n settla $PGBOUNCER_POD -- psql -p 6432 -U pgbouncer pgbouncer -c "SHOW CLIENTS;"
```

---

## 3. Scaling

### Scale Application Replicas

HPAs are disabled in the homelab overlay. Scale manually:

```bash
# Scale settla-server (e.g., from 3 to 4)
kubectl scale deployment/settla-server -n settla --replicas=4

# Scale settla-node (keep in sync with server count; update SETTLA_NODE_PARTITIONS)
kubectl scale statefulset/settla-node -n settla --replicas=4

# Scale gateway
kubectl scale deployment/gateway -n settla --replicas=2
```

**Important:** When changing settla-node replicas, also update `SETTLA_NODE_PARTITIONS` in the ConfigMap to maintain the 2-partitions-per-replica ratio:

| Replicas | Partitions | Ratio |
|----------|------------|-------|
| 2 | 4 | 2 per node |
| 3 | 6 | 2 per node |
| 4 | 8 | 2 per node |

```bash
# Edit the ConfigMap
kubectl edit configmap settla-node-config -n settla
# Change SETTLA_NODE_PARTITIONS to the new value

# Restart to pick up the change
kubectl rollout restart statefulset/settla-node -n settla
```

### Resource Budget When Scaling

Before scaling up, check available resources:

```bash
kubectl top nodes
```

| Config | CPU Req | Mem Req | Fits 2 nodes? | Fits 3 nodes? |
|--------|---------|---------|---------------|---------------|
| 3 server + 3 node (default) | 10.1 | 13.8 Gi | Yes (5.9 free) | Yes (13.9 free) |
| 4 server + 4 node | 12.6 | 17.3 Gi | Tight (3.4 free) | Yes (11.4 free) |
| 5 server + 5 node | 15.1 | 20.8 Gi | No (0.9 free) | Yes (8.9 free) |
| 6 server + 6 node | 17.6 | 24.4 Gi | No | Yes (6.4 free) |

**Do not exceed 4 server + 4 node on 2 nodes.** Wait for optiplex-3 to come back online before scaling beyond that.

### Scale for Peak (5,000 TPS)

With all 3 nodes online:

```bash
# Scale up
kubectl scale deployment/settla-server -n settla --replicas=5
kubectl scale statefulset/settla-node -n settla --replicas=5
kubectl scale deployment/gateway -n settla --replicas=2

# Update node partitions
kubectl patch configmap settla-node-config -n settla \
  --type merge -p '{"data":{"SETTLA_NODE_PARTITIONS":"10"}}'
kubectl rollout restart statefulset/settla-node -n settla

# Run peak test
make bench-peak GATEWAY_URL=http://<node-ip>:30080

# Scale back down after test
kubectl scale deployment/settla-server -n settla --replicas=3
kubectl scale statefulset/settla-node -n settla --replicas=3
kubectl scale deployment/gateway -n settla --replicas=1
kubectl patch configmap settla-node-config -n settla \
  --type merge -p '{"data":{"SETTLA_NODE_PARTITIONS":"6"}}'
kubectl rollout restart statefulset/settla-node -n settla
```

---

## 4. Load Testing

### Prerequisites

- External data plane running (MacBook Postgres + TigerBeetle)
- Cluster deployed and validated (`make k8s-homelab-validate`)
- Test data seeded (`make seed`)
- Load test tenant limits set high enough (see memory: load_test_config.md)

### Gateway URL

All load tests target the gateway NodePort:

```bash
export GATEWAY_URL=http://<any-node-ip>:30080
```

### Test Scenarios

| Scenario | Command | TPS | Duration | Purpose |
|----------|---------|-----|----------|---------|
| Smoke | `make bench-smoke GATEWAY_URL=$GATEWAY_URL` | 10 | 60s | Sanity check |
| Sustained (demo) | `make bench-sustained GATEWAY_URL=$GATEWAY_URL` | 580 | 10 min | Primary demo scenario |
| Daily simulation | `make loadtest-daily GATEWAY_URL=$GATEWAY_URL` | 580 | 1 hour | Long-running stability |
| Soak | `make soak GATEWAY_URL=$GATEWAY_URL` | 1000 | 2 hours | Resource leak detection |
| Peak burst | `make bench-peak GATEWAY_URL=$GATEWAY_URL` | 5000 | 5 min | Burst capacity proof |

### Recommended Test Sequence

1. **Smoke test** first to verify connectivity
2. **Sustained test** (580 TPS / 10 min) for the demo
3. Monitor during test:
   - `kubectl top pods -n settla` in another terminal
   - Grafana dashboards at `http://<node-ip>:30030`
4. After test, check logs for errors:
   ```bash
   kubectl logs -n settla -l app.kubernetes.io/name=settla-server --tail=50 | grep -i error
   kubectl logs -n settla -l app.kubernetes.io/name=settla-node --tail=50 | grep -i error
   ```

### Validation with Load Test

```bash
# Run validation + automatic Scenario B load test
./scripts/homelab-validate.sh --load-test
```

### Purge NATS Between Test Runs

NATS JetStream retains messages. Purge between test runs to avoid stale consumer offsets:

```bash
NATS_POD=$(kubectl get pods -n settla -l app.kubernetes.io/name=nats -o jsonpath='{.items[0].metadata.name}')

# Delete and recreate the NATS pod (quickest way to reset JetStream)
kubectl delete pod -n settla $NATS_POD
# Wait for it to come back
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=nats -n settla --timeout=60s
```

Or for a surgical purge, exec into the pod and use the NATS CLI (if available).

---

## 5. Troubleshooting

### Pod Won't Start

```bash
# Check pod events
kubectl describe pod <pod-name> -n settla

# Common causes:
# - "Insufficient cpu" → cluster is out of CPU requests. Scale down or wait for node-3.
# - "Insufficient memory" → same, check kubectl top nodes.
# - "ImagePullBackOff" → image not available. Check image name in deployment.
# - "CrashLoopBackOff" → app is crashing. Check logs.
```

### OOMKilled

```bash
# Find OOMKilled containers
kubectl get pods -n settla -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.containerStatuses[*]}{.lastState.terminated.reason}{"\n"}{end}{end}' | grep OOMKilled

# Fix: increase memory limit in patches/homelab-resources.yaml
# Then redeploy:
make k8s-homelab-deploy
```

### CrashLoopBackOff

```bash
# Check what's crashing
kubectl logs -n settla <pod-name> --previous

# Common causes:
# 1. Cannot connect to Postgres → Check MacBook is running, PgBouncer upstream IPs correct
# 2. Cannot connect to NATS → Check NATS pod is healthy
# 3. Cannot connect to TigerBeetle → Check MacBook TigerBeetle is running
# 4. Invalid config → Check ConfigMap values (kubectl get configmap settla-server-config -n settla -o yaml)
```

### PgBouncer Cannot Reach External Postgres

```bash
# Test from PgBouncer pod
PGBOUNCER_POD=$(kubectl get pods -n settla -l app.kubernetes.io/name=pgbouncer-transfer -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n settla $PGBOUNCER_POD -- sh -c "nc -z -w3 <macbook-ip> 5434 && echo OK || echo FAIL"

# If FAIL:
# 1. Check MacBook Postgres is running and listening on 0.0.0.0 (not just localhost)
# 2. Check MacBook firewall allows connections from LAN
# 3. Check pg_hba.conf allows connections from OptiPlex IP range
# 4. Verify the IP in .env.homelab matches MacBook's actual LAN IP
```

### NATS Not Healthy

```bash
NATS_POD=$(kubectl get pods -n settla -l app.kubernetes.io/name=nats -o jsonpath='{.items[0].metadata.name}')

# Check health
kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/healthz

# Check JetStream storage usage
kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/jsz

# If JetStream storage is full, the PVC may need expanding or old streams need purging
kubectl exec -n settla $NATS_POD -- ls -lah /data/jetstream/

# Nuclear option: delete the PVC and recreate (loses all messages)
kubectl delete statefulset nats -n settla
kubectl delete pvc data-nats-0 -n settla
make k8s-homelab-deploy
```

### High Latency During Load Test

Check in order:

1. **PgBouncer pool saturation:**
   ```bash
   # If server_active == DEFAULT_POOL_SIZE, the pool is saturated
   kubectl exec -n settla $(kubectl get pods -n settla -l app.kubernetes.io/name=pgbouncer-transfer -o jsonpath='{.items[0].metadata.name}') -- psql -p 6432 -U pgbouncer pgbouncer -c "SHOW POOLS;"
   ```
   Fix: increase `DEFAULT_POOL_SIZE` in `patches/pgbouncer-external-db.yaml` and redeploy.

2. **External Postgres slow queries:**
   Check the MacBook for CPU/IO saturation. PostgreSQL logs may show slow queries.

3. **NATS consumer lag:**
   ```bash
   kubectl exec -n settla $NATS_POD -- wget -qO- http://localhost:8222/jsz | python3 -m json.tool
   ```
   If consumers are lagging, scale up settla-node replicas.

4. **CPU throttling on cluster:**
   ```bash
   kubectl top pods -n settla --sort-by=cpu
   ```
   If pods are hitting CPU limits, scale up replicas or increase limits.

### Pods Stuck in Pending

```bash
kubectl describe pod <pending-pod> -n settla | grep -A5 "Events:"

# "Insufficient cpu" → too many requests for available nodes
# Fix: scale down other workloads or bring optiplex-3 online

# "no nodes available" → taint/toleration issue or all nodes NotReady
kubectl get nodes
```

### Gateway Returns 503

```bash
# Check gateway logs
kubectl logs -n settla -l app.kubernetes.io/name=gateway --tail=20

# Usually means settla-server is not ready
kubectl get pods -n settla -l app.kubernetes.io/name=settla-server

# Check settla-server readiness
kubectl describe pod -n settla -l app.kubernetes.io/name=settla-server | grep -A5 "Readiness:"
```

---

## 6. Node Management

### Bringing optiplex-3 Back Online

After replacing the SSD:

```bash
# 1. Install k3s agent
curl -sfL https://get.k3s.io | K3S_URL=https://<optiplex-2-ip>:6443 \
  K3S_TOKEN=<token> sh -

# 2. Verify node joined
kubectl get nodes

# 3. Label the node
kubectl label node optiplex-3 settla.io/role=mixed settla.io/storage=1ti --overwrite

# 4. Pods will automatically spread (soft anti-affinity)
# Monitor the spread:
kubectl get pods -n settla -o wide
```

No redeploy needed. k3s will automatically schedule new pods on the third node as existing pods get rescheduled.

### Draining a Node for Maintenance

```bash
# Cordon (prevent new pods)
kubectl cordon optiplex-1

# Drain (evict existing pods, respects PDBs)
kubectl drain optiplex-1 --ignore-daemonsets --delete-emptydir-data

# ... perform maintenance ...

# Uncordon
kubectl uncordon optiplex-1
```

With PDB minAvailable=0, all pods can be evicted. They will be rescheduled on remaining nodes.

### Node Is NotReady

```bash
# Check node status
kubectl describe node <node-name> | grep -A20 "Conditions:"

# Common causes:
# - k3s agent stopped → SSH to node, run: sudo systemctl status k3s-agent
# - Disk pressure → SSH to node, check df -h
# - Memory pressure → SSH to node, check free -h
# - Network issue → ping from another node

# Restart k3s on the node:
sudo systemctl restart k3s-agent   # for agents
sudo systemctl restart k3s         # for server (optiplex-2)
```

---

## 7. Secret Rotation

### Rotate a Secret

```bash
# 1. Decrypt secrets
make k8s-homelab-secrets-decrypt

# 2. Edit deploy/k8s/overlays/homelab/secrets.yaml
# Change the relevant password/key values

# 3. Re-encrypt
make k8s-homelab-secrets-encrypt

# 4. Apply
make k8s-homelab-deploy

# 5. Restart affected pods to pick up new secrets
kubectl rollout restart deployment/settla-server -n settla
kubectl rollout restart statefulset/settla-node -n settla
kubectl rollout restart deployment/gateway -n settla
kubectl rollout restart deployment/webhook -n settla
```

### Rotate PostgreSQL Password

If you change a Postgres password on a MacBook, you must also update:

1. `secrets.yaml` — `settla-db-credentials.{transfer,ledger,treasury}-password` and `patroni-credentials.{transfer,ledger,treasury}-password`
2. Redeploy and restart PgBouncer + all app pods:

```bash
make k8s-homelab-deploy
kubectl rollout restart deployment -n settla -l app.kubernetes.io/component=connection-pool
kubectl rollout restart deployment/settla-server -n settla
kubectl rollout restart statefulset/settla-node -n settla
kubectl rollout restart deployment/webhook -n settla
```

### Rotate SOPS Age Key

```bash
# 1. Generate new age key
age-keygen -o ~/.config/sops/age/keys-new.txt

# 2. Decrypt with old key
SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt sops -d -i deploy/k8s/overlays/homelab/secrets.yaml

# 3. Update .sops.yaml with new public key

# 4. Re-encrypt with new key
SOPS_AGE_KEY_FILE=~/.config/sops/age/keys-new.txt sops -e -i deploy/k8s/overlays/homelab/secrets.yaml

# 5. Replace old key file
mv ~/.config/sops/age/keys-new.txt ~/.config/sops/age/keys.txt
```

---

## 8. Backup and Recovery

### What to Back Up

The homelab cluster is primarily stateless. Critical state lives on the MacBook:

| Data | Location | Backup Method |
|------|----------|---------------|
| PostgreSQL (3 DBs) | MacBook | `pg_dump` or WAL archiving |
| TigerBeetle | MacBook | TigerBeetle snapshot |
| NATS JetStream | k3s PVC (optiplex-2) | Ephemeral for demo — no backup needed |
| Redis | k3s PVC | Ephemeral cache — no backup needed |
| Prometheus metrics | k3s emptyDir | Lost on pod restart — OK for demo |
| Secrets | Git repo (SOPS-encrypted) | Git |
| Cluster config | Git repo (kustomize) | Git |

### PostgreSQL Backup (MacBook)

```bash
# Dump all three databases
pg_dump -h localhost -p 5433 -U settla settla_ledger > backup_ledger_$(date +%Y%m%d).sql
pg_dump -h localhost -p 5434 -U settla settla_transfer > backup_transfer_$(date +%Y%m%d).sql
pg_dump -h localhost -p 5435 -U settla settla_treasury > backup_treasury_$(date +%Y%m%d).sql
```

### PostgreSQL Restore

```bash
psql -h localhost -p 5433 -U settla settla_ledger < backup_ledger_YYYYMMDD.sql
psql -h localhost -p 5434 -U settla settla_transfer < backup_transfer_YYYYMMDD.sql
psql -h localhost -p 5435 -U settla settla_treasury < backup_treasury_YYYYMMDD.sql
```

### Full Cluster Recovery

If you need to rebuild from scratch:

```bash
# 1. Ensure MacBook data plane is running
# 2. Ensure k3s cluster is up
kubectl get nodes

# 3. Run migrations
make migrate-up

# 4. Seed data
make seed

# 5. Deploy
make k8s-homelab-deploy

# 6. Validate
make k8s-homelab-validate
```

---

## 9. Teardown and Redeployment

### Remove All Settla Workloads

```bash
# Delete all resources in the settla namespace
kubectl delete namespace settla

# This removes all pods, services, PVCs, configmaps, secrets, etc.
# The namespace will be recreated on next deploy.
```

### Remove Only Application Pods (Keep Infrastructure)

```bash
kubectl delete deployment settla-server gateway webhook dashboard alertmanager -n settla
kubectl delete statefulset settla-node -n settla
```

### Full Redeploy

```bash
kubectl delete namespace settla
# Wait for namespace termination
kubectl wait --for=delete namespace/settla --timeout=120s 2>/dev/null || true
make k8s-homelab-deploy
make k8s-homelab-validate
```

### Reset NATS JetStream

Between unrelated test runs, purge NATS to clear stale consumer state:

```bash
kubectl delete statefulset nats -n settla
kubectl delete pvc data-nats-0 -n settla
make k8s-homelab-deploy
```

---

## Quick Reference Card

```
# Deploy
make k8s-homelab-deploy

# Status
make k8s-homelab-status

# Validate
make k8s-homelab-validate

# Logs
kubectl logs -n settla -l app.kubernetes.io/name=settla-server -f --tail=50

# Scale
kubectl scale deployment/settla-server -n settla --replicas=<N>

# Restart
kubectl rollout restart deployment/settla-server -n settla

# Load test (580 TPS / 10 min)
make bench-sustained GATEWAY_URL=http://<node-ip>:30080

# Secret edit
make k8s-homelab-secrets-decrypt
# edit secrets.yaml
make k8s-homelab-secrets-encrypt
make k8s-homelab-deploy

# Grafana
open http://<node-ip>:30030

# Node trouble
kubectl describe node <name>
kubectl top nodes
```
