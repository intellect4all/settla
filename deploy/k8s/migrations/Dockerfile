# ============================================================================
# Settla Migration Image (Goose)
# ============================================================================
# A minimal container with:
#   - goose v3 migration tool
#   - The entire db/migrations/ directory baked in
#   - An entrypoint script that runs migrations against all 3 databases
#
# Build context must be the repo root:
#   docker build -f deploy/k8s/migrations/Dockerfile -t settla-migrate:latest .
#
# Or use the Makefile target:
#   make k8s-homelab-migrate-build
# ============================================================================

FROM golang:1.23-alpine AS builder
RUN go install github.com/pressly/goose/v3/cmd/goose@latest

FROM alpine:3.19

COPY --from=builder /go/bin/goose /usr/local/bin/goose

# Copy the migration SQL files (goose format: single file with -- +goose Up/Down)
COPY db/migrations/ledger    /migrations/ledger
COPY db/migrations/transfer  /migrations/transfer
COPY db/migrations/treasury  /migrations/treasury

# Copy the entrypoint script
COPY deploy/k8s/migrations/run-migrations.sh /usr/local/bin/run-migrations.sh
RUN chmod +x /usr/local/bin/run-migrations.sh

ENTRYPOINT ["/usr/local/bin/run-migrations.sh"]
