-- db/seeds/development.sql
-- Development Seed Data for MyCaffe Core POS
-- Multi-tenant sample data with Indonesian Rupiah whole integer amounts and Basis Points (BPS)
-- STRICTLY ISOLATED TO LOCAL / CI DEVELOPMENT ONLY. NEVER EXECUTE IN PRODUCTION!
-- Using strictly valid RFC 4122 hexadecimal UUIDs and generic multi-tenant identities.

-- 1. STORES (TENANTS)
INSERT INTO stores (id, slug, name, address, phone, timezone, is_active, order_prefix, queue_prefix, default_billing_mode, shift_mode, enabled_payment_methods, tax_bps, service_bps, qris_mdr_bps, currency)
VALUES 
    ('11111111-1111-4111-8111-111111111111', 'central-cafe', 'MyCaffe Demo Store - Central', 'Jl. Sudirman No. 101, Jakarta', '081234567890', 'Asia/Jakarta', true, 'ORD', 'A', 'pay_first', 'single_auto', '["qris","bank_transfer","gopay","shopeepay","cash","static_qris","edc"]'::jsonb, 1000, 500, 70, 'IDR'),
    ('22222222-2222-4222-8222-222222222222', 'express-counter', 'MyCaffe Demo Store - Express Counter', 'Lobby Tower A, Jakarta', '081298765432', 'Asia/Jakarta', true, 'EXP', 'B', 'pay_first', 'single_auto', '["qris","gopay","cash"]'::jsonb, 1000, 0, 70, 'IDR')
ON CONFLICT (id) DO NOTHING;

-- 2. USERS (STAFF - 4 Roles: owner, cashier, barista, kitchen)
-- Development-only PIN for every seeded user: 482913 (argon2id, ADR-003 D2). Login with
-- store_slug "central-cafe" or "express-counter" + the email below. Never seed PINs in production.
INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
VALUES
    ('a0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'abdulaziz543333@gmail.com', 'Alexander (Owner)', 'owner', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'kitchen.central@mycaffe.local', 'Sarah Jenkins (Kitchen)', 'kitchen', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'cashier.central@mycaffe.local', 'Maya Putri (Cashier)', 'cashier', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'barista.central@mycaffe.local', 'Doni Pratama (Barista)', 'barista', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW()),
    ('a0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'cashier.express@mycaffe.local', 'Lestari Indah (Cashier)', 'cashier', '$argon2id$v=19$m=19456,t=2,p=1$asaQjWwUQ7vf35jdVNPtlw$giPi5ZwfklHxgmJ2rcCuvkNw3zn5mEfDTdbaGvy+sFY', true, NOW())
ON CONFLICT (id) DO UPDATE SET email = EXCLUDED.email, pin_hash = EXCLUDED.pin_hash, pin_set_at = COALESCE(users.pin_set_at, NOW());

-- 3. CATEGORIES
INSERT INTO categories (id, store_id, name, display_order, is_active)
VALUES
    ('c0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Signature Series', 1, true),
    ('c0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Espresso & Coffee', 2, true),
    ('c0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Manual Brew & Specialty', 3, true),
    ('c0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'Artisan Pastry & Bakery', 4, true),
    ('c0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'Quick Grab Coffee', 1, true)
ON CONFLICT (id) DO NOTHING;

-- 4. PRODUCTS (Whole integer IDR, e.g. Rp 24.000 = 24000)
INSERT INTO products (id, store_id, category_id, name, description, base_price, is_available, track_stock, stock_quantity)
VALUES
    ('d0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Classic Iced Latte', 'Double espresso shot with fresh dairy milk and artisanal sugar', 24000, true, false, 0),
    ('d0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Caffe Latte', 'Espresso with steamed fresh milk and microfoam', 26000, true, false, 0),
    ('d0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Americano (Double Shot)', 'Double ristretto poured over filtered water', 20000, true, false, 0),
    ('d0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'V60 Single Origin Gayo', 'Filter brewed single origin with floral citrus notes', 32000, true, true, 45),
    ('d0000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Butter Croissant Artisanal', 'Freshly baked flaky French butter croissant', 24000, true, true, 18),
    ('d0000006-0000-4000-8000-000000000006', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Express Cold Brew', 'Steeped for 18 hours, rich and smooth finish', 22000, true, false, 0)
ON CONFLICT (id) DO NOTHING;

-- 5. MODIFIER GROUPS, OPTIONS & 1-LEVEL NESTED SUB-ADDONS
INSERT INTO modifier_groups (id, store_id, name, selection_type, is_required, min_select, max_select, display_order, is_active)
VALUES
    ('10000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Milk Selection', 'single', true, 1, 1, 1, true),
    ('10000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Sweetness Level', 'single', true, 1, 1, 2, true),
    ('10000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Plant Milk Preparation (Sub-Addon)', 'single', true, 1, 1, 3, true)
ON CONFLICT (id) DO NOTHING;

INSERT INTO modifier_options (id, store_id, group_id, name, price_delta, is_available, display_order)
VALUES
    ('20000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '10000001-0000-4000-8000-000000000001', 'Fresh Dairy Milk', 0, true, 1),
    ('20000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', '10000001-0000-4000-8000-000000000001', 'Oat Milk Barista Blend', 8000, true, 2),
    ('20000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', '10000002-0000-4000-8000-000000000002', 'Normal Sugar (100%)', 0, true, 1),
    ('20000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', '10000002-0000-4000-8000-000000000002', 'Less Sugar (50%)', 0, true, 2),
    ('20000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', '10000003-0000-4000-8000-000000000003', 'Standard Pour', 0, true, 1),
    ('20000006-0000-4000-8000-000000000006', '11111111-1111-4111-8111-111111111111', '10000003-0000-4000-8000-000000000003', 'Whipped Oat Cold Foam', 4000, true, 2)
ON CONFLICT (id) DO NOTHING;

INSERT INTO product_modifier_groups (store_id, product_id, group_id, display_order)
VALUES
    ('11111111-1111-4111-8111-111111111111', 'd0000001-0000-4000-8000-000000000001', '10000001-0000-4000-8000-000000000001', 1),
    ('11111111-1111-4111-8111-111111111111', 'd0000001-0000-4000-8000-000000000001', '10000002-0000-4000-8000-000000000002', 2)
ON CONFLICT (store_id, product_id, group_id) DO NOTHING;

-- Choosing Oat Milk (20000002) unlocks Plant Milk Preparation sub-addon group (10000003)
INSERT INTO option_modifier_groups (store_id, parent_option_id, child_group_id, display_order)
VALUES
    ('11111111-1111-4111-8111-111111111111', '20000002-0000-4000-8000-000000000002', '10000003-0000-4000-8000-000000000003', 1)
ON CONFLICT (store_id, parent_option_id, child_group_id) DO NOTHING;

-- 6. STORE DAILY SEQUENCES
INSERT INTO store_daily_sequences (store_id, business_date, last_seq)
VALUES
    ('11111111-1111-4111-8111-111111111111', CURRENT_DATE, 1)
ON CONFLICT (store_id, business_date) DO NOTHING;

-- 7. ORDERS (Sample completed order)
INSERT INTO orders (
    id, store_id, idempotency_key, order_number, queue_number, table_number, customer_name, customer_phone,
    order_type, billing_mode, status, payment_status, subtotal_amount, discount_amount, tax_amount, service_charge_amount, total_amount, paid_amount, notes
) VALUES (
    'e0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'idem-seed-order-0001',
    'ORD-20261028-0001', 'A01', 'Meja 04', 'Alexander Budi', '081311223344',
    'dine_in', 'pay_first', 'served', 'paid', 50000, 0, 5000, 2500, 57500, 57500, 'Less ice for Latte'
)
ON CONFLICT (id) DO NOTHING;

-- 8. ORDER ITEMS (Snapshots of purchased products)
INSERT INTO order_items (
    id, store_id, order_id, product_id, config_hash, snapshot_name, unit_base_price, modifier_total_price, snapshot_price, quantity, subtotal_amount, selected_options, notes
) VALUES
    ('f0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000001-0000-4000-8000-000000000001', 'cfg-hash-001', 'Classic Iced Latte', 24000, 0, 24000, 1, 24000, '[{"group":"Milk Selection","choice":"Fresh Dairy Milk","price_delta":0}]'::jsonb, 'Less sugar'),
    ('f0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000002-0000-4000-8000-000000000002', 'cfg-hash-002', 'Caffe Latte', 26000, 0, 26000, 1, 26000, '[]'::jsonb, '')
ON CONFLICT (id) DO NOTHING;

INSERT INTO order_item_options (
    id, store_id, order_item_id, modifier_group_id, modifier_option_id, snapshot_group_name, snapshot_option_name, snapshot_price_delta, quantity
) VALUES (
    '30000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111',
    'f0000001-0000-4000-8000-000000000001', '10000001-0000-4000-8000-000000000001', '20000001-0000-4000-8000-000000000001',
    'Milk Selection', 'Fresh Dairy Milk', 0, 1
)
ON CONFLICT (id) DO NOTHING;

-- 9. PAYMENT SPLITS & PAYMENTS
INSERT INTO payment_splits (
    id, store_id, order_id, split_number, label, split_mode, subtotal_amount, tax_amount, service_charge_amount, total_amount, paid_amount, status
) VALUES (
    '40000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001',
    1, 'Full Bill', 'full', 50000, 5000, 2500, 57500, 57500, 'paid'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO payments (
    id, store_id, order_id, split_id, attempt_no, idempotency_key, provider, provider_order_id, amount, method, status, raw_provider_status, transaction_id, payment_type, qr_string, paid_at
) VALUES (
    'b0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001',
    '40000001-0000-4000-8000-000000000001', 1, 'idem-ord-20261028-0001-att1', 'midtrans',
    'ORD-20261028-0001', 57500, 'qris', 'paid', 'settlement', 'midtrans-mock-tx-001', 'qris', '00020101021226...MOCKQRIS...', NOW()
)
ON CONFLICT (id) DO NOTHING;

-- 10. AUDIT LOGS
INSERT INTO audit_logs (
    id, store_id, actor_id, actor_name, action, reason, reference_id, metadata
) VALUES (
    'b0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111',
    'a0000003-0000-4000-8000-000000000003', 'Maya Putri (Cashier)', 'STOCK_OVERRIDE',
    'Initial stock replenishment for Gayo beans', 'd0000004-0000-4000-8000-000000000004',
    '{"old_stock": 0, "new_stock": 45}'::jsonb
)
ON CONFLICT (id) DO NOTHING;

-- 12. CATALOG TYPES, STATIONS & VARIANTS (migration 000008, owner decision K1)
-- Croissant is a ready-to-serve kitchen item; drinks are prepared at the bar (column default).
UPDATE products SET product_type = 'ready_to_serve', prep_station = 'kitchen'
WHERE id = 'd0000005-0000-4000-8000-000000000005';

-- Every product sells through at least one default variant (idempotent backfill).
INSERT INTO product_variants (store_id, product_id, name, sku, price, is_default, is_available)
SELECT p.store_id, p.id, 'Reguler', '', p.base_price, true, true
FROM products p
WHERE NOT EXISTS (SELECT 1 FROM product_variants v WHERE v.store_id = p.store_id AND v.product_id = p.id);

-- A sized drink: Caffe Latte Regular (default) and Large with their own SKUs.
UPDATE product_variants SET sku = 'LAT-R'
WHERE product_id = 'd0000002-0000-4000-8000-000000000002' AND is_default AND sku = '';
INSERT INTO product_variants (id, store_id, product_id, name, sku, price, is_default, display_order)
VALUES ('e0000002-0000-4000-8000-000000000022', '11111111-1111-4111-8111-111111111111',
        'd0000002-0000-4000-8000-000000000002', 'Large', 'LAT-L', 32000, false, 1)
ON CONFLICT (id) DO NOTHING;
