-- Settla Crypto Deposit Seed Data
-- Tokens, block checkpoints, and tenant crypto configuration

-- ── Tokens ───────────────────────────────────────────────────────────────────

-- Tron TRC-20 tokens
INSERT INTO tokens (id, chain, symbol, contract_address, decimals, is_active)
VALUES (
    'c0000000-0000-0000-0000-000000000001',
    'tron', 'USDT', 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t', 6, true
) ON CONFLICT (chain, symbol) DO NOTHING;

INSERT INTO tokens (id, chain, symbol, contract_address, decimals, is_active)
VALUES (
    'c0000000-0000-0000-0000-000000000002',
    'tron', 'USDC', 'TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8', 6, true
) ON CONFLICT (chain, symbol) DO NOTHING;

-- Ethereum ERC-20 tokens
INSERT INTO tokens (id, chain, symbol, contract_address, decimals, is_active)
VALUES (
    'c0000000-0000-0000-0000-000000000003',
    'ethereum', 'USDT', '0xdAC17F958D2ee523a2206206994597C13D831ec7', 6, true
) ON CONFLICT (chain, symbol) DO NOTHING;

INSERT INTO tokens (id, chain, symbol, contract_address, decimals, is_active)
VALUES (
    'c0000000-0000-0000-0000-000000000004',
    'ethereum', 'USDC', '0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48', 6, true
) ON CONFLICT (chain, symbol) DO NOTHING;

-- Base ERC-20 tokens
INSERT INTO tokens (id, chain, symbol, contract_address, decimals, is_active)
VALUES (
    'c0000000-0000-0000-0000-000000000005',
    'base', 'USDT', '0xfde4C96c8593536E31F229EA8f37b2ADa2699bb2', 6, true
) ON CONFLICT (chain, symbol) DO NOTHING;

INSERT INTO tokens (id, chain, symbol, contract_address, decimals, is_active)
VALUES (
    'c0000000-0000-0000-0000-000000000006',
    'base', 'USDC', '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', 6, true
) ON CONFLICT (chain, symbol) DO NOTHING;

-- ── Block checkpoints (starting points for chain monitors) ───────────────────

INSERT INTO block_checkpoints (chain, block_number, block_hash)
VALUES ('tron', 0, '')
ON CONFLICT (chain) DO NOTHING;

INSERT INTO block_checkpoints (chain, block_number, block_hash)
VALUES ('ethereum', 0, '')
ON CONFLICT (chain) DO NOTHING;

INSERT INTO block_checkpoints (chain, block_number, block_hash)
VALUES ('base', 0, '')
ON CONFLICT (chain) DO NOTHING;

-- ── Crypto address pool ─────────────────────────────────────────────────────
-- Pre-derived HD wallet addresses for deposit sessions.
-- 100 per (tenant, chain) combination for the seed tenants.

DO $$
DECLARE
    v_tenant_id UUID;
    v_chain TEXT;
    v_i INT;
    v_address TEXT;
BEGIN
    FOR v_tenant_id IN
        VALUES
            ('a0000000-0000-0000-0000-000000000001'::uuid),
            ('b0000000-0000-0000-0000-000000000002'::uuid)
    LOOP
        FOR v_chain IN
            VALUES ('tron'), ('ethereum'), ('base')
        LOOP
            FOR v_i IN 1..100 LOOP
                v_address := CASE v_chain
                    WHEN 'tron'     THEN 'TRX' || md5(v_tenant_id::text || v_chain || v_i::text)
                    WHEN 'ethereum' THEN '0x' || md5(v_tenant_id::text || v_chain || v_i::text)
                    WHEN 'base'     THEN '0xB' || substring(md5(v_tenant_id::text || v_chain || v_i::text), 2)
                END;

                INSERT INTO crypto_address_pool (
                    tenant_id, chain, address, derivation_index, dispensed
                ) VALUES (
                    v_tenant_id, v_chain, v_address, v_i, false
                ) ON CONFLICT (chain, address) DO NOTHING;
            END LOOP;
        END LOOP;
    END LOOP;
END;
$$;

-- ── Tenant crypto configuration ──────────────────────────────────────────────

-- Lemfi: enable crypto deposits on Tron
UPDATE tenants SET
    crypto_enabled = true,
    default_settlement_pref = 'AUTO_CONVERT',
    supported_chains = ARRAY['tron'],
    min_confirmations_tron = 19,
    payment_tolerance_bps = 50,
    default_session_ttl_secs = 3600,
    fee_schedule = fee_schedule || '{"crypto_collection_bps": 30, "crypto_collection_max_fee_usd": "250.00"}'::jsonb
WHERE slug = 'lemfi';

-- Fincra: enable crypto deposits on Tron + Ethereum
UPDATE tenants SET
    crypto_enabled = true,
    default_settlement_pref = 'HOLD',
    supported_chains = ARRAY['tron', 'ethereum'],
    min_confirmations_tron = 19,
    min_confirmations_eth = 12,
    payment_tolerance_bps = 100,
    default_session_ttl_secs = 7200,
    fee_schedule = fee_schedule || '{"crypto_collection_bps": 25, "crypto_collection_max_fee_usd": "500.00"}'::jsonb
WHERE slug = 'fincra';

-- Paystack: enable crypto deposits on Tron
UPDATE tenants SET
    crypto_enabled = true,
    default_settlement_pref = 'AUTO_CONVERT',
    supported_chains = ARRAY['tron'],
    min_confirmations_tron = 19,
    payment_tolerance_bps = 50,
    default_session_ttl_secs = 3600,
    fee_schedule = fee_schedule || '{"crypto_collection_bps": 35, "crypto_collection_max_fee_usd": "300.00"}'::jsonb
WHERE slug = 'paystack';
