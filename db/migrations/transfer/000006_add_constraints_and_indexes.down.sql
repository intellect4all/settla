-- Reverse constraints and indexes.

DROP INDEX IF EXISTS idx_provider_txns_transfer_type;
DROP INDEX CONCURRENTLY IF EXISTS idx_transfers_analytics;
DROP INDEX IF EXISTS uk_provider_txns_transfer_type;
DROP INDEX CONCURRENTLY IF EXISTS idx_outbox_relay_covering;

ALTER TABLE net_settlements DROP CONSTRAINT IF EXISTS fk_net_settlements_tenant;
ALTER TABLE compensation_records DROP CONSTRAINT IF EXISTS fk_compensation_tenant;
ALTER TABLE manual_reviews DROP CONSTRAINT IF EXISTS fk_manual_reviews_tenant;
ALTER TABLE provider_transactions DROP CONSTRAINT IF EXISTS fk_provider_txns_tenant;

ALTER TABLE compensation_records DROP CONSTRAINT IF EXISTS uk_compensation_records_transfer_id;
ALTER TABLE manual_reviews DROP CONSTRAINT IF EXISTS uk_manual_reviews_transfer_id;
