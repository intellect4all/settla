-- ============================================================================
-- Position Events: event-sourced audit log for all treasury position mutations.
-- Every Credit, Debit, Reserve, Release, Commit, and Consume operation is
-- recorded as an immutable event. Serves as:
--   1. Complete audit trail for compliance and tenant-facing history
--   2. Crash recovery source (replay events after last snapshot)
--   3. Real-time streaming source (published to NATS for SSE)
--
-- At peak load (~20,000 events/sec), events are batch-inserted every 10ms
-- by the treasury event writer goroutine.
-- ============================================================================

CREATE TABLE position_events (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    position_id     UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL CHECK (event_type IN ('CREDIT', 'DEBIT', 'RESERVE', 'RELEASE', 'COMMIT', 'CONSUME')),
    amount          NUMERIC(28, 8) NOT NULL,
    balance_after   NUMERIC(28, 8) NOT NULL,
    locked_after    NUMERIC(28, 8) NOT NULL,
    reference_id    UUID NOT NULL,
    reference_type  TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, recorded_at)
) PARTITION BY RANGE (recorded_at);

-- Idempotency: prevent duplicate events within the same partition.
CREATE UNIQUE INDEX idx_position_events_idempotency
    ON position_events (idempotency_key, recorded_at);

-- Crash recovery: replay events after the last flush timestamp for a position.
CREATE INDEX idx_position_events_recovery
    ON position_events (position_id, recorded_at);

-- Tenant-facing history: query events for a specific position.
CREATE INDEX idx_position_events_tenant_history
    ON position_events (tenant_id, position_id, recorded_at DESC);

-- Monthly partitions: 6 months (2026-03 through 2026-08) + default
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

-- Update reserve_ops CHECK constraint to include new op types.
ALTER TABLE reserve_ops DROP CONSTRAINT IF EXISTS reserve_ops_op_type_check;
ALTER TABLE reserve_ops ADD CONSTRAINT reserve_ops_op_type_check
    CHECK (op_type IN ('reserve', 'release', 'commit', 'consume', 'credit', 'debit'));

-- Grant access to the position_events table.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE position_events TO settla_app;

-- RLS for tenant isolation.
ALTER TABLE position_events ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON position_events
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
