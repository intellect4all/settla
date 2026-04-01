-- +goose Up


CREATE TABLE banking_partners (
    id                    UUID PRIMARY KEY,
    name                  TEXT NOT NULL,
    webhook_secret        TEXT,
    supported_currencies  TEXT[] NOT NULL DEFAULT '{}',
    is_active             BOOLEAN NOT NULL DEFAULT true,
    metadata              JSONB NOT NULL DEFAULT '{}',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);


CREATE TABLE virtual_account_pool (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    banking_partner_id  UUID NOT NULL REFERENCES banking_partners(id),
    account_number      TEXT NOT NULL UNIQUE,
    account_name        TEXT NOT NULL DEFAULT '',
    sort_code           TEXT NOT NULL DEFAULT '',
    iban                TEXT NOT NULL DEFAULT '',
    currency            TEXT NOT NULL,
    account_type        virtual_account_type_enum NOT NULL DEFAULT 'TEMPORARY',
    available           BOOLEAN NOT NULL DEFAULT true,
    session_id          UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_virtual_account_pool_available
    ON virtual_account_pool(tenant_id, currency, available) WHERE available = true;
CREATE INDEX idx_virtual_account_pool_unavailable
    ON virtual_account_pool(account_number) WHERE available = false;


CREATE TABLE virtual_account_index (
    id              UUID PRIMARY KEY,
    account_number  TEXT NOT NULL,
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    session_id      UUID,
    account_type    virtual_account_type_enum NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_virtual_account_index_number ON virtual_account_index(account_number);


CREATE TABLE bank_deposit_sessions (
    id                      UUID NOT NULL,
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
    metadata                JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX idx_bank_deposit_sessions_tenant_idempotency
    ON bank_deposit_sessions(tenant_id, idempotency_key, created_at)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_bank_deposit_sessions_tenant_created
    ON bank_deposit_sessions(tenant_id, created_at DESC);
CREATE INDEX idx_bank_deposit_sessions_tenant_status
    ON bank_deposit_sessions(tenant_id, status);
CREATE INDEX idx_bank_deposit_sessions_account
    ON bank_deposit_sessions(account_number);
CREATE INDEX idx_bank_deposit_sessions_expiry
    ON bank_deposit_sessions(expires_at) WHERE status = 'PENDING_PAYMENT';
CREATE INDEX idx_bank_deposit_sessions_webhook_lookup
    ON bank_deposit_sessions(account_number, status, created_at DESC)
    WHERE status = 'PENDING_PAYMENT';

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


CREATE TABLE bank_deposit_transactions (
    id                    UUID NOT NULL,
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

CREATE UNIQUE INDEX idx_bank_deposit_txns_reference
    ON bank_deposit_transactions(bank_reference, created_at);
CREATE INDEX idx_bank_deposit_txns_session
    ON bank_deposit_transactions(session_id, created_at DESC);

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

-- +goose Down
DROP TABLE IF EXISTS bank_deposit_transactions;
DROP TABLE IF EXISTS bank_deposit_sessions;
DROP TABLE IF EXISTS virtual_account_index;
DROP TABLE IF EXISTS virtual_account_pool;
DROP TABLE IF EXISTS banking_partners;
