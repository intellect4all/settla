-- Settla Treasury DB Seed Data
-- Pre-funded positions for all 50 demo tenants
-- GBP tenants (odd): Lemfi, Paystack, Chipper, Kuda, Ecobank, PalmPay, FairMoney, WorldRemit, Revolut, Remitly, Starling, Nala, Sendwave, Azimo, TymeBank, Xoom, Currencycloud, TransferGo, Skrill, Payoneer, Afriex, Mukuru, Taptap, Cellulant, MFS Africa
-- NGN tenants (even): Fincra, Flutterwave, Moniepoint, OPay, Access, Carbon, Wise, Monzo, Paga, TeamApt, VFD, Wema, Zenith, GTBank, FirstBank, UBA, Interswitch, Stanbic, FCMB, Sterling, Providus, Fidelity, Polaris, UnionBank, Heritage

-- ── Lemfi (GBP) ────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('a0000000-0000-0000-0000-000000000001', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('a0000000-0000-0000-0000-000000000001', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Fincra (NGN) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('b0000000-0000-0000-0000-000000000002', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('b0000000-0000-0000-0000-000000000002', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('b0000000-0000-0000-0000-000000000002', 'USD',  'bank:usd',   999999999.00000000, 0, 30000.00000000,    500000.00000000),
    ('b0000000-0000-0000-0000-000000000002', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Paystack (GBP) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('c0000000-0000-0000-0000-000000000003', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('c0000000-0000-0000-0000-000000000003', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('c0000000-0000-0000-0000-000000000003', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Flutterwave (NGN) ──────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('d0000000-0000-0000-0000-000000000004', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('d0000000-0000-0000-0000-000000000004', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('d0000000-0000-0000-0000-000000000004', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Chipper (GBP) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('e0000000-0000-0000-0000-000000000005', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('e0000000-0000-0000-0000-000000000005', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('e0000000-0000-0000-0000-000000000005', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Moniepoint (NGN) ───────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('f0000000-0000-0000-0000-000000000006', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('f0000000-0000-0000-0000-000000000006', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('f0000000-0000-0000-0000-000000000006', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Kuda (GBP) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('10000000-0000-0000-0000-000000000007', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('10000000-0000-0000-0000-000000000007', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('10000000-0000-0000-0000-000000000007', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── OPay (NGN) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('20000000-0000-0000-0000-000000000008', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('20000000-0000-0000-0000-000000000008', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('20000000-0000-0000-0000-000000000008', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Ecobank (GBP) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('30000000-0000-0000-0000-000000000009', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('30000000-0000-0000-0000-000000000009', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('30000000-0000-0000-0000-000000000009', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Access (NGN) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('40000000-0000-0000-0000-000000000010', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('40000000-0000-0000-0000-000000000010', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('40000000-0000-0000-0000-000000000010', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── PalmPay (GBP) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000011', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000011', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000011', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Carbon (NGN) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000012', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000012', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000012', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── FairMoney (GBP) ────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000013', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000013', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000013', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Wise (NGN) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000014', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000014', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000014', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── WorldRemit (GBP) ───────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000015', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000015', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000015', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Monzo (NGN) ────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000016', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000016', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000016', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Revolut (GBP) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000017', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000017', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000017', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Paga (NGN) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000018', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000018', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000018', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Remitly (GBP) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000019', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000019', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000019', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── TeamApt (NGN) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000020', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000020', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000020', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Starling (GBP) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000021', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000021', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000021', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── VFD (NGN) ──────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000022', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000022', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000022', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Nala (GBP) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000023', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000023', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000023', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Wema (NGN) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000024', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000024', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000024', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Sendwave (GBP) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000025', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000025', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000025', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Zenith (NGN) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000026', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000026', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000026', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Azimo (GBP) ────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000027', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000027', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000027', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── GTBank (NGN) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000028', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000028', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000028', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── TymeBank (GBP) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000029', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000029', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000029', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── FirstBank (NGN) ────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000030', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000030', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000030', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Xoom (GBP) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000031', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000031', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000031', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── UBA (NGN) ──────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000032', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000032', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000032', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Currencycloud (GBP) ────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000033', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000033', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000033', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Interswitch (NGN) ──────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000034', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000034', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000034', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── TransferGo (GBP) ───────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000035', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000035', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000035', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Stanbic (NGN) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000036', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000036', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000036', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Skrill (GBP) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000037', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000037', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000037', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── FCMB (NGN) ─────────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000038', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000038', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000038', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Payoneer (GBP) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000039', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000039', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000039', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Sterling (NGN) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000040', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000040', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000040', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Afriex (GBP) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000041', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000041', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000041', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Providus (NGN) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000042', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000042', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000042', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Mukuru (GBP) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000043', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000043', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000043', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Fidelity (NGN) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000044', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000044', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000044', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Taptap (GBP) ───────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000045', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000045', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000045', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Polaris (NGN) ──────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000046', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000046', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000046', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Cellulant (GBP) ────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000047', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000047', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000047', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── UnionBank (NGN) ────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000048', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000048', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000048', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── MFS Africa (GBP) ───────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000049', 'GBP',  'bank:gbp',   999999999.00000000, 0, 50000.00000000,  750000.00000000),
    ('50000000-0000-0000-0000-000000000049', 'USDT', 'chain:tron', 999999999.00000000, 0, 20000.00000000,  300000.00000000),
    ('50000000-0000-0000-0000-000000000049', 'NGN',  'bank:ngn',   999999999.00000000, 0, 10000000.00000000, 150000000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;

-- ── Heritage (NGN) ─────────────────────────────────────────────────────────
INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance) VALUES
    ('50000000-0000-0000-0000-000000000050', 'NGN',  'bank:ngn',   999999999.00000000, 0, 50000000.00000000, 750000000.00000000),
    ('50000000-0000-0000-0000-000000000050', 'USDT', 'chain:tron', 999999999.00000000, 0, 50000.00000000,    750000.00000000),
    ('50000000-0000-0000-0000-000000000050', 'GBP',  'bank:gbp',   999999999.00000000, 0, 10000.00000000,    200000.00000000)
ON CONFLICT (tenant_id, currency, location) DO NOTHING;
