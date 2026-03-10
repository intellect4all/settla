-- ============================================================================
-- Consolidated migration: reserve ops table + RLS
-- Merges original treasury migrations 000002 and 000003
-- ============================================================================

-- Reserve operations log for treasury crash recovery.
CREATE TABLE reserve_ops (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    currency    TEXT NOT NULL,
    location    TEXT NOT NULL,
    amount      NUMERIC(28, 8) NOT NULL,
    reference   UUID NOT NULL,
    op_type     TEXT NOT NULL CHECK (op_type IN ('reserve', 'release', 'commit')),
    completed   BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reserve_ops_uncommitted ON reserve_ops(completed, created_at)
    WHERE completed = false;
CREATE INDEX idx_reserve_ops_reference ON reserve_ops(reference, op_type);
CREATE INDEX idx_reserve_ops_cleanup ON reserve_ops(completed, created_at)
    WHERE completed = true;

-- ============================================================================
-- RLS: Row-Level Security for tenant isolation on Treasury DB.
-- ============================================================================

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
    CREATE ROLE settla_app LOGIN PASSWORD 'settla_app';
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE positions, position_history, reserve_ops TO settla_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;

ALTER TABLE positions ENABLE ROW LEVEL SECURITY;
ALTER TABLE position_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE reserve_ops ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON positions
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON position_history
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON reserve_ops
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
