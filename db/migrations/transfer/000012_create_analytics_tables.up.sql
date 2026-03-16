-- ============================================================================
-- Analytics tables: pre-aggregated daily snapshots + async export jobs.
-- Snapshots use typed columns (not JSONB) for efficient B-tree index scans
-- at 50M rows/day scale.
-- ============================================================================

-- ── analytics_daily_snapshots: pre-aggregated daily metrics per tenant ──────
CREATE TABLE analytics_daily_snapshots (
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL,
    snapshot_date     DATE NOT NULL,
    metric_type       TEXT NOT NULL,  -- 'transfer', 'crypto_deposit', 'bank_deposit', 'provider'
    source_currency   TEXT NOT NULL DEFAULT '',
    dest_currency     TEXT NOT NULL DEFAULT '',
    provider          TEXT NOT NULL DEFAULT '',
    transfer_count    BIGINT NOT NULL DEFAULT 0,
    completed_count   BIGINT NOT NULL DEFAULT 0,
    failed_count      BIGINT NOT NULL DEFAULT 0,
    volume_usd        NUMERIC(28,8) NOT NULL DEFAULT 0,
    fees_usd          NUMERIC(28,8) NOT NULL DEFAULT 0,
    on_ramp_fees_usd  NUMERIC(28,8) NOT NULL DEFAULT 0,
    off_ramp_fees_usd NUMERIC(28,8) NOT NULL DEFAULT 0,
    network_fees_usd  NUMERIC(28,8) NOT NULL DEFAULT 0,
    avg_latency_ms    INTEGER NOT NULL DEFAULT 0,
    p50_latency_ms    INTEGER NOT NULL DEFAULT 0,
    p90_latency_ms    INTEGER NOT NULL DEFAULT 0,
    p95_latency_ms    INTEGER NOT NULL DEFAULT 0,
    success_rate      NUMERIC(5,2) NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    UNIQUE (tenant_id, snapshot_date, metric_type, source_currency, dest_currency, provider)
);

CREATE INDEX idx_snapshots_tenant_date ON analytics_daily_snapshots(tenant_id, snapshot_date DESC);
CREATE INDEX idx_snapshots_tenant_type_date ON analytics_daily_snapshots(tenant_id, metric_type, snapshot_date DESC);

-- ── analytics_export_jobs: async export tracking ───────────────────────────
CREATE TABLE analytics_export_jobs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','failed')),
    export_type         TEXT NOT NULL,
    parameters          JSONB NOT NULL DEFAULT '{}',
    file_path           TEXT,
    download_url        TEXT,
    download_expires_at TIMESTAMPTZ,
    row_count           BIGINT NOT NULL DEFAULT 0,
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_export_jobs_tenant ON analytics_export_jobs(tenant_id, created_at DESC);
CREATE INDEX idx_export_jobs_pending ON analytics_export_jobs(status) WHERE status = 'pending';
