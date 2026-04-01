-- +goose Up

CREATE TABLE outbox (
    id              UUID NOT NULL,
    aggregate_type  TEXT NOT NULL,
    aggregate_id    UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    is_intent       BOOLEAN NOT NULL DEFAULT false,
    published       BOOLEAN NOT NULL DEFAULT false,
    published_at    TIMESTAMPTZ,
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 5,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Only a default partition — the PartitionManager creates daily partitions at runtime.
CREATE TABLE outbox_default PARTITION OF outbox DEFAULT;

-- Relay & query indexes (final improved versions)
CREATE INDEX idx_outbox_unpublished ON outbox(published, created_at ASC) WHERE published = false;
CREATE INDEX idx_outbox_aggregate ON outbox(aggregate_type, aggregate_id, created_at DESC);
CREATE INDEX idx_outbox_tenant ON outbox(tenant_id, created_at DESC);
CREATE INDEX idx_outbox_relay_covering ON outbox(published, created_at ASC, published_at)
    WHERE published = false;
CREATE INDEX idx_outbox_relay_poll ON outbox(created_at ASC, retry_count)
    WHERE published = false;

-- +goose Down
DROP TABLE IF EXISTS outbox;
