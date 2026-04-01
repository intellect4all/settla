-- +goose Up

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
    EXECUTE format('CREATE ROLE settla_app LOGIN PASSWORD %L',
      coalesce(current_setting('app.settla_app_password', true), 'settla_app_dev'));
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE positions, position_history, reserve_ops, position_events TO settla_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;

ALTER TABLE positions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON positions
    FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE position_history ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON position_history
    FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE reserve_ops ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON reserve_ops
    FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE position_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON position_events
    FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON position_events;
DROP POLICY IF EXISTS tenant_isolation ON reserve_ops;
DROP POLICY IF EXISTS tenant_isolation ON position_history;
DROP POLICY IF EXISTS tenant_isolation ON positions;
ALTER TABLE position_events DISABLE ROW LEVEL SECURITY;
ALTER TABLE reserve_ops DISABLE ROW LEVEL SECURITY;
ALTER TABLE position_history DISABLE ROW LEVEL SECURITY;
ALTER TABLE positions DISABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE positions, position_history, reserve_ops, position_events FROM settla_app;
REVOKE USAGE ON ALL SEQUENCES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON SCHEMA public FROM settla_app;
