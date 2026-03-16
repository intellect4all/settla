-- name: UpsertBlockCheckpoint :one
INSERT INTO block_checkpoints (chain, block_number, block_hash, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (chain)
DO UPDATE SET block_number = $2, block_hash = $3, updated_at = now()
RETURNING *;

-- name: GetBlockCheckpoint :one
SELECT * FROM block_checkpoints WHERE chain = $1;

-- name: ListBlockCheckpoints :many
SELECT * FROM block_checkpoints ORDER BY chain;
