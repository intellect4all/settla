-- Settla Transfer DB Seed Data
-- 50 demo tenants for load testing: GBP-based (odd), NGN-based (even)

-- ── Tenant 1: Lemfi (GBP) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'a0000000-0000-0000-0000-000000000001',
    'Lemfi', 'lemfi', 'ACTIVE',
    '{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00", "bank_collection_bps": 30, "bank_collection_min_fee_usd": "0.50", "bank_collection_max_fee_usd": "250.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.lemfi.example/settla',
    'whsec_lemfi_demo_secret_key_2024',
    9999999999.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 2: Fincra (NGN) ─────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'b0000000-0000-0000-0000-000000000002',
    'Fincra', 'fincra', 'ACTIVE',
    '{"onramp_bps": 50, "offramp_bps": 30, "min_fee_usd": "1.00", "max_fee_usd": "1000.00", "bank_collection_bps": 25, "bank_collection_min_fee_usd": "1.00", "bank_collection_max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.fincra.example/settla',
    'whsec_fincra_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 3: Paystack (GBP) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'c0000000-0000-0000-0000-000000000003',
    'Paystack', 'paystack', 'ACTIVE',
    '{"onramp_bps": 35, "offramp_bps": 20, "min_fee_usd": "0.25", "max_fee_usd": "250.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.paystack.example/settla',
    'whsec_paystack_demo_secret_key_2024',
    9999999999.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 4: Flutterwave (NGN) ────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'd0000000-0000-0000-0000-000000000004',
    'Flutterwave', 'flutterwave', 'ACTIVE',
    '{"onramp_bps": 45, "offramp_bps": 25, "min_fee_usd": "0.75", "max_fee_usd": "750.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.flutterwave.example/settla',
    'whsec_flutterwave_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 5: Chipper (GBP) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'e0000000-0000-0000-0000-000000000005',
    'Chipper', 'chipper', 'ACTIVE',
    '{"onramp_bps": 30, "offramp_bps": 20, "min_fee_usd": "0.50", "max_fee_usd": "300.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.chipper.example/settla',
    'whsec_chipper_demo_secret_key_2024',
    9999999999.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 6: Moniepoint (NGN) ─────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    'f0000000-0000-0000-0000-000000000006',
    'Moniepoint', 'moniepoint', 'ACTIVE',
    '{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "1.00", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.moniepoint.example/settla',
    'whsec_moniepoint_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 7: Kuda (GBP) ───────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '10000000-0000-0000-0000-000000000007',
    'Kuda', 'kuda', 'ACTIVE',
    '{"onramp_bps": 35, "offramp_bps": 20, "min_fee_usd": "0.25", "max_fee_usd": "200.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.kuda.example/settla',
    'whsec_kuda_demo_secret_key_2024',
    9999999999.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 8: OPay (NGN) ───────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '20000000-0000-0000-0000-000000000008',
    'OPay', 'opay', 'ACTIVE',
    '{"onramp_bps": 45, "offramp_bps": 30, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.opay.example/settla',
    'whsec_opay_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 9: Ecobank (GBP) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '30000000-0000-0000-0000-000000000009',
    'Ecobank', 'ecobank', 'ACTIVE',
    '{"onramp_bps": 30, "offramp_bps": 15, "min_fee_usd": "1.00", "max_fee_usd": "1000.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.ecobank.example/settla',
    'whsec_ecobank_demo_secret_key_2024',
    9999999999.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 10: Access (NGN) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '40000000-0000-0000-0000-000000000010',
    'Access', 'access', 'ACTIVE',
    '{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.access.example/settla',
    'whsec_access_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 11: PalmPay (GBP) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000011',
    'PalmPay', 'palmpay', 'ACTIVE',
    '{"onramp_bps": 35, "offramp_bps": 20, "min_fee_usd": "0.50", "max_fee_usd": "400.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.palmpay.example/settla',
    'whsec_palmpay_demo_secret_key_2024',
    9999999999.00000000,
    600000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 12: Carbon (NGN) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000012',
    'Carbon', 'carbon', 'ACTIVE',
    '{"onramp_bps": 45, "offramp_bps": 30, "min_fee_usd": "0.75", "max_fee_usd": "600.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.carbon.example/settla',
    'whsec_carbon_demo_secret_key_2024',
    9999999999.00000000,
    800000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 13: FairMoney (GBP) ─────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000013',
    'FairMoney', 'fairmoney', 'ACTIVE',
    '{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.fairmoney.example/settla',
    'whsec_fairmoney_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 14: Wise (NGN) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000014',
    'Wise', 'wise', 'ACTIVE',
    '{"onramp_bps": 25, "offramp_bps": 15, "min_fee_usd": "0.25", "max_fee_usd": "200.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.wise.example/settla',
    'whsec_wise_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 15: WorldRemit (GBP) ────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000015',
    'WorldRemit', 'worldremit', 'ACTIVE',
    '{"onramp_bps": 30, "offramp_bps": 18, "min_fee_usd": "0.50", "max_fee_usd": "350.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.worldremit.example/settla',
    'whsec_worldremit_demo_secret_key_2024',
    9999999999.00000000,
    900000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 16: Monzo (NGN) ─────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000016',
    'Monzo', 'monzo', 'ACTIVE',
    '{"onramp_bps": 28, "offramp_bps": 16, "min_fee_usd": "0.25", "max_fee_usd": "250.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.monzo.example/settla',
    'whsec_monzo_demo_secret_key_2024',
    9999999999.00000000,
    750000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 17: Revolut (GBP) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000017',
    'Revolut', 'revolut', 'ACTIVE',
    '{"onramp_bps": 25, "offramp_bps": 15, "min_fee_usd": "0.25", "max_fee_usd": "200.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.revolut.example/settla',
    'whsec_revolut_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 18: Paga (NGN) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000018',
    'Paga', 'paga', 'ACTIVE',
    '{"onramp_bps": 50, "offramp_bps": 30, "min_fee_usd": "1.00", "max_fee_usd": "700.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.paga.example/settla',
    'whsec_paga_demo_secret_key_2024',
    9999999999.00000000,
    600000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 19: Remitly (GBP) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000019',
    'Remitly', 'remitly', 'ACTIVE',
    '{"onramp_bps": 33, "offramp_bps": 22, "min_fee_usd": "0.50", "max_fee_usd": "450.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.remitly.example/settla',
    'whsec_remitly_demo_secret_key_2024',
    9999999999.00000000,
    850000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 20: TeamApt (NGN) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000020',
    'TeamApt', 'teamapt', 'ACTIVE',
    '{"onramp_bps": 42, "offramp_bps": 28, "min_fee_usd": "0.75", "max_fee_usd": "550.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.teamapt.example/settla',
    'whsec_teamapt_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 21: Starling (GBP) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000021',
    'Starling', 'starling', 'ACTIVE',
    '{"onramp_bps": 27, "offramp_bps": 17, "min_fee_usd": "0.25", "max_fee_usd": "300.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.starling.example/settla',
    'whsec_starling_demo_secret_key_2024',
    9999999999.00000000,
    800000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 22: VFD (NGN) ───────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000022',
    'VFD', 'vfd', 'ACTIVE',
    '{"onramp_bps": 48, "offramp_bps": 32, "min_fee_usd": "1.00", "max_fee_usd": "800.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.vfd.example/settla',
    'whsec_vfd_demo_secret_key_2024',
    9999999999.00000000,
    550000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 23: Nala (GBP) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000023',
    'Nala', 'nala', 'ACTIVE',
    '{"onramp_bps": 38, "offramp_bps": 24, "min_fee_usd": "0.50", "max_fee_usd": "400.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.nala.example/settla',
    'whsec_nala_demo_secret_key_2024',
    9999999999.00000000,
    650000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 24: Wema (NGN) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000024',
    'Wema', 'wema', 'ACTIVE',
    '{"onramp_bps": 44, "offramp_bps": 26, "min_fee_usd": "0.75", "max_fee_usd": "650.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.wema.example/settla',
    'whsec_wema_demo_secret_key_2024',
    9999999999.00000000,
    750000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 25: Sendwave (GBP) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000025',
    'Sendwave', 'sendwave', 'ACTIVE',
    '{"onramp_bps": 32, "offramp_bps": 19, "min_fee_usd": "0.50", "max_fee_usd": "350.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.sendwave.example/settla',
    'whsec_sendwave_demo_secret_key_2024',
    9999999999.00000000,
    900000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 26: Zenith (NGN) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000026',
    'Zenith', 'zenith', 'ACTIVE',
    '{"onramp_bps": 38, "offramp_bps": 22, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.zenith.example/settla',
    'whsec_zenith_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 27: Azimo (GBP) ─────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000027',
    'Azimo', 'azimo', 'ACTIVE',
    '{"onramp_bps": 29, "offramp_bps": 18, "min_fee_usd": "0.25", "max_fee_usd": "250.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.azimo.example/settla',
    'whsec_azimo_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 28: GTBank (NGN) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000028',
    'GTBank', 'gtbank', 'ACTIVE',
    '{"onramp_bps": 36, "offramp_bps": 21, "min_fee_usd": "0.50", "max_fee_usd": "450.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.gtbank.example/settla',
    'whsec_gtbank_demo_secret_key_2024',
    9999999999.00000000,
    850000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 29: TymeBank (GBP) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000029',
    'TymeBank', 'tymebank', 'ACTIVE',
    '{"onramp_bps": 42, "offramp_bps": 28, "min_fee_usd": "0.75", "max_fee_usd": "600.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.tymebank.example/settla',
    'whsec_tymebank_demo_secret_key_2024',
    9999999999.00000000,
    650000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 30: FirstBank (NGN) ─────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000030',
    'FirstBank', 'firstbank', 'ACTIVE',
    '{"onramp_bps": 40, "offramp_bps": 24, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.firstbank.example/settla',
    'whsec_firstbank_demo_secret_key_2024',
    9999999999.00000000,
    900000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 31: Xoom (GBP) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000031',
    'Xoom', 'xoom', 'ACTIVE',
    '{"onramp_bps": 30, "offramp_bps": 20, "min_fee_usd": "0.50", "max_fee_usd": "400.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.xoom.example/settla',
    'whsec_xoom_demo_secret_key_2024',
    9999999999.00000000,
    800000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 32: UBA (NGN) ───────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000032',
    'UBA', 'uba', 'ACTIVE',
    '{"onramp_bps": 46, "offramp_bps": 30, "min_fee_usd": "1.00", "max_fee_usd": "750.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.uba.example/settla',
    'whsec_uba_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 33: Currencycloud (GBP) ─────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000033',
    'Currencycloud', 'currencycloud', 'ACTIVE',
    '{"onramp_bps": 26, "offramp_bps": 16, "min_fee_usd": "0.25", "max_fee_usd": "200.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.currencycloud.example/settla',
    'whsec_currencycloud_demo_secret_key_2024',
    9999999999.00000000,
    1000000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 34: Interswitch (NGN) ───────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000034',
    'Interswitch', 'interswitch', 'ACTIVE',
    '{"onramp_bps": 55, "offramp_bps": 35, "min_fee_usd": "1.50", "max_fee_usd": "1000.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.interswitch.example/settla',
    'whsec_interswitch_demo_secret_key_2024',
    9999999999.00000000,
    500000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 35: TransferGo (GBP) ────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000035',
    'TransferGo', 'transfergo', 'ACTIVE',
    '{"onramp_bps": 34, "offramp_bps": 21, "min_fee_usd": "0.50", "max_fee_usd": "350.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.transfergo.example/settla',
    'whsec_transfergo_demo_secret_key_2024',
    9999999999.00000000,
    750000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 36: Stanbic (NGN) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000036',
    'Stanbic', 'stanbic', 'ACTIVE',
    '{"onramp_bps": 39, "offramp_bps": 23, "min_fee_usd": "0.75", "max_fee_usd": "550.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.stanbic.example/settla',
    'whsec_stanbic_demo_secret_key_2024',
    9999999999.00000000,
    850000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 37: Skrill (GBP) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000037',
    'Skrill', 'skrill', 'ACTIVE',
    '{"onramp_bps": 28, "offramp_bps": 17, "min_fee_usd": "0.25", "max_fee_usd": "250.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.skrill.example/settla',
    'whsec_skrill_demo_secret_key_2024',
    9999999999.00000000,
    600000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 38: FCMB (NGN) ──────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000038',
    'FCMB', 'fcmb', 'ACTIVE',
    '{"onramp_bps": 43, "offramp_bps": 27, "min_fee_usd": "0.75", "max_fee_usd": "600.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.fcmb.example/settla',
    'whsec_fcmb_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 39: Payoneer (GBP) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000039',
    'Payoneer', 'payoneer', 'ACTIVE',
    '{"onramp_bps": 31, "offramp_bps": 19, "min_fee_usd": "0.50", "max_fee_usd": "400.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.payoneer.example/settla',
    'whsec_payoneer_demo_secret_key_2024',
    9999999999.00000000,
    950000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 40: Sterling (NGN) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000040',
    'Sterling', 'sterling', 'ACTIVE',
    '{"onramp_bps": 41, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.sterling.example/settla',
    'whsec_sterling_demo_secret_key_2024',
    9999999999.00000000,
    800000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 41: Afriex (GBP) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000041',
    'Afriex', 'afriex', 'ACTIVE',
    '{"onramp_bps": 37, "offramp_bps": 23, "min_fee_usd": "0.50", "max_fee_usd": "450.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.afriex.example/settla',
    'whsec_afriex_demo_secret_key_2024',
    9999999999.00000000,
    650000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 42: Providus (NGN) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000042',
    'Providus', 'providus', 'ACTIVE',
    '{"onramp_bps": 47, "offramp_bps": 31, "min_fee_usd": "1.00", "max_fee_usd": "700.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.providus.example/settla',
    'whsec_providus_demo_secret_key_2024',
    9999999999.00000000,
    550000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 43: Mukuru (GBP) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000043',
    'Mukuru', 'mukuru', 'ACTIVE',
    '{"onramp_bps": 44, "offramp_bps": 29, "min_fee_usd": "0.75", "max_fee_usd": "550.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.mukuru.example/settla',
    'whsec_mukuru_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 44: Fidelity (NGN) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000044',
    'Fidelity', 'fidelity', 'ACTIVE',
    '{"onramp_bps": 35, "offramp_bps": 20, "min_fee_usd": "0.50", "max_fee_usd": "400.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.fidelity.example/settla',
    'whsec_fidelity_demo_secret_key_2024',
    9999999999.00000000,
    900000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 45: Taptap (GBP) ────────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000045',
    'Taptap', 'taptap', 'ACTIVE',
    '{"onramp_bps": 33, "offramp_bps": 21, "min_fee_usd": "0.50", "max_fee_usd": "350.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.taptap.example/settla',
    'whsec_taptap_demo_secret_key_2024',
    9999999999.00000000,
    750000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 46: Polaris (NGN) ───────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000046',
    'Polaris', 'polaris', 'ACTIVE',
    '{"onramp_bps": 50, "offramp_bps": 33, "min_fee_usd": "1.25", "max_fee_usd": "800.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.polaris.example/settla',
    'whsec_polaris_demo_secret_key_2024',
    9999999999.00000000,
    600000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 47: Cellulant (GBP) ─────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000047',
    'Cellulant', 'cellulant', 'ACTIVE',
    '{"onramp_bps": 39, "offramp_bps": 24, "min_fee_usd": "0.75", "max_fee_usd": "500.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.cellulant.example/settla',
    'whsec_cellulant_demo_secret_key_2024',
    9999999999.00000000,
    850000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 48: UnionBank (NGN) ─────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000048',
    'UnionBank', 'unionbank', 'ACTIVE',
    '{"onramp_bps": 42, "offramp_bps": 26, "min_fee_usd": "0.50", "max_fee_usd": "550.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.unionbank.example/settla',
    'whsec_unionbank_demo_secret_key_2024',
    9999999999.00000000,
    700000.00000000,
    'VERIFIED', now(),
    '{"tier": "growth"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 49: MFS Africa (GBP) ────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000049',
    'MFS Africa', 'mfsafrica', 'ACTIVE',
    '{"onramp_bps": 36, "offramp_bps": 22, "min_fee_usd": "0.50", "max_fee_usd": "450.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.mfsafrica.example/settla',
    'whsec_mfsafrica_demo_secret_key_2024',
    9999999999.00000000,
    950000.00000000,
    'VERIFIED', now(),
    '{"tier": "standard"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── Tenant 50: Heritage (NGN) ──────────────────────────────────────────────
INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
    webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
    kyb_status, kyb_verified_at, metadata)
VALUES (
    '50000000-0000-0000-0000-000000000050',
    'Heritage', 'heritage', 'ACTIVE',
    '{"onramp_bps": 45, "offramp_bps": 28, "min_fee_usd": "0.75", "max_fee_usd": "650.00"}'::jsonb,
    'PREFUNDED',
    'https://webhooks.heritage.example/settla',
    'whsec_heritage_demo_secret_key_2024',
    9999999999.00000000,
    800000.00000000,
    'VERIFIED', now(),
    '{"tier": "enterprise"}'::jsonb
) ON CONFLICT (slug) DO NOTHING;

-- ── API keys (SHA-256 hashes of raw keys) ──────────────────────────────────
INSERT INTO api_keys (tenant_id, key_hash, key_prefix, environment, name) VALUES
    ('a0000000-0000-0000-0000-000000000001', encode(sha256('sk_live_lemfi_demo_key'::bytea), 'hex'),        'sk_live_', 'LIVE', 'Lemfi Production Key'),
    ('b0000000-0000-0000-0000-000000000002', encode(sha256('sk_live_fincra_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Fincra Production Key'),
    ('c0000000-0000-0000-0000-000000000003', encode(sha256('sk_live_paystack_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Paystack Production Key'),
    ('d0000000-0000-0000-0000-000000000004', encode(sha256('sk_live_flutterwave_demo_key'::bytea), 'hex'),  'sk_live_', 'LIVE', 'Flutterwave Production Key'),
    ('e0000000-0000-0000-0000-000000000005', encode(sha256('sk_live_chipper_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'Chipper Production Key'),
    ('f0000000-0000-0000-0000-000000000006', encode(sha256('sk_live_moniepoint_demo_key'::bytea), 'hex'),   'sk_live_', 'LIVE', 'Moniepoint Production Key'),
    ('10000000-0000-0000-0000-000000000007', encode(sha256('sk_live_kuda_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'Kuda Production Key'),
    ('20000000-0000-0000-0000-000000000008', encode(sha256('sk_live_opay_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'OPay Production Key'),
    ('30000000-0000-0000-0000-000000000009', encode(sha256('sk_live_ecobank_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'Ecobank Production Key'),
    ('40000000-0000-0000-0000-000000000010', encode(sha256('sk_live_access_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Access Production Key'),
    ('50000000-0000-0000-0000-000000000011', encode(sha256('sk_live_palmpay_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'PalmPay Production Key'),
    ('50000000-0000-0000-0000-000000000012', encode(sha256('sk_live_carbon_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Carbon Production Key'),
    ('50000000-0000-0000-0000-000000000013', encode(sha256('sk_live_fairmoney_demo_key'::bytea), 'hex'),    'sk_live_', 'LIVE', 'FairMoney Production Key'),
    ('50000000-0000-0000-0000-000000000014', encode(sha256('sk_live_wise_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'Wise Production Key'),
    ('50000000-0000-0000-0000-000000000015', encode(sha256('sk_live_worldremit_demo_key'::bytea), 'hex'),   'sk_live_', 'LIVE', 'WorldRemit Production Key'),
    ('50000000-0000-0000-0000-000000000016', encode(sha256('sk_live_monzo_demo_key'::bytea), 'hex'),        'sk_live_', 'LIVE', 'Monzo Production Key'),
    ('50000000-0000-0000-0000-000000000017', encode(sha256('sk_live_revolut_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'Revolut Production Key'),
    ('50000000-0000-0000-0000-000000000018', encode(sha256('sk_live_paga_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'Paga Production Key'),
    ('50000000-0000-0000-0000-000000000019', encode(sha256('sk_live_remitly_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'Remitly Production Key'),
    ('50000000-0000-0000-0000-000000000020', encode(sha256('sk_live_teamapt_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'TeamApt Production Key'),
    ('50000000-0000-0000-0000-000000000021', encode(sha256('sk_live_starling_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Starling Production Key'),
    ('50000000-0000-0000-0000-000000000022', encode(sha256('sk_live_vfd_demo_key'::bytea), 'hex'),          'sk_live_', 'LIVE', 'VFD Production Key'),
    ('50000000-0000-0000-0000-000000000023', encode(sha256('sk_live_nala_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'Nala Production Key'),
    ('50000000-0000-0000-0000-000000000024', encode(sha256('sk_live_wema_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'Wema Production Key'),
    ('50000000-0000-0000-0000-000000000025', encode(sha256('sk_live_sendwave_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Sendwave Production Key'),
    ('50000000-0000-0000-0000-000000000026', encode(sha256('sk_live_zenith_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Zenith Production Key'),
    ('50000000-0000-0000-0000-000000000027', encode(sha256('sk_live_azimo_demo_key'::bytea), 'hex'),        'sk_live_', 'LIVE', 'Azimo Production Key'),
    ('50000000-0000-0000-0000-000000000028', encode(sha256('sk_live_gtbank_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'GTBank Production Key'),
    ('50000000-0000-0000-0000-000000000029', encode(sha256('sk_live_tymebank_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'TymeBank Production Key'),
    ('50000000-0000-0000-0000-000000000030', encode(sha256('sk_live_firstbank_demo_key'::bytea), 'hex'),    'sk_live_', 'LIVE', 'FirstBank Production Key'),
    ('50000000-0000-0000-0000-000000000031', encode(sha256('sk_live_xoom_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'Xoom Production Key'),
    ('50000000-0000-0000-0000-000000000032', encode(sha256('sk_live_uba_demo_key'::bytea), 'hex'),          'sk_live_', 'LIVE', 'UBA Production Key'),
    ('50000000-0000-0000-0000-000000000033', encode(sha256('sk_live_currencycloud_demo_key'::bytea), 'hex'),'sk_live_', 'LIVE', 'Currencycloud Production Key'),
    ('50000000-0000-0000-0000-000000000034', encode(sha256('sk_live_interswitch_demo_key'::bytea), 'hex'),  'sk_live_', 'LIVE', 'Interswitch Production Key'),
    ('50000000-0000-0000-0000-000000000035', encode(sha256('sk_live_transfergo_demo_key'::bytea), 'hex'),   'sk_live_', 'LIVE', 'TransferGo Production Key'),
    ('50000000-0000-0000-0000-000000000036', encode(sha256('sk_live_stanbic_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'Stanbic Production Key'),
    ('50000000-0000-0000-0000-000000000037', encode(sha256('sk_live_skrill_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Skrill Production Key'),
    ('50000000-0000-0000-0000-000000000038', encode(sha256('sk_live_fcmb_demo_key'::bytea), 'hex'),         'sk_live_', 'LIVE', 'FCMB Production Key'),
    ('50000000-0000-0000-0000-000000000039', encode(sha256('sk_live_payoneer_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Payoneer Production Key'),
    ('50000000-0000-0000-0000-000000000040', encode(sha256('sk_live_sterling_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Sterling Production Key'),
    ('50000000-0000-0000-0000-000000000041', encode(sha256('sk_live_afriex_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Afriex Production Key'),
    ('50000000-0000-0000-0000-000000000042', encode(sha256('sk_live_providus_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Providus Production Key'),
    ('50000000-0000-0000-0000-000000000043', encode(sha256('sk_live_mukuru_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Mukuru Production Key'),
    ('50000000-0000-0000-0000-000000000044', encode(sha256('sk_live_fidelity_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Fidelity Production Key'),
    ('50000000-0000-0000-0000-000000000045', encode(sha256('sk_live_taptap_demo_key'::bytea), 'hex'),       'sk_live_', 'LIVE', 'Taptap Production Key'),
    ('50000000-0000-0000-0000-000000000046', encode(sha256('sk_live_polaris_demo_key'::bytea), 'hex'),      'sk_live_', 'LIVE', 'Polaris Production Key'),
    ('50000000-0000-0000-0000-000000000047', encode(sha256('sk_live_cellulant_demo_key'::bytea), 'hex'),    'sk_live_', 'LIVE', 'Cellulant Production Key'),
    ('50000000-0000-0000-0000-000000000048', encode(sha256('sk_live_unionbank_demo_key'::bytea), 'hex'),    'sk_live_', 'LIVE', 'UnionBank Production Key'),
    ('50000000-0000-0000-0000-000000000049', encode(sha256('sk_live_mfsafrica_demo_key'::bytea), 'hex'),    'sk_live_', 'LIVE', 'MFS Africa Production Key'),
    ('50000000-0000-0000-0000-000000000050', encode(sha256('sk_live_heritage_demo_key'::bytea), 'hex'),     'sk_live_', 'LIVE', 'Heritage Production Key')
ON CONFLICT (key_hash) DO NOTHING;

-- ── Portal users for seed tenants ──────────────────────────────────────────
-- Password for all seed users: "settla-demo-2024" (bcrypt cost 12)
-- bcrypt hash: $2a$12$LJ3m4ys4Kz5VHjKXx5Wz7OQyBz6u1q7XbZTsWFsLBqLvJrXYKvPa
INSERT INTO portal_users (tenant_id, email, password_hash, display_name, role, email_verified)
VALUES
    ('a0000000-0000-0000-0000-000000000001', 'admin@lemfi.example', '$2a$12$LJ3m4ys4Kz5VHjKXx5Wz7OQyBz6u1q7XbZTsWFsLBqLvJrXYKvPa', 'Lemfi Admin', 'OWNER', true),
    ('b0000000-0000-0000-0000-000000000002', 'admin@fincra.example', '$2a$12$LJ3m4ys4Kz5VHjKXx5Wz7OQyBz6u1q7XbZTsWFsLBqLvJrXYKvPa', 'Fincra Admin', 'OWNER', true)
ON CONFLICT (email) DO NOTHING;

-- ── Additional portal users for test tenants (TestFintech1–10) ────────────
-- Password for all test users: "password"
-- bcrypt hash: $2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy
INSERT INTO portal_users (id, tenant_id, email, password_hash, display_name, role, email_verified)
VALUES
    ('c0000000-0000-0000-0000-000000000003', 'c0000000-0000-0000-0000-000000000003', 'admin@testfintech1.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech1 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000004', 'd0000000-0000-0000-0000-000000000004', 'admin@testfintech2.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech2 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000005', 'e0000000-0000-0000-0000-000000000005', 'admin@testfintech3.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech3 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000006', 'f0000000-0000-0000-0000-000000000006', 'admin@testfintech4.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech4 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000007', '10000000-0000-0000-0000-000000000007', 'admin@testfintech5.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech5 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000008', '20000000-0000-0000-0000-000000000008', 'admin@testfintech6.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech6 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000009', '30000000-0000-0000-0000-000000000009', 'admin@testfintech7.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech7 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000010', '40000000-0000-0000-0000-000000000010', 'admin@testfintech8.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech8 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000011', '50000000-0000-0000-0000-000000000011', 'admin@testfintech9.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech9 Admin', 'OWNER', true),
    ('c0000000-0000-0000-0000-000000000012', '50000000-0000-0000-0000-000000000012', 'admin@testfintech10.example', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy', 'TestFintech10 Admin', 'OWNER', true)
ON CONFLICT (email) DO NOTHING;
