-- supabase/seed.sql
-- Development Seed Data for Kafe Senja Vibe POS
-- Multi-tenant sample data with Indonesian Rupiah whole integer amounts and Basis Points (BPS)
-- Using strictly valid RFC 4122 hexadecimal UUIDs

-- Clear existing data if re-seeding
TRUNCATE TABLE audit_logs, payment_notification_logs, payments, order_items, orders, products, categories, users, stores CASCADE;

-- 1. STORES (TENANTS)
INSERT INTO stores (id, slug, name, address, phone, is_active, tax_bps, service_bps, qris_mdr_bps, currency)
VALUES 
    ('11111111-1111-4111-8111-111111111111', 'senja-utama', 'Kafe Senja Vibe — Grand Galaxy', 'Jl. Galaxy Raya No. 12, Bekasi', '081234567890', true, 1000, 500, 70, 'IDR'),
    ('22222222-2222-4222-8222-222222222222', 'senja-cabang', 'Kafe Senja Vibe — Harapan Indah', 'Ruko Sentra Niaga Blok B-05, Bekasi', '081298765432', true, 1000, 0, 70, 'IDR');

-- 2. USERS (STAFF)
-- Default test pin_hash (dummy bcrypt hash for testing)
INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active)
VALUES
    ('a0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'owner@kafesenja.com', 'Ahmad Senjaya (Owner)', 'owner', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'manager.galaxy@kafesenja.com', 'Rian Hidayat (Manager)', 'manager', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'kasir.galaxy@kafesenja.com', 'Siti Rahma (Cashier)', 'cashier', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'barista.galaxy@kafesenja.com', 'Budi Santoso (Barista)', 'barista', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'kasir.harapan@kafesenja.com', 'Dewi Lestari (Cashier)', 'cashier', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true);

-- 3. CATEGORIES
INSERT INTO categories (id, store_id, name, display_order, is_active)
VALUES
    ('c0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Signature Senja', 1, true),
    ('c0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Espresso Based', 2, true),
    ('c0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Manual Brew', 3, true),
    ('c0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'Pastry & Bakery', 4, true),
    ('c0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'Signature Senja', 1, true);

-- 4. PRODUCTS (Whole integer IDR, e.g. Rp 22.000 = 22000)
INSERT INTO products (id, store_id, category_id, name, description, base_price, is_available, track_stock, stock_quantity)
VALUES
    ('d0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Kopi Susu Senja', 'Espresso, fresh milk, and authentic gula aren syrup', 22000, true, false, 0),
    ('d0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Caffe Latte', 'Espresso with steamed fresh milk and microfoam', 26000, true, false, 0),
    ('d0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Americano (Double Shot)', 'Double ristretto poured over filtered water', 20000, true, false, 0),
    ('d0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'V60 Gayo Anaerobic', 'Single origin Aceh Gayo with sweet fruity notes', 32000, true, true, 45),
    ('d0000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Butter Croissant Artisanal', 'Freshly baked flaky French butter croissant', 24000, true, true, 18),
    ('d0000006-0000-4000-8000-000000000006', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Kopi Susu Senja Cabang', 'Espresso and authentic gula aren syrup', 22000, true, false, 0);

-- 5. ORDERS (Sample completed order)
INSERT INTO orders (
    id, store_id, order_number, queue_number, table_number, customer_name, customer_phone,
    order_type, status, payment_status, subtotal_amount, discount_amount, tax_amount, service_charge_amount, total_amount, notes
) VALUES (
    'e0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111',
    'ORD-20261028-001', 'A01', 'Meja 04', 'Andi Pratama', '081311223344',
    'dine_in', 'ready', 'paid', 48000, 0, 4800, 2400, 55200, 'Less ice for Kopi Susu'
);

-- 6. ORDER ITEMS (Snapshots of purchased products)
INSERT INTO order_items (
    id, store_id, order_id, product_id, snapshot_name, snapshot_price, quantity, subtotal_amount, selected_options, notes
) VALUES
    ('f0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000001-0000-4000-8000-000000000001', 'Kopi Susu Senja', 22000, 1, 22000, '[{"group":"Ice","choice":"Less Ice"}]'::jsonb, 'Less sugar'),
    ('f0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000002-0000-4000-8000-000000000002', 'Caffe Latte', 26000, 1, 26000, '[{"group":"Milk","choice":"Fresh Milk"}]'::jsonb, '');

-- 7. PAYMENTS
INSERT INTO payments (
    id, store_id, order_id, amount, method, status, transaction_id, payment_type, qr_string, paid_at
) VALUES (
    'b0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001',
    55200, 'qris', 'paid', 'midtrans-mock-tx-001', 'qris', '00020101021226...MOCKQRIS...', NOW()
);

-- 8. AUDIT LOGS
INSERT INTO audit_logs (
    id, store_id, actor_id, actor_name, action, reason, reference_id, metadata
) VALUES (
    'b0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111',
    'a0000002-0000-4000-8000-000000000002', 'Rian Hidayat (Manager)', 'STOCK_OVERRIDE',
    'Initial stock replenishment for Aceh Gayo beans', 'd0000004-0000-4000-8000-000000000004',
    '{"old_stock": 0, "new_stock": 45}'::jsonb
);
