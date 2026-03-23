-- name: UpsertPosition :one
INSERT INTO positions (
    tenant_id, currency, location, balance, locked, min_balance, target_balance
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (tenant_id, currency, location) DO UPDATE SET
    balance = EXCLUDED.balance,
    locked = EXCLUDED.locked,
    min_balance = EXCLUDED.min_balance,
    target_balance = EXCLUDED.target_balance,
    updated_at = now()
RETURNING *;

-- name: GetPosition :one
SELECT * FROM positions
WHERE tenant_id = $1 AND currency = $2 AND location = $3;

-- name: ListPositionsByTenant :many
SELECT * FROM positions
WHERE tenant_id = $1
ORDER BY currency, location;

-- name: ListAllPositions :many
SELECT * FROM positions
ORDER BY tenant_id, currency, location;

-- name: ListPositionsPaginated :many
SELECT * FROM positions
ORDER BY tenant_id, currency, location
LIMIT $1 OFFSET $2;

-- name: UpdatePositionBalances :exec
UPDATE positions
SET balance = $2, locked = $3, updated_at = now()
WHERE id = $1;

-- name: BatchUpdatePositions :exec
UPDATE positions
SET balance = $2, locked = $3, updated_at = now()
WHERE id = $1;

-- name: DeletePosition :exec
DELETE FROM positions WHERE id = $1;

-- name: CreatePositionHistory :one
INSERT INTO position_history (
    position_id, tenant_id, balance, locked,
    trigger_type, trigger_ref
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: ListPositionHistory :many
SELECT * FROM position_history
WHERE tenant_id = $1 AND position_id = $2
ORDER BY recorded_at DESC
LIMIT $3 OFFSET $4;

-- name: ListPositionHistoryInDateRange :many
SELECT * FROM position_history
WHERE tenant_id = $1
  AND recorded_at >= $2
  AND recorded_at < $3
ORDER BY recorded_at DESC
LIMIT $4 OFFSET $5;
