-- Rollback: remove RLS policies added in 000017
DROP POLICY IF EXISTS crypto_deposit_sessions_tenant_isolation ON crypto_deposit_sessions;
DROP POLICY IF EXISTS crypto_deposit_addresses_tenant_isolation ON crypto_deposit_addresses;
DROP POLICY IF EXISTS bank_deposit_sessions_tenant_isolation ON bank_deposit_sessions;
DROP POLICY IF EXISTS bank_deposit_transactions_tenant_isolation ON bank_deposit_transactions;
DROP POLICY IF EXISTS portal_users_tenant_isolation ON portal_users;

ALTER TABLE crypto_deposit_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE crypto_deposit_addresses DISABLE ROW LEVEL SECURITY;
ALTER TABLE bank_deposit_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE bank_deposit_transactions DISABLE ROW LEVEL SECURITY;
ALTER TABLE portal_users DISABLE ROW LEVEL SECURITY;
