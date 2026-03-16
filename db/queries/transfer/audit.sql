-- name: InsertAuditEntry :exec
INSERT INTO audit_log (tenant_id, actor_type, actor_id, action, entity_type, entity_id, old_value, new_value, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListAuditEntriesByTenant :many
SELECT * FROM audit_log
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListAuditEntriesByEntity :many
SELECT * FROM audit_log
WHERE entity_type = $1 AND entity_id = $2
ORDER BY created_at DESC
LIMIT $3;
