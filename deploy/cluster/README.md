# Multi-Machine Cluster Setup for Load Testing

This directory contains Docker Compose files split across 3 machines for
running full-scale load tests (580 TPS sustained, 5,000 TPS peak).

## Machine Roles

| Machine | Role | IP (example) | Specs |
|---------|------|-------------|-------|
| **fedora-data** | Data tier | 192.168.1.10 | Fedora, 32GB RAM, 1TB SSD |
| **fedora-compute** | Compute tier | 192.168.1.11 | Fedora, 32GB RAM, 1TB HDD |
| **mac-loadgen** | Load generator | 192.168.1.12 | M3 Pro Mac |

## Quick Start

### 1. Set IPs in `.env.cluster`

```bash
cp deploy/cluster/.env.cluster.example deploy/cluster/.env.cluster
# Edit with your actual IPs
```

### 2. Clone repo on all machines

```bash
# On fedora-data and fedora-compute:
git clone <repo-url> settla && cd settla
cp deploy/cluster/.env.cluster .env.cluster
# Edit .env.cluster with correct IPs
```

### 3. Start data tier (fedora-data)

```bash
cd settla
docker compose -f deploy/cluster/docker-compose.data.yml --env-file deploy/cluster/.env.cluster up -d
# Wait for healthy:
docker compose -f deploy/cluster/docker-compose.data.yml --env-file deploy/cluster/.env.cluster ps
```

### 4. Start compute tier (fedora-compute)

```bash
cd settla
docker compose -f deploy/cluster/docker-compose.compute.yml --env-file deploy/cluster/.env.cluster up -d --build
# Wait for healthy:
docker compose -f deploy/cluster/docker-compose.compute.yml --env-file deploy/cluster/.env.cluster ps
```

### 5. Seed data (from any machine with psql access)

```bash
# From fedora-data or mac-loadgen:
PGPASSWORD=settla psql -h $DATA_HOST -p 5434 -U settla -d settla_transfer < db/seed/transfer_seed.sql
PGPASSWORD=settla psql -h $DATA_HOST -p 5435 -U settla -d settla_treasury < db/seed/treasury_seed.sql

# For 20K tenant scale tests:
go run ./tests/loadtest/ seed -count=20000 \
  -transfer-db="postgres://settla:settla@$DATA_HOST:5434/settla_transfer?sslmode=disable" \
  -treasury-db="postgres://settla:settla@$DATA_HOST:5435/settla_treasury?sslmode=disable"
```

### 6. Run load tests (mac-loadgen)

```bash
# Scenario A - Smoke
make bench-smoke GATEWAY_URL=http://$COMPUTE_HOST:3100

# Scenario B - Sustained 580 TPS
make bench-sustained GATEWAY_URL=http://$COMPUTE_HOST:3100

# Scenario C - Peak 5,000 TPS
make bench-peak GATEWAY_URL=http://$COMPUTE_HOST:3100

# Full suite
make bench-all GATEWAY_URL=http://$COMPUTE_HOST:3100
```

## Memory Budget

### fedora-data (32GB)

| Component | Memory | Count | Total |
|-----------|--------|-------|-------|
| PostgreSQL (transfer) | 6 GB | 1 | 6 GB |
| PostgreSQL (ledger) | 4 GB | 1 | 4 GB |
| PostgreSQL (treasury) | 2 GB | 1 | 2 GB |
| TigerBeetle | 4 GB | 1 | 4 GB |
| PgBouncer x3 | 256 MB | 3 | 0.75 GB |
| Redis | 2 GB | 1 | 2 GB |
| OS + buffer | — | — | ~13 GB |
| **Total** | | | **~19 GB** |

### fedora-compute (32GB)

| Component | Memory | Count | Total |
|-----------|--------|-------|-------|
| settla-server | 2 GB | 4 | 8 GB |
| settla-node | 2 GB | 4 | 8 GB |
| gateway | 512 MB | 2 | 1 GB |
| NATS | 2 GB | 1 | 2 GB |
| Tyk | 512 MB | 1 | 0.5 GB |
| OS + buffer | — | — | ~12.5 GB |
| **Total** | | | **~19.5 GB** |

## Port Map

### fedora-data exposed ports

| Port | Service |
|------|---------|
| 5433 | PostgreSQL (ledger) raw |
| 5434 | PostgreSQL (transfer) raw |
| 5435 | PostgreSQL (treasury) raw |
| 6433 | PgBouncer (ledger) |
| 6434 | PgBouncer (transfer) |
| 6435 | PgBouncer (treasury) |
| 3001 | TigerBeetle |
| 6380 | Redis |

### fedora-compute exposed ports

| Port | Service |
|------|---------|
| 3100 | Gateway (load balancer entry) |
| 3101 | Gateway replica 2 |
| 4222 | NATS client |
| 8222 | NATS monitoring |
| 9090-9093 | settla-server gRPC (x4) |
| 8081-8084 | settla-server HTTP (x4) |
| 6060 | pprof (server 1) |

## Networking

All machines must be on the same LAN. Docker containers use `host` networking
on the Fedora machines (no Docker bridge overhead = lower latency). On macOS,
Docker Desktop always uses a VM, so the load generator connects via the
Fedora machine's LAN IP.

## Scaling Up

To increase throughput beyond 580 TPS:

```bash
# Scale settla-server to 6 replicas:
docker compose -f deploy/cluster/docker-compose.compute.yml --env-file deploy/cluster/.env.cluster \
  up -d --scale settla-server=6

# Scale settla-node to 8 replicas (one per NATS partition):
docker compose -f deploy/cluster/docker-compose.compute.yml --env-file deploy/cluster/.env.cluster \
  up -d --scale settla-node=8
```
