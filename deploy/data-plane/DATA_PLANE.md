# Settla Data Plane — 2-MacBook Deployment Guide

Declarative deployment of the Settla stateful data plane (PostgreSQL × 3 + TigerBeetle) across **two MacBooks** for sustained **580 TPS** throughput in the homelab topology.

This guide covers hardware prerequisites, topology design rationale, step-by-step setup, tuning reference, network configuration, verification, operations, backup/restore, and troubleshooting.

---

## Table of Contents

1. [Why a Split Data Plane](#1-why-a-split-data-plane)
2. [Topology](#2-topology)
3. [Hardware Requirements](#3-hardware-requirements)
4. [Prerequisites](#4-prerequisites)
5. [Step-by-Step Setup](#5-step-by-step-setup)
6. [Configuration Reference](#6-configuration-reference)
7. [Network & Firewall](#7-network--firewall)
8. [Verification](#8-verification)
9. [Wiring Into the k3s Homelab Cluster](#9-wiring-into-the-k3s-homelab-cluster)
10. [Running Migrations & Seeding](#10-running-migrations--seeding)
11. [Operations](#11-operations)
12. [Backup & Restore](#12-backup--restore)
13. [Troubleshooting](#13-troubleshooting)
14. [Performance Tuning](#14-performance-tuning)

---

## 1. Why a Split Data Plane

The Settla data plane consists of four stateful services:

| Service | Role | Write Volume | I/O Profile |
|---------|------|-------------:|-------------|
| **PostgreSQL transfer** | Transfer records, idempotency, outbox | Highest | Write-heavy |
| **TigerBeetle** | Double-entry balance engine | Highest | Write-heavy, fsync-critical |
| **PostgreSQL ledger** | Audit trail (double-entry history) | Moderate | Mixed |
| **PostgreSQL treasury** | Position tracking | Low | Mostly reads |

Running all four on a single MacBook would saturate the SSD and contend for CPU during the 580 TPS demo. Splitting across two MacBooks:

- **Distributes I/O** across two independent SSDs
- **Isolates the hot path** (transfer + TigerBeetle) from audit/reporting paths
- **Keeps latency low** by co-locating services that commit together
- **Leaves headroom** for 5,000 TPS peak bursts

This is the same pattern used in cloud production: transfer + TigerBeetle share a write-optimized storage tier, while ledger and treasury run on a read-optimized tier.

---

## 2. Topology

```
                         Gigabit LAN
    ┌───────────────────────────────────────────────────────────────┐
    │                                                               │
    │  ┌──────────────────────────┐  ┌──────────────────────────┐   │
    │  │ MacBook 1 (data-hot)     │  │ MacBook 2 (data-audit)   │   │
    │  │                          │  │                          │   │
    │  │ • postgres-transfer:5434 │  │ • postgres-ledger:5433   │   │
    │  │ • tigerbeetle:3001       │  │ • postgres-treasury:5435 │   │
    │  │                          │  │                          │   │
    │  │ 8 GB transfer DB RAM     │  │ 5 GB ledger DB RAM       │   │
    │  │ 4 GB TigerBeetle RAM     │  │ 3 GB treasury DB RAM     │   │
    │  │ Required: 16 GB Mac      │  │ Required: 16 GB Mac      │   │
    │  └────────────┬─────────────┘  └────────────┬─────────────┘   │
    │               │                             │                 │
    │               └──────────┬──────────────────┘                 │
    │                          │                                    │
    │                          │ k3s cluster reads                   │
    │                          │ via PgBouncer upstream              │
    │                          ▼                                    │
    │   ┌──────────────────────────────────────────────────┐        │
    │   │ k3s Homelab Cluster (3× Dell OptiPlex)           │        │
    │   │                                                  │        │
    │   │  pgbouncer-transfer  → MacBook 1:5434            │        │
    │   │  pgbouncer-ledger    → MacBook 2:5433            │        │
    │   │  pgbouncer-treasury  → MacBook 2:5435            │        │
    │   │                                                  │        │
    │   │  settla-server/node  → MacBook 1:3001 (TB)       │        │
    │   └──────────────────────────────────────────────────┘        │
    └───────────────────────────────────────────────────────────────┘
```

### Service Placement Rationale

**MacBook 1 (data-hot) — transfer DB + TigerBeetle**

The transfer write path flows: `gateway → settla-server → settla-node → [transfer DB + TigerBeetle]`. Every transfer writes to both the transfer DB (for the record and idempotency key) and TigerBeetle (for the balance mutation). Co-locating these on one machine minimizes round-trips across the LAN for the critical path.

**MacBook 2 (data-audit) — ledger DB + treasury DB**

- **Ledger DB** receives double-entry audit records written by `settla-node` via the outbox pattern after TigerBeetle commits. This happens asynchronously and can tolerate 1–2 ms of extra LAN latency.
- **Treasury DB** receives position updates from the treasury flush goroutine (every 100ms batch). Low volume.

Both are well-isolated from the hot write path.

---

## 3. Hardware Requirements

### Minimum per MacBook

| | MacBook 1 | MacBook 2 |
|-|-----------|-----------|
| **CPU** | Apple Silicon M1/M2/M3/M4 (8+ cores) | Apple Silicon M1/M2/M3/M4 (6+ cores) |
| **RAM** | **16 GB** minimum (32 GB recommended) | **16 GB** minimum |
| **Storage** | 500 GB free SSD | 500 GB free SSD |
| **Network** | Gigabit Ethernet (strongly preferred) | Gigabit Ethernet (strongly preferred) |
| **OS** | macOS 13 Ventura or later | macOS 13 Ventura or later |

### Container Memory Budget

| MacBook | Service | Container Limit | Container Reservation |
|---------|---------|----------------:|----------------------:|
| 1 | postgres-transfer | 8 GB | 4 GB |
| 1 | tigerbeetle | 4 GB | 2 GB |
| 1 | tigerbeetle-init | 512 MB | — |
| 1 | **Total (Docker)** | **~12.5 GB** | **6 GB** |
| 2 | postgres-ledger | 5 GB | 2 GB |
| 2 | postgres-treasury | 3 GB | 1 GB |
| 2 | **Total (Docker)** | **8 GB** | **3 GB** |

**Leave at least 4 GB free for macOS + Docker Desktop VM overhead.** On a 16 GB Mac, that leaves ~12 GB for containers. MacBook 1 is tight on 16 GB — 32 GB is recommended for headroom.

### Why Ethernet Over WiFi

WiFi introduces jitter (5–50 ms spikes) that destroys tail latency at 580 TPS. Use Ethernet with a USB-C adapter if your MacBooks lack native Ethernet. Target **<1 ms** round-trip latency between the k3s cluster and both MacBooks (verify with `ping`).

---

## 4. Prerequisites

### Install on Both MacBooks

```bash
# Docker Desktop for Mac (includes Docker Engine + Compose)
# Download from: https://www.docker.com/products/docker-desktop/

# Verify:
docker --version          # Should be 24.0+
docker compose version    # Should be v2.20+
```

**Docker Desktop settings to configure (both Macs):**

1. Open **Docker Desktop → Settings → Resources**
2. Set **CPUs** to at least **6** (MacBook 1) or **4** (MacBook 2)
3. Set **Memory** to at least **14 GB** (MacBook 1) or **10 GB** (MacBook 2)
4. Set **Swap** to 2 GB
5. Set **Virtual disk limit** to 200 GB
6. Under **Features in development**, enable **Use containerd for pulling and storing images** (optional, faster image pulls)
7. Click **Apply & Restart**

### Prevent System Sleep (Critical)

macOS will aggressively sleep during idle periods, killing database connections. You must prevent this.

**Option A — Permanent (recommended for dedicated server use):**

```bash
# Disable display sleep, disk sleep, and system sleep
sudo pmset -a displaysleep 0
sudo pmset -a disksleep 0
sudo pmset -a sleep 0
sudo pmset -a powernap 0

# Verify:
pmset -g
```

**Option B — Temporary (per-session):**

```bash
# Run in a terminal and keep it open for the entire demo
caffeinate -dimsu
```

**Option C — GUI:**

System Settings → Energy Saver → Set "Prevent your Mac from automatically sleeping when the display is off" ✓

### Clone the Settla Repository on Both MacBooks

```bash
git clone <settla-repo-url> ~/settla
cd ~/settla
```

Or rsync the `deploy/data-plane/` directory to each MacBook if you don't want the full repo on both.

---

## 5. Step-by-Step Setup

### Step 1: Configure MacBook 1

**On MacBook 1**, navigate to the data plane directory:

```bash
cd ~/settla/deploy/data-plane/macbook-1
cp .env.example .env
```

Edit `.env`:

```bash
POSTGRES_USER=settla
POSTGRES_PASSWORD=<strong-password-here>
TB_CLUSTER_ID=0
```

Start the stack:

```bash
../scripts/setup-macbook-1.sh
```

The script will:
1. Check Docker is running
2. Start `docker compose up -d`
3. Wait for health checks to pass
4. Display the LAN IP and connection URLs

Or run manually:

```bash
docker compose up -d
docker compose ps
```

Expected output:

```
NAME                       STATUS                 PORTS
settla-postgres-transfer   Up 30s (healthy)       0.0.0.0:5434->5432/tcp
settla-tigerbeetle         Up 25s (healthy)       0.0.0.0:3001->3001/tcp
settla-tigerbeetle-init    Exited (0)
```

**Note the MacBook 1 LAN IP** (e.g., `192.168.1.100`):

```bash
ifconfig | grep -E 'inet (192\.168\.|10\.)'
```

### Step 2: Configure MacBook 2

**On MacBook 2**:

```bash
cd ~/settla/deploy/data-plane/macbook-2
cp .env.example .env
```

Edit `.env`, using the **same** `POSTGRES_PASSWORD` as MacBook 1:

```bash
POSTGRES_USER=settla
POSTGRES_PASSWORD=<same-password-as-mac1>
```

Start the stack:

```bash
../scripts/setup-macbook-2.sh
```

Expected output:

```
NAME                        STATUS              PORTS
settla-postgres-ledger      Up 20s (healthy)    0.0.0.0:5433->5432/tcp
settla-postgres-treasury    Up 20s (healthy)    0.0.0.0:5435->5432/tcp
```

**Note the MacBook 2 LAN IP** (e.g., `192.168.1.101`).

### Step 3: Verify LAN Connectivity

From **any OptiPlex node** or your workstation:

```bash
cd ~/settla/deploy/data-plane
./scripts/verify.sh 192.168.1.100 192.168.1.101
```

Expected output:

```
=== Data Plane Connectivity Check ===

MacBook 1 (192.168.1.100):
  [OK]   Transfer DB  192.168.1.100:5434
  [OK]   TigerBeetle  192.168.1.100:3001

MacBook 2 (192.168.1.101):
  [OK]   Ledger DB    192.168.1.101:5433
  [OK]   Treasury DB  192.168.1.101:5435

All data plane services reachable.
```

If any check fails, see [Troubleshooting](#13-troubleshooting).

---

## 6. Configuration Reference

### File Structure

```
deploy/data-plane/
├── DATA_PLANE.md                    # This document
├── macbook-1/
│   ├── docker-compose.yml           # transfer DB + TigerBeetle
│   ├── .env.example                 # Template
│   └── config/
│       ├── postgresql-transfer.conf # Postgres tuning (hot-path)
│       └── pg_hba.conf              # Client authentication
├── macbook-2/
│   ├── docker-compose.yml           # ledger DB + treasury DB
│   ├── .env.example                 # Template
│   └── config/
│       ├── postgresql-ledger.conf   # Postgres tuning
│       ├── postgresql-treasury.conf # Postgres tuning
│       └── pg_hba.conf              # Client authentication
└── scripts/
    ├── setup-macbook-1.sh           # Bring up Mac 1 stack
    ├── setup-macbook-2.sh           # Bring up Mac 2 stack
    ├── verify.sh                    # LAN connectivity check
    └── psql-connect.sh              # Quick psql helper
```

### PostgreSQL Tuning Summary

| Parameter | Transfer (Mac 1) | Ledger (Mac 2) | Treasury (Mac 2) | Rationale |
|-----------|-----------------:|---------------:|-----------------:|-----------|
| `shared_buffers` | 2 GB | 1280 MB | 768 MB | 25% of container RAM |
| `effective_cache_size` | 4 GB | 3 GB | 1536 MB | ~50% of container RAM |
| `work_mem` | 32 MB | 16 MB | 8 MB | Scales with query complexity |
| `maintenance_work_mem` | 512 MB | 256 MB | 128 MB | For VACUUM, CREATE INDEX |
| `wal_buffers` | 64 MB | 64 MB | 32 MB | Write-heavy DBs get more |
| `max_wal_size` | 4 GB | 2 GB | 1 GB | Bounds checkpoint spikes |
| `checkpoint_timeout` | 15 min | 15 min | 15 min | Smooth I/O |
| `random_page_cost` | 1.1 | 1.1 | 1.1 | SSD-optimized |
| `effective_io_concurrency` | 200 | 200 | 200 | SSD async I/O |
| `max_connections` | 400 | 400 | 200 | PgBouncer multiplexes anyway |
| `synchronous_commit` | on | on | on | Durability required |

### TigerBeetle Configuration

| Parameter | Value | Notes |
|-----------|-------|-------|
| Cluster ID | 0 | Matches `TB_CLUSTER_ID` in .env |
| Replica count | 1 | Single-node homelab deployment |
| Address | `0.0.0.0:3001` | Listens on all interfaces |
| Cache grid | 2 GiB | In-memory cache size |
| Data file | `/data/0_0.tigerbeetle` | Persisted in Docker volume |
| Expected throughput | 1M+ TPS capability | 580 TPS is 0.06% of capacity |

### PgBouncer Pool Sizing (upstream from k3s)

The k3s `pgbouncer-*` deployments proxy connections from the cluster to these external PostgreSQL instances. Pool sizing in `deploy/k8s/overlays/homelab/patches/pgbouncer-external-db.yaml`:

| DB | MAX_CLIENT_CONN | DEFAULT_POOL_SIZE | MIN_POOL_SIZE |
|----|----------------:|------------------:|--------------:|
| transfer | 2000 | 200 | 50 |
| ledger | 2000 | 200 | 50 |
| treasury | 2000 | 100 | 25 |

Each `DEFAULT_POOL_SIZE` backend connection is one `max_connections` slot on Postgres. 200 + 200 + 100 = 500 concurrent backend connections. Postgres `max_connections` is 400 per instance — plenty of headroom given PgBouncer's transaction mode releases connections immediately after each transaction.

---

## 7. Network & Firewall

### Find Each MacBook's LAN IP

```bash
# Preferred (Ethernet):
ifconfig en0 | grep "inet "

# Or all IPv4 LAN addresses:
ifconfig | grep -E 'inet (192\.168\.|10\.)' | awk '{print $2}'

# Set a static IP in System Settings → Network → <interface> → Details → TCP/IP → Configure IPv4: Manually
```

**Strongly recommended: set static IPs** (via macOS Network settings or DHCP reservation on your router) so the IPs don't change after reboot. Otherwise you'll have to update `.env.homelab` and redeploy the k3s cluster every time.

### macOS Firewall

macOS has the application firewall disabled by default. If you enabled it:

1. **System Settings → Network → Firewall**
2. Click **Options**
3. Add **Docker Desktop** → **Allow incoming connections**
4. Or disable the firewall entirely for the homelab network (simplest for a dedicated homelab setup)

Docker Desktop handles port forwarding at the VM level, so as long as macOS doesn't block the ports, external connections will reach the containers.

### Required Ports (Inbound)

| MacBook | Port | Protocol | Service |
|---------|------|----------|---------|
| 1 | 5434 | TCP | PostgreSQL transfer |
| 1 | 3001 | TCP | TigerBeetle |
| 2 | 5433 | TCP | PostgreSQL ledger |
| 2 | 5435 | TCP | PostgreSQL treasury |

### Router Configuration

If both MacBooks and the k3s cluster are on the same subnet (e.g., `192.168.1.0/24`), no router changes are needed. If they're on different VLANs, ensure inter-VLAN routing is allowed for the ports above.

---

## 8. Verification

### Automated LAN Check

```bash
./deploy/data-plane/scripts/verify.sh <mac1-ip> <mac2-ip>
```

### Manual TCP Checks

From any machine on the LAN:

```bash
nc -z 192.168.1.100 5434 && echo "Transfer DB OK"
nc -z 192.168.1.100 3001 && echo "TigerBeetle OK"
nc -z 192.168.1.101 5433 && echo "Ledger DB OK"
nc -z 192.168.1.101 5435 && echo "Treasury DB OK"
```

### PostgreSQL Connection Test

```bash
# Transfer DB
PGPASSWORD=<password> psql -h 192.168.1.100 -p 5434 -U settla -d settla_transfer -c 'SELECT version();'

# Ledger DB
PGPASSWORD=<password> psql -h 192.168.1.101 -p 5433 -U settla -d settla_ledger -c 'SELECT version();'

# Treasury DB
PGPASSWORD=<password> psql -h 192.168.1.101 -p 5435 -U settla -d settla_treasury -c 'SELECT version();'
```

### TigerBeetle Connection Test

```bash
# Using the TigerBeetle REPL from any machine with the binary:
echo "" | tigerbeetle repl --cluster=0 --addresses=192.168.1.100:3001
# Should print: "connected to replica 0"
```

### Latency Check (Critical)

LAN latency between the k3s cluster and each MacBook should be **under 1 ms** for 580 TPS to be smooth:

```bash
# From an OptiPlex node:
ping -c 20 192.168.1.100
ping -c 20 192.168.1.101
```

Look at the `avg` field. If it's consistently above 2 ms, you're on WiFi or a congested network — switch to Ethernet.

---

## 9. Wiring Into the k3s Homelab Cluster

Once the data plane is up, configure the k3s homelab overlay to point at it.

### Update `.env.homelab`

On your k3s controller workstation:

```bash
cd ~/settla
cp deploy/k8s/overlays/homelab/.env.homelab.example deploy/k8s/overlays/homelab/.env.homelab
```

Edit `deploy/k8s/overlays/homelab/.env.homelab`:

```bash
MACBOOK_1_IP=192.168.1.100
MACBOOK_2_IP=192.168.1.101
OPTIPLEX_1_IP=192.168.1.10
OPTIPLEX_2_IP=192.168.1.11
OPTIPLEX_3_IP=192.168.1.12
POSTGRES_PASSWORD=<same-password-as-macbooks>
```

### Update Kubernetes Secrets

```bash
make k8s-homelab-secrets-decrypt
# Edit deploy/k8s/overlays/homelab/secrets.yaml
# Set settla-db-credentials.app-password and patroni-credentials.app-password
# to the same POSTGRES_PASSWORD used on the MacBooks.
make k8s-homelab-secrets-encrypt
```

### Deploy

```bash
make k8s-homelab-deploy
```

This renders the overlay, substitutes `${MACBOOK_1_IP}` and `${MACBOOK_2_IP}` in the PgBouncer upstream URLs and the TigerBeetle address, and applies to the cluster.

### Validate

```bash
make k8s-homelab-validate
```

The validation script checks that PgBouncer can reach the external Postgres instances over the LAN.

---

## 10. Running Migrations & Seeding

**Migrations are fully automated.** You do NOT need to run `make migrate-up` manually.

The k3s homelab overlay includes a Kubernetes `Job` (`settla-migrate`) that runs
migrations against all three external databases before any application pods
start. App pods (`settla-server`, `settla-node`, `webhook`) have `initContainers`
that block via `kubectl wait` until the Job reaches `condition=Complete`.

**Workflow:**

```bash
# 1. Build the migration image and import into k3s (one-time, or after new migrations)
make k8s-homelab-migrate-build

# 2. Deploy — this automatically runs migrations before apps start
make k8s-homelab-deploy
```

`make k8s-homelab-deploy` now:
1. Deletes any existing `settla-migrate` Job (so migrations re-run)
2. Applies the full manifest (Job + RBAC + apps)
3. Waits for the migration Job to complete
4. Shows migration logs
5. App pods start automatically once their `initContainer` sees the Job complete

See **[MIGRATIONS.md](../k8s/migrations/MIGRATIONS.md)** for full details on how the
migration pipeline works, how to add new migrations, and how to troubleshoot.

### Manual Migration (Rarely Needed)

If you need to re-run migrations without a full redeploy:

```bash
make k8s-homelab-migrate
```

This deletes the existing Job and re-applies it, waiting for completion.

To run migrations manually from your workstation (e.g., when the cluster is down):

```bash
export SETTLA_LEDGER_DB_MIGRATE_URL="postgres://settla:<password>@192.168.1.101:5433/settla_ledger?sslmode=disable"
export SETTLA_TRANSFER_DB_MIGRATE_URL="postgres://settla:<password>@192.168.1.100:5434/settla_transfer?sslmode=disable"
export SETTLA_TREASURY_DB_MIGRATE_URL="postgres://settla:<password>@192.168.1.101:5435/settla_treasury?sslmode=disable"

make migrate-up
```

### Seed Test Data

```bash
# Standard seed (50 tenants)
make seed

# Or for tenant-scale tests:
make bench-seed-20k      # 20,000 tenants
make bench-seed-100k     # 100,000 tenants
```

### Verify Schema

```bash
./deploy/data-plane/scripts/psql-connect.sh transfer
\dt
\q
```

---

## 11. Operations

### Start / Stop Services

**On MacBook 1:**

```bash
cd ~/settla/deploy/data-plane/macbook-1
docker compose up -d       # Start
docker compose stop        # Stop (preserves data)
docker compose down        # Stop and remove containers (preserves volumes)
docker compose down -v     # Stop and DESTROY all data (dangerous!)
```

**On MacBook 2:**

```bash
cd ~/settla/deploy/data-plane/macbook-2
docker compose up -d
docker compose stop
```

### View Logs

```bash
# All services on one Mac
docker compose logs -f

# Specific service
docker compose logs -f postgres-transfer
docker compose logs -f tigerbeetle
```

### Check Resource Usage

```bash
docker stats --no-stream

# Or via Docker Desktop dashboard
```

### Monitor PostgreSQL Activity

```bash
# Show active connections
docker exec -it settla-postgres-transfer psql -U settla -d settla_transfer -c "
  SELECT pid, state, wait_event_type, wait_event, query
  FROM pg_stat_activity
  WHERE state != 'idle'
  ORDER BY query_start;
"

# Show slow queries
docker exec -it settla-postgres-transfer psql -U settla -d settla_transfer -c "
  SELECT query, calls, total_exec_time, mean_exec_time
  FROM pg_stat_statements
  ORDER BY mean_exec_time DESC
  LIMIT 20;
"

# Show database size
docker exec -it settla-postgres-transfer psql -U settla -d settla_transfer -c "
  SELECT pg_size_pretty(pg_database_size('settla_transfer'));
"
```

### Monitor TigerBeetle

TigerBeetle doesn't expose Prometheus metrics directly, but you can observe:

```bash
# Container stats
docker stats settla-tigerbeetle --no-stream

# Data file size
docker exec settla-tigerbeetle ls -lh /data/

# Logs (commit throughput)
docker compose logs tigerbeetle | tail -50
```

### Restart After macOS Reboot

By default, Docker Desktop starts on boot and `restart: unless-stopped` brings the containers back up automatically. If they don't:

```bash
# MacBook 1
cd ~/settla/deploy/data-plane/macbook-1 && docker compose up -d

# MacBook 2
cd ~/settla/deploy/data-plane/macbook-2 && docker compose up -d
```

Consider adding a LaunchAgent to ensure the stacks come back after a reboot:

```bash
# ~/Library/LaunchAgents/io.settla.data-plane.plist
# (Create a plist that runs docker compose up -d at login)
```

---

## 12. Backup & Restore

### Daily Backup (manual)

**PostgreSQL** (run on each MacBook or from any machine with `pg_dump`):

```bash
# MacBook 1 — transfer
pg_dump -h 192.168.1.100 -p 5434 -U settla -Fc settla_transfer \
  > backup_transfer_$(date +%Y%m%d).dump

# MacBook 2 — ledger + treasury
pg_dump -h 192.168.1.101 -p 5433 -U settla -Fc settla_ledger \
  > backup_ledger_$(date +%Y%m%d).dump
pg_dump -h 192.168.1.101 -p 5435 -U settla -Fc settla_treasury \
  > backup_treasury_$(date +%Y%m%d).dump
```

**TigerBeetle** — stop the container, copy the data file, restart:

```bash
# On MacBook 1
cd ~/settla/deploy/data-plane/macbook-1
docker compose stop tigerbeetle
docker run --rm -v settla-data-mac1_tbdata:/data -v $(pwd):/backup alpine \
  cp /data/0_0.tigerbeetle /backup/tigerbeetle_$(date +%Y%m%d).bin
docker compose start tigerbeetle
```

### Automated Backup (recommended)

Create a cron job on each MacBook:

```bash
# On MacBook 1
crontab -e
# Add:
0 2 * * * cd ~/settla/deploy/data-plane/macbook-1 && pg_dump -h localhost -p 5434 -U settla -Fc settla_transfer > ~/backups/transfer_$(date +\%Y\%m\%d).dump 2>&1 | logger -t settla-backup
```

### Restore PostgreSQL

```bash
# Drop existing DB first (DANGEROUS — make sure you want to do this)
docker exec -it settla-postgres-transfer psql -U settla -d postgres -c "
  DROP DATABASE IF EXISTS settla_transfer;
  CREATE DATABASE settla_transfer;
"

# Restore from dump
pg_restore -h 192.168.1.100 -p 5434 -U settla -d settla_transfer \
  backup_transfer_20260404.dump
```

### Restore TigerBeetle

```bash
cd ~/settla/deploy/data-plane/macbook-1
docker compose stop tigerbeetle

# Replace the data file
docker run --rm -v settla-data-mac1_tbdata:/data -v $(pwd):/backup alpine \
  cp /backup/tigerbeetle_20260404.bin /data/0_0.tigerbeetle

docker compose start tigerbeetle
```

---

## 13. Troubleshooting

### Cannot Connect From LAN

```bash
# 1. Is the container running?
docker compose ps

# 2. Is the port bound to 0.0.0.0 (not just 127.0.0.1)?
docker port settla-postgres-transfer 5432
# Should show: 0.0.0.0:5434

# 3. Can you reach it from localhost?
nc -z 127.0.0.1 5434

# 4. Can you reach it from another machine?
# From an OptiPlex:
nc -z <mac-ip> 5434

# 5. macOS firewall blocking?
sudo pfctl -sr | grep 5434

# 6. Are you on the same subnet?
ifconfig | grep inet
```

### Container Won't Start

```bash
# Check logs
docker compose logs postgres-transfer

# Common errors:
# - "data directory has wrong ownership" → rm the volume and recreate
# - "could not bind socket" → another service is using the port
# - "FATAL: password authentication failed" → .env password changed, data dir has old
```

### High Latency (>5ms p99)

1. **WiFi instead of Ethernet** — check with `ping <mac-ip>`
2. **macOS is throttling** — `pmset -g` should show "coreaudiod" not "sleep"
3. **Docker Desktop resources too low** — bump CPU to 6+ and memory to 14GB+
4. **PostgreSQL is swapping** — check `docker stats`; if memory usage is at the limit, increase container memory

### TigerBeetle Shows "could not format"

This means the data file already exists. If you're re-initializing intentionally:

```bash
cd ~/settla/deploy/data-plane/macbook-1
docker compose down
docker volume rm settla-data-mac1_tbdata
docker compose up -d
```

**Warning:** This destroys all TigerBeetle state — every balance and every transfer. Only do this for a clean reset before a fresh test run.

### PostgreSQL "too many connections"

```bash
# Check current connections
docker exec settla-postgres-transfer psql -U settla -d settla_transfer -c "
  SELECT count(*) FROM pg_stat_activity;
"

# If near 400, check which pods are connecting from the cluster:
kubectl exec -n settla deploy/pgbouncer-transfer -- psql -p 6432 -U pgbouncer pgbouncer -c "SHOW CLIENTS;"

# Usually means PgBouncer pool sizes are too high. Reduce DEFAULT_POOL_SIZE
# in deploy/k8s/overlays/homelab/patches/pgbouncer-external-db.yaml.
```

### Docker Desktop VM Unresponsive

```bash
# Restart Docker Desktop
osascript -e 'quit app "Docker"'
open -a Docker

# Or force-reset the VM (keeps images and volumes):
# Docker Desktop → Troubleshoot → Clean / Purge data → Volumes: NO
```

### macOS Went to Sleep Mid-Demo

```bash
# Verify sleep is disabled:
pmset -g | grep -E 'displaysleep|sleep'
# sleep should be 0, displaysleep should be 0

# If not:
sudo pmset -a sleep 0 displaysleep 0 disksleep 0
```

---

## 14. Performance Tuning

### If You're Not Hitting 580 TPS

In order of likelihood:

1. **Check LAN latency** — `ping <mac-ip>` from an OptiPlex. Target <1 ms avg.

2. **Check PostgreSQL wait events:**
   ```sql
   SELECT wait_event_type, wait_event, count(*)
   FROM pg_stat_activity
   WHERE state = 'active'
   GROUP BY 1, 2
   ORDER BY 3 DESC;
   ```
   - `LWLock: WALWriteLock` → WAL is bottleneck. Increase `wal_buffers`, tune `checkpoint_timeout`.
   - `IO: DataFileRead` → Cold cache, increase `shared_buffers`.
   - `Client: ClientRead` → Network latency, switch to Ethernet.

3. **Check Docker Desktop resource usage** — if container is pegged at its CPU limit, increase the limit.

4. **Check TigerBeetle commit throughput** — TigerBeetle logs every 1000 commits. 580 TPS should show smooth progress with no warning messages.

5. **Check PgBouncer pool saturation** (in the k3s cluster):
   ```bash
   kubectl exec -n settla deploy/pgbouncer-transfer -- psql -p 6432 -U pgbouncer pgbouncer -c "SHOW POOLS;"
   ```
   If `sv_active == DEFAULT_POOL_SIZE`, the pool is saturated. Increase it.

### If You Want More Than 580 TPS

The bottleneck for going beyond 580 TPS on this hardware will typically be:

- **PostgreSQL transfer DB fsync throughput** — the single hottest bottleneck. On Apple Silicon with internal NVMe, expect ~5,000 TPS ceiling on the transfer DB alone before you saturate WAL writes.
- **LAN bandwidth** — Gigabit is 125 MB/s. Each transfer is ~1 KB across the wire, so theoretical ceiling is ~100K TPS (not a concern at 5K TPS peak).
- **TigerBeetle is never the bottleneck** — it can sustain 1M+ TPS on a single node.

For **5,000 TPS peak** bursts, the current tuning already supports this — just run `make bench-peak`.

For sustained 5,000 TPS, you'd need to:
- Move PostgreSQL to NVMe with sustained >500 MB/s write throughput
- Increase `max_wal_size` to 16 GB
- Set `synchronous_commit = off` (accept losing the last few ms on crash)
- Upgrade to 64 GB MacBooks

### Tuning Changes That DON'T Help at 580 TPS

- Increasing `shared_buffers` above 4 GB — diminishing returns
- Enabling parallel query for OLTP workloads — row-level ops are already fast
- Tweaking `random_page_cost` below 1.1 — already SSD-optimized
- Adding more autovacuum workers — 3-4 is enough at this scale

---

## Quick Reference Card

```bash
# === MacBook 1 ===
cd ~/settla/deploy/data-plane/macbook-1
docker compose up -d       # Start
docker compose down        # Stop
docker compose logs -f     # Logs
docker compose ps          # Status

# === MacBook 2 ===
cd ~/settla/deploy/data-plane/macbook-2
docker compose up -d
docker compose down
docker compose logs -f
docker compose ps

# === From anywhere ===
./deploy/data-plane/scripts/verify.sh <mac1-ip> <mac2-ip>
./deploy/data-plane/scripts/psql-connect.sh transfer
./deploy/data-plane/scripts/psql-connect.sh ledger
./deploy/data-plane/scripts/psql-connect.sh treasury

# === k3s deployment (after data plane is up) ===
make k8s-homelab-deploy
make k8s-homelab-validate

# === Load test ===
make bench-sustained GATEWAY_URL=http://<optiplex-ip>:30080
```

---

## Summary Checklist

Before running the 580 TPS demo, verify:

- [ ] Both MacBooks have 16+ GB RAM, 500+ GB free SSD
- [ ] Docker Desktop allocated 14+ GB RAM (Mac 1), 10+ GB RAM (Mac 2)
- [ ] `pmset` shows sleep=0 on both MacBooks
- [ ] Static LAN IPs configured for both MacBooks
- [ ] Both Macs using Ethernet (not WiFi)
- [ ] `./verify.sh` passes all 4 connectivity checks
- [ ] `ping` shows <1 ms avg latency from OptiPlex nodes to both Macs
- [ ] `.env.homelab` has correct `MACBOOK_1_IP` and `MACBOOK_2_IP`
- [ ] K8s secrets have matching `POSTGRES_PASSWORD`
- [ ] `make migrate-up` completed successfully
- [ ] `make seed` completed successfully
- [ ] `make k8s-homelab-deploy` completed successfully
- [ ] `make k8s-homelab-validate` passes all checks
- [ ] Smoke test passes: `make bench-smoke GATEWAY_URL=http://<node>:30080`

Once all items are checked, run the demo:

```bash
make bench-sustained GATEWAY_URL=http://<optiplex-ip>:30080
```
