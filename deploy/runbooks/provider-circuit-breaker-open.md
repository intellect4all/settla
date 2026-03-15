# Provider Circuit Breaker Open

## When to Use

- Alert: `SettlaProviderCircuitBreakerOpen` firing
- Transfers failing for a specific corridor (e.g., NGN->USDT or GBP->USDT)
- Circuit breaker metric shows open state: `settla_circuit_breaker_state{name="provider-onramp-xxx"} == 1`
- settla-node logs show repeated provider call failures followed by "circuit breaker open" rejections

## Impact

- Transfers routed through the affected provider will fail at the `executing` stage.
- If fallback providers are configured for the corridor, the router should automatically re-route.
- If no fallback is available, the entire corridor is effectively down.
- No data loss; affected transfers will be retried or failed gracefully.

**Severity:** P1 -- Critical (if no fallback available) or P2 -- High (if fallback routing is active).

## Prerequisites

- `kubectl` access to the `settla` namespace
- Access to Prometheus / Grafana dashboards
- Access to settla-node logs
- Knowledge of provider status pages (for external provider health checks)

## Steps

### 1. Identify affected corridor and provider

```bash
# Check which circuit breaker is open from alert labels
# Alert label: name=provider-onramp-{id} or name=provider-offramp-{id}
curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state==1" | jq '.data.result[] | {name: .metric.name, state: .value[1]}'

# Check which corridors use this provider
kubectl -n settla logs statefulset/settla-node --tail=200 | grep -i "circuit breaker\|provider.*open\|provider.*error"
```

### 2. Check provider health

```bash
# Check provider error rates over last 15 minutes
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_provider_requests_total{status='error'}[15m]))by(provider)" | jq '.data.result'

# Check provider latency
curl -s "http://prometheus:9090/api/v1/query?query=histogram_quantile(0.99,sum(rate(settla_provider_latency_seconds_bucket[15m]))by(le,provider))" | jq '.data.result'

# Check the provider's external status page (provider-specific)
# Consult the provider integration docs for status page URLs
```

### 3. Check settla-node logs for specific errors

```bash
# Get detailed error messages from the affected provider
kubectl -n settla logs statefulset/settla-node --tail=500 | grep -i "provider" | grep -i "error\|fail\|timeout\|5[0-9][0-9]\|connection refused"

# Check the timeline of failures leading to CB open
kubectl -n settla logs statefulset/settla-node --since=15m | grep -i "circuit" | head -30
```

### 4. Review circuit breaker configuration

The circuit breaker is configured with:
- **Failure threshold:** 5 consecutive failures to trip open
- **Reset timeout:** 30 seconds before transitioning to half-open
- **Half-open probes:** 2 successful requests required to close the breaker

```bash
# Verify current CB config in settla-node
kubectl -n settla exec statefulset/settla-node-0 -- cat /etc/settla/config.yaml | grep -A 10 "circuit_breaker"
```

### 5. Check fallback routing

```bash
# Verify whether transfers are being re-routed to fallback providers
kubectl -n settla logs statefulset/settla-node --tail=200 | grep -i "fallback\|route.*update\|alternative.*provider"

# Check router scoring — is a fallback provider available for the corridor?
curl -s "http://prometheus:9090/api/v1/query?query=settla_provider_requests_total" | jq '.data.result[] | {provider: .metric.provider, corridor: .metric.corridor}'
```

## Recovery

### Automatic recovery (preferred)

1. **Wait for half-open reset.** After 30 seconds of no failures, the circuit breaker transitions to half-open and sends 2 probe requests to the provider.
2. If probes succeed, the breaker closes automatically. Monitor:
   ```bash
   # Watch CB state transition back to closed (0)
   watch -n 5 'curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state" | jq ".data.result[] | {name: .metric.name, state: .value[1]}"'
   ```
3. Confirm transfers are flowing again:
   ```bash
   curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_provider_requests_total{status='success'}[2m]))by(provider)" | jq '.data.result'
   ```

### If provider is confirmed down

1. Verify fallback routing is active. Transfers should be routed to alternative providers.
2. If no fallback is available, consider temporarily disabling the corridor to prevent repeated failures:
   ```bash
   # Check affected transfer count
   kubectl -n settla exec statefulset/postgres-transfer-0 -- \
     psql -U settla -d settla_transfer -c \
     "SELECT COUNT(*) FROM transfers
      WHERE status = 'executing'
        AND updated_at > NOW() - INTERVAL '30 minutes';"
   ```

### Verify recovery

```bash
# CB state should be 0 (closed)
curl -s "http://prometheus:9090/api/v1/query?query=settla_circuit_breaker_state" | jq '.data.result[] | {name: .metric.name, state: .value[1]}'
# Expected: all values == "0"

# Provider success rate should be normal
curl -s "http://prometheus:9090/api/v1/query?query=sum(rate(settla_provider_requests_total{status='success'}[5m]))by(provider)" | jq '.data.result'
```

## Verification

- [ ] Circuit breaker state is `0` (closed) for all providers
- [ ] Provider error rate back to baseline
- [ ] Transfer completion rate > 99% on Grafana Settla Overview
- [ ] No stuck transfers in `executing` state (see `deploy/runbooks/stuck-transfers.md`)
- [ ] Fallback routing disabled (if it was manually enabled)

## Post-Incident

1. **Root cause:** Was this a provider API outage, network issue, rate limiting, or authentication failure?
2. **Affected scope:** How many transfers failed? Which tenants and corridors were affected?
3. **CB threshold review:** If the provider is inherently flaky, consider:
   - Increasing the failure threshold (e.g., from 5 to 10)
   - Increasing the reset timeout (e.g., from 30s to 60s for known-flaky providers)
   - Adding jitter to the half-open probe timing
4. **Fallback coverage:** Ensure every critical corridor has at least one fallback provider configured.
5. **Tenant notification:** Notify affected tenants if transfers were delayed or failed.
6. **Post-mortem:** Required if CB stayed open > 5 minutes with no fallback available.

## Escalation

- If CB stays open for > 5 minutes and no fallback provider is available, page the on-call engineer.
- If multiple providers have CBs open simultaneously, treat as P1 incident and page the engineering lead.
