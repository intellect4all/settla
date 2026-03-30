# Chapter 8.4: Observability

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain the three pillars of observability (logs, metrics, traces) as applied to settlement systems
2. Read Settla's `metrics.go` and understand each Prometheus metric type and its labels
3. Interpret SLI burn-rate alerts and calculate error budget consumption
4. Design Grafana dashboards for the four critical subsystems (transfers, treasury, ledger, outbox)
5. Use structured logging fields (`slog` in Go, `pino` in TypeScript) for cross-service tracing
6. Configure OpenTelemetry distributed tracing with OTLP gRPC export
7. Monitor connection pool health with background Prometheus gauges

---

## The Three Pillars for Settlement Systems

Settlement infrastructure adds a fourth dimension to the standard three pillars:
**financial invariant monitoring**. You must observe not just "is the system
healthy?" but "is the money correct?"

```
+-------------------+------------------+--------------------------------+
| Pillar            | Tool             | Settlement-Specific Use        |
+-------------------+------------------+--------------------------------+
| Logs              | slog / pino      | Transfer state transitions,    |
|                   |                  | error context, audit trail     |
+-------------------+------------------+--------------------------------+
| Metrics           | Prometheus       | TPS, latency, queue depth,     |
|                   |                  | treasury balance gauges         |
+-------------------+------------------+--------------------------------+
| Traces            | OpenTelemetry +  | Cross-service transfer flow    |
|                   | OTLP gRPC       | (gateway -> gRPC -> engine)    |
+-------------------+------------------+--------------------------------+
| Financial         | Custom checks    | Debit=credit balance,          |
| Invariants        |                  | outbox drain lag, treasury     |
|                   |                  | reconciliation                 |
+-------------------+------------------+--------------------------------+
```

---

## Structured Logging

### Go: slog

Settla uses Go's standard `log/slog` with structured fields that enable
machine-parseable log analysis:

```go
logger.Info("transfer state transition",
    "tenant_id", transfer.TenantID,
    "transfer_id", transfer.ID,
    "from_status", fromStatus,
    "to_status", toStatus,
    "version", transfer.Version,
)

logger.Error("settla-treasury: reserve failed",
    "tenant_id", tenantID,
    "currency", currency,
    "location", location,
    "amount", amount,
    "available", pos.Available(),
    "error", err,
)
```

**Conventions:**
- Error messages use the `settla-{module}:` prefix for grep-ability
- Every log entry includes `tenant_id` for tenant-scoped filtering
- Transaction-level logs include `transfer_id` for end-to-end tracing
- Monetary values use `shopspring/decimal` (never float)

### TypeScript: pino (via Fastify)

The gateway and webhook services use pino through Fastify:

```typescript
fastify.log.info({
    request_id: req.id,
    tenant_id: auth.tenantId,
    method: req.method,
    path: req.url,
    status: reply.statusCode,
    duration_ms: elapsed,
}, 'request completed');
```

**Conventions:**
- Every request log includes `request_id`, `tenant_id`, `method`, `path`,
  `status`, `duration_ms`
- The `request_id` is propagated to the gRPC call via metadata, enabling
  cross-service tracing without a full distributed tracing system

---

## Prometheus Metrics

All Settla metrics are defined in a single file: `observability/metrics.go`.
The `Metrics` struct is instantiated once per process and passed to modules
via constructors.

### The Metrics Struct

```go
type Metrics struct {
    // -- Transfer metrics --
    TransfersTotal    *prometheus.CounterVec    // labels: tenant, status, corridor
    TransferDuration  *prometheus.HistogramVec  // labels: tenant, corridor, chain

    // -- Ledger metrics --
    LedgerPostingsTotal   *prometheus.CounterVec   // labels: reference_type
    LedgerTBWritesTotal   prometheus.Counter
    LedgerTBWriteLatency  prometheus.Histogram
    LedgerTBBatchSize     prometheus.Histogram
    LedgerPGSyncLag       prometheus.Gauge

    // -- Treasury metrics --
    TreasuryReserveTotal   *prometheus.CounterVec   // labels: tenant, currency
    TreasuryReserveLatency prometheus.Histogram
    TreasuryFlushLag       prometheus.Gauge
    TreasuryFlushDuration  prometheus.Histogram
    TreasuryBalance        *prometheus.GaugeVec     // labels: tenant, currency, location
    TreasuryLocked         *prometheus.GaugeVec     // labels: tenant, currency, location

    // -- Provider metrics --
    ProviderRequestsTotal *prometheus.CounterVec   // labels: provider, operation, status
    ProviderLatency       *prometheus.HistogramVec  // labels: provider, operation

    // -- NATS metrics --
    NATSMessagesTotal       *prometheus.CounterVec  // labels: partition, status
    NATSPartitionQueueDepth *prometheus.GaugeVec    // labels: partition

    // -- gRPC metrics --
    GRPCRequestsTotal  *prometheus.CounterVec      // labels: service, method, code
    GRPCRequestLatency *prometheus.HistogramVec    // labels: service, method

    // -- Outbox relay metrics --
    OutboxPublishedTotal prometheus.Counter
    OutboxRelayLag       prometheus.Gauge

    // -- Cache metrics --
    CacheHitsTotal   *prometheus.CounterVec   // labels: cache_type
    CacheMissesTotal *prometheus.CounterVec   // labels: cache_type

    // -- Treasury WAL metrics --
    TreasuryWALWriteFailures prometheus.Counter
    TreasuryUncommittedOps   prometheus.Gauge

    // -- TigerBeetle metrics --
    TigerBeetleResponseLatency         *prometheus.HistogramVec  // labels: operation
    TigerBeetleAccountCreationFailures prometheus.Counter

    // -- Ledger sync queue --
    LedgerSyncQueueDepth   prometheus.Gauge
    LedgerSyncQueueDropped prometheus.Counter
}
```

### Metric Types Explained

Settla uses all four Prometheus metric types:

```
+-----------+-----------------------------+----------------------------------+
| Type      | Example                     | When to Use                      |
+-----------+-----------------------------+----------------------------------+
| Counter   | settla_transfers_total      | Monotonically increasing count   |
|           |                             | (requests, errors, bytes)        |
+-----------+-----------------------------+----------------------------------+
| Gauge     | settla_treasury_balance     | Value that goes up and down      |
|           |                             | (queue depth, balance, lag)       |
+-----------+-----------------------------+----------------------------------+
| Histogram | settla_transfer_duration_s  | Distribution of values           |
|           |                             | (latency percentiles)            |
+-----------+-----------------------------+----------------------------------+
| Summary   | (not used)                  | Client-side percentiles          |
|           |                             | (less flexible than histograms)  |
+-----------+-----------------------------+----------------------------------+
```

### Bucket Design

Histogram buckets are tuned to the expected latency ranges:

```go
// Transfer end-to-end: seconds (most complete in 1-30s)
TransferDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
    Name:    "settla_transfer_duration_seconds",
    Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120},
}, []string{"tenant", "corridor", "chain"})

// Treasury CAS loop: sub-microsecond
TreasuryReserveLatency: promauto.NewHistogram(prometheus.HistogramOpts{
    Name:    "settla_treasury_reserve_latency_seconds",
    Buckets: []float64{0.0000001, 0.0000005, 0.000001, 0.000005, 0.00001, 0.0001, 0.001},
})

// TigerBeetle write: sub-millisecond
LedgerTBWriteLatency: promauto.NewHistogram(prometheus.HistogramOpts{
    Name:    "settla_ledger_tb_write_latency_seconds",
    Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1},
})

// Provider calls: seconds (external services)
ProviderLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
    Name:    "settla_provider_latency_seconds",
    Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
}, []string{"provider", "operation"})
```

**Key Insight:** The treasury reserve latency buckets start at 100 nanoseconds
(0.0000001s). This reflects the fact that the CAS loop operates entirely in
memory -- if you see values above 1 microsecond, something is wrong
(excessive contention, GC pauses, or lock convoy).

### Label Design Principles

Labels enable multi-dimensional querying but must be used carefully:

```
GOOD labels (bounded cardinality):
  tenant:      ~50 tenants (known set)
  status:      ~8 states (CREATED, FUNDED, ON_RAMPING, ...)
  corridor:    ~20 currency pairs (GBP_NGN, NGN_GBP, ...)
  provider:    ~10 providers (known set)

BAD labels (unbounded cardinality):
  transfer_id: 50M distinct values per day --> OOM
  amount:      infinite distinct values --> OOM
  user_email:  unbounded --> OOM + PII leak
```

---

## SLI/SLO Alerting

Settla's alert rules live in `deploy/prometheus/alerts/settla-sli-alerts.yml`
and use the Google SRE multi-window, multi-burn-rate approach.

### SLO Definition

```
Target: 99.9% success rate (0.1% error budget)
Monthly error budget: 43.2 minutes of downtime
```

### Burn Rate Alerts

Instead of alerting on static thresholds ("error rate > 1%"), burn rate alerts
fire when the error budget is being consumed faster than sustainable:

```yaml
# Fast burn: 14.4x consumption rate
# Exhausts monthly budget in ~1 hour
# Triggers immediate page
- alert: SettlaHighErrorRateFastBurn
  expr: |
    (
      sum(rate(settla_gateway_requests_total{status=~"5.."}[5m]))
      /
      sum(rate(settla_gateway_requests_total[5m]))
    ) > (14.4 * 0.001)
    and
    (
      sum(rate(settla_gateway_requests_total{status=~"5.."}[1m]))
      /
      sum(rate(settla_gateway_requests_total[1m]))
    ) > (14.4 * 0.001)
  for: 2m
  labels:
    severity: critical
    team: platform
  annotations:
    summary: "Settla error rate consuming budget at 14.4x (budget exhausted in ~1h)"
    description: "5xx error rate {{ $value | humanizePercentage }} exceeds 1.44% threshold"
    runbook: "docs/runbooks/high-error-rate.md"
```

**Why two windows?** The `5m` window catches sustained errors. The `1m` window
confirms the problem is not a single-scrape spike. Both must exceed the threshold
for the alert to fire. This prevents false pages from momentary blips.

### The Three Burn Rate Tiers

```
+-------+------+-----------+--------------------+-----------+
| Tier  | Rate | Budget    | Response Time      | Severity  |
|       |      | Exhaustion|                    |           |
+-------+------+-----------+--------------------+-----------+
| Fast  | 14.4x| ~1 hour  | Page immediately   | critical  |
| Medium| 6x   | ~6 hours | Page within 30 min | critical  |
| Slow  | 1x   | ~30 days | Ticket next sprint | warning   |
+-------+------+-----------+--------------------+-----------+
```

### Latency SLI

```yaml
# p99 latency exceeds 5 seconds for transfer creation
- alert: SettlaTransferLatencyHigh
  expr: |
    histogram_quantile(0.99,
      sum(rate(settla_grpc_request_latency_seconds_bucket{
        service="SettlementService",
        method="CreateTransfer"
      }[5m])) by (le)
    ) > 5
  for: 5m
  labels:
    severity: warning
```

### Infrastructure Alerts

Beyond SLI alerts, Settla monitors infrastructure health:

```yaml
# Outbox relay lag exceeds 30 seconds
- alert: SettlaOutboxRelayLagHigh
  expr: settla_outbox_relay_lag_seconds > 30
  for: 2m
  annotations:
    summary: "Outbox relay lag {{ $value }}s -- side effects are delayed"

# Treasury flush lag exceeds 5 seconds
- alert: SettlaTreasuryFlushLagHigh
  expr: settla_treasury_flush_lag_seconds > 5
  for: 1m
  annotations:
    summary: "Treasury flush stalled -- crash would lose in-memory reservations"

# NATS partition imbalance
- alert: SettlaNATSPartitionImbalance
  expr: |
    max(settla_nats_partition_lag_messages) by (stream)
    /
    (avg(settla_nats_partition_lag_messages) by (stream) + 1)
    > 5
  for: 5m

# TigerBeetle to Postgres sync lag
- alert: SettlaLedgerSyncLagHigh
  expr: settla_ledger_pg_sync_lag_seconds > 60
  for: 5m
  annotations:
    summary: "Ledger read-side is {{ $value }}s behind TigerBeetle"

# PgBouncer connection pool waiting clients
- alert: SettlaPgBouncerWaitingClientsHigh
  expr: pgbouncer_show_pools_cl_waiting > 10
  for: 2m

# Treasury uncommitted operations
- alert: SettlaTreasuryUncommittedOpsHigh
  expr: settla_treasury_uncommitted_ops_pending > 100
  for: 5m
  annotations:
    summary: "{{ $value }} uncommitted treasury ops -- possible WAL issue"
```

---

## Grafana Dashboard Design

### Dashboard 1: Transfer Pipeline

```
+------------------------------------------------------------------+
| TRANSFER PIPELINE                                                 |
+------------------------------------------------------------------+
| [Counter] Transfers/sec by status    | [Histogram] E2E latency   |
|   rate(settla_transfers_total[5m])   |   p50, p95, p99            |
|   Split: completed, failed, stuck    |   by corridor              |
+--------------------------------------+---------------------------+
| [Counter] Transfers by corridor      | [Gauge] Active transfers  |
|   GBP->NGN, NGN->GBP, etc.          |   in non-terminal state   |
+--------------------------------------+---------------------------+
```

### Dashboard 2: Treasury Health

```
+------------------------------------------------------------------+
| TREASURY HEALTH                                                   |
+------------------------------------------------------------------+
| [Gauge] Balance by tenant+currency   | [Histogram] Reserve CAS   |
|   settla_treasury_balance            |   latency (sub-us target) |
+--------------------------------------+---------------------------+
| [Gauge] Locked by tenant+currency    | [Gauge] Flush lag         |
|   settla_treasury_locked             |   settla_treasury_flush_  |
|                                      |   lag_seconds             |
+--------------------------------------+---------------------------+
| [Counter] Reserve ops by tenant      | [Gauge] Uncommitted ops   |
+--------------------------------------+---------------------------+
```

### Dashboard 3: Infrastructure

```
+------------------------------------------------------------------+
| INFRASTRUCTURE                                                    |
+------------------------------------------------------------------+
| [Gauge] Outbox relay lag             | [Counter] NATS messages   |
|   settla_outbox_relay_lag_seconds    |   by partition + status   |
+--------------------------------------+---------------------------+
| [Gauge] NATS queue depth            | [Gauge] PG sync lag       |
|   settla_nats_partition_queue_depth  |   settla_ledger_pg_sync_  |
|   by partition                       |   lag_seconds             |
+--------------------------------------+---------------------------+
| [Counter] Cache hit/miss ratio       | [Histogram] TB write      |
|   settla_cache_hits_total /          |   latency + batch size    |
|   (hits + misses)                    |                           |
+--------------------------------------+---------------------------+
```

### Dashboard 4: Provider Performance

```
+------------------------------------------------------------------+
| PROVIDER PERFORMANCE                                              |
+------------------------------------------------------------------+
| [Counter] Requests by provider       | [Histogram] Latency by    |
|   + operation + status               |   provider + operation    |
+--------------------------------------+---------------------------+
| [Counter] gRPC requests by method    | [Histogram] gRPC latency  |
|   + status code                      |   by service + method     |
+--------------------------------------+---------------------------+
```

---

## OpenTelemetry Distributed Tracing

Settla supports distributed tracing via OpenTelemetry with OTLP gRPC export.
The implementation lives in `observability/tracing.go`:

```go
// InitTracer sets up an OpenTelemetry TracerProvider with OTLP gRPC exporter.
// Configuration is driven by standard OTEL_* environment variables:
//   - OTEL_EXPORTER_OTLP_ENDPOINT (e.g. "localhost:4317")
//   - OTEL_TRACES_SAMPLER (e.g. "parentbased_traceidratio")
//   - OTEL_TRACES_SAMPLER_ARG (e.g. "0.1" for 10% sampling)
//   - OTEL_SERVICE_NAME (fallback to serviceName parameter)
//
// Returns a shutdown function that should be deferred.
// If the OTLP endpoint is not configured, returns a no-op shutdown.
func InitTracer(ctx context.Context, serviceName, version string, logger *slog.Logger) (func(context.Context) error, error) {
    exporter, err := otlptracegrpc.New(ctx)
    if err != nil {
        logger.Warn("settla-tracing: OTLP exporter unavailable, tracing disabled", "error", err)
        return func(context.Context) error { return nil }, nil
    }

    res, err := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceNameKey.String(serviceName),
            semconv.ServiceVersionKey.String(version),
        ),
    )
    if err != nil {
        return nil, err
    }

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )
    otel.SetTracerProvider(tp)

    logger.Info("settla-tracing: OpenTelemetry tracer initialized",
        "service", serviceName, "version", version)

    return tp.Shutdown, nil
}
```

**Key Design Decisions:**

1. **Auto-detection:** If `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, the
   exporter creation fails gracefully and tracing is disabled. This means
   local development runs without a Jaeger/Tempo instance -- no configuration
   needed, no errors.

2. **Standard environment variables:** All configuration uses the official
   OTEL SDK environment variable names. No custom configuration struct. This
   means any OpenTelemetry-compatible collector (Jaeger, Tempo, Datadog)
   works without code changes.

3. **Batched export:** `sdktrace.WithBatcher(exporter)` batches trace spans
   before export, reducing network overhead at 5,000 TPS.

4. **Shutdown function:** The returned shutdown function is deferred in
   `main()` to flush pending spans on graceful termination.

### Trace Propagation

Request IDs flow from the gateway through gRPC metadata to the engine.
With OpenTelemetry enabled, these become proper W3C trace context headers
(`traceparent`/`tracestate`), enabling end-to-end trace visualization:

```
Gateway (Fastify) --> gRPC metadata --> settla-server --> NATS --> settla-node
      |                    |                  |                      |
      span: http.request   span: grpc.call    span: engine.create   span: worker.process
```

---

## Connection Pool Metrics

Database connection pool exhaustion is one of the most common production
failure modes. Settla monitors pool health with a background goroutine
(`observability/pool_metrics.go`):

```go
// RegisterPoolMetrics starts a background goroutine that polls pool.Stat()
// every 5 seconds and updates the PgxPool* gauges. The goroutine stops when
// ctx is cancelled.
func RegisterPoolMetrics(ctx context.Context, pool *pgxpool.Pool, dbName string, m *Metrics) {
    if pool == nil || m == nil {
        return
    }
    go func() {
        ticker := time.NewTicker(5 * time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                stat := pool.Stat()
                m.PgxPoolMaxConns.WithLabelValues(dbName).Set(float64(stat.MaxConns()))
                m.PgxPoolCurrentConns.WithLabelValues(dbName).Set(float64(stat.AcquiredConns() + stat.IdleConns()))
                m.PgxPoolIdleConns.WithLabelValues(dbName).Set(float64(stat.IdleConns()))
            }
        }
    }()
}
```

Three Prometheus gauges are emitted per database, labeled by `dbName`
("ledger", "transfer", "treasury"):

```
+-----------------------------+--------------------------------------------+
| Gauge                       | What It Measures                           |
+-----------------------------+--------------------------------------------+
| PgxPoolMaxConns             | Configured maximum connections             |
| PgxPoolCurrentConns         | Acquired + idle connections (total in use) |
| PgxPoolIdleConns            | Connections sitting idle in the pool       |
+-----------------------------+--------------------------------------------+
```

**When to alert:** If `PgxPoolCurrentConns` approaches `PgxPoolMaxConns`,
the pool is nearing saturation. New queries will block waiting for a
connection. The alert fires when utilization exceeds 80% for 2 minutes:

```
PgxPoolCurrentConns / PgxPoolMaxConns > 0.8  for 2m
```

The nil-check pattern (`if pool == nil || m == nil`) allows tests and local
development to skip metrics registration without panicking.

---

## Grafana Dashboards

Settla ships 11 pre-built Grafana dashboards in `deploy/grafana/dashboards/`:

```
deploy/grafana/dashboards/
  settla-overview.json           -- System-wide transfer pipeline
  api-performance.json           -- Gateway latency and error rates
  treasury-health.json           -- Treasury positions, reserve latency, flush lag
  tenant-health.json             -- Per-tenant transfer health
  capacity-planning.json         -- Throughput headroom and resource utilization
  deposit-health.json            -- Crypto deposit session monitoring
  bank-deposit-health.json       -- Fiat deposit session monitoring
  executive-overview.json        -- High-level business metrics (NEW)
  infrastructure.json            -- System health and resource utilization (NEW)
  security-reliability.json      -- Security and reliability KPIs (NEW)
  tenant-scale.json              -- Per-tenant performance at scale (NEW)
```

The four new dashboards support operational concerns beyond the core pipeline:

### Executive Overview Dashboard

Business-level metrics for non-technical stakeholders: total transfer volume
(USD), daily active tenants, corridor distribution, settlement cycle health,
and revenue from fees. No infrastructure details -- just business outcomes.

### Infrastructure Dashboard

System health and resource utilization: CPU/memory per service, connection
pool utilization (using the PgxPool gauges above), NATS queue depth, Redis
hit rates, disk I/O. This is the first dashboard an on-call engineer checks
during an incident.

### Security and Reliability Dashboard

Security KPIs: authentication failure rates, rate limit trigger frequency,
API key rotation age. Reliability KPIs: SLO burn rate, error budget remaining,
mean time to recovery (MTTR) for the last 5 incidents.

### Tenant Scale Dashboard

Per-tenant performance at scale: request rates by tenant (using Zipf distribution
overlay), per-tenant p99 latency, treasury position utilization, and tenant
isolation verification (cross-tenant query rate should always be zero).

---

## Alerting Rules

Alert rules live in `deploy/prometheus/alerts/` across three files:

```
deploy/prometheus/alerts/
  settla-sli-alerts.yml      -- SLI burn-rate alerts (described below)
  recording-rules.yml        -- Precomputed metrics for dashboard efficiency (NEW)
  supplemental-rules.yml     -- Custom operational alerts (NEW)
```

### Recording Rules (`recording-rules.yml`)

Recording rules precompute expensive PromQL queries at scrape time, so
dashboards load instantly instead of computing aggregations on every refresh:

```yaml
# Example: precompute per-tenant success rate (avoids re-scanning counters)
- record: settla:tenant:success_rate_5m
  expr: |
    sum(rate(settla_transfers_total{status="COMPLETED"}[5m])) by (tenant)
    /
    sum(rate(settla_transfers_total[5m])) by (tenant)
```

### Supplemental Rules (`supplemental-rules.yml`)

Operational alerts beyond the SLI burn-rate alerts. These catch
infrastructure-specific issues that do not directly affect the error rate
but indicate degradation:

- Connection pool approaching saturation (>80% utilization for 2m)
- NATS consumer lag exceeding 10,000 messages for 5m
- Treasury event writer backlog (channel >80% full for 1m)
- TigerBeetle sync lag exceeding 120 seconds
- Partition manager failing to create next month's partitions

---

## Metric Scrape Configuration

Each Settla process exposes metrics on a dedicated port:

```
+------------------+------+----------+
| Process          | Port | Path     |
+------------------+------+----------+
| settla-server    | 8080 | /metrics |
| settla-node      | 9091 | /metrics |
| gateway          | 3000 | /metrics |
| webhook          | 3001 | /metrics |
+------------------+------+----------+
```

Kubernetes annotations enable automatic Prometheus discovery:

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8080"
  prometheus.io/path: "/metrics"
```

---

## Common Mistakes

### Mistake 1: Using High-Cardinality Labels

```go
// BAD: transfer_id has 50M distinct values per day
TransfersTotal: promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "settla_transfers_total",
}, []string{"tenant", "transfer_id"})  // OOM within hours

// GOOD: bounded cardinality labels
TransfersTotal: promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "settla_transfers_total",
}, []string{"tenant", "status", "corridor"})
```

### Mistake 2: Alerting on Absolute Thresholds

```yaml
# BAD: Static threshold -- fires during deploy, maintenance, etc.
- alert: HighErrorRate
  expr: rate(errors_total[5m]) > 10

# GOOD: Burn rate -- fires only when error budget is threatened
- alert: HighErrorRate
  expr: error_rate > (14.4 * 0.001)  # 14.4x burn rate
```

### Mistake 3: Not Nil-Checking Metrics

All Settla modules nil-check metrics before use, allowing tests to pass nil:

```go
if m.metrics != nil {
    m.metrics.TreasuryReserveLatency.Observe(elapsed.Seconds())
}
```

This avoids duplicate Prometheus registration panics in tests that create
multiple module instances.

---

## Exercises

1. **Calculate error budget remaining.** If the monthly error budget is
   43.2 minutes (99.9% SLO) and the system had 5 minutes of 5xx errors
   this month, what percentage of the budget remains? How many more
   minutes of downtime can occur before the SLO is violated?

2. **Add a custom metric.** Add `settla_transfer_retries_total` (CounterVec
   with labels: tenant, corridor, retry_reason) to `metrics.go`. Where in
   the codebase would you increment this counter?

3. **Write a PromQL query.** Write a query that returns the per-tenant
   success rate over the last hour. Which Prometheus function do you use
   to handle the counter reset case?

---

## What's Next

With observability providing visibility into production behavior, Chapter 8.5
covers the deployment architecture -- Kubernetes manifests, replica counts,
PgBouncer configuration, and the complete infrastructure topology that turns
the codebase into a running system.
