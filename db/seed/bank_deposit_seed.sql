-- Settla Bank Deposit Seed Data
-- Banking partners, virtual accounts, and tenant bank deposit configuration

-- ── Banking partner ────────────────────────────────────────────────────────

INSERT INTO banking_partners (id, name, webhook_secret, supported_currencies, is_active, metadata)
VALUES (
    'c0000000-0000-0000-0000-000000000003',
    'mock_settla_bank',
    'whsec_bank_mock_secret_2026',
    ARRAY['USD', 'GBP', 'NGN'],
    true,
    '{"environment": "sandbox", "api_version": "v1"}'::jsonb
) ON CONFLICT (id) DO NOTHING;

-- ── Virtual accounts: 100 per currency per seed tenant ─────────────────────
-- Lemfi (a0000000-...-000000000001) and Fincra (b0000000-...-000000000002)

DO $$
DECLARE
    v_tenant_id UUID;
    v_tenant_slug TEXT;
    v_currency TEXT;
    v_i INT;
    v_account_number TEXT;
BEGIN
    FOR v_tenant_id, v_tenant_slug IN
        VALUES
            ('a0000000-0000-0000-0000-000000000001'::uuid, 'lemfi'),
            ('b0000000-0000-0000-0000-000000000002'::uuid, 'fincra')
    LOOP
        FOR v_currency IN
            VALUES ('USD'), ('GBP'), ('NGN')
        LOOP
            FOR v_i IN 1..100 LOOP
                v_account_number := 'VA-' || v_currency || '-' || v_tenant_slug || '-' || LPAD(v_i::TEXT, 3, '0');

                INSERT INTO virtual_account_pool (
                    tenant_id, banking_partner_id, account_number, account_name,
                    sort_code, iban, currency, account_type, available
                ) VALUES (
                    v_tenant_id,
                    'c0000000-0000-0000-0000-000000000003',
                    v_account_number,
                    v_tenant_slug || ' Virtual Account ' || v_i,
                    CASE v_currency WHEN 'GBP' THEN '000000' ELSE '' END,
                    CASE v_currency WHEN 'GBP' THEN 'GB00MOCK' || LPAD(v_i::TEXT, 10, '0') ELSE '' END,
                    v_currency,
                    'TEMPORARY',
                    true
                ) ON CONFLICT (account_number) DO NOTHING;
            END LOOP;
        END LOOP;
    END LOOP;
END;
$$;

-- ── Tenant bank deposit configuration ──────────────────────────────────────

-- Lemfi: enable bank deposits, USD/GBP/NGN
UPDATE tenants SET
    bank_deposits_enabled = true,
    default_banking_partner = 'c0000000-0000-0000-0000-000000000003',
    bank_supported_currencies = ARRAY['USD', 'GBP', 'NGN'],
    default_mismatch_policy = 'REJECT',
    bank_default_session_ttl_secs = 3600,
    fee_schedule = fee_schedule || '{"bank_collection_bps": 30, "bank_collection_min_fee_usd": "0.50", "bank_collection_max_fee_usd": "250.00"}'::jsonb
WHERE slug = 'lemfi';

-- Fincra: enable bank deposits, USD/GBP/NGN
UPDATE tenants SET
    bank_deposits_enabled = true,
    default_banking_partner = 'c0000000-0000-0000-0000-000000000003',
    bank_supported_currencies = ARRAY['USD', 'GBP', 'NGN'],
    default_mismatch_policy = 'ACCEPT',
    bank_default_session_ttl_secs = 7200,
    fee_schedule = fee_schedule || '{"bank_collection_bps": 25, "bank_collection_min_fee_usd": "1.00", "bank_collection_max_fee_usd": "500.00"}'::jsonb
WHERE slug = 'fincra';

-- Paystack: enable bank deposits, USD/NGN
UPDATE tenants SET
    bank_deposits_enabled = true,
    default_banking_partner = 'c0000000-0000-0000-0000-000000000003',
    bank_supported_currencies = ARRAY['USD', 'NGN'],
    default_mismatch_policy = 'REJECT',
    bank_default_session_ttl_secs = 3600,
    fee_schedule = fee_schedule || '{"bank_collection_bps": 35, "bank_collection_min_fee_usd": "0.50", "bank_collection_max_fee_usd": "300.00"}'::jsonb
WHERE slug = 'paystack';
