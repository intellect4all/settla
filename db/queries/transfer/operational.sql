-- ============================================================================
-- manual_reviews
-- ============================================================================

-- name: CreateManualReview :one
INSERT INTO manual_reviews (
    transfer_id, tenant_id, status, transfer_status,
    stuck_since, attempted_recoveries
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: GetManualReview :one
SELECT * FROM manual_reviews
WHERE id = $1;

-- name: ListManualReviewsByStatus :many
SELECT * FROM manual_reviews
WHERE tenant_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: ListManualReviewsByTenant :many
SELECT * FROM manual_reviews
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: UpdateManualReview :exec
UPDATE manual_reviews
SET status = $2,
    resolution = $3,
    resolved_by = $4,
    resolved_at = CASE WHEN $2 = 'resolved' THEN now() ELSE resolved_at END
WHERE id = $1;

-- ============================================================================
-- compensation_records
-- ============================================================================

-- name: CreateCompensationRecord :one
INSERT INTO compensation_records (
    transfer_id, tenant_id, strategy, refund_amount, refund_currency
) VALUES (
    $1, $2, $3, $4, $5
) RETURNING *;

-- name: GetCompensationRecord :one
SELECT * FROM compensation_records
WHERE id = $1;

-- name: UpdateCompensationRecord :exec
UPDATE compensation_records
SET steps_completed = $2,
    steps_failed = $3,
    fx_loss = $4,
    status = $5,
    completed_at = CASE WHEN $5 = 'completed' THEN now() ELSE completed_at END
WHERE id = $1;

-- ============================================================================
-- net_settlements
-- ============================================================================

-- name: CreateNetSettlementOps :one
INSERT INTO net_settlements (
    tenant_id, period_start, period_end, corridors,
    net_by_currency, total_fees_usd, instructions, status, due_date
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING *;

-- name: GetNetSettlementOps :one
SELECT * FROM net_settlements
WHERE id = $1;

-- name: ListNetSettlementsByTenant :many
SELECT * FROM net_settlements
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListNetSettlementsByStatus :many
SELECT * FROM net_settlements
WHERE tenant_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- ============================================================================
-- reconciliation_reports
-- ============================================================================

-- name: CreateReconciliationReport :one
INSERT INTO reconciliation_reports (
    job_name, run_at, duration_ms, checks_run, checks_passed,
    discrepancies, auto_corrected, needs_review
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: GetReconciliationReport :one
SELECT * FROM reconciliation_reports
WHERE id = $1;

-- name: ListReconciliationReports :many
SELECT * FROM reconciliation_reports
ORDER BY run_at DESC
LIMIT $1 OFFSET $2;
