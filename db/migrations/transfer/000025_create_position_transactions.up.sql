-- Position Transactions: tenant-initiated position changes (top-up, withdrawal,
-- deposit credit, internal rebalance). Each transaction follows a state machine:
-- PENDING → PROCESSING → COMPLETED/FAILED.

CREATE TABLE position_transactions (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('TOP_UP', 'WITHDRAWAL', 'DEPOSIT_CREDIT', 'INTERNAL_REBALANCE')),
    currency        TEXT NOT NULL,
    location        TEXT NOT NULL,
    amount          NUMERIC(28, 8) NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PROCESSING', 'COMPLETED', 'FAILED')),
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
    ON position_transactions (tenant_id, status);
CREATE INDEX idx_position_transactions_tenant_created
    ON position_transactions (tenant_id, created_at DESC);

-- Monthly partitions: 6 months (2026-03 through 2026-08) + default
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

-- Grant access.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE position_transactions TO settla_app;

-- RLS for tenant isolation.
ALTER TABLE position_transactions ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON position_transactions
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
