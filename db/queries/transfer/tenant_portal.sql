-- ============================================================================
-- Tenant portal queries — self-service dashboard metrics and analytics.
-- All queries are tenant-scoped (tenant_id filter) per the tenant isolation invariant.
-- ============================================================================

-- name: GetTenantDashboardMetrics :one
-- Aggregated metrics for the last N days. Runs one scan for multiple aggregation windows.
SELECT
    -- Today
    COUNT(*) FILTER (WHERE created_at >= @today_start::timestamptz) AS transfers_today,
    COALESCE(SUM(source_amount) FILTER (WHERE created_at >= @today_start::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_today_usd,
    COUNT(*) FILTER (WHERE created_at >= @today_start::timestamptz AND status = 'COMPLETED') AS completed_today,
    COUNT(*) FILTER (WHERE created_at >= @today_start::timestamptz AND status = 'FAILED') AS failed_today,
    -- 7 days
    COUNT(*) FILTER (WHERE created_at >= @seven_days_ago::timestamptz) AS transfers_7d,
    COALESCE(SUM(source_amount) FILTER (WHERE created_at >= @seven_days_ago::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_7d_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE created_at >= @seven_days_ago::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS fees_7d_usd,
    -- 30 days
    COUNT(*) FILTER (WHERE created_at >= @thirty_days_ago::timestamptz) AS transfers_30d,
    COALESCE(SUM(source_amount) FILTER (WHERE created_at >= @thirty_days_ago::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_30d_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE created_at >= @thirty_days_ago::timestamptz AND status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS fees_30d_usd,
    -- Success rate (30d)
    CASE
        WHEN COUNT(*) FILTER (WHERE created_at >= @thirty_days_ago::timestamptz AND status IN ('COMPLETED','FAILED')) = 0 THEN 0
        ELSE ROUND(
            100.0 * COUNT(*) FILTER (WHERE created_at >= @thirty_days_ago::timestamptz AND status = 'COMPLETED')
            / COUNT(*) FILTER (WHERE created_at >= @thirty_days_ago::timestamptz AND status IN ('COMPLETED','FAILED')),
            2
        )
    END::NUMERIC(5,2) AS success_rate_30d
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @thirty_days_ago::timestamptz;

-- name: GetTransferStatsHourly :many
-- Hourly bucketed transfer stats for a given date range (for charts).
SELECT
    date_trunc('hour', created_at)::timestamptz AS bucket,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'COMPLETED') AS completed,
    COUNT(*) FILTER (WHERE status = 'FAILED') AS failed,
    COALESCE(SUM(source_amount) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS fees_usd
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz
GROUP BY bucket
ORDER BY bucket;

-- name: GetTransferStatsDaily :many
-- Daily bucketed transfer stats for a given date range (for charts).
SELECT
    date_trunc('day', created_at)::timestamptz AS bucket,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'COMPLETED') AS completed,
    COUNT(*) FILTER (WHERE status = 'FAILED') AS failed,
    COALESCE(SUM(source_amount) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS fees_usd
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz
GROUP BY bucket
ORDER BY bucket;

-- name: GetFeeReportByCorridor :many
-- Fee breakdown grouped by currency corridor for a given period.
SELECT
    source_currency,
    dest_currency,
    COUNT(*) AS transfer_count,
    COALESCE(SUM(source_amount), 0)::NUMERIC(28,8) AS total_volume_usd,
    COALESCE(SUM((fees->>'on_ramp_fee')::NUMERIC(28,8)), 0)::NUMERIC(28,8) AS on_ramp_fees_usd,
    COALESCE(SUM((fees->>'off_ramp_fee')::NUMERIC(28,8)), 0)::NUMERIC(28,8) AS off_ramp_fees_usd,
    COALESCE(SUM((fees->>'network_fee')::NUMERIC(28,8)), 0)::NUMERIC(28,8) AS network_fees_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)), 0)::NUMERIC(28,8) AS total_fees_usd
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz
  AND status NOT IN ('FAILED', 'REFUNDED')
GROUP BY source_currency, dest_currency
ORDER BY total_fees_usd DESC;

-- name: GetAPIKeyByIDAndTenant :one
-- Get a specific API key ensuring tenant ownership.
SELECT id, tenant_id, key_prefix, environment, name, is_active, last_used_at, expires_at, created_at
FROM api_keys
WHERE id = @key_id AND tenant_id = @tenant_id;

-- name: DeactivateAPIKeyByTenant :exec
-- Deactivate an API key, ensuring it belongs to the tenant.
UPDATE api_keys SET is_active = false
WHERE id = @key_id AND tenant_id = @tenant_id;
