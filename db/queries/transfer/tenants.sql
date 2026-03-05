-- name: CreateTenant :one
INSERT INTO tenants (
    name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
) RETURNING *;

-- name: GetTenant :one
SELECT * FROM tenants WHERE id = $1;

-- name: GetTenantBySlug :one
SELECT * FROM tenants WHERE slug = $1;

-- name: ListTenants :many
SELECT * FROM tenants
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: UpdateTenantStatus :exec
UPDATE tenants
SET status = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateTenantKYB :exec
UPDATE tenants
SET kyb_status = $2,
    kyb_verified_at = CASE WHEN $2 = 'VERIFIED' THEN now() ELSE kyb_verified_at END,
    updated_at = now()
WHERE id = $1;

-- name: UpdateTenantWebhook :exec
UPDATE tenants
SET webhook_url = $2, webhook_secret = $3, updated_at = now()
WHERE id = $1;

-- name: UpdateTenantFeeSchedule :exec
UPDATE tenants
SET fee_schedule = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateTenantLimits :exec
UPDATE tenants
SET daily_limit_usd = $2, per_transfer_limit = $3, updated_at = now()
WHERE id = $1;

-- name: CreateAPIKey :one
INSERT INTO api_keys (
    tenant_id, key_hash, key_prefix, environment, name, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: ValidateAPIKey :one
SELECT ak.id, ak.tenant_id, ak.environment, ak.is_active, ak.expires_at,
       t.id AS tenant_uuid, t.slug, t.status AS tenant_status,
       t.fee_schedule, t.settlement_model,
       t.daily_limit_usd, t.per_transfer_limit,
       t.webhook_url, t.webhook_secret
FROM api_keys ak
JOIN tenants t ON t.id = ak.tenant_id
WHERE ak.key_hash = $1
  AND ak.is_active = true
  AND (ak.expires_at IS NULL OR ak.expires_at > now());

-- name: UpdateAPIKeyLastUsed :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: DeactivateAPIKey :exec
UPDATE api_keys SET is_active = false WHERE id = $1;

-- name: ListAPIKeysByTenant :many
SELECT id, tenant_id, key_prefix, environment, name, is_active, last_used_at, expires_at, created_at
FROM api_keys
WHERE tenant_id = $1
ORDER BY created_at DESC;
