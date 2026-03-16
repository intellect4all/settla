-- ============================================================================
-- Consolidated migration: RLS + autovacuum performance tuning
-- Merges original migrations 000013 (RLS) and 000015 (autovacuum)
-- ============================================================================

-- ============================================================================
-- RLS: Row-Level Security for tenant isolation on Transfer DB.
-- Two-role model: settla (owner, BYPASSRLS) for admin/cross-tenant ops,
-- settla_app (subject to RLS) for tenant-scoped API operations.
-- ============================================================================

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
    EXECUTE format('CREATE ROLE settla_app LOGIN PASSWORD %L',
      coalesce(current_setting('app.settla_app_password', true), 'settla_app_dev'));
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
  transfers,
  transfer_events,
  outbox,
  quotes,
  provider_transactions,
  api_keys,
  webhook_deliveries,
  manual_reviews,
  compensation_records,
  net_settlements,
  webhook_event_subscriptions
TO settla_app;

GRANT SELECT ON TABLE tenants TO settla_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;

ALTER TABLE transfers ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE quotes ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE manual_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE compensation_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE net_settlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_event_subscriptions ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON transfers
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON transfer_events
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON outbox
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON quotes
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON provider_transactions
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON api_keys
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON webhook_deliveries
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON manual_reviews
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON compensation_records
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON net_settlements
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE POLICY tenant_isolation ON webhook_event_subscriptions
  USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ============================================================================
-- Autovacuum: aggressive settings for high-volume partitions.
-- At 50M transactions/day the default thresholds cause severe bloat.
-- ============================================================================

DO $$
DECLARE
  r RECORD;
BEGIN
  FOR r IN
    SELECT c.relname AS child_name
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname IN ('transfers', 'transfer_events', 'outbox', 'provider_transactions')
  LOOP
    EXECUTE format(
      'ALTER TABLE %I SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_analyze_scale_factor = 0.005)',
      r.child_name
    );
  END LOOP;
END;
$$;
