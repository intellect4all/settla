#!/bin/bash
# Docker entrypoint init script: creates the settla_app role used by RLS.
# Mounted as /docker-entrypoint-initdb.d/init-rls-role.sh on all 3 Postgres containers.
# Only runs on first container initialization (empty data dir).
#
# The password is read from the SETTLA_APP_DB_PASSWORD environment variable
# (default: settla_app). CHANGE IN PRODUCTION.

set -e

APP_PASSWORD="${SETTLA_APP_DB_PASSWORD:-settla_app}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
  -- 1. Create settla_app role if it does not already exist
  DO \$\$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'settla_app') THEN
      CREATE ROLE settla_app LOGIN PASSWORD '${APP_PASSWORD}';
    END IF;
  END \$\$;

  -- 2. Grant schema usage so settla_app can see objects in public
  GRANT USAGE ON SCHEMA public TO settla_app;

  -- 3. Grant DML on all existing tables and sequence usage
  GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO settla_app;
  GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO settla_app;

  -- 4. Ensure future tables/sequences created by the owner also get grants
  ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO settla_app;
  ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE ON SEQUENCES TO settla_app;

  -- 5. Enable RLS on tenant-scoped tables (idempotent — safe to re-run).
  --    The actual RLS policies are created by the migrations; this just ensures
  --    the flag is on even if migrations haven't run yet or new tables appear.
  DO \$\$ DECLARE
    tbl TEXT;
  BEGIN
    FOR tbl IN
      SELECT tablename FROM pg_tables
      WHERE schemaname = 'public'
        AND tablename IN (
          'transfers', 'transfer_events', 'outbox', 'quotes',
          'provider_transactions', 'api_keys', 'webhook_deliveries',
          'manual_reviews', 'compensation_records', 'net_settlements',
          'webhook_event_subscriptions', 'portal_users',
          'crypto_deposits', 'crypto_addresses',
          'bank_deposit_sessions', 'bank_deposit_transactions',
          'virtual_accounts', 'payment_links'
        )
    LOOP
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    END LOOP;
  END \$\$;
EOSQL
