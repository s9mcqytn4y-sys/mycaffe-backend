-- db/seeds/development.sql
-- Development Seed Data for MyCaffe Core POS
-- Multi-tenant sample data with Indonesian Rupiah whole integer amounts and Basis Points (BPS)
-- STRICTLY ISOLATED TO LOCAL / CI DEVELOPMENT ONLY. NEVER EXECUTE IN PRODUCTION!
-- Using strictly valid RFC 4122 hexadecimal UUIDs and generic multi-tenant identities.

-- 1. STORES (TENANTS)
INSERT INTO stores (id, slug, name, address, phone, timezone, is_active, order_prefix, queue_prefix, tax_bps, service_bps, qris_mdr_bps, currency)
VALUES 
    ('11111111-1111-4111-8111-111111111111', 'central-cafe', 'MyCaffe Demo Store — Central', 'Jl. Sudirman No. 101, Jakarta', '081234567890', 'Asia/Jakarta', true, 'ORD', 'A', 1000, 500, 70, 'IDR'),
    ('22222222-2222-4222-8222-222222222222', 'express-counter', 'MyCaffe Demo Store — Express Counter', 'Lobby Tower A, Jakarta', '081298765432', 'Asia/Jakarta', true, 'EXP', 'B', 1000, 0, 70, 'IDR')
ON CONFLICT (id) DO NOTHING;

-- 2. USERS (STAFF)
-- Default test pin_hash (dummy bcrypt hash for testing)
INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active)
VALUES
    ('a0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'owner@mycaffe.local', 'Alexander (Owner)', 'owner', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'manager.central@mycaffe.local', 'Sarah Jenkins (Manager)', 'manager', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'cashier.central@mycaffe.local', 'Maya Putri (Cashier)', 'cashier', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'barista.central@mycaffe.local', 'Doni Pratama (Barista)', 'barista', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'cashier.express@mycaffe.local', 'Lestari Indah (Cashier)', 'cashier', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true)
ON CONFLICT (id) DO NOTHING;

-- 3. CATEGORIES
INSERT INTO categories (id, store_id, name, display_order, is_active)
VALUES
    ('c0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Signature Series', 1, true),
    ('c0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Espresso & Coffee', 2, true),
    ('c0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Manual Brew & Specialty', 3, true),
    ('c0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'Artisan Pastry & Bakery', 4, true),
    ('c0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'Quick Grab Coffee', 1, true)
ON CONFLICT (id) DO NOTHING;

-- 4. PRODUCTS (Whole integer IDR, e.g. Rp 22.000 = 22000)
INSERT INTO products (id, store_id, category_id, name, description, base_price, is_available, track_stock, stock_quantity)
VALUES
    ('d0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Classic Iced Latte', 'Double espresso shot with fresh dairy milk and artisanal sugar', 24000, true, false, 0),
    ('d0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Caffe Latte', 'Espresso with steamed fresh milk and microfoam', 26000, true, false, 0),
    ('d0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Americano (Double Shot)', 'Double ristretto poured over filtered water', 20000, true, false, 0),
    ('d0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'V60 Single Origin Gayo', 'Filter brewed single origin with floral citrus notes', 32000, true, true, 45),
    ('d0000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Butter Croissant Artisanal', 'Freshly baked flaky French butter croissant', 24000, true, true, 18),
    ('d0000006-0000-4000-8000-000000000006', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Express Cold Brew', 'Steeped for 18 hours, rich and smooth finish', 22000, true, false, 0)
ON CONFLICT (id) DO NOTHING;

-- 5. STORE DAILY SEQUENCES
INSERT INTO store_daily_sequences (store_id, business_date, last_seq)
VALUES
    ('11111111-1111-4111-8111-111111111111', CURRENT_DATE, 1)
ON CONFLICT (store_id, business_date) DO NOTHING;

-- 6. ORDERS (Sample completed order)
INSERT INTO orders (
    id, store_id, order_number, queue_number, table_number, customer_name, customer_phone,
    order_type, status, payment_status, subtotal_amount, discount_amount, tax_amount, service_charge_amount, total_amount, notes
) VALUES (
    'e0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111',
    'ORD-20261028-0001', 'A01', 'Meja 04', 'Alexander Budi', '081311223344',
    'dine_in', 'ready', 'paid', 50000, 0, 5000, 2500, 57500, 'Less ice for Latte'
)
ON CONFLICT (id) DO NOTHING;

-- 7. ORDER ITEMS (Snapshots of purchased products)
INSERT INTO order_items (
    id, store_id, order_id, product_id, snapshot_name, snapshot_price, quantity, subtotal_amount, selected_options, notes
) VALUES
    ('f0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000001-0000-4000-8000-000000000001', 'Classic Iced Latte', 24000, 1, 24000, '[{"group":"Ice","choice":"Less Ice"}]'::jsonb, 'Less sugar'),
    ('f0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000002-0000-4000-8000-000000000002', 'Caffe Latte', 26000, 1, 26000, '[{"group":"Milk","choice":"Fresh Milk"}]'::jsonb, '')
ON CONFLICT (id) DO NOTHING;

-- 8. PAYMENTS
INSERT INTO payments (
    id, store_id, order_id, provider_order_id, amount, method, status, transaction_id, payment_type, qr_string, paid_at
) VALUES (
    'b0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001',
    'ORD-20261028-0001', 57500, 'qris', 'paid', 'midtrans-mock-tx-001', 'qris', '00020101021226...MOCKQRIS...', NOW()
)
ON CONFLICT (id) DO NOTHING;

-- 9. AUDIT LOGS
INSERT INTO audit_logs (
    id, store_id, actor_id, actor_name, action, reason, reference_id, metadata
) VALUES (
    'b0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111',
    'a0000002-0000-4000-8000-000000000002', 'Sarah Jenkins (Manager)', 'STOCK_OVERRIDE',
    'Initial stock replenishment for Gayo beans', 'd0000004-0000-4000-8000-000000000004',
    '{"old_stock": 0, "new_stock": 45}'::jsonb
)
ON CONFLICT (id) DO NOTHING;
