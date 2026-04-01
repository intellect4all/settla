-- +goose Up

CREATE TABLE positions (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL,
    currency        TEXT NOT NULL,
    location        TEXT NOT NULL,
    balance         NUMERIC(28,8) NOT NULL DEFAULT 0,
    locked          NUMERIC(28,8) NOT NULL DEFAULT 0,
    min_balance     NUMERIC(28,8) NOT NULL DEFAULT 0,
    target_balance  NUMERIC(28,8),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, currency, location)
);

CREATE INDEX idx_positions_tenant_currency ON positions(tenant_id, currency);

CREATE TABLE position_history (
    id            UUID NOT NULL,
    position_id   UUID NOT NULL,
    tenant_id     UUID NOT NULL,
    balance       NUMERIC(28,8) NOT NULL,
    locked        NUMERIC(28,8) NOT NULL,
    trigger_type  TEXT,
    trigger_ref   UUID,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, recorded_at)
) PARTITION BY RANGE (recorded_at);

CREATE INDEX idx_position_history_position ON position_history(position_id, recorded_at DESC);
CREATE INDEX idx_position_history_tenant ON position_history(tenant_id, recorded_at DESC);

CREATE TABLE position_history_2026_03 PARTITION OF position_history
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE position_history_2026_04 PARTITION OF position_history
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE position_history_2026_05 PARTITION OF position_history
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE position_history_2026_06 PARTITION OF position_history
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE position_history_2026_07 PARTITION OF position_history
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE position_history_2026_08 PARTITION OF position_history
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE position_history_default PARTITION OF position_history DEFAULT;

CREATE TABLE reserve_ops (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL,
    currency    TEXT NOT NULL,
    location    TEXT NOT NULL,
    amount      NUMERIC(28,8) NOT NULL,
    reference   UUID NOT NULL,
    op_type     TEXT NOT NULL CHECK (op_type IN ('reserve','release','commit','consume','credit','debit')),
    completed   BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reserve_ops_uncommitted ON reserve_ops(completed, created_at) WHERE completed = false;
CREATE INDEX idx_reserve_ops_reference ON reserve_ops(reference, op_type);
CREATE INDEX idx_reserve_ops_cleanup ON reserve_ops(completed, created_at) WHERE completed = true;

CREATE TABLE position_events (
    id              UUID NOT NULL,
    position_id     UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL CHECK (event_type IN ('CREDIT','DEBIT','RESERVE','RELEASE','COMMIT','CONSUME')),
    amount          NUMERIC(28,8) NOT NULL,
    balance_after   NUMERIC(28,8) NOT NULL,
    locked_after    NUMERIC(28,8) NOT NULL,
    reference_id    UUID NOT NULL,
    reference_type  TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, recorded_at)
) PARTITION BY RANGE (recorded_at);

CREATE UNIQUE INDEX idx_position_events_idempotency ON position_events(idempotency_key, recorded_at);
CREATE INDEX idx_position_events_recovery ON position_events(position_id, recorded_at);
CREATE INDEX idx_position_events_tenant_history ON position_events(tenant_id, position_id, recorded_at DESC);

CREATE TABLE position_events_2026_03 PARTITION OF position_events
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE position_events_2026_04 PARTITION OF position_events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE position_events_2026_05 PARTITION OF position_events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE position_events_2026_06 PARTITION OF position_events
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE position_events_2026_07 PARTITION OF position_events
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE position_events_2026_08 PARTITION OF position_events
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE position_events_default PARTITION OF position_events DEFAULT;

-- +goose Down
DROP TABLE IF EXISTS position_events;
DROP TABLE IF EXISTS reserve_ops;
DROP TABLE IF EXISTS position_history;
DROP TABLE IF EXISTS positions;
