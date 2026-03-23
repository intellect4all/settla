-- ============================================================================
-- Migration 000014: Scale-critical indexes for 50M transactions/day
-- Note: CONCURRENTLY removed because golang-migrate runs migrations in transactions.
-- ============================================================================

-- 1. Provider transaction external_id lookup (webhook idempotency checks)
-- Workers use CHECK-BEFORE-CALL pattern: look up by external_id to see if a
-- provider callback has already been processed. The existing unique index
-- uk_provider_txns_provider_external_id covers (provider, external_id) but
-- queries that only filter on external_id cannot use a leading-provider index.
CREATE INDEX IF NOT EXISTS idx_provider_txns_external_id
    ON provider_transactions(external_id)
    WHERE external_id IS NOT NULL;

-- 2. Outbox relay covering index with retry_count
-- The relay polls with: WHERE published = false AND retry_count < max_retries
-- ORDER BY created_at ASC. The existing idx_outbox_unpublished and
-- idx_outbox_relay_covering lack retry_count, forcing a filter step on every
-- poll cycle (every 20ms, batch 500). Including retry_count lets the planner
-- use an index-only scan for the retry guard.
CREATE INDEX IF NOT EXISTS idx_outbox_relay_poll
    ON outbox(created_at ASC)
    WHERE published = false;

-- 3. Settlement period query index (completed transfers by date range)
-- The net settlement calculator queries completed transfers within a period
-- grouped by tenant. Without this, it seq-scans the full transfers partition.
CREATE INDEX IF NOT EXISTS idx_transfers_settlement_period
    ON transfers(tenant_id, completed_at DESC)
    WHERE status = 'COMPLETED';

-- 4. Stuck transfer detection index (recovery detector)
-- The recovery module runs every 60s to find transfers stuck in intermediate
-- states for >60s. This partial index covers only non-terminal states so the
-- scan is narrow even at 50M rows/day.
CREATE INDEX IF NOT EXISTS idx_transfers_stuck_detection
    ON transfers(status, updated_at)
    WHERE status NOT IN ('COMPLETED', 'FAILED', 'REFUNDED');
