-- name: BatchInsertPositionEvents :exec
INSERT INTO position_events (
    id, position_id, tenant_id, event_type, amount,
    balance_after, locked_after, reference_id, reference_type,
    idempotency_key, recorded_at
)
SELECT
    unnest(@ids::uuid[]),
    unnest(@position_ids::uuid[]),
    unnest(@tenant_ids::uuid[]),
    unnest(@event_types::text[]),
    unnest(@amounts::numeric[]),
    unnest(@balance_afters::numeric[]),
    unnest(@locked_afters::numeric[]),
    unnest(@reference_ids::uuid[]),
    unnest(@reference_types::text[]),
    unnest(@idempotency_keys::text[]),
    unnest(@recorded_ats::timestamptz[])
ON CONFLICT (idempotency_key, recorded_at) DO NOTHING;

-- name: GetEventsAfterTimestamp :many
SELECT id, position_id, tenant_id, event_type, amount,
       balance_after, locked_after, reference_id, reference_type,
       idempotency_key, recorded_at
FROM position_events
WHERE position_id = $1 AND recorded_at > $2
ORDER BY recorded_at ASC;

-- name: GetPositionEventHistory :many
SELECT id, position_id, tenant_id, event_type, amount,
       balance_after, locked_after, reference_id, reference_type,
       idempotency_key, recorded_at
FROM position_events
WHERE tenant_id = $1 AND position_id = $2
  AND recorded_at >= $3 AND recorded_at <= $4
ORDER BY recorded_at DESC
LIMIT $5 OFFSET $6;

-- name: CountPositionEvents :one
SELECT count(*) FROM position_events
WHERE tenant_id = $1 AND position_id = $2
  AND recorded_at >= $3 AND recorded_at <= $4;
