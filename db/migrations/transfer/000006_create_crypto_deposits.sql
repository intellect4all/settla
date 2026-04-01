-- +goose Up


CREATE TABLE tokens (
    id                UUID PRIMARY KEY,
    chain             TEXT NOT NULL,
    symbol            TEXT NOT NULL,
    contract_address  TEXT NOT NULL,
    decimals          INTEGER NOT NULL DEFAULT 6,
    is_active         BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(chain, symbol),
    UNIQUE(chain, contract_address)
);

CREATE INDEX idx_tokens_chain_active ON tokens(chain) WHERE is_active = true;


CREATE TABLE block_checkpoints (
    id            UUID PRIMARY KEY,
    chain         TEXT NOT NULL UNIQUE,
    block_number  BIGINT NOT NULL DEFAULT 0,
    block_hash    TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);


CREATE TABLE crypto_address_pool (
    id                UUID PRIMARY KEY,
    tenant_id         UUID NOT NULL REFERENCES tenants(id),
    chain             TEXT NOT NULL,
    address           TEXT NOT NULL,
    derivation_index  BIGINT NOT NULL,
    dispensed         BOOLEAN NOT NULL DEFAULT false,
    dispensed_at      TIMESTAMPTZ,
    session_id        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(chain, address),
    UNIQUE(tenant_id, chain, derivation_index)
);

-- Improved index with derivation_index for ordered dispensing (from migration 029)
CREATE INDEX idx_crypto_address_pool_available
    ON crypto_address_pool(tenant_id, chain, derivation_index ASC)
    WHERE dispensed = false;


CREATE TABLE crypto_deposit_address_index (
    id          UUID PRIMARY KEY,
    chain       TEXT NOT NULL,
    address     TEXT NOT NULL,
    tenant_id   UUID NOT NULL,
    session_id  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(chain, address)
);

CREATE INDEX idx_deposit_address_index_tenant ON crypto_deposit_address_index(tenant_id);


CREATE TABLE crypto_derivation_counters (
    tenant_id   UUID NOT NULL,
    chain       TEXT NOT NULL,
    next_index  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, chain)
);


CREATE TABLE crypto_deposit_sessions (
    id                      UUID NOT NULL,
    tenant_id               UUID NOT NULL,
    idempotency_key         TEXT,
    status                  deposit_session_status_enum NOT NULL DEFAULT 'PENDING_PAYMENT',
    version                 BIGINT NOT NULL DEFAULT 0,
    chain                   TEXT NOT NULL,
    token                   TEXT NOT NULL,
    deposit_address         TEXT NOT NULL,
    expected_amount         NUMERIC(28,8) NOT NULL,
    received_amount         NUMERIC(28,8) NOT NULL DEFAULT 0,
    currency                TEXT NOT NULL,
    collection_fee_bps      INTEGER NOT NULL DEFAULT 0,
    fee_amount              NUMERIC(28,8) NOT NULL DEFAULT 0,
    net_amount              NUMERIC(28,8) NOT NULL DEFAULT 0,
    settlement_pref         settlement_preference_enum NOT NULL DEFAULT 'HOLD',
    settlement_transfer_id  UUID,
    derivation_index        BIGINT NOT NULL,
    expires_at              TIMESTAMPTZ NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    detected_at             TIMESTAMPTZ,
    confirmed_at            TIMESTAMPTZ,
    credited_at             TIMESTAMPTZ,
    settled_at              TIMESTAMPTZ,
    expired_at              TIMESTAMPTZ,
    failed_at               TIMESTAMPTZ,
    failure_reason          TEXT,
    failure_code            TEXT,
    metadata                JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX idx_deposit_sessions_tenant_idempotency
    ON crypto_deposit_sessions(tenant_id, idempotency_key, created_at)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_deposit_sessions_tenant_status
    ON crypto_deposit_sessions(tenant_id, status, created_at DESC);
CREATE INDEX idx_deposit_sessions_tenant_created
    ON crypto_deposit_sessions(tenant_id, created_at DESC);
CREATE INDEX idx_deposit_sessions_address
    ON crypto_deposit_sessions(deposit_address, created_at DESC);
CREATE INDEX idx_deposit_sessions_expiry
    ON crypto_deposit_sessions(expires_at) WHERE status = 'PENDING_PAYMENT';

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


CREATE TABLE crypto_deposit_transactions (
    id                UUID NOT NULL,
    session_id        UUID NOT NULL,
    tenant_id         UUID NOT NULL,
    chain             TEXT NOT NULL,
    tx_hash           TEXT NOT NULL,
    from_address      TEXT NOT NULL,
    to_address        TEXT NOT NULL,
    token_contract    TEXT NOT NULL,
    amount            NUMERIC(28,8) NOT NULL,
    block_number      BIGINT NOT NULL,
    block_hash        TEXT NOT NULL,
    confirmations     INTEGER NOT NULL DEFAULT 0,
    required_confirm  INTEGER NOT NULL,
    confirmed         BOOLEAN NOT NULL DEFAULT false,
    detected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX idx_deposit_txns_chain_hash
    ON crypto_deposit_transactions(chain, tx_hash, created_at);
CREATE INDEX idx_deposit_txns_session
    ON crypto_deposit_transactions(session_id, created_at DESC);
CREATE INDEX idx_deposit_txns_unconfirmed
    ON crypto_deposit_transactions(confirmed, created_at)
    WHERE confirmed = false;
-- Improved index for unconfirmed tx scanning (from migration 030)
CREATE INDEX idx_crypto_deposit_tx_unconfirmed
    ON crypto_deposit_transactions(created_at ASC) WHERE confirmed = false;

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

-- +goose Down
DROP TABLE IF EXISTS crypto_deposit_transactions;
DROP TABLE IF EXISTS crypto_deposit_sessions;
DROP TABLE IF EXISTS crypto_derivation_counters;
DROP TABLE IF EXISTS crypto_deposit_address_index;
DROP TABLE IF EXISTS crypto_address_pool;
DROP TABLE IF EXISTS block_checkpoints;
DROP TABLE IF EXISTS tokens;
