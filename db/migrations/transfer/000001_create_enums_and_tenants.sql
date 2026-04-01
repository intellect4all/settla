-- +goose Up


CREATE TYPE transfer_status_enum AS ENUM (
    'CREATED','FUNDED','ON_RAMPING','SETTLING','OFF_RAMPING',
    'COMPLETED','FAILED','REFUNDING','REFUNDED'
);

CREATE TYPE compensation_strategy_enum AS ENUM (
    'SIMPLE_REFUND','REVERSE_ONRAMP','CREDIT_STABLECOIN','MANUAL_REVIEW'
);

CREATE TYPE review_status_enum AS ENUM ('pending','investigating','resolved');

CREATE TYPE provider_tx_type_enum AS ENUM ('ON_RAMP','OFF_RAMP','BLOCKCHAIN');

CREATE TYPE deposit_session_status_enum AS ENUM (
    'PENDING_PAYMENT','DETECTED','CONFIRMED','CREDITING','CREDITED',
    'SETTLING','SETTLED','HELD','EXPIRED','FAILED','CANCELLED'
);

CREATE TYPE settlement_preference_enum AS ENUM ('AUTO_CONVERT','HOLD','THRESHOLD');

CREATE TYPE bank_deposit_session_status_enum AS ENUM (
    'PENDING_PAYMENT','PAYMENT_RECEIVED','CREDITING','CREDITED',
    'SETTLING','SETTLED','HELD','EXPIRED','FAILED','CANCELLED',
    'UNDERPAID','OVERPAID'
);

CREATE TYPE virtual_account_type_enum AS ENUM ('PERMANENT','TEMPORARY');

CREATE TYPE payment_mismatch_policy_enum AS ENUM ('ACCEPT','REJECT');


CREATE TABLE tenants (
    id                          UUID PRIMARY KEY,
    name                        TEXT NOT NULL,
    slug                        TEXT NOT NULL UNIQUE,
    status                      TEXT NOT NULL DEFAULT 'ONBOARDING'
                                    CHECK (status IN ('ACTIVE','SUSPENDED','ONBOARDING')),
    fee_schedule                JSONB NOT NULL DEFAULT '{}',
    fee_schedule_version        INT NOT NULL DEFAULT 1,
    settlement_model            TEXT NOT NULL DEFAULT 'PREFUNDED'
                                    CHECK (settlement_model IN ('PREFUNDED','NET_SETTLEMENT')),
    webhook_url                 TEXT,
    webhook_secret              TEXT,
    webhook_events              TEXT[] NOT NULL DEFAULT '{}',
    daily_limit_usd             NUMERIC(28,8),
    per_transfer_limit          NUMERIC(28,8),
    max_pending_transfers       INTEGER NOT NULL DEFAULT 0,
    kyb_status                  TEXT NOT NULL DEFAULT 'PENDING'
                                    CHECK (kyb_status IN ('PENDING','IN_REVIEW','VERIFIED','REJECTED')),
    kyb_verified_at             TIMESTAMPTZ,
    -- Crypto deposit settings
    crypto_enabled              BOOLEAN NOT NULL DEFAULT false,
    default_settlement_pref     TEXT NOT NULL DEFAULT 'HOLD'
                                    CHECK (default_settlement_pref IN ('AUTO_CONVERT','HOLD','THRESHOLD')),
    supported_chains            TEXT[] NOT NULL DEFAULT '{}',
    min_confirmations_tron      INTEGER NOT NULL DEFAULT 19,
    min_confirmations_eth       INTEGER NOT NULL DEFAULT 12,
    min_confirmations_base      INTEGER NOT NULL DEFAULT 12,
    payment_tolerance_bps       INTEGER NOT NULL DEFAULT 50,
    default_session_ttl_secs    INTEGER NOT NULL DEFAULT 3600,
    -- Bank deposit settings
    bank_deposits_enabled       BOOLEAN NOT NULL DEFAULT false,
    default_banking_partner     TEXT,
    bank_supported_currencies   TEXT[] NOT NULL DEFAULT '{}',
    default_mismatch_policy     payment_mismatch_policy_enum NOT NULL DEFAULT 'REJECT',
    bank_default_session_ttl_secs INT NOT NULL DEFAULT 3600,
    -- General
    metadata                    JSONB NOT NULL DEFAULT '{}',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);


CREATE TABLE api_keys (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL REFERENCES tenants(id),
    key_hash    TEXT NOT NULL UNIQUE,
    key_prefix  TEXT NOT NULL,
    environment TEXT NOT NULL CHECK (environment IN ('LIVE','TEST')),
    name        TEXT,
    is_active   BOOLEAN NOT NULL DEFAULT true,
    last_used_at TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_api_keys_tenant_id ON api_keys(tenant_id);
CREATE INDEX idx_api_keys_active_hash ON api_keys(key_hash) WHERE is_active = true;


CREATE TABLE portal_users (
    id                      UUID PRIMARY KEY,
    tenant_id               UUID NOT NULL REFERENCES tenants(id),
    email                   TEXT NOT NULL UNIQUE,
    password_hash           TEXT NOT NULL,
    display_name            TEXT NOT NULL,
    role                    TEXT NOT NULL DEFAULT 'OWNER'
                                CHECK (role IN ('OWNER','ADMIN','MEMBER')),
    email_verified          BOOLEAN NOT NULL DEFAULT false,
    email_token_hash        TEXT,
    email_token_expires_at  TIMESTAMPTZ,
    last_login_at           TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_portal_users_tenant_id ON portal_users(tenant_id);
CREATE INDEX idx_portal_users_email_token ON portal_users(email_token_hash)
    WHERE email_token_hash IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS portal_users;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS tenants;
DROP TYPE IF EXISTS payment_mismatch_policy_enum;
DROP TYPE IF EXISTS virtual_account_type_enum;
DROP TYPE IF EXISTS bank_deposit_session_status_enum;
DROP TYPE IF EXISTS settlement_preference_enum;
DROP TYPE IF EXISTS deposit_session_status_enum;
DROP TYPE IF EXISTS provider_tx_type_enum;
DROP TYPE IF EXISTS review_status_enum;
DROP TYPE IF EXISTS compensation_strategy_enum;
DROP TYPE IF EXISTS transfer_status_enum;
