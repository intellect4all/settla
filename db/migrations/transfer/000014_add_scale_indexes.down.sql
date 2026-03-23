-- ============================================================================
-- Rollback migration 000014: Drop scale-critical indexes
-- ============================================================================

DROP INDEX IF EXISTS idx_transfers_stuck_detection;
DROP INDEX IF EXISTS idx_transfers_settlement_period;
DROP INDEX IF EXISTS idx_outbox_relay_poll;
DROP INDEX IF EXISTS idx_provider_txns_external_id;
