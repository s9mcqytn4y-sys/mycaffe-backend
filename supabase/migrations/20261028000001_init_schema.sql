-- 20261028000001_init_schema.sql
-- Multi-Tenant PostgreSQL Schema for Kafe Senja Vibe POS
-- Hardened with tenant composite foreign keys, basis points (BPS), and strict check constraints.

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
    tax_bps BIGINT NOT NULL DEFAULT 1000,           -- 1000 bps = 10.00% PB1
    service_bps BIGINT NOT NULL DEFAULT 0,          -- Service Charge in bps (e.g. 500 = 5%)
    qris_mdr_bps BIGINT NOT NULL DEFAULT 70,        -- 70 bps = 0.70% QRIS MDR
    currency VARCHAR(10) NOT NULL DEFAULT 'IDR',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_stores_tax_bps CHECK (tax_bps >= 0 AND tax_bps <= 10000),
    CONSTRAINT chk_stores_service_bps CHECK (service_bps >= 0 AND service_bps <= 10000),
    CONSTRAINT chk_stores_qris_mdr_bps CHECK (qris_mdr_bps >= 0 AND qris_mdr_bps <= 10000)
);

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
    CONSTRAINT uq_users_store_email UNIQUE(store_id, email),
    CONSTRAINT chk_users_role CHECK (role IN ('owner', 'manager', 'cashier', 'barista', 'kitchen'))
);

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

-- 4. PRODUCTS
CREATE TABLE IF NOT EXISTS products (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    category_id UUID,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    base_price BIGINT NOT NULL,                         -- Integer IDR (minor units)
    image_url TEXT,
    is_available BOOLEAN NOT NULL DEFAULT true,
    track_stock BOOLEAN NOT NULL DEFAULT false,
    stock_quantity INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_products_store_id UNIQUE(store_id, id),
    CONSTRAINT fk_products_category FOREIGN KEY (store_id, category_id) REFERENCES categories(store_id, id) ON DELETE SET NULL,
    CONSTRAINT chk_products_base_price CHECK (base_price >= 0),
    CONSTRAINT chk_products_stock_quantity CHECK (stock_quantity >= 0)
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
    order_type VARCHAR(30) NOT NULL DEFAULT 'dine_in',
    status VARCHAR(50) NOT NULL DEFAULT 'placed',
    payment_status VARCHAR(50) NOT NULL DEFAULT 'pending',

    subtotal_amount BIGINT NOT NULL DEFAULT 0,          -- IDR minor unit
    discount_amount BIGINT NOT NULL DEFAULT 0,          -- IDR minor unit
    tax_amount BIGINT NOT NULL DEFAULT 0,               -- IDR minor unit
    service_charge_amount BIGINT NOT NULL DEFAULT 0,    -- IDR minor unit
    total_amount BIGINT NOT NULL DEFAULT 0,             -- IDR minor unit

    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_orders_store_id UNIQUE(store_id, id),
    CONSTRAINT uq_orders_store_order_number UNIQUE(store_id, order_number),
    CONSTRAINT chk_orders_order_type CHECK (order_type IN ('dine_in', 'takeaway')),
    CONSTRAINT chk_orders_status CHECK (status IN ('placed', 'queued', 'preparing', 'ready', 'completed', 'cancelled')),
    CONSTRAINT chk_orders_payment_status CHECK (payment_status IN ('pending', 'paid', 'failed', 'expired', 'refunded')),
    CONSTRAINT chk_orders_subtotal CHECK (subtotal_amount >= 0),
    CONSTRAINT chk_orders_discount CHECK (discount_amount >= 0),
    CONSTRAINT chk_orders_tax CHECK (tax_amount >= 0),
    CONSTRAINT chk_orders_service CHECK (service_charge_amount >= 0),
    CONSTRAINT chk_orders_total CHECK (total_amount >= 0)
);

-- 6. ORDER ITEMS (Tenant-isolated snapshot of item, modifiers, price)
CREATE TABLE IF NOT EXISTS order_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    order_id UUID NOT NULL,
    product_id UUID,
    snapshot_name VARCHAR(255) NOT NULL,
    snapshot_price BIGINT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    subtotal_amount BIGINT NOT NULL,
    selected_options JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_order_items_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_order_items_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE SET NULL,
    CONSTRAINT chk_order_items_price CHECK (snapshot_price >= 0),
    CONSTRAINT chk_order_items_quantity CHECK (quantity > 0),
    CONSTRAINT chk_order_items_subtotal CHECK (subtotal_amount >= 0)
);

-- 7. PAYMENTS (Tenant-isolated payment ledger)
CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    order_id UUID NOT NULL,
    amount BIGINT NOT NULL,
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
    CONSTRAINT chk_payments_amount CHECK (amount > 0),
    CONSTRAINT chk_payments_method CHECK (method IN ('qris', 'bank_transfer', 'cash')),
    CONSTRAINT chk_payments_status CHECK (status IN ('pending', 'paid', 'failed', 'expired', 'refunded'))
);

-- 8. PAYMENT NOTIFICATION LOGS (Audit & Idempotency)
CREATE TABLE IF NOT EXISTS payment_notification_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID REFERENCES stores(id) ON DELETE CASCADE,
    order_id UUID,
    provider VARCHAR(50) NOT NULL DEFAULT 'midtrans',
    notification_id VARCHAR(150) NOT NULL,
    status_code VARCHAR(10) NOT NULL,
    transaction_status VARCHAR(50) NOT NULL,
    raw_payload TEXT NOT NULL,
    is_processed BOOLEAN NOT NULL DEFAULT false,
    processed_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_notif_logs_provider_id UNIQUE(provider, notification_id),
    CONSTRAINT chk_notif_logs_processed_state CHECK (
        (is_processed = false AND processed_at IS NULL) OR
        (is_processed = true AND processed_at IS NOT NULL)
    )
);

-- 9. AUDIT LOGS (Operational tracking: void, refund, stock override, price change, shift discrepancy)
CREATE TABLE IF NOT EXISTS audit_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    actor_id UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_name VARCHAR(255) NOT NULL,
    action VARCHAR(100) NOT NULL,
    reason TEXT NOT NULL,
    reference_id VARCHAR(100),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
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
CREATE INDEX IF NOT EXISTS idx_notif_logs_provider_id ON payment_notification_logs(provider, notification_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_store_created ON audit_logs(store_id, created_at DESC);
