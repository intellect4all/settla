-- Tenants: each fintech (Lemfi, Fincra) that integrates Settla.
-- At 50M txn/day, tenant table is small (hundreds of rows) but queried on every request via API key lookup.

CREATE TABLE tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'ACTIVE'
                    CHECK (status IN ('ACTIVE','SUSPENDED','ONBOARDING')),
    fee_schedule    JSONB NOT NULL DEFAULT '{}',
    settlement_model TEXT NOT NULL DEFAULT 'PREFUNDED'
                    CHECK (settlement_model IN ('PREFUNDED', 'NET_SETTLEMENT')),
    webhook_url     TEXT,
    webhook_secret  TEXT,
    daily_limit_usd    NUMERIC(28, 8),
    per_transfer_limit NUMERIC(28, 8),
    kyb_status      TEXT NOT NULL DEFAULT 'PENDING'
                    CHECK (kyb_status IN ('PENDING','VERIFIED','REJECTED')),
    kyb_verified_at TIMESTAMPTZ,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    key_hash        TEXT NOT NULL UNIQUE,
    key_prefix      TEXT NOT NULL,
    environment     TEXT NOT NULL CHECK (environment IN ('LIVE', 'TEST')),
    name            TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    last_used_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_api_keys_tenant_id ON api_keys(tenant_id);
