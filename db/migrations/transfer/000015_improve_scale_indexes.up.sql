-- ============================================================================
-- Migration 000015: Improve scale indexes for better query performance
-- All indexes use to avoid table locks during creation.
-- ============================================================================

-- 1. Improve outbox relay poll index to include retry_count for index-only scans
-- The relay polls with: WHERE published = false AND retry_count < max_retries
-- ORDER BY created_at ASC. Including retry_count lets the planner use an
-- index-only scan for the retry guard instead of a heap fetch per row.
DROP INDEX IF EXISTS idx_outbox_relay_poll;
CREATE INDEX idx_outbox_relay_poll
    ON outbox(created_at ASC, retry_count)
    WHERE published = false;

-- 2. Support recent activity queries (analytics.sql GetRecentActivity)
-- Covers WHERE tenant_id = ? ORDER BY updated_at DESC queries used by the
-- analytics dashboard to show recently changed transfers.
CREATE INDEX IF NOT EXISTS idx_transfers_tenant_updated_at
    ON transfers(tenant_id, updated_at DESC);

-- 3. Improve stuck transfer detection with tenant_id for scoped queries
-- The recovery module runs every 60s to find transfers stuck in intermediate
-- states. Adding tenant_id enables tenant-scoped stuck detection without
-- filtering the full index.
DROP INDEX IF EXISTS idx_transfers_stuck_detection;
CREATE INDEX idx_transfers_stuck_detection
    ON transfers(status, updated_at, tenant_id)
    WHERE status NOT IN ('COMPLETED', 'FAILED', 'REFUNDED');

-- 4. Drop redundant index (covered by uk_provider_txns_provider_external_id)
-- The unique constraint already covers external_id lookups; the standalone
-- index is redundant and wastes write I/O at 50M inserts/day.
DROP INDEX IF EXISTS idx_provider_txns_external_id;
