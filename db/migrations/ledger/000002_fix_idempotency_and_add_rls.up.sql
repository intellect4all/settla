-- ============================================================================
-- Consolidated migration: fix idempotency index + add RLS
-- Merges original ledger migrations 000002 and 000003
-- ============================================================================

-- Fix journal_entries idempotency index to be tenant-scoped.
DROP INDEX IF EXISTS idx_journal_entries_idempotency;

-- Tenant entries: unique per (tenant, key, month-partition)
CREATE UNIQUE INDEX idx_journal_entries_idempotency_tenant
    ON journal_entries(tenant_id, idempotency_key, posted_at)
    WHERE tenant_id IS NOT NULL AND idempotency_key IS NOT NULL;

-- System entries: globally unique per (key, month-partition)
CREATE UNIQUE INDEX idx_journal_entries_idempotency_system
    ON journal_entries(idempotency_key, posted_at)
    WHERE tenant_id IS NULL AND idempotency_key IS NOT NULL;

-- ============================================================================
-- RLS: Row-Level Security for tenant isolation on Ledger DB.
-- accounts and journal_entries have nullable tenant_id (system accounts have NULL).
-- entry_lines and balance_snapshots have no tenant_id column — exempt from RLS.
-- ============================================================================

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
    CREATE ROLE settla_app LOGIN PASSWORD 'settla_app';
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE accounts, journal_entries TO settla_app;
GRANT SELECT ON TABLE entry_lines, balance_snapshots TO settla_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;

ALTER TABLE accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_entries ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON accounts
  USING (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON journal_entries
  USING (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid);
