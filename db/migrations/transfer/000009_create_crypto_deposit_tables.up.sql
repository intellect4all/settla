-- ============================================================================
-- Crypto deposit tables for the payment gateway feature.
-- Deposit sessions are partitioned by created_at (monthly) — same pattern as transfers.
-- ============================================================================

-- ── Deposit session status enum ──────────────────────────────────────────────
CREATE TYPE deposit_session_status_enum AS ENUM (
    'PENDING_PAYMENT','DETECTED','CONFIRMED','CREDITING','CREDITED',
    'SETTLING','SETTLED','HELD','EXPIRED','FAILED','CANCELLED'
);

-- ── Settlement preference enum ───────────────────────────────────────────────
CREATE TYPE settlement_preference_enum AS ENUM (
    'AUTO_CONVERT','HOLD','THRESHOLD'
);

-- ── Tokens: supported tokens per chain ───────────────────────────────────────
CREATE TABLE tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain           TEXT NOT NULL,
    symbol          TEXT NOT NULL,
    contract_address TEXT NOT NULL,
    decimals        INTEGER NOT NULL DEFAULT 6,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain, symbol),
    UNIQUE (chain, contract_address)
);

CREATE INDEX idx_tokens_chain_active ON tokens(chain) WHERE is_active = true;

-- ── Block checkpoints: last scanned block per chain ──────────────────────────
CREATE TABLE block_checkpoints (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain           TEXT NOT NULL UNIQUE,
    block_number    BIGINT NOT NULL DEFAULT 0,
    block_hash      TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Crypto address pool: pre-generated deposit addresses ─────────────────────
CREATE TABLE crypto_address_pool (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenants(id),
    chain            TEXT NOT NULL,
    address          TEXT NOT NULL,
    derivation_index BIGINT NOT NULL,
    dispensed        BOOLEAN NOT NULL DEFAULT false,
    dispensed_at     TIMESTAMPTZ,
    session_id       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain, address),
    UNIQUE (tenant_id, chain, derivation_index)
);

CREATE INDEX idx_crypto_address_pool_available
    ON crypto_address_pool(tenant_id, chain)
    WHERE dispensed = false;

-- ── Crypto deposit address index: maps addresses to sessions ─────────────────
CREATE TABLE crypto_deposit_address_index (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chain      TEXT NOT NULL,
    address    TEXT NOT NULL,
    tenant_id  UUID NOT NULL,
    session_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain, address)
);

CREATE INDEX idx_deposit_address_index_tenant ON crypto_deposit_address_index(tenant_id);

-- ── Derivation index sequences per (tenant, chain) ──────────────────────────
-- Using a table + advisory lock pattern for portable sequence management.
CREATE TABLE crypto_derivation_counters (
    tenant_id UUID NOT NULL,
    chain     TEXT NOT NULL,
    next_index BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, chain)
);

-- ── Crypto deposit sessions: partitioned by created_at (monthly) ─────────────
-- At scale, deposit sessions could reach millions/day.
CREATE TABLE crypto_deposit_sessions (
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    idempotency_key     TEXT,
    status              deposit_session_status_enum NOT NULL DEFAULT 'PENDING_PAYMENT',
    version             BIGINT NOT NULL DEFAULT 0,

    chain               TEXT NOT NULL,
    token               TEXT NOT NULL,
    deposit_address     TEXT NOT NULL,
    expected_amount     NUMERIC(28, 8) NOT NULL,
    received_amount     NUMERIC(28, 8) NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL,

    collection_fee_bps  INTEGER NOT NULL DEFAULT 0,
    fee_amount          NUMERIC(28, 8) NOT NULL DEFAULT 0,
    net_amount          NUMERIC(28, 8) NOT NULL DEFAULT 0,

    settlement_pref     settlement_preference_enum NOT NULL DEFAULT 'HOLD',
    settlement_transfer_id UUID,

    derivation_index    BIGINT NOT NULL,

    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    detected_at         TIMESTAMPTZ,
    confirmed_at        TIMESTAMPTZ,
    credited_at         TIMESTAMPTZ,
    settled_at          TIMESTAMPTZ,
    expired_at          TIMESTAMPTZ,
    failed_at           TIMESTAMPTZ,

    failure_reason      TEXT,
    failure_code        TEXT,

    metadata            JSONB DEFAULT '{}',

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Idempotency scoped per tenant. Must include partition key for partitioned tables.
CREATE UNIQUE INDEX idx_deposit_sessions_tenant_idempotency
    ON crypto_deposit_sessions(tenant_id, idempotency_key, created_at)
    WHERE idempotency_key IS NOT NULL;

-- Query indexes
CREATE INDEX idx_deposit_sessions_tenant_status
    ON crypto_deposit_sessions(tenant_id, status, created_at DESC);
CREATE INDEX idx_deposit_sessions_tenant_created
    ON crypto_deposit_sessions(tenant_id, created_at DESC);
CREATE INDEX idx_deposit_sessions_address
    ON crypto_deposit_sessions(deposit_address, created_at DESC);
CREATE INDEX idx_deposit_sessions_expiry
    ON crypto_deposit_sessions(expires_at)
    WHERE status = 'PENDING_PAYMENT';

-- Monthly partitions: 6 months ahead from 2026-03 + default
CREATE TABLE crypto_deposit_sessions_y2026m03 PARTITION OF crypto_deposit_sessions
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE crypto_deposit_sessions_y2026m04 PARTITION OF crypto_deposit_sessions
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE crypto_deposit_sessions_y2026m05 PARTITION OF crypto_deposit_sessions
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE crypto_deposit_sessions_y2026m06 PARTITION OF crypto_deposit_sessions
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE crypto_deposit_sessions_y2026m07 PARTITION OF crypto_deposit_sessions
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE crypto_deposit_sessions_y2026m08 PARTITION OF crypto_deposit_sessions
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE crypto_deposit_sessions_default PARTITION OF crypto_deposit_sessions DEFAULT;


-- ── Crypto deposit transactions: partitioned by created_at (monthly) ─────────
-- Tracks individual on-chain transactions linked to deposit sessions.
CREATE TABLE crypto_deposit_transactions (
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    session_id       UUID NOT NULL,
    tenant_id        UUID NOT NULL,

    chain            TEXT NOT NULL,
    tx_hash          TEXT NOT NULL,
    from_address     TEXT NOT NULL,
    to_address       TEXT NOT NULL,
    token_contract   TEXT NOT NULL,
    amount           NUMERIC(28, 8) NOT NULL,
    block_number     BIGINT NOT NULL,
    block_hash       TEXT NOT NULL,
    confirmations    INTEGER NOT NULL DEFAULT 0,
    required_confirm INTEGER NOT NULL,
    confirmed        BOOLEAN NOT NULL DEFAULT false,

    detected_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Unique tx hash per chain (within partition)
CREATE UNIQUE INDEX idx_deposit_txns_chain_hash
    ON crypto_deposit_transactions(chain, tx_hash, created_at);
CREATE INDEX idx_deposit_txns_session
    ON crypto_deposit_transactions(session_id, created_at DESC);
CREATE INDEX idx_deposit_txns_unconfirmed
    ON crypto_deposit_transactions(confirmed, created_at)
    WHERE confirmed = false;

-- Monthly partitions
CREATE TABLE crypto_deposit_transactions_y2026m03 PARTITION OF crypto_deposit_transactions
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE crypto_deposit_transactions_y2026m04 PARTITION OF crypto_deposit_transactions
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE crypto_deposit_transactions_y2026m05 PARTITION OF crypto_deposit_transactions
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE crypto_deposit_transactions_y2026m06 PARTITION OF crypto_deposit_transactions
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE crypto_deposit_transactions_y2026m07 PARTITION OF crypto_deposit_transactions
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE crypto_deposit_transactions_y2026m08 PARTITION OF crypto_deposit_transactions
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE crypto_deposit_transactions_default PARTITION OF crypto_deposit_transactions DEFAULT;


-- ── Tenant crypto configuration columns ──────────────────────────────────────
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS crypto_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS default_settlement_pref TEXT NOT NULL DEFAULT 'HOLD'
    CHECK (default_settlement_pref IN ('AUTO_CONVERT','HOLD','THRESHOLD'));
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS supported_chains TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS min_confirmations_tron INTEGER NOT NULL DEFAULT 19;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS min_confirmations_eth INTEGER NOT NULL DEFAULT 12;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS min_confirmations_base INTEGER NOT NULL DEFAULT 12;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS payment_tolerance_bps INTEGER NOT NULL DEFAULT 50;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS default_session_ttl_secs INTEGER NOT NULL DEFAULT 3600;

-- Add crypto collection fee fields to fee_schedule JSONB (handled at app layer)
-- fee_schedule already stores: {"onramp_bps", "offramp_bps", "min_fee_usd", "max_fee_usd"}
-- We add: "crypto_collection_bps", "crypto_collection_max_fee_usd"

-- ── RLS policies for new tables ──────────────────────────────────────────────
DO $$ BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
            crypto_deposit_sessions, crypto_deposit_transactions,
            crypto_deposit_address_index, crypto_address_pool,
            crypto_derivation_counters, block_checkpoints, tokens
        TO settla_app;

        ALTER TABLE crypto_deposit_sessions ENABLE ROW LEVEL SECURITY;
        ALTER TABLE crypto_deposit_transactions ENABLE ROW LEVEL SECURITY;
        ALTER TABLE crypto_deposit_address_index ENABLE ROW LEVEL SECURITY;
        ALTER TABLE crypto_address_pool ENABLE ROW LEVEL SECURITY;

        CREATE POLICY tenant_isolation ON crypto_deposit_sessions
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
        CREATE POLICY tenant_isolation ON crypto_deposit_transactions
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
        CREATE POLICY tenant_isolation ON crypto_deposit_address_index
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
        CREATE POLICY tenant_isolation ON crypto_address_pool
            USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
    END IF;
END $$;

-- ── Autovacuum tuning for high-volume partitions ─────────────────────────────
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT c.relname AS child_name
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname IN ('crypto_deposit_sessions', 'crypto_deposit_transactions')
    LOOP
        EXECUTE format(
            'ALTER TABLE %I SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_analyze_scale_factor = 0.005)',
            r.child_name
        );
    END LOOP;
END;
$$;
