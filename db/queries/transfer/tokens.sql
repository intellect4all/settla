-- name: ListTokensByChain :many
SELECT * FROM tokens WHERE chain = $1 AND is_active = true ORDER BY symbol;

-- name: GetToken :one
SELECT * FROM tokens WHERE chain = $1 AND symbol = $2;

-- name: GetTokenByContract :one
SELECT * FROM tokens WHERE chain = $1 AND contract_address = $2;

-- name: UpsertToken :one
INSERT INTO tokens (chain, symbol, contract_address, decimals, is_active)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (chain, symbol)
DO UPDATE SET contract_address = $3, decimals = $4, is_active = $5, updated_at = now()
RETURNING *;

-- name: ListAllActiveTokens :many
SELECT * FROM tokens WHERE is_active = true ORDER BY chain, symbol;
