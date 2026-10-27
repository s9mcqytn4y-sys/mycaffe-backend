-- 000001_init_schema.up.sql
-- Multi-Tenant PostgreSQL Schema for Kafe Senja Vibe

-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 1. STORES (Tenants)
CREATE TABLE IF NOT EXISTS stores (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug VARCHAR(100) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    address TEXT,
    phone VARCHAR(50),
    is_active BOOLEAN NOT NULL DEFAULT true,
    tax_percentage BIGINT NOT NULL DEFAULT 10,           -- 10% PB1
    service_percentage BIGINT NOT NULL DEFAULT 0,       -- Service Charge
    qris_mdr_percentage BIGINT NOT NULL DEFAULT 70,     -- 70 bps = 0.7%
    currency VARCHAR(10) NOT NULL DEFAULT 'IDR',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. USERS / STAFF
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    full_name VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'staff',           -- owner, manager, cashier, barista, kitchen
    pin_hash VARCHAR(255) NOT NULL,                     -- Salted 6-digit PIN hash
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(store_id, email)
);

-- 3. MENU CATEGORIES
CREATE TABLE IF NOT EXISTS categories (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 4. PRODUCTS
CREATE TABLE IF NOT EXISTS products (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    category_id UUID REFERENCES categories(id) ON DELETE SET NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    base_price BIGINT NOT NULL,                         -- Integer IDR (no floating point!)
    image_url TEXT,
    is_available BOOLEAN NOT NULL DEFAULT true,
    track_stock BOOLEAN NOT NULL DEFAULT false,
    stock_quantity INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 5. ORDERS
CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    order_number VARCHAR(50) NOT NULL,                  -- Display ID: e.g. ORD-20261028-001
    queue_number VARCHAR(20) NOT NULL,                  -- Display Queue: e.g. A01
    table_number VARCHAR(50) NOT NULL DEFAULT '',
    customer_name VARCHAR(100) NOT NULL,
    customer_phone VARCHAR(50) DEFAULT '',
    order_type VARCHAR(30) NOT NULL DEFAULT 'dine_in',  -- dine_in, takeaway
    status VARCHAR(50) NOT NULL DEFAULT 'placed',       -- placed, queued, preparing, ready, completed, cancelled
    payment_status VARCHAR(50) NOT NULL DEFAULT 'pending', -- pending, paid, failed, expired, refunded

    subtotal_amount BIGINT NOT NULL DEFAULT 0,          -- IDR
    discount_amount BIGINT NOT NULL DEFAULT 0,          -- IDR
    tax_amount BIGINT NOT NULL DEFAULT 0,               -- IDR
    service_charge_amount BIGINT NOT NULL DEFAULT 0,    -- IDR
    total_amount BIGINT NOT NULL DEFAULT 0,             -- IDR

    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 6. ORDER ITEMS (Snapshotting price, modifiers, and historical product name)
CREATE TABLE IF NOT EXISTS order_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id UUID REFERENCES products(id) ON DELETE SET NULL,
    snapshot_name VARCHAR(255) NOT NULL,
    snapshot_price BIGINT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    subtotal_amount BIGINT NOT NULL,
    selected_options JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 7. PAYMENTS
CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    amount BIGINT NOT NULL,
    method VARCHAR(50) NOT NULL,                         -- qris, bank_transfer, cash
    status VARCHAR(50) NOT NULL DEFAULT 'pending',       -- pending, paid, failed, expired, refunded
    transaction_id VARCHAR(100) DEFAULT '',              -- Gateway Transaction ID
    payment_type VARCHAR(50) DEFAULT '',
    qr_string TEXT DEFAULT '',
    va_number VARCHAR(100) DEFAULT '',
    bank VARCHAR(50) DEFAULT '',
    paid_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 8. PAYMENT NOTIFICATION LOGS (Audit & Idempotency)
CREATE TABLE IF NOT EXISTS payment_notification_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID REFERENCES stores(id) ON DELETE CASCADE,
    order_id UUID REFERENCES orders(id) ON DELETE SET NULL,
    provider VARCHAR(50) NOT NULL DEFAULT 'midtrans',
    notification_id VARCHAR(150) NOT NULL,
    status_code VARCHAR(10) NOT NULL,
    transaction_status VARCHAR(50) NOT NULL,
    raw_payload TEXT NOT NULL,
    is_processed BOOLEAN NOT NULL DEFAULT false,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 9. AUDIT LOGS (Operational tracking: void, refund, stock override, price change, shift discrepancy)
CREATE TABLE IF NOT EXISTS audit_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    actor_id UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_name VARCHAR(255) NOT NULL,
    action VARCHAR(100) NOT NULL,                       -- VOID_ORDER, REFUND, STOCK_OVERRIDE, SHIFT_CLOSE
    reason TEXT NOT NULL,
    reference_id VARCHAR(100),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- INDEXES for multi-tenant and fast query performance
CREATE INDEX IF NOT EXISTS idx_orders_store_status ON orders(store_id, status);
CREATE INDEX IF NOT EXISTS idx_orders_store_payment_status ON orders(store_id, payment_status);
CREATE INDEX IF NOT EXISTS idx_order_items_order_id ON order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_payments_store_order ON payments(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_payments_transaction_id ON payments(transaction_id);
CREATE INDEX IF NOT EXISTS idx_notif_logs_notif_id ON payment_notification_logs(notification_id);

