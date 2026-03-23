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
-- SECURITY NOTE: Intentionally omits tenant_id — virtual accounts are recycled by
-- the worker after a session completes, using the account number from the session.
-- The tenant context is validated by the caller (BankDepositWorker) before recycling.
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

-- name: ListVirtualAccountsByTenantPaginated :many
SELECT * FROM virtual_account_pool
WHERE tenant_id = @tenant_id
  AND (@currency::text = '' OR currency = @currency)
  AND (@account_type::text = '' OR account_type = @account_type::virtual_account_type_enum)
ORDER BY created_at ASC
LIMIT @page_limit OFFSET @page_offset;

-- name: CountVirtualAccountsByTenant :one
SELECT count(*) FROM virtual_account_pool
WHERE tenant_id = @tenant_id
  AND (@currency::text = '' OR currency = @currency)
  AND (@account_type::text = '' OR account_type = @account_type::virtual_account_type_enum);

-- name: CountAvailableVirtualAccountsByCurrency :many
SELECT currency, count(*) AS available_count
FROM virtual_account_pool
WHERE tenant_id = $1 AND available = true
GROUP BY currency;

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

-- name: UpsertVirtualAccountIndex :exec
-- Inserts or updates the virtual account index entry. On conflict (duplicate
-- account_number), updates session_id and account_type to point to the new session.
INSERT INTO virtual_account_index (account_number, tenant_id, session_id, account_type)
VALUES (@account_number, @tenant_id, @session_id, @account_type)
ON CONFLICT (account_number) DO UPDATE
    SET session_id = @session_id, account_type = @account_type;
