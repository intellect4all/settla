-- name: InsertOutboxEntry :one
INSERT INTO outbox (
    aggregate_type, aggregate_id, tenant_id, event_type,
    payload, is_intent, max_retries, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, now()
) RETURNING id, aggregate_type, aggregate_id, tenant_id, event_type,
    payload, is_intent, published, published_at, retry_count, max_retries, created_at;

-- name: InsertOutboxEntries :copyfrom
INSERT INTO outbox (
    id, aggregate_type, aggregate_id, tenant_id, event_type,
    payload, is_intent, max_retries, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
);

-- name: GetUnpublishedEntries :many
SELECT id, aggregate_type, aggregate_id, tenant_id, event_type,
    payload, is_intent, published, published_at, retry_count, max_retries, created_at
FROM outbox
WHERE published = false
  AND retry_count < max_retries
ORDER BY created_at ASC
LIMIT $1;

-- name: MarkPublished :exec
UPDATE outbox
SET published = true, published_at = now()
WHERE id = $1 AND created_at = $2 AND published = false;

-- name: MarkFailed :exec
UPDATE outbox
SET retry_count = retry_count + 1
WHERE id = $1 AND created_at = $2;

-- name: GetOutboxEntriesByAggregate :many
SELECT id, aggregate_type, aggregate_id, tenant_id, event_type,
    payload, is_intent, published, published_at, retry_count, max_retries, created_at
FROM outbox
WHERE aggregate_type = $1 AND aggregate_id = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;
