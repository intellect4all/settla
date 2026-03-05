-- Settla Treasury — Per-Tenant Position Snapshots
-- These tables are updated by the treasury flush goroutine every 100ms,
-- NOT per-transaction. The in-memory reservation system in settla-server
-- is the real-time authority for available balance.
-- On restart, positions are loaded from this table into memory.

CREATE TABLE positions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    currency        TEXT NOT NULL,
    location        TEXT NOT NULL,               -- e.g. "bank:clearing", "chain:tron", "bank:mercury"
    balance         NUMERIC(28, 8) NOT NULL DEFAULT 0,
    locked          NUMERIC(28, 8) NOT NULL DEFAULT 0,
    min_balance     NUMERIC(28, 8) NOT NULL DEFAULT 0,
    target_balance  NUMERIC(28, 8),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, currency, location)
);

CREATE INDEX idx_positions_tenant_currency ON positions(tenant_id, currency);

-- Position history for audit trail. Partitioned monthly.
-- Written by the flush goroutine alongside position updates.
CREATE TABLE position_history (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    position_id     UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    balance         NUMERIC(28, 8) NOT NULL,
    locked          NUMERIC(28, 8) NOT NULL,
    trigger_type    TEXT,                        -- e.g. "transfer", "manual_adjustment", "flush"
    trigger_ref     UUID,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, recorded_at)
) PARTITION BY RANGE (recorded_at);

CREATE INDEX idx_position_history_position ON position_history(position_id, recorded_at DESC);
CREATE INDEX idx_position_history_tenant ON position_history(tenant_id, recorded_at DESC);

-- Monthly partitions: 6 months (2026-03 through 2026-08) + default
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
