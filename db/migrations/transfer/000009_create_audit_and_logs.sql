-- +goose Up


CREATE TABLE audit_log (
    id            UUID PRIMARY KEY,
    tenant_id     UUID NOT NULL,
    actor_type    VARCHAR(20) NOT NULL,
    actor_id      VARCHAR(255) NOT NULL,
    action        VARCHAR(100) NOT NULL,
    entity_type   VARCHAR(50) NOT NULL,
    entity_id     UUID,
    old_value     JSONB,
    new_value     JSONB,
    metadata      JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_log_tenant_created ON audit_log(tenant_id, created_at DESC);
CREATE INDEX idx_audit_log_entity ON audit_log(entity_type, entity_id, created_at DESC);
CREATE INDEX idx_audit_log_actor ON audit_log(actor_id, created_at DESC);


CREATE TABLE provider_webhook_logs (
    id                UUID NOT NULL,
    provider_slug     TEXT NOT NULL,
    idempotency_key   TEXT NOT NULL,
    transfer_id       UUID,
    tenant_id         UUID,
    raw_body          BYTEA NOT NULL,
    normalized        JSONB,
    status            TEXT NOT NULL DEFAULT 'received'
                          CHECK (status IN ('received','processed','skipped','failed','duplicate')),
    error_message     TEXT,
    http_headers      JSONB,
    source_ip         TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at      TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX idx_provider_webhook_logs_dedup
    ON provider_webhook_logs(provider_slug, idempotency_key, created_at);
CREATE INDEX idx_provider_webhook_logs_transfer
    ON provider_webhook_logs(transfer_id, created_at DESC)
    WHERE transfer_id IS NOT NULL;
CREATE INDEX idx_provider_webhook_logs_provider_status
    ON provider_webhook_logs(provider_slug, status, created_at DESC);

-- Create monthly partitions dynamically
DO $$
DECLARE
    m INT;
    start_date DATE;
    end_date DATE;
    part_name TEXT;
BEGIN
    FOR m IN 3..8 LOOP
        start_date := make_date(2026, m, 1);
        end_date   := start_date + INTERVAL '1 month';
        part_name  := 'provider_webhook_logs_' || to_char(start_date, 'YYYY_MM');
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF provider_webhook_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, start_date, end_date
        );
    END LOOP;
END $$;

CREATE TABLE provider_webhook_logs_default PARTITION OF provider_webhook_logs DEFAULT;


CREATE TABLE position_transactions (
    id              UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('TOP_UP','WITHDRAWAL','DEPOSIT_CREDIT','INTERNAL_REBALANCE')),
    currency        TEXT NOT NULL,
    location        TEXT NOT NULL,
    amount          NUMERIC(28,8) NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING','PROCESSING','COMPLETED','FAILED')),
    method          TEXT NOT NULL DEFAULT '',
    destination     TEXT NOT NULL DEFAULT '',
    reference       TEXT NOT NULL DEFAULT '',
    failure_reason  TEXT NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_position_transactions_tenant_status
    ON position_transactions(tenant_id, status);
CREATE INDEX idx_position_transactions_tenant_created
    ON position_transactions(tenant_id, created_at DESC);

CREATE TABLE position_transactions_2026_03 PARTITION OF position_transactions
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE position_transactions_2026_04 PARTITION OF position_transactions
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE position_transactions_2026_05 PARTITION OF position_transactions
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE position_transactions_2026_06 PARTITION OF position_transactions
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE position_transactions_2026_07 PARTITION OF position_transactions
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE position_transactions_2026_08 PARTITION OF position_transactions
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE position_transactions_default PARTITION OF position_transactions DEFAULT;

-- +goose Down
DROP TABLE IF EXISTS position_transactions;
DROP TABLE IF EXISTS provider_webhook_logs;
DROP TABLE IF EXISTS audit_log;
