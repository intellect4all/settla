-- name: CreatePositionTransaction :one
INSERT INTO position_transactions (
    id, tenant_id, type, currency, location, amount, status, method, destination, reference, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING *;

-- name: GetPositionTransaction :one
SELECT * FROM position_transactions
WHERE id = $1 AND tenant_id = $2;

-- name: UpdatePositionTransactionStatus :exec
UPDATE position_transactions
SET status = $3, failure_reason = $4, version = version + 1, updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: ListPositionTransactionsByTenant :many
SELECT * FROM position_transactions
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListPositionTransactionsByTenantFirst :many
SELECT * FROM position_transactions
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListPositionTransactionsByTenantCursor :many
SELECT * FROM position_transactions
WHERE tenant_id = $1
  AND created_at < @cursor_created_at
ORDER BY created_at DESC
LIMIT @page_size;

-- name: ListPositionTransactionsByTenantAndStatus :many
SELECT * FROM position_transactions
WHERE tenant_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: CountPositionTransactionsByTenant :one
SELECT count(*) FROM position_transactions
WHERE tenant_id = $1;
