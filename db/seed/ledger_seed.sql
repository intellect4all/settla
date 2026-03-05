-- Settla Ledger DB Seed Data
-- System accounts + per-tenant accounts for Lemfi and Fincra

-- System accounts (no tenant)
INSERT INTO accounts (tenant_id, code, name, type, currency, normal_balance) VALUES
    (NULL, 'assets:crypto:usdt:tron',       'USDT on Tron',            'ASSET',   'USDT', 'DEBIT'),
    (NULL, 'assets:crypto:usdc:solana',     'USDC on Solana',          'ASSET',   'USDC', 'DEBIT'),
    (NULL, 'assets:settlement:in_transit',  'Settlement In Transit',   'ASSET',   'USD',  'DEBIT'),
    (NULL, 'expenses:network:tron:gas',     'Tron Gas Fees',           'EXPENSE', 'USDT', 'DEBIT'),
    (NULL, 'expenses:network:solana:gas',   'Solana Gas Fees',         'EXPENSE', 'USDC', 'DEBIT'),
    (NULL, 'expenses:provider:onramp',      'On-Ramp Provider Fees',   'EXPENSE', 'USD',  'DEBIT'),
    (NULL, 'expenses:provider:offramp',     'Off-Ramp Provider Fees',  'EXPENSE', 'USD',  'DEBIT')
ON CONFLICT (code) DO NOTHING;

-- Lemfi accounts
INSERT INTO accounts (tenant_id, code, name, type, currency, normal_balance) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'tenant:lemfi:assets:bank:gbp:clearing',        'Lemfi GBP Clearing',       'ASSET',     'GBP',  'DEBIT'),
    ('a0000000-0000-0000-0000-000000000001', 'tenant:lemfi:assets:crypto:usdt:tron',          'Lemfi USDT on Tron',       'ASSET',     'USDT', 'DEBIT'),
    ('a0000000-0000-0000-0000-000000000001', 'tenant:lemfi:assets:bank:ngn:collection',       'Lemfi NGN Collection',     'ASSET',     'NGN',  'DEBIT'),
    ('a0000000-0000-0000-0000-000000000001', 'tenant:lemfi:liabilities:customer:pending',     'Lemfi Customer Pending',   'LIABILITY', 'GBP',  'CREDIT'),
    ('a0000000-0000-0000-0000-000000000001', 'tenant:lemfi:liabilities:payable:recipient',    'Lemfi Payable Recipient',  'LIABILITY', 'NGN',  'CREDIT'),
    ('a0000000-0000-0000-0000-000000000001', 'tenant:lemfi:revenue:fees:settlement',          'Lemfi Settlement Fees',    'REVENUE',   'USD',  'CREDIT')
ON CONFLICT (code) DO NOTHING;

-- Fincra accounts
INSERT INTO accounts (tenant_id, code, name, type, currency, normal_balance) VALUES
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:assets:bank:ngn:collection',      'Fincra NGN Collection',    'ASSET',     'NGN',  'DEBIT'),
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:assets:crypto:usdt:tron',          'Fincra USDT on Tron',      'ASSET',     'USDT', 'DEBIT'),
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:assets:bank:usd:mercury',          'Fincra USD Mercury',       'ASSET',     'USD',  'DEBIT'),
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:assets:bank:gbp:clearing',         'Fincra GBP Clearing',      'ASSET',     'GBP',  'DEBIT'),
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:liabilities:customer:pending',     'Fincra Customer Pending',  'LIABILITY', 'NGN',  'CREDIT'),
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:liabilities:payable:recipient',    'Fincra Payable Recipient', 'LIABILITY', 'GBP',  'CREDIT'),
    ('b0000000-0000-0000-0000-000000000002', 'tenant:fincra:revenue:fees:settlement',          'Fincra Settlement Fees',   'REVENUE',   'USD',  'CREDIT')
ON CONFLICT (code) DO NOTHING;

-- Seed balance snapshots (all zero — TigerBeetle is the authority)
INSERT INTO balance_snapshots (account_id, balance, version)
SELECT id, 0, 0 FROM accounts
ON CONFLICT (account_id) DO NOTHING;
