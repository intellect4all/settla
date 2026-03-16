-- ============================================================================
-- Migration 000013: Critical indexes and constraints from codebase assessment
-- ============================================================================

-- P1-8: Dedicated index for idempotency key lookup (hot path at 5K QPS)
-- Supports: GetTransferByIdempotencyKey query with tenant_id + idempotency_key + created_at
CREATE INDEX IF NOT EXISTS idx_transfers_idempotency_key
    ON transfers (tenant_id, idempotency_key, created_at DESC)
    WHERE idempotency_key IS NOT NULL;

-- P1-9: Unique constraint on provider external_id per provider to prevent double-spending
-- Only applies when external_id is populated (NULL external_ids are allowed to coexist)
CREATE UNIQUE INDEX IF NOT EXISTS uk_provider_txns_provider_external_id
    ON provider_transactions (provider, external_id)
    WHERE external_id IS NOT NULL AND external_id != '';

-- P1-12: Index to support external_ref lookup with date boundary
CREATE INDEX IF NOT EXISTS idx_transfers_external_ref
    ON transfers (tenant_id, external_ref, created_at DESC)
    WHERE external_ref IS NOT NULL AND external_ref != '';

-- NOTE: journal_entries index belongs in ledger DB, not transfer DB — removed.
