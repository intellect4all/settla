-- Reverse: drop RLS and reserve_ops table.

DROP POLICY IF EXISTS tenant_isolation ON positions;
DROP POLICY IF EXISTS tenant_isolation ON position_history;
DROP POLICY IF EXISTS tenant_isolation ON reserve_ops;

ALTER TABLE positions DISABLE ROW LEVEL SECURITY;
ALTER TABLE position_history DISABLE ROW LEVEL SECURITY;
ALTER TABLE reserve_ops DISABLE ROW LEVEL SECURITY;

REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLE positions, position_history, reserve_ops FROM settla_app;
REVOKE USAGE ON ALL SEQUENCES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON SCHEMA public FROM settla_app;

DROP TABLE IF EXISTS reserve_ops;
