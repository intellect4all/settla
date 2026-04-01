-- name: InsertReserveOp :exec
INSERT INTO reserve_ops (id, tenant_id, currency, location, amount, reference, op_type, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING;

-- name: GetUncommittedReserveOps :many
SELECT r.id, r.tenant_id, r.currency, r.location, r.amount, r.reference, r.op_type, r.created_at
FROM reserve_ops r
WHERE r.op_type = 'reserve'
  AND NOT EXISTS (
      SELECT 1 FROM reserve_ops c
      WHERE c.reference = r.reference
        AND c.op_type IN ('commit', 'release', 'consume')
  )
ORDER BY r.created_at ASC;

-- name: MarkReserveOpCompleted :exec
UPDATE reserve_ops SET completed = true WHERE id = $1;

-- name: CleanupCompletedReserveOps :exec
DELETE FROM reserve_ops r
WHERE r.created_at < $1
  AND (
      r.op_type IN ('commit', 'release', 'consume')
      OR EXISTS (
          SELECT 1 FROM reserve_ops c
          WHERE c.reference = r.reference
            AND c.op_type IN ('commit', 'release', 'consume')
      )
  );
