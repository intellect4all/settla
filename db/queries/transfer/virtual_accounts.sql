-- name: DispenseVirtualAccount :one
UPDATE virtual_account_pool
SET available = false, session_id = $3, updated_at = now()
WHERE id = (
    SELECT vap.id FROM virtual_account_pool vap
    WHERE vap.tenant_id = $1 AND vap.currency = $2 AND vap.available = true AND vap.account_type = 'TEMPORARY'
    ORDER BY vap.created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: RecycleVirtualAccount :exec
UPDATE virtual_account_pool
SET available = true, session_id = NULL, updated_at = now()
WHERE account_number = $1;

-- name: InsertVirtualAccount :one
INSERT INTO virtual_account_pool (
    tenant_id, banking_partner_id, account_number, account_name,
    sort_code, iban, currency, account_type
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: ListVirtualAccountsByTenant :many
SELECT * FROM virtual_account_pool
WHERE tenant_id = $1
ORDER BY created_at ASC;

-- name: InsertVirtualAccountIndex :one
INSERT INTO virtual_account_index (
    account_number, tenant_id, session_id, account_type
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: GetVirtualAccountIndexByNumber :one
SELECT * FROM virtual_account_index
WHERE account_number = $1
ORDER BY created_at DESC
LIMIT 1;
