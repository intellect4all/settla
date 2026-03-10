-- Reverse: restore original idempotency index, remove RLS.

DROP POLICY IF EXISTS tenant_isolation ON accounts;
DROP POLICY IF EXISTS tenant_isolation ON journal_entries;

ALTER TABLE accounts DISABLE ROW LEVEL SECURITY;
ALTER TABLE journal_entries DISABLE ROW LEVEL SECURITY;

REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLE accounts, journal_entries FROM settla_app;
REVOKE SELECT ON TABLE entry_lines, balance_snapshots FROM settla_app;
REVOKE USAGE ON ALL SEQUENCES IN SCHEMA public FROM settla_app;
REVOKE USAGE ON SCHEMA public FROM settla_app;

DROP INDEX IF EXISTS idx_journal_entries_idempotency_tenant;
DROP INDEX IF EXISTS idx_journal_entries_idempotency_system;

CREATE UNIQUE INDEX idx_journal_entries_idempotency
    ON journal_entries(idempotency_key, posted_at)
    WHERE idempotency_key IS NOT NULL;
