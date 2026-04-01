-- +goose Up

-- Create application role with configurable password.
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
    EXECUTE format('CREATE ROLE settla_app LOGIN PASSWORD %L',
      coalesce(current_setting('app.settla_app_password', true), 'settla_app_dev'));
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE accounts, journal_entries TO settla_app;
GRANT SELECT ON TABLE entry_lines, balance_snapshots TO settla_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;

-- RLS: accounts (tenant_id nullable — system accounts have NULL)
ALTER TABLE accounts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON accounts
    FOR ALL TO settla_app
    USING  (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- RLS: journal_entries (tenant_id nullable — system entries have NULL)
ALTER TABLE journal_entries ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON journal_entries
    FOR ALL TO settla_app
    USING  (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON journal_entries;
DROP POLICY IF EXISTS tenant_isolation ON accounts;
ALTER TABLE journal_entries DISABLE ROW LEVEL SECURITY;
ALTER TABLE accounts DISABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE accounts, journal_entries, entry_lines, balance_snapshots FROM settla_app;
REVOKE USAGE ON ALL SEQUENCES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON SCHEMA public FROM settla_app;
