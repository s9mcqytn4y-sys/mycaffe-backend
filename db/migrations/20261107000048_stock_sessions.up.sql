-- 20261107000048_stock_sessions.up.sql
-- v0.11 PR 11.6 (K2, decision X8): stock counts and waste, recorded by staff with inventory.count. A session
-- whose value (absolute count variance or waste, at current cost) is above the store limit waits for the owner
-- to approve or reject it; below the limit it posts at once. Each line keeps the balance seen when counting and
-- the delta applied when posted, so sales between counting and approval are not lost.

CREATE TABLE IF NOT EXISTS stock_sessions (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id     UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    kind         VARCHAR(5) NOT NULL,
    status       VARCHAR(10) NOT NULL,
    value_amount BIGINT NOT NULL,
    note         VARCHAR(500) NOT NULL DEFAULT '',
    created_by   UUID NOT NULL,
    decided_by   UUID,
    decision_note VARCHAR(500) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at   TIMESTAMPTZ,
    CONSTRAINT uq_stock_sessions_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_stock_sessions_creator FOREIGN KEY (store_id, created_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_stock_sessions_decider FOREIGN KEY (store_id, decided_by) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_stock_sessions_kind CHECK (kind IN ('count', 'waste')),
    CONSTRAINT chk_stock_sessions_status CHECK (status IN ('posted', 'pending', 'rejected')),
    CONSTRAINT chk_stock_sessions_value CHECK (value_amount >= 0),
    CONSTRAINT chk_stock_sessions_decided CHECK ((decided_at IS NULL) = (decided_by IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_stock_sessions_pending ON stock_sessions(store_id, created_at) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS stock_session_lines (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    stock_session_id UUID NOT NULL,
    ingredient_id    UUID,
    product_id       UUID,
    expected_quantity BIGINT NOT NULL,
    counted_quantity BIGINT,
    delta            BIGINT NOT NULL,
    unit_cost_per_1000 BIGINT NOT NULL,
    reason           VARCHAR(200) NOT NULL DEFAULT '',
    CONSTRAINT fk_stock_session_lines_session FOREIGN KEY (store_id, stock_session_id) REFERENCES stock_sessions(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_stock_session_lines_ingredient FOREIGN KEY (store_id, ingredient_id) REFERENCES ingredients(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_stock_session_lines_product FOREIGN KEY (store_id, product_id) REFERENCES products(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_stock_session_lines_item CHECK ((ingredient_id IS NULL) <> (product_id IS NULL)),
    CONSTRAINT chk_stock_session_lines_counted CHECK (counted_quantity IS NULL OR counted_quantity >= 0),
    CONSTRAINT chk_stock_session_lines_cost CHECK (unit_cost_per_1000 >= 0)
);
CREATE INDEX IF NOT EXISTS idx_stock_session_lines_session ON stock_session_lines(store_id, stock_session_id);

ALTER TABLE stock_sessions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_stock_sessions_tenant_isolation ON stock_sessions;
CREATE POLICY p_stock_sessions_tenant_isolation ON stock_sessions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE stock_session_lines ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_stock_session_lines_tenant_isolation ON stock_session_lines;
CREATE POLICY p_stock_session_lines_tenant_isolation ON stock_session_lines FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
-- A session only changes status (approve or reject); its lines never change.
GRANT SELECT, INSERT, UPDATE ON stock_sessions TO mycaffe_app;
GRANT SELECT, INSERT ON stock_session_lines TO mycaffe_app;

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
        'STOCK_SESSION_SUBMITTED', 'STOCK_SESSION_APPROVED', 'STOCK_SESSION_REJECTED'
    )
);
