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

-- ============================================================================
-- Extended analytics queries — fees, providers, deposits, reconciliation,
-- snapshots, and export jobs.
-- ============================================================================

-- name: GetFeeBreakdown :many
-- Fee revenue by corridor with breakdown by fee type.
SELECT
    source_currency,
    dest_currency,
    COUNT(*) AS transfer_count,
    COALESCE(SUM(source_amount) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS volume_usd,
    COALESCE(SUM((fees->>'on_ramp_fee')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS on_ramp_fees_usd,
    COALESCE(SUM((fees->>'off_ramp_fee')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS off_ramp_fees_usd,
    COALESCE(SUM((fees->>'network_fee')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS network_fees_usd,
    COALESCE(SUM((fees->>'total_fee_usd')::NUMERIC(28,8)) FILTER (WHERE status NOT IN ('FAILED','REFUNDED')), 0)::NUMERIC(28,8) AS total_fees_usd
FROM transfers
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz
GROUP BY source_currency, dest_currency
ORDER BY total_fees_usd DESC;

-- name: GetProviderPerformance :many
-- Provider performance: success rate, avg settlement time, volume per corridor.
SELECT
    pt.provider,
    t.source_currency,
    t.dest_currency,
    COUNT(*) AS transaction_count,
    COUNT(*) FILTER (WHERE pt.status = 'COMPLETED') AS completed,
    COUNT(*) FILTER (WHERE pt.status = 'FAILED') AS failed,
    CASE
        WHEN COUNT(*) FILTER (WHERE pt.status IN ('COMPLETED','FAILED')) = 0 THEN 0
        ELSE ROUND(
            100.0 * COUNT(*) FILTER (WHERE pt.status = 'COMPLETED')
            / COUNT(*) FILTER (WHERE pt.status IN ('COMPLETED','FAILED')),
            2
        )
    END::NUMERIC(5,2) AS success_rate,
    COALESCE(
        AVG(EXTRACT(EPOCH FROM (pt.updated_at - pt.created_at)) * 1000)
        FILTER (WHERE pt.status = 'COMPLETED'),
        0
    )::INTEGER AS avg_settlement_ms,
    COALESCE(SUM(t.source_amount) FILTER (WHERE pt.status = 'COMPLETED'), 0)::NUMERIC(28,8) AS total_volume
FROM provider_transactions pt
JOIN transfers t ON t.id = pt.transfer_id
WHERE t.tenant_id = @tenant_id
  AND pt.created_at >= @from_time::timestamptz
  AND pt.created_at < @to_time::timestamptz
GROUP BY pt.provider, t.source_currency, t.dest_currency
ORDER BY total_volume DESC;

-- name: GetCryptoDepositAnalytics :one
-- Aggregate crypto deposit metrics for a tenant and period.
SELECT
    COUNT(*) AS total_sessions,
    COUNT(*) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')) AS completed_sessions,
    COUNT(*) FILTER (WHERE status = 'EXPIRED') AS expired_sessions,
    COUNT(*) FILTER (WHERE status = 'FAILED') AS failed_sessions,
    CASE
        WHEN COUNT(*) = 0 THEN 0
        ELSE ROUND(
            100.0 * COUNT(*) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD'))
            / COUNT(*),
            2
        )
    END::NUMERIC(5,2) AS conversion_rate,
    COALESCE(SUM(received_amount) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')), 0)::NUMERIC(28,8) AS total_received,
    COALESCE(SUM(fee_amount) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')), 0)::NUMERIC(28,8) AS total_fees,
    COALESCE(SUM(net_amount) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')), 0)::NUMERIC(28,8) AS total_net
FROM crypto_deposit_sessions
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz;

-- name: GetBankDepositAnalytics :one
-- Aggregate bank deposit metrics for a tenant and period.
SELECT
    COUNT(*) AS total_sessions,
    COUNT(*) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')) AS completed_sessions,
    COUNT(*) FILTER (WHERE status = 'EXPIRED') AS expired_sessions,
    COUNT(*) FILTER (WHERE status = 'FAILED') AS failed_sessions,
    CASE
        WHEN COUNT(*) = 0 THEN 0
        ELSE ROUND(
            100.0 * COUNT(*) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD'))
            / COUNT(*),
            2
        )
    END::NUMERIC(5,2) AS conversion_rate,
    COALESCE(SUM(received_amount) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')), 0)::NUMERIC(28,8) AS total_received,
    COALESCE(SUM(fee_amount) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')), 0)::NUMERIC(28,8) AS total_fees,
    COALESCE(SUM(net_amount) FILTER (WHERE status IN ('CREDITED','SETTLING','SETTLED','HELD')), 0)::NUMERIC(28,8) AS total_net
FROM bank_deposit_sessions
WHERE tenant_id = @tenant_id
  AND created_at >= @from_time::timestamptz
  AND created_at < @to_time::timestamptz;

-- name: GetReconciliationSummary :one
-- System-wide reconciliation health summary (no tenant_id on reconciliation_reports).
SELECT
    COUNT(*) AS total_runs,
    COALESCE(SUM(checks_passed), 0)::bigint AS checks_passed,
    COALESCE(SUM(checks_run - checks_passed), 0)::bigint AS checks_failed,
    CASE
        WHEN SUM(checks_run) = 0 THEN 0
        ELSE ROUND(100.0 * SUM(checks_passed) / SUM(checks_run), 2)
    END::NUMERIC(5,2) AS pass_rate,
    MAX(run_at) AS last_run_at,
    COUNT(*) FILTER (WHERE needs_review = true) AS needs_review_count
FROM reconciliation_reports
WHERE run_at >= @from_time::timestamptz
  AND run_at < @to_time::timestamptz;

-- name: GetDailySnapshots :many
-- Read pre-aggregated data from analytics_daily_snapshots.
SELECT
    id, tenant_id, snapshot_date, metric_type,
    source_currency, dest_currency, provider,
    transfer_count, completed_count, failed_count,
    volume_usd, fees_usd, on_ramp_fees_usd, off_ramp_fees_usd, network_fees_usd,
    avg_latency_ms, p50_latency_ms, p90_latency_ms, p95_latency_ms,
    success_rate, created_at
FROM analytics_daily_snapshots
WHERE tenant_id = @tenant_id
  AND metric_type = @metric_type
  AND snapshot_date >= @from_date::date
  AND snapshot_date <= @to_date::date
ORDER BY snapshot_date DESC;

-- name: UpsertDailySnapshot :exec
-- INSERT ON CONFLICT for nightly snapshot job.
INSERT INTO analytics_daily_snapshots (
    tenant_id, snapshot_date, metric_type,
    source_currency, dest_currency, provider,
    transfer_count, completed_count, failed_count,
    volume_usd, fees_usd, on_ramp_fees_usd, off_ramp_fees_usd, network_fees_usd,
    avg_latency_ms, p50_latency_ms, p90_latency_ms, p95_latency_ms,
    success_rate
) VALUES (
    @tenant_id, @snapshot_date, @metric_type,
    @source_currency, @dest_currency, @provider,
    @transfer_count, @completed_count, @failed_count,
    @volume_usd, @fees_usd, @on_ramp_fees_usd, @off_ramp_fees_usd, @network_fees_usd,
    @avg_latency_ms, @p50_latency_ms, @p90_latency_ms, @p95_latency_ms,
    @success_rate
) ON CONFLICT (tenant_id, snapshot_date, metric_type, source_currency, dest_currency, provider)
DO UPDATE SET
    transfer_count = EXCLUDED.transfer_count,
    completed_count = EXCLUDED.completed_count,
    failed_count = EXCLUDED.failed_count,
    volume_usd = EXCLUDED.volume_usd,
    fees_usd = EXCLUDED.fees_usd,
    on_ramp_fees_usd = EXCLUDED.on_ramp_fees_usd,
    off_ramp_fees_usd = EXCLUDED.off_ramp_fees_usd,
    network_fees_usd = EXCLUDED.network_fees_usd,
    avg_latency_ms = EXCLUDED.avg_latency_ms,
    p50_latency_ms = EXCLUDED.p50_latency_ms,
    p90_latency_ms = EXCLUDED.p90_latency_ms,
    p95_latency_ms = EXCLUDED.p95_latency_ms,
    success_rate = EXCLUDED.success_rate;

-- name: CreateExportJob :one
-- Create a new export job.
INSERT INTO analytics_export_jobs (tenant_id, export_type, parameters)
VALUES (@tenant_id, @export_type, @parameters)
RETURNING id, tenant_id, status, export_type, parameters, file_path,
    download_url, download_expires_at, row_count, error_message,
    created_at, completed_at;

-- name: GetExportJob :one
-- Get an export job by ID (tenant-scoped).
SELECT id, tenant_id, status, export_type, parameters, file_path,
    download_url, download_expires_at, row_count, error_message,
    created_at, completed_at
FROM analytics_export_jobs
WHERE id = @id AND tenant_id = @tenant_id;

-- name: ListExportJobs :many
-- List export jobs for a tenant.
SELECT id, tenant_id, status, export_type, parameters, file_path,
    download_url, download_expires_at, row_count, error_message,
    created_at, completed_at
FROM analytics_export_jobs
WHERE tenant_id = @tenant_id
ORDER BY created_at DESC
LIMIT @page_size;

-- name: UpdateExportJobStatus :exec
-- Update export job status after processing.
UPDATE analytics_export_jobs
SET status = @status,
    file_path = @file_path,
    download_url = @download_url,
    download_expires_at = @download_expires_at,
    row_count = @row_count,
    error_message = @error_message,
    completed_at = @completed_at
WHERE id = @id;

-- name: ListPendingExportJobs :many
-- List pending export jobs for the exporter to process.
SELECT id, tenant_id, status, export_type, parameters, file_path,
    download_url, download_expires_at, row_count, error_message,
    created_at, completed_at
FROM analytics_export_jobs
WHERE status = 'pending'
ORDER BY created_at ASC
LIMIT @batch_size;

-- name: ListActiveTenantIDs :many
-- List all active tenant IDs for the snapshot scheduler.
SELECT id FROM tenants WHERE status = 'ACTIVE';
