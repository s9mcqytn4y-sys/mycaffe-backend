-- db/migrations/20261028000001_init_schema.up.sql
-- Multi-Tenant PostgreSQL 18 Schema for MyCaffe Core POS
-- Hardened with tenant composite foreign keys, column-specific ON DELETE SET NULL, basis points (BPS), and durable webhook idempotency.
-- ADR-002 Compliant: Single-VPS Docker Modular Monolith, native PostgreSQL 18, neutral multi-tenant branding.

-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 0. UPDATED_AT TRIGGER FUNCTION (Accurate clock_timestamp)
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 1. STORES (Tenants)
CREATE TABLE IF NOT EXISTS stores (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug VARCHAR(100) UNIQUE NOT NULL,                  -- e.g. 'central-cafe', 'express-counter'
    name VARCHAR(255) NOT NULL,
    address TEXT,
    phone VARCHAR(50),
    timezone VARCHAR(50) NOT NULL DEFAULT 'Asia/Jakarta',
    is_active BOOLEAN NOT NULL DEFAULT true,
    deleted_at TIMESTAMPTZ DEFAULT NULL,                -- Soft-delete timestamp
    order_prefix VARCHAR(10) NOT NULL DEFAULT 'ORD',    -- Configurable per store: e.g. 'ORD', 'MC', 'POS'
    queue_prefix VARCHAR(10) NOT NULL DEFAULT 'A',      -- Configurable per store: e.g. 'A', 'B', 'Q'
    tax_bps BIGINT NOT NULL DEFAULT 1000,               -- 1000 bps = 10.00% PB1 (Configurable per store)
    service_bps BIGINT NOT NULL DEFAULT 0,              -- Service charge in bps (e.g. 500 = 5.00%)
    qris_mdr_bps BIGINT NOT NULL DEFAULT 70,            -- 70 bps = 0.70% QRIS MDR (Configurable per store)
    currency VARCHAR(10) NOT NULL DEFAULT 'IDR',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_stores_tax_bps CHECK (tax_bps >= 0 AND tax_bps <= 10000),
    CONSTRAINT chk_stores_service_bps CHECK (service_bps >= 0 AND service_bps <= 10000),
    CONSTRAINT chk_stores_qris_mdr_bps CHECK (qris_mdr_bps >= 0 AND qris_mdr_bps <= 10000)
);

CREATE TRIGGER trg_stores_updated_at
BEFORE UPDATE ON stores
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 2. USERS / STAFF
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    full_name VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'cashier',
    pin_hash VARCHAR(255) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_users_store_id UNIQUE(store_id, id),
    CONSTRAINT uq_users_store_email UNIQUE(store_id, email),
    CONSTRAINT chk_users_role CHECK (role IN ('owner', 'manager', 'cashier', 'barista', 'kitchen'))
);

CREATE TRIGGER trg_users_updated_at
BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 3. MENU CATEGORIES
CREATE TABLE IF NOT EXISTS categories (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_categories_store_id UNIQUE(store_id, id)
);

CREATE TRIGGER trg_categories_updated_at
BEFORE UPDATE ON categories
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 4. PRODUCTS
-- Money amounts: Whole integer Indonesian Rupiah (e.g. 25000 = Rp 25.000; NO fractional cents)
CREATE TABLE IF NOT EXISTS products (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    category_id UUID,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    base_price BIGINT NOT NULL,                         -- Whole integer IDR
    image_url TEXT,
    is_available BOOLEAN NOT NULL DEFAULT true,
    track_stock BOOLEAN NOT NULL DEFAULT false,
    stock_quantity INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_products_store_id UNIQUE(store_id, id),
    CONSTRAINT fk_products_category FOREIGN KEY (store_id, category_id) REFERENCES categories(store_id, id) ON DELETE SET NULL (category_id),
    CONSTRAINT chk_products_base_price CHECK (base_price >= 0),
    CONSTRAINT chk_products_stock_quantity CHECK (stock_quantity >= 0)
);

CREATE TRIGGER trg_products_updated_at
BEFORE UPDATE ON products
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 5. STORE DAILY SEQUENCES (Atomic Queue Number Allocation per Business Date)
CREATE TABLE IF NOT EXISTS store_daily_sequences (
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    business_date DATE NOT NULL,
    last_seq INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (store_id, business_date)
);

-- 6. ORDERS
-- Money amounts: Whole integer Indonesian Rupiah (e.g. 50000 = Rp 50.000)
-- Financial history protection: store_id references stores(id) ON DELETE RESTRICT
CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_number VARCHAR(50) NOT NULL,                  -- Display/Business ID: e.g. ORD-20261028-0001
    queue_number VARCHAR(20) NOT NULL,                  -- Display Queue: e.g. A01
    table_number VARCHAR(50) NOT NULL DEFAULT '',
    customer_name VARCHAR(100) NOT NULL,
    customer_phone VARCHAR(50) DEFAULT '',
    order_type VARCHAR(30) NOT NULL DEFAULT 'dine_in',
    status VARCHAR(50) NOT NULL DEFAULT 'pending_payment',
    payment_status VARCHAR(50) NOT NULL DEFAULT 'pending',

    subtotal_amount BIGINT NOT NULL DEFAULT 0,          -- Whole integer IDR
    discount_amount BIGINT NOT NULL DEFAULT 0,          -- Whole integer IDR
    tax_amount BIGINT NOT NULL DEFAULT 0,               -- Whole integer IDR (PB1 Restaurant Tax)
    service_charge_amount BIGINT NOT NULL DEFAULT 0,    -- Whole integer IDR
    total_amount BIGINT NOT NULL DEFAULT 0,             -- Whole integer IDR

    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_orders_store_id UNIQUE(store_id, id),
    CONSTRAINT uq_orders_store_order_number UNIQUE(store_id, order_number),
    CONSTRAINT chk_orders_order_type CHECK (order_type IN ('dine_in', 'takeaway')),
    CONSTRAINT chk_orders_status CHECK (status IN ('pending_payment', 'queued', 'preparing', 'ready', 'completed', 'cancelled')),
    CONSTRAINT chk_orders_payment_status CHECK (payment_status IN ('pending', 'paid', 'failed', 'expired', 'partially_refunded', 'refunded')),
    CONSTRAINT chk_orders_subtotal CHECK (subtotal_amount >= 0),
    CONSTRAINT chk_orders_discount CHECK (discount_amount >= 0),
    CONSTRAINT chk_orders_tax CHECK (tax_amount >= 0),
    CONSTRAINT chk_orders_service CHECK (service_charge_amount >= 0),
    CONSTRAINT chk_orders_total CHECK (total_amount >= 0)
);

CREATE TRIGGER trg_orders_updated_at
BEFORE UPDATE ON orders
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 7. ORDER ITEMS (Tenant-isolated snapshot of item, modifiers, price)
-- When a product is deleted, preserve historical receipt line by nulling product_id only
CREATE TABLE IF NOT EXISTS order_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    order_id UUID NOT NULL,
    product_id UUID,
    snapshot_name VARCHAR(255) NOT NULL,
    snapshot_price BIGINT NOT NULL,                     -- Whole integer IDR
    quantity INT NOT NULL DEFAULT 1,
    subtotal_amount BIGINT NOT NULL,                    -- Whole integer IDR
    selected_options JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_order_items_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_order_items_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE SET NULL (product_id),
    CONSTRAINT chk_order_items_price CHECK (snapshot_price >= 0),
    CONSTRAINT chk_order_items_quantity CHECK (quantity > 0),
    CONSTRAINT chk_order_items_subtotal CHECK (subtotal_amount >= 0)
);

-- 8. PAYMENTS (Tenant-isolated payment ledger)
CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id UUID NOT NULL,
    provider_order_id VARCHAR(100) NOT NULL,            -- Stable provider order id (matched with Midtrans order_id)
    amount BIGINT NOT NULL,                             -- Whole integer IDR
    method VARCHAR(50) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    transaction_id VARCHAR(100) DEFAULT '',
    payment_type VARCHAR(50) DEFAULT '',
    qr_string TEXT DEFAULT '',
    va_number VARCHAR(100) DEFAULT '',
    bank VARCHAR(50) DEFAULT '',
    paid_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_payments_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE CASCADE,
    CONSTRAINT uq_payments_provider_order UNIQUE (store_id, provider_order_id),
    CONSTRAINT chk_payments_amount CHECK (amount > 0),
    CONSTRAINT chk_payments_method CHECK (method IN ('qris', 'bank_transfer', 'cash')),
    CONSTRAINT chk_payments_status CHECK (status IN ('pending', 'paid', 'failed', 'expired', 'partially_refunded', 'refunded'))
);

CREATE TRIGGER trg_payments_updated_at
BEFORE UPDATE ON payments
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 9. PAYMENT NOTIFICATION LOGS (Durable Webhook Inbox & Idempotency)
CREATE TABLE IF NOT EXISTS payment_notification_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID,                                      -- Nullable in durable inbox before store resolution
    order_id UUID,                                      -- Nullable in durable inbox before order resolution
    provider VARCHAR(50) NOT NULL DEFAULT 'midtrans',
    transaction_id VARCHAR(150) NOT NULL,              -- Midtrans transaction_id
    order_number VARCHAR(100) NOT NULL,                 -- Midtrans order_id mapped to store order_number / provider_order_id
    status_code VARCHAR(10) NOT NULL,                   -- Midtrans status_code (e.g. '200')
    transaction_status VARCHAR(50) NOT NULL,            -- e.g. settlement, pending, expire, cancel
    gross_amount BIGINT NOT NULL,                       -- Whole integer IDR
    raw_payload JSONB NOT NULL,
    is_processed BOOLEAN NOT NULL DEFAULT false,
    processed_at TIMESTAMPTZ DEFAULT NULL,
    error_message TEXT DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_notif_logs_event UNIQUE(provider, transaction_id, transaction_status, status_code),
    CONSTRAINT fk_notif_logs_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE SET NULL (order_id),
    CONSTRAINT chk_notif_logs_processed CHECK (
        (is_processed = false AND processed_at IS NULL) OR
        (is_processed = true AND processed_at IS NOT NULL)
    )
);

-- 10. AUDIT LOGS (Operational tracking with strict tenant boundary)
CREATE TABLE IF NOT EXISTS audit_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    actor_id UUID,
    actor_name VARCHAR(255) NOT NULL,
    action VARCHAR(100) NOT NULL,
    reason TEXT NOT NULL,
    reference_id VARCHAR(100),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_audit_logs_actor FOREIGN KEY (store_id, actor_id) REFERENCES users(store_id, id) ON DELETE SET NULL (actor_id),
    CONSTRAINT chk_audit_logs_action CHECK (action IN ('VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE', 'SHIFT_CLOSE', 'ROLE_CHANGE'))
);

-- INDEXES for multi-tenant isolation and fast queries
CREATE INDEX IF NOT EXISTS idx_users_store_role ON users(store_id, role);
CREATE INDEX IF NOT EXISTS idx_products_store_category ON products(store_id, category_id);
CREATE INDEX IF NOT EXISTS idx_orders_store_status ON orders(store_id, status);
CREATE INDEX IF NOT EXISTS idx_orders_store_payment_status ON orders(store_id, payment_status);
CREATE INDEX IF NOT EXISTS idx_order_items_store_order ON order_items(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_payments_store_order ON payments(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_payments_transaction_id ON payments(transaction_id);
CREATE INDEX IF NOT EXISTS idx_payments_provider_order ON payments(store_id, provider_order_id);
CREATE INDEX IF NOT EXISTS idx_notif_logs_lookup ON payment_notification_logs(provider, transaction_id, transaction_status);
CREATE INDEX IF NOT EXISTS idx_notif_logs_order_number ON payment_notification_logs(order_number);
CREATE INDEX IF NOT EXISTS idx_audit_logs_store_created ON audit_logs(store_id, created_at DESC);
