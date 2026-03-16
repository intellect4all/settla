DROP INDEX CONCURRENTLY IF EXISTS idx_journal_entries_idempotency;
DROP INDEX CONCURRENTLY IF EXISTS idx_transfers_external_ref;
DROP INDEX CONCURRENTLY IF EXISTS uk_provider_txns_provider_external_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_transfers_idempotency_key;
