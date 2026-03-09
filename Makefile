.PHONY: build test lint proto migrate-up migrate-down docker-up docker-down docker-logs docker-reset \
       seed demo clean bench loadtest loadtest-quick loadtest-sustained loadtest-burst loadtest-flood \
       loadtest-multi soak soak-short chaos report test-integration profile \
       testnet-setup testnet-verify testnet-status provider-mode-mock provider-mode-testnet

# Go
GO := go
GOFLAGS := -race

# Binaries
BIN_DIR := bin
SERVER_BIN := $(BIN_DIR)/settla-server
NODE_BIN := $(BIN_DIR)/settla-node

COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env

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
	$(GO) run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=10

## loadtest-quick: Quick load test (1,000 TPS for 2 minutes, CI-friendly)
loadtest-quick:
	$(GO) run ./tests/loadtest/ -tps=1000 -duration=2m -tenants=5

## loadtest-sustained: Sustained load test (600 TPS for 30 minutes)
loadtest-sustained:
	$(GO) run ./tests/loadtest/ -tps=600 -duration=30m -tenants=10

## loadtest-burst: Burst recovery test (ramp 600→8000→600 TPS)
loadtest-burst:
	@echo "Burst test: ramping 600→8000→600 TPS"
	$(GO) run ./tests/loadtest/ -tps=8000 -duration=5m -tenants=20 -rampup=2m

## loadtest-flood: Single tenant flood test (3,000 TPS, one tenant)
loadtest-flood:
	$(GO) run ./tests/loadtest/ -tps=3000 -duration=5m -tenants=1

## loadtest-multi: Multi-tenant scale test (50 tenants × 100 TPS)
loadtest-multi:
	$(GO) run ./tests/loadtest/ -tps=5000 -duration=10m -tenants=50

## soak: 2-hour soak test at 1,000 TPS (with health monitoring)
soak:
	$(GO) run ./tests/loadtest/ -soak -tps=1000 -duration=2h -tenants=10

## soak-short: 15-minute soak test at 1,000 TPS (CI-feasible)
soak-short:
	$(GO) run ./tests/loadtest/ -soak -tps=1000 -duration=15m -tenants=5

## chaos: Run all chaos test scenarios
chaos:
	$(GO) run ./tests/chaos/

## report: Generate full benchmark report (runs bench + loadtest-quick + soak-short)
report:
	@mkdir -p tests/reports
	$(MAKE) bench
	$(MAKE) loadtest-quick
	$(MAKE) soak-short
	@echo "Report generated at tests/reports/benchmark-report.md"

## demo: Run interactive demo scenario
demo:
	bash scripts/demo.sh

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

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR) bench-results.txt
