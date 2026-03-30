# Demo Pre-flight Checklist

This document covers everything you need to run a successful Settla demo, from hardware requirements to backup plans.

## Hardware Requirements

| Profile | Tenants | CPU | RAM | Disk | Suited for |
|---------|---------|-----|-----|------|------------|
| **Quick** | 10 | 4 cores | 8 GB | 20 GB | MacBook Pro laptop demos |
| **Scale** | 20,000 | 16+ cores | 64 GB | 100 GB | Cloud instance or staging server |
| **Stress** | 100,000 | 32+ cores | 128 GB | 500 GB | Dedicated staging cluster |

**Docker Desktop settings** (macOS): Settings > Resources > set Memory to at least 8 GB (12 GB recommended for scale demos).

## Software Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Docker Desktop | 4.x+ | [docker.com](https://www.docker.com/products/docker-desktop/) |
| Go | 1.25+ | `brew install go` |
| psql | 16+ | `brew install libpq && brew link --force libpq` |
| curl | any | (pre-installed on macOS) |
| jq | any | `brew install jq` |
| migrate CLI | latest | `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest` |
| asciinema (optional) | any | `brew install asciinema` |

## Network Requirements

All services run locally in Docker. No external network access required unless demonstrating blockchain features (requires RPC access).

**Ports that must be free:**

| Port | Service |
|------|---------|
| 3000 | Grafana |
| 8080 | Gateway API |
| 3003 | Metabase |
| 5433-5435 | PostgreSQL (ledger, transfer, treasury) |
| 6433-6435 | PgBouncer |
| 6060 | pprof |
| 8081 | settla-server HTTP |
| 8222 | NATS monitoring |
| 9090 | Prometheus |
| 9091 | settla-server gRPC |
| 9095 | Mock Provider |

Check for conflicts: `lsof -i -P | grep -E '(3000|8080|9090|9095)' | grep LISTEN`

## Quick Demo: 30 Minutes Before

```bash
# 1. Verify Docker is running
docker info >/dev/null 2>&1 || echo "Start Docker Desktop first!"

# 2. Start the demo environment
make demo-up
# Expected: ~5 minutes to build, seed, and start

# 3. Verify all services are healthy
make demo-status
# Expected: all services show UP

# 4. Open key URLs in browser tabs
open http://localhost:3000    # Grafana (admin / settla-dev-local)
open http://localhost:8080/docs  # API documentation
open http://localhost:9095/admin/config  # Mock provider control

# 5. Run a smoke test
curl -s http://localhost:8080/health | jq .
# Expected: {"status":"ok"}
```

## Scale Demo: 1 Hour Before

```bash
# 1. Increase Docker memory to 12+ GB in Docker Desktop settings

# 2. Start with scale profile
make demo-up-scale
# Expected: ~8 minutes (building + seeding 20K tenants)

# 3. Verify tenant count
make demo-scale-check
# Expected: 20,002 tenants (20K + Lemfi + Fincra)

# 4. Verify Grafana dashboards load correctly
open http://localhost:3000/d/tenant-scale
# Expected: tenant-scale dashboard with 20K tenants visible

# 5. Run a quick load test to warm caches
make bench-smoke
# Expected: passes within 60 seconds

# 6. Check memory baseline
docker stats --no-stream
# Expected: settla-server < 1.5 GB, settla-node < 1.5 GB
```

## Mock Provider Scenarios (Resilience Demo)

During the resilience portion of the demo (Act 6 in DEMO_SCRIPT.md), use these curl commands to dynamically control provider behavior.

### Simulate Provider Outage
```bash
# Fail a specific provider
curl -sX POST http://localhost:9095/admin/scenarios/provider-outage \
  -H 'Content-Type: application/json' \
  -d '{"provider": "mock-onramp-gbp"}'

# Fail ALL providers (global outage)
curl -sX POST http://localhost:9095/admin/scenarios/provider-outage
```

### Simulate High Latency
```bash
# Default: 5000ms latency
curl -sX POST http://localhost:9095/admin/scenarios/high-latency

# Custom latency
curl -sX POST http://localhost:9095/admin/scenarios/high-latency \
  -H 'Content-Type: application/json' \
  -d '{"latency_ms": 10000}'
```

### Simulate Partial Failures
```bash
# Default: 30% error rate
curl -sX POST http://localhost:9095/admin/scenarios/partial-failure

# Custom error rate
curl -sX POST http://localhost:9095/admin/scenarios/partial-failure \
  -H 'Content-Type: application/json' \
  -d '{"error_rate": 0.5}'
```

### Simulate Deposit Detection
```bash
# Queue a mock on-chain deposit for a watched address
curl -sX POST http://localhost:9095/admin/scenarios/simulate-deposit \
  -H 'Content-Type: application/json' \
  -d '{"address": "TXyz123...", "amount": "500.00", "token": "USDT", "chain": "tron"}'

# The chain monitor will detect this on its next poll cycle
```

### Reset to Normal
```bash
curl -sX POST http://localhost:9095/admin/reset
```

### Check Current Config
```bash
curl -s http://localhost:9095/admin/config | jq .
```

### View Request Logs
```bash
curl -s http://localhost:9095/admin/logs | jq '.[-5:]'
```

## Backup Plans

### If Docker Compose fails to start

1. Check Docker Desktop is running: `docker info`
2. Check available resources: Docker Desktop > Settings > Resources
3. Free up ports: `lsof -i :3000 -i :9090 -i :9095 | grep LISTEN`
4. Try a clean restart: `make demo-down && make demo-up`
5. Check logs: `make demo-logs`

### If seeding fails

1. Check database connectivity: `psql postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable -c "SELECT 1"`
2. Run migrations manually: `migrate -path db/migrations/transfer -database "postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable" up`
3. Seed manually: `go run scripts/demo-seed.go --tenant-count=10 --verbose`

### If a service won't come healthy

1. Check its logs: `bash scripts/demo-logs.sh --service=settla-server`
2. Restart just that service: `docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.demo.yml restart settla-server`
3. Check resource usage: `docker stats --no-stream`

### Staging fallback

If the local demo environment cannot be started, use the staging environment:
- Gateway: https://demo.settla.dev (if configured)
- Grafana: https://grafana.demo.settla.dev (if configured)

## Common Issues

| Issue | Symptom | Fix |
|-------|---------|-----|
| **Port conflict** | `bind: address already in use` | Kill the process using the port: `kill $(lsof -ti :PORT)` |
| **Docker memory** | OOM kill, containers restarting | Increase Docker Desktop memory to 12+ GB |
| **TigerBeetle privileged** | TigerBeetle fails to start | Ensure `privileged: true` is set (required for io_uring on Docker Desktop) |
| **PgBouncer exhaustion** | `too many clients` errors | Restart PgBouncer: `docker compose restart pgbouncer-transfer` |
| **Slow 20K seed** | Seeding takes >5 minutes | Check disk I/O; use SSD; increase Docker disk allocation |
| **NATS connection refused** | Workers can't connect | Check NATS health: `curl http://localhost:8222/healthz`; restart NATS |
| **Migration dirty** | `Dirty database version X` | Force version: `migrate -path db/migrations/transfer -database "$URL" force X` |
| **Go build fails** | Missing dependencies | Run `go mod download` from project root |
| **Script permission denied** | `Permission denied` on scripts | Run `chmod +x scripts/demo-*.sh` |

## Demo Scripts Quick Reference

```bash
make demo-up              # Start everything (quick profile)
make demo-up-scale        # Start with 20K tenants
make demo-down            # Stop and remove all volumes
make demo-reset           # Reset data without restarting
make demo-status          # Check health of all services
make demo-seed-quick      # Re-seed with 10 tenants
make demo-seed-scale      # Re-seed with 20K tenants
make demo-logs            # Tail application logs
make demo-scale-check     # Verify scale readiness
make demo-record          # Record terminal session
make demo                 # Run integration test scenarios
```
