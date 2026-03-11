-- ============================================================================
-- Analytics queries — enhanced metrics for the tenant portal analytics page.
-- All queries are tenant-scoped per the tenant isolation invariant.
-- ============================================================================

-- name: GetTransferStatusDistribution :many
-- Count of transfers by current status for a given period.
SELECT
    status,
    COUNT(*) AS count
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz
GROUP BY status
ORDER BY count DESC;

-- name: GetCorridorMetrics :many
-- Per-corridor volume, count, success rate, and average latency.
SELECT
    source_currency,
    dest_currency,
    COUNT(*) AS transfer_count,
    COALESCE(SUM(source_amount) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS fees_usd,
    COUNT(*) FILTER (WHERE status = 'COMPLETED') AS completed,
    COUNT(*) FILTER (WHERE status = 'FAILED') AS failed,
    CASE
        WHEN COUNT(*) FILTER (WHERE status IN ('COMPLETED','FAILED')) = 0 THEN 0
        ELSE ROUND(
            100.0 * COUNT(*) FILTER (WHERE status = 'COMPLETED')
            / COUNT(*) FILTER (WHERE status IN ('COMPLETED','FAILED')),
            2
        )
    END::NUMERIC(5,2) AS success_rate,
    COALESCE(
        AVG(EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000)
        FILTER (WHERE status = 'COMPLETED' AND completed_at IS NOT NULL),
        0
    )::INTEGER AS avg_latency_ms
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz
GROUP BY source_currency, dest_currency
ORDER BY volume_usd DESC;

-- name: GetTransferLatencyPercentiles :one
-- Transfer completion latency percentiles (p50, p90, p95, p99) for completed transfers.
SELECT
    COUNT(*) FILTER (WHERE status = 'COMPLETED')::bigint AS sample_count,
    COALESCE(PERCENTILE_CONT(0.50) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000
    ) FILTER (WHERE status = 'COMPLETED' AND completed_at IS NOT NULL), 0)::INTEGER AS p50_ms,
    COALESCE(PERCENTILE_CONT(0.90) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000
    ) FILTER (WHERE status = 'COMPLETED' AND completed_at IS NOT NULL), 0)::INTEGER AS p90_ms,
    COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000
    ) FILTER (WHERE status = 'COMPLETED' AND completed_at IS NOT NULL), 0)::INTEGER AS p95_ms,
    COALESCE(PERCENTILE_CONT(0.99) WITHIN GROUP (
        ORDER BY EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000
    ) FILTER (WHERE status = 'COMPLETED' AND completed_at IS NOT NULL), 0)::INTEGER AS p99_ms
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz;

-- name: GetVolumeComparison :one
-- Compare current period volume/count vs. previous period (for week-over-week or month-over-month).
SELECT
    -- Current period
    COUNT(*) FILTER (WHERE created_at >= @current_start::timestamptz) AS current_count,
    COALESCE(SUM(source_amount) FILTER (WHERE created_at >= @current_start::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS current_volume_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE created_at >= @current_start::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS current_fees_usd,
    -- Previous period
    COUNT(*) FILTER (WHERE created_at < @current_start::timestamptz) AS previous_count,
    COALESCE(SUM(source_amount) FILTER (WHERE created_at < @current_start::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS previous_volume_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE created_at < @current_start::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS previous_fees_usd
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @previous_start::timestamptz
  AND created_at < @current_end::timestamptz;

-- name: GetRecentActivity :many
-- Recent transfer activity feed (latest state changes).
SELECT
    t.id AS transfer_id,
    t.external_ref,
    t.status,
    t.source_currency,
    t.source_amount,
    t.dest_currency,
    t.dest_amount,
    t.updated_at,
    t.failure_reason
FROM transfers t
WHERE t.tenant_id = @tenant_id
ORDER BY t.updated_at DESC
LIMIT @page_size;
