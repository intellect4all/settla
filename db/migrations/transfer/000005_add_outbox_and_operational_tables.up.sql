-- ============================================================================
-- Consolidated migration: outbox + operational tables + webhook deliveries
-- Merges original migrations 000005, 000006, 000007, 000011, 000014
-- Creates enum types from the start (without the unused COMPLETING state).
-- ============================================================================

-- 1. Create enum types for type-safe status/type columns.
CREATE TYPE transfer_status_enum AS ENUM (
  'CREATED','FUNDED','ON_RAMPING','SETTLING','OFF_RAMPING',
  'COMPLETED','FAILED','REFUNDING','REFUNDED'
);

CREATE TYPE compensation_strategy_enum AS ENUM (
  'SIMPLE_REFUND','REVERSE_ONRAMP','CREDIT_STABLECOIN','MANUAL_REVIEW'
);

CREATE TYPE review_status_enum AS ENUM ('pending','investigating','resolved');

CREATE TYPE provider_tx_type_enum AS ENUM ('ON_RAMP','OFF_RAMP','BLOCKCHAIN');

-- 2. Convert existing columns to enum types.
-- transfers (partitioned) — drop inline CHECK then convert.
ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_status_check;
ALTER TABLE transfers ALTER COLUMN status DROP DEFAULT;
ALTER TABLE transfers
  ALTER COLUMN status TYPE transfer_status_enum
  USING status::transfer_status_enum;
ALTER TABLE transfers ALTER COLUMN status SET DEFAULT 'CREATED'::transfer_status_enum;

-- provider_transactions — tx_type column
ALTER TABLE provider_transactions DROP CONSTRAINT IF EXISTS provider_transactions_tx_type_check;
ALTER TABLE provider_transactions
  ALTER COLUMN tx_type TYPE provider_tx_type_enum
  USING tx_type::provider_tx_type_enum;

-- ============================================================================
-- outbox — transactional outbox for events and worker intents
-- At 500M entries/day (~85 GB slim entries). Cleanup: DROP partition (not DELETE).
-- Daily partitions because high volume + short retention.
-- ============================================================================

CREATE TABLE IF NOT EXISTS outbox (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
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

-- Relay polling: fetch unpublished entries in order
CREATE INDEX idx_outbox_unpublished ON outbox(published, created_at ASC)
    WHERE published = false;

-- Lookup by aggregate for debugging/testing
CREATE INDEX idx_outbox_aggregate ON outbox(aggregate_type, aggregate_id, created_at DESC);

-- Tenant-scoped lookups
CREATE INDEX idx_outbox_tenant ON outbox(tenant_id, created_at DESC);

-- Daily partitions: today + 3 days ahead + default
CREATE TABLE outbox_y2026m03d09 PARTITION OF outbox
    FOR VALUES FROM ('2026-03-09') TO ('2026-03-10');
CREATE TABLE outbox_y2026m03d10 PARTITION OF outbox
    FOR VALUES FROM ('2026-03-10') TO ('2026-03-11');
CREATE TABLE outbox_y2026m03d11 PARTITION OF outbox
    FOR VALUES FROM ('2026-03-11') TO ('2026-03-12');
CREATE TABLE outbox_y2026m03d12 PARTITION OF outbox
    FOR VALUES FROM ('2026-03-12') TO ('2026-03-13');
CREATE TABLE outbox_default PARTITION OF outbox DEFAULT;

-- ============================================================================
-- manual_reviews — transfers stuck in an intermediate state requiring human review
-- ============================================================================

CREATE TABLE IF NOT EXISTS manual_reviews (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transfer_id           UUID NOT NULL,
    tenant_id             UUID NOT NULL,
    status                review_status_enum NOT NULL DEFAULT 'pending',
    transfer_status       TEXT NOT NULL,
    stuck_since           TIMESTAMPTZ NOT NULL,
    attempted_recoveries  INT NOT NULL DEFAULT 0,
    resolution            TEXT,
    resolved_by           TEXT,
    resolved_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_manual_reviews_tenant_status ON manual_reviews(tenant_id, status, created_at DESC);
CREATE INDEX idx_manual_reviews_transfer ON manual_reviews(transfer_id);

-- ============================================================================
-- compensation_records — tracks refund/reversal compensation workflows
-- ============================================================================

CREATE TABLE IF NOT EXISTS compensation_records (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transfer_id      UUID NOT NULL,
    tenant_id        UUID NOT NULL,
    strategy         compensation_strategy_enum NOT NULL,
    steps_completed  JSONB NOT NULL DEFAULT '[]',
    steps_failed     JSONB NOT NULL DEFAULT '[]',
    refund_amount    NUMERIC(28, 8),
    refund_currency  TEXT,
    fx_loss          NUMERIC(28, 8),
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ
);

CREATE INDEX idx_compensation_records_tenant_status ON compensation_records(tenant_id, status, created_at DESC);
CREATE INDEX idx_compensation_records_transfer ON compensation_records(transfer_id);

-- ============================================================================
-- net_settlements — periodic netting for provider settlement
-- ============================================================================

CREATE TABLE IF NOT EXISTS net_settlements (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    period_start     TIMESTAMPTZ NOT NULL,
    period_end       TIMESTAMPTZ NOT NULL,
    corridors        JSONB NOT NULL DEFAULT '[]',
    net_by_currency  JSONB NOT NULL DEFAULT '{}',
    total_fees_usd   NUMERIC(28, 8) NOT NULL DEFAULT 0,
    instructions     JSONB NOT NULL DEFAULT '[]',
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'settled', 'overdue')),
    due_date         DATE,
    settled_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, period_start, period_end)
);

CREATE INDEX idx_net_settlements_tenant_status ON net_settlements(tenant_id, status, created_at DESC);

-- ============================================================================
-- reconciliation_reports — periodic reconciliation run results
-- ============================================================================

CREATE TABLE IF NOT EXISTS reconciliation_reports (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_name         TEXT NOT NULL,
    run_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_ms      INT NOT NULL DEFAULT 0,
    checks_run       INT NOT NULL DEFAULT 0,
    checks_passed    INT NOT NULL DEFAULT 0,
    discrepancies    JSONB NOT NULL DEFAULT '[]',
    auto_corrected   INT NOT NULL DEFAULT 0,
    needs_review     BOOLEAN NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reconciliation_reports_job ON reconciliation_reports(job_name, run_at DESC);
CREATE INDEX idx_reconciliation_reports_needs_review ON reconciliation_reports(needs_review, created_at DESC)
    WHERE needs_review = true;

-- ============================================================================
-- webhook_deliveries — persistent log of webhook delivery attempts
-- ============================================================================

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    transfer_id     UUID,
    delivery_id     TEXT NOT NULL,          -- X-Settla-Delivery header value
    webhook_url     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'delivered', 'failed', 'dead_letter')),
    status_code     INT,                    -- HTTP response code (null if network error)
    attempt         INT NOT NULL DEFAULT 1,
    max_attempts    INT NOT NULL DEFAULT 5,
    error_message   TEXT,
    request_body    JSONB,                  -- webhook payload (stored for debugging)
    duration_ms     INT,                    -- delivery latency
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ,
    next_retry_at   TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Create monthly partitions (6 months ahead + default)
CREATE TABLE webhook_deliveries_default PARTITION OF webhook_deliveries DEFAULT;
DO $$
DECLARE
    m INT;
    y INT;
    start_date DATE;
    end_date DATE;
BEGIN
    FOR i IN 0..6 LOOP
        start_date := date_trunc('month', CURRENT_DATE) + (i || ' months')::interval;
        end_date := start_date + '1 month'::interval;
        y := EXTRACT(YEAR FROM start_date);
        m := EXTRACT(MONTH FROM start_date);
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS webhook_deliveries_%s_%s PARTITION OF webhook_deliveries FOR VALUES FROM (%L) TO (%L)',
            y, lpad(m::text, 2, '0'), start_date, end_date
        );
    END LOOP;
END $$;

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

-- ============================================================================
-- webhook_event_subscriptions — event types a tenant wants to receive
-- ============================================================================

CREATE TABLE IF NOT EXISTS webhook_event_subscriptions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    event_type  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, event_type)
);

CREATE INDEX idx_webhook_event_subs_tenant
    ON webhook_event_subscriptions(tenant_id);

-- Add webhook_events column to tenants for quick filter check
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS webhook_events TEXT[] DEFAULT '{}';
