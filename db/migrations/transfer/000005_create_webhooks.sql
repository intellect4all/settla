-- +goose Up


CREATE TABLE webhook_deliveries (
    id              UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    transfer_id     UUID,
    delivery_id     TEXT NOT NULL,
    webhook_url     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','delivered','failed','dead_letter')),
    status_code     INT,
    attempt         INT NOT NULL DEFAULT 1,
    max_attempts    INT NOT NULL DEFAULT 5,
    error_message   TEXT,
    request_body    JSONB,
    duration_ms     INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ,
    next_retry_at   TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_webhook_deliveries_tenant_created
    ON webhook_deliveries(tenant_id, created_at DESC);
CREATE INDEX idx_webhook_deliveries_tenant_status
    ON webhook_deliveries(tenant_id, status, created_at DESC);
CREATE INDEX idx_webhook_deliveries_transfer
    ON webhook_deliveries(transfer_id, created_at DESC)
    WHERE transfer_id IS NOT NULL;
CREATE INDEX idx_webhook_deliveries_pending_retry
    ON webhook_deliveries(next_retry_at)
    WHERE status = 'pending' AND next_retry_at IS NOT NULL;

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
        part_name  := 'webhook_deliveries_y2026m' || lpad(m::text, 2, '0');
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF webhook_deliveries FOR VALUES FROM (%L) TO (%L)',
            part_name, start_date, end_date
        );
    END LOOP;
END $$;

CREATE TABLE webhook_deliveries_default PARTITION OF webhook_deliveries DEFAULT;


CREATE TABLE webhook_event_subscriptions (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL,
    event_type  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, event_type)
);

CREATE INDEX idx_webhook_event_subs_tenant ON webhook_event_subscriptions(tenant_id);

-- +goose Down
DROP TABLE IF EXISTS webhook_event_subscriptions;
DROP TABLE IF EXISTS webhook_deliveries;
