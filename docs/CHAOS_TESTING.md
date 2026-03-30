# Chaos Testing Runbook

> Last updated: 2026-03-29
>
> This document describes how to run chaos tests safely, interpret results, and respond to failures. It covers both the automated Go chaos test suite (`tests/chaos/`) and manual ad-hoc failure injection.

---

## Table of Contents

1. [Pre-Flight Checklist](#pre-flight-checklist)
2. [Running the Automated Suite](#running-the-automated-suite)
3. [Scenario Reference (20 Scenarios)](#scenario-reference)
4. [Order of Operations](#order-of-operations)
5. [Emergency Stop Procedure](#emergency-stop-procedure)
6. [Interpreting Results](#interpreting-results)
7. [Manual Fault Injection Recipes](#manual-fault-injection-recipes)
8. [Toxiproxy Setup for Latency Injection](#toxiproxy-setup)
9. [Environment Requirements](#environment-requirements)

---

## Pre-Flight Checklist

Before running any chaos tests, complete every item:

- [ ] **Staging environment only** — never run chaos tests against production
- [ ] **No real traffic** — confirm no real tenants are routed to the staging cluster
- [ ] **Monitoring active** — Grafana dashboards accessible, Prometheus scraping, alerts routing to test channel
- [ ] **Database backups fresh** — take a snapshot of Transfer DB, Ledger DB, Treasury DB (if running on persistent staging)
- [ ] **Seed data loaded** — at minimum: `make db-seed` (Lemfi + Fincra tenants). For scale scenarios: `scripts/demo-seed.sh scale` (20K tenants)
- [ ] **All services healthy** — verify:
  ```bash
  curl -s http://localhost:3100/health  # gateway
  curl -s http://localhost:3100/ready   # gateway readiness (checks gRPC + Redis)
  curl -s http://localhost:8080/health  # settla-server
  ```
- [ ] **Docker Compose up** — `make docker-up` (or `docker compose -f deploy/docker-compose.yml up -d`)
- [ ] **NATS streams created** — verify at `http://localhost:8222/jsz?streams=true`
- [ ] **Git state clean** — no uncommitted changes that could interfere with restarts
- [ ] **Team notified** — post in #engineering that chaos tests are running

---

## Running the Automated Suite

### Quick Start

```bash
# Run all 8 built-in scenarios
make chaos

# Or directly:
go run ./tests/chaos/ \
  -compose deploy/docker-compose.yml \
  -env .env \
  -gateway http://localhost:3100 \
  -server http://localhost:8080 \
  -tps 500
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-compose` | `deploy/docker-compose.yml` | Docker Compose file path |
| `-env` | `.env` | Environment file |
| `-gateway` | `http://localhost:3100` | Gateway URL |
| `-server` | `http://localhost:8080` | Server URL |
| `-tps` | `500` | Background TPS during chaos |

### Built-in Scenarios (from `tests/chaos/`)

The automated suite runs 8 scenarios sequentially, with health checks between each:

1. **TigerBeetle Restart** — `docker compose restart tigerbeetle`
2. **Postgres Pause** — `docker compose pause postgres-ledger` for 10s
3. **NATS Restart** — `docker compose restart nats`
4. **Redis Failure** — `docker compose stop redis` for 15s
5. **Server Crash** — `docker compose kill -s SIGKILL settla-server`
6. **PgBouncer Saturation** — 2000 TPS for 90s
7. **Outbox Relay Interruption** — `docker compose kill -s SIGKILL settla-node` for 15s
8. **Worker Node Restart** — `docker compose restart settla-node`

Each scenario verifies three invariants:
- **Outbox drained**: All entries published within 60s of recovery
- **Money conservation**: sum(DEBIT) == sum(CREDIT) in entry_lines
- **No stuck transfers**: Zero non-terminal transfers older than 30 minutes

---

## Scenario Reference

### Scenario 1: Transfer DB Down

**ID**: `CHAOS-01` | **Severity if fails**: P0 | **Estimated duration**: 3 min

**What it tests**: The system's behavior when the primary data store is completely unavailable.

**Setup script**:
```bash
# Kill PgBouncer (simulates complete DB unavailability from app perspective)
docker compose -f deploy/docker-compose.yml kill -s SIGKILL pgbouncer-transfer

# Alternative: kill Postgres directly
docker compose -f deploy/docker-compose.yml stop postgres-transfer
```

**Verification script**:
```bash
# 1. API should return 503 for mutations
curl -s -o /dev/null -w '%{http_code}' \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-01","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: 503

# 2. Health check should fail
curl -s http://localhost:3100/ready | jq .
# Expected: {"status":"degraded","dependencies":{"grpc":"unhealthy",...}}

# 3. After recovery, verify no data loss
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c \
  "SELECT COUNT(*) as stuck FROM transfers WHERE status NOT IN ('COMPLETED','FAILED','REFUNDED') AND created_at > now() - interval '30 minutes';"
# Expected: 0 stuck transfers
```

**Teardown script**:
```bash
docker compose -f deploy/docker-compose.yml up -d pgbouncer-transfer
# Wait for health
sleep 10
curl -sf http://localhost:3100/ready
```

**Pass/fail criteria**:
- PASS: API returns 503 during outage, recovers within 30s of PgBouncer restart, zero stuck transfers, money balanced
- FAIL: Data loss, partial writes, recovery > 60s, stuck transfers after 5 minutes

**Blast radius**: All API mutations fail. Reads from cache continue briefly. No data corruption.
**Recovery time**: < 30s (PgBouncer restart), 2-5 min (Postgres failover)

---

### Scenario 2: TigerBeetle Down

**ID**: `CHAOS-02` | **Severity if fails**: P0 | **Estimated duration**: 3 min

**What it tests**: Ledger write path failure. LedgerWorker should queue in NATS and resume on recovery.

**Setup script**:
```bash
docker compose -f deploy/docker-compose.yml kill -s SIGKILL tigerbeetle
```

**Verification script**:
```bash
# 1. New transfers still accepted (engine writes to outbox, TB not needed yet)
curl -s -w '\n%{http_code}' \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-02-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: 201 (transfer created, ledger post queued in NATS)

# 2. LedgerWorker circuit breaker should be open
curl -s http://localhost:8080/metrics | grep 'settla_circuit_breaker_state.*tigerbeetle'
# Expected: value = 1 (open)

# 3. After TB restart, verify money conservation
docker compose -f deploy/docker-compose.yml up -d tigerbeetle
sleep 30
docker compose -f deploy/docker-compose.yml exec postgres-ledger \
  psql -U settla -d settla_ledger -t -c \
  "SELECT COALESCE(SUM(CASE WHEN entry_type='DEBIT' THEN amount ELSE 0 END),0), COALESCE(SUM(CASE WHEN entry_type='CREDIT' THEN amount ELSE 0 END),0) FROM entry_lines;"
# Expected: debits == credits
```

**Teardown script**:
```bash
docker compose -f deploy/docker-compose.yml up -d tigerbeetle
sleep 15
curl -sf http://localhost:8080/health
```

**Pass/fail criteria**:
- PASS: Transfers still created during TB outage, NATS queues ledger intents, all posted after recovery, money balanced
- FAIL: Transfers rejected during TB outage (engine shouldn't need TB), money imbalanced after recovery

**Blast radius**: Ledger posts delayed. Transfers stall after FUNDED state. No data loss.
**Recovery time**: < 30s

---

### Scenario 3: NATS Down

**ID**: `CHAOS-03` | **Severity if fails**: P1 | **Estimated duration**: 3 min

**What it tests**: Outbox accumulates when NATS is unavailable, drains correctly on recovery with no duplicates.

**Setup script**:
```bash
docker compose -f deploy/docker-compose.yml stop nats
```

**Verification script**:
```bash
# 1. Transfers still created (engine writes to DB + outbox atomically)
# 2. Outbox backlog should grow
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count < max_retries;"
# Expected: growing count

# 3. After NATS restart, outbox should drain
docker compose -f deploy/docker-compose.yml start nats
sleep 30
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count < max_retries;"
# Expected: 0

# 4. Verify no duplicate messages (money conservation)
docker compose -f deploy/docker-compose.yml exec postgres-ledger \
  psql -U settla -d settla_ledger -t -c \
  "SELECT COALESCE(SUM(CASE WHEN entry_type='DEBIT' THEN amount ELSE 0 END),0), COALESCE(SUM(CASE WHEN entry_type='CREDIT' THEN amount ELSE 0 END),0) FROM entry_lines;"
```

**Teardown script**:
```bash
docker compose -f deploy/docker-compose.yml up -d nats
sleep 15
# Verify streams exist
curl -s http://localhost:8222/jsz?streams=true | jq '.streams | length'
# Expected: 12
```

**Pass/fail criteria**:
- PASS: Outbox drains within 60s of NATS recovery, zero duplicates (NATS dedup via Nats-Msg-Id), money balanced
- FAIL: Outbox doesn't drain, duplicate messages cause double-credits, stuck transfers

**Blast radius**: No side effects execute (no provider calls, no ledger posts). API still accepts transfers.
**Recovery time**: < 30s (NATS restart + outbox drain)

---

### Scenario 4: Redis Down

**ID**: `CHAOS-04` | **Severity if fails**: P1 | **Estimated duration**: 3 min

**What it tests**: Auth degrades to L3 (DB), rate limiting falls back to local-only, system continues.

**Setup script**:
```bash
docker compose -f deploy/docker-compose.yml stop redis
```

**Verification script**:
```bash
# 1. Gateway readiness should report Redis degraded
curl -s http://localhost:3100/ready | jq .
# Expected: {"status":"degraded","dependencies":{"redis":"unhealthy",...}}

# 2. Transfers should still work (auth from L1 cache or L3 DB fallback)
curl -s -w '\n%{http_code}' \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-04-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: 201 (transfer created)

# 3. Rate limiting should still work (local-only mode)
for i in $(seq 1 25); do
  curl -s -o /dev/null -w '%{http_code} ' \
    -X POST http://localhost:3100/v1/transfers \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
    -d '{"idempotency_key":"chaos-04-rate-'$i'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
done
echo
# Expected: mostly 201s (local rate limiter is more permissive, but still present)
```

**Teardown script**:
```bash
docker compose -f deploy/docker-compose.yml start redis
sleep 5
curl -s http://localhost:3100/ready | jq .status
# Expected: "ok"
```

**Pass/fail criteria**:
- PASS: Transfers succeed during Redis outage, auth resolves via L1/L3, rate limiting active (local), no data loss
- FAIL: All requests rejected, auth completely fails, data loss

**Blast radius**: Minimal — Redis is a cache layer. Slightly higher DB load for auth lookups.
**Recovery time**: Instant (L1 serves cached keys), full cache rebuild 2-5 min

---

### Scenario 5: Provider API Slow (5s Latency)

**ID**: `CHAOS-05` | **Severity if fails**: P1 | **Estimated duration**: 5 min

**What it tests**: Circuit breaker trips on slow provider, transfers fail fast, other tenants unaffected.

**Setup script** (requires toxiproxy or mock provider):
```bash
# Option A: Using mock provider (deploy/docker-compose.demo.yml)
# Configure mock provider to add 5s delay
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"latency_ms": 5000}'

# Option B: Using toxiproxy (if available)
# Create proxy for provider endpoint
toxiproxy-cli create provider_slow -l 0.0.0.0:9998 -u provider:443
toxiproxy-cli toxic add provider_slow -t latency -a latency=5000
```

**Verification script**:
```bash
# 1. ProviderWorker timeout budget (30s total) should accommodate slow calls initially
# 2. After 15 failures, circuit breaker should trip
curl -s http://localhost:8080/metrics | grep 'settla_circuit_breaker_state.*provider.*onramp'
# Expected: value = 1 (open) after ~15 failed calls

# 3. Subsequent transfers should fail fast (no 30s wait)
time curl -s -w '\n%{http_code}' \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-05-fast-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: fast response (< 1s), not 30s timeout

# 4. Verify other tenant (Fincra) is unaffected (per-provider CB isolation)
# If using different provider routes per tenant
```

**Teardown script**:
```bash
# Reset mock provider
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"latency_ms": 0}'

# Or remove toxiproxy toxic
toxiproxy-cli toxic remove provider_slow -n latency_downstream
```

**Pass/fail criteria**:
- PASS: CB trips after 15 failures, fail-fast < 1s for subsequent requests, other tenants unaffected, compensation triggers
- FAIL: Requests hang for 30s, CB doesn't trip, cascading failure to other tenants

**Blast radius**: Single provider affected. Fallback routing to alternatives. Per-tenant isolation maintained.
**Recovery time**: CB reset timeout (10s) after provider recovers

---

### Scenario 6: Provider API Returning Errors

**ID**: `CHAOS-06` | **Severity if fails**: P1 | **Estimated duration**: 4 min

**What it tests**: Retry exhaustion, transfer failure, and compensation flow.

**Setup script**:
```bash
# Mock provider returns 500 for all requests
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"error_rate": 1.0, "error_code": 500}'
```

**Verification script**:
```bash
# 1. Create transfer
TRANSFER=$(curl -s \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-06-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}')
TRANSFER_ID=$(echo $TRANSFER | jq -r '.id')

# 2. Wait for retries to exhaust (3 attempts × 200ms backoff = ~2s)
sleep 15

# 3. Check transfer status — should be FAILED
curl -s http://localhost:3100/v1/transfers/$TRANSFER_ID \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' | jq .status
# Expected: "FAILED"

# 4. Check retry exhaustion metric
curl -s http://localhost:8080/metrics | grep 'settla_retry_exhausted_total'

# 5. After NATS DLQ processing, verify compensation
curl -s http://localhost:8080/metrics | grep 'settla_dlq_messages_total'
```

**Teardown script**:
```bash
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"error_rate": 0.0}'
```

**Pass/fail criteria**:
- PASS: Retries exhaust (3 attempts), transfer → FAILED, compensation flow triggered, no money lost
- FAIL: Infinite retries, transfer stuck, money locked without compensation

**Blast radius**: Affected provider's transfers fail. Other providers continue normally.
**Recovery time**: N/A (automatic failure handling)

---

### Scenario 7: Blockchain RPC Down

**ID**: `CHAOS-07` | **Severity if fails**: P1 | **Estimated duration**: 4 min

**What it tests**: Deposit detection pauses, resumes on recovery, no missed deposits.

**Setup script**:
```bash
# Block RPC endpoint (requires network access)
# Option A: iptables (Linux)
sudo iptables -A OUTPUT -d <rpc-host> -j DROP

# Option B: Docker network disconnect
docker network disconnect settla-net <rpc-container>

# Option C: Mock RPC returns connection refused
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"blockchain_rpc_error": true}'
```

**Verification script**:
```bash
# 1. Chain monitor block lag should increase
curl -s http://localhost:8080/metrics | grep 'settla_chain_monitor_block_lag'
# Expected: increasing value

# 2. BlockchainWorker circuit breaker should trip (3 failures, 60s reset)
curl -s http://localhost:8080/metrics | grep 'settla_circuit_breaker_state.*blockchain'
# Expected: value = 1 (open)

# 3. Existing deposit sessions should remain in PENDING (not lost)
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT status, COUNT(*) FROM crypto_deposit_sessions GROUP BY status;"

# 4. After recovery, chain monitor resumes from checkpoint (no missed blocks)
# Restore RPC access, then check block lag decreasing
```

**Teardown script**:
```bash
# Restore RPC access
sudo iptables -D OUTPUT -d <rpc-host> -j DROP
# Or reconnect Docker network
docker network connect settla-net <rpc-container>
# Or reset mock
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"blockchain_rpc_error": false}'
```

**Pass/fail criteria**:
- PASS: Detection pauses cleanly, CB trips, resumes from correct checkpoint on recovery, no missed deposits
- FAIL: Missed deposits after recovery, stuck deposit sessions, CB doesn't reset

**Blast radius**: New deposit detection paused. Existing confirmed deposits unaffected. Transfer processing unaffected.
**Recovery time**: < 60s (CB reset) + catch-up time for missed blocks

---

### Scenario 8: Network Partition (Server ↔ Node)

**ID**: `CHAOS-08` | **Severity if fails**: P1 | **Estimated duration**: 4 min

**What it tests**: Outbox accumulates on server side, node processes backlog on heal.

**Setup script**:
```bash
# Option A: Docker network manipulation
# Disconnect node from the shared network
docker network disconnect settla-net settla-node

# Option B: iptables between specific containers
# Get container IPs first
SERVER_IP=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' settla-server)
NODE_IP=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' settla-node)
# Block communication
docker exec settla-node iptables -A INPUT -s $SERVER_IP -j DROP
docker exec settla-node iptables -A OUTPUT -d $SERVER_IP -j DROP
```

**Verification script**:
```bash
# 1. API still accepts transfers (server can write to local DB)
curl -s -w '\n%{http_code}' \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-08-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: 201

# 2. Outbox backlog grows (relay can't reach NATS or DB?)
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM outbox WHERE published = false;"

# 3. After healing, outbox drains
docker network connect settla-net settla-node
sleep 30
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count < max_retries;"
# Expected: 0
```

**Teardown script**:
```bash
docker network connect settla-net settla-node
# Or remove iptables rules
docker exec settla-node iptables -F
sleep 15
```

**Pass/fail criteria**:
- PASS: Outbox accumulates, drains on heal, no duplicates, money balanced
- FAIL: Data loss, permanent stall after heal, duplicates

**Blast radius**: No side effects execute during partition. Transfers stall in intermediate states.
**Recovery time**: Outbox drain time (typically < 10s for backlog)

---

### Scenario 9: Memory Pressure

**ID**: `CHAOS-09` | **Severity if fails**: P1 | **Estimated duration**: 5 min

**What it tests**: System degrades gracefully under memory pressure, no OOM kill.

**Setup script**:
```bash
# Apply memory pressure to settla-server container
docker exec settla-server stress --vm 1 --vm-bytes 90% --timeout 120s &
STRESS_PID=$!
```

**Verification script**:
```bash
# 1. Monitor RSS growth
docker stats settla-server --no-stream --format '{{.MemUsage}}'

# 2. Check if load shedder activates
curl -s http://localhost:8080/metrics | grep 'settla_load_shedding_rejected_total'

# 3. Verify service still responding (even if degraded)
curl -s -w '\n%{http_code}' http://localhost:8080/health
# Expected: 200 (still alive)

# 4. Verify treasury in-memory state survives
curl -s http://localhost:8080/metrics | grep 'settla_treasury_flush_duration'
```

**Teardown script**:
```bash
docker exec settla-server pkill stress || true
sleep 10
curl -sf http://localhost:8080/health
```

**Pass/fail criteria**:
- PASS: Service degrades gracefully (load shedding), no OOM kill, recovers after pressure removed
- FAIL: OOM kill, treasury state lost, permanent degradation

**Blast radius**: Higher latency, some requests rejected via load shedding
**Recovery time**: Immediate once pressure removed

---

### Scenario 10: NATS Disk Full

**ID**: `CHAOS-10` | **Severity if fails**: P1 | **Estimated duration**: 5 min

**What it tests**: NATS DiscardOld policy drops oldest messages, alerts fire, recovery detector catches affected transfers.

**Setup script**:
```bash
# Fill NATS data directory
NATS_CONTAINER=$(docker compose -f deploy/docker-compose.yml ps -q nats)
docker exec $NATS_CONTAINER dd if=/dev/zero of=/data/filler bs=1M count=4096 2>/dev/null || true
```

**Verification script**:
```bash
# 1. Check NATS stream stats for message drops
curl -s http://localhost:8222/jsz?streams=true | jq '.streams[] | {name: .name, messages: .state.messages, bytes: .state.bytes}'

# 2. Monitor for discarded messages
curl -s http://localhost:8222/jsz?streams=true | jq '.streams[] | select(.state.num_deleted > 0)'

# 3. After cleanup, verify recovery detector catches stuck transfers
sleep 120  # Wait for recovery detector cycle (60s)
curl -s http://localhost:8080/metrics | grep 'settla_stuck_transfers'
curl -s http://localhost:8080/metrics | grep 'settla_recovery_attempts_total'
```

**Teardown script**:
```bash
NATS_CONTAINER=$(docker compose -f deploy/docker-compose.yml ps -q nats)
docker exec $NATS_CONTAINER rm -f /data/filler
docker compose -f deploy/docker-compose.yml restart nats
sleep 15
```

**Pass/fail criteria**:
- PASS: DiscardOld policy activates, oldest messages dropped, recovery detector catches affected transfers within 2 cycles
- FAIL: NATS crashes, unrecoverable data loss, stuck transfers not detected

**Blast radius**: Oldest messages in each stream dropped. Recent messages preserved.
**Recovery time**: Recovery detector cycle (60s) catches affected transfers

---

### Scenario 11: Clock Skew

**ID**: `CHAOS-11` | **Severity if fails**: P2 | **Estimated duration**: 5 min

**What it tests**: Transfer timestamps still valid, TTLs work, settlement scheduler runs correctly.

**Setup script**:
```bash
# Skew settla-server clock forward 5 minutes
docker exec settla-server date -s "$(date -u -d '+5 minutes' '+%Y-%m-%d %H:%M:%S')" 2>/dev/null || \
docker exec settla-server bash -c 'faketime "+5m" sleep infinity &'

# Note: Docker containers typically share host clock. May need faketime or libfaketime.
# Alternative: modify application config for time offset
```

**Verification script**:
```bash
# 1. Create transfer, verify timestamp is reasonable
RESP=$(curl -s -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-11-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}')
echo $RESP | jq .created_at
# Verify within reasonable range

# 2. Check NATS dedup still works (5-min window)
# If clock skewed +5min, messages from "future" may bypass dedup incorrectly

# 3. Verify settlement scheduler window calculation
curl -s http://localhost:8080/metrics | grep 'settla_settlement_last_success'

# 4. Check idempotency key TTLs
# Replay same idempotency key — should still be rejected
curl -s -w '\n%{http_code}' -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-11-duplicate","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: 409 (duplicate)
```

**Teardown script**:
```bash
# Reset clock (restart container is simplest)
docker compose -f deploy/docker-compose.yml restart settla-server
sleep 15
```

**Pass/fail criteria**:
- PASS: Transfers work with skewed clock, idempotency still enforced, settlement scheduler adapts
- FAIL: Duplicate processing due to broken dedup, settlement skips window

**Blast radius**: Minimal if within NATS dedup window (5 min). Larger skew may cause dedup failures.
**Recovery time**: Immediate on clock correction

---

### Scenario 12: Slow NATS Consumer

**ID**: `CHAOS-12` | **Severity if fails**: P1 | **Estimated duration**: 5 min

**What it tests**: Backpressure to NATS, MaxAckPending reached, no message loss.

**Setup script**:
```bash
# Inject artificial delay in a worker by overloading the downstream dependency
# Option: Slow down Transfer DB responses for worker queries
docker compose -f deploy/docker-compose.yml exec pgbouncer-transfer \
  psql -U settla -p 6432 -c "SET statement_timeout = '5000';" 2>/dev/null || true

# Alternative: Generate very high load to create natural backpressure
go run ./tests/chaos/ -tps 3000 -gateway http://localhost:3100
```

**Verification script**:
```bash
# 1. Check NATS consumer pending messages
curl -s http://localhost:8222/jsz?consumers=true | \
  jq '.streams[].consumers[] | {stream: .stream_name, name: .name, pending: .num_pending, ack_pending: .num_ack_pending}'

# 2. Verify MaxAckPending is respected (no unbounded growth)
# MaxAckPending = max(100, poolSize * 2)

# 3. Verify no message loss — messages stay in stream until acked
curl -s http://localhost:8222/jsz?streams=true | \
  jq '.streams[] | {name: .name, messages: .state.messages}'

# 4. After backpressure clears, verify all messages processed
sleep 60
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count < max_retries;"
# Expected: 0
```

**Teardown script**:
```bash
# Reset DB timeout
docker compose -f deploy/docker-compose.yml restart pgbouncer-transfer
sleep 10
```

**Pass/fail criteria**:
- PASS: MaxAckPending limits in-flight messages, backlog queues in JetStream, no message loss, clears after slowdown resolves
- FAIL: Unbounded memory growth, messages dropped, consumer crash

**Blast radius**: Affected consumer type only. Other consumers on different streams unaffected.
**Recovery time**: Automatic when downstream recovers

---

### Scenario 13: PgBouncer Connection Exhaustion

**ID**: `CHAOS-13` | **Severity if fails**: P1 | **Estimated duration**: 4 min

**What it tests**: Graceful rejection when connection pool exhausted, no connection leak, recovery on capacity free.

**Setup script**:
```bash
# Temporarily reduce PgBouncer max connections
docker compose -f deploy/docker-compose.yml exec pgbouncer-transfer \
  bash -c 'echo "max_client_conn = 5" >> /etc/pgbouncer/pgbouncer.ini && kill -HUP 1'

# Generate load to exhaust the pool
for i in $(seq 1 20); do
  docker compose -f deploy/docker-compose.yml exec -d postgres-transfer \
    psql -U settla -d settla_transfer -c "SELECT pg_sleep(30);" &
done
```

**Verification script**:
```bash
# 1. New connections should be rejected gracefully
curl -s -w '\n%{http_code}' http://localhost:3100/v1/transfers \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' | tail -1
# Expected: 503 (not hang indefinitely)

# 2. Check PgBouncer stats
docker compose -f deploy/docker-compose.yml exec pgbouncer-transfer \
  psql -U settla -p 6432 -c "SHOW POOLS;"

# 3. After sleeping connections release, verify recovery
sleep 35  # Wait for pg_sleep(30) to complete
curl -s -w '\n%{http_code}' http://localhost:3100/health
# Expected: 200
```

**Teardown script**:
```bash
# Restore PgBouncer config
docker compose -f deploy/docker-compose.yml restart pgbouncer-transfer
sleep 10
```

**Pass/fail criteria**:
- PASS: Graceful 503 rejection, no connection leak, auto-recovery when connections free
- FAIL: Infinite hang, connection leak, permanent degradation

**Blast radius**: All Transfer DB operations fail during exhaustion
**Recovery time**: Immediate when connections release (transaction completion)

---

### Scenario 14: Concurrent Settlement Trigger

**ID**: `CHAOS-14` | **Severity if fails**: P0 | **Estimated duration**: 3 min

**What it tests**: Settlement idempotency — only one settlement runs per tenant per window.

**Setup script**:
```bash
# Trigger settlement from two processes simultaneously
# The settlement_idempotency table has UNIQUE(tenant_id, window_start, window_end)

# Process 1
curl -s -X POST http://localhost:8080/ops/settlement/trigger &
PID1=$!

# Process 2 (simultaneous)
curl -s -X POST http://localhost:8080/ops/settlement/trigger &
PID2=$!

wait $PID1 $PID2
```

**Verification script**:
```bash
# 1. Check settlement_idempotency table — should have exactly ONE entry per tenant per window
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c \
  "SELECT tenant_id, window_start, window_end, COUNT(*) FROM net_settlements GROUP BY tenant_id, window_start, window_end HAVING COUNT(*) > 1;"
# Expected: 0 rows (no duplicates)

# 2. Verify settlement amounts are correct (no double-calculation)
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c \
  "SELECT COUNT(*) as total, SUM(CASE WHEN status = 'COMPLETED' THEN 1 ELSE 0 END) as completed FROM net_settlements WHERE created_at > now() - interval '1 hour';"

# 3. Run reconciliation to verify consistency
curl -s -X POST http://localhost:8080/ops/reconciliation/run
```

**Teardown script**:
```bash
# No teardown needed — idempotency handles it
echo "No cleanup required"
```

**Pass/fail criteria**:
- PASS: Exactly one settlement per tenant per window, second attempt either no-ops or returns existing result
- FAIL: Duplicate settlements, double-counted amounts

**Blast radius**: None if idempotency works. P0 if it doesn't (double-settlement = financial loss).
**Recovery time**: N/A

---

### Scenario 15: Mid-Transfer Crash

**ID**: `CHAOS-15` | **Severity if fails**: P0 | **Estimated duration**: 5 min

**What it tests**: On restart, outbox replays, transfer resumes from last state, no corruption.

This scenario is covered by the automated suite (Scenario 5: Server Crash and Scenario 7: Outbox Relay Interruption). Key verification:

**Setup script**:
```bash
# Generate steady load
go run ./tests/chaos/ -tps 200 &
LOAD_PID=$!
sleep 30  # Let transfers enter various states

# SIGKILL the server mid-transaction
docker compose -f deploy/docker-compose.yml kill -s SIGKILL settla-server

# Wait 5 seconds (transfers in DB, outbox entries written atomically)
sleep 5

# Restart
docker compose -f deploy/docker-compose.yml up -d settla-server
```

**Verification script**:
```bash
# 1. Wait for health
sleep 15
curl -sf http://localhost:8080/health

# 2. Verify outbox drains (relay resumes from last unpublished)
sleep 30
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count < max_retries;"
# Expected: 0

# 3. Verify no stuck transfers (recovery detector catches any)
sleep 120  # Wait for recovery detector cycle
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM transfers WHERE status NOT IN ('COMPLETED','FAILED','REFUNDED') AND created_at > now() - interval '30 minutes';"
# Expected: 0 (or decreasing toward 0)

# 4. Money conservation
docker compose -f deploy/docker-compose.yml exec postgres-ledger \
  psql -U settla -d settla_ledger -t -c \
  "SELECT COALESCE(SUM(CASE WHEN entry_type='DEBIT' THEN amount ELSE 0 END),0) as debits, COALESCE(SUM(CASE WHEN entry_type='CREDIT' THEN amount ELSE 0 END),0) as credits FROM entry_lines;"
# Expected: debits == credits
```

**Pass/fail criteria**:
- PASS: Outbox drains, transfers resume, money balanced, no corruption, recovery < 30s
- FAIL: Stuck transfers, money imbalance, outbox stuck

**Blast radius**: All in-flight requests fail during crash. Recovery is automatic.
**Recovery time**: < 30s

---

### Scenario 16: Tenant Onboarding Burst

**ID**: `CHAOS-16` | **Severity if fails**: P2 | **Estimated duration**: 5 min

**What it tests**: System handles bulk tenant creation without auth cache thrashing or existing tenant degradation.

**Setup script**:
```bash
# Create 1,000 tenants in 60 seconds via API
for i in $(seq 1 1000); do
  curl -s -X POST http://localhost:8080/ops/tenants \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Chaos Tenant $i\",\"slug\":\"chaos-$i\",\"settlement_mode\":\"NET_SETTLEMENT\"}" &

  # Rate limit: ~17 per second
  if (( $i % 17 == 0 )); then sleep 1; fi
done
wait
```

**Verification script**:
```bash
# 1. Verify existing tenant traffic unaffected during onboarding
# Run parallel transfers for Lemfi while onboarding runs
time curl -s -w '%{http_code}' \
  -X POST http://localhost:3100/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key' \
  -d '{"idempotency_key":"chaos-16-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
# Expected: 201 in < 500ms

# 2. Check auth cache metrics
curl -s http://localhost:8080/metrics | grep 'settla_auth_cache'
# L1 hit rate should remain high for existing tenants

# 3. Verify all tenants created
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -t -c \
  "SELECT COUNT(*) FROM tenants WHERE slug LIKE 'chaos-%';"
# Expected: 1000
```

**Teardown script**:
```bash
# Clean up chaos tenants
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c \
  "DELETE FROM tenants WHERE slug LIKE 'chaos-%';"
```

**Pass/fail criteria**:
- PASS: All 1,000 tenants created, existing tenant latency < 500ms, auth cache stable
- FAIL: Existing tenant latency spikes > 2s, cache thrashing, onboarding failures

**Blast radius**: Minimal — new tenant creation is independent of existing tenant operations
**Recovery time**: N/A

---

### Scenario 17: Hot Tenant + Cold Tenant Fairness

**ID**: `CHAOS-17` | **Severity if fails**: P1 | **Estimated duration**: 10 min

**What it tests**: One tenant generating 80% of traffic at 580 TPS doesn't starve cold tenants.

**Setup script**:
```bash
# Run hot tenant load (Lemfi at 464 TPS = 80% of 580)
go run ./tests/loadtest/ \
  -scenario hotspot \
  -tps 580 \
  -hot-tenant-ratio 0.8 \
  -duration 5m &
LOAD_PID=$!
```

**Verification script**:
```bash
# 1. Measure cold tenant (Fincra) response time during hot tenant pressure
for i in $(seq 1 10); do
  time curl -s -o /dev/null -w '%{http_code}' \
    -X POST http://localhost:3100/v1/transfers \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer sk_live_fincra_demo_key' \
    -d '{"idempotency_key":"chaos-17-cold-'$(uuidgen)'","source_currency":"GBP","source_amount":"100","dest_currency":"NGN","sender":{"name":"Test","email":"t@t.com","country":"GB"},"recipient":{"name":"Test","country":"NG"}}'
  sleep 1
done
# Expected: All < 200ms, 201 status

# 2. Check per-tenant metrics
curl -s http://localhost:8080/metrics | grep 'settla_tps.*tenant'

# 3. Verify NATS partition distribution (8 partitions by tenant hash)
curl -s http://localhost:8222/jsz?consumers=true | \
  jq '.streams[0].consumers[] | {name: .name, pending: .num_pending}'
# Partitions should not be severely skewed
```

**Teardown script**:
```bash
kill $LOAD_PID 2>/dev/null || true
```

**Pass/fail criteria**:
- PASS: Cold tenant p99 < 200ms, NATS partitions reasonably balanced, no starvation
- FAIL: Cold tenant > 1s latency, starvation, unfair partition distribution

**Blast radius**: Hot tenant may experience rate limiting. Cold tenants should be unaffected.
**Recovery time**: Immediate once hot tenant load drops

---

### Scenario 18: Settlement at 20K Tenants

**ID**: `CHAOS-18` | **Severity if fails**: P1 | **Estimated duration**: 2.5 hours

**What it tests**: Settlement completes for all 20K tenants within 2 hours, no tenant skipped.

**Setup script**:
```bash
# Seed 20K tenants
scripts/demo-seed.sh scale  # Creates 20K tenants

# Generate some transfer volume per tenant
go run ./tests/loadtest/ -scenario tenants-20k -tps 580 -duration 10m

# Trigger settlement
curl -s -X POST http://localhost:8080/ops/settlement/trigger
```

**Verification script**:
```bash
# 1. Monitor settlement progress
watch -n 30 'curl -s http://localhost:8080/metrics | grep settla_settlement'

# 2. After completion, verify all tenants processed
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c \
  "SELECT COUNT(DISTINCT tenant_id) as settled_tenants FROM net_settlements WHERE created_at > now() - interval '3 hours';"
# Expected: ~20000

# 3. Run reconciliation
curl -s -X POST http://localhost:8080/ops/reconciliation/run
# Expected: all checks pass

# 4. Verify settlement duration metric
curl -s http://localhost:8080/metrics | grep 'settla_settlement_tick_duration'
# Expected: < 7200 seconds (2 hours)
```

**Pass/fail criteria**:
- PASS: All tenants settled within 2 hours, reconciliation passes, no tenant skipped
- FAIL: Settlement exceeds 2 hours, tenants skipped, reconciliation discrepancies

**Blast radius**: None (settlement is background process)
**Recovery time**: N/A (can re-trigger if failed)

---

### Scenario 19: Auth Cache Stampede

**ID**: `CHAOS-19` | **Severity if fails**: P1 | **Estimated duration**: 10 min

**What it tests**: Cold cache with 20K tenants, L3 (DB) handles burst auth lookups without connection exhaustion.

**Setup script**:
```bash
# Seed 20K tenants
scripts/demo-seed.sh scale

# Restart all gateway instances simultaneously (flush L1 + L2 cache)
docker compose -f deploy/docker-compose.yml restart gateway
# Also flush Redis
docker compose -f deploy/docker-compose.yml exec redis redis-cli FLUSHALL
```

**Verification script**:
```bash
# 1. Wait for gateway to be ready
sleep 10
curl -sf http://localhost:3100/health

# 2. Generate burst auth traffic from many tenants simultaneously
# Each request triggers L3 (DB) lookup since L1 and L2 are empty
for i in $(seq 1 100); do
  curl -s -o /dev/null -w '%{http_code} ' \
    http://localhost:3100/v1/transfers \
    -H "Authorization: Bearer sk_live_chaos_key_$i" &
done
wait
echo

# 3. Monitor DB connection pool during stampede
curl -s http://localhost:8080/metrics | grep 'settla_pgx_pool'

# 4. Verify cache rebuilds (L1 hit rate increases over time)
sleep 30
curl -s http://localhost:3100/v1/transfers \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key'
sleep 5
curl -s http://localhost:8080/metrics | grep 'settla_auth_cache_hit_rate'
# Expected: L1 hits increasing

# 5. Verify latency returns to normal within 2-5 minutes
sleep 120
time curl -s -o /dev/null http://localhost:3100/v1/transfers \
  -H 'Authorization: Bearer sk_live_lemfi_demo_key'
# Expected: < 50ms
```

**Teardown script**:
```bash
# No teardown needed — cache self-heals
echo "Cache rebuilds automatically"
```

**Pass/fail criteria**:
- PASS: DB handles burst without connection exhaustion, cache rebuilds within 2-5 min, latency spike contained
- FAIL: DB connection pool exhausted, permanent degradation, gateway crash

**Blast radius**: Higher latency for all tenants during cache rebuild (seconds to minutes)
**Recovery time**: 2-5 minutes for full cache warm-up

---

### Scenario 20: Per-Tenant Memory Leak Detection

**ID**: `CHAOS-20` | **Severity if fails**: P1 | **Estimated duration**: 1.5 hours

**What it tests**: RSS per service grows < 10% over sustained load across many tenants.

**Setup script**:
```bash
# Seed tenants
scripts/demo-seed.sh scale  # 20K tenants

# Record baseline RSS
docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}' > /tmp/chaos-mem-baseline.txt
cat /tmp/chaos-mem-baseline.txt

# Start sustained load across all tenants
go run ./tests/loadtest/ \
  -scenario tenants-20k \
  -tps 580 \
  -duration 1h &
LOAD_PID=$!
```

**Verification script**:
```bash
# Sample RSS every 5 minutes
for i in $(seq 1 12); do
  echo "=== Sample $i ($(date)) ==="
  docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}'
  sleep 300
done > /tmp/chaos-mem-samples.txt

# Compare final vs baseline
echo "=== Baseline ==="
cat /tmp/chaos-mem-baseline.txt
echo "=== Final ==="
docker stats --no-stream --format '{{.Name}}\t{{.MemUsage}}'

# Check for specific leak patterns
# 1. sync.Map growth (per-tenant mutexes in NATS subscriber)
curl -s http://localhost:8080/debug/pprof/heap?debug=1 | head -50

# 2. Rate limiter entry buildup
curl -s http://localhost:8080/metrics | grep 'settla_rate_limiter_entries'

# 3. Webhook worker per-tenant semaphore/CB count
curl -s http://localhost:8080/metrics | grep 'settla_webhook_tenant_state'

# 4. Check goroutine count (should be stable)
curl -s http://localhost:8080/debug/pprof/goroutine?debug=1 | head -5
```

**Teardown script**:
```bash
kill $LOAD_PID 2>/dev/null || true
rm -f /tmp/chaos-mem-*.txt
```

**Pass/fail criteria**:
- PASS: RSS growth < 10% over 1 hour, goroutine count stable, per-tenant state properly evicted (5-min idle cleanup)
- FAIL: RSS growth > 10%, goroutine leak, unbounded sync.Map or rate limiter growth

**Blast radius**: None (monitoring only)
**Recovery time**: N/A

---

## Order of Operations

Run chaos scenarios in this order, from least to most destructive. Stop if any P0 scenario fails.

### Phase 1: Non-Destructive (Safe to Run First)
1. **Scenario 4**: Redis Down — safest, Redis is just a cache
2. **Scenario 12**: Slow NATS Consumer — backpressure only
3. **Scenario 16**: Tenant Onboarding Burst — creation only
4. **Scenario 11**: Clock Skew — ephemeral, no data mutation

### Phase 2: Single-Component Failures
5. **Scenario 2**: TigerBeetle Down — ledger queues in NATS
6. **Scenario 3**: NATS Down — outbox accumulates
7. **Scenario 7**: Blockchain RPC Down — deposits pause
8. **Scenario 5**: Provider API Slow — circuit breaker test
9. **Scenario 6**: Provider API Errors — compensation test

### Phase 3: Infrastructure Failures
10. **Scenario 1**: Transfer DB Down — critical path
11. **Scenario 13**: PgBouncer Connection Exhaustion
12. **Scenario 8**: Network Partition (Server ↔ Node)
13. **Scenario 9**: Memory Pressure
14. **Scenario 10**: NATS Disk Full

### Phase 4: Complex Scenarios
15. **Scenario 15**: Mid-Transfer Crash
16. **Scenario 14**: Concurrent Settlement Trigger
17. **Scenario 17**: Hot Tenant + Cold Tenant Fairness
18. **Scenario 19**: Auth Cache Stampede

### Phase 5: Long-Running (Schedule Separately)
19. **Scenario 18**: Settlement at 20K Tenants (2.5 hours)
20. **Scenario 20**: Per-Tenant Memory Leak Detection (1.5 hours)

---

## Emergency Stop Procedure

If a chaos test causes unexpected production impact or unrecoverable state:

### Immediate Actions (< 1 minute)

```bash
# 1. Stop all chaos load generators
pkill -f 'tests/chaos'
pkill -f 'tests/loadtest'

# 2. Restore all Docker containers to healthy state
docker compose -f deploy/docker-compose.yml up -d

# 3. Remove any network manipulation
docker exec settla-node iptables -F 2>/dev/null || true
docker exec settla-server iptables -F 2>/dev/null || true

# 4. Remove any toxiproxy toxics
toxiproxy-cli toxic list 2>/dev/null | while read proxy; do
  toxiproxy-cli toxic remove "$proxy" 2>/dev/null
done

# 5. Reset mock provider
curl -X POST http://localhost:9999/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"latency_ms":0,"error_rate":0.0,"blockchain_rpc_error":false}' 2>/dev/null || true
```

### Recovery Verification (< 5 minutes)

```bash
# 1. Verify all services healthy
curl -sf http://localhost:3100/ready && echo "Gateway: OK" || echo "Gateway: DEGRADED"
curl -sf http://localhost:8080/health && echo "Server: OK" || echo "Server: DEGRADED"

# 2. Verify databases accessible
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c "SELECT 1;" && echo "Transfer DB: OK"
docker compose -f deploy/docker-compose.yml exec postgres-ledger \
  psql -U settla -d settla_ledger -c "SELECT 1;" && echo "Ledger DB: OK"
docker compose -f deploy/docker-compose.yml exec postgres-treasury \
  psql -U settla -d settla_treasury -c "SELECT 1;" && echo "Treasury DB: OK"

# 3. Verify NATS streaming
curl -sf http://localhost:8222/jsz?streams=true | jq '.streams | length'
# Expected: 12

# 4. Check for stuck transfers
docker compose -f deploy/docker-compose.yml exec postgres-transfer \
  psql -U settla -d settla_transfer -c \
  "SELECT status, COUNT(*) FROM transfers WHERE status NOT IN ('COMPLETED','FAILED','REFUNDED') AND created_at > now() - interval '30 minutes' GROUP BY status;"

# 5. Verify money conservation
docker compose -f deploy/docker-compose.yml exec postgres-ledger \
  psql -U settla -d settla_ledger -t -c \
  "SELECT COALESCE(SUM(CASE WHEN entry_type='DEBIT' THEN amount ELSE 0 END),0) as d, COALESCE(SUM(CASE WHEN entry_type='CREDIT' THEN amount ELSE 0 END),0) as c FROM entry_lines;"
```

### Nuclear Option (If All Else Fails)

```bash
# Full environment reset — destroys all data
make docker-reset
make db-seed
```

---

## Interpreting Results

### Automated Suite Output

The chaos test suite prints a summary table:

```
══════════════════════════════════════════════════════════════
  CHAOS TEST SUMMARY
══════════════════════════════════════════════════════════════

  PASS  TigerBeetle Restart       duration=2m30s   recovery=28s     affected=3.2%
  PASS  Postgres Pause             duration=1m45s   recovery=12s     affected=1.1%
  FAIL  NATS Restart               duration=3m15s   recovery=45s     affected=8.5%
       reason: outbox not fully drained: 23 unpublished entries remain after 60s
       ledger_balanced=true  treasury_consistent=true

  7 passed, 1 failed out of 8 scenarios
```

### Key Metrics to Watch

| Metric | Normal | Warning | Critical |
|--------|--------|---------|----------|
| `affected` | < 5% | 5-15% | > 15% |
| `recovery` | < 30s | 30-60s | > 60s |
| `ledger_balanced` | true | — | false (P0) |
| `treasury_consistent` | true | — | false (P0) |
| `stuck_transfers` | 0 | 1-10 | > 10 |

### What "PASS" Means

A passing scenario confirms:
1. The failure was **detected** (health checks, circuit breakers, metrics)
2. The failure was **contained** (didn't cascade to unrelated components)
3. The system **recovered automatically** when the fault was removed
4. **Data integrity was preserved** (money balanced, no stuck transfers)

### What "FAIL" Means

A failing scenario requires investigation:
1. Check the `FailReason` in the output
2. Review service logs: `docker compose -f deploy/docker-compose.yml logs --tail=100 settla-server settla-node gateway`
3. Check metrics: `curl -s http://localhost:8080/metrics | grep -E '(error|fail|stuck|lag)'`
4. File a bug with the scenario name, fail reason, and relevant logs

---

## Known Issues Found by Chaos Testing

### Treasury Pending Ops Channel Saturation (Fixed)

**Discovered**: 2026-03-29 during Scenario 5 (Server Crash)
**Severity**: P0 — server unrecoverable after SIGKILL under load
**Root cause**: After SIGKILL, `LoadPositions()` replays uncommitted reserve ops via `replayOp()` → `Reserve()` → `logOp()`, which enqueues to the `pendingOps` channel (capacity 10,000). The flush loop hasn't started yet (it starts after `LoadPositions`), so nothing drains the channel. With >10K uncommitted ops, every replay blocks for 1s (the `pendingOpsTimeout`) then fails, effectively DOSing the server for hours.
**Fix**: Added `atomic.Bool replaying` flag to `Manager`. During replay, `logOp()` returns immediately since ops are already WAL-logged. Recovery time: timeout (>60s) → 10 seconds.
**Files changed**: `treasury/manager.go`, `treasury/loader.go`
**Regression test**: `TestReplayWithLargeOpBacklog` in `treasury/manager_test.go`

---

## Manual Fault Injection Recipes

### Quick Docker Commands

```bash
# Pause a container (freezes process, simulates complete hang)
docker compose -f deploy/docker-compose.yml pause <service>
docker compose -f deploy/docker-compose.yml unpause <service>

# Stop a container (graceful shutdown)
docker compose -f deploy/docker-compose.yml stop <service>
docker compose -f deploy/docker-compose.yml start <service>

# Kill a container (SIGKILL, simulates OOM or crash)
docker compose -f deploy/docker-compose.yml kill -s SIGKILL <service>
docker compose -f deploy/docker-compose.yml up -d <service>

# Restart a container (graceful stop + start)
docker compose -f deploy/docker-compose.yml restart <service>

# Network disconnect (simulates partition)
docker network disconnect settla-net <container-name>
docker network connect settla-net <container-name>
```

### Service Names for Docker Compose

| Service | Container |
|---------|-----------|
| `postgres-transfer` | Transfer DB |
| `postgres-ledger` | Ledger DB |
| `postgres-treasury` | Treasury DB |
| `pgbouncer-transfer` | Transfer PgBouncer |
| `pgbouncer-ledger` | Ledger PgBouncer |
| `pgbouncer-treasury` | Treasury PgBouncer |
| `tigerbeetle` | TigerBeetle |
| `nats` | NATS JetStream |
| `redis` | Redis |
| `settla-server` | Go gRPC + HTTP server |
| `settla-node` | Worker processes |
| `gateway` | TypeScript API gateway |

---

## Toxiproxy Setup

For fine-grained latency and error injection without killing services:

### Installation

```bash
# Install toxiproxy server + CLI
brew install toxiproxy  # macOS
# Or: go install github.com/Shopify/toxiproxy/v2/cmd/toxiproxy-server@latest
#     go install github.com/Shopify/toxiproxy/v2/cmd/toxiproxy-cli@latest
```

### Docker Compose Overlay

Add to a `docker-compose.toxiproxy.yml` overlay:

```yaml
services:
  toxiproxy:
    image: ghcr.io/shopify/toxiproxy:2.9.0
    ports:
      - "8474:8474"    # API
      - "19090:19090"  # Proxy for gRPC
      - "16434:16434"  # Proxy for Transfer PgBouncer
      - "16433:16433"  # Proxy for Ledger PgBouncer
      - "16379:16379"  # Proxy for Redis
    networks:
      - settla-net
```

### Creating Proxies

```bash
# Create proxies for each dependency
toxiproxy-cli create transfer-db -l 0.0.0.0:16434 -u pgbouncer-transfer:6434
toxiproxy-cli create ledger-db -l 0.0.0.0:16433 -u pgbouncer-ledger:6433
toxiproxy-cli create redis-proxy -l 0.0.0.0:16379 -u redis:6379
toxiproxy-cli create grpc-proxy -l 0.0.0.0:19090 -u settla-server:9090
```

### Injecting Faults

```bash
# Add 5s latency to Transfer DB
toxiproxy-cli toxic add transfer-db -t latency -a latency=5000

# Add 50% packet loss to Redis
toxiproxy-cli toxic add redis-proxy -t timeout -a timeout=5000

# Add bandwidth limit (simulate slow network)
toxiproxy-cli toxic add grpc-proxy -t bandwidth -a rate=1024  # 1KB/s

# Add connection reset (simulate network partition)
toxiproxy-cli toxic add transfer-db -t reset_peer -a timeout=0

# Remove all toxics from a proxy
toxiproxy-cli toxic remove transfer-db -n latency_downstream
```

---

## Environment Requirements

### Minimum Resources for Chaos Testing

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 4 cores | 8 cores |
| RAM | 16 GB | 32 GB |
| Disk | 50 GB SSD | 100 GB NVMe |
| Docker | 24.0+ | Latest stable |
| Docker Compose | v2.20+ | Latest stable |

### Required Tools

| Tool | Purpose | Installation |
|------|---------|-------------|
| `docker` + `docker compose` | Container orchestration | Docker Desktop or Docker Engine |
| `go` (1.22+) | Compile chaos test suite | `brew install go` |
| `curl` + `jq` | API calls and JSON parsing | `brew install curl jq` |
| `toxiproxy` (optional) | Fine-grained fault injection | `brew install toxiproxy` |
| `stress` (optional) | Memory/CPU pressure | `brew install stress` |

### Environment Variables

The chaos tests use the same `.env` file as the main application. Key variables:

```bash
SETTLA_GATEWAY_URL=http://localhost:3100
SETTLA_SERVER_URL=http://localhost:8080
SETTLA_CHAOS_TPS=500
SETTLA_CHAOS_LOAD_DURATION=60s
SETTLA_CHAOS_RECOVERY_WAIT=30s
```
