-- +goose Up


CREATE TABLE quotes (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL,
    source_currency TEXT NOT NULL,
    source_amount   NUMERIC(28,8) NOT NULL,
    dest_currency   TEXT NOT NULL,
    dest_amount     NUMERIC(28,8) NOT NULL,
    stable_amount   NUMERIC(28,8) NOT NULL DEFAULT 0,
    fx_rate         NUMERIC(28,12) NOT NULL,
    fees            JSONB NOT NULL DEFAULT '{}',
    route           JSONB NOT NULL DEFAULT '{}',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_quotes_tenant_expires ON quotes(tenant_id, expires_at);


CREATE TABLE transfers (
    id                      UUID NOT NULL,
    tenant_id               UUID NOT NULL,
    external_ref            TEXT,
    idempotency_key         TEXT,
    status                  transfer_status_enum NOT NULL DEFAULT 'CREATED',
    version                 BIGINT NOT NULL DEFAULT 0,
    source_currency         TEXT NOT NULL,
    source_amount           NUMERIC(28,8) NOT NULL,
    dest_currency           TEXT NOT NULL,
    dest_amount             NUMERIC(28,8),
    stable_coin             TEXT,
    stable_amount           NUMERIC(28,8),
    chain                   TEXT,
    fx_rate                 NUMERIC(28,12),
    fees                    JSONB NOT NULL DEFAULT '{}',
    fee_schedule_snapshot   JSONB,
    sender                  JSONB NOT NULL,
    recipient               JSONB NOT NULL,
    quote_id                UUID,
    on_ramp_provider_id     TEXT,
    off_ramp_provider_id    TEXT,
    pii_encryption_version  SMALLINT NOT NULL DEFAULT 1,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    funded_at               TIMESTAMPTZ,
    completed_at            TIMESTAMPTZ,
    failed_at               TIMESTAMPTZ,
    failure_reason          TEXT,
    failure_code            TEXT,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Idempotency & external ref uniqueness
CREATE UNIQUE INDEX idx_transfers_tenant_idempotency
    ON transfers(tenant_id, idempotency_key, created_at)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX idx_transfers_tenant_external_ref
    ON transfers(tenant_id, external_ref, created_at)
    WHERE external_ref IS NOT NULL;

-- Query indexes
CREATE INDEX idx_transfers_tenant_status ON transfers(tenant_id, status, created_at DESC);
CREATE INDEX idx_transfers_tenant_created ON transfers(tenant_id, created_at DESC);
CREATE INDEX idx_transfers_analytics ON transfers(tenant_id, created_at DESC, status);
CREATE INDEX idx_transfers_settlement_period ON transfers(tenant_id, completed_at DESC)
    WHERE status = 'COMPLETED';
CREATE INDEX idx_transfers_stuck_detection ON transfers(status, updated_at, tenant_id)
    WHERE status NOT IN ('COMPLETED','FAILED','REFUNDED');
CREATE INDEX idx_transfers_tenant_updated_at ON transfers(tenant_id, updated_at DESC);
CREATE INDEX idx_transfers_idempotency_key
    ON transfers(tenant_id, idempotency_key, created_at DESC)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_transfers_external_ref
    ON transfers(tenant_id, external_ref, created_at DESC)
    WHERE external_ref IS NOT NULL AND external_ref != '';

CREATE TABLE transfers_y2026m03 PARTITION OF transfers
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE transfers_y2026m04 PARTITION OF transfers
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE transfers_y2026m05 PARTITION OF transfers
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE transfers_y2026m06 PARTITION OF transfers
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE transfers_y2026m07 PARTITION OF transfers
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE transfers_y2026m08 PARTITION OF transfers
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE transfers_default PARTITION OF transfers DEFAULT;


CREATE TABLE transfer_events (
    id           UUID NOT NULL,
    transfer_id  UUID NOT NULL,
    tenant_id    UUID NOT NULL,
    from_status  TEXT,
    to_status    TEXT NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata     JSONB NOT NULL DEFAULT '{}',
    provider_ref TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_transfer_events_tenant_transfer
    ON transfer_events(tenant_id, transfer_id, occurred_at DESC);

CREATE TABLE transfer_events_y2026m03 PARTITION OF transfer_events
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE transfer_events_y2026m04 PARTITION OF transfer_events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE transfer_events_y2026m05 PARTITION OF transfer_events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE transfer_events_y2026m06 PARTITION OF transfer_events
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE transfer_events_y2026m07 PARTITION OF transfer_events
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE transfer_events_y2026m08 PARTITION OF transfer_events
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE transfer_events_default PARTITION OF transfer_events DEFAULT;


CREATE TABLE provider_transactions (
    id          UUID NOT NULL,
    tenant_id   UUID NOT NULL REFERENCES tenants(id),
    provider    TEXT NOT NULL,
    tx_type     provider_tx_type_enum NOT NULL,
    external_id TEXT,
    transfer_id UUID NOT NULL,
    status      TEXT NOT NULL,
    amount      NUMERIC(28,8) NOT NULL,
    currency    TEXT NOT NULL,
    chain       TEXT,
    tx_hash     TEXT,
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_provider_txns_tenant_transfer ON provider_transactions(tenant_id, transfer_id);
CREATE UNIQUE INDEX uk_provider_txns_provider_external_id
    ON provider_transactions(provider, external_id) WHERE external_id IS NOT NULL AND external_id != '';
CREATE UNIQUE INDEX uk_provider_txns_transfer_type
    ON provider_transactions(tenant_id, transfer_id, tx_type);
CREATE INDEX idx_provider_txns_transfer_type ON provider_transactions(transfer_id, tx_type);
CREATE INDEX idx_provider_txns_external_id ON provider_transactions(external_id)
    WHERE external_id IS NOT NULL;

CREATE TABLE provider_transactions_y2026m03 PARTITION OF provider_transactions
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE provider_transactions_y2026m04 PARTITION OF provider_transactions
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE provider_transactions_y2026m05 PARTITION OF provider_transactions
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE provider_transactions_y2026m06 PARTITION OF provider_transactions
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE provider_transactions_y2026m07 PARTITION OF provider_transactions
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE provider_transactions_y2026m08 PARTITION OF provider_transactions
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE provider_transactions_default PARTITION OF provider_transactions DEFAULT;

-- +goose Down
DROP TABLE IF EXISTS provider_transactions;
DROP TABLE IF EXISTS transfer_events;
DROP TABLE IF EXISTS transfers;
DROP TABLE IF EXISTS quotes;
