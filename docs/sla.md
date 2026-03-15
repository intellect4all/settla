# Settla Service Level Agreements

## Overview

This document defines the SLA (external commitments), SLO (internal targets), and SLI (measurements) for Settla's B2B stablecoin settlement infrastructure. All metrics are monitored via Prometheus and visualized in Grafana dashboards under `deploy/grafana/dashboards/`.

---

## Service Level Agreement (External -- What We Promise Tenants)

These are contractual commitments published to tenant fintechs (Lemfi, Fincra, Paystack, etc.) in our service agreement.

| Metric | Commitment |
|--------|------------|
| **Availability** | 99.9% monthly (max 43.2 min downtime/month, 8.7 hours/year) |
| **Transfer success** | >99.5% of transfers complete within 30 minutes of initiation |
| **API latency** | p95 < 200ms for `POST /v1/quotes` |
| **Webhook delivery** | 99% of webhooks delivered within 60 seconds of state change |
| **Data durability** | Zero data loss for completed transactions (TigerBeetle is write authority) |
| **Tenant isolation** | No cross-tenant data leakage under any failure mode |

### SLA Breach Consequences

| Availability | Credit |
|-------------|--------|
| 99.9% -- 99.5% | 10% monthly fee credit |
| 99.5% -- 99.0% | 25% monthly fee credit |
| < 99.0% | 50% monthly fee credit |

Credits are applied to the next billing cycle. Tenants must request credits within 30 days of the incident.

---

## Service Level Objectives (Internal -- What We Target)

These are internal engineering targets, stricter than the external SLA, providing a safety margin.

| Metric | Target | Rationale |
|--------|--------|-----------|
| **Availability** | 99.95% (4.4 hours/year) | 0.05% margin above SLA |
| **Transfer success** | >99.8% | 0.3% margin above SLA |
| **Quote latency (p50)** | < 10ms | Most quotes served from cache |
| **Quote latency (p95)** | < 50ms | Provider lookups + scoring |
| **Quote latency (p99)** | < 200ms | Cold cache, complex corridors |
| **Transfer E2E (p50)** | < 5 minutes | On-chain confirmation time is the floor |
| **Transfer E2E (p95)** | < 15 minutes | Includes provider processing |
| **Transfer E2E (p99)** | < 30 minutes | Edge cases, retries |
| **Ledger posting (p99)** | < 5ms | TigerBeetle write path |
| **Treasury reserve (p99)** | < 10 microseconds | In-memory atomic operation |
| **Component recovery** | < 5 minutes | Any single component failure |
| **Webhook delivery (p95)** | < 5 seconds | First delivery attempt |

---

## Service Level Indicators (What We Measure)

Each SLI maps to a specific Prometheus metric scraped from our services.

### Availability

```
availability = successful_requests / total_requests per 5-minute window
```

**Prometheus query:**
```promql
# 5-minute availability (gateway)
1 - (
  sum(rate(settla_gateway_requests_total{status=~"5.."}[5m]))
  /
  sum(rate(settla_gateway_requests_total[5m]))
)
```

**Dashboard:** Settla Overview (`settla-overview.json`)

### Transfer Success Rate

```
transfer_ok = completed_transfers / total_transfers per 1-hour window
```

**Prometheus query:**
```promql
sum(rate(settla_transfers_total{status="completed"}[1h]))
/
sum(rate(settla_transfers_total[1h]))
* 100
```

**Dashboard:** Settla Overview (`settla-overview.json`) -- "Transfer Success Rate" gauge

### Quote Latency

```
quote_latency = histogram of POST /v1/quotes response time
```

**Prometheus query:**
```promql
histogram_quantile(0.95,
  sum(rate(settla_gateway_request_duration_seconds_bucket{path="/v1/quotes",method="POST"}[5m])) by (le)
)
```

**Dashboard:** API Performance (`api-performance.json`)

### Transfer End-to-End Latency

```
e2e_latency = histogram of (completed_at - created_at) on transfers
```

**Prometheus query:**
```promql
histogram_quantile(0.95,
  sum(rate(settla_transfer_duration_seconds_bucket[5m])) by (le)
)
```

**Dashboard:** Settla Overview (`settla-overview.json`) -- "Transfer Duration (p50/p95/p99)"

### Ledger Posting Latency

```
ledger_latency = histogram of TigerBeetle PostEntries duration
```

**Prometheus query:**
```promql
histogram_quantile(0.99,
  rate(settla_ledger_tb_write_latency_seconds_bucket[5m])
)
```

**Dashboard:** Capacity Planning (`capacity-planning.json`)

### Treasury Reserve Latency

```
reserve_latency = histogram of treasury Reserve() duration
```

**Prometheus query:**
```promql
histogram_quantile(0.99,
  rate(settla_treasury_reserve_latency_seconds_bucket[5m])
) * 1e9  # Convert to nanoseconds
```

**Dashboard:** Capacity Planning (`capacity-planning.json`)

### Webhook Delivery

```
webhook_delivery = histogram of webhook delivery duration by status
```

**Prometheus query:**
```promql
sum(rate(settla_webhook_deliveries_total{status="delivered"}[5m]))
/
sum(rate(settla_webhook_deliveries_total[5m]))
* 100
```

**Dashboard:** API Performance (`api-performance.json`)

---

## Error Budget

### Monthly Budget at 99.9% SLA

| Item | Duration |
|------|----------|
| **Total budget** | 43.2 minutes/month |
| Planned deploys (rolling, 6 replicas) | ~20 minutes/month |
| **Remaining for incidents** | 23.2 minutes/month |

### Error Budget Policy

| Budget remaining | Action |
|-----------------|--------|
| > 50% (> 21.6 min) | Normal development velocity |
| 25% -- 50% (10.8 -- 21.6 min) | Prioritize reliability work, require SRE review for risky changes |
| < 25% (< 10.8 min) | Freeze non-critical deploys, all engineering on reliability |
| Exhausted (0 min) | Full deploy freeze until next month, mandatory post-mortem for every incident |

### Burn Rate Alerts

```promql
# Fast burn: 14.4x budget consumption (exhausts in 3 hours)
settla_sli_error_rate_5m > 14.4 * 0.001  # severity: critical, page on-call

# Slow burn: 3x budget consumption (exhausts in 10 days)
settla_sli_error_rate_1h > 3 * 0.001  # severity: warning, Slack notification
```

---

## Monitoring and Alerting

### Grafana Dashboards

| Dashboard | File | Purpose |
|-----------|------|---------|
| Settla Overview | `settla-overview.json` | Transfer rates, success rates, E2E latency, corridors |
| API Performance | `api-performance.json` | Gateway latency, error rates, auth cache, rate limits, webhooks |
| Capacity Planning | `capacity-planning.json` | TB writes, batch sizes, PG sync lag, NATS queue depth |
| Treasury Health | `treasury-health.json` | Balances, locked funds, flush lag, reserve rates |
| Tenant Health | `tenant-health.json` | Per-tenant transfer rates, success, API usage, positions |

### Alert Routing

| Severity | Channel | Response |
|----------|---------|----------|
| Critical | PagerDuty + Slack #settla-incidents | Acknowledge within 5 min, resolve within 30 min |
| Warning | Slack #settla-alerts | Acknowledge within 1 hour, resolve within 4 hours |
| Info | Slack #settla-monitoring | Review during business hours |

---

## Review Cadence

- **Weekly:** SLI dashboard review in SRE standup
- **Monthly:** Error budget review, SLO attainment report
- **Quarterly:** SLA/SLO revision based on tenant feedback and operational data
