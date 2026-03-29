# Load Testing & Benchmarking

## When to Use

- Validating capacity claims (580 TPS sustained, 5,000 TPS peak, 50M transfers/day)
- Before production releases with performance-sensitive changes
- Proving tenant scale (20K–100K tenants) with Zipf traffic distribution
- Measuring settlement batch time at scale
- Establishing baseline performance after infrastructure changes

## Prerequisites

- Repo cloned on all machines
- Docker installed on data and compute machines
- Go 1.25+ installed on load generator machine
- `psql` and `redis-cli` for connectivity checks (optional)
- All machines on the same LAN (< 1ms latency between them)

## Infrastructure Layout

### Machines

| Machine | Role | Min Specs | Runs |
|---------|------|-----------|------|
| **fedora-data** | Data tier | 32GB RAM, 1TB SSD, 8+ cores | PostgreSQL x3, PgBouncer x3, TigerBeetle, Redis |
| **fedora-compute** | Compute tier | 32GB RAM, 1TB storage, 8+ cores | settla-server x4–6, settla-node x4–8, gateway x2, NATS |
| **mac-loadgen** | Load generator | 16GB RAM, 4+ cores | Load test harness, monitoring (Grafana, Prometheus) |

### Architecture

```
┌──────────────────────┐     ┌──────────────────────────┐     ┌───────────────────┐
│  fedora-data         │     │  fedora-compute          │     │  mac-loadgen      │
│                      │     │                          │     │                   │
│  PostgreSQL x3       │◄────│  settla-server x4        │◄────│  Load test        │
│    ledger   :5433    │     │  settla-node   x4        │     │  harness          │
│    transfer :5434    │     │  gateway       x2  :3100 │     │                   │
│    treasury :5435    │     │  NATS          :4222     │     │  make bench-*     │
│  PgBouncer x3        │     │                          │     │                   │
│    :6433 :6434 :6435 │     │  gRPC :9090              │     │  Grafana  :3002   │
│  TigerBeetle :3001   │     │  HTTP :8080              │     │  Prometheus :9092 │
│  Redis       :6380   │     │  pprof :6060             │     │                   │
│                      │     │                          │     │                   │
│  ~19 GB RAM          │     │  ~20 GB RAM              │     │  ~4 GB RAM        │
└──────────────────────┘     └──────────────────────────┘     └───────────────────┘
```

### Why This Layout

- **Data tier on SSD**: PostgreSQL at 50M rows/day needs fast sequential writes. TigerBeetle's io_uring requires low-latency disk. The 1TB SSD is the critical resource.
- **Compute tier separate from data**: settla-server and settla-node are CPU-bound. Co-locating them with databases causes CPU contention that skews both throughput and latency.
- **Load generator on a third machine**: The harness at 5K TPS creates ~15K HTTP connections (quote + create + poll per transfer). Running it on the same machine as the system under test contaminates latency measurements and steals CPU.
- **NATS on compute tier**: Workers consume from NATS with sub-millisecond latency requirements. Network hops between NATS and workers add tail latency to every transfer.

### Memory Budgets

**fedora-data (32GB)**

| Component | Memory | Notes |
|-----------|--------|-------|
| PostgreSQL (transfer) | 6 GB | shared_buffers=2GB, effective_cache=4GB, heaviest writer |
| PostgreSQL (ledger) | 4 GB | shared_buffers=1GB, CQRS read model |
| PostgreSQL (treasury) | 2 GB | Light — only position snapshots |
| TigerBeetle | 4 GB | Fixed allocation, io_uring buffers |
| PgBouncer x3 | 0.75 GB | 256MB each, connection multiplexing |
| Redis | 2 GB | maxmemory=2gb, volatile-lru |
| **Total** | **~19 GB** | Leaves 13GB for OS page cache (critical for PG performance) |

**fedora-compute (32GB)**

| Component | Memory | Notes |
|-----------|--------|-------|
| settla-server x4 | 8 GB | 2GB each, gRPC + treasury reserve + outbox write |
| settla-node x4 | 8 GB | 2GB each, worker pools + ledger batch buffer |
| gateway x2 | 1 GB | 512MB each, gRPC pool + tenant cache |
| NATS | 2 GB | JetStream file store, 11 streams |
| **Total** | **~19 GB** | Leaves 13GB headroom for spikes |

## Steps

### 1. Configure Environment

```bash
# On ALL machines: clone repo
git clone <repo-url> settla && cd settla

# Create cluster config from template
cp deploy/cluster/.env.cluster.example deploy/cluster/.env.cluster
```

Edit `deploy/cluster/.env.cluster` with actual IPs:

```bash
# Replace with your real LAN IPs
DATA_HOST=192.168.1.10        # fedora-data
COMPUTE_HOST=192.168.1.11     # fedora-compute
LOADGEN_HOST=192.168.1.12     # mac-loadgen
```

Key tuning parameters (already set in the template):

| Parameter | Value | Why |
|-----------|-------|-----|
| `SETTLA_MOCK_DELAY_MS` | 0 | No artificial provider delay — measures real system throughput |
| `SETTLA_LOG_LEVEL` | info | Debug logging adds 10–20% CPU overhead |
| `SETTLA_RELAY_BATCH_SIZE` | 500 | Outbox relay fetches 500 entries per poll (default 200) |
| `SETTLA_RELAY_POLL_INTERVAL_MS` | 20 | Poll every 20ms (default 30ms) |
| `SETTLA_WORKER_POOL_PROVIDER` | 32 | 32 concurrent provider calls (bottleneck for throughput) |
| `SETTLA_LOAD_SHED_MAX_CONCURRENT` | 10000 | Allow 10K inflight at gateway (default 5000) |
| `SETTLA_RATE_LIMIT_PER_TENANT` | 10000 | Don't rate-limit during load tests |

### 2. Start Data Tier (on fedora-data)

```bash
cd settla
bash deploy/cluster/setup.sh data-up
```

Wait for all health checks to pass (~30 seconds):

```bash
bash deploy/cluster/setup.sh data-status
```

Expected: all services `Up (healthy)`.

**Verify PostgreSQL tuning applied:**

```bash
docker exec deploy-cluster-postgres-transfer-1 \
  psql -U settla -d settla_transfer -c "SHOW shared_buffers; SHOW effective_cache_size;"
# Should show: shared_buffers=2GB, effective_cache_size=4GB
```

### 3. Start Compute Tier (on fedora-compute)

```bash
cd settla
bash deploy/cluster/setup.sh compute-up
```

This builds the Go binaries inside Docker (~90 seconds first time, cached after). Wait for health checks (~20 seconds after build):

```bash
bash deploy/cluster/setup.sh compute-status
```

Expected: 4 settla-server, 4 settla-node, 2 gateway, 1 NATS — all running.

### 4. Verify Cross-Machine Connectivity (from mac-loadgen)

```bash
bash deploy/cluster/setup.sh check
```

Expected output:

```
Checking connectivity...
  Data tier (192.168.1.10): PostgreSQL OK
  Redis (192.168.1.10:6380): OK
  Gateway (192.168.1.11:3100): OK
  NATS (192.168.1.11:8222): OK
```

If anything shows UNREACHABLE:
- Check firewall rules: `sudo firewall-cmd --list-ports` (Fedora)
- Open required ports: `sudo firewall-cmd --add-port=5434/tcp --permanent && sudo firewall-cmd --reload`
- Verify Docker is listening on 0.0.0.0 (not 127.0.0.1)

### 5. Seed Test Data

Base seed (50 tenants — required for all scenarios):

```bash
bash deploy/cluster/setup.sh seed
```

For tenant scale tests (Scenarios G–J):

```bash
# 20K tenants — takes ~1–2 minutes
bash deploy/cluster/setup.sh seed-20k

# 100K tenants — takes ~5–10 minutes
bash deploy/cluster/setup.sh seed-100k
```

Verify tenant count:

```bash
PGPASSWORD=settla psql -h $DATA_HOST -p 5434 -U settla -d settla_transfer \
  -c "SELECT COUNT(*) FROM tenants;"
```

### 6. Run Load Tests (from mac-loadgen)

Set the gateway URL for all commands:

```bash
export GATEWAY_URL=http://$COMPUTE_HOST:3100
```

#### Scenario A — Smoke Test (1 minute)

```bash
make bench-smoke
```

Pass criteria: 0% error rate, all transfers complete, p99 < 2s.

#### Scenario B — Sustained 580 TPS (12 minutes)

```bash
make bench-sustained
```

Pass criteria: p50 < 100ms, p99 < 500ms, error rate < 0.1%, zero stuck transfers.

#### Scenario C — Peak 5,000 TPS (7 minutes)

```bash
make bench-peak
```

Pass criteria: p50 < 200ms, p99 < 1s, error rate < 1%. If this fails, scale up:

```bash
# On fedora-compute:
bash deploy/cluster/setup.sh compute-scale 6 8
# Then re-run from mac-loadgen
```

#### Scenario D — Soak Test (1 hour)

```bash
make bench-soak
```

Pass criteria: RSS growth < 10%, goroutine count stable, no connection pool exhaustion.

#### Scenario E — Spike Test (3 minutes)

```bash
make bench-spike
```

Pass criteria: recovery within 30 seconds after spike drops, no data loss.

#### Scenario F — Hot-Spot (6 minutes)

```bash
make bench-hotspot
```

Pass criteria: rate limiting activates for hot tenant, cold tenants unaffected.

#### Scenario G — 20K Tenants (12 minutes)

Requires `seed-20k` completed first.

```bash
make bench-tenants-20k
```

Pass criteria: auth cache memory stable, latency unchanged vs 50-tenant baseline.

#### Scenario H — 100K Tenants (12 minutes)

Requires `seed-100k` completed first.

```bash
make bench-tenants-100k
```

#### Scenario I — 20K Tenants at 5K TPS (7 minutes)

Compound stress test. Requires `seed-20k`.

```bash
make bench-tenants-peak
```

#### Scenario J — Settlement Batch (1+ hours)

Requires `seed-20k`. Generates 1 hour of traffic then triggers settlement.

```bash
make bench-settlement
```

Pass criteria: settlement completes within 2 hours for 20K tenants.

#### Full Suite

```bash
# Core scenarios (A–F + microbenchmarks)
make bench-all

# Scale scenarios (need pre-seeded tenants)
make bench-tenants-20k bench-tenants-peak bench-settlement
```

### 7. Collect Results

All scenarios write structured JSON to `tests/loadtest/results/`:

```bash
ls tests/loadtest/results/*.json
```

Generate aggregate report:

```bash
make bench-report
cat tests/loadtest/results/aggregate-report.json | python3 -m json.tool
```

Report template for manual fill-in: `tests/loadtest/REPORT_TEMPLATE.md`.

### 8. Capture Profiles During Load

While a scenario is running, capture CPU/memory profiles from settla-server:

```bash
# Heap profile
curl -s http://$COMPUTE_HOST:6060/debug/pprof/heap > profiles/heap-scenario-c.prof

# 30-second CPU profile
curl -s "http://$COMPUTE_HOST:6060/debug/pprof/profile?seconds=30" > profiles/cpu-scenario-c.prof

# Goroutine dump
curl -s http://$COMPUTE_HOST:6060/debug/pprof/goroutine > profiles/goroutine-scenario-c.prof

# Analyze
go tool pprof profiles/cpu-scenario-c.prof
```

### 9. Tear Down

```bash
# On fedora-compute:
bash deploy/cluster/setup.sh compute-down

# On fedora-data:
bash deploy/cluster/setup.sh data-down

# To remove all data (volumes):
docker compose -f deploy/cluster/docker-compose.data.yml --env-file deploy/cluster/.env.cluster down -v
docker compose -f deploy/cluster/docker-compose.compute.yml --env-file deploy/cluster/.env.cluster down -v
```

## Scaling Guide

| TPS Target | settla-server | settla-node | gateway | Notes |
|-----------|---------------|-------------|---------|-------|
| 100 | 1 | 1 | 1 | Development |
| 580 | 4 | 4 | 2 | Sustained daily average |
| 2,000 | 4 | 4 | 2 | Moderate peak |
| 5,000 | 6 | 8 | 2 | Maximum peak |
| 5,000+ | 8+ | 8+ | 4 | Requires 3rd compute machine |

Scale command:

```bash
bash deploy/cluster/setup.sh compute-scale <servers> <nodes>
# Example: 6 servers, 8 nodes
bash deploy/cluster/setup.sh compute-scale 6 8
```

## Troubleshooting

### Transfers created but never complete

**Cause:** settla-node workers not processing NATS messages.

```bash
# Check node logs
docker compose -f deploy/cluster/docker-compose.compute.yml \
  --env-file deploy/cluster/.env.cluster logs settla-node --tail=50

# Check NATS stream depth
curl -s http://$COMPUTE_HOST:8222/jsz | python3 -m json.tool | grep -A5 pending
```

Fix: restart nodes or check DB connectivity from compute to data tier.

### Gateway returns 503 (circuit breaker open)

**Cause:** gRPC backend (settla-server) is down or overloaded.

```bash
# Check server health
curl -s http://$COMPUTE_HOST:8081/health

# Check server logs for panics
docker compose -f deploy/cluster/docker-compose.compute.yml \
  --env-file deploy/cluster/.env.cluster logs settla-server --tail=50
```

Fix: restart gateway to reset circuit breaker, or scale up servers.

### High p99 latency (> 1s) at moderate TPS

**Cause:** Usually PgBouncer connection pool saturation or PostgreSQL checkpoint stalls.

```bash
# Check PgBouncer waiting clients
PGPASSWORD=settla psql -h $DATA_HOST -p 6434 -U settla -d pgbouncer -c "SHOW POOLS;"

# Check PostgreSQL active queries
PGPASSWORD=settla psql -h $DATA_HOST -p 5434 -U settla -d settla_transfer \
  -c "SELECT count(*), state FROM pg_stat_activity GROUP BY state;"
```

Fix: increase `DEFAULT_POOL_SIZE` in PgBouncer, or tune `max_wal_size` to reduce checkpoint frequency.

### Auth errors (401) on scale tests

**Cause:** Scale-test tenants not seeded. Scenarios G–J use generated tenants with API keys like `sk_live_scale_0000001`.

```bash
# Check if scale tenants exist
PGPASSWORD=settla psql -h $DATA_HOST -p 5434 -U settla -d settla_transfer \
  -c "SELECT COUNT(*) FROM tenants WHERE slug LIKE 'scale-tenant-%';"
```

Fix: run `bash deploy/cluster/setup.sh seed-20k` (or `seed-100k`).

### Connection refused from compute to data

**Cause:** Firewall blocking ports or Docker binding to 127.0.0.1.

```bash
# On fedora-data: check ports are open
sudo firewall-cmd --list-ports

# Open all required ports
for port in 5433 5434 5435 6433 6434 6435 3001 6380; do
  sudo firewall-cmd --add-port=${port}/tcp --permanent
done
sudo firewall-cmd --reload

# Verify Docker is listening on all interfaces
ss -tlnp | grep -E '5433|5434|5435|6433|6434|6435|3001|6380'
# Should show 0.0.0.0:PORT, not 127.0.0.1:PORT
```

### Scenario B passes but Scenario C fails

Expected if running 4 servers + 4 nodes. 5K TPS requires 6+ servers and 8 nodes:

```bash
bash deploy/cluster/setup.sh compute-scale 6 8
# Wait 30s for new replicas to start
make bench-peak GATEWAY_URL=http://$COMPUTE_HOST:3100
```

## Scenario Quick Reference

| Scenario | Make Target | Duration | TPS | Tenants | Pre-requisite |
|----------|-------------|----------|-----|---------|---------------|
| A Smoke | `bench-smoke` | 1 min | 10 | 1 | Base seed |
| B Sustained | `bench-sustained` | 12 min | 580 | 50 | Base seed |
| C Peak | `bench-peak` | 7 min | 5,000 | 50 | Base seed |
| D Soak | `bench-soak` | 1 hr | 580 | 50 | Base seed |
| E Spike | `bench-spike` | 3 min | 100→5K→100 | 50 | Base seed |
| F HotSpot | `bench-hotspot` | 6 min | 580 | 10 | Base seed |
| G Scale 20K | `bench-tenants-20k` | 12 min | 580 | 20,000 | seed-20k |
| H Scale 100K | `bench-tenants-100k` | 12 min | 580 | 100,000 | seed-100k |
| I Scale+Peak | `bench-tenants-peak` | 7 min | 5,000 | 20,000 | seed-20k |
| J Settlement | `bench-settlement` | 1+ hr | 580 | 20,000 | seed-20k |
| Micro | `bench-micro` | 2 min | — | — | None |
| All | `bench-all` | ~30 min | — | — | Base seed |
| Report | `bench-report` | instant | — | — | Results exist |
