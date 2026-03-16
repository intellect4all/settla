-- Reverse crypto deposit tables migration

-- Drop RLS policies
DO $$ BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
        DROP POLICY IF EXISTS tenant_isolation ON crypto_deposit_sessions;
        DROP POLICY IF EXISTS tenant_isolation ON crypto_deposit_transactions;
        DROP POLICY IF EXISTS tenant_isolation ON crypto_deposit_address_index;
        DROP POLICY IF EXISTS tenant_isolation ON crypto_address_pool;
    END IF;
END $$;

-- Drop partitioned tables (cascades to partitions)
DROP TABLE IF EXISTS crypto_deposit_transactions CASCADE;
DROP TABLE IF EXISTS crypto_deposit_sessions CASCADE;
DROP TABLE IF EXISTS crypto_deposit_address_index CASCADE;
DROP TABLE IF EXISTS crypto_address_pool CASCADE;
DROP TABLE IF EXISTS crypto_derivation_counters CASCADE;
DROP TABLE IF EXISTS block_checkpoints CASCADE;
DROP TABLE IF EXISTS tokens CASCADE;

-- Drop enums
DROP TYPE IF EXISTS deposit_session_status_enum;
DROP TYPE IF EXISTS settlement_preference_enum;

-- Remove tenant crypto columns
ALTER TABLE tenants DROP COLUMN IF EXISTS crypto_enabled;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_settlement_pref;
ALTER TABLE tenants DROP COLUMN IF EXISTS supported_chains;
ALTER TABLE tenants DROP COLUMN IF EXISTS min_confirmations_tron;
ALTER TABLE tenants DROP COLUMN IF EXISTS min_confirmations_eth;
ALTER TABLE tenants DROP COLUMN IF EXISTS min_confirmations_base;
ALTER TABLE tenants DROP COLUMN IF EXISTS payment_tolerance_bps;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_session_ttl_secs;
