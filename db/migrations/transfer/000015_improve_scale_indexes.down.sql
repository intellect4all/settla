-- ============================================================================
-- Rollback migration 000015: Revert index improvements to previous state
-- ============================================================================

-- Restore the original idx_provider_txns_external_id (dropped in up migration)
CREATE INDEX IF NOT EXISTS idx_provider_txns_external_id
    ON provider_transactions(external_id)
    WHERE external_id IS NOT NULL;

-- Drop the tenant-scoped stuck detection index and restore the original
DROP INDEX IF EXISTS idx_transfers_stuck_detection;
CREATE INDEX idx_transfers_stuck_detection
    ON transfers(status, updated_at)
    WHERE status NOT IN ('COMPLETED', 'FAILED', 'REFUNDED');

-- Drop the analytics recent activity index (did not exist before)
DROP INDEX IF EXISTS idx_transfers_tenant_updated_at;

-- Revert outbox relay poll index to original (without retry_count)
DROP INDEX IF EXISTS idx_outbox_relay_poll;
CREATE INDEX idx_outbox_relay_poll
    ON outbox(created_at ASC)
    WHERE published = false;
