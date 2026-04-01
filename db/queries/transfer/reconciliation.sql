-- ============================================================================
-- Reconciliation queries — used by the automated reconciliation system.
-- These are admin-only system queries that scan across all tenants.
-- ============================================================================

-- name: CountTransfersInStatus :one
-- Count transfers stuck in a given status older than a threshold.
SELECT COUNT(*)::int AS count
FROM transfers
WHERE status = $1
  AND updated_at < $2;

-- name: CountUnpublishedOlderThan :one
-- Count unpublished outbox entries older than a threshold.
SELECT COUNT(*)::int AS count
FROM outbox
WHERE published = false
  AND created_at < $1;

-- name: CountDefaultPartitionRows :one
-- Count rows in the outbox_default partition (should always be zero).
SELECT COUNT(*)::int AS count
FROM outbox_default;

-- name: CountPendingProviderTxOlderThan :one
-- Count provider transactions stuck in pending status older than a threshold.
SELECT COUNT(*)::int AS count
FROM provider_transactions
WHERE status = 'pending'
  AND created_at < $1;

-- name: GetDailyTransferCount :one
-- Count transfers created on a given UTC date [start, end).
SELECT COUNT(*)::int AS count
FROM transfers
WHERE created_at >= @start_time
  AND created_at < @end_time;

-- name: GetAverageDailyTransferCount :one
-- Average number of transfers per day over [start, end).
SELECT COALESCE(
    CAST(COUNT(*) AS float) / GREATEST(
        EXTRACT(EPOCH FROM (CAST(@end_date AS timestamptz) - CAST(@start_date AS timestamptz))) / 86400,
        1
    ),
    0
)::float AS avg_count
FROM transfers
WHERE created_at >= @start_date
  AND created_at < @end_date;

-- name: GetTenantSlug :one
-- Get the slug for a tenant by ID.
SELECT slug FROM tenants WHERE id = $1;

-- name: GetLatestNetSettlement :one
-- Get the most recently created net settlement across all tenants.
SELECT id, tenant_id, period_start, period_end, total_fees_usd
FROM net_settlements
ORDER BY created_at DESC
LIMIT 1;

-- name: SumCompletedTransferFeesUSD :one
-- Sum fees from completed transfers for a tenant in [start, end).
SELECT COALESCE(SUM((fees->>'TotalFeeUSD')::numeric), 0)::NUMERIC(28,8) AS total_fees
FROM transfers
WHERE tenant_id = @tenant_id
  AND status = 'COMPLETED'
  AND completed_at >= @start_time
  AND completed_at < @end_time;

-- name: CountStuckDepositSessions :one
-- Count deposit sessions in non-terminal status older than a threshold.
SELECT COUNT(*)::int AS count
FROM crypto_deposit_sessions
WHERE status NOT IN ('SETTLED', 'HELD', 'EXPIRED', 'FAILED', 'CANCELLED')
  AND updated_at < $1;

-- name: CountStaleBlockCheckpoints :one
-- Count chain monitors whose checkpoint has not been updated since threshold.
SELECT COUNT(*)::int AS count
FROM block_checkpoints
WHERE updated_at < $1;

-- name: CountAvailablePoolAddressesAll :one
-- Count undispensed addresses across all tenants and chains.
SELECT COUNT(*)::int AS count
FROM crypto_address_pool
WHERE dispensed = false;

-- name: CountDepositTxAmountMismatches :one
-- Count sessions where received_amount doesn't match sum of confirmed tx amounts.
SELECT COUNT(*)::int AS count
FROM crypto_deposit_sessions s
WHERE s.status IN ('CONFIRMED', 'CREDITED', 'SETTLING', 'SETTLED', 'HELD')
  AND s.received_amount != COALESCE(
    (SELECT SUM(t.amount) FROM crypto_deposit_transactions t
     WHERE t.session_id = s.id AND t.confirmed = true),
    0
  );

-- name: CountStuckBankDepositSessions :one
-- Count bank deposit sessions in PENDING_PAYMENT older than threshold.
SELECT COUNT(*)::int AS count
FROM bank_deposit_sessions
WHERE status = 'PENDING_PAYMENT'
  AND updated_at < $1;

-- name: CountStuckBankDepositCrediting :one
-- Count bank deposit sessions in CREDITING status older than threshold.
SELECT COUNT(*)::int AS count
FROM bank_deposit_sessions
WHERE status = 'CREDITING'
  AND updated_at < $1;

-- name: CountOrphanedVirtualAccounts :one
-- Count virtual accounts marked unavailable whose session reached terminal state.
SELECT COUNT(*)::int AS count
FROM virtual_account_pool p
WHERE p.available = false
  AND EXISTS (
    SELECT 1 FROM bank_deposit_sessions s
    WHERE s.account_number = p.account_number
      AND s.status IN ('EXPIRED', 'FAILED', 'CANCELLED', 'SETTLED', 'HELD')
  );

-- name: HasActiveReview :one
-- Check if an open review exists for a given transfer.
SELECT EXISTS (
    SELECT 1 FROM manual_reviews
    WHERE transfer_id = $1
      AND status IN ('pending', 'investigating')
) AS has_review;

-- name: ResolveManualReview :exec
-- Resolve a manual review by updating its status and recording the resolution.
UPDATE manual_reviews
SET status = $2, resolution = $3, resolved_by = $4, resolved_at = now()
WHERE id = $1;

-- name: UpdateTenantMetadata :exec
-- Update the metadata JSONB field for a tenant.
UPDATE tenants
SET metadata = $2, updated_at = now()
WHERE id = $1;

-- name: MarkSettlementPaid :exec
-- Mark a net settlement as paid with a payment reference.
UPDATE net_settlements
SET status = 'paid',
    settled_at = now(),
    instructions = jsonb_set(
        COALESCE(instructions, '[]'::jsonb),
        '{payment_ref}',
        to_jsonb(@payment_ref::text)
    )
WHERE id = @id;

-- name: ListOpsManualReviews :many
-- List manual reviews for ops dashboard with tenant and transfer details.
-- Use sqlc.narg for optional filters: pass NULL to skip filtering.
SELECT
    mr.id,
    mr.transfer_id,
    mr.tenant_id,
    COALESCE(t.name, '') AS tenant_name,
    mr.status,
    mr.transfer_status,
    mr.attempted_recoveries,
    COALESCE(tr.source_amount, 0) AS source_amount,
    COALESCE(tr.source_currency, '') AS source_currency,
    COALESCE(tr.dest_currency, '') AS dest_currency,
    COALESCE(tr.failure_code, '') AS failure_code,
    mr.created_at AS escalated_at,
    mr.resolved_at,
    COALESCE(mr.resolved_by, '') AS resolved_by,
    COALESCE(mr.resolution, '') AS resolution
FROM manual_reviews mr
LEFT JOIN tenants t ON t.id = mr.tenant_id
LEFT JOIN transfers tr ON tr.id = mr.transfer_id AND tr.tenant_id = mr.tenant_id
WHERE (sqlc.narg('filter_tenant_id')::uuid IS NULL OR mr.tenant_id = sqlc.narg('filter_tenant_id'))
  AND (sqlc.narg('filter_status')::text IS NULL OR mr.status = sqlc.narg('filter_status'))
ORDER BY mr.created_at DESC
LIMIT 100;

-- name: ListOpsNetSettlements :many
-- List net settlements for ops dashboard with tenant names.
-- Use sqlc.narg for optional tenant filter: pass NULL for all tenants.
SELECT
    ns.id,
    ns.tenant_id,
    COALESCE(t.name, '') AS tenant_name,
    ns.period_start,
    ns.period_end,
    ns.corridors,
    ns.net_by_currency,
    ns.total_fees_usd,
    ns.instructions,
    ns.status,
    ns.due_date,
    ns.settled_at
FROM net_settlements ns
LEFT JOIN tenants t ON t.id = ns.tenant_id
WHERE (sqlc.narg('filter_tenant_id')::uuid IS NULL OR ns.tenant_id = sqlc.narg('filter_tenant_id'))
ORDER BY ns.created_at DESC
LIMIT 50;
