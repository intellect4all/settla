-- Reverse RLS and autovacuum settings.

-- Reset autovacuum
ALTER TABLE transfers RESET (autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);
ALTER TABLE transfer_events RESET (autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);
ALTER TABLE outbox RESET (autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);
ALTER TABLE provider_transactions RESET (autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);

-- Drop RLS policies
DROP POLICY IF EXISTS tenant_isolation ON transfers;
DROP POLICY IF EXISTS tenant_isolation ON transfer_events;
DROP POLICY IF EXISTS tenant_isolation ON outbox;
DROP POLICY IF EXISTS tenant_isolation ON quotes;
DROP POLICY IF EXISTS tenant_isolation ON provider_transactions;
DROP POLICY IF EXISTS tenant_isolation ON api_keys;
DROP POLICY IF EXISTS tenant_isolation ON webhook_deliveries;
DROP POLICY IF EXISTS tenant_isolation ON manual_reviews;
DROP POLICY IF EXISTS tenant_isolation ON compensation_records;
DROP POLICY IF EXISTS tenant_isolation ON net_settlements;
DROP POLICY IF EXISTS tenant_isolation ON webhook_event_subscriptions;

ALTER TABLE transfers DISABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_events DISABLE ROW LEVEL SECURITY;
ALTER TABLE outbox DISABLE ROW LEVEL SECURITY;
ALTER TABLE quotes DISABLE ROW LEVEL SECURITY;
ALTER TABLE provider_transactions DISABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys DISABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries DISABLE ROW LEVEL SECURITY;
ALTER TABLE manual_reviews DISABLE ROW LEVEL SECURITY;
ALTER TABLE compensation_records DISABLE ROW LEVEL SECURITY;
ALTER TABLE net_settlements DISABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_event_subscriptions DISABLE ROW LEVEL SECURITY;

REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLE
  transfers, transfer_events, outbox, quotes, provider_transactions,
  api_keys, webhook_deliveries, manual_reviews, compensation_records,
  net_settlements, webhook_event_subscriptions
FROM settla_app;
REVOKE SELECT ON TABLE tenants FROM settla_app;
REVOKE USAGE ON ALL SEQUENCES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON SCHEMA public FROM settla_app;
