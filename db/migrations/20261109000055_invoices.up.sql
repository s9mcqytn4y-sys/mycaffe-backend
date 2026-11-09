-- 20261109000055_invoices.up.sql
-- v0.11.1 PR 11.9 (X18): B2B pay later. The cashier charges an order to a business customer with the `invoice`
-- method, so the kitchen, stock and COGS run as for any paid order. The owner then bills one or more charged
-- orders on an invoice, which is paid in instalments. Invoice payments are append-only.

CREATE TABLE IF NOT EXISTS business_customers (
    id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id           UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name               VARCHAR(100) NOT NULL,
    tax_id             VARCHAR(30) NOT NULL DEFAULT '',
    email              VARCHAR(200) NOT NULL DEFAULT '',
    phone              VARCHAR(16) NOT NULL DEFAULT '',
    payment_terms_days INT NOT NULL DEFAULT 14,
    is_active          BOOLEAN NOT NULL DEFAULT true,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_business_customers_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_business_customers_name UNIQUE (store_id, name),
    CONSTRAINT chk_business_customers_terms CHECK (payment_terms_days BETWEEN 0 AND 120)
);

CREATE TABLE IF NOT EXISTS invoices (
    id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id             UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    number               INT NOT NULL,
    business_customer_id UUID NOT NULL,
    customer_name        VARCHAR(100) NOT NULL,
    tax_id               VARCHAR(30) NOT NULL DEFAULT '',
    total_amount         BIGINT NOT NULL,
    paid_amount          BIGINT NOT NULL DEFAULT 0,
    due_date             DATE NOT NULL,
    note                 VARCHAR(500) NOT NULL DEFAULT '',
    created_by           UUID NOT NULL,
    issued_at            TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_invoices_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_invoices_number UNIQUE (store_id, number),
    CONSTRAINT fk_invoices_customer FOREIGN KEY (store_id, business_customer_id) REFERENCES business_customers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_invoices_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_invoices_number CHECK (number >= 1),
    CONSTRAINT chk_invoices_amounts CHECK (total_amount > 0 AND paid_amount BETWEEN 0 AND total_amount)
);
CREATE INDEX IF NOT EXISTS idx_invoices_open ON invoices(store_id, due_date) WHERE paid_amount < total_amount;

CREATE TABLE IF NOT EXISTS invoice_payments (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id    UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    invoice_id  UUID NOT NULL,
    amount      BIGINT NOT NULL,
    method      VARCHAR(10) NOT NULL,
    reference   VARCHAR(100) NOT NULL DEFAULT '',
    note        VARCHAR(500) NOT NULL DEFAULT '',
    recorded_by UUID NOT NULL,
    paid_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_invoice_payments_invoice FOREIGN KEY (store_id, invoice_id) REFERENCES invoices(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_invoice_payments_recorder FOREIGN KEY (store_id, recorded_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_invoice_payments_amount CHECK (amount > 0),
    CONSTRAINT chk_invoice_payments_method CHECK (method IN ('transfer', 'cash', 'qris'))
);
CREATE INDEX IF NOT EXISTS idx_invoice_payments_invoice ON invoice_payments(store_id, invoice_id);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS business_customer_id UUID;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS invoice_id UUID;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_business_customer;
ALTER TABLE orders ADD CONSTRAINT fk_orders_business_customer FOREIGN KEY (store_id, business_customer_id) REFERENCES business_customers(store_id, id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS fk_orders_invoice;
ALTER TABLE orders ADD CONSTRAINT fk_orders_invoice FOREIGN KEY (store_id, invoice_id) REFERENCES invoices(store_id, id) ON DELETE RESTRICT;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_invoice_customer;
ALTER TABLE orders ADD CONSTRAINT chk_orders_invoice_customer CHECK (invoice_id IS NULL OR business_customer_id IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_orders_uninvoiced ON orders(store_id, business_customer_id) WHERE business_customer_id IS NOT NULL AND invoice_id IS NULL;

ALTER TABLE payments DROP CONSTRAINT IF EXISTS chk_payments_method;
ALTER TABLE payments ADD CONSTRAINT chk_payments_method CHECK (method IN ('qris', 'static_qris', 'bank_transfer', 'gopay', 'shopeepay', 'card', 'cstore', 'cash', 'edc', 'platform', 'invoice'));

ALTER TABLE business_customers ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_business_customers_tenant_isolation ON business_customers;
CREATE POLICY p_business_customers_tenant_isolation ON business_customers FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_invoices_tenant_isolation ON invoices;
CREATE POLICY p_invoices_tenant_isolation ON invoices FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE invoice_payments ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_invoice_payments_tenant_isolation ON invoice_payments;
CREATE POLICY p_invoice_payments_tenant_isolation ON invoice_payments FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON business_customers TO mycaffe_app;
GRANT SELECT, INSERT, UPDATE ON invoices TO mycaffe_app;
GRANT SELECT, INSERT ON invoice_payments TO mycaffe_app;

ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS chk_audit_logs_action;
ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_logs_action CHECK (
    action IN (
        'VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE', 'PRICE_CHANGE',
        'SHIFT_OPEN', 'SHIFT_CLOSE', 'SHIFT_FORCE_CLOSE',
        'ROLE_CHANGE', 'STATIC_QRIS_RECONCILE', 'CASH_PAYMENT_RECORDED', 'EDC_RECONCILE',
        'ORDER_CREATE', 'ORDER_CANCEL', 'PAYMENT_SETTLED',
        'STAFF_LOGIN_LOCKED', 'STAFF_UNLOCK', 'STAFF_PIN_RESET', 'STAFF_DEACTIVATED',
        'STAFF_SESSION_REVOKED', 'STAFF_INVITED', 'STAFF_PIN_SET', 'STAFF_TEMP_PIN_ISSUED',
        'STORE_BOOTSTRAP', 'STORE_SETTINGS_CHANGED',
        'CASH_MOVEMENT', 'DRAWER_CREATE', 'DRAWER_UPDATE',
        'TAB_OPEN', 'ROUND_SUBMIT', 'TAB_CLOSE', 'TAB_REOPEN',
        'OWNER_PIN_FAILED',
        'TABLE_CREATE', 'TABLE_UPDATE', 'TABLE_QR_ROTATE', 'TABLE_SESSION_CLOSE',
        'PRODUCT_IMAGE_CHANGED',
        'APPROVAL_REQUESTED', 'APPROVAL_APPROVED', 'APPROVAL_REJECTED',
        'STAFF_PHONE_SET', 'CUSTOMER_PHONE_VERIFIED',
        'OUTLET_CASH_MOVEMENT',
        'ORDER_DISCOUNT',
        'STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED',
        'TABLE_MOVE', 'TABLE_MERGE', 'TABLE_SPLIT',
        'TABLE_TIMER_EXTEND',
        'DISPLAY_TOKEN_CREATED', 'DISPLAY_TOKEN_REVOKED',
        'PRINTER_CREATED', 'PRINTER_UPDATED', 'PRINTER_TOKEN_ROTATED', 'PRINT_REPRINT',
        'RECEIPT_SENT',
        'DEVICE_REGISTERED', 'DEVICE_RENAMED', 'DEVICE_REVOKED', 'DEVICE_PIN_VERIFIER_ISSUED',
        'OFFLINE_ORDER_SYNCED', 'OFFLINE_ORDER_REJECTED', 'OFFLINE_ORDER_RESOLVED', 'DEVICE_PIN_FAILED',
        'PROMOTION_CREATED', 'PROMOTION_UPDATED',
        'BUNDLE_CHANGED',
        'INGREDIENT_CREATED', 'INGREDIENT_UPDATED', 'RECIPE_CHANGED',
        'SUPPLIER_CHANGED', 'PURCHASE_ORDER_CHANGED', 'GOODS_RECEIVED',
        'PAYABLE_PAID',
        'STOCK_SESSION_SUBMITTED', 'STOCK_SESSION_APPROVED', 'STOCK_SESSION_REJECTED',
        'SALES_CHANNEL_CHANGED', 'CHANNEL_PRICES_CHANGED', 'PLATFORM_PAYMENT_RECORDED',
        'STAMP_PROGRAM_CHANGED', 'STAMP_REWARD_REDEEMED',
        'BUSINESS_CUSTOMER_CHANGED', 'INVOICE_CHARGED', 'INVOICE_ISSUED', 'INVOICE_PAYMENT_RECORDED'
    )
);
