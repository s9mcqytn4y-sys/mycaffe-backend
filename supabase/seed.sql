-- supabase/seed.sql
-- Development Seed Data for MyCaffe Core POS Platform
-- Reference to db/seeds/development.sql
-- Multi-tenant sample data with Indonesian Rupiah whole integer amounts and Basis Points (BPS)
-- Using strictly valid RFC 4122 hexadecimal UUIDs
-- Idempotent non-destructive seeding using ON CONFLICT DO NOTHING

-- 1. STORES (TENANTS)
INSERT INTO stores (id, slug, name, address, phone, timezone, is_active, tax_bps, service_bps, qris_mdr_bps, currency, order_prefix, queue_prefix)
VALUES 
    ('11111111-1111-4111-8111-111111111111', 'central-cafe', 'MyCaffe Demo Store — Central', 'Jl. Sudirman No. 10, Jakarta Pusat', '081234567890', 'Asia/Jakarta', true, 1000, 500, 70, 'IDR', 'ORD', 'A'),
    ('22222222-2222-4222-8222-222222222222', 'express-counter', 'MyCaffe Demo Store — Express Counter', 'Mall Artha Lt. LG No. 5, Jakarta Utara', '081298765432', 'Asia/Jakarta', true, 1000, 0, 70, 'IDR', 'EXP', 'B')
ON CONFLICT (id) DO NOTHING;

-- 2. USERS (STAFF)
INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active)
VALUES
    ('a0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'owner@mycaffe.local', 'Store Owner (Demo)', 'owner', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'manager.central@mycaffe.local', 'Store Manager (Central)', 'manager', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'cashier.central@mycaffe.local', 'Cashier Alpha (Central)', 'cashier', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'barista.central@mycaffe.local', 'Barista Alpha (Central)', 'barista', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true),
    ('a0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'cashier.express@mycaffe.local', 'Cashier Beta (Express)', 'cashier', '$2a$10$abcdefghijklmnopqrstuvw1234567890testdummyhash', true)
ON CONFLICT (id) DO NOTHING;

-- 3. CATEGORIES
INSERT INTO categories (id, store_id, name, display_order, is_active)
VALUES
    ('c0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'Signature Coffee', 1, true),
    ('c0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'Espresso Based', 2, true),
    ('c0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'Manual Brew', 3, true),
    ('c0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'Pastry & Bakery', 4, true),
    ('c0000005-0000-4000-8000-000000000005', '22222222-2222-4222-8222-222222222222', 'Cold Brew & Grab', 1, true)
ON CONFLICT (id) DO NOTHING;

-- 4. PRODUCTS (Whole integer IDR, e.g. Rp 25.000 = 25000)
INSERT INTO products (id, store_id, category_id, name, description, base_price, is_available, track_stock, stock_quantity)
VALUES
    ('d0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'c0000001-0000-4000-8000-000000000001', 'Classic Iced Latte', 'Signature espresso, fresh milk, and organic palm sugar', 25000, true, false, 0),
    ('d0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Caffe Latte', 'Espresso with steamed fresh milk and silky microfoam', 28000, true, false, 0),
    ('d0000003-0000-4000-8000-000000000003', '11111111-1111-4111-8111-111111111111', 'c0000002-0000-4000-8000-000000000002', 'Americano (Double Shot)', 'Double ristretto poured over filtered water', 22000, true, false, 0),
    ('d0000004-0000-4000-8000-000000000004', '11111111-1111-4111-8111-111111111111', 'c0000003-0000-4000-8000-000000000003', 'V60 Single Origin Gayo', 'Specialty single origin Aceh Gayo with bright fruity notes', 35000, true, true, 50),
    ('d0000005-0000-4000-8000-000000000005', '11111111-1111-4111-8111-111111111111', 'c0000004-0000-4000-8000-000000000004', 'Butter Croissant Artisanal', 'Freshly baked flaky French butter croissant', 26000, true, true, 25),
    ('d0000006-0000-4000-8000-000000000006', '22222222-2222-4222-8222-222222222222', 'c0000005-0000-4000-8000-000000000005', 'Express Cold Brew', '18-hour cold steeped single origin coffee', 28000, true, false, 0)
ON CONFLICT (id) DO NOTHING;

-- 5. STORE DAILY SEQUENCES
INSERT INTO store_daily_sequences (store_id, business_date, last_seq)
VALUES
    ('11111111-1111-4111-8111-111111111111', CURRENT_DATE, 1)
ON CONFLICT (store_id, business_date) DO NOTHING;

-- 6. SAMPLE ORDER WITH FULL SNAPSHOT
INSERT INTO orders (id, store_id, order_number, queue_number, customer_name, order_type, table_number, status, subtotal_amount, discount_amount, tax_amount, service_amount, total_amount, created_at, updated_at)
VALUES
    ('e0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'ORD-DEMO-0001', 'A01', 'Demo Customer', 'dine_in', 'T-04', 'pending_payment', 51000, 0, 5355, 2550, 58905, NOW(), NOW())
ON CONFLICT (id) DO NOTHING;

-- 7. ORDER ITEMS (SNAPSHOTTED)
INSERT INTO order_items (id, store_id, order_id, product_id, snapshot_name, snapshot_price, quantity, subtotal_amount, notes)
VALUES
    ('f0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000001-0000-4000-8000-000000000001', 'Classic Iced Latte', 25000, 1, 25000, 'Less ice, normal sugar'),
    ('f0000002-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'd0000005-0000-4000-8000-000000000005', 'Butter Croissant Artisanal', 26000, 1, 26000, 'Warm up before serving')
ON CONFLICT (id) DO NOTHING;

-- 8. PAYMENT RECORD
INSERT INTO payments (id, store_id, order_id, provider, provider_order_id, method, amount, status, created_at, updated_at)
VALUES
    ('p0000001-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'e0000001-0000-4000-8000-000000000001', 'midtrans', 'ORD-DEMO-0001', 'qris', 58905, 'pending', NOW(), NOW())
ON CONFLICT (id) DO NOTHING;
