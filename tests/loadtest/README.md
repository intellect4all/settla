# Load Testing Harness for Settla

This directory contains a Go-based load testing harness that proves Settla can handle 50M transactions/day by sustaining peak TPS for extended periods.

## Philosophy

Instead of running 50M transactions (which would take 24 hours), we prove the system can handle **peak load** (5,000 TPS) for 10 minutes. If the system handles peak for 10 minutes without degradation, it handles sustained 580 TPS indefinitely.

## Why Go (Not k6 or External Tools)?

- **Same domain types**: Uses the same structs and types as the real system
- **Correctness verification**: Can verify business logic while generating load (not just HTTP status codes)
- **Native integration**: Can be run as part of `make loadtest`
- **Realistic payloads**: Generates realistic transfer data with proper currencies, amounts, and recipient details

## Architecture

The load test runs in 4 phases:

### Phase 1: Ramp-up (30 seconds)
- Gradually increases from 0 to target TPS over 30 seconds
- Warms up caches, fills connection pools, triggers JIT compilation

### Phase 2: Sustained Peak (configurable, default 10 minutes)
- Maintains target TPS using a rate limiter (`golang.org/x/time/rate`)
- Each "transaction" is the full API flow:
  1. POST /v1/quotes (as random tenant)
  2. POST /v1/transfers (with quote_id)
  3. Poll GET /v1/transfers/:id until terminal state (COMPLETED or FAILED)
- Records per-request: latency, status, tenant_id, errors

### Phase 3: Drain (60 seconds)
- Stops creating new transfers
- Waits for all in-flight transfers to reach terminal state
- Verifies: no stuck transfers (all COMPLETED or FAILED)

### Phase 4: Verification
After drain, verifies data consistency:
1. For each tenant: sum all completed transfer amounts
2. GET /v1/treasury/positions for each tenant
3. Verifies: positions changed by exactly the right amounts
4. Verifies: no transfer is in a non-terminal state
5. Verifies: total fees collected match expected (from tenant fee schedules)
6. Verifies: 0 ledger imbalance (debits = credits across all accounts)

## Usage

### Basic Usage

```bash
# Run with default settings (1000 TPS, 10 minutes, 2 tenants)
go run ./tests/loadtest/

# Run peak load test (5000 TPS for 10 minutes)
go run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=10

# Quick test for CI (1000 TPS for 2 minutes)
go run ./tests/loadtest/ -tps=1000 -duration=2m -tenants=5

# Single tenant flood test (3000 TPS, one tenant)
go run ./tests/loadtest/ -tps=3000 -duration=5m -tenants=1
```

### Using Make

```bash
# Peak load test (5,000 TPS for 10 minutes)
make loadtest

# Quick test for CI (1,000 TPS for 2 minutes)
make loadtest-quick

# Sustained load (600 TPS for 30 minutes)
make loadtest-sustained

# Burst recovery test (ramp 600→8000→600 TPS)
make loadtest-burst

# Single tenant flood (3,000 TPS, one tenant)
make loadtest-flood

# Multi-tenant scale (50 tenants × 100 TPS)
make loadtest-multi

# Soak test (2 hours at 1,000 TPS)
make soak

# Short soak for CI (15 minutes at 1,000 TPS)
make soak-short
```

### Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-gateway` | `http://localhost:3000` | Gateway URL |
| `-tps` | `1000` | Target transactions per second |
| `-duration` | `10m` | Test duration (e.g., 10m, 2h) |
| `-tenants` | `2` | Number of tenants to simulate |
| `-rampup` | `30s` | Ramp-up duration |
| `-drain` | `60s` | Drain duration |

## Metrics

The load test collects the following metrics in real-time:

### Throughput Counters
- `RequestsTotal`: Total HTTP requests made
- `TransfersCreated`: Transfers successfully created
- `TransfersCompleted`: Transfers that reached COMPLETED state
- `TransfersFailed`: Transfers that reached FAILED state
- `PeakInflight`: Maximum concurrent transfers in-flight

### Latency Histograms (per-operation)
- `QuoteLatency`: Time to create a quote (p50, p95, p99)
- `CreateLatency`: Time to create a transfer (p50, p95, p99)
- `PollLatency`: Time to poll until terminal state (p50, p95, p99)
- `EndToEndLatency`: Total time from quote to completion (p50, p95, p99)

### Error Tracking
- `Errors`: Map of error code → count (e.g., "quote_create_failed", "transfer_create_failed")

## Live Dashboard

Every 5 seconds, the load test prints a live dashboard:

```
=== Load Test Metrics ===
Transfers: created=15000, completed=14850, failed=150, inflight=23
Success Rate: 99.00%

Latency Statistics:
  Quote:     p50=2.3ms, p95=5.1ms, p99=12.4ms (n=15000)
  Create:    p50=4.5ms, p95=8.2ms, p99=18.7ms (n=15000)
  Poll:      p50=1.2s, p95=2.5s, p99=4.8s (n=14850)
  End-to-End: p50=1.3s, p95=2.6s, p99=5.1s (n=14850)

Errors:
  quote_create_failed: 5
  transfer_create_failed: 145
```

## Verification

After the test completes, the harness verifies:

1. **No stuck transfers**: All transfers reach COMPLETED, FAILED, or REFUNDED state
2. **Data consistency**: Per-tenant transfer amounts match treasury position changes
3. **Fee accuracy**: Total fees match expected values based on tenant fee schedules
4. **Ledger balance**: Debits equal credits across all accounts

## Test Scenarios

### Peak Load (5,000 TPS for 10 minutes)
Proves the system can handle maximum capacity without degradation.

```bash
make loadtest
```

### Sustained Load (580 TPS for 30 minutes)
Simulates the average load for 50M transactions/day.

```bash
make loadtest-sustained
```

### Burst Recovery (600→8000→600 TPS)
Tests the system's ability to handle traffic spikes and recover.

```bash
make loadtest-burst
```

### Single Tenant Flood (3,000 TPS, one tenant)
Tests tenant isolation under extreme load from a single tenant.

```bash
make loadtest-flood
```

### Multi-Tenant Scale (50 tenants × 100 TPS)
Tests horizontal scaling with many concurrent tenants.

```bash
make loadtest-multi
```

### Soak Test (1,000 TPS for 2 hours)
Tests for memory leaks, connection pool exhaustion, or gradual degradation.

```bash
make soak
```

## Requirements

### Infrastructure Requirements

For a 5,000 TPS peak load test, you'll need:

- **Gateway**: 2-4 instances (load balanced)
- **Settla Server**: 4-8 instances
- **TigerBeetle**: 3-node cluster (replicated)
- **Postgres**: Primary + read replicas
- **Redis**: Cluster mode with 3 masters
- **NATS**: 3-node JetStream cluster

### Network Requirements

- Bandwidth: ~50 Mbps (assuming 1KB per request)
- Latency: <10ms between services
- No packet loss

### Client Requirements

- CPU: 4+ cores (for generating load)
- RAM: 8GB+ (for tracking metrics)
- Network: Direct connection to gateway (not over VPN)

## Interpreting Results

### Success Criteria

A load test is considered **PASS** if:

1. **Success Rate ≥ 99.9%**: <0.1% of transfers fail
2. **No Stuck Transfers**: All transfers reach terminal state within drain period
3. **Latency SLO Met**: p99 end-to-end latency <10 seconds
4. **No Errors**: No connection errors, timeouts, or 5xx responses
5. **Data Consistent**: Verification phase passes

### Warning Signs

- Success rate <99%: System is overloaded
- Increasing latency over time: Resource exhaustion (memory leak, connection pool)
- Connection errors: Network or gateway capacity issues
- 5xx errors: Backend services failing
- Stuck transfers: State machine issues or deadlocks

### Performance Tuning

If the test fails to meet targets:

1. **Check resource utilization**: CPU, memory, network, disk I/O
2. **Review connection pools**: Ensure sufficient connections to DB/cache
3. **Tune batch sizes**: Adjust TigerBeetle batch window
4. **Scale horizontally**: Add more gateway/server instances
5. **Optimize hot paths**: Review profiler output for bottlenecks

## CI Integration

For CI/CD pipelines, use the quick test:

```yaml
# .github/workflows/loadtest.yml
- name: Load Test
  run: make loadtest-quick
  timeout-minutes: 5
```

The quick test runs for 2 minutes at 1,000 TPS and is sufficient to catch:
- Compilation errors
- Configuration issues
- Basic connectivity problems
- Obvious performance regressions

## Troubleshooting

### "Connection refused"
- Gateway is not running
- Wrong gateway URL
- Network connectivity issue

### "Context deadline exceeded"
- Gateway timeout
- Backend services are slow
- Increase `-timeout` flag

### "Too many open files"
- Increase ulimit: `ulimit -n 65536`
- Reduce TPS or number of workers

### High latency
- Backend services are overloaded
- Network latency is high
- Database is slow (check indexes)

### Low success rate
- Backend services are failing
- Rate limiting is enabled
- Insufficient capacity

## Future Enhancements

1. **Distributed load generation**: Coordinate multiple load test clients
2. **Chaos testing integration**: Inject failures during load test
3. **Real-time Grafana dashboard**: Push metrics to Prometheus/Grafana
4. **Automatic scaling**: Trigger auto-scaling based on load
5. **Historical comparison**: Compare results to previous runs
6. **Custom scenarios**: Support custom transfer patterns (batch, burst, etc.)

## See Also

- `../integration/` - Integration tests
- `../chaos/` - Chaos testing
- `../../docs/BENCHMARKS.md` - Component benchmarks
