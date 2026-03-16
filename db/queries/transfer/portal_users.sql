-- name: CreatePortalUser :one
INSERT INTO portal_users (
    tenant_id, email, password_hash, display_name, role,
    email_token_hash, email_token_expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: GetPortalUserByEmail :one
SELECT pu.*, t.name AS tenant_name, t.slug AS tenant_slug,
       t.status AS tenant_status, t.kyb_status
FROM portal_users pu
JOIN tenants t ON t.id = pu.tenant_id
WHERE pu.email = $1;

-- name: GetPortalUserByID :one
SELECT pu.*, t.name AS tenant_name, t.slug AS tenant_slug,
       t.status AS tenant_status, t.kyb_status
FROM portal_users pu
JOIN tenants t ON t.id = pu.tenant_id
WHERE pu.id = $1;

-- name: VerifyPortalUserEmail :exec
UPDATE portal_users
SET email_verified = true,
    email_token_hash = NULL,
    email_token_expires_at = NULL,
    updated_at = now()
WHERE email_token_hash = $1
  AND email_token_expires_at > now();

-- name: UpdatePortalUserLastLogin :exec
UPDATE portal_users
SET last_login_at = now(), updated_at = now()
WHERE id = $1;

-- name: GetPortalUsersByTenant :many
SELECT id, tenant_id, email, display_name, role, email_verified,
       last_login_at, created_at, updated_at
FROM portal_users
WHERE tenant_id = $1
ORDER BY created_at ASC;
