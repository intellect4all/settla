-- Portal users: self-service login for tenant administrators.
-- Each tenant can have multiple users (OWNER, ADMIN, MEMBER).

-- Add IN_REVIEW to kyb_status constraint
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_kyb_status_check;
ALTER TABLE tenants ADD CONSTRAINT tenants_kyb_status_check
    CHECK (kyb_status IN ('PENDING','IN_REVIEW','VERIFIED','REJECTED'));

CREATE TABLE portal_users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'OWNER'
                    CHECK (role IN ('OWNER','ADMIN','MEMBER')),
    email_verified  BOOLEAN NOT NULL DEFAULT false,
    email_token_hash TEXT,
    email_token_expires_at TIMESTAMPTZ,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_portal_users_tenant_id ON portal_users(tenant_id);
CREATE INDEX idx_portal_users_email_token ON portal_users(email_token_hash) WHERE email_token_hash IS NOT NULL;
