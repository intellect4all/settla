-- Reverse bank deposit tables migration

-- Drop RLS policies
DO $$ BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
        DROP POLICY IF EXISTS tenant_isolation ON bank_deposit_sessions;
        DROP POLICY IF EXISTS tenant_isolation ON bank_deposit_transactions;
        DROP POLICY IF EXISTS tenant_isolation ON virtual_account_pool;
        DROP POLICY IF EXISTS tenant_isolation ON virtual_account_index;
    END IF;
END $$;

-- Drop partitioned tables (cascades to partitions)
DROP TABLE IF EXISTS bank_deposit_transactions CASCADE;
DROP TABLE IF EXISTS bank_deposit_sessions CASCADE;
DROP TABLE IF EXISTS virtual_account_index CASCADE;
DROP TABLE IF EXISTS virtual_account_pool CASCADE;
DROP TABLE IF EXISTS banking_partners CASCADE;

-- Remove tenant bank deposit columns
ALTER TABLE tenants DROP COLUMN IF EXISTS bank_deposits_enabled;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_banking_partner;
ALTER TABLE tenants DROP COLUMN IF EXISTS bank_supported_currencies;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_mismatch_policy;
ALTER TABLE tenants DROP COLUMN IF EXISTS bank_default_session_ttl_secs;

-- Drop enums
DROP TYPE IF EXISTS payment_mismatch_policy_enum;
DROP TYPE IF EXISTS virtual_account_type_enum;
DROP TYPE IF EXISTS bank_deposit_session_status_enum;
