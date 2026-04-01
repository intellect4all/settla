-- +goose Up

CREATE TABLE accounts (
    id              UUID PRIMARY KEY,
    tenant_id       UUID,  -- NULL for system accounts
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('ASSET','LIABILITY','REVENUE','EXPENSE')),
    currency        TEXT NOT NULL,
    normal_balance  TEXT NOT NULL CHECK (normal_balance IN ('DEBIT','CREDIT')),
    parent_id       UUID REFERENCES accounts(id),
    is_active       BOOLEAN NOT NULL DEFAULT true,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_accounts_tenant_type_currency ON accounts(tenant_id, type, currency);
CREATE INDEX idx_accounts_code ON accounts(code);

CREATE TABLE journal_entries (
    id              UUID NOT NULL,
    tenant_id       UUID,  -- NULL for system entries
    idempotency_key TEXT,
    posted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    effective_date  DATE NOT NULL DEFAULT CURRENT_DATE,
    description     TEXT NOT NULL,
    reference_type  TEXT,
    reference_id    UUID,
    reversed_by     UUID,
    reversal_of     UUID,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, posted_at)
) PARTITION BY RANGE (posted_at);

-- Fixed idempotency indexes: separate tenant-scoped and system-scoped
CREATE UNIQUE INDEX idx_journal_entries_idempotency_tenant
    ON journal_entries(tenant_id, idempotency_key, posted_at)
    WHERE tenant_id IS NOT NULL AND idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX idx_journal_entries_idempotency_system
    ON journal_entries(idempotency_key, posted_at)
    WHERE tenant_id IS NULL AND idempotency_key IS NOT NULL;

CREATE INDEX idx_journal_entries_ref ON journal_entries(reference_type, reference_id);
CREATE INDEX idx_journal_entries_tenant ON journal_entries(tenant_id, posted_at DESC);

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

CREATE TABLE entry_lines (
    id                UUID NOT NULL,
    journal_entry_id  UUID NOT NULL,
    account_id        UUID NOT NULL,
    entry_type        TEXT NOT NULL CHECK (entry_type IN ('DEBIT','CREDIT')),
    amount            NUMERIC(28,8) NOT NULL CHECK (amount > 0),
    currency          TEXT NOT NULL,
    description       TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_entry_lines_journal ON entry_lines(journal_entry_id);
CREATE INDEX idx_entry_lines_account ON entry_lines(account_id, created_at DESC);
CREATE INDEX idx_entry_lines_account_currency ON entry_lines(account_id, currency, created_at DESC);

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

CREATE TABLE balance_snapshots (
    account_id    UUID NOT NULL PRIMARY KEY REFERENCES accounts(id),
    balance       NUMERIC(28,8) NOT NULL DEFAULT 0,
    last_entry_id UUID,
    version       BIGINT NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS balance_snapshots;
DROP TABLE IF EXISTS entry_lines;
DROP TABLE IF EXISTS journal_entries;
DROP TABLE IF EXISTS accounts;
