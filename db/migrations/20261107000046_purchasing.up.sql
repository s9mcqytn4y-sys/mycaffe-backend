-- 20261107000046_purchasing.up.sql
-- v0.11 PR 11.4 (K2, decisions X7, X9): suppliers, purchase orders (draft -> sent -> partial/received -> closed,
-- or cancelled), staged goods receipts and direct purchases without a PO. A line buys an ingredient or a
-- ready-to-serve product in a purchase unit with a conversion to base units (1 karton = 12000 ml); each
-- receipt adds stock and updates the moving-average cost. Payables per receipt arrive with PR 11.5.

CREATE TABLE IF NOT EXISTS suppliers (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id     UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    name         VARCHAR(100) NOT NULL,
    contact      VARCHAR(150) NOT NULL DEFAULT '',
    payment_days INT NOT NULL DEFAULT 0,
    notes        VARCHAR(500) NOT NULL DEFAULT '',
    is_active    BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_suppliers_store_id UNIQUE (store_id, id),
    CONSTRAINT chk_suppliers_name CHECK (length(btrim(name)) > 0),
    CONSTRAINT chk_suppliers_days CHECK (payment_days BETWEEN 0 AND 180)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_suppliers_store_name_active ON suppliers(store_id, lower(name)) WHERE is_active;

CREATE TABLE IF NOT EXISTS purchase_orders (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    number        INT NOT NULL,
    supplier_id   UUID NOT NULL,
    status        VARCHAR(10) NOT NULL DEFAULT 'draft',
    expected_date DATE,
    notes         VARCHAR(500) NOT NULL DEFAULT '',
    created_by    UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_purchase_orders_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_purchase_orders_number UNIQUE (store_id, number),
    CONSTRAINT fk_purchase_orders_supplier FOREIGN KEY (store_id, supplier_id) REFERENCES suppliers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_purchase_orders_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_purchase_orders_status CHECK (status IN ('draft', 'sent', 'partial', 'received', 'closed', 'cancelled')),
    CONSTRAINT chk_purchase_orders_number CHECK (number >= 1)
);
CREATE INDEX IF NOT EXISTS idx_purchase_orders_status ON purchase_orders(store_id, status, created_at);

CREATE TABLE IF NOT EXISTS purchase_order_lines (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    purchase_order_id UUID NOT NULL,
    ingredient_id     UUID,
    product_id        UUID,
    purchase_unit     VARCHAR(30) NOT NULL,
    conversion        BIGINT NOT NULL,
    quantity          BIGINT NOT NULL,
    unit_price        BIGINT NOT NULL,
    received_quantity BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT uq_purchase_order_lines_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_purchase_order_lines_po FOREIGN KEY (store_id, purchase_order_id) REFERENCES purchase_orders(store_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_purchase_order_lines_ingredient FOREIGN KEY (store_id, ingredient_id) REFERENCES ingredients(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_purchase_order_lines_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_purchase_order_lines_item CHECK ((ingredient_id IS NULL) <> (product_id IS NULL)),
    CONSTRAINT chk_purchase_order_lines_amounts CHECK (conversion BETWEEN 1 AND 1000000 AND quantity BETWEEN 1 AND 1000000
        AND unit_price BETWEEN 0 AND 100000000 AND received_quantity BETWEEN 0 AND quantity)
);

CREATE TABLE IF NOT EXISTS goods_receipts (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    purchase_order_id UUID,
    supplier_id       UUID,
    supplier_name     VARCHAR(100) NOT NULL DEFAULT '',
    total_amount      BIGINT NOT NULL,
    note              VARCHAR(500) NOT NULL DEFAULT '',
    received_by       UUID NOT NULL,
    received_at       TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_goods_receipts_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_goods_receipts_po FOREIGN KEY (store_id, purchase_order_id) REFERENCES purchase_orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_goods_receipts_supplier FOREIGN KEY (store_id, supplier_id) REFERENCES suppliers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_goods_receipts_user FOREIGN KEY (store_id, received_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_goods_receipts_total CHECK (total_amount >= 0),
    -- A PO receipt names the PO's supplier; a direct purchase names a supplier or writes its name.
    CONSTRAINT chk_goods_receipts_supplier CHECK (supplier_id IS NOT NULL OR length(btrim(supplier_name)) > 0)
);
CREATE INDEX IF NOT EXISTS idx_goods_receipts_received ON goods_receipts(store_id, received_at);

CREATE TABLE IF NOT EXISTS goods_receipt_lines (
    id                     UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id               UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    goods_receipt_id       UUID NOT NULL,
    purchase_order_line_id UUID,
    ingredient_id          UUID,
    product_id             UUID,
    purchase_unit          VARCHAR(30) NOT NULL,
    conversion             BIGINT NOT NULL,
    quantity               BIGINT NOT NULL,
    unit_price             BIGINT NOT NULL,
    line_total             BIGINT NOT NULL,
    CONSTRAINT fk_goods_receipt_lines_receipt FOREIGN KEY (store_id, goods_receipt_id) REFERENCES goods_receipts(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_goods_receipt_lines_po_line FOREIGN KEY (store_id, purchase_order_line_id) REFERENCES purchase_order_lines(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_goods_receipt_lines_ingredient FOREIGN KEY (store_id, ingredient_id) REFERENCES ingredients(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_goods_receipt_lines_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_goods_receipt_lines_item CHECK ((ingredient_id IS NULL) <> (product_id IS NULL)),
    CONSTRAINT chk_goods_receipt_lines_amounts CHECK (conversion BETWEEN 1 AND 1000000 AND quantity BETWEEN 1 AND 1000000
        AND unit_price BETWEEN 0 AND 100000000 AND line_total = quantity * unit_price)
);

ALTER TABLE suppliers ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_suppliers_tenant_isolation ON suppliers;
CREATE POLICY p_suppliers_tenant_isolation ON suppliers FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE purchase_orders ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_purchase_orders_tenant_isolation ON purchase_orders;
CREATE POLICY p_purchase_orders_tenant_isolation ON purchase_orders FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE purchase_order_lines ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_purchase_order_lines_tenant_isolation ON purchase_order_lines;
CREATE POLICY p_purchase_order_lines_tenant_isolation ON purchase_order_lines FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE goods_receipts ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_goods_receipts_tenant_isolation ON goods_receipts;
CREATE POLICY p_goods_receipts_tenant_isolation ON goods_receipts FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE goods_receipt_lines ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_goods_receipt_lines_tenant_isolation ON goods_receipt_lines;
CREATE POLICY p_goods_receipt_lines_tenant_isolation ON goods_receipt_lines FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());

GRANT SELECT, INSERT, UPDATE ON suppliers, purchase_orders TO mycaffe_app;
-- Draft PO lines are replaced as a whole.
GRANT SELECT, INSERT, UPDATE, DELETE ON purchase_order_lines TO mycaffe_app;
-- Receipts are append-only: stock and cost already moved.
GRANT SELECT, INSERT ON goods_receipts, goods_receipt_lines TO mycaffe_app;

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
        'SUPPLIER_CHANGED', 'PURCHASE_ORDER_CHANGED', 'GOODS_RECEIVED'
    )
);
