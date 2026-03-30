# Chapter 3.5: The Data Layer

**Reading time:** ~30 minutes
**Prerequisites:** Chapters 3.1-3.4
**Code references:** `db/queries/transfer/transfers.sql`, `store/transferdb/adapter.go`,
`store/transferdb/outbox_store.go`, `core/store.go`

---

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why Settla uses SQLC instead of an ORM and what tradeoffs that
   involves.
2. Read and write SQLC query annotations and understand the generated code
   they produce.
3. Describe the adapter pattern that bridges SQLC-generated code to the
   engine's `TransferStore` interface.
4. Walk through the `TransitionWithOutbox` implementation to see how atomic
   state transitions work at the SQL level.
5. Explain optimistic locking, PII encryption, and RLS tenant isolation in
   the data layer.

---

## 3.5.1 Why SQLC Over ORMs

Settla uses SQLC -- a tool that generates type-safe Go code from SQL queries.
You write SQL, SQLC writes Go. This is a deliberate choice over ORMs like
GORM or Ent.

### The ORM Problem at Scale

At 50M transfers/day (580 TPS sustained), an ORM introduces three risks:

```
ORM Risks at Scale
====================

Risk                What happens                      Settla's answer
------------------  --------------------------------  ----------------------
N+1 queries         ORM lazily loads associations     SQLC: you write the
                    generating 1 query per row.       exact SQL. No hidden
                    At 580 TPS, this kills the DB.    queries.

Query unpredictability  ORM generates SQL you cannot   SQLC: the SQL in the
                    predict. A small Go code change   .sql file IS the query.
                    might generate a table scan.      No surprises.

Impedance mismatch  ORM maps objects to tables.       SQLC: maps queries to
                    Complex queries (joins, CTEs,     functions. Any SQL
                    window functions) fight the       PostgreSQL supports,
                    abstraction.                      SQLC supports.
```

### The SQLC Workflow

```
SQLC Code Generation Pipeline
================================

1. You write SQL in db/queries/transfer/transfers.sql:

   -- name: GetTransfer :one
   SELECT * FROM transfers
   WHERE id = $1 AND tenant_id = $2;

2. You run: make sqlc-generate (or: cd db && sqlc generate)

3. SQLC reads the SQL + your schema + sqlc.yaml config

4. SQLC generates Go code in store/transferdb/:
   - models.go      (struct Transfer matching the table columns)
   - querier.go     (interface with GetTransfer method)
   - transfers.sql.go (implementation of GetTransfer)
```

The generated code is never edited by hand. If you need to change a query,
you edit the `.sql` file and regenerate.

---

## 3.5.2 The Actual SQL Queries

Here are the key queries from `db/queries/transfer/transfers.sql` with
annotations explaining each SQLC directive:

### CreateTransfer

```sql
-- name: CreateTransfer :one
INSERT INTO transfers (
    tenant_id, external_ref, idempotency_key, status,
    source_currency, source_amount, dest_currency, dest_amount,
    stable_coin, stable_amount, chain, fx_rate, fees,
    sender, recipient, quote_id,
    on_ramp_provider_id, off_ramp_provider_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18
) RETURNING *;
```

- `-- name: CreateTransfer` -- this becomes the Go function name
- `:one` -- tells SQLC this returns a single row (from `RETURNING *`)
- `$1` through `$18` -- positional parameters, mapped to a generated
  `CreateTransferParams` struct

SQLC generates:

```go
// In store/transferdb/transfers.sql.go (generated, do not edit)
type CreateTransferParams struct {
    TenantID          uuid.UUID
    ExternalRef       pgtype.Text
    IdempotencyKey    pgtype.Text
    Status            TransferStatusEnum
    SourceCurrency    string
    SourceAmount      pgtype.Numeric
    DestCurrency      string
    DestAmount        pgtype.Numeric
    StableCoin        pgtype.Text
    StableAmount      pgtype.Numeric
    Chain             pgtype.Text
    FxRate            pgtype.Numeric
    Fees              []byte
    Sender            []byte
    Recipient         []byte
    QuoteID           pgtype.UUID
    OnRampProviderID  pgtype.Text
    OffRampProviderID pgtype.Text
}

func (q *Queries) CreateTransfer(ctx context.Context, arg CreateTransferParams) (Transfer, error) {
    // ... executes the SQL and scans the result into a Transfer struct ...
}
```

### GetTransfer (Tenant-Scoped)

```sql
-- name: GetTransfer :one
SELECT * FROM transfers
WHERE id = $1 AND tenant_id = $2;
```

Every query that returns tenant data includes `AND tenant_id = $2`. This is
the fundamental tenant isolation guarantee. Even if an attacker guesses a
valid transfer ID, they cannot access it without the correct tenant ID.

### GetTransferByIdempotencyKey (Time-Windowed)

```sql
-- name: GetTransferByIdempotencyKey :one
SELECT * FROM transfers
WHERE tenant_id = $1 AND idempotency_key = $2
  AND created_at >= now() - INTERVAL '24 hours'
LIMIT 1;
```

The 24-hour window is a pragmatic choice. Idempotency keys must be unique
within a tenant, but enforcing uniqueness forever would require an
ever-growing unique index. The time window keeps the index manageable while
providing sufficient deduplication protection.

### UpdateTransferStatusWithVersion (Optimistic Lock)

```sql
-- name: UpdateTransferStatusWithVersion :exec
UPDATE transfers
SET status = $3,
    version = version + 1,
    updated_at = now()
WHERE id = $1 AND tenant_id = $2 AND version = $4;
```

- `:exec` -- no return value (the engine checks `RowsAffected()`)
- `AND version = $4` -- the optimistic lock. If another transaction already
  incremented the version, this UPDATE matches zero rows and the engine
  returns `ErrOptimisticLock`.

### UpdateTransferStatus (With Timestamp Triggers)

```sql
-- name: UpdateTransferStatus :exec
UPDATE transfers
SET status = $3,
    version = version + 1,
    updated_at = now(),
    funded_at = CASE WHEN $3::text = 'FUNDED' THEN now() ELSE funded_at END,
    completed_at = CASE WHEN $3::text = 'COMPLETED' THEN now() ELSE completed_at END,
    failed_at = CASE WHEN $3::text = 'FAILED' THEN now() ELSE failed_at END
WHERE id = $1 AND tenant_id = $2;
```

The `CASE` expressions set milestone timestamps only when the status matches.
This avoids separate UPDATE statements for each milestone and keeps the
transition atomic.

### SumDailyVolumeByTenant

```sql
-- name: SumDailyVolumeByTenant :one
SELECT COALESCE(SUM(source_amount), 0)::NUMERIC(28,8) AS total_volume
FROM transfers
WHERE tenant_id = $1
  AND created_at >= $2
  AND created_at < $3
  AND status NOT IN ('FAILED', 'REFUNDED');
```

- `COALESCE(..., 0)` -- returns 0 for new tenants with no transfers
- `NUMERIC(28,8)` -- 28 digits total, 8 decimal places. Enough for
  billions with sub-cent precision
- `NOT IN ('FAILED', 'REFUNDED')` -- failed transfers do not count
  toward the daily limit

### ListTransfersByTenant (Paginated)

```sql
-- name: ListTransfersByTenant :many
SELECT * FROM transfers
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
```

- `:many` -- tells SQLC this returns multiple rows
- `ORDER BY created_at DESC` -- newest first
- `LIMIT $2 OFFSET $3` -- standard cursor pagination

### Provider Transaction Claiming

```sql
-- name: ClaimProviderTransaction :one
INSERT INTO provider_transactions (
    tenant_id, provider, tx_type,
    transfer_id, status, amount, currency, metadata
) VALUES (
    @tenant_id, @provider, @tx_type,
    @transfer_id, 'claiming', 0, '', '{}'
)
ON CONFLICT (tenant_id, transfer_id, tx_type) DO NOTHING
RETURNING id;
```

This is the database-level implementation of CHECK-BEFORE-CALL. The
`ON CONFLICT DO NOTHING` ensures atomic claim semantics -- only one worker
can successfully claim a given `(tenant_id, transfer_id, tx_type)` tuple.

---

## 3.5.2a Expanded Query Catalog

The examples above focused on `transfers.sql`, but the data layer spans three
databases with many more query files. As the platform has grown to include
crypto deposits, bank deposits, payment links, position management, and webhook
auditing, the SQLC query catalog has grown with it. Here is the complete
inventory.

### Transfer DB Queries (`db/queries/transfer/`)

| File | Purpose |
|------|---------|
| `transfers.sql` | Transfer CRUD, status updates with optimistic locking, pagination, daily volume aggregation |
| `analytics.sql` | Aggregations for analytics snapshots and data exports |
| `bank_deposit_sessions.sql` | Bank deposit session lifecycle (create, transition, expiry detection) |
| `bank_deposit_transactions.sql` | Individual bank credit records linked to deposit sessions |
| `banking_partners.sql` | Banking partner configuration for virtual account provisioning |
| `block_checkpoints.sql` | Chain monitor block height tracking per chain/token |
| `crypto_addresses.sql` | HD-derived deposit address pool management |
| `crypto_deposits.sql` | Crypto deposit session lifecycle (create, detect, confirm, credit, settle) |
| `operational.sql` | Operational queries for the ops dashboard |
| `outbox.sql` | Outbox entry insertion, polling, and batch publishing |
| `payment_links.sql` | Payment link creation, redemption, and listing |
| `portal_users.sql` | Tenant portal user authentication and management |
| `position_transactions.sql` | Position top-ups, withdrawals, and internal rebalancing *(new)* |
| `provider_webhook_logs.sql` | Inbound webhook audit trail with deduplication *(new)* |
| `reconciliation.sql` | Reconciliation check queries (balance drift, orphan detection) |
| `settlement.sql` | Net settlement calculation, cycle management, and settlement records |
| `tenant_portal.sql` | Tenant self-service portal queries (KYB, settings) |
| `tenants.sql` | Tenant and API key management, fee schedule lookups |
| `tokens.sql` | Refresh token management for portal authentication |
| `virtual_accounts.sql` | Virtual account pool provisioning and assignment |
| `webhook_management.sql` | Outbound webhook endpoint configuration per tenant |

### Treasury DB Queries (`db/queries/treasury/`)

| File | Purpose |
|------|---------|
| `treasury.sql` | Position snapshots, balance updates, position history |
| `position_events.sql` | Event-sourced position audit trail with batch inserts *(new)* |

### Ledger DB Queries (`db/queries/ledger/`)

| File | Purpose |
|------|---------|
| `ledger.sql` | Account management, journal entries, entry lines for the CQRS read model |

### Examples from the New Query Files

**Position transactions** (`position_transactions.sql`) follow the same
tenant-scoped patterns as transfers. Every query includes `AND tenant_id`:

```sql
-- name: GetPositionTransaction :one
SELECT * FROM position_transactions
WHERE id = $1 AND tenant_id = $2;

-- name: ListPositionTransactionsByTenantAndStatus :many
SELECT * FROM position_transactions
WHERE tenant_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountPositionTransactionsByTenant :one
SELECT count(*) FROM position_transactions
WHERE tenant_id = $1;
```

- `:one` returns a single row -- used for lookups and counts
- `:many` returns a slice -- used for paginated listings
- Every query scopes to `tenant_id` for isolation

**Provider webhook logs** (`provider_webhook_logs.sql`) use `ON CONFLICT`
for idempotent deduplication, the same pattern as provider transaction
claiming:

```sql
-- name: InsertProviderWebhookLog :one
INSERT INTO provider_webhook_logs (
    provider_slug, idempotency_key, raw_body, http_headers, source_ip, status
) VALUES ($1, $2, $3, $4, $5, 'received')
ON CONFLICT (provider_slug, idempotency_key, created_at) DO NOTHING
RETURNING id, created_at;
```

The `ON CONFLICT DO NOTHING` combined with `RETURNING` means: if the row
already exists, the INSERT silently returns no rows. The adapter detects the
missing row (pgx returns `ErrNoRows`) and treats it as a duplicate. This
prevents the same provider callback from being processed twice -- critical
when providers retry webhook deliveries on timeout.

```sql
-- name: UpdateWebhookLogProcessed :exec
UPDATE provider_webhook_logs
SET transfer_id = $2,
    tenant_id = $3,
    normalized = $4,
    status = $5,
    error_message = $6,
    processed_at = now()
WHERE id = $1 AND created_at = $7;
```

Note the `AND created_at = $7` in the WHERE clause. The `provider_webhook_logs`
table is partitioned by `created_at`, so including the partition key in
every UPDATE and SELECT avoids scanning all partitions.

**Position events** (`position_events.sql`) use `unnest` for high-throughput
batch inserts -- the same technique the ledger uses for entry lines:

```sql
-- name: BatchInsertPositionEvents :exec
INSERT INTO position_events (
    id, position_id, tenant_id, event_type, amount,
    balance_after, locked_after, reference_id, reference_type,
    idempotency_key, recorded_at
)
SELECT
    unnest(@ids::uuid[]),
    unnest(@position_ids::uuid[]),
    unnest(@tenant_ids::uuid[]),
    unnest(@event_types::text[]),
    unnest(@amounts::numeric[]),
    unnest(@balance_afters::numeric[]),
    unnest(@locked_afters::numeric[]),
    unnest(@reference_ids::uuid[]),
    unnest(@reference_types::text[]),
    unnest(@idempotency_keys::text[]),
    unnest(@recorded_ats::timestamptz[])
ON CONFLICT (idempotency_key, recorded_at) DO NOTHING;
```

- `unnest(@ids::uuid[])` -- SQLC's `@param` syntax names the parameter for
  clarity; the `::uuid[]` cast tells PostgreSQL these are arrays
- The `SELECT unnest(...), unnest(...)` pattern expands N arrays into N rows
  in a single INSERT, avoiding N round-trips
- `ON CONFLICT (idempotency_key, recorded_at) DO NOTHING` -- idempotent by
  design. The treasury flush goroutine may retry, but duplicate events are
  silently skipped

This batch pattern is why the treasury can flush position events every 100ms
without creating a write bottleneck. A single flush might insert 50-100 events
in one SQL statement.

---

## 3.5.3 What SQLC Generates

For each `.sql` file, SQLC generates three files:

### models.go (Generated Structs)

```go
// store/transferdb/models.go (generated)
type Transfer struct {
    ID                uuid.UUID
    TenantID          uuid.UUID
    ExternalRef       pgtype.Text
    IdempotencyKey    pgtype.Text
    Status            TransferStatusEnum
    Version           int64
    SourceCurrency    string
    SourceAmount      pgtype.Numeric
    DestCurrency      string
    DestAmount        pgtype.Numeric
    StableCoin        pgtype.Text
    StableAmount      pgtype.Numeric
    Chain             pgtype.Text
    FxRate            pgtype.Numeric
    Fees              []byte
    Sender            []byte
    Recipient         []byte
    QuoteID           pgtype.UUID
    OnRampProviderID  pgtype.Text
    OffRampProviderID pgtype.Text
    CreatedAt         time.Time
    UpdatedAt         time.Time
    FundedAt          pgtype.Timestamptz
    CompletedAt       pgtype.Timestamptz
    FailedAt          pgtype.Timestamptz
    FailureReason     pgtype.Text
    FailureCode       pgtype.Text
}
```

Note the use of `pgtype.*` for nullable columns. `pgtype.Text` wraps
`string` + `Valid bool`; `pgtype.Numeric` wraps `*big.Int` + `Exp int32` +
`Valid bool`. These types map directly to PostgreSQL's wire protocol without
any conversion overhead.

### querier.go (Generated Interface)

```go
// store/transferdb/querier.go (generated)
type Querier interface {
    CreateTransfer(ctx context.Context, arg CreateTransferParams) (Transfer, error)
    GetTransfer(ctx context.Context, arg GetTransferParams) (Transfer, error)
    GetTransferByIdempotencyKey(ctx context.Context, arg GetTransferByIdempotencyKeyParams) (Transfer, error)
    GetTransferByExternalRef(ctx context.Context, arg GetTransferByExternalRefParams) (Transfer, error)
    ListTransfersByTenant(ctx context.Context, arg ListTransfersByTenantParams) ([]Transfer, error)
    UpdateTransferStatus(ctx context.Context, arg UpdateTransferStatusParams) error
    UpdateTransferStatusWithVersion(ctx context.Context, arg UpdateTransferStatusWithVersionParams) error
    SumDailyVolumeByTenant(ctx context.Context, arg SumDailyVolumeByTenantParams) (pgtype.Numeric, error)
    CreateTransferEvent(ctx context.Context, arg CreateTransferEventParams) (TransferEvent, error)
    ListTransferEvents(ctx context.Context, arg ListTransferEventsParams) ([]TransferEvent, error)
    CreateQuote(ctx context.Context, arg CreateQuoteParams) (Quote, error)
    GetQuote(ctx context.Context, arg GetQuoteParams) (Quote, error)
    // ... more methods
}
```

### transfers.sql.go (Generated Implementation)

Each query becomes a method on `*Queries` that executes the SQL and scans
results into the generated struct:

```go
// store/transferdb/transfers.sql.go (generated, simplified)
const getTransfer = `SELECT * FROM transfers WHERE id = $1 AND tenant_id = $2`

func (q *Queries) GetTransfer(ctx context.Context, arg GetTransferParams) (Transfer, error) {
    row := q.db.QueryRow(ctx, getTransfer, arg.ID, arg.TenantID)
    var i Transfer
    err := row.Scan(
        &i.ID, &i.TenantID, &i.ExternalRef, &i.IdempotencyKey,
        &i.Status, &i.Version, &i.SourceCurrency, &i.SourceAmount,
        // ... all columns
    )
    return i, err
}
```

The SQL string is embedded as a Go constant. There is zero query generation
at runtime -- the database sees the exact SQL you wrote.

---

## 3.5.4 The Adapter Pattern

The SQLC-generated code uses `pgtype.*` types that are specific to the
PostgreSQL driver. The engine should not know about `pgtype.Numeric` or
`pgtype.Text` -- those are data layer concerns. The adapter bridges this gap.

```
Adapter Pattern
=================

Engine (core/engine.go)
   |
   | depends on interface
   v
TransferStore (core/store.go)
   |
   | uses domain types: decimal.Decimal, uuid.UUID, domain.Transfer
   |
   | implemented by
   v
TransferStoreAdapter (store/transferdb/adapter.go)
   |
   | converts domain types <-> pgtype types
   | delegates to SQLC-generated Queries
   v
Queries (store/transferdb/transfers.sql.go)
   |
   | executes raw SQL
   v
PostgreSQL (via pgx/v5)
```

### The Adapter Struct

```go
type TransferStoreAdapter struct {
    q         *Queries           // SQLC-generated query methods
    pool      TxBeginner         // for transactional operations
    appPool   *pgxpool.Pool      // optional: RLS-enforced pool
    piiCrypto *domain.PIIEncryptor // optional: PII encryption
}
```

### Compile-Time Interface Check

```go
var (
    _ core.TransferStore = (*TransferStoreAdapter)(nil)
    _ core.TenantStore   = (*TenantStoreAdapter)(nil)
)
```

These compile-time assertions ensure the adapter satisfies the engine's
interface. If someone adds a method to `core.TransferStore` without
implementing it in the adapter, the code will not compile.

> **Key Insight:** The compile-time interface check is free -- it generates
> no runtime code. The `_` assignment is a blank identifier that tells the
> compiler "verify this type assertion but discard the result."

### Conversion Helpers

The adapter uses helper functions to convert between domain types and pgtype:

```go
func textFromString(s string) pgtype.Text {
    if s == "" {
        return pgtype.Text{}
    }
    return pgtype.Text{String: s, Valid: true}
}

func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
    n := pgtype.Numeric{}
    _ = n.Scan(d.String())
    return n
}

func decimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
    if !n.Valid || n.Int == nil {
        return decimal.Zero
    }
    return decimal.NewFromBigInt(n.Int, n.Exp)
}

func uuidFromPtr(id *uuid.UUID) pgtype.UUID {
    if id == nil {
        return pgtype.UUID{}
    }
    return pgtype.UUID{Bytes: *id, Valid: true}
}
```

These helpers handle nullable values correctly. A `nil` UUID pointer becomes
an invalid `pgtype.UUID` (which PostgreSQL stores as NULL). A zero decimal
becomes a valid `pgtype.Numeric` with value 0.

### Row-to-Domain Conversion

```go
func transferFromRow(row Transfer) (*domain.Transfer, error) {
    t := &domain.Transfer{
        ID:             row.ID,
        TenantID:       row.TenantID,
        ExternalRef:    row.ExternalRef.String,
        IdempotencyKey: row.IdempotencyKey.String,
        Status:         domain.TransferStatus(row.Status),
        Version:        row.Version,
        SourceCurrency: domain.Currency(row.SourceCurrency),
        SourceAmount:   decimalFromNumeric(row.SourceAmount),
        DestCurrency:   domain.Currency(row.DestCurrency),
        DestAmount:     decimalFromNumeric(row.DestAmount),
        StableCoin:     domain.Currency(row.StableCoin.String),
        StableAmount:   decimalFromNumeric(row.StableAmount),
        Chain:          row.Chain.String,
        FXRate:         decimalFromNumeric(row.FxRate),
        // ... remaining fields ...
    }

    if row.QuoteID.Valid {
        id := uuid.UUID(row.QuoteID.Bytes)
        t.QuoteID = &id
    }
    if row.Fees != nil {
        if err := json.Unmarshal(row.Fees, &t.Fees); err != nil {
            slog.Warn("settla-store: failed to unmarshal fees",
                "transfer_id", row.ID, "error", err)
        }
    }
    if row.Sender != nil {
        if err := json.Unmarshal(row.Sender, &t.Sender); err != nil {
            slog.Warn("settla-store: failed to unmarshal sender",
                "transfer_id", row.ID, "error", err)
        }
    }
    // ...
    return t, nil
}
```

JSON-encoded columns (`fees`, `sender`, `recipient`) are stored as `[]byte`
in the database and unmarshalled into domain structs on read. This allows
complex nested structures without schema migrations when the structure
changes.

---

## 3.5.4a The Full Adapter Catalog

Section 3.5.4 showed one adapter (`TransferStoreAdapter`). In practice, the
Transfer DB alone has over fifteen adapters, each implementing a specific
domain interface. The Treasury DB and Ledger DB have their own. Here is the
complete catalog.

### Transfer DB Adapters (`store/transferdb/`)

| File | Adapter | Interface / Purpose |
|------|---------|---------------------|
| `adapter.go` | `TransferStoreAdapter` | Core transfer operations (`core.TransferStore`) -- CRUD, status transitions, idempotency lookups |
| `outbox_store.go` | (embedded in `TransferStoreAdapter`) | `TransitionWithOutbox`, `CreateTransferWithOutbox` -- atomic state + outbox writes |
| `analytics_adapter.go` | `AnalyticsStoreAdapter` | Daily volume, transfer count, and status breakdown aggregations |
| `analytics_ext_adapter.go` | `AnalyticsExtAdapter` | Extended analytics: data exports, snapshot persistence |
| `audit_adapter.go` | `AuditStoreAdapter` | Audit trail queries for compliance and debugging |
| `auth_adapter.go` | `AuthStoreAdapter` | API key hash lookups, tenant resolution, fee schedule retrieval |
| `bank_deposit_store_adapter.go` | `BankDepositStoreAdapter` | Bank deposit session lifecycle (`core/bankdeposit.Store`) |
| `chainmonitor_adapters.go` | `ChainMonitorAdapter` | Block checkpoint persistence, deposit address discovery |
| `compensation_adapter.go` | `CompensationAdapter` | Compensation flow queries for partial failure recovery |
| `deposit_store_adapter.go` | `DepositStoreAdapter` | Crypto deposit session lifecycle (`core/deposit.Store`) |
| `ops_adapter.go` | `OpsAdapter` | Operational dashboard queries (stuck transfers, queue depths) |
| `outbox_relay_adapter.go` | `OutboxRelayAdapter` | Outbox polling and batch mark-as-published for the relay |
| `payment_link_store_adapter.go` | `PaymentLinkStoreAdapter` | Payment link CRUD and redemption (`core/paymentlink.Store`) |
| `portal_adapter.go` | `PortalAdapter` | Tenant portal self-service (KYB status, settings updates) |
| `position_transaction_adapter.go` | `PositionTransactionStoreAdapter` | Position top-up/withdrawal lifecycle *(new)* |
| `provider_tx_adapter.go` | `ProviderTxAdapter` | Provider transaction claim and status tracking |
| `provider_webhook_log_adapter.go` | `ProviderWebhookLogAdapter` | Inbound webhook deduplication and audit trail *(new)* |
| `reconciliation_adapter.go` | `ReconciliationAdapter` | Consistency check queries (balance drift, orphan detection) |
| `recovery_query_adapter.go` | `RecoveryQueryAdapter` | Stuck transfer detection queries for the recovery detector |
| `review_adapter.go` | `ReviewAdapter` | Manual review queue for escalated transfers |
| `settlement_adapter.go` | `SettlementAdapter` | Net settlement cycle management and position netting |
| `webhook_adapter.go` | `WebhookAdapter` | Outbound webhook endpoint configuration lookups |

### Treasury DB Adapters (`store/treasurydb/`)

The Treasury DB is simpler -- it primarily stores position snapshots that the
in-memory treasury flushes every 100ms:

- `treasury.sql.go` -- SQLC-generated position upsert, history recording
- `position_events.sql.go` *(new)* -- Batch insert for the event-sourced
  position audit trail

### Ledger DB Adapters (`store/ledgerdb/`)

The Ledger DB serves the CQRS read model. TigerBeetle is the write authority;
Postgres stores the same data for query flexibility:

- `ledger.sql.go` -- Accounts, journal entries, entry lines, balance snapshots

### Example: Position Transaction Adapter

The `PositionTransactionStoreAdapter` demonstrates how a new adapter follows
the established patterns. It uses `CreateWithOutbox` for atomic creation, just
like transfers:

```go
// PositionTransactionStoreAdapter implements the position transaction store
// interface using raw SQL queries against the Transfer DB.
type PositionTransactionStoreAdapter struct {
    pool *pgxpool.Pool
}

// CreateWithOutbox atomically creates a position transaction and inserts
// outbox entries.
func (s *PositionTransactionStoreAdapter) CreateWithOutbox(
    ctx context.Context,
    tx *domain.PositionTransaction,
    entries []domain.OutboxEntry,
) error {
    dbTx, err := s.pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("settla-position-tx-store: begin tx: %w", err)
    }
    defer dbTx.Rollback(ctx)

    _, err = dbTx.Exec(ctx, `
        INSERT INTO position_transactions (
            id, tenant_id, type, currency, location, amount, status,
            method, destination, reference, version, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
        tx.ID, tx.TenantID, string(tx.Type), string(tx.Currency), tx.Location,
        tx.Amount.String(), string(tx.Status), tx.Method, tx.Destination,
        tx.Reference, tx.Version, tx.CreatedAt, tx.UpdatedAt,
    )
    if err != nil {
        return fmt.Errorf("settla-position-tx-store: insert transaction: %w", err)
    }

    for _, entry := range entries {
        _, err = dbTx.Exec(ctx, `
            INSERT INTO outbox (
                id, aggregate_type, aggregate_id, tenant_id, correlation_id,
                event_type, payload, is_intent, created_at
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
            entry.ID, entry.AggregateType, entry.AggregateID, entry.TenantID,
            entry.CorrelationID, entry.EventType, entry.Payload, entry.IsIntent,
            entry.CreatedAt,
        )
        if err != nil {
            return fmt.Errorf("settla-position-tx-store: insert outbox entry: %w", err)
        }
    }

    return dbTx.Commit(ctx)
}
```

Key observations:

1. **Same atomic guarantee.** The position transaction INSERT and all outbox
   entries are committed in a single database transaction. If any step fails,
   everything rolls back.

2. **Raw SQL instead of SQLC.** Unlike the `TransferStoreAdapter` which
   delegates to SQLC-generated `*Queries`, this adapter uses inline SQL via
   `dbTx.Exec`. Both approaches are valid -- SQLC provides type safety at
   generation time, while raw SQL is simpler for new adapters that may still
   be evolving.

3. **Amount as string.** `tx.Amount.String()` converts the `decimal.Decimal`
   to a string for PostgreSQL's `NUMERIC` column. This avoids float64
   precision loss. PostgreSQL parses the string representation into its
   internal numeric format.

4. **Familiar scan pattern.** The `Get` and `ListByTenant` methods use a
   shared `scanPositionTransaction` helper that converts database strings
   back to domain types (`domain.Currency`, `domain.PositionTxStatus`):

```go
func scanPositionTransaction(row scanRow) (*domain.PositionTransaction, error) {
    var tx domain.PositionTransaction
    var txType, currency, status, amount string

    err := row.Scan(
        &tx.ID, &tx.TenantID, &txType, &currency, &tx.Location,
        &amount, &status, &tx.Method, &tx.Destination, &tx.Reference,
        &tx.FailureReason, &tx.Version, &tx.CreatedAt, &tx.UpdatedAt,
    )
    if err != nil {
        return nil, err
    }

    tx.Type = domain.PositionTxType(txType)
    tx.Currency = domain.Currency(currency)
    tx.Status = domain.PositionTxStatus(status)
    tx.Amount, _ = decimal.NewFromString(amount)
    return &tx, nil
}
```

The `scanRow` interface (`Scan(dest ...any) error`) accepts both
`pgx.Row` (from `QueryRow`) and `pgx.Rows` (from `Query`), so the same
scan function works for both single-row and multi-row queries.

### Example: Provider Webhook Log Adapter

The `ProviderWebhookLogAdapter` shows a different pattern -- it wraps
SQLC-generated queries and adds domain-level semantics:

```go
type ProviderWebhookLogAdapter struct {
    q    *Queries
    pool *pgxpool.Pool
}

func (a *ProviderWebhookLogAdapter) InsertRaw(
    ctx context.Context,
    slug, idempotencyKey string,
    rawBody []byte,
    headers map[string]string,
    sourceIP string,
) (uuid.UUID, time.Time, bool, error) {
    headersJSON, err := json.Marshal(headers)
    if err != nil {
        headersJSON = []byte("{}")
    }

    row, err := a.q.InsertProviderWebhookLog(ctx, InsertProviderWebhookLogParams{
        ProviderSlug:   slug,
        IdempotencyKey: idempotencyKey,
        RawBody:        rawBody,
        HttpHeaders:    headersJSON,
        SourceIp:       pgtype.Text{String: sourceIP, Valid: sourceIP != ""},
    })
    if err != nil {
        // ON CONFLICT DO NOTHING returns no rows -- pgx returns ErrNoRows
        if err.Error() == "no rows in result set" {
            return uuid.UUID{}, time.Time{}, true, nil
        }
        return uuid.UUID{}, time.Time{}, false,
            fmt.Errorf("inserting webhook log: %w", err)
    }

    return row.ID, row.CreatedAt, false, nil
}
```

The third return value (`isDuplicate bool`) is a domain concept that does not
exist in the SQLC layer. The adapter translates the database-level signal
(no rows returned from `ON CONFLICT DO NOTHING`) into a domain-level boolean.
This is exactly the kind of translation that adapters are for -- keeping
database semantics out of the domain layer.

---

## 3.5.5 TransitionWithOutbox: The Atomic Transaction

This is the most critical method in the entire data layer. Every state
transition in the engine depends on it. Here is the full implementation from
`store/transferdb/outbox_store.go`:

```go
func (s *TransferStoreAdapter) TransitionWithOutbox(
    ctx context.Context,
    transferID uuid.UUID,
    newStatus domain.TransferStatus,
    expectedVersion int64,
    entries []domain.OutboxEntry,
) error {

    if s.pool == nil {
        return fmt.Errorf("settla-store: TransitionWithOutbox requires a " +
            "TxBeginner (pool); adapter was created without one")
    }

    tx, err := beginRepeatableRead(ctx, s.pool)
    if err != nil {
        return fmt.Errorf("settla-store: begin tx: %w", err)
    }
    defer tx.Rollback(ctx)
```

**Line 1: Begin with REPEATABLE READ.** As discussed in Chapter 3.1, this
isolation level prevents phantom reads during the concurrent UPDATE + INSERT
pattern.

```go
    // Set RLS tenant context when appPool is configured
    if s.appPool != nil && len(entries) > 0 && entries[0].TenantID != uuid.Nil {
        if err := rls.SetTenantLocal(ctx, tx, entries[0].TenantID); err != nil {
            return fmt.Errorf("settla-store: set tenant context for transfer %s: %w",
                transferID, err)
        }
    }
```

**RLS setup.** When Row-Level Security is enabled, the adapter sets
`SET LOCAL app.current_tenant_id = '{tenant_id}'` within the transaction.
PostgreSQL's RLS policies then automatically filter rows by tenant, providing
a second layer of tenant isolation beyond the `WHERE tenant_id = $1` in every
query.

```go
    // 1. UPDATE transfer with optimistic lock + status-specific timestamps.
    tag, err := tx.Exec(ctx,
        `UPDATE transfers
         SET status = $1::transfer_status_enum,
             version = version + 1,
             updated_at = now(),
             funded_at    = CASE WHEN $1::text = 'FUNDED'    THEN now() ELSE funded_at    END,
             completed_at = CASE WHEN $1::text = 'COMPLETED' THEN now() ELSE completed_at END,
             failed_at    = CASE WHEN $1::text = 'FAILED'    THEN now() ELSE failed_at    END
         WHERE id = $2 AND version = $3`,
        string(newStatus), transferID, expectedVersion)
    if err != nil {
        return fmt.Errorf("settla-store: update transfer %s: %w", transferID, err)
    }
    if tag.RowsAffected() == 0 {
        return fmt.Errorf("settla-store: transfer %s: %w",
            transferID, core.ErrOptimisticLock)
    }
```

**The optimistic lock.** `WHERE id = $2 AND version = $3` ensures this
UPDATE only succeeds if no other transaction has incremented the version
since the engine loaded the transfer. If `RowsAffected() == 0`, another
transaction got there first, and we return `ErrOptimisticLock`.

Note: the `$1::transfer_status_enum` cast tells PostgreSQL to treat the
string as an enum value. The `$1::text` in the CASE expressions compares
it as a string for the timestamp logic. Both reference the same parameter.

```go
    // 2. Batch INSERT outbox entries.
    if len(entries) > 0 {
        params := outboxEntriesToParams(entries)
        qtx := s.q.WithTx(tx)
        if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
            return fmt.Errorf("settla-store: insert outbox entries for transfer %s: %w",
                transferID, err)
        }
    }
```

**Batch insert.** All outbox entries are inserted in a single batch operation
within the same transaction. `s.q.WithTx(tx)` creates a new Queries instance
that executes against the transaction rather than the connection pool.

```go
    if err := tx.Commit(ctx); err != nil {
        return fmt.Errorf("settla-store: commit tx for transfer %s: %w",
            transferID, err)
    }
    return nil
}
```

**Commit.** Either everything succeeds (status update + all outbox entries)
or everything rolls back (deferred `tx.Rollback(ctx)` runs if Commit is
never reached).

### The outboxEntriesToParams Converter

```go
func outboxEntriesToParams(entries []domain.OutboxEntry) []InsertOutboxEntriesParams {
    params := make([]InsertOutboxEntriesParams, len(entries))
    for i, e := range entries {
        createdAt := e.CreatedAt
        if createdAt.IsZero() {
            createdAt = time.Now().UTC()
        }
        payload := e.Payload
        if payload == nil {
            payload = []byte("{}")
        }
        params[i] = InsertOutboxEntriesParams{
            ID:            e.ID,
            AggregateType: e.AggregateType,
            AggregateID:   e.AggregateID,
            TenantID:      e.TenantID,
            EventType:     e.EventType,
            Payload:       payload,
            IsIntent:      e.IsIntent,
            MaxRetries:    int32(e.MaxRetries),
            CreatedAt:     createdAt,
        }
    }
    return params
}
```

Defensive defaults: zero timestamps become `now()`, nil payloads become `{}`.
This prevents NULL-related issues in downstream consumers.

---

## 3.5.6 CreateTransferWithOutbox: Atomic Creation

The creation variant is similar but handles the ID assignment problem:

```go
func (s *TransferStoreAdapter) CreateTransferWithOutbox(
    ctx context.Context,
    transfer *domain.Transfer,
    entries []domain.OutboxEntry,
) error {
    // ... begin tx, marshal fees/sender/recipient ...

    // 1. INSERT transfer within transaction.
    qtx := s.q.WithTx(tx)
    row, err := qtx.CreateTransfer(ctx, CreateTransferParams{...})
    if err != nil {
        return fmt.Errorf("settla-store: creating transfer: %w", err)
    }

    transfer.ID = row.ID
    transfer.Version = row.Version
    transfer.CreatedAt = row.CreatedAt
    transfer.UpdatedAt = row.UpdatedAt
```

The database assigns the final ID (via `RETURNING *`). The adapter writes it
back to the transfer struct.

```go
    // 2. Batch INSERT outbox entries.
    if len(entries) > 0 {
        // Re-marshal the transfer payload now that the DB has assigned the real ID.
        updatedPayload, err := json.Marshal(transfer)
        if err != nil {
            return fmt.Errorf("settla-store: re-marshalling transfer payload " +
                "after ID assignment: %w", err)
        }

        for i := range entries {
            if entries[i].AggregateID == uuid.Nil {
                entries[i].AggregateID = transfer.ID
            }
            // Replace stale payload with one containing the real transfer ID.
            if entries[i].AggregateType == "transfer" && !entries[i].IsIntent {
                entries[i].Payload = updatedPayload
            }
        }
        params := outboxEntriesToParams(entries)
        if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
            return fmt.Errorf("settla-store: insert outbox entries for new transfer: %w",
                err)
        }
    }

    if err := tx.Commit(ctx); err != nil {
        return fmt.Errorf("settla-store: commit tx for new transfer: %w", err)
    }
    return nil
}
```

> **Key Insight:** The payload re-marshalling step is subtle but critical.
> The engine marshals the transfer payload *before* calling
> `CreateTransferWithOutbox` (in `CreateTransfer`, step h). At that point,
> the transfer has a client-generated UUID. After the INSERT, the database
> may have assigned a different ID (via the `DEFAULT gen_random_uuid()`
> column default). The adapter must re-marshal the payload with the real
> ID so that downstream consumers (the relay, workers) see the correct
> transfer ID.

---

## 3.5.7 PII Encryption in the Adapter

The adapter optionally encrypts Personally Identifiable Information before
storage:

```go
type TransferStoreAdapter struct {
    // ...
    piiCrypto *domain.PIIEncryptor // optional: encrypts PII before INSERT
}

func (s *TransferStoreAdapter) CreateTransfer(ctx context.Context,
    transfer *domain.Transfer) error {

    // ...
    if s.piiCrypto != nil {
        // Encrypt PII fields before storage.
        encSender, err := s.piiCrypto.EncryptSender(transfer.TenantID, transfer.Sender)
        // ...
        encRecipient, err := s.piiCrypto.EncryptRecipient(transfer.TenantID, transfer.Recipient)
        // ...
    } else {
        // No encryption configured -- store plaintext (development/test only).
        senderJSON, err = json.Marshal(transfer.Sender)
        // ...
    }
}
```

On read, the adapter detects encrypted data and decrypts:

```go
func (s *TransferStoreAdapter) transferFromRowWithDecrypt(row Transfer) (*domain.Transfer, error) {
    t, err := transferFromRow(row)
    if err != nil {
        return nil, err
    }

    if s.piiCrypto == nil {
        return t, nil
    }

    // Try to decrypt sender PII from the raw JSON.
    if len(row.Sender) > 0 {
        var encSender domain.EncryptedSender
        if err := json.Unmarshal(row.Sender, &encSender); err == nil &&
            len(encSender.EncryptedName) > 0 {
            sender, err := s.piiCrypto.DecryptSender(t.TenantID, &encSender)
            if err != nil {
                return nil, fmt.Errorf("settla-core: decrypting sender PII: %w", err)
            }
            t.Sender = sender
        }
        // If EncryptedName is empty, the data is plaintext (pre-encryption migration).
    }
    // ... same for recipient ...
}
```

The adapter tries to unmarshal as `EncryptedSender`. If the `EncryptedName`
field is empty, the data is plaintext (from before encryption was enabled),
and the normal unmarshal from `transferFromRow` already handled it.

This backward-compatible approach means you can enable encryption without
migrating existing data. New writes are encrypted; old reads gracefully
fall back to plaintext.

---

## 3.5.8 RLS Tenant Isolation

When an `appPool` is configured (a connection pool using the `settla_app`
PostgreSQL role), the adapter enforces Row-Level Security:

```go
func (s *TransferStoreAdapter) GetTransfer(ctx context.Context,
    tenantID, transferID uuid.UUID) (*domain.Transfer, error) {

    // Use RLS-enforced pool when available.
    if s.appPool != nil {
        var result *domain.Transfer
        err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
            row, err := s.q.WithTx(tx).GetTransfer(ctx, GetTransferParams{
                ID:       transferID,
                TenantID: tenantID,
            })
            // ...
        })
        // ...
    }

    // Fallback: no RLS, rely on WHERE tenant_id = $2 clause only
    row, err := s.q.GetTransfer(ctx, GetTransferParams{
        ID:       transferID,
        TenantID: tenantID,
    })
    // ...
}
```

`rls.WithTenantReadTx` does:
1. Begin a transaction on the `settla_app` pool
2. Execute `SET LOCAL app.current_tenant_id = '{tenantID}'`
3. Run the query (PostgreSQL RLS policies filter by the set tenant ID)
4. Commit the read-only transaction

This provides defense-in-depth: even if a developer forgets the
`WHERE tenant_id = $2` clause in a new query, the RLS policy at the
database level prevents cross-tenant data access.

---

## 3.5.9 The TxBeginner Interface

```go
type TxBeginner interface {
    Begin(ctx context.Context) (pgx.Tx, error)
}

type TxBeginnerWithOptions interface {
    TxBeginner
    BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}
```

This abstraction allows the adapter to work with both `*pgxpool.Pool`
(production) and mock transaction starters (testing). The
`TxBeginnerWithOptions` extension enables REPEATABLE READ isolation when the
pool supports it, falling back gracefully otherwise.

---

## Common Mistakes

1. **Editing SQLC-generated files.** Files in `store/transferdb/` that are
   generated by SQLC will be overwritten on the next `make sqlc-generate`.
   Always edit the SQL source in `db/queries/transfer/` instead.

2. **Using `float64` in conversion helpers.** `numericFromDecimal` converts
   via `d.String()` + `n.Scan()`, never through float. Any conversion that
   touches `float64` risks precision loss on monetary amounts.

3. **Forgetting to pass the pool.** `TransitionWithOutbox` returns an error
   if `s.pool == nil`. When creating the adapter, always pass a pool if you
   need transactional outbox operations:
   `NewTransferStoreAdapterWithOptions(q, WithTxPool(pool))`

4. **Ignoring RowsAffected.** The optimistic lock in `TransitionWithOutbox`
   depends on checking `tag.RowsAffected() == 0`. If you write similar
   patterns, always check the affected row count -- a silent zero-row update
   is worse than an explicit error.

5. **Not re-marshalling after ID assignment.** In `CreateTransferWithOutbox`,
   the outbox entry payload must be updated after the INSERT assigns the real
   transfer ID. Forgetting this means workers process entries with a stale
   UUID that does not match any database row.

---

## Exercises

1. **Write a new SQLC query.** Add a query to `transfers.sql` that counts
   transfers by status for a given tenant within a date range. What annotation
   would you use (`:one`, `:many`, `:exec`)? What index would optimize this
   query?

2. **Trace the transaction.** Draw a timeline showing the exact SQL statements
   executed during a `TransitionWithOutbox` call for a FUNDED -> ON_RAMPING
   transition with one intent outbox entry. Include the BEGIN, SET LOCAL
   (if RLS), UPDATE, INSERT, and COMMIT.

3. **Design a batch transition.** The current `TransitionWithOutbox` handles
   one transfer at a time. Design a `BatchTransitionWithOutbox` that
   transitions multiple transfers atomically. What are the tradeoffs? When
   would this be useful (hint: settlement netting)?

4. **Add a new column.** You need to add a `compliance_status` column to the
   transfers table. List every file you would need to modify: the migration
   SQL, the SQLC query file, the domain struct, the adapter conversion, and
   the engine. Which of these are generated and which are hand-written?

5. **PII encryption key rotation.** The current `PIIEncryptor` uses per-tenant
   Data Encryption Keys (DEKs). Design a key rotation strategy that allows
   re-encrypting existing rows without downtime. What adapter changes would
   be needed?

---

## What's Next

You have now completed Module 3: The Settlement Engine. You understand the
transactional outbox pattern, the zero-side-effect engine design, the full
transfer lifecycle, failure compensation, and the data layer that makes it
all work.

In Module 4, we will examine Treasury Management and Smart Routing -- how
Settla maintains per-tenant liquidity positions with in-memory atomic
reservations, flushes position state to the database, manages the
event-sourced audit trail, and routes transfers through the optimal provider
based on cost, speed, liquidity, and reliability scoring.
