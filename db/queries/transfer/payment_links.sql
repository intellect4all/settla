-- name: CreatePaymentLink :one
INSERT INTO payment_links (
    tenant_id, short_code, description, session_config,
    use_limit, expires_at, redirect_url, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: GetPaymentLinkByID :one
SELECT * FROM payment_links
WHERE id = $1 AND tenant_id = $2;

-- name: GetPaymentLinkByShortCode :one
-- SECURITY NOTE: This query intentionally omits tenant_id because it serves the
-- public payment link resolution flow (/v1/payment-links/resolve/:code).
-- The caller MUST only return public-safe fields (short_code, description, amount,
-- currency, status, expires_at) — never tenant internal data.
SELECT * FROM payment_links
WHERE short_code = $1;

-- name: ListPaymentLinksByTenant :many
SELECT * FROM payment_links
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CountPaymentLinksByTenant :one
SELECT count(*) FROM payment_links
WHERE tenant_id = $1;

-- name: IncrementPaymentLinkUseCount :exec
UPDATE payment_links
SET use_count = use_count + 1, updated_at = now()
WHERE id = $1;

-- name: UpdatePaymentLinkStatus :exec
UPDATE payment_links
SET status = $2, updated_at = now()
WHERE id = $1 AND tenant_id = $3;
