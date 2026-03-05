-- ============================================================================
-- transfers — the core transfer aggregate, partitioned by created_at (monthly)
-- At 50M txn/day, transfers table receives ~50M inserts/day.
-- Monthly partitions hold ~1.5B rows each.
-- ============================================================================

CREATE TABLE transfers (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    external_ref    TEXT,
    idempotency_key TEXT,
    status          TEXT NOT NULL DEFAULT 'CREATED'
                    CHECK (status IN ('CREATED','FUNDED','ON_RAMPING','SETTLING',
                           'OFF_RAMPING','COMPLETING','COMPLETED','FAILED','REFUNDING','REFUNDED')),
    version         BIGINT NOT NULL DEFAULT 0,
    source_currency   TEXT NOT NULL,
    source_amount     NUMERIC(28, 8) NOT NULL,
    dest_currency     TEXT NOT NULL,
    dest_amount       NUMERIC(28, 8),
    stable_coin       TEXT,
    stable_amount     NUMERIC(28, 8),
    chain             TEXT,
    fx_rate           NUMERIC(28, 12),
    fees              JSONB DEFAULT '{}',
    sender            JSONB NOT NULL,
    recipient         JSONB NOT NULL,
    quote_id          UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    funded_at         TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    failed_at         TIMESTAMPTZ,
    failure_reason    TEXT,
    failure_code      TEXT,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Idempotency scoped per tenant. Must include partition key (created_at) for partitioned tables.
-- Effectively unique within each partition; application layer ensures cross-partition uniqueness
-- by always using the same created_at window for idempotent retries.
CREATE UNIQUE INDEX idx_transfers_tenant_idempotency
    ON transfers(tenant_id, idempotency_key, created_at) WHERE idempotency_key IS NOT NULL;
-- External refs scoped per tenant (same partition key requirement).
CREATE UNIQUE INDEX idx_transfers_tenant_external_ref
    ON transfers(tenant_id, external_ref, created_at) WHERE external_ref IS NOT NULL;
-- Query indexes (include tenant_id for partition elimination)
CREATE INDEX idx_transfers_tenant_status ON transfers(tenant_id, status, created_at DESC);
CREATE INDEX idx_transfers_tenant_created ON transfers(tenant_id, created_at DESC);

-- Monthly partitions: 6 months ahead from 2026-03 + default
-- At 50M txn/day, each monthly partition holds ~1.5B rows.
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


-- ============================================================================
-- transfer_events — state transition audit log, partitioned by created_at
-- At 50M txn/day, transfer_events receives ~50M inserts/day.
-- ============================================================================

CREATE TABLE transfer_events (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    transfer_id     UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    from_status     TEXT,
    to_status       TEXT NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata        JSONB DEFAULT '{}',
    provider_ref    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_transfer_events_tenant_transfer ON transfer_events(tenant_id, transfer_id, occurred_at DESC);

-- Monthly partitions — same cadence as transfers (~1.5B rows/month)
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


-- ============================================================================
-- quotes — FX quotes with TTL, not partitioned (lower volume)
-- ============================================================================

CREATE TABLE quotes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    source_currency TEXT NOT NULL,
    source_amount   NUMERIC(28, 8) NOT NULL,
    dest_currency   TEXT NOT NULL,
    dest_amount     NUMERIC(28, 8) NOT NULL,
    fx_rate         NUMERIC(28, 12) NOT NULL,
    fees            JSONB NOT NULL DEFAULT '{}',
    route           JSONB NOT NULL DEFAULT '{}',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_quotes_tenant_expires ON quotes(tenant_id, expires_at);


-- ============================================================================
-- provider_transactions — external provider tracking, not partitioned (lower volume)
-- ============================================================================

CREATE TABLE provider_transactions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    provider        TEXT NOT NULL,
    tx_type         TEXT NOT NULL CHECK (tx_type IN ('ON_RAMP','OFF_RAMP','BLOCKCHAIN')),
    external_id     TEXT,
    transfer_id     UUID NOT NULL,
    status          TEXT NOT NULL,
    amount          NUMERIC(28, 8) NOT NULL,
    currency        TEXT NOT NULL,
    chain           TEXT,
    tx_hash         TEXT,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_provider_txns_tenant_transfer ON provider_transactions(tenant_id, transfer_id);
