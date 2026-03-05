-- Settla Transfer DB Seed Data
-- Two demo tenants: Lemfi (UK→Nigeria) and Fincra (Nigeria→UK)

INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'a0000000-0000-0000-0000-000000000001',
    'Lemfi', 'lemfi', 'ACTIVE',
    '{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.lemfi.example/settla',
    'whsec_lemfi_demo_secret_key_2024',
    10000000.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'b0000000-0000-0000-0000-000000000002',
    'Fincra', 'fincra', 'ACTIVE',
    '{"onramp_bps": 50, "offramp_bps": 30, "min_fee_usd": "1.00", "max_fee_usd": "1000.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.fincra.example/settla',
    'whsec_fincra_demo_secret_key_2024',
    25000000.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- API keys (SHA-256 hashes of raw keys)
INSERT INTO api_keys (tenant_id, key_hash, key_prefix, environment, name)
VALUES (
    'a0000000-0000-0000-0000-000000000001',
    encode(sha256('sk_live_lemfi_demo_key'::bytea), 'hex'),
    'sk_live_',
    'LIVE',
    'Lemfi Production Key'
) ON CONFLICT (key_hash) DO NOTHING;

INSERT INTO api_keys (tenant_id, key_hash, key_prefix, environment, name)
VALUES (
    'b0000000-0000-0000-0000-000000000002',
    encode(sha256('sk_live_fincra_demo_key'::bytea), 'hex'),
    'sk_live_',
    'LIVE',
    'Fincra Production Key'
) ON CONFLICT (key_hash) DO NOTHING;
