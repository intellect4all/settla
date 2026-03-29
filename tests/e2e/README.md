# End-to-End Test Suite

Automated e2e tests covering every critical user journey in Settla — from API call to terminal state.

## Quick Start

```bash
# 1. Start all services
make docker-up

# 2. Seed test data
make db-seed

# 3. Run e2e tests
go test -tags e2e ./tests/e2e/ -v -timeout 10m

# 4. Run only consistency checks (post-test)
go test -tags e2e -run TestConsistency ./tests/e2e/ -v
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `GATEWAY_URL` | `http://localhost:3100` | Gateway base URL |
| `E2E_SEED_API_KEY` | `sk_live_lemfi_demo_key` | Seed tenant A API key |
| `E2E_SEED_API_KEY_B` | `sk_live_fincra_demo_key` | Seed tenant B API key |
| `SETTLA_OPS_API_KEY` | `settla-ops-secret-change-me` | Ops API key |
| `E2E_REPORT_DIR` | (none) | Directory for JSON reports |

## Test Files

| File | Domain | Tests |
|---|---|---|
| `transfers_test.go` | Transfer lifecycle | Create, retrieve, corridors, concurrent creation, terminal state polling, verification |
| `quotes_test.go` | Quotes | Create, retrieve, cache consistency, quote-linked transfer, routing options |
| `deposits_test.go` | Crypto & bank deposits | Session create/cancel/list, idempotency, public status, auto-convert, virtual accounts |
| `payment_links_test.go` | Payment links | Create, resolve, redeem, use limits, disable |
| `settlement_test.go` | Settlement & treasury | Positions, liquidity, topup/withdraw, multi-tenant independence |
| `tenants_test.go` | Tenant lifecycle | Profile, API keys, webhooks, crypto settings, analytics, rapid onboarding, isolation |
| `negative_test.go` | Error paths | Malformed requests, invalid currencies, auth errors, cross-tenant access, rate limiting, idempotency conflicts |
| `consistency_test.go` | Post-test checks | Stuck transfers, deposit/bank-deposit integrity, reconciliation, webhook health |

## Running Specific Tests

```bash
# Run transfer tests only
go test -tags e2e -run TestTransfer ./tests/e2e/ -v

# Run negative tests only
go test -tags e2e -run TestNegative ./tests/e2e/ -v

# Run all tenant isolation tests
go test -tags e2e -run TestTenant_Isolation ./tests/e2e/ -v

# Run consistency checker with JSON output
E2E_REPORT_DIR=./tests/e2e go test -tags e2e -run TestConsistency_FullCheck ./tests/e2e/ -v
```

## Running Against Staging

```bash
GATEWAY_URL=https://staging-api.settla.io \
E2E_SEED_API_KEY=sk_live_staging_key \
go test -tags e2e ./tests/e2e/ -v -timeout 10m
```

## Parallelism

Tests within independent domains (transfers, deposits, payment links) can run in parallel.
Use `go test -parallel N` to control parallelism. Tests that share state (settlement, consistency) run sequentially by default.

## Journeys That Cannot Be Fully Automated

| Journey | Reason | Workaround |
|---|---|---|
| Full crypto deposit lifecycle | Requires real on-chain transactions (chain monitor detects real txs) | Covered by `tests/integration/deposit_e2e_test.go` with mock providers |
| Full bank deposit lifecycle | Requires real bank credit webhook from banking partner | Covered by `tests/integration/bank_deposit_test.go` with mock providers |
| Blockchain confirmation | Requires waiting for real block confirmations (minutes) | Mock mode confirms instantly in integration tests |
| Tenant suspension | Requires admin API access that could affect other tests | Documented in `TestNegative_SuspendedTenantNote` |
| HMAC webhook signature verification | Requires a running webhook receiver to inspect headers | Covered by `api/webhook` unit tests |
| DLQ dead-letter after max retries | Requires failing webhook delivery N times | Covered by integration tests |

## CI Integration

```yaml
# GitHub Actions example
- name: Run e2e tests
  run: |
    make docker-up
    make db-seed
    E2E_REPORT_DIR=./test-results \
    go test -tags e2e ./tests/e2e/ -v -timeout 10m \
      -json > ./test-results/e2e.json
  env:
    GATEWAY_URL: http://localhost:3100
```

## Architecture

Tests use the HTTP REST API (Fastify gateway) exclusively — no direct gRPC or database access. This tests the full stack: gateway → gRPC → engine → outbox → workers → state transitions.

The consistency checker runs the same API calls but focuses on data integrity: stuck transfers, invalid states, accessible endpoints. It can be run independently as a post-deployment smoke test.
