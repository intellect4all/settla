-- 000017: Add RLS policies for crypto deposit, bank deposit, and portal user tables.
-- These tables were added in 000009-000011 but lacked RLS policies,
-- creating a potential cross-tenant data leakage vector when using the appPool.

-- Enable RLS on deposit tables
ALTER TABLE crypto_deposit_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE crypto_deposit_addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_deposit_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_deposit_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_users ENABLE ROW LEVEL SECURITY;

-- Crypto deposit sessions: tenant isolation
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE polname = 'crypto_deposit_sessions_tenant_isolation') THEN
    CREATE POLICY crypto_deposit_sessions_tenant_isolation ON crypto_deposit_sessions
        FOR ALL
        TO settla_app
        USING (tenant_id = current_setting('app.current_tenant_id')::uuid)
        WITH CHECK (tenant_id = current_setting('app.current_tenant_id')::uuid);
END IF;
END $$;

-- Crypto deposit addresses: tenant isolation
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE polname = 'crypto_deposit_addresses_tenant_isolation') THEN
    CREATE POLICY crypto_deposit_addresses_tenant_isolation ON crypto_deposit_addresses
        FOR ALL
        TO settla_app
        USING (tenant_id = current_setting('app.current_tenant_id')::uuid)
        WITH CHECK (tenant_id = current_setting('app.current_tenant_id')::uuid);
END IF;
END $$;

-- Bank deposit sessions: tenant isolation
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE polname = 'bank_deposit_sessions_tenant_isolation') THEN
    CREATE POLICY bank_deposit_sessions_tenant_isolation ON bank_deposit_sessions
        FOR ALL
        TO settla_app
        USING (tenant_id = current_setting('app.current_tenant_id')::uuid)
        WITH CHECK (tenant_id = current_setting('app.current_tenant_id')::uuid);
END IF;
END $$;

-- Bank deposit transactions: tenant isolation (via session join)
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE polname = 'bank_deposit_transactions_tenant_isolation') THEN
    CREATE POLICY bank_deposit_transactions_tenant_isolation ON bank_deposit_transactions
        FOR ALL
        TO settla_app
        USING (tenant_id = current_setting('app.current_tenant_id')::uuid)
        WITH CHECK (tenant_id = current_setting('app.current_tenant_id')::uuid);
END IF;
END $$;

-- Portal users: tenant isolation
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE polname = 'portal_users_tenant_isolation') THEN
    CREATE POLICY portal_users_tenant_isolation ON portal_users
        FOR ALL
        TO settla_app
        USING (tenant_id = current_setting('app.current_tenant_id')::uuid)
        WITH CHECK (tenant_id = current_setting('app.current_tenant_id')::uuid);
END IF;
END $$;

-- Grant necessary permissions
GRANT SELECT, INSERT, UPDATE, DELETE ON crypto_deposit_sessions TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON crypto_deposit_addresses TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON bank_deposit_sessions TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON bank_deposit_transactions TO settla_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON portal_users TO settla_app;
