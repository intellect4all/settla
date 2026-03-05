-- Settla Ledger — CQRS Read Model
-- These tables are populated by the TigerBeetle → Postgres sync consumer.
-- TigerBeetle is the source of truth for balances.
-- Postgres provides rich query capability for dashboards, audit, and reporting.
--
-- Capacity planning (50M txn/day target):
--   accounts:          ~100K rows, non-partitioned
--   journal_entries:   ~50M inserts/day (~1.5B rows/month per partition)
--   entry_lines:       ~200-250M inserts/day (~7.5B rows/month per partition)
--   balance_snapshots: ~100K rows, non-partitioned (one row per account)
--
-- Partition strategy: MONTHLY partitions for journal_entries and entry_lines.
-- Rationale: append-only tables (no UPDATEs) so no vacuum bloat; queries always
-- filter by tenant_id + time range giving excellent partition pruning; can
-- sub-partition by tenant_id hash later if monthly partitions prove too large.

-- ============================================================================
-- accounts — ledger chart of accounts
-- ============================================================================
CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,                       -- NULL for system accounts (e.g. assets:crypto:usdt:tron)
    code            TEXT NOT NULL UNIQUE,        -- e.g. "tenant:lemfi:assets:bank:gbp:clearing"
    name            TEXT NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('ASSET', 'LIABILITY', 'REVENUE', 'EXPENSE')),
    currency        TEXT NOT NULL,
    normal_balance  TEXT NOT NULL CHECK (normal_balance IN ('DEBIT', 'CREDIT')),
    parent_id       UUID REFERENCES accounts(id),
    is_active       BOOLEAN NOT NULL DEFAULT true,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Account code naming convention:
-- System accounts:   assets:crypto:usdt:tron, expenses:network:tron:gas
-- Tenant accounts:   tenant:{slug}:assets:bank:gbp:clearing
--                    tenant:{slug}:liabilities:customer:pending
--                    tenant:{slug}:revenue:fees:settlement

CREATE INDEX idx_accounts_tenant_type_currency ON accounts(tenant_id, type, currency);
CREATE INDEX idx_accounts_code ON accounts(code);


-- ============================================================================
-- journal_entries — double-entry journal headers, partitioned by posted_at
-- At 50M txn/day, each monthly partition holds ~1.5B rows.
-- Populated by TB→PG sync consumer, NOT by the hot write path.
-- ============================================================================
CREATE TABLE journal_entries (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID,                       -- NULL for system-level entries
    idempotency_key TEXT,
    posted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    effective_date  DATE NOT NULL DEFAULT CURRENT_DATE,
    description     TEXT NOT NULL,
    reference_type  TEXT,
    reference_id    UUID,
    reversed_by     UUID,
    reversal_of     UUID,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, posted_at)
) PARTITION BY RANGE (posted_at);

-- Unique index must include partition key (posted_at) on partitioned tables.
-- Application layer ensures global idempotency via TigerBeetle's dedup.
CREATE UNIQUE INDEX idx_journal_entries_idempotency
    ON journal_entries(idempotency_key, posted_at) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_journal_entries_ref ON journal_entries(reference_type, reference_id);
CREATE INDEX idx_journal_entries_tenant ON journal_entries(tenant_id, posted_at DESC);

-- Monthly partitions: 6 months (2026-03 through 2026-08) + default
CREATE TABLE journal_entries_y2026m03 PARTITION OF journal_entries
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE journal_entries_y2026m04 PARTITION OF journal_entries
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE journal_entries_y2026m05 PARTITION OF journal_entries
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE journal_entries_y2026m06 PARTITION OF journal_entries
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE journal_entries_y2026m07 PARTITION OF journal_entries
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE journal_entries_y2026m08 PARTITION OF journal_entries
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE journal_entries_default PARTITION OF journal_entries DEFAULT;


-- ============================================================================
-- entry_lines — individual debit/credit lines, partitioned by created_at
-- At 50M txn/day × 4-5 entries each = 200-250M entry_lines per day.
-- Monthly partitions hold ~7.5B rows. Consider weekly if query perf degrades.
-- amount is always positive; entry_type indicates debit vs credit.
-- NOTE: No FK to journal_entries because both tables are partitioned on
-- different columns (posted_at vs created_at). Referential integrity is
-- enforced by the sync consumer application layer.
-- ============================================================================
CREATE TABLE entry_lines (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    journal_entry_id UUID NOT NULL,
    account_id      UUID NOT NULL,
    entry_type      TEXT NOT NULL CHECK (entry_type IN ('DEBIT', 'CREDIT')),
    amount          NUMERIC(28, 8) NOT NULL CHECK (amount > 0),
    currency        TEXT NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_entry_lines_journal ON entry_lines(journal_entry_id);
CREATE INDEX idx_entry_lines_account ON entry_lines(account_id, created_at DESC);

-- Monthly partitions: 6 months (2026-03 through 2026-08) + default
CREATE TABLE entry_lines_y2026m03 PARTITION OF entry_lines
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE entry_lines_y2026m04 PARTITION OF entry_lines
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE entry_lines_y2026m05 PARTITION OF entry_lines
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE entry_lines_y2026m06 PARTITION OF entry_lines
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE entry_lines_y2026m07 PARTITION OF entry_lines
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE entry_lines_y2026m08 PARTITION OF entry_lines
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE entry_lines_default PARTITION OF entry_lines DEFAULT;


-- ============================================================================
-- balance_snapshots — cached read model of TigerBeetle account balances
-- Updated periodically by the TB→PG sync consumer. NOT the source of truth.
-- Non-partitioned: one row per account (~100K rows).
-- ============================================================================
CREATE TABLE balance_snapshots (
    account_id      UUID NOT NULL REFERENCES accounts(id),
    balance         NUMERIC(28, 8) NOT NULL DEFAULT 0,
    last_entry_id   UUID,
    version         BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id)
);
