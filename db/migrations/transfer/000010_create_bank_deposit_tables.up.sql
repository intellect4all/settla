-- ============================================================================
-- Bank deposit tables for the Virtual IBANs / Bank Deposits feature.
-- Deposit sessions are partitioned by created_at (monthly) — same pattern as transfers.
-- ============================================================================

-- ── Bank deposit session status enum ───────────────────────────────────────
CREATE TYPE bank_deposit_session_status_enum AS ENUM (
    'PENDING_PAYMENT', 'PAYMENT_RECEIVED', 'CREDITING', 'CREDITED',
    'SETTLING', 'SETTLED', 'HELD', 'EXPIRED', 'FAILED', 'CANCELLED',
    'UNDERPAID', 'OVERPAID'
);

-- ── Virtual account type enum ──────────────────────────────────────────────
CREATE TYPE virtual_account_type_enum AS ENUM ('PERMANENT', 'TEMPORARY');

-- ── Payment mismatch policy enum ───────────────────────────────────────────
CREATE TYPE payment_mismatch_policy_enum AS ENUM ('ACCEPT', 'REJECT');

-- ── Banking partners: upstream bank integrations ───────────────────────────
CREATE TABLE banking_partners (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                  TEXT NOT NULL,
    webhook_secret        TEXT,
    supported_currencies  TEXT[] NOT NULL DEFAULT '{}',
    is_active             BOOLEAN NOT NULL DEFAULT true,
    metadata              JSONB DEFAULT '{}',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Virtual account pool: pre-provisioned bank accounts for dispense ──────
CREATE TABLE virtual_account_pool (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    banking_partner_id  UUID NOT NULL REFERENCES banking_partners(id),
    account_number      TEXT NOT NULL,
    account_name        TEXT NOT NULL DEFAULT '',
    sort_code           TEXT NOT NULL DEFAULT '',
    iban                TEXT NOT NULL DEFAULT '',
    currency            TEXT NOT NULL,
    account_type        virtual_account_type_enum NOT NULL DEFAULT 'TEMPORARY',
    available           BOOLEAN NOT NULL DEFAULT true,
    session_id          UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_number)
);

-- Partial index for SKIP LOCKED dispense pattern
CREATE INDEX idx_virtual_account_pool_available
    ON virtual_account_pool(tenant_id, currency, available)
    WHERE available = true;

-- ── Virtual account index: maps account numbers to sessions ────────────────
CREATE TABLE virtual_account_index (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_number  TEXT NOT NULL,
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    session_id      UUID,
    account_type    virtual_account_type_enum NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_virtual_account_index_number
    ON virtual_account_index(account_number);

-- ── Bank deposit sessions: partitioned by created_at (monthly) ─────────────
-- At scale, deposit sessions could reach millions/day.
CREATE TABLE bank_deposit_sessions (
    id                      UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id               UUID NOT NULL,
    idempotency_key         TEXT,
    status                  bank_deposit_session_status_enum NOT NULL DEFAULT 'PENDING_PAYMENT',
    version                 BIGINT NOT NULL DEFAULT 1,

    banking_partner_id      TEXT NOT NULL,
    account_number          TEXT NOT NULL,
    account_name            TEXT NOT NULL DEFAULT '',
    sort_code               TEXT NOT NULL DEFAULT '',
    iban                    TEXT NOT NULL DEFAULT '',
    account_type            virtual_account_type_enum NOT NULL,
    currency                TEXT NOT NULL,

    expected_amount         NUMERIC NOT NULL DEFAULT 0,
    min_amount              NUMERIC NOT NULL DEFAULT 0,
    max_amount              NUMERIC NOT NULL DEFAULT 0,
    received_amount         NUMERIC NOT NULL DEFAULT 0,
    fee_amount              NUMERIC NOT NULL DEFAULT 0,
    net_amount              NUMERIC NOT NULL DEFAULT 0,

    mismatch_policy         payment_mismatch_policy_enum NOT NULL DEFAULT 'REJECT',
    collection_fee_bps      INT NOT NULL DEFAULT 0,
    settlement_pref         settlement_preference_enum NOT NULL DEFAULT 'HOLD',
    settlement_transfer_id  UUID,

    payer_name              TEXT NOT NULL DEFAULT '',
    payer_reference         TEXT NOT NULL DEFAULT '',
    bank_reference          TEXT NOT NULL DEFAULT '',

    expires_at              TIMESTAMPTZ NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    payment_received_at     TIMESTAMPTZ,
    credited_at             TIMESTAMPTZ,
    settled_at              TIMESTAMPTZ,
    expired_at              TIMESTAMPTZ,
    failed_at               TIMESTAMPTZ,

    failure_reason          TEXT,
    failure_code            TEXT,

    metadata                JSONB DEFAULT '{}',

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Idempotency scoped per tenant. Must include partition key for partitioned tables.
CREATE UNIQUE INDEX idx_bank_deposit_sessions_tenant_idempotency
    ON bank_deposit_sessions(tenant_id, idempotency_key, created_at)
    WHERE idempotency_key IS NOT NULL;

-- Query indexes
CREATE INDEX idx_bank_deposit_sessions_tenant_created
    ON bank_deposit_sessions(tenant_id, created_at DESC);
CREATE INDEX idx_bank_deposit_sessions_tenant_status
    ON bank_deposit_sessions(tenant_id, status);
CREATE INDEX idx_bank_deposit_sessions_account
    ON bank_deposit_sessions(account_number);
CREATE INDEX idx_bank_deposit_sessions_expiry
    ON bank_deposit_sessions(expires_at)
    WHERE status = 'PENDING_PAYMENT';

-- Monthly partitions: 6 months ahead from 2026-03 + default
CREATE TABLE bank_deposit_sessions_y2026m03 PARTITION OF bank_deposit_sessions
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE bank_deposit_sessions_y2026m04 PARTITION OF bank_deposit_sessions
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE bank_deposit_sessions_y2026m05 PARTITION OF bank_deposit_sessions
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE bank_deposit_sessions_y2026m06 PARTITION OF bank_deposit_sessions
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE bank_deposit_sessions_y2026m07 PARTITION OF bank_deposit_sessions
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE bank_deposit_sessions_y2026m08 PARTITION OF bank_deposit_sessions
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE bank_deposit_sessions_default PARTITION OF bank_deposit_sessions DEFAULT;


-- ── Bank deposit transactions: partitioned by created_at (monthly) ─────────
-- Tracks individual bank payments linked to deposit sessions.
CREATE TABLE bank_deposit_transactions (
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    session_id            UUID NOT NULL,
    tenant_id             UUID NOT NULL,
    bank_reference        TEXT NOT NULL,
    payer_name            TEXT NOT NULL DEFAULT '',
    payer_account_number  TEXT NOT NULL DEFAULT '',
    amount                NUMERIC NOT NULL,
    currency              TEXT NOT NULL,
    received_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Unique bank reference per partition window
CREATE UNIQUE INDEX idx_bank_deposit_txns_reference
    ON bank_deposit_transactions(bank_reference, created_at);
CREATE INDEX idx_bank_deposit_txns_session
    ON bank_deposit_transactions(session_id, created_at DESC);

-- Monthly partitions
CREATE TABLE bank_deposit_transactions_y2026m03 PARTITION OF bank_deposit_transactions
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE bank_deposit_transactions_y2026m04 PARTITION OF bank_deposit_transactions
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE bank_deposit_transactions_y2026m05 PARTITION OF bank_deposit_transactions
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE bank_deposit_transactions_y2026m06 PARTITION OF bank_deposit_transactions
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE bank_deposit_transactions_y2026m07 PARTITION OF bank_deposit_transactions
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE bank_deposit_transactions_y2026m08 PARTITION OF bank_deposit_transactions
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE bank_deposit_transactions_default PARTITION OF bank_deposit_transactions DEFAULT;


-- ── Tenant bank deposit configuration columns ─────────────────────────────
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS bank_deposits_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS default_banking_partner TEXT;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS bank_supported_currencies TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS default_mismatch_policy payment_mismatch_policy_enum NOT NULL DEFAULT 'REJECT';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS bank_default_session_ttl_secs INT NOT NULL DEFAULT 3600;

-- Add bank collection fee fields to fee_schedule JSONB (handled at app layer)
-- fee_schedule already stores: {"onramp_bps", "offramp_bps", "min_fee_usd", "max_fee_usd",
--   "crypto_collection_bps", "crypto_collection_max_fee_usd"}
-- We add: "bank_collection_bps", "bank_collection_min_fee_usd", "bank_collection_max_fee_usd"

-- ── RLS policies for new tables ────────────────────────────────────────────
DO $$ BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
            bank_deposit_sessions, bank_deposit_transactions,
            virtual_account_pool, virtual_account_index,
            banking_partners
        TO settla_app;

        ALTER TABLE bank_deposit_sessions ENABLE ROW LEVEL SECURITY;
        ALTER TABLE bank_deposit_transactions ENABLE ROW LEVEL SECURITY;
        ALTER TABLE virtual_account_pool ENABLE ROW LEVEL SECURITY;
        ALTER TABLE virtual_account_index ENABLE ROW LEVEL SECURITY;

        CREATE POLICY tenant_isolation ON bank_deposit_sessions
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
        CREATE POLICY tenant_isolation ON bank_deposit_transactions
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
        CREATE POLICY tenant_isolation ON virtual_account_pool
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
        CREATE POLICY tenant_isolation ON virtual_account_index
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
    END IF;
END $$;

-- ── Autovacuum tuning for high-volume partitions ───────────────────────────
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT c.relname AS child_name
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname IN ('bank_deposit_sessions', 'bank_deposit_transactions')
    LOOP
        EXECUTE format(
            'ALTER TABLE %I SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_analyze_scale_factor = 0.005)',
            r.child_name
        );
    END LOOP;
END;
$$;
