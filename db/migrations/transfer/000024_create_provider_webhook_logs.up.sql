-- provider_webhook_logs — persistent storage for inbound provider webhooks
-- ============================================================================
-- Stores the raw payload from every provider webhook before normalization.
-- Critical for: deduplication, debugging, issue resolution, replay after normalizer fixes.
--
-- At scale (50M tx/day × ~2 webhooks/tx = ~100M rows/day), monthly partitions
-- are essential. Old partitions are dropped by PartitionManager (same as outbox).

CREATE TABLE IF NOT EXISTS provider_webhook_logs (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    provider_slug   TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    transfer_id     UUID,                    -- populated after normalization
    tenant_id       UUID,                    -- populated after normalization
    raw_body        BYTEA NOT NULL,
    normalized      JSONB,                   -- ProviderWebhookPayload JSON, null before normalization
    status          TEXT NOT NULL DEFAULT 'received'
                    CHECK (status IN ('received', 'processed', 'skipped', 'failed', 'duplicate')),
    error_message   TEXT,
    http_headers    JSONB,
    source_ip       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at    TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Default partition for overflow.
CREATE TABLE provider_webhook_logs_default PARTITION OF provider_webhook_logs DEFAULT;

-- Create monthly partitions (6 months ahead).
DO $$
DECLARE
    start_date DATE;
    end_date DATE;
    y INT;
    m INT;
BEGIN
    FOR i IN 0..6 LOOP
        start_date := date_trunc('month', CURRENT_DATE) + (i || ' months')::interval;
        end_date := start_date + '1 month'::interval;
        y := EXTRACT(YEAR FROM start_date);
        m := EXTRACT(MONTH FROM start_date);
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS provider_webhook_logs_%s_%s PARTITION OF provider_webhook_logs FOR VALUES FROM (%L) TO (%L)',
            y, lpad(m::text, 2, '0'), start_date, end_date
        );
    END LOOP;
END $$;

-- Deduplication: prevent processing the same webhook twice.
CREATE UNIQUE INDEX idx_provider_webhook_logs_dedup
    ON provider_webhook_logs(provider_slug, idempotency_key, created_at);

-- Lookup by transfer (debugging: "show me all webhooks for this transfer").
CREATE INDEX idx_provider_webhook_logs_transfer
    ON provider_webhook_logs(transfer_id, created_at DESC)
    WHERE transfer_id IS NOT NULL;

-- Operational: find failed/pending webhooks by provider.
CREATE INDEX idx_provider_webhook_logs_provider_status
    ON provider_webhook_logs(provider_slug, status, created_at DESC);
