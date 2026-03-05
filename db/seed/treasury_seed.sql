-- Settla Treasury DB Seed Data
-- Pre-funded positions for Lemfi and Fincra demo tenants

-- Lemfi positions (location = bank:{currency_lowercase} to match engine convention)
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'GBP',  'bank:gbp',         500000.00000000,     0, 50000.00000000,    750000.00000000),
    ('a0000000-0000-0000-0000-000000000001', 'USDT', 'chain:tron',       200000.00000000,     0, 20000.00000000,    300000.00000000),
    ('a0000000-0000-0000-0000-000000000001', 'NGN',  'bank:ngn',         100000000.00000000,  0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- Fincra positions
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('b0000000-0000-0000-0000-000000000002', 'NGN',  'bank:ngn',         500000000.00000000,  0, 50000000.00000000,  750000000.00000000),
    ('b0000000-0000-0000-0000-000000000002', 'USDT', 'chain:tron',       500000.00000000,     0, 50000.00000000,     750000.00000000),
    ('b0000000-0000-0000-0000-000000000002', 'USD',  'bank:usd',         300000.00000000,     0, 30000.00000000,     500000.00000000),
    ('b0000000-0000-0000-0000-000000000002', 'GBP',  'bank:gbp',         100000.00000000,     0, 10000.00000000,     200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;
