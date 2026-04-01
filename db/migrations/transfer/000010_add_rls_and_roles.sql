-- +goose Up


DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
    EXECUTE format('CREATE ROLE settla_app LOGIN PASSWORD %L',
      coalesce(current_setting('app.settla_app_password', true), 'settla_app_dev'));
  END IF;
END $$;


GRANT USAGE ON SCHEMA public TO settla_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;


-- Tenants: read-only for app role (admin operations bypass RLS)
GRANT SELECT ON TABLE tenants TO settla_app;

-- Full CRUD for all other tenant-scoped tables
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
    transfers,
    transfer_events,
    quotes,
    provider_transactions,
    api_keys,
    portal_users,
    outbox,
    manual_reviews,
    compensation_records,
    net_settlements,
    reconciliation_reports,
    webhook_deliveries,
    webhook_event_subscriptions,
    tokens,
    block_checkpoints,
    crypto_address_pool,
    crypto_deposit_address_index,
    crypto_derivation_counters,
    crypto_deposit_sessions,
    crypto_deposit_transactions,
    banking_partners,
    virtual_account_pool,
    virtual_account_index,
    bank_deposit_sessions,
    bank_deposit_transactions,
    payment_links,
    analytics_daily_snapshots,
    analytics_export_jobs,
    audit_log,
    provider_webhook_logs,
    position_transactions
TO settla_app;

-- Enable RLS on all tenant-scoped tables. Tables without tenant_id
-- (reconciliation_reports, banking_partners, block_checkpoints, tokens,
-- crypto_derivation_counters) are excluded.

ALTER TABLE transfers ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON transfers FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE transfer_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON transfer_events FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE quotes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quotes FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE provider_transactions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON provider_transactions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_keys FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE portal_users ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON portal_users FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON outbox FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE manual_reviews ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON manual_reviews FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE compensation_records ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON compensation_records FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE net_settlements ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON net_settlements FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_deliveries FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE webhook_event_subscriptions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_event_subscriptions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE crypto_deposit_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON crypto_deposit_sessions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE crypto_deposit_transactions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON crypto_deposit_transactions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE crypto_deposit_address_index ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON crypto_deposit_address_index FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE crypto_address_pool ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON crypto_address_pool FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE virtual_account_pool ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON virtual_account_pool FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE virtual_account_index ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON virtual_account_index FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE bank_deposit_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank_deposit_sessions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE bank_deposit_transactions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank_deposit_transactions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE payment_links ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON payment_links FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE analytics_daily_snapshots ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON analytics_daily_snapshots FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE analytics_export_jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON analytics_export_jobs FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- provider_webhook_logs: tenant_id is nullable (populated after normalization)
ALTER TABLE provider_webhook_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON provider_webhook_logs FOR ALL TO settla_app
    USING  (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id IS NULL OR tenant_id = current_setting('app.current_tenant_id', true)::uuid);

ALTER TABLE position_transactions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON position_transactions FOR ALL TO settla_app
    USING  (tenant_id = current_setting('app.current_tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Aggressive settings for high-write partitions: vacuum at 1% dead rows,
-- analyze at 0.5% changed rows.

DO $$ DECLARE r RECORD; BEGIN
  FOR r IN
    SELECT c.relname AS child_name
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname IN (
      'transfers', 'transfer_events', 'outbox', 'provider_transactions',
      'crypto_deposit_sessions', 'crypto_deposit_transactions',
      'bank_deposit_sessions', 'bank_deposit_transactions',
      'webhook_deliveries', 'provider_webhook_logs',
      'position_transactions', 'net_settlements'
    )
  LOOP
    EXECUTE format(
      'ALTER TABLE %I SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_analyze_scale_factor = 0.005)',
      r.child_name
    );
  END LOOP;
END; $$;

-- +goose Down

-- Drop all RLS policies
DO $$ DECLARE r RECORD; BEGIN
  FOR r IN
    SELECT schemaname, tablename, policyname
    FROM pg_policies
    WHERE policyname = 'tenant_isolation'
      AND schemaname = 'public'
  LOOP
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I.%I', r.policyname, r.schemaname, r.tablename);
  END LOOP;
END; $$;

-- Disable RLS on all tables
DO $$ DECLARE r RECORD; BEGIN
  FOR r IN
    SELECT tablename FROM pg_tables
    WHERE schemaname = 'public'
      AND tablename IN (
        'transfers', 'transfer_events', 'quotes', 'provider_transactions',
        'api_keys', 'portal_users', 'outbox', 'manual_reviews',
        'compensation_records', 'net_settlements', 'webhook_deliveries',
        'webhook_event_subscriptions', 'crypto_deposit_sessions',
        'crypto_deposit_transactions', 'crypto_deposit_address_index',
        'crypto_address_pool', 'virtual_account_pool', 'virtual_account_index',
        'bank_deposit_sessions', 'bank_deposit_transactions', 'payment_links',
        'analytics_daily_snapshots', 'analytics_export_jobs', 'audit_log',
        'provider_webhook_logs', 'position_transactions'
      )
  LOOP
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', r.tablename);
  END LOOP;
END; $$;

-- Revoke grants
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON ALL SEQUENCES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON SCHEMA public FROM settla_app;
