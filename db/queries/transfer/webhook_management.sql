-- name: InsertWebhookDelivery :one
INSERT INTO webhook_deliveries (
    tenant_id, event_type, transfer_id, delivery_id,
    webhook_url, status, status_code, attempt, max_attempts,
    error_message, request_body, duration_ms, delivered_at, next_retry_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    $10, $11, $12, $13, $14
) RETURNING id, created_at;

-- name: ListWebhookDeliveries :many
SELECT id, tenant_id, event_type, transfer_id, delivery_id,
       webhook_url, status, status_code, attempt, max_attempts,
       error_message, duration_ms, created_at, delivered_at
FROM webhook_deliveries
WHERE tenant_id = @tenant_id
  AND (@event_type::text = '' OR event_type = @event_type)
  AND (@status_filter::text = '' OR status = @status_filter)
ORDER BY created_at DESC
LIMIT @page_size
OFFSET @page_offset;

-- name: CountWebhookDeliveries :one
SELECT COUNT(*)::bigint AS total
FROM webhook_deliveries
WHERE tenant_id = @tenant_id
  AND (@event_type::text = '' OR event_type = @event_type)
  AND (@status_filter::text = '' OR status = @status_filter);

-- name: GetWebhookDelivery :one
SELECT id, tenant_id, event_type, transfer_id, delivery_id,
       webhook_url, status, status_code, attempt, max_attempts,
       error_message, request_body, duration_ms,
       created_at, delivered_at, next_retry_at
FROM webhook_deliveries
WHERE id = @delivery_id AND tenant_id = @tenant_id;

-- name: GetWebhookDeliveryStats :one
SELECT
    COUNT(*)::bigint AS total_deliveries,
    COUNT(*) FILTER (WHERE status = 'delivered')::bigint AS successful,
    COUNT(*) FILTER (WHERE status = 'failed')::bigint AS failed,
    COUNT(*) FILTER (WHERE status = 'dead_letter')::bigint AS dead_lettered,
    COUNT(*) FILTER (WHERE status = 'pending')::bigint AS pending,
    COALESCE(AVG(duration_ms) FILTER (WHERE status = 'delivered'), 0)::integer AS avg_latency_ms,
    COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status = 'delivered'), 0)::integer AS p95_latency_ms
FROM webhook_deliveries
WHERE tenant_id = @tenant_id
  AND created_at >= @since::timestamptz;

-- name: ListWebhookEventSubscriptions :many
SELECT id, tenant_id, event_type, created_at
FROM webhook_event_subscriptions
WHERE tenant_id = @tenant_id
ORDER BY event_type;

-- name: UpsertWebhookEventSubscription :one
INSERT INTO webhook_event_subscriptions (tenant_id, event_type)
VALUES (@tenant_id, @event_type)
ON CONFLICT (tenant_id, event_type) DO NOTHING
RETURNING id, tenant_id, event_type, created_at;

-- name: DeleteWebhookEventSubscription :exec
DELETE FROM webhook_event_subscriptions
WHERE tenant_id = @tenant_id AND event_type = @event_type;

-- name: DeleteAllWebhookEventSubscriptions :exec
DELETE FROM webhook_event_subscriptions
WHERE tenant_id = @tenant_id;

-- name: UpdateTenantWebhookEvents :exec
UPDATE tenants
SET webhook_events = @events::text[],
    updated_at = now()
WHERE id = @tenant_id;
