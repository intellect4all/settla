-- Partition net_settlements by created_at (monthly) to support unbounded growth
-- at 20K-100K tenants. Without partitioning, this table would accumulate
-- ~36.5M rows/year at 100K tenants, degrading query performance over time.
--
-- Note: PostgreSQL requires the partition key (created_at) in every unique
-- constraint. The UNIQUE on (tenant_id, period_start, period_end) becomes
-- (tenant_id, period_start, period_end, created_at). Since created_at defaults
-- to now() and settlements are created once per period, collisions are
-- practically impossible.

BEGIN;

-- 1. Preserve existing data
ALTER TABLE net_settlements RENAME TO net_settlements_old;

-- 2. Create partitioned replacement
CREATE TABLE net_settlements (
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
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
    PRIMARY KEY (id, created_at),
    UNIQUE (tenant_id, period_start, period_end, created_at)
) PARTITION BY RANGE (created_at);

-- 3. Default partition catches historical data and any rows outside explicit ranges
CREATE TABLE net_settlements_default PARTITION OF net_settlements DEFAULT;

-- 4. Monthly partitions: current month + 6 months ahead
CREATE TABLE net_settlements_y2026m03 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE net_settlements_y2026m04 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE net_settlements_y2026m05 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE net_settlements_y2026m06 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE net_settlements_y2026m07 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE net_settlements_y2026m08 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE net_settlements_y2026m09 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

-- 5. Migrate existing data
INSERT INTO net_settlements SELECT * FROM net_settlements_old;

-- 6. Drop old table
DROP TABLE net_settlements_old;

-- 7. Recreate indexes
CREATE INDEX idx_net_settlements_tenant_status ON net_settlements (tenant_id, status);
CREATE INDEX idx_net_settlements_due_date ON net_settlements (due_date)
    WHERE status IN ('pending', 'overdue');

COMMIT;
