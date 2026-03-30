# Chapter 8.5: Deployment

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the complete infrastructure topology for a 50M transactions/day settlement system
2. Read Kubernetes manifests for settla-server, settla-node, and the gateway
3. Understand replica count decisions and why settla-node uses StatefulSet
4. Configure PgBouncer connection pooling for three bounded-context databases
5. Design zero-downtime rolling updates with proper probe configuration

---

## Infrastructure Overview

Settla's production deployment spans 20+ services across four categories:

```
                            +-------------------+
                            |   Tyk API Gateway |
                            |   (rate limit,    |
                            |    API key mgmt)  |
                            +--------+----------+
                                     |
                    +----------------+----------------+
                    |                                  |
            +-------+--------+              +---------+--------+
            |  gateway (4x)  |              |  webhook (2x)    |
            |  Fastify REST  |              |  Inbound provider|
            |  :3000         |              |  callbacks :3001 |
            +-------+--------+              +---------+--------+
                    |                                  |
                    |  gRPC (connection pool ~50)      |  NATS publish
                    |                                  |
            +-------+--------+                         |
            | settla-server  |                         |
            |  (6x replicas) |                         |
            |  :8080 :9090   |                         |
            +-------+--------+                         |
                    |                                  |
     +--------------+---+---+---+---+---+              |
     |              |       |       |   |              |
+----+---+  +------+--+ +--+--+ +--+--+|     +--------+------+
|Transfer|  | Ledger  | |Treas| |Tiger||     | settla-node   |
|DB      |  | DB      | |DB   | |Btl  ||     | (8x replicas) |
|PgB:6434|  | PgB:6433| |:6435| |:3001||     | Outbox relay  |
+--------+  +---------+ +-----+ +-----+|     | + 7 workers   |
                                        |     +--------+------+
                                        |              |
                                 +------+------+  +----+----+
                                 |    NATS     |  |  Redis  |
                                 |  JetStream  |  | (cache) |
                                 |    :4222    |  |  :6379  |
                                 +-------------+  +---------+
```

---

## Local Development: Docker Compose

For local development, all services run via Docker Compose:

```bash
cp .env.example .env   # Create local env file
make docker-up         # Builds Go/TS containers + starts all infra
```

Infrastructure ports:

```
+------------------+------+------------------------------------------+
| Service          | Port | Purpose                                  |
+------------------+------+------------------------------------------+
| TigerBeetle      | 3001 | Ledger write authority (1M+ TPS)        |
| PgBouncer Ledger  | 6433 | Connection pool for ledger reads        |
| PgBouncer Transfer| 6434 | Connection pool for transfer DB         |
| PgBouncer Treasury| 6435 | Connection pool for treasury DB         |
| Postgres Ledger   | 5433 | Raw connection (migrations only)        |
| Postgres Transfer | 5434 | Raw connection (migrations only)        |
| Postgres Treasury | 5435 | Raw connection (migrations only)        |
| Redis            | 6379 | L2 cache, rate limiting, idempotency     |
| NATS             | 4222 | Client connections                       |
| NATS Monitor     | 8222 | HTTP monitoring API                      |
+------------------+------+------------------------------------------+

Application ports:
| settla-server    | 8080 | HTTP (health, metrics)                   |
| settla-server    | 9090 | gRPC (SettlementService)                 |
| settla-server    | 6060 | pprof (profiling)                        |
| gateway          | 3000 | REST API                                 |
| webhook          | 3001 | Inbound provider webhooks                |
+------------------+------+------------------------------------------+
```

### Docker Compose Commands

```bash
make docker-up          # Build and start all services
make docker-down        # Stop all services
make docker-logs        # Tail logs from all services
make docker-reset       # Clean slate: down -v + rebuild
make migrate-up         # Run DB migrations (uses raw PG ports)
make db-seed            # Load seed data into all databases
```

---

## Kubernetes Manifests

Production deployment uses Kubernetes with Kustomize overlays:

```
deploy/k8s/
  base/                          # Shared manifests
    settla-server/
      deployment.yaml            # 6 replicas, Deployment
      service.yaml
      configmap.yaml
      pdb.yaml                   # PodDisruptionBudget
      rollout.yaml               # Argo Rollouts canary
    settla-node/
      statefulset.yaml           # 8 replicas, StatefulSet
      service.yaml
      configmap.yaml
    gateway/
      deployment.yaml            # 4 replicas, Deployment
      service.yaml
      configmap.yaml
    webhook/
      deployment.yaml            # 2 replicas
    dashboard/
      deployment.yaml
    alertmanager/
    ingress/
    network-policies/
    secrets/
    backups/
    maintenance/
  infrastructure/
    tigerbeetle/statefulset.yaml
    postgres/statefulset.yaml
    pgbouncer/deployment.yaml
    nats/statefulset.yaml
    redis/statefulset.yaml
    redis-sentinel/
    patroni/
    prometheus/
    grafana/
  overlays/
    development/                 # Reduced replicas, relaxed resources
    staging/                     # Moderate replicas, pprof enabled
    production/                  # Full replicas, topology spread
```

### settla-server Deployment (6 replicas)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: settla-server
spec:
  replicas: 6
  strategy:
    rollingUpdate:
      maxSurge: 2
      maxUnavailable: 1
    type: RollingUpdate
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
    spec:
      terminationGracePeriodSeconds: 30
      containers:
        - name: settla-server
          image: settla-server:latest
          ports:
            - containerPort: 9090
              name: grpc
            - containerPort: 8080
              name: http
          resources:
            requests:
              cpu: "2000m"
              memory: "4Gi"
            limits:
              cpu: "4000m"
              memory: "8Gi"
          readinessProbe:
            grpc:
              port: 9090
            initialDelaySeconds: 15
            periodSeconds: 10
          livenessProbe:
            grpc:
              port: 9090
            initialDelaySeconds: 30
            periodSeconds: 15
          lifecycle:
            preStop:
              exec:
                # Drain connections + treasury flush
                command: ["/bin/sh", "-c", "sleep 15"]
          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: settla-db-credentials
                  key: app-password
```

**Why 6 replicas?** At 5,000 TPS peak with each server handling ~1,000 TPS
(limited by DB connection pool), 5 replicas handle peak load. The 6th provides
headroom for rolling updates (maxUnavailable: 1 means 5 are always available).

**Why preStop sleep 15?** When a pod is terminated, Kubernetes removes it from
the Service endpoints. But kube-proxy takes a few seconds to update iptables/IPVS.
The 15-second sleep ensures in-flight requests complete and the treasury flush
goroutine writes pending state to Postgres before the process exits.

### settla-node StatefulSet (8 replicas)

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: settla-node
spec:
  replicas: 8
  serviceName: settla-node
  podManagementPolicy: Parallel  # All pods start simultaneously
  template:
    spec:
      containers:
        - name: settla-node
          resources:
            requests:
              cpu: "1000m"
              memory: "2Gi"
            limits:
              cpu: "2000m"
              memory: "4Gi"
          readinessProbe:
            httpGet:
              path: /health
              port: 9091
            initialDelaySeconds: 5
            periodSeconds: 5
          env:
            # Pod name: settla-node-0, settla-node-1, ... settla-node-7
            # Application parses ordinal index for partition assignment
            - name: SETTLA_NODE_PARTITION
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
```

**Why StatefulSet instead of Deployment?** NATS JetStream uses 8 partitions
for the SETTLA_TRANSFERS stream. Each settla-node instance must consume a
specific partition to maintain per-tenant ordering. StatefulSet provides
stable pod names (settla-node-0 through settla-node-7), which the application
uses to determine partition assignment:

```
settla-node-0 --> partition 0
settla-node-1 --> partition 1
...
settla-node-7 --> partition 7
```

**Why `podManagementPolicy: Parallel`?** During recovery (e.g., after a cluster
restart), all 8 nodes must start simultaneously to begin consuming from all
partitions. The default `OrderedReady` policy would start them sequentially,
leaving partitions 1-7 unprocessed while partition 0's pod completes readiness.

### Gateway Deployment (4 replicas)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gateway
spec:
  replicas: 4
  strategy:
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0  # Zero-downtime: new pod must be ready before old is killed
  template:
    spec:
      containers:
        - name: gateway
          resources:
            requests:
              cpu: "1000m"
              memory: "2Gi"
          # Startup probe gates readiness/liveness until Fastify is initialized
          startupProbe:
            httpGet:
              path: /health
              port: 3000
            periodSeconds: 2
            failureThreshold: 15   # 30s max startup time
          # Readiness: fast removal (6s) from load balancer
          readinessProbe:
            httpGet:
              path: /health
              port: 3000
            periodSeconds: 3
            failureThreshold: 2    # 2 * 3s = 6s to remove bad pod
          # Liveness: conservative restart (30s)
          livenessProbe:
            httpGet:
              path: /health
              port: 3000
            periodSeconds: 10
            failureThreshold: 3    # 3 * 10s = 30s before restart
          lifecycle:
            preStop:
              exec:
                # Wait for kube-proxy to deregister pod from iptables
                command: ["/bin/sh", "-c", "sleep 5"]
```

**Key Insight: Three-tier probe strategy.** The gateway uses startup, readiness,
and liveness probes with different sensitivities:

```
startupProbe:   "Is the process initialized?"     30s tolerance
                (gRPC pool warmed, cache loaded)

readinessProbe: "Can this pod handle traffic?"     6s to remove
                (aggressive -- every second counts
                at 5,000 TPS)

livenessProbe:  "Is the process deadlocked?"       30s before restart
                (conservative -- avoid restart storms
                during Redis Sentinel failover)
```

**Why maxUnavailable: 0?** Zero-downtime rolling updates. A new pod must pass
its startup and readiness probes before the old pod is terminated. This
guarantees that at least 4 healthy pods are always serving traffic.

---

## PgBouncer Configuration

Each bounded-context database has its own PgBouncer instance:

```
+-------------------+      +-------------------+      +-------------------+
| PgBouncer :6433   |      | PgBouncer :6434   |      | PgBouncer :6435   |
| Ledger DB pool    |      | Transfer DB pool  |      | Treasury DB pool  |
| pool_size: 50     |      | pool_size: 50     |      | pool_size: 30     |
+--------+----------+      +--------+----------+      +--------+----------+
         |                          |                          |
+--------+----------+      +--------+----------+      +--------+----------+
| Postgres :5433    |      | Postgres :5434    |      | Postgres :5435    |
| settla_ledger     |      | settla_transfer   |      | settla_treasury   |
+-------------------+      +-------------------+      +-------------------+
```

**Why separate PgBouncer per database?** Connection limits must be tuned per
workload:

- **Transfer DB (pool_size: 50):** Highest throughput -- 50M transfers/day
  means high write and read concurrency
- **Ledger DB (pool_size: 50):** TB-to-PG sync writes + API reads
- **Treasury DB (pool_size: 30):** Lower throughput -- only flush writes
  (100ms interval) and position reads

With 6 settla-server replicas each holding ~8 connections per pool, total
connections per DB: 6 * 8 = 48, well within the pool_size of 50.

**Database TLS:** Production uses `sslmode=verify-ca` (or `verify-full`) for all database connections. The server rejects `sslmode=disable` at startup in production. Development defaults to `sslmode=prefer`.

---

## NATS JetStream Cluster

NATS runs as a 3-node cluster for high availability. Authentication is required in production via `SETTLA_NATS_TOKEN` (token auth) or `SETTLA_NATS_USER`/`SETTLA_NATS_PASSWORD` (user/password auth). Both `settla-server` and `settla-node` warn at startup if no NATS auth is configured in production.

```yaml
# deploy/k8s/infrastructure/nats/statefulset.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: nats
spec:
  replicas: 3
  serviceName: nats
```

Twelve streams with WorkQueue retention (each message delivered to exactly one
consumer):

```
+-------------------------+-----------------------------------------+----------+
| Stream                  | Subject Pattern                         | Max Age  |
+-------------------------+-----------------------------------------+----------+
| SETTLA_TRANSFERS        | settla.transfer.partition.*.>           | 7 days   |
| SETTLA_PROVIDERS        | settla.provider.command.partition.*.>   | 7 days   |
| SETTLA_LEDGER           | settla.ledger.partition.*.>             | 7 days   |
| SETTLA_TREASURY         | settla.treasury.partition.*.>           | 7 days   |
| SETTLA_BLOCKCHAIN       | settla.blockchain.partition.*.>        | 7 days   |
| SETTLA_WEBHOOKS         | settla.webhook.partition.*.>           | 7 days   |
| SETTLA_PROVIDER_WEBHOOKS| settla.provider.inbound.partition.*.>  | 7 days   |
| SETTLA_CRYPTO_DEPOSITS  | settla.deposit.partition.*.>           | 7 days   |
| SETTLA_BANK_DEPOSITS    | settla.bank_deposit.partition.*.>      | 7 days   |
| SETTLA_EMAILS           | settla.email.partition.*.>             | 7 days   |
| SETTLA_POSITION_EVENTS  | settla.position.event.>                | 7 days   |
| SETTLA_DLQ              | settla.dlq.>                           | 7 days   |
+-------------------------+-----------------------------------------+----------+
Dedup window: 5 minutes
```

---

## Replica Count Summary

```
+------------------+----------+---------+-------------------------------+
| Service          | Replicas | Type    | Rationale                     |
+------------------+----------+---------+-------------------------------+
| settla-server    | 6        | Deploy  | 5 for peak + 1 for updates   |
| settla-node      | 8        | SS      | 1:1 with NATS partitions     |
| gateway          | 4        | Deploy  | 5K TPS / ~1.5K TPS per pod   |
| webhook          | 2        | Deploy  | Lower throughput              |
| dashboard        | 1        | Deploy  | Internal ops console          |
| TigerBeetle      | 1        | SS      | Single-node (TB replication   |
|                  |          |         | is a separate concern)        |
| Postgres (each)  | 2        | SS      | Primary + 1 replica (Patroni) |
| PgBouncer (each) | 2        | Deploy  | HA pair per DB                |
| NATS             | 3        | SS      | Quorum cluster                |
| Redis            | 3        | SS      | Primary + 2 (Sentinel HA)     |
| Prometheus       | 1        | Deploy  | Singleton with persistent vol |
| Grafana          | 1        | Deploy  | Stateless (config in CM)      |
+------------------+----------+---------+-------------------------------+
```

---

## Environment Overlays

### Development (reduced resources)

```yaml
# deploy/k8s/overlays/development/patches/reduce-replicas.yaml
- op: replace
  path: /spec/replicas
  value: 1  # Single replica for all services
```

### Staging (moderate, pprof enabled)

```yaml
# deploy/k8s/overlays/staging/patches/staging-replicas.yaml
- settla-server: 2 replicas
- settla-node: 2 replicas
- gateway: 2 replicas
```

### Production (full, topology spread)

```yaml
# deploy/k8s/overlays/production/patches/production-topology.yaml
# Spread pods across availability zones
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: DoNotSchedule
```

---

## Network Policies

Settla uses Kubernetes NetworkPolicies to enforce module boundaries at the
network level:

```yaml
# Only gateway can reach settla-server's gRPC port
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: settla-server
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: settla-server
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: gateway
      ports:
        - port: 9090   # gRPC
        - port: 8080   # health/metrics
```

---

## Backup Strategy

```
+------------------+------------+----------+---------------------------+
| System           | Frequency  | Retention| Method                    |
+------------------+------------+----------+---------------------------+
| Postgres (all 3) | Hourly     | 30 days  | pg_dump to S3             |
| TigerBeetle      | Daily      | 90 days  | Data file snapshot to S3  |
| NATS JetStream   | Daily      | 7 days   | Stream backup to S3       |
+------------------+------------+----------+---------------------------+
```

Backup verification runs daily:

```bash
# deploy/k8s/base/backups/backup-verify-cronjob.yaml
# Restores latest backup to a temporary DB and runs integrity checks
```

---

## Multi-Machine Cluster Deployment

For environments where Kubernetes is not available, Settla provides a
two-machine Docker Compose cluster deployment in `deploy/cluster/`:

```
deploy/cluster/
  docker-compose.data.yml      -- Data tier: PostgreSQL x3, PgBouncer x3,
                                  TigerBeetle, Redis
  docker-compose.compute.yml   -- Compute tier: settla-server x4,
                                  settla-node x4, gateway x2, NATS
  setup.sh                     -- Cluster initialization script
  README.md                    -- Deployment guide
```

### Data Tier (`docker-compose.data.yml`)

Runs on a dedicated machine (e.g., 32GB RAM, 1TB SSD). Contains all
stateful services:

```
+------------------------------------------------------------------+
| DATA TIER (fedora-data, ~19GB memory budget)                     |
+------------------------------------------------------------------+
| PostgreSQL x3 (4GB each)    | PgBouncer x3 (256MB each)         |
|   postgres-ledger :5433     |   pgbouncer-ledger :6433          |
|   postgres-transfer :5434   |   pgbouncer-transfer :6434        |
|   postgres-treasury :5435   |   pgbouncer-treasury :6435        |
+-----------------------------+------------------------------------+
| TigerBeetle (2GB)           | Redis (1GB, password-protected)   |
|   :3001                     |   :6380                           |
+-----------------------------+------------------------------------+
```

Each PostgreSQL instance is tuned for the workload: `shared_buffers=1GB`,
`effective_cache_size=3GB`, `wal_buffers=64MB`, `max_connections=400`.

### Compute Tier (`docker-compose.compute.yml`)

Runs on a separate machine. All DB URLs point to the data tier machine
via the `DATA_HOST` environment variable:

```
+------------------------------------------------------------------+
| COMPUTE TIER (fedora-compute, ~19.5GB memory budget)             |
+------------------------------------------------------------------+
| settla-server x4 (2GB each) | settla-node x4 (1.5GB each)      |
|   gRPC :9090                 |   Outbox relay + workers          |
|   HTTP :8080                 |                                   |
+------------------------------+-----------------------------------+
| gateway x2 (1GB each)       | NATS JetStream (2GB)              |
|   REST :3100                 |   :4222 (client)                  |
|                              |   :8222 (monitoring)              |
+------------------------------+-----------------------------------+
```

NATS lives on the compute tier to minimize latency to workers. The
`DATA_HOST` env var is required and points to the data tier's IP address.

### Running the Cluster

```bash
# On the data machine:
docker compose -f deploy/cluster/docker-compose.data.yml \
  --env-file deploy/cluster/.env.cluster up -d

# On the compute machine:
DATA_HOST=192.168.1.10 docker compose -f deploy/cluster/docker-compose.compute.yml \
  --env-file deploy/cluster/.env.cluster up -d --build
```

---

## Demo Environment

For demonstrations and API testing, Settla provides a complete demo stack:

```bash
make demo
# Or directly:
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.demo.yml up -d --build
```

The demo overlay (`deploy/docker-compose.demo.yml`) adds:

1. **mockprovider** -- An HTTP-controllable mock payment provider that
   simulates on-ramp/off-ramp operations. Admin API at `:9095/admin/config`
   supports scenario injection (provider outage, high latency, partial
   failure).

2. **Demo-friendly port remapping:**
   - Gateway API: `http://localhost:8080`
   - Grafana: `http://localhost:3000`
   - Prometheus: `http://localhost:9090`
   - Mock Provider: `http://localhost:9095`

### Demo Scripts

```bash
scripts/demo-up.sh        # Start the demo environment
scripts/demo-down.sh      # Stop the demo environment
scripts/demo-reset.sh     # Clean slate: down + remove volumes + rebuild
scripts/demo-status.sh    # Check health of all services
scripts/demo-logs.sh      # Tail logs from all services
```

### API Testing

```bash
make api-test             # Run E2E API tests against running services
make api-test-full        # Start Docker, seed data, then run API tests
```

The `api-test` target runs the E2E test suite (`tests/e2e/`) against the
gateway, exercising the full stack through HTTP. See Chapter 8.6 for details.

---

## Common Mistakes

### Mistake 1: Using Deployment for settla-node

A Deployment assigns random pod names. With 8 NATS partitions, you need
stable ordinal identities (settla-node-0 through settla-node-7) to assign
partitions deterministically. Use StatefulSet.

### Mistake 2: Connecting Directly to Postgres (Bypassing PgBouncer)

```
# BAD: Direct connection exhausts max_connections
SETTLA_TRANSFER_DB_URL=postgres://...@postgres-transfer:5434/...

# GOOD: PgBouncer connection pool
SETTLA_TRANSFER_DB_URL=postgres://...@pgbouncer-transfer:6434/...
```

Direct connections bypass the connection pool. With 6 server replicas each
opening 100 connections, you hit Postgres's default `max_connections=100`
immediately.

### Mistake 3: Not Setting preStop Hook

Without `preStop: sleep 15`, a terminated pod receives SIGTERM and begins
shutting down immediately. But kube-proxy has not yet removed it from Service
endpoints. For 5-10 seconds, traffic is still routed to a dying pod, causing
connection resets.

---

## Exercises

1. **Calculate connection pool sizing.** With 6 settla-server replicas and
   8 settla-node replicas, each needing 8 connections to the Transfer DB
   PgBouncer, what should `pool_size` be? What happens if you set it too low?

2. **Design a canary deployment.** Using Argo Rollouts, write a strategy
   that sends 5% of traffic to the canary, waits 5 minutes, then promotes
   to 100%. What metric would you use as the success criteria?

3. **Add a HorizontalPodAutoscaler.** Write an HPA for settla-server that
   scales between 4 and 12 replicas based on CPU utilization (target 70%).
   Why might you also want to scale on a custom metric like
   `settla_grpc_request_latency_seconds`?

---

## What's Next

With the deployment architecture providing the runtime environment, Chapter 8.6
covers the integration tests that verify cross-module behavior -- tenant
isolation, concurrent treasury contention, and end-to-end transfer pipelines
using in-memory test harnesses.
