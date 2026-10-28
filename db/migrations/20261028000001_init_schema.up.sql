-- db/migrations/20261028000001_init_schema.up.sql
-- Multi-Tenant PostgreSQL 18 Schema for MyCaffe Core POS
-- ADR-002 Compliant: Single-VPS Docker Modular Monolith, native PostgreSQL 18, neutral multi-tenant branding.
-- Features:
-- 1. Least-privilege runtime role (mycaffe_app) & Native Row-Level Security (RLS) via current_store_id()
-- 2. Strict ON DELETE RESTRICT on all financial records (stores -> orders -> payments / payment_splits)
-- 3. Full Catalog Modifier Groups & 1-Level Nested Sub-Addons (option_modifier_groups) + Relational Order Item Option Snapshots
-- 4. Single-Order Split Bill (payment_splits, payment_split_items) & Multi-Attempt Core API Payment Ledger
-- 5. Durable Webhook Inbox (payment_notification_logs) & Transactional Outbox (outbox_events)

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 0. HELPER FUNCTIONS & RUNTIME ROLE
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Tenant context resolver for Row-Level Security (RLS)
CREATE OR REPLACE FUNCTION current_store_id()
RETURNS UUID AS $$
    SELECT NULLIF(current_setting('app.current_store_id', true), '')::UUID;
$$ LANGUAGE sql STABLE;

-- Create least-privilege runtime application role (if not exists)
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mycaffe_app') THEN
        CREATE ROLE mycaffe_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
END $$;

-- 1. STORES (Tenants)
CREATE TABLE IF NOT EXISTS stores (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug VARCHAR(100) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    address TEXT,
    phone VARCHAR(50),
    timezone VARCHAR(50) NOT NULL DEFAULT 'Asia/Jakarta',
    is_active BOOLEAN NOT NULL DEFAULT true,
    deleted_at TIMESTAMPTZ DEFAULT NULL,
    order_prefix VARCHAR(10) NOT NULL DEFAULT 'ORD',
    queue_prefix VARCHAR(10) NOT NULL DEFAULT 'A',
    default_billing_mode VARCHAR(20) NOT NULL DEFAULT 'pay_first',
    enabled_payment_methods JSONB NOT NULL DEFAULT '["qris","bank_transfer","gopay","shopeepay","cash"]'::jsonb,
    tax_bps BIGINT NOT NULL DEFAULT 1000,               -- 1000 bps = 10.00% PB1
    service_bps BIGINT NOT NULL DEFAULT 0,              -- e.g. 500 = 5.00%
    qris_mdr_bps BIGINT NOT NULL DEFAULT 70,            -- 70 bps = 0.70% QRIS MDR
    currency VARCHAR(10) NOT NULL DEFAULT 'IDR',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_stores_billing_mode CHECK (default_billing_mode IN ('pay_first', 'open_tab')),
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
CREATE TABLE IF NOT EXISTS products (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    category_id UUID,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    base_price BIGINT NOT NULL,
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

-- 5. MODIFIER GROUPS & OPTIONS (With 1-Level Nested Sub-Addon Support)
CREATE TABLE IF NOT EXISTS modifier_groups (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    selection_type VARCHAR(20) NOT NULL DEFAULT 'single',
    is_required BOOLEAN NOT NULL DEFAULT false,
    min_select INT NOT NULL DEFAULT 0,
    max_select INT NOT NULL DEFAULT 1,
    display_order INT NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_modifier_groups_store_id UNIQUE(store_id, id),
    CONSTRAINT chk_modifier_groups_selection_type CHECK (selection_type IN ('single', 'multiple')),
    CONSTRAINT chk_modifier_groups_bounds CHECK (min_select >= 0 AND max_select >= 1 AND min_select <= max_select)
);

CREATE TRIGGER trg_modifier_groups_updated_at
BEFORE UPDATE ON modifier_groups
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TABLE IF NOT EXISTS modifier_options (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    group_id UUID NOT NULL,
    name VARCHAR(100) NOT NULL,
    price_delta BIGINT NOT NULL DEFAULT 0,
    is_available BOOLEAN NOT NULL DEFAULT true,
    display_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_modifier_options_store_id UNIQUE(store_id, id),
    CONSTRAINT fk_modifier_options_group FOREIGN KEY (store_id, group_id) REFERENCES modifier_groups(store_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_modifier_options_price_delta CHECK (price_delta >= 0)
);

CREATE TRIGGER trg_modifier_options_updated_at
BEFORE UPDATE ON modifier_options
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Product -> Modifier Group mapping
CREATE TABLE IF NOT EXISTS product_modifier_groups (
    store_id UUID NOT NULL,
    product_id UUID NOT NULL,
    group_id UUID NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    override_required BOOLEAN DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (store_id, product_id, group_id),
    CONSTRAINT fk_pmg_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_pmg_group FOREIGN KEY (store_id, group_id) REFERENCES modifier_groups(store_id, id) ON DELETE CASCADE
);

-- Option -> Child Modifier Group mapping (1-Level Nested Sub-Addons)
CREATE TABLE IF NOT EXISTS option_modifier_groups (
    store_id UUID NOT NULL,
    parent_option_id UUID NOT NULL,
    child_group_id UUID NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (store_id, parent_option_id, child_group_id),
    CONSTRAINT fk_omg_parent_option FOREIGN KEY (store_id, parent_option_id) REFERENCES modifier_options(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_omg_child_group FOREIGN KEY (store_id, child_group_id) REFERENCES modifier_groups(store_id, id) ON DELETE CASCADE
);

-- 6. STORE DAILY SEQUENCES (Atomic Queue Number Allocation per Business Date)
CREATE TABLE IF NOT EXISTS store_daily_sequences (
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    business_date DATE NOT NULL,
    last_seq INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (store_id, business_date)
);

-- 7. ORDERS
-- Financial history protection: store_id references stores(id) ON DELETE RESTRICT
CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_number VARCHAR(50) NOT NULL,
    queue_number VARCHAR(20) NOT NULL,
    table_number VARCHAR(50) NOT NULL DEFAULT '',
    customer_name VARCHAR(100) NOT NULL,
    customer_phone VARCHAR(50) DEFAULT '',
    order_type VARCHAR(30) NOT NULL DEFAULT 'dine_in',
    billing_mode VARCHAR(20) NOT NULL DEFAULT 'pay_first',
    status VARCHAR(50) NOT NULL DEFAULT 'pending_payment',
    payment_status VARCHAR(50) NOT NULL DEFAULT 'unpaid',

    subtotal_amount BIGINT NOT NULL DEFAULT 0,
    discount_amount BIGINT NOT NULL DEFAULT 0,
    tax_amount BIGINT NOT NULL DEFAULT 0,
    service_charge_amount BIGINT NOT NULL DEFAULT 0,
    total_amount BIGINT NOT NULL DEFAULT 0,
    paid_amount BIGINT NOT NULL DEFAULT 0,
    refunded_amount BIGINT NOT NULL DEFAULT 0,

    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_orders_store_id UNIQUE(store_id, id),
    CONSTRAINT uq_orders_store_order_number UNIQUE(store_id, order_number),
    CONSTRAINT chk_orders_order_type CHECK (order_type IN ('dine_in', 'takeaway')),
    CONSTRAINT chk_orders_billing_mode CHECK (billing_mode IN ('pay_first', 'open_tab', 'split_bill')),
    CONSTRAINT chk_orders_status CHECK (status IN ('pending_payment', 'queued', 'preparing', 'ready', 'completed', 'cancelled')),
    CONSTRAINT chk_orders_payment_status CHECK (payment_status IN ('unpaid', 'pending', 'partially_paid', 'paid', 'failed', 'expired', 'partially_refunded', 'refunded')),
    CONSTRAINT chk_orders_subtotal CHECK (subtotal_amount >= 0),
    CONSTRAINT chk_orders_discount CHECK (discount_amount >= 0),
    CONSTRAINT chk_orders_tax CHECK (tax_amount >= 0),
    CONSTRAINT chk_orders_service CHECK (service_charge_amount >= 0),
    CONSTRAINT chk_orders_total CHECK (total_amount >= 0),
    CONSTRAINT chk_orders_paid_amount CHECK (paid_amount >= 0),
    CONSTRAINT chk_orders_refunded_amount CHECK (refunded_amount >= 0 AND refunded_amount <= paid_amount)
);

CREATE TRIGGER trg_orders_updated_at
BEFORE UPDATE ON orders
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 8. ORDER ITEMS & SNAPSHOTTED MODIFIER OPTIONS
CREATE TABLE IF NOT EXISTS order_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id UUID NOT NULL,
    product_id UUID,
    config_hash VARCHAR(64) NOT NULL DEFAULT '',
    snapshot_name VARCHAR(255) NOT NULL,
    unit_base_price BIGINT NOT NULL DEFAULT 0,
    modifier_total_price BIGINT NOT NULL DEFAULT 0,
    snapshot_price BIGINT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    subtotal_amount BIGINT NOT NULL,
    selected_options JSONB NOT NULL DEFAULT '[]'::jsonb,
    notes TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_order_items_store_id UNIQUE(store_id, id),
    CONSTRAINT fk_order_items_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_order_items_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE SET NULL (product_id),
    CONSTRAINT chk_order_items_base_price CHECK (unit_base_price >= 0),
    CONSTRAINT chk_order_items_mod_price CHECK (modifier_total_price >= 0),
    CONSTRAINT chk_order_items_price CHECK (snapshot_price >= 0),
    CONSTRAINT chk_order_items_quantity CHECK (quantity > 0),
    CONSTRAINT chk_order_items_subtotal CHECK (subtotal_amount >= 0)
);

CREATE TABLE IF NOT EXISTS order_item_options (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_item_id UUID NOT NULL,
    modifier_group_id UUID,
    modifier_option_id UUID,
    parent_option_id UUID,
    snapshot_group_name VARCHAR(100) NOT NULL,
    snapshot_option_name VARCHAR(100) NOT NULL,
    snapshot_price_delta BIGINT NOT NULL DEFAULT 0,
    quantity INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_oio_order_item FOREIGN KEY (store_id, order_item_id) REFERENCES order_items(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_oio_group FOREIGN KEY (store_id, modifier_group_id) REFERENCES modifier_groups(store_id, id) ON DELETE SET NULL (modifier_group_id),
    CONSTRAINT fk_oio_option FOREIGN KEY (store_id, modifier_option_id) REFERENCES modifier_options(store_id, id) ON DELETE SET NULL (modifier_option_id),
    CONSTRAINT fk_oio_parent_option FOREIGN KEY (store_id, parent_option_id) REFERENCES modifier_options(store_id, id) ON DELETE SET NULL (parent_option_id),
    CONSTRAINT chk_oio_price_delta CHECK (snapshot_price_delta >= 0),
    CONSTRAINT chk_oio_quantity CHECK (quantity > 0)
);

-- 9. SPLIT BILL ALLOCATIONS (Single Parent Order -> Multiple Bill Splits)
CREATE TABLE IF NOT EXISTS payment_splits (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id UUID NOT NULL,
    split_number INT NOT NULL DEFAULT 1,
    label VARCHAR(100) NOT NULL DEFAULT 'Main Bill',
    split_mode VARCHAR(30) NOT NULL DEFAULT 'full',
    subtotal_amount BIGINT NOT NULL DEFAULT 0,
    tax_amount BIGINT NOT NULL DEFAULT 0,
    service_charge_amount BIGINT NOT NULL DEFAULT 0,
    total_amount BIGINT NOT NULL,
    paid_amount BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(30) NOT NULL DEFAULT 'unpaid',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_payment_splits_store_id UNIQUE(store_id, id),
    CONSTRAINT uq_payment_splits_order_num UNIQUE(store_id, order_id, split_number),
    CONSTRAINT fk_payment_splits_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_payment_splits_mode CHECK (split_mode IN ('full', 'equal', 'by_item', 'custom_amount')),
    CONSTRAINT chk_payment_splits_status CHECK (status IN ('unpaid', 'pending', 'paid', 'cancelled')),
    CONSTRAINT chk_payment_splits_amounts CHECK (subtotal_amount >= 0 AND tax_amount >= 0 AND service_charge_amount >= 0 AND total_amount > 0 AND paid_amount >= 0)
);

CREATE TRIGGER trg_payment_splits_updated_at
BEFORE UPDATE ON payment_splits
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TABLE IF NOT EXISTS payment_split_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    split_id UUID NOT NULL,
    order_item_id UUID NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    allocated_amount BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_psi_split FOREIGN KEY (store_id, split_id) REFERENCES payment_splits(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_psi_order_item FOREIGN KEY (store_id, order_item_id) REFERENCES order_items(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_psi_quantity CHECK (quantity > 0),
    CONSTRAINT chk_psi_allocated CHECK (allocated_amount >= 0)
);

-- 10. PAYMENTS (Tenant-isolated Payment Attempts & Financial Ledger)
-- Financial history protection: fk_payments_order uses ON DELETE RESTRICT so orders with payments can NEVER be physically deleted.
CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id UUID NOT NULL,
    split_id UUID DEFAULT NULL,
    attempt_no INT NOT NULL DEFAULT 1,
    idempotency_key VARCHAR(100) NOT NULL DEFAULT '',
    provider VARCHAR(50) NOT NULL DEFAULT 'midtrans',
    provider_order_id VARCHAR(100) NOT NULL,
    amount BIGINT NOT NULL,
    method VARCHAR(50) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    raw_provider_status VARCHAR(50) NOT NULL DEFAULT '',
    fraud_status VARCHAR(50) NOT NULL DEFAULT '',
    transaction_id VARCHAR(100) DEFAULT '',
    payment_type VARCHAR(50) DEFAULT '',
    qr_string TEXT DEFAULT '',
    va_number VARCHAR(100) DEFAULT '',
    bank VARCHAR(50) DEFAULT '',
    reconciliation_ref VARCHAR(150) DEFAULT '',
    paid_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_payments_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payments_split FOREIGN KEY (store_id, split_id) REFERENCES payment_splits(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT uq_payments_provider_order UNIQUE (store_id, provider_order_id),
    CONSTRAINT chk_payments_attempt_no CHECK (attempt_no >= 1),
    CONSTRAINT chk_payments_amount CHECK (amount > 0),
    CONSTRAINT chk_payments_method CHECK (method IN ('qris', 'static_qris', 'bank_transfer', 'gopay', 'shopeepay', 'card', 'cstore', 'cash', 'edc')),
    CONSTRAINT chk_payments_status CHECK (status IN ('pending', 'authorized', 'paid', 'failed', 'expired', 'cancelled', 'partially_refunded', 'refunded', 'chargeback'))
);

CREATE TRIGGER trg_payments_updated_at
BEFORE UPDATE ON payments
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 11. PAYMENT NOTIFICATION LOGS (Durable Webhook Inbox & Idempotency)
CREATE TABLE IF NOT EXISTS payment_notification_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID,
    order_id UUID,
    provider VARCHAR(50) NOT NULL DEFAULT 'midtrans',
    transaction_id VARCHAR(150) NOT NULL,
    order_number VARCHAR(100) NOT NULL,
    status_code VARCHAR(10) NOT NULL,
    transaction_status VARCHAR(50) NOT NULL,
    gross_amount BIGINT NOT NULL,
    raw_payload JSONB NOT NULL,
    is_processed BOOLEAN NOT NULL DEFAULT false,
    processed_at TIMESTAMPTZ DEFAULT NULL,
    error_message TEXT DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_notif_logs_event UNIQUE(provider, transaction_id, transaction_status, status_code),
    CONSTRAINT fk_notif_logs_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_notif_logs_processed CHECK (
        (is_processed = false AND processed_at IS NULL) OR
        (is_processed = true AND processed_at IS NOT NULL)
    )
);

-- 12. OUTBOX EVENTS (Transactional Outbox for Reliable KDS / Realtime Delivery)
CREATE TABLE IF NOT EXISTS outbox_events (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    event_type VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(32) NOT NULL,
    aggregate_id VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ DEFAULT NULL,
    retry_count INT NOT NULL DEFAULT 0,
    last_error TEXT DEFAULT NULL
);

-- 13. AUDIT LOGS (Operational tracking with strict tenant boundary)
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
    CONSTRAINT chk_audit_logs_action CHECK (action IN ('VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE', 'SHIFT_CLOSE', 'ROLE_CHANGE', 'STATIC_QRIS_RECONCILE'))
);

-- 14. ROW-LEVEL SECURITY (RLS) ENFORCEMENT & LEAST-PRIVILEGE GRANTS
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_users ON users FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE categories ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_categories ON categories FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE products ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_products ON products FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE modifier_groups ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_modifier_groups ON modifier_groups FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE modifier_options ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_modifier_options ON modifier_options FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE product_modifier_groups ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_pmg ON product_modifier_groups FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE option_modifier_groups ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_omg ON option_modifier_groups FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE store_daily_sequences ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_sequences ON store_daily_sequences FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_orders ON orders FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE order_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_order_items ON order_items FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE order_item_options ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_order_item_options ON order_item_options FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE payment_splits ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_payment_splits ON payment_splits FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE payment_split_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_payment_split_items ON payment_split_items FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE payments ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_payments ON payments FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE outbox_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_outbox_events ON outbox_events FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation_audit_logs ON audit_logs FOR ALL USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

-- Grant DML-only privileges to mycaffe_app runtime role
GRANT SELECT ON stores TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON
    users,
    categories,
    products,
    modifier_groups,
    modifier_options,
    product_modifier_groups,
    option_modifier_groups,
    store_daily_sequences,
    orders,
    order_items,
    order_item_options,
    payment_splits,
    payment_split_items,
    payments,
    payment_notification_logs,
    outbox_events
TO mycaffe_app;
GRANT SELECT, INSERT ON audit_logs TO mycaffe_app;

-- 15. INDEXES for Multi-Tenant Isolation and High-Performance Queries
CREATE INDEX IF NOT EXISTS idx_users_store_role ON users(store_id, role);
CREATE INDEX IF NOT EXISTS idx_products_store_category ON products(store_id, category_id);
CREATE INDEX IF NOT EXISTS idx_modifier_options_store_group ON modifier_options(store_id, group_id);
CREATE INDEX IF NOT EXISTS idx_orders_store_status ON orders(store_id, status);
CREATE INDEX IF NOT EXISTS idx_orders_store_payment_status ON orders(store_id, payment_status);
CREATE INDEX IF NOT EXISTS idx_order_items_store_order ON order_items(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_order_item_options_item ON order_item_options(store_id, order_item_id);
CREATE INDEX IF NOT EXISTS idx_payment_splits_store_order ON payment_splits(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_payments_store_order ON payments(store_id, order_id);
CREATE INDEX IF NOT EXISTS idx_payments_store_split ON payments(store_id, split_id);
CREATE INDEX IF NOT EXISTS idx_payments_transaction_id ON payments(transaction_id);
CREATE INDEX IF NOT EXISTS idx_payments_provider_order ON payments(store_id, provider_order_id);
CREATE INDEX IF NOT EXISTS idx_notif_logs_lookup ON payment_notification_logs(provider, transaction_id, transaction_status);
CREATE INDEX IF NOT EXISTS idx_notif_logs_order_number ON payment_notification_logs(order_number);
CREATE INDEX IF NOT EXISTS idx_outbox_unprocessed ON outbox_events(created_at) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_audit_logs_store_created ON audit_logs(store_id, created_at DESC);
