# Settla Homelab Deployment Guide

Deploys the Settla payment infrastructure on a **3-node Dell OptiPlex k3s cluster** (96 GB RAM, 24 CPU cores, 4 TB storage) for sustained 580 TPS demo throughput with 5,000 TPS peak burst capacity.

## Architecture Overview

```
                        Gigabit LAN
    ┌───────────────────────────────────────────────────────────┐
    │                                                           │
    │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
    │  │ optiplex-1   │  │ optiplex-2   │  │ optiplex-3   │      │
    │  │ 8 CPU / 32GB │  │ 8 CPU / 32GB │  │ 8 CPU / 32GB │      │
    │  │ 1TB SSD      │  │ 2TB SSD      │  │ 1TB SSD      │      │
    │  │              │  │              │  │ (offline)    │      │
    │  │ k3s agent    │  │ k3s server   │  │ k3s agent    │      │
    │  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘      │
    │         │                 │                 │              │
    │         └────────┬────────┘                 │              │
    │                  │ ◄── k3s cluster ──►      │              │
    │                  │                          │              │
    │  ┌───────────────┴──────────────────────────┘              │
    │  │  Workloads (on-cluster):                               │
    │  │    settla-server x3  settla-node x3  gateway x1        │
    │  │    webhook x1  NATS x1  Redis x1  PgBouncer x3        │
    │  │    Prometheus x1  Grafana x1  Dashboard x1             │
    │  └───────────────┬──────────────────────────┘              │
    │                  │                                        │
    │                  │ PgBouncer → external Postgres           │
    │                  │ App → external TigerBeetle              │
    │                  ▼                                        │
    │  ┌─────────────────────────────┐                          │
    │  │ MacBook (data plane)        │                          │
    │  │ PostgreSQL x3 (5433-5435)   │                          │
    │  │ TigerBeetle (3001)          │                          │
    │  └─────────────────────────────┘                          │
    │                                                           │
    │  ┌─────────────────────────────┐                          │
    │  │ Mac (load generator)        │                          │
    │  │ → http://<node-ip>:30080    │                          │
    │  └─────────────────────────────┘                          │
    └───────────────────────────────────────────────────────────┘
```

**Compute tier** (k3s cluster): settla-server, settla-node, gateway, webhook, NATS, Redis, PgBouncer, monitoring.
**Data tier** (external MacBook): PostgreSQL (3 bounded-context databases), TigerBeetle (double-entry ledger engine).

The cluster handles all stateless compute; the data plane runs externally on a MacBook with fast NVMe storage. PgBouncer on the cluster proxies database connections to the external PostgreSQL over the LAN.

## Prerequisites

### Hardware

| Node | CPU | RAM | Storage | Role |
|------|-----|-----|---------|------|
| optiplex-1 | 8 cores | 32 GB | 1 TB SSD | k3s agent |
| optiplex-2 | 8 cores | 32 GB | 2 TB SSD | k3s server (control plane) |
| optiplex-3 | 8 cores | 32 GB | 1 TB SSD | k3s agent (currently offline) |
| MacBook | - | - | NVMe | PostgreSQL + TigerBeetle |

The cluster operates in **degraded mode** (2 nodes / 64 GB / 16 CPU) when optiplex-3 is offline. All manifests use soft anti-affinity so pods schedule on 2 or 3 nodes without failure.

### Software

Install on all k3s nodes:

| Tool | Version | Purpose |
|------|---------|---------|
| k3s | v1.29+ | Lightweight Kubernetes |
| kubectl | v1.29+ | Cluster management |

Install on your workstation:

| Tool | Version | Purpose |
|------|---------|---------|
| kubectl | v1.29+ | Cluster management |
| kustomize | v5.0+ | Manifest rendering (bundled with kubectl) |
| sops | v3.8+ | Secret encryption |
| age | v1.1+ | Encryption key management |
| envsubst | any | Environment variable substitution (`gettext` package) |

### k3s Cluster Setup

If k3s is not yet installed:

```bash
# On optiplex-2 (server):
curl -sfL https://get.k3s.io | sh -s - server \
  --disable traefik \
  --disable servicelb \
  --write-kubeconfig-mode 644

# Get the join token:
cat /var/lib/rancher/k3s/server/node-token

# On optiplex-1 and optiplex-3 (agents):
curl -sfL https://get.k3s.io | K3S_URL=https://<optiplex-2-ip>:6443 \
  K3S_TOKEN=<token> sh -
```

Copy the kubeconfig to your workstation:

```bash
scp optiplex-2:/etc/rancher/k3s/k3s.yaml ~/.kube/config
# Edit ~/.kube/config — change 127.0.0.1 to optiplex-2's LAN IP
```

### External Data Plane (MacBook)

PostgreSQL and TigerBeetle must be running on the MacBook before deploying. The cluster connects to them over the LAN.

**PostgreSQL** (3 instances on ports 5433, 5434, 5435):

```bash
# Using Docker Compose from the project:
docker compose -f deploy/docker-compose.yml up -d postgres-ledger postgres-transfer postgres-treasury
```

Or run standalone PostgreSQL instances with the tuning from `deploy/cluster/docker-compose.data.yml`.

**TigerBeetle** (port 3001):

```bash
docker compose -f deploy/docker-compose.yml up -d tigerbeetle-init tigerbeetle
```

Verify connectivity from any k3s node:

```bash
# From an OptiPlex node:
nc -z <macbook-ip> 5433 && echo "Ledger DB OK"
nc -z <macbook-ip> 5434 && echo "Transfer DB OK"
nc -z <macbook-ip> 5435 && echo "Treasury DB OK"
nc -z <macbook-ip> 3001 && echo "TigerBeetle OK"
```

---

## Deployment

### Step 1: Configure Environment

```bash
cp deploy/k8s/overlays/homelab/.env.homelab.example \
   deploy/k8s/overlays/homelab/.env.homelab
```

Edit `.env.homelab` with actual values:

```bash
MACBOOK_DATA_IP=192.168.1.100    # MacBook running Postgres + TigerBeetle
OPTIPLEX_1_IP=192.168.1.10
OPTIPLEX_2_IP=192.168.1.11
OPTIPLEX_3_IP=192.168.1.12
POSTGRES_TRANSFER_PASSWORD=settla # Must match MacBook 1 transfer DB password
POSTGRES_LEDGER_PASSWORD=settla   # Must match MacBook 2 ledger DB password
POSTGRES_TREASURY_PASSWORD=settla # Must match MacBook 2 treasury DB password
```

### Step 2: Configure Secrets

Edit the secrets file with real credentials:

```bash
# Decrypt if already encrypted:
make k8s-homelab-secrets-decrypt

# Edit deploy/k8s/overlays/homelab/secrets.yaml
# Set all CHANGE_ME values to real passwords/keys.
# For wallet keys, generate with: openssl rand -hex 32

# Encrypt before committing:
make k8s-homelab-secrets-encrypt
```

**First-time SOPS setup:**

```bash
# Generate age key pair:
age-keygen -o ~/.config/sops/age/keys.txt

# Copy the public key from output (age1xxxx...) into:
#   1. .sops.yaml (replace placeholder age key)
#   2. .env.homelab (SOPS_AGE_RECIPIENTS)

# Export for CLI:
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
```

### Step 3: Label Nodes

```bash
make k8s-homelab-label-nodes
```

This applies:
- `settla.io/role=mixed` on all nodes
- `settla.io/storage=2ti` on optiplex-2 (NATS JetStream PVC prefers this node)
- `settla.io/storage=1ti` on optiplex-1 and optiplex-3

### Step 4: Build the Migration Image

Migrations run automatically during deploy via a Kubernetes Job. Build the image once (and rebuild whenever SQL files change):

```bash
# Set K3S_SSH_HOSTS so the image is imported into every node's containerd
export K3S_SSH_HOSTS="optiplex-1 optiplex-2 optiplex-3"
make k8s-homelab-migrate-build
```

See [deploy/k8s/migrations/MIGRATIONS.md](../../migrations/MIGRATIONS.md) for the full migration pipeline architecture.

### Step 5: Seed Test Data

```bash
make seed
# Or for large-scale tenant tests:
# make bench-seed-20k
```

### Step 6: Deploy

```bash
make k8s-homelab-deploy
```

This:
1. Deletes any existing migration Job
2. Renders the Kustomize overlay and substitutes `${MACBOOK_1_IP}` / `${MACBOOK_2_IP}`
3. Applies the full manifest (migration Job + RBAC + apps)
4. Waits for the migration Job to complete (up to 10 minutes)
5. Prints migration logs
6. App pods start automatically — their `initContainers` block until the Job is `Complete`

### Step 7: Validate

```bash
make k8s-homelab-validate
```

Checks: node health, pod status, OOMKills, pod spread, service endpoints, PVC binding, NATS health, external data plane connectivity.

### Step 8: Access Services

| Service | URL | Purpose |
|---------|-----|---------|
| Gateway API | `http://<any-node-ip>:30080` | Load test target, API access |
| Grafana | `http://<any-node-ip>:30030` | Dashboards and monitoring |
| Prometheus | Port-forward: `kubectl port-forward -n settla svc/prometheus 9090:9090` | Metrics queries |

---

## Resource Budget

### Per-Workload Allocations

| Workload | Replicas | CPU Req/Lim | Mem Req/Lim |
|----------|----------|-------------|-------------|
| settla-server | 3 | 1.5/3.0 | 2Gi/4Gi |
| settla-node | 3 | 1.0/2.0 | 1.5Gi/3Gi |
| gateway | 1 | 0.5/1.5 | 512Mi/1Gi |
| webhook | 1 | 0.25/0.5 | 256Mi/512Mi |
| nats | 1 | 0.5/1.5 | 512Mi/1Gi |
| redis | 1 | 0.25/0.5 | 512Mi/1Gi |
| pgbouncer x3 | 1 each | 0.1/0.3 | 64Mi/256Mi |
| prometheus | 1 | 0.5/1.0 | 1Gi/2Gi |
| grafana | 1 | 0.1/0.5 | 128Mi/512Mi |
| dashboard | 1 | 0.1/0.25 | 128Mi/256Mi |
| alertmanager | 1 | 0.075/0.25 | 96Mi/192Mi |

### Cluster Totals

| | CPU Requests | CPU Limits | Mem Requests | Mem Limits | Pods |
|--|-------------|------------|-------------|------------|------|
| **Total** | 10.1 cores | 21.9 cores | 13.8 Gi | 28.2 Gi | 18 |
| **3-node capacity** | 24 cores | 24 cores | 96 GB | 96 GB | - |
| **Headroom (3-node)** | 13.9 (58%) | ~0 (burst OK) | 82.2 Gi (86%) | 67.8 Gi | - |
| **Headroom (2-node)** | 5.9 (37%) | tight | 50.2 Gi (78%) | 35.8 Gi | - |

CPU limits (21.9) exceed 2-node physical cores (16) but not 3-node (24). On 2 nodes, burst contention is possible under peak load but CPU requests (10.1) are well within budget. Actual sustained utilization at 580 TPS is ~10-12 cores.

---

## Configuration Reference

### Application Tuning (`patches/homelab-env.yaml`)

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| `SETTLA_NODE_PARTITIONS` | 6 | 2 per settla-node replica (3 replicas) |
| `SETTLA_WORKER_POOL_PROVIDER` | 24 | Tuned down from 32 for 3 replicas |
| `SETTLA_WORKER_POOL_WEBHOOK` | 24 | Tuned down from 32 for 3 replicas |
| `SETTLA_RELAY_BATCH_SIZE` | 500 | Matches cluster config |
| `SETTLA_RELAY_POLL_INTERVAL_MS` | 20 | Aggressive polling for low latency |
| `SETTLA_TREASURY_FLUSH_INTERVAL_MS` | 100 | Batch treasury writes every 100ms |
| `SETTLA_LEDGER_BATCH_WINDOW_MS` | 10 | Batch TigerBeetle writes every 10ms |
| `SETTLA_LEDGER_BATCH_MAX_SIZE` | 500 | Max entries per TigerBeetle batch |
| `SETTLA_MOCK_DELAY_MS` | 0 | No artificial delay for demo |
| `SETTLA_PROVIDER_MODE` | mock | Mock payment providers |
| `SETTLA_LOG_LEVEL` | info | Not debug (saves CPU), not warn (need visibility) |

### PgBouncer (`patches/pgbouncer-external-db.yaml`)

| Parameter | Value | Notes |
|-----------|-------|-------|
| Upstream hosts | `${MACBOOK_DATA_IP}:5433/5434/5435` | External MacBook Postgres |
| `POOL_MODE` | transaction | Connection released after each transaction |
| `MAX_CLIENT_CONN` | 2000 | Max concurrent client connections |
| `DEFAULT_POOL_SIZE` | 200 (ledger/transfer), 100 (treasury) | Backend connections to Postgres |
| `MIN_POOL_SIZE` | 50 (ledger/transfer), 25 (treasury) | Pre-warmed connections |
| `SERVER_IDLE_TIMEOUT` | 300s | Close idle backend connections after 5 min |
| `SERVER_LIFETIME` | 3600s | Recycle backend connections every hour |

### Redis (`patches/redis-homelab.yaml`)

| Parameter | Value |
|-----------|-------|
| Mode | Standalone (single replica) |
| `maxmemory` | 1 GB |
| `maxmemory-policy` | volatile-lru |
| Persistence | AOF, everysec fsync |
| PVC | 10 Gi (local-path) |

### NATS (`patches/nats-homelab.yaml`)

| Parameter | Value |
|-----------|-------|
| Mode | Single-node JetStream |
| PVC | 50 Gi (local-path, prefers 2TB node) |

---

## File Structure

```
deploy/k8s/overlays/homelab/
├── kustomization.yaml              # Main overlay definition
├── secrets.yaml                    # K8s Secrets (encrypt with SOPS before commit)
├── .env.homelab.example            # Template for environment variables
└── patches/
    ├── homelab-replicas.yaml       # Replica counts (3/3/1/1/1/1, readonly pgbouncers → 0)
    ├── homelab-resources.yaml      # CPU/memory requests and limits
    ├── homelab-env.yaml            # ConfigMap overrides (tuning, provider mode, DB URLs)
    ├── homelab-topology.yaml       # Soft anti-affinity for 2/3 node compatibility
    ├── disable-hpa.yaml            # Relax all PDBs to minAvailable=0
    ├── delete-hpa-server.yaml      # Remove settla-server HPA
    ├── delete-hpa-gateway.yaml     # Remove gateway HPA
    ├── enable-pprof.yaml           # pprof profiling port on settla-server
    ├── pgbouncer-external-db.yaml  # PgBouncer → external MacBook Postgres
    ├── redis-homelab.yaml          # Redis standalone, 1GB, tuned down
    ├── nats-homelab.yaml           # NATS single-node JetStream
    ├── storage-local-path.yaml     # All PVCs use k3s local-path provisioner
    └── gateway-nodeport.yaml       # Gateway exposed on NodePort 30080
```

---

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make k8s-homelab-deploy` | Render, substitute env vars, apply to cluster |
| `make k8s-homelab-template` | Dry-run: render manifests to stdout |
| `make k8s-homelab-validate` | Run post-deploy health checks |
| `make k8s-homelab-status` | Show nodes, pods, resource usage |
| `make k8s-homelab-label-nodes` | Apply scheduling labels to nodes |
| `make k8s-homelab-secrets-encrypt` | Encrypt secrets.yaml with SOPS + age |
| `make k8s-homelab-secrets-decrypt` | Decrypt secrets.yaml for editing |

---

## Differences from Other Overlays

| Feature | Development | Staging | Production | **Homelab** |
|---------|-------------|---------|------------|-------------|
| Namespace | settla-dev | settla-staging | settla | **settla** |
| settla-server replicas | 1 | 2 | 6 (HPA 6-12) | **3** |
| settla-node replicas | 1 | 4 | 8 | **3** |
| gateway replicas | 1 | 2 | 4 (HPA 2-8) | **1** |
| PostgreSQL | In-cluster | In-cluster | Patroni 3-node HA | **External MacBook** |
| TigerBeetle | In-cluster | In-cluster | 3-node Raft | **External MacBook** |
| Redis | In-cluster standalone | In-cluster | Sentinel 3+3 | **In-cluster standalone** |
| NATS | In-cluster single | In-cluster | 3-node JetStream | **In-cluster single** |
| HPA | Disabled | Moderate | Aggressive | **Disabled** |
| PDB minAvailable | 0 | 1 | 4-6 | **0** |
| Anti-affinity | None | None | Hard (zone + host) | **Soft (host only)** |
| Secrets | Plain K8s Secrets | ESO | ESO + AWS SM | **Plain + SOPS/age** |
| Provider mode | mock (100ms delay) | testnet | live | **mock (0ms delay)** |
| Log level | debug | info | warn | **info** |
| Storage class | standard | standard | fast-ssd / standard | **local-path** |
| Target TPS | N/A | N/A | 580 sustained / 5K peak | **580 sustained** |
