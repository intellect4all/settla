-- name: InsertProviderWebhookLog :one
INSERT INTO provider_webhook_logs (
    provider_slug, idempotency_key, raw_body, http_headers, source_ip, status
) VALUES ($1, $2, $3, $4, $5, 'received')
ON CONFLICT (provider_slug, idempotency_key, created_at) DO NOTHING
RETURNING id, created_at;

-- name: CheckWebhookDuplicate :one
SELECT id, status FROM provider_webhook_logs
WHERE provider_slug = $1 AND idempotency_key = $2 AND status = 'processed'
ORDER BY created_at DESC LIMIT 1;

-- name: UpdateWebhookLogProcessed :exec
UPDATE provider_webhook_logs
SET transfer_id = $2,
    tenant_id = $3,
    normalized = $4,
    status = $5,
    error_message = $6,
    processed_at = now()
WHERE id = $1 AND created_at = $7;

-- name: GetProviderWebhookLog :one
SELECT id, provider_slug, idempotency_key, transfer_id, tenant_id,
       raw_body, normalized, status, error_message, http_headers,
       source_ip, created_at, processed_at
FROM provider_webhook_logs
WHERE id = $1 AND created_at = $2;

-- name: ListProviderWebhookLogsByTransfer :many
SELECT id, provider_slug, idempotency_key, transfer_id, tenant_id,
       status, error_message, created_at, processed_at
FROM provider_webhook_logs
WHERE transfer_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListProviderWebhookLogsByProvider :many
SELECT id, provider_slug, idempotency_key, transfer_id, tenant_id,
       status, error_message, created_at, processed_at
FROM provider_webhook_logs
WHERE provider_slug = $1 AND status = COALESCE(sqlc.narg('status_filter'), status)
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
