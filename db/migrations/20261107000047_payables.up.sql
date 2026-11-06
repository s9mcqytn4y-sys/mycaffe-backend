-- 20261107000047_payables.up.sql
-- v0.11 PR 11.5 (decision X7): every goods receipt becomes a payable to its supplier, due after the supplier's
-- payment terms. It is paid in one go or in instalments from the outlet cash fund (an expense movement of
-- category ingredients, V13), by transfer or in cash. Payments are append-only.

CREATE TABLE IF NOT EXISTS payables (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    goods_receipt_id UUID NOT NULL,
    supplier_id      UUID,
    supplier_name    VARCHAR(100) NOT NULL,
    amount           BIGINT NOT NULL,
    paid_amount      BIGINT NOT NULL DEFAULT 0,
    due_date         DATE NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_payables_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_payables_receipt UNIQUE (store_id, goods_receipt_id),
    CONSTRAINT fk_payables_receipt FOREIGN KEY (store_id, goods_receipt_id) REFERENCES goods_receipts(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payables_supplier FOREIGN KEY (store_id, supplier_id) REFERENCES suppliers(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_payables_amounts CHECK (amount >= 0 AND paid_amount BETWEEN 0 AND amount)
);
CREATE INDEX IF NOT EXISTS idx_payables_open ON payables(store_id, due_date) WHERE paid_amount < amount;

CREATE TABLE IF NOT EXISTS payable_payments (
    id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id                UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    payable_id              UUID NOT NULL,
    amount                  BIGINT NOT NULL,
    method                  VARCHAR(12) NOT NULL,
    reference               VARCHAR(100) NOT NULL DEFAULT '',
    note                    VARCHAR(500) NOT NULL DEFAULT '',
    outlet_cash_movement_id UUID,
    actor_id                UUID NOT NULL,
    paid_at                 TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_payable_payments_payable FOREIGN KEY (store_id, payable_id) REFERENCES payables(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_payable_payments_actor FOREIGN KEY (store_id, actor_id) REFERENCES users(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_payable_payments_amount CHECK (amount > 0),
    CONSTRAINT chk_payable_payments_method CHECK (method IN ('outlet_cash', 'transfer', 'cash')),
    CONSTRAINT chk_payable_payments_outlet CHECK ((method = 'outlet_cash') = (outlet_cash_movement_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_payable_payments_payable ON payable_payments(store_id, payable_id);

ALTER TABLE payables ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_payables_tenant_isolation ON payables;
CREATE POLICY p_payables_tenant_isolation ON payables FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE payable_payments ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS p_payable_payments_tenant_isolation ON payable_payments;
CREATE POLICY p_payable_payments_tenant_isolation ON payable_payments FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON payables TO mycaffe_app;
GRANT SELECT, INSERT ON payable_payments TO mycaffe_app;

-- Receipts recorded before this migration (PR 11.4) become payables too.
INSERT INTO payables (store_id, goods_receipt_id, supplier_id, supplier_name, amount, due_date, created_at)
SELECT r.store_id, r.id, r.supplier_id, r.supplier_name, r.total_amount,
       (r.received_at AT TIME ZONE COALESCE(NULLIF(st.timezone, ''), 'Asia/Jakarta'))::date + COALESCE(s.payment_days, 0), r.received_at
FROM goods_receipts r JOIN stores st ON st.id = r.store_id
LEFT JOIN suppliers s ON s.store_id = r.store_id AND s.id = r.supplier_id
ON CONFLICT (store_id, goods_receipt_id) DO NOTHING;

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
        'PAYABLE_PAID'
    )
);
