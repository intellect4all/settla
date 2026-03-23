-- Add per-tenant limit on pending (non-terminal) transfers.
-- 0 means unlimited (default, backward-compatible).
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS max_pending_transfers INTEGER NOT NULL DEFAULT 0;
