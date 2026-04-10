# Settla Kubernetes Migration Pipeline (Goose)

Automated database migration pipeline for the Settla homelab k3s deployment using [goose](https://github.com/pressly/goose). Guarantees that **all pending migrations are applied before any application pods start**.

---

## Overview

The migration system has three components:

1. **Migration image** — Docker image with the `goose` binary, `psql` client, and all SQL files from `db/migrations/`
2. **Migration Job** — Kubernetes `Job` that runs goose against all three external PostgreSQL databases
3. **Wait-for-migrations initContainers** — init containers on `settla-server`, `settla-node`, and `webhook` that block on Job completion via `kubectl wait`

Additionally, both `settla-server` and `settla-node` run migrations at startup via the embedded `db/automigrate` package (using goose's Go API with `embed.FS`). This provides a safety net even outside Kubernetes.

---

## Migration File Format

Goose uses a **single file** per migration with `-- +goose Up` / `-- +goose Down` markers:

```sql
-- +goose Up
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL
);

-- +goose Down
DROP TABLE users;
```

### Non-Transactional Migrations (for CONCURRENTLY)

Index-only migrations use `-- +goose NO TRANSACTION` so `CREATE INDEX CONCURRENTLY` works:

```sql
-- +goose NO TRANSACTION

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_name ON users(name);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_users_name;
```

**Rule:** Only migrations containing exclusively `CREATE INDEX` / `DROP INDEX` statements get `NO TRANSACTION`. Migrations mixing schema changes (CREATE TABLE, ALTER TABLE) with indexes keep the default transactional behavior for atomicity.

Currently **9 of 35 migrations** use `NO TRANSACTION`.

---

## Adding New Migrations

```bash
# Create a new migration file (sequential numbering)
make migrate-create DB=transfer NAME=add_my_new_table

# This creates: db/migrations/transfer/000030_add_my_new_table.sql
# Edit the file, add -- +goose Up and -- +goose Down sections.
```

For index-only migrations, add `-- +goose NO TRANSACTION` at the top and use `CONCURRENTLY`:

```bash
make migrate-create DB=transfer NAME=add_performance_indexes
# Edit: add -- +goose NO TRANSACTION at top, use CONCURRENTLY
```

### Testing Locally

```bash
export SETTLA_TRANSFER_DB_MIGRATE_URL="postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable"
make migrate-up
make migrate-status     # Show which migrations are applied
make migrate-down       # Rollback all
make migrate-up         # Re-apply to verify round-trip
```

### Deploy to Homelab

```bash
make k8s-homelab-migrate-build   # Rebuild image with new SQL files
make k8s-homelab-deploy          # Deploy (runs migrations automatically)
make k8s-homelab-migrate-logs    # View migration output
```

---

## Rollback

```bash
# Roll back the most recent migration
goose -dir db/migrations/transfer postgres "$URL" down

# Roll back to a specific version
goose -dir db/migrations/transfer postgres "$URL" down-to 15

# Roll back everything
make migrate-down
```

**Production policy:** prefer forward migrations (write a new migration to reverse the change) over rollbacks.

---

## Troubleshooting

### Migration Job fails in K8s

```bash
kubectl logs -n settla job/settla-migrate --tail=200
```

Common causes:
- **"connection refused"** — MacBook Postgres not running
- **"password authentication failed"** — per-DB password mismatch (check `POSTGRES_TRANSFER_PASSWORD`, `POSTGRES_LEDGER_PASSWORD`, `POSTGRES_TREASURY_PASSWORD`)
- **"cannot run inside a transaction block"** — missing `-- +goose NO TRANSACTION` on a CONCURRENTLY migration

### App pods stuck in `Init:0/1`

Expected — init container is waiting for the Job. Check Job status:

```bash
kubectl get job settla-migrate -n settla
kubectl logs -n settla job/settla-migrate
```

### Re-running migrations

```bash
make k8s-homelab-migrate
```

---

## Quick Reference

```bash
make migrate-up                      # Apply all migrations (local)
make migrate-down                    # Rollback all (local)
make migrate-status                  # Show status (local)
make migrate-create DB=X NAME=Y     # Create new migration

make k8s-homelab-migrate-build      # Build + import image
make k8s-homelab-deploy             # Deploy (runs migrations)
make k8s-homelab-migrate            # Re-run migrations only
make k8s-homelab-migrate-logs       # View Job logs

goose -dir db/migrations/transfer postgres "$URL" status    # Direct CLI
goose -dir db/migrations/transfer postgres "$URL" up        # Direct CLI
```
