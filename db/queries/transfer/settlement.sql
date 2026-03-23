-- name: ListCompletedTransfersByPeriod :many
SELECT id, source_currency, source_amount, dest_currency, dest_amount,
       COALESCE((fees->>'total_usd')::NUMERIC(28,8), 0) AS fees_usd
FROM transfers
WHERE tenant_id = $1
  AND status = 'COMPLETED'
  AND completed_at >= $2
  AND completed_at < $3;

-- name: CreateNetSettlement :one
INSERT INTO net_settlements (
    id, tenant_id, period_start, period_end,
    corridors, net_by_currency, total_fees_usd,
    instructions, status, due_date
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: GetNetSettlement :one
SELECT * FROM net_settlements WHERE id = $1;

-- name: ListPendingSettlements :many
SELECT ns.*, t.name AS tenant_name
FROM net_settlements ns
JOIN tenants t ON t.id = ns.tenant_id
WHERE ns.tenant_id = $1
  AND ns.status IN ('pending', 'overdue')
ORDER BY ns.due_date ASC;

-- name: ListAllPendingSettlements :many
-- Admin-only: returns pending settlements across all tenants.
-- Callers MUST verify admin authorization before invoking.
SELECT ns.*, t.name AS tenant_name
FROM net_settlements ns
JOIN tenants t ON t.id = ns.tenant_id
WHERE ns.status IN ('pending', 'overdue')
ORDER BY ns.due_date ASC;

-- name: UpdateSettlementStatus :exec
UPDATE net_settlements
SET status = $2,
    settled_at = CASE WHEN $2 = 'settled' THEN now() ELSE settled_at END
WHERE id = $1;

-- name: ListTenantsBySettlementModel :many
SELECT * FROM tenants
WHERE settlement_model = $1
ORDER BY id
LIMIT $2 OFFSET $3;

-- name: ListActiveTenantIDsBySettlementModel :many
-- Cursor-based pagination: pass uuid.Nil for the first page.
SELECT id FROM tenants
WHERE settlement_model = $1 AND status = 'ACTIVE' AND id > $2
ORDER BY id
LIMIT $3;

-- name: CountActiveTenantsBySettlementModel :one
SELECT count(*) FROM tenants
WHERE settlement_model = $1 AND status = 'ACTIVE';

-- name: AggregateCompletedTransfersByPeriod :many
SELECT source_currency, dest_currency,
       SUM(source_amount)::NUMERIC(28,8) AS total_source,
       SUM(dest_amount)::NUMERIC(28,8) AS total_dest,
       COUNT(*) AS transfer_count,
       SUM(COALESCE((fees->>'total_usd')::NUMERIC(28,8), 0))::NUMERIC(28,8) AS total_fees_usd
FROM transfers
WHERE tenant_id = $1
  AND status = 'COMPLETED'
  AND completed_at >= $2
  AND completed_at < $3
GROUP BY source_currency, dest_currency;
