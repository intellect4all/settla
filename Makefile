.PHONY: build test lint proto migrate-up migrate-down docker-up docker-down docker-logs docker-reset \
       seed demo clean bench loadtest loadtest-quick loadtest-sustained loadtest-burst loadtest-flood \
       loadtest-multi loadtest-daily soak soak-short chaos report test-integration profile \
       testnet-setup testnet-verify testnet-status provider-mode-mock provider-mode-testnet \
       tyk-setup openapi-export docs-openapi docs-dev docs-build api-test api-test-full \
       bench-smoke bench-sustained bench-peak bench-soak bench-spike bench-hotspot \
       bench-tenants-20k bench-tenants-100k bench-tenants-peak bench-settlement \
       bench-micro bench-all bench-report bench-seed bench-seed-20k bench-seed-100k bench-cleanup

# Go
GO := go
GOFLAGS := -race

# Binaries
BIN_DIR := bin
SERVER_BIN := $(BIN_DIR)/settla-server
NODE_BIN := $(BIN_DIR)/settla-node

COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env

# Load test gateway URL — override via env or make argument
GATEWAY_PORT ?= $(shell grep -m1 '^SETTLA_GATEWAY_PORT=' .env 2>/dev/null | cut -d= -f2 | tr -d ' ')
GATEWAY_PORT := $(or $(GATEWAY_PORT),3100)
GATEWAY_URL ?= http://localhost:$(GATEWAY_PORT)

## build: Compile all Go binaries
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(SERVER_BIN) ./cmd/settla-server/...
	$(GO) build -o $(NODE_BIN) ./cmd/settla-node/...

## test: Run all Go tests with race detector
test:
	$(GO) test $(GOFLAGS) ./...

## test-integration: Run end-to-end integration tests
test-integration:
	$(GO) test $(GOFLAGS) -tags=integration -timeout=5m ./tests/integration/...

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## proto: Generate protobuf stubs (Go + TypeScript via buf)
proto:
	rm -rf gen/settla api/gateway/src/gen
	buf lint
	buf generate

## migrate-up: Run all database migrations (uses raw Postgres, not PgBouncer)
migrate-up:
	migrate -path db/migrations/ledger -database "$${SETTLA_LEDGER_DB_MIGRATE_URL}" up
	migrate -path db/migrations/transfer -database "$${SETTLA_TRANSFER_DB_MIGRATE_URL}" up
	migrate -path db/migrations/treasury -database "$${SETTLA_TREASURY_DB_MIGRATE_URL}" up

## migrate-down: Rollback all database migrations
migrate-down:
	migrate -path db/migrations/treasury -database "$${SETTLA_TREASURY_DB_MIGRATE_URL}" down
	migrate -path db/migrations/transfer -database "$${SETTLA_TRANSFER_DB_MIGRATE_URL}" down
	migrate -path db/migrations/ledger -database "$${SETTLA_LEDGER_DB_MIGRATE_URL}" down

## migrate-create: Create a new migration (usage: make migrate-create DB=ledger NAME=add_foo)
migrate-create:
	migrate create -ext sql -dir db/migrations/$(DB) -seq $(NAME)

## sqlc-generate: Generate Go code from SQL queries
sqlc-generate:
	cd db && sqlc generate

## db-seed: Load seed data into all databases (uses raw Postgres, not PgBouncer)
db-seed:
	psql "$${SETTLA_TRANSFER_DB_MIGRATE_URL}" -f db/seed/transfer_seed.sql
	psql "$${SETTLA_TRANSFER_DB_MIGRATE_URL}" -f db/seed/crypto_seed.sql
	psql "$${SETTLA_LEDGER_DB_MIGRATE_URL}" -f db/seed/ledger_seed.sql
	psql "$${SETTLA_TREASURY_DB_MIGRATE_URL}" -f db/seed/treasury_seed.sql

## docker-up: Start all services (infra + app)
docker-up:
	$(COMPOSE) up -d --build

## docker-down: Stop all services
docker-down:
	$(COMPOSE) down

## docker-logs: Tail logs from all services
docker-logs:
	$(COMPOSE) logs -f

## docker-reset: Clean slate — remove volumes and rebuild
docker-reset:
	$(COMPOSE) down -v
	$(COMPOSE) up -d --build

## bench: Run all Go benchmarks and compare against targets
bench:
	$(GO) test ./... -bench=Benchmark -benchmem -benchtime=5s -run=^$$ -count=1 | tee bench-results.txt
	@echo "Results written to bench-results.txt"
	@echo ""
	@echo "=== Threshold Comparison ==="
	python3 scripts/parse_benchmarks.py < bench-results.txt

## loadtest: Peak load test (5,000 TPS for 10 minutes)
loadtest:
	$(GO) run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=10 -gateway=$(GATEWAY_URL) -drain=300s

## loadtest-quick: Quick load test (1,000 TPS for 2 minutes, CI-friendly)
# Passes DB URLs so the post-test verification step can check:
#   - Outbox fully drained (zero unpublished entries)
#   - Debits = credits across all ledger accounts
#   - Zero stuck transfers in non-terminal state
# Override TRANSFER_DB_URL / LEDGER_DB_URL to point at a remote DB.
TRANSFER_DB_URL ?= $(shell grep -m1 '^SETTLA_TRANSFER_DB_URL=' .env 2>/dev/null | cut -d= -f2 | tr -d ' ')
LEDGER_DB_URL   ?= $(shell grep -m1 '^SETTLA_LEDGER_DB_URL=' .env 2>/dev/null | cut -d= -f2 | tr -d ' ')
loadtest-quick:
	$(GO) run ./tests/loadtest/ -tps=1000 -duration=2m -tenants=5 -gateway=$(GATEWAY_URL) -drain=300s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## loadtest-sustained: Sustained load test (600 TPS for 30 minutes)
loadtest-sustained:
	$(GO) run ./tests/loadtest/ -tps=600 -duration=30m -tenants=10 -gateway=$(GATEWAY_URL) -drain=300s

## loadtest-burst: Burst recovery test (ramp 600→8000→600 TPS)
loadtest-burst:
	@echo "Burst test: ramping 600→8000→600 TPS"
	$(GO) run ./tests/loadtest/ -tps=8000 -duration=5m -tenants=20 -rampup=2m -gateway=$(GATEWAY_URL) -drain=300s

## loadtest-flood: Single tenant flood test (3,000 TPS, one tenant)
loadtest-flood:
	$(GO) run ./tests/loadtest/ -tps=3000 -duration=5m -tenants=1 -gateway=$(GATEWAY_URL) -drain=300s

## loadtest-multi: Multi-tenant scale test (50 tenants × 100 TPS)
loadtest-multi:
	$(GO) run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=50 -gateway=$(GATEWAY_URL) -drain=300s

## loadtest-daily: Simulated daily volume (580 TPS for 1 hour = 2.1M transfers)
loadtest-daily:
	$(GO) run ./tests/loadtest/ -tps=580 -duration=1h -tenants=50 -gateway=$(GATEWAY_URL) -drain=600s \
		-verify-ledger -verify-settlements

## soak: 2-hour soak test at 1,000 TPS (with health monitoring)
soak:
	$(GO) run ./tests/loadtest/ -soak -tps=1000 -duration=2h -tenants=10 -gateway=$(GATEWAY_URL) -drain=600s

## soak-short: 15-minute soak test at 1,000 TPS (CI-feasible)
soak-short:
	$(GO) run ./tests/loadtest/ -soak -tps=1000 -duration=15m -tenants=5 -gateway=$(GATEWAY_URL) -drain=300s

## chaos: Run all chaos test scenarios
chaos:
	$(GO) run ./tests/chaos/ -gateway=$(GATEWAY_URL)

## report: Generate full benchmark report (unit benchmarks + load test + outbox metrics)
report:
	@mkdir -p tests/reports
	bash scripts/generate-report.sh

## demo: Run interactive demo scenario
demo:
	bash scripts/demo.sh

## demo-up: Start full demo environment (builds, seeds, prints URLs)
demo-up:
	bash scripts/demo-up.sh

## demo-up-scale: Start demo with 20K tenants for scale demonstration
demo-up-scale:
	bash scripts/demo-up.sh --profile=scale

## demo-down: Stop demo environment and remove volumes
demo-down:
	bash scripts/demo-down.sh

## demo-reset: Reset demo data without restarting containers
demo-reset:
	bash scripts/demo-reset.sh

## demo-status: Show health status of all demo services
demo-status:
	bash scripts/demo-status.sh

## demo-seed-quick: Seed 10 tenants (quick profile)
demo-seed-quick:
	bash scripts/demo-seed.sh --profile=quick

## demo-seed-scale: Seed 20K tenants (scale profile)
demo-seed-scale:
	bash scripts/demo-seed.sh --profile=scale

## demo-seed-stress: Seed 100K tenants (stress profile)
demo-seed-stress:
	bash scripts/demo-seed.sh --profile=stress

## demo-logs: Tail application logs
demo-logs:
	bash scripts/demo-logs.sh

## demo-scale-check: Verify scale-test tenant provisioning
demo-scale-check:
	bash scripts/demo-scale-check.sh

## demo-record: Record terminal session for demo
demo-record:
	bash scripts/demo-record.sh

## profile: Capture and compare profiles from running settla-server
profile:
	@mkdir -p tests/loadtest/profiles
	@echo "Capturing heap profile..."
	curl -s http://localhost:6060/debug/pprof/heap > tests/loadtest/profiles/heap-manual.prof
	@echo "Capturing goroutine profile..."
	curl -s http://localhost:6060/debug/pprof/goroutine > tests/loadtest/profiles/goroutine-manual.prof
	@echo "Capturing 30s CPU profile..."
	curl -s http://localhost:6060/debug/pprof/profile?seconds=30 > tests/loadtest/profiles/cpu-manual.prof
	@echo "Profiles saved to tests/loadtest/profiles/"
	@echo "Analyze with: go tool pprof tests/loadtest/profiles/<file>.prof"

## testnet-setup: Initialize testnet wallets and fund from faucets
testnet-setup:
	bash scripts/testnet-setup.sh

## testnet-verify: Verify testnet RPC connectivity and wallet status
testnet-verify:
	bash scripts/testnet-verify.sh

## testnet-status: Show testnet wallet addresses and explorer links
testnet-status:
	@if [ -f .env ]; then set -a && . ./.env && set +a; fi && \
	$(GO) run ./cmd/testnet-tools/ status

## provider-mode-mock: Set provider mode to mock (no blockchain)
provider-mode-mock:
	@if [ -f .env ]; then \
		if grep -q '^SETTLA_PROVIDER_MODE=' .env; then \
			sed -i'' -e 's/^SETTLA_PROVIDER_MODE=.*/SETTLA_PROVIDER_MODE=mock/' .env; \
		else \
			echo 'SETTLA_PROVIDER_MODE=mock' >> .env; \
		fi; \
		echo "Provider mode set to: mock"; \
	else \
		echo "ERROR: .env file not found. Run: cp .env.example .env"; \
		exit 1; \
	fi

## provider-mode-testnet: Set provider mode to testnet (real blockchain)
provider-mode-testnet:
	@if [ -f .env ]; then \
		if grep -q '^SETTLA_PROVIDER_MODE=' .env; then \
			sed -i'' -e 's/^SETTLA_PROVIDER_MODE=.*/SETTLA_PROVIDER_MODE=testnet/' .env; \
		else \
			echo 'SETTLA_PROVIDER_MODE=testnet' >> .env; \
		fi; \
		echo "Provider mode set to: testnet"; \
	else \
		echo "ERROR: .env file not found. Run: cp .env.example .env"; \
		exit 1; \
	fi

## tyk-setup: Create Tyk API keys for seed tenants (run after docker-up)
tyk-setup:
	@bash scripts/tyk-setup.sh

## openapi-export: Export OpenAPI spec from gateway
openapi-export:
	pnpm --filter @settla/gateway run export-openapi

## docs-openapi: Export OpenAPI spec and copy to docs-site
docs-openapi: openapi-export
	cp api/gateway/openapi.json docs-site/openapi.json

## docs-dev: Run Mintlify dev server locally
docs-dev:
	cd docs-site && npx mintlify dev

## docs-build: Build the docs site
docs-build:
	cd docs-site && npx mintlify build

## api-test: Run tenant API tests against running services
api-test:
	$(GO) run ./tests/api-test/ -gateway=$(GATEWAY_URL)

## api-test-full: Start Docker, seed, then run tenant API tests
api-test-full:
	$(COMPOSE) up -d --build
	@echo "Waiting for gateway health..."
	@for i in $$(seq 1 24); do \
		curl -sf $(GATEWAY_URL)/health > /dev/null 2>&1 && break; \
		echo "  Attempt $$i/24 — retrying in 5s..."; \
		sleep 5; \
	done
	$(MAKE) db-seed
	$(GO) run ./tests/api-test/ -gateway=$(GATEWAY_URL)

## init-testnet-wallets: Generate wallet encryption key and master seed for testnet mode
init-testnet-wallets:
	@bash scripts/init-testnet-wallets.sh

## fund-testnet-wallets: Print instructions for funding testnet wallets
fund-testnet-wallets:
	@bash scripts/fund-testnet-wallets.sh

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR) bench-results.txt

# ===========================================================================
# Scenario-based load test suite (A-J)
# All scenarios output structured JSON results to tests/loadtest/results/
# ===========================================================================

LOADTEST_CMD = $(GO) run ./tests/loadtest/
SEED_CMD = $(GO) run ./tests/loadtest/ seed

## bench-seed: Provision scale-test tenants (default 50, set TENANT_COUNT for more)
TENANT_COUNT ?= 50
bench-seed:
	$(SEED_CMD) -count=$(TENANT_COUNT) \
		-transfer-db=$${SETTLA_TRANSFER_DB_MIGRATE_URL} \
		-treasury-db=$${SETTLA_TREASURY_DB_MIGRATE_URL}

## bench-seed-20k: Provision 20,000 scale-test tenants
bench-seed-20k:
	$(SEED_CMD) -count=20000 \
		-transfer-db=$${SETTLA_TRANSFER_DB_MIGRATE_URL} \
		-treasury-db=$${SETTLA_TREASURY_DB_MIGRATE_URL}

## bench-seed-100k: Provision 100,000 scale-test tenants
bench-seed-100k:
	$(SEED_CMD) -count=100000 \
		-transfer-db=$${SETTLA_TRANSFER_DB_MIGRATE_URL} \
		-treasury-db=$${SETTLA_TREASURY_DB_MIGRATE_URL}

## bench-cleanup: Remove all scale-test tenants
bench-cleanup:
	$(SEED_CMD) -cleanup \
		-transfer-db=$${SETTLA_TRANSFER_DB_MIGRATE_URL} \
		-treasury-db=$${SETTLA_TREASURY_DB_MIGRATE_URL}

## bench-smoke: Scenario A — 10 TPS for 60s, single tenant, sanity check
bench-smoke:
	$(LOADTEST_CMD) -scenario=SmokeTest -gateway=$(GATEWAY_URL) -json -drain=30s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-sustained: Scenario B — 580 TPS sustained, 50 tenants, multi-currency
bench-sustained:
	$(LOADTEST_CMD) -scenario=SustainedLoad -gateway=$(GATEWAY_URL) -json -drain=120s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-peak: Scenario C — 5,000 TPS peak burst, 200 tenants
bench-peak:
	$(LOADTEST_CMD) -scenario=PeakBurst -gateway=$(GATEWAY_URL) -json -drain=120s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-soak: Scenario D — 580 TPS for 1 hour, resource stability
bench-soak:
	$(LOADTEST_CMD) -scenario=SoakTest -gateway=$(GATEWAY_URL) -json -soak -drain=120s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-spike: Scenario E — instant 100→5,000→100 TPS spike
bench-spike:
	$(LOADTEST_CMD) -scenario=SpikeTest -gateway=$(GATEWAY_URL) -json -drain=60s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-hotspot: Scenario F — 580 TPS, 80% to one tenant
bench-hotspot:
	$(LOADTEST_CMD) -scenario=HotSpot -gateway=$(GATEWAY_URL) -json -drain=60s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-tenants-20k: Scenario G — 580 TPS across 20K tenants (Zipf)
bench-tenants-20k:
	$(LOADTEST_CMD) -scenario=TenantScale20K -gateway=$(GATEWAY_URL) -json -drain=120s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-tenants-100k: Scenario H — 580 TPS across 100K tenants (Zipf)
bench-tenants-100k:
	$(LOADTEST_CMD) -scenario=TenantScale100K -gateway=$(GATEWAY_URL) -json -drain=120s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-tenants-peak: Scenario I — 5,000 TPS across 20K tenants (Zipf)
bench-tenants-peak:
	$(LOADTEST_CMD) -scenario=TenantScalePeak -gateway=$(GATEWAY_URL) -json -drain=120s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-settlement: Scenario J — 580 TPS for 1h then settlement batch across 20K tenants
bench-settlement:
	$(LOADTEST_CMD) -scenario=SettlementBatch -gateway=$(GATEWAY_URL) -json -drain=300s \
		$(if $(TRANSFER_DB_URL),-transfer-db=$(TRANSFER_DB_URL)) \
		$(if $(LEDGER_DB_URL),-ledger-db=$(LEDGER_DB_URL))

## bench-micro: Run component-level microbenchmarks (Go testing.B)
bench-micro:
	@echo "=== Component Microbenchmarks ==="
	$(GO) test -bench=Benchmark -benchmem -benchtime=3s -run='^$$' -count=1 ./tests/loadtest/ | tee tests/loadtest/results/micro-benchmarks.txt
	$(GO) test -bench=Benchmark -benchmem -benchtime=3s -run='^$$' -count=1 ./treasury/... | tee -a tests/loadtest/results/micro-benchmarks.txt
	$(GO) test -bench=Benchmark -benchmem -benchtime=3s -run='^$$' -count=1 ./cache/... | tee -a tests/loadtest/results/micro-benchmarks.txt
	$(GO) test -bench=Benchmark -benchmem -benchtime=3s -run='^$$' -count=1 ./ledger/... | tee -a tests/loadtest/results/micro-benchmarks.txt
	$(GO) test -bench=Benchmark -benchmem -benchtime=3s -run='^$$' -count=1 ./core/... | tee -a tests/loadtest/results/micro-benchmarks.txt
	$(GO) test -bench=Benchmark -benchmem -benchtime=3s -run='^$$' -count=1 ./node/outbox/... | tee -a tests/loadtest/results/micro-benchmarks.txt
	@echo "Results written to tests/loadtest/results/micro-benchmarks.txt"

## bench-all: Run all scenarios in sequence (A through J + microbenchmarks)
bench-all: bench-micro bench-smoke bench-sustained bench-peak bench-spike bench-hotspot bench-soak
	@echo ""
	@echo "=== Core scenarios complete. Scale tests require pre-provisioned tenants. ==="
	@echo "Run 'make bench-seed-20k' then 'make bench-tenants-20k bench-tenants-peak bench-settlement'"
	@echo "Run 'make bench-seed-100k' then 'make bench-tenants-100k'"

## bench-report: Aggregate all JSON results into a single report
bench-report:
	@mkdir -p tests/loadtest/results
	$(LOADTEST_CMD) report tests/loadtest/results tests/loadtest/results/aggregate-report.json
	@echo "Aggregate report: tests/loadtest/results/aggregate-report.json"
	@echo "Report template: tests/loadtest/REPORT_TEMPLATE.md"
